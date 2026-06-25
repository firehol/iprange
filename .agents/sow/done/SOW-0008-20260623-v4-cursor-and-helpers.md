# SOW-0008 - iprange v4 cursor API + standard SDK helpers

## Status

Status: completed

Sub-state: First of the v4.1 additions — a read-only cursor API + standard SDK helpers on the
locked v4 core (**no on-disk format change**). Delivered in **Rust + Go** with shared
conformance; committed `69c607f`. External review round-1: **4/4 capable reviewers PRODUCTION
GRADE** (zero P0/P1). Review fixes (jointly with SOW-0009) committed `ff5f783` and re-verified.
The post-fix re-review panel was **infra-blocked** (nova/litellm saturated — tracked in
Followup). **Completed 2026-06-25.**

## Requirements

### Purpose

Give the v4 core an ordered **cursor** (`seek`/`next`/`prev`/read) with a defined
mutate-during-iteration contract, plus a small **standard SDK-helper** set (selectors,
coalescing scan, query/count), consistent and conformance-tested across the language
implementations. This is the mechanism that lets external callers (update-ipsets, Netdata)
build retention, multi-feed comparison, and geo lookups themselves — without embedding any
of that policy in iprange and without each caller re-implementing the same traversals.
iprange stays a generic interval-map engine over `[from,to,scope]`; it contains **zero**
retention/comparison/geo logic, and `scope` stays opaque to it.

### Design decisions (resolved 2026-06-23)

1. **Mechanism vs policy.** iprange ships the cursor + helpers; retention, comparison, and
   geo are **caller modules** built on top, out of iprange.
2. **Cursor.** `seek(key)`, `next()`, `prev()`, read-at-cursor (range + scope), with the
   **mutate-during-open-cursor contract**: the cursor binds to the committed root and reads
   only committed pages; `set`/`delete` build new pages from the free set, and D7
   (reclaim-after-commit) keeps those committed pages alive for the whole txn — so the view
   is a stable snapshot. **`commit` invalidates open cursors** (re-`seek`; using one across a
   commit is a `State` error). This lets a caller walk the *old* tree to compute a delta
   while writing the *new* one. (Full detail: spec §v4.1.A.)
3. **Standard helper set** (shipped): **selectors = caller predicates `match(scope)->bool`
   over the opaque scope** (the engine never interprets scope); coalescing scan;
   `queryRanges`, `queryRangesMerged`, `queryCIDRs`, `queryCIDRsMerged`; `countIPs`,
   `countCIDRs`. Visitor returns `Continue`/`Stop` (+ error); CIDR output is the canonical
   minimal cover (largest-aligned-prefix) for cross-language identity. (Full detail: spec
   §v4.1.B.)
4. **Counters are on-demand** (cursor scan), **not** maintained format state — this is why
   there is no on-disk counters region and no format change here. A caller needing instant
   counts maintains its own (it already computes its deltas).
5. **Languages.** Rust + Go now (C via the Rust library), shared conformance — matching the
   v4 core.
6. **`prev()` is included.** Callers need only forward traversal today, but `prev` completes
   the cursor contract (reverse scans, predecessor queries) at small cost.

### Assistant Understanding

- The cursor is the single enabling primitive: with `seek`/`next`/`prev` + `set`/`delete`
  + read-scope, every higher module is caller-buildable —
  - **retention** = a caller loop over a new snapshot + a B-cursor, `set`/`delete` at delta
    points (`O(delta)` COW writes), with caller-owned companion ledgers (its output
    contract is captured in `update-ipsets-v4-adoption-findings.md` — **caller reference,
    not iprange work**);
  - **comparison** = one cursor scan reading per-record scope membership;
  - **geo** = `lookup`/scan over a categorical-scope file.
- The cursor + helpers are **purely additive** to the v4 core: no on-disk byte-format
  change. The cursor is a traversal construct — a root→leaf path stack; D3 ("no leaf
  sibling pointers") means `next`/`prev` advance within a leaf and re-descend at leaf
  boundaries. No v4 minor/major bump.

## Pre-Implementation Gate

### Problem / root-cause model

Embedding higher-level modules (retention/comparison/geo) in the file format would bind the
format to one consumer's policy and bloat it. The only thing missing for callers to build
those modules themselves is an ordered cursor with safe mutate-during-iteration. The
5-agent update-ipsets investigation confirmed every heavy flow (retention merge, N²
comparison, geo attribution) reduces to a cursor scan + `set`/`delete` over a multi-scope
interval map.

### Evidence reviewed

- `firehol/iprange` specs: `design-iprange-v4-livedb.md` (locked core — reader scan,
  writer set/delete/commit, COW, derived allocator, D3 no sibling pointers),
  `design-iprange-v4-scope-api.md` (§v4.1.A/B), `update-ipsets-v4-adoption-findings.md`.
- `firehol/update-ipsets` (Go) — 5-agent read-only investigation (cursor-buildable
  modules; retention output contract is caller-side).

### Affected contracts and surfaces

- **New (additive):** v4 core cursor API (`seek`/`next`/`prev`/read) + the
  mutate-during-open contract; the standard SDK-helper set. Rust + Go, shared conformance.
- **No change:** the on-disk `.iprdb` byte format; existing `set`/`delete`/`lookup`/`commit`.
- **Out of iprange (caller modules):** retention, comparison, geo.

### Existing patterns to reuse

v4 reader scan + `validate_node` (the cursor is a stateful form of the existing
descent/scan); writer COW + commit (the pending-tree side of the mutate-during-iteration
contract); the v3/v4 shared conformance harness (extend for cursor + helper goldens); the
bounds-safety/fuzz discipline from SOW-0007 (cursor must never OOB on hostile input).

### Risk and blast radius

- **Mutate-during-iteration correctness** — the cursor must read a stable snapshot while
  COW mutations proceed; the central thing to get right and test.
- **Cross-language helper consistency** — `countIPs`/`queryMerged`/CIDR counts must be
  value-identical across Rust + Go (conformance).
- `prev()` without sibling pointers (D3) = cursor-stack re-descent; correctness at leaf
  boundaries and tree edges.
- Overall blast radius is low: additive API, no format change, no behavior change to
  existing ops.

### Sensitive data handling plan

None — generic engine API; no secrets, no FireHOL infra details.

### Implementation plan (ordered)

1. **Read cursor** (Rust + Go): `seek`, `next`, `prev`, read-at-cursor; path-stack,
   D3-aware boundary handling; bounds-safe on hostile files.
2. **Mutate-during-open-cursor contract**: cursor reads the committed snapshot; `set`/
   `delete` build the pending COW tree; define and test view stability until commit.
3. **Standard SDK helpers** (on the cursor): selectors; coalescing open-run scan;
   `queryRanges`/`queryRangesMerged`; `queryCIDRs`/`queryCIDRsMerged`; `countIPs`;
   `countCIDRs`. Streaming/visitor, zero-alloc.
4. **Shared conformance** for cursor + helpers (cross-language goldens).

### Validation plan

- Cursor conformance: seek/next/prev correctness; empty tree, single record, full-family
  span, leaf boundaries, before-first/after-last; cross-language identical sequences.
- Mutate-during-iteration: walk + interleaved `set`/`delete`; assert the cursor's view is
  the pre-commit snapshot and results are correct.
- Helper goldens: `countIPs`, `queryRangesMerged`, `queryCIDRs(Merged)`, `countCIDRs` —
  value-identical Rust ↔ Go on the shared corpus.
- Fuzz: cursor + helpers never panic/OOB on malformed `.iprdb` (extends SOW-0007
  robustness).

### Artifact impact plan

- `design-iprange-v4-scope-api.md` — already specifies the cursor + helpers (§v4.1.A/B).
- `AGENTS.md` (iprange) — add cursor/helper build+test commands when code lands.
- Retention output-contract findings — kept as **caller reference** (for update-ipsets),
  explicitly out of iprange scope.

### Open decisions

None — all resolved (see Design decisions). Remaining choices are implementation-defined
(M-class): the cursor-stack representation and the visitor signature shape.

## Validation

- **Acceptance criteria** — met: ordered cursor (`seek`/`first`/`last`/`next`/`prev`/`current`)
  + standard SDK helpers (`query_ranges[_merged]`, `query_cidrs[_merged]`, `count_ips`,
  `count_cidrs`), selectors as caller predicates over opaque scope, canonical minimal-cover
  CIDR, in **Rust + Go** with shared conformance. No on-disk format change.
- **Tests (re-verified by the assistant, not trusting the implementer)** — Rust: lib 85 / 97
  (`--features export-v3`), conformance + metadata-conformance + robustness/fuzz green; clippy
  `--all-targets --all-features -D warnings` clean; `fmt --check` clean; `no_std` and
  `no_std+alloc` builds clean. Go: `test ./...`, `vet`, `gofmt -l` clean; `test -race -count=1`
  clean (421s). Cursor + helper goldens value-identical Rust↔Go.
- **Reviewer findings** — shipped and reviewed jointly with SOW-0009 (commit `69c607f`; fixes
  `ff5f783`). Round-1 external panel: **4/4 capable reviewers PRODUCTION GRADE, zero P0/P1**
  (mimo, deepseek, glm, qwen; minimax produced no usable output). No cursor/helper-specific
  finding; acted-on findings were in the metadata/KV/writer code (see SOW-0009 Validation).
  Post-fix re-review: **infra-blocked** (all 6 reviewers timed out — nova/litellm saturated);
  tracked in Followup. The PRODUCTION-GRADE verdict rests on round-1 + the re-verification above.
- **Same-failure search** — the parity nit (`scope::find` descent-loop bound) was fixed
  symmetrically in Rust + Go.
- **Sensitive data gate:** clean — synthetic ranges/fixtures only; no secrets, credentials,
  customer data, or FireHOL infra details in any durable artifact.
- **Artifact maintenance gate** — `design-iprange-v4-scope-api.md` §A/§B specify the cursor +
  helpers; `AGENTS.md` v4 build/test commands already cover this crate (cursor/helpers live in
  the same module — no new command needed). No end-user docs (pre-1.0 library consumed via the
  SDK); no project skills affected.
- **Status/dir consistency:** `Status: completed` in `done/`.

## Outcome

Delivered the v4 ordered **cursor** + **standard SDK helpers** in Rust + Go — the mechanism
that lets callers build retention, multi-feed comparison, and geo modules externally, with no
retention/comparison/geo policy in iprange and `scope` kept opaque. Purely additive to the
locked v4 core (no byte-format change). PRODUCTION GRADE per round-1 + assistant re-verification.

## Lessons Extracted

- A parallel implementation session can race ahead of the design discussion; verify the
  filesystem/git state before acting, to avoid clobbering uncommitted parallel work.
- The cursor's mutate-during-open contract (commit invalidates open cursors; reads bind to the
  committed root) is the single primitive that keeps higher-level modules out of the format.

## Followup

- **Re-run the post-fix external review panel** when the nova/litellm backend recovers (round-1
  was clean; the fix re-review timed out on a saturated backend). Joint with SOW-0009.
- Caller-side modules (retention, comparison, geo) are downstream consumer work (separate
  repos/SOWs), built on this cursor + helpers.
