# SOW-0026 - Gatescan performance and typed ownership analyzer follow-up

## Status

Status: open

Sub-state: Tracked follow-up for the v4/go-gate scanner cost accepted by
SOW-0025 decision 2026-08-19 (Measured gate reality) and recorded by the
M3 chunk-1 review round (Leibniz P2-2/P2-3). Not M3 scope; the routine
per-chunk gate keeps using the current scanner at its measured cost.

## Requirements

### Purpose

Bring the typed gatescan (v4/go-gate) back to a cost where the durable
battery and per-chunk scans are routinely usable, and replace the
summary-flag taint model with a type-aware ownership analyzer that
closes the remaining launder shapes (generic type-parameter erasure is
already patched in M3 chunk 1; channel laundering and interface-only
delivery remain summary-blind by design).

### User Request

SOW-0025's boundary decision (2026-08-19, resolved with the user)
recorded: "The gate's allocation/GC cost is a tracked follow-up (typed
ownership analyzer / allocation reduction), not M3 scope." The M3
chunk-1 adversarial review (Leibniz, 2026-08-19) required this item to
be represented by a real pending SOW so SOW-0025 can close under the
Followup Discipline.

### Assistant Understanding

Facts:

- One single-config scan of the grown module measures ~6-12 min
  (SOW-0025 decision record; analyzer GC-bound, profile ~55% runtime
  marking).
- The M2-era scan completed in seconds; the cost jump correlates with
  the scanner rewrite (per-OS-config loader + parallel config scans
  sharing one *token.FileSet, page-flow allocation volume).
- The tool owns the durable battery (714 forms after M3 chunk-1):
  --self-test/--self-test-jobs; the routine check per chunk stays
  "one real-module linux scan (rc=0) plus dynamic mmap-only evidence".
- Known summary-model blind spots recorded by the M3 chunk-1 review:
  generic type-param method calls (patched in chunk 1 via receiver
  provenance propagation), channel sends (no channel tracking), and
  interface-method delivery inside the holder set.

Inferences:

- A typed ownership analyzer (results carry an Ownership enum instead of
  boolean mapped flags) can drop most allocation and fix the remaining
  blind spots without losing fail-closed behavior.

Unknowns:

- Exact allocation profile after the loader refactor; whether the shared
  FileSet is a correctness race under parallel config scans (go/token
  does not document AddFile as thread-safe).

### Acceptance Criteria

- One linux scan of the real module completes in a cost usable as a
  routine per-chunk check (target: well under 5 min, ideally seconds),
  measured and recorded.
- The durable battery and the boundary corpus complete on request
  without changing their expectations (711+ forms stay green; new
  launder shapes keep failing closed).
- Channel-launder and interface-delivery shapes are either analyzed
  (typed ownership) or explicitly documented as tripwire limits.
- All runs at nice.

## Analysis

Sources checked:

- v4/go-gate/main.go (scanConfig, FileSet, per-config loader)
- v4/go-gate/pageflow.go (funcSummary, maxSrcOf, evalExpr)
- v4/go-gate/memory stats recorded in SOW-0025 decision 2026-08-19
- .agents/sow/current/SOW-0025-20260811-pure-go-exact-v4-port.md

Current state:

- Scanner cost accepted at ~6-12 min/linux scan; boundary corpus and
  battery archived; per-chunk routine = one scan + dynamic evidence.
- M3 chunk-1 patched the generic-erasure shape (receiver/argument
  provenance propagation on unproven callees) and the root-boundary
  export rule; B5-B7 pin both in the battery.

Risks:

- Rewriting the analyzer while the SDK grows risks gate regressions;
  the durable battery is the regression net.

## Pre-Implementation Gate

Status: blocked

Problem / root-cause model:

- The scanner's per-package summary pass allocates heavily (GC-bound);
  parallel config scans share a token.FileSet; the taint model keeps
  per-record maxSrc lists for every statement.

Evidence reviewed:

- SOW-0025 Measured gate reality (2026-08-19)
- M3 chunk-1 review round (Leibniz P2-1/P2-2/P2-3, Sartre
  P2-1/P2-2/P3-2)

Affected contracts and surfaces:

- v4/go-gate (tooling only; production code paths must not change:
  performance is a hard constraint of the SOW-0025 decision)

Existing patterns to reuse:

- The durable battery + boundary corpus as the regression net; the
  existing loader import cache; per-config scanning.

Risk and blast radius:

- Tooling only. The routine gate remains the current scanner until the
  replacement is battery-clean at nice.

Sensitive data handling plan:

- None: static analysis of a public module, no secrets.

Implementation plan:

1. Profile allocation sites; refactor the loader (per-config isolated
   FileSet; single-pass summaries; drop per-record maxSrc retention).
2. Add the typed Ownership-return model for results/fields; keep the
   existing checkViewHolderExports and battery expectations.
3. Re-measure and record; re-run battery + boundary at nice.

Validation plan:

- --self-test-jobs on the full battery, boundary corpus, real-module
  linux scan rc=0, all at nice; compare expectations unchanged.

Artifact impact plan:

- SOW-0026 close record; SOW-0025 decision record updated at close.

Open decisions:

- None blocking; the chunk-5 M3 gate may re-schedule this work.

## Follow-up Discipline

This SOW exists to satisfy SOW-0025's tracked-follow-up requirement.

### 2026-08-19 - additional defect: generic type-parameter helper termination (found in the M3 chunk-1 battery verification)

The first B5 battery form ("generic type-param helper laundering a minted
page") did not terminate: a generic function whose type-parameter receiver
calls an interface-bound method (Peek5[T pager5](m T) { return m.Page(0) })
drives the summary leaf-walk through fresh instantiated type identities
(types.Instantiate results are not cached), so paramLeafPathsSeen never
finds a seen-struct repeat and the walk nests without bound (measured:
depth > 120, 10.7M diagnostic hits in 40 s at depth>120, never
terminating). The type-parameter form is therefore not a usable battery
case until the analyzer caches instantiated summaries or keys the walk
cycle-set on type ORIGINS. The M3 chunk-1 round rewrote B5 to the
interface-receiver form (PeekC(m pager5)) exercising the same
unproven-callee provenance fix; the type-parameter form stays a tracked
defect here.

Second proven trigger of the same walk defect (2026-08-20, boundary
corpus run): the durable battery case P49 (channel round trip: ch <-
page; p := <-ch; append(owned, p...)) drives paramLeafPathsSeen into
the same non-terminating recursion (SIGQUIT dump: goroutine in
pageflow.go:11022/11049/11077, container/key element recursion with
fresh struct identities; the scanRoot main goroutine blocked on the
config-results channel after 73+ min). The walk and its callers are
byte-identical to HEAD 2f2a975, so the defect predates M3 chunk 1 (the
production scan passes because no production shape reaches the
divergent graph). The channel case is excluded from the routine
boundary corpus until the walk is fixed (origin-keyed seen-set,
instantiation-cached element types, or a typed ownership analyzer);
it remains in the archived full battery as the regression pin for the
fix. The 2026-08-19 record's diagnosis (generic instantiations) is
therefore one instance of a broader defect: the seen-set keys on type
identity, which container-element and key derivation can defeat just
like generics do.

Related gap recorded from the 2026-08-19 boundary re-verification, and
its resolution: the interface-receiver helper now summarizes mapped -
the module-interface miss path carries the mapped flag when the erased
receiver could be the mapping owner (types.Implements check,
couldBeMappingOwner in pageflow.go), so the view-holder export rule
fires on the launder shape (battery B5). Interfaces the mapping cannot
implement (tree.Codec, external error/Stringer/io.Reader) keep
tainted-only results so bounded record copies stay legal. Remaining
work for the typed analyzer: the generic type-parameter form still
does not terminate (fresh instantiated identities defeat the walk
seen-set), and summaries are still computed with untainted parameters
for external-interface receivers, which stays safe only because the
type-erasure launder rule polices mapped arguments at call sites.

Resolution (2026-08-20, M3 chunk-1 final re-run): the walk termination
items in this section are FIXED. paramLeafPathsSeen was rewritten as a
per-struct memoized walk keyed on the dereferenced *types.Struct, with
an in-progress nil marker that stops revisiting structs on cycles and a
fail-closed leafWalkBudget (1<<20) that panics leafWalkDivergence on
exhaustion; scanRoot recovers the panic per OS config and fails that
config closed. The generic type-parameter form that originally diverged
is now routine battery case B8 (it terminates and fires the whitelist
rule), and P49 (channel round trip) is restored to the routine boundary
corpus and passes in ~20 s. The typed analyzer's remaining
conservatism is unchanged: summaries are still computed with untainted
parameters for external-interface receivers, and cycle detection keys
on type identity - future walks that regenerate identities must reuse
this memo pattern.
