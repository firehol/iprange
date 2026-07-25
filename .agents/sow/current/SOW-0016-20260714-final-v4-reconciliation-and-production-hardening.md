# SOW-0016 - v4 Core SDK Reconciliation and Production Hardening

## Status

Status: in-progress

Sub-state: all unsigned Phase-1 product decisions are resolved. The exact
wire/static foundation, shared allocator/late-binding lifecycle, and scoped
retirement/selective-finalization work are implemented and accepted in Go and
Rust. Go fixed-point coordinator chunks 1-3 are accepted. Rust chunks 1-3 have
subsequently been changed by the unfinished sparse aggregate chunks 4-6 and must
be reviewed again as one whole. The aggregate work is active: the current Rust
no-default and all-features matrices compile and pass, but its production
lifecycle and canonical-record finalization are still incomplete. Transaction
durability, the remaining fixed-point coordinator work, feed catalog/membership,
metadata, normalization/workflows, recovery, snapshots, public SDKs, and the
Rust-provided C ABI remain pending. Snapshot signing remains wholly in pending
SOW-0017 and cannot block the core SDK. The prior adversarial audit and its red
tests remain the evidence baseline.

2026-07-24 restart execution: the Rust coordinator now uses three
caller-provided fixed journals of prebound `Cell` writes instead of heap
`Vec`s; it resets only their used prefixes. Generic checkpoint cleanup now
clears all stale index/scope scratch, and scope aggregates exclude the
deliberately-changing stale-authority epoch while refreshing after authority and
transfer changes. Both Rust test matrices are green: 419 no-default tests and
511 all-feature tests. Scoped checkpoint header rollback was inspected rather
than changed: scoped checkpoints intentionally reject scope-header mutation,
while generic rollback already restores it.

The first restart chunk completed decision 69A literally: the two permanent
`sparse_replay_*` fields were removed from every private-pool slot and replaced
with a caller-owned, generation-stamped slot-to-overlay index. Preparation now
mutates only detached scratch and exact after-images; cancellation/drop and
replay clear only touched sidecar entries. Permanent 4,096-slot coverage proves
undersized sidecars reject before mutation, cancellation restores the complete
live pool state, and both scratch classes are reset after cancellation and
replay. This is a direct implementation of the selected bounded-memory
contract, not a new product decision.

The remaining coordinator sequence is now concrete: first move the fixed
journals and overlay sidecar into one opaque, transaction-budgeted workspace
instead of the current test-local construction; then connect the typed bitmap
and retirement export to the real writer lifecycle, so production—not a test
only call graph—constructs and consumes the aggregate. Only after those two
steps can chunks 4-6 be re-audited as a whole for producer authority, exact
cleanup ownership, persistent records, and Active-suffix guarantees.

2026-07-24 workspace milestone: Rust now has one private caller-backed
coordinator workspace containing stable ledger slots, all three fixed
`Cell`-write journals, prior-return/new-location scratch, and the detached
sparse-overlay sidecar. The core charges its complete retained capacity before
preparation, binds it to one workspace identity, rejects a substituted workspace
at execute, and refuses to discard the transaction until explicit cancellation
returns the reservation. Stable records retain only a pool-free prepared image;
later provenance checks receive the live pool explicitly. This removes the
otherwise self-referential draft borrow that Rust correctly rejected during
cancellation/abort.

The production-shaped permanent integration test now proves exact workspace
budget acceptance and one-byte-short rejection, atomic restoration after an
invalid prior return, allocation-free Active replay, abort rejection while the
reservation is live, and cancellation followed by a successful whole-draft
abort. This stage deliberately keeps commit rejected after a sealed aggregate:
pages remain in the coordinator scope until the later canonical-record
finalization stage transfers or cleans that authority. A commit-success claim
before that stage would be false. The next work remains the real writer
lifecycle and final-record handoff, not a public API claim.

2026-07-24 canonical-record correction: the retained record now covers every
sealed terminal page, including retirement pages, rather than only the bitmap
subset. The private input-finish boundary consumes the final successor without
releasing those records, so commit remains blocked until the pending output-drain
and exact scope-cleanup stage. Full Rust/Go validation is green for this internal
milestone; durable private-file output and public SDK behavior remain pending.

2026-07-24 private-output cleanup milestone: Rust now drains every retained
private page through a bounded callback before cleaning its exact sealed scope.
The coordinator's pending-cleanup count is released only after scope closure,
so the internal commit fence can pass after a complete drain. A sink failure
makes the draft abort-required and preserves the normal explicit
cancel-and-abort route. This is still not durable file output or a public SDK;
the OS writer and target-meta publication remain pending.

2026-07-24 commit-preparation milestone: Rust now freezes the final private
transaction state before its page-drain callback can run. The callback requires
an exact preparation capability, and a successful drain can return only an
internal target-metadata authorization after workspace release and a final
preflight. No file bytes, metadata page, sync, writer-lease transition, or
durable transaction result exists yet; those remain the next Linux writer work.

2026-07-24 live-reclamation boundary plan: the next Rust slice replaces the
retirement reader's loose caller-supplied reader threshold with a
borrow-bound reclaim fence made from the Linux live operation barrier's stable
reader-table scan. Selection, verification, and the second pass retain that
fence, so they cannot run after the operation barrier is released. The same
fence also supplies the future normal-commit decision between direct free pages
and a reader-protected retirement batch. This slice adds no file write,
metadata publication, or public SDK behavior.

2026-07-24 live-reclamation fence milestone: implemented that boundary in Rust.
The retirement selection API no longer accepts a loose threshold. A
registration blocks all reclamation; otherwise a batch is eligible only when
the oldest pinned reader is at or after its `retired_by_txn`. The Linux barrier
returns the borrow-bound fence rather than raw reader facts, and verification
and both second-pass checks retain it. Focused no-default retirement and
all-feature Linux lifecycle tests pass. The full Rust matrices pass with 420
no-default and 521 all-feature tests; formatting, benchmark compilation,
warnings-denied Clippy, Go test/vet, SOW audit, and whitespace checks also pass.
Real allocation/finalization and file publication remain separate pending work.

2026-07-24 trusted-reclamation handoff milestone: Rust now exposes reclaimed
pages to the bitmap allocator only through an opaque result produced by the
verified retirement second pass into caller-owned bounded scratch. The first
and second passes reject duplicate or descending pages across batch boundaries,
not only inside one blob. The raw page-slice completion helper is test-only;
normal builds cannot claim arbitrary pages as safely reclaimed. This is not yet
the end-to-end operation-barrier lifetime proof: no live physical commit calls
the allocator/finalizer yet, so the following physical-commit slice must keep
the Linux operation barrier held through finalization, page output, metadata
publication, and the lease update.

2026-07-25 architecture reset: the user rejected the size, structure, and
indirection of the current implementation. Passing internal tests does not make
an implementation suitable when it has no usable public SDK and its design has
grown far beyond the format's intended simplicity. The existing implementation
is evidence only; unfinished work is not an accepted milestone.

The replacement follows the engineering philosophy in `AGENTS.md`: every
mechanism must trace to an approved requirement and concrete failure; production
code aims for roughly 5,000 lines per language, files aim for roughly 500 lines,
and functions normally have one purpose. These are review triggers, not
mechanical limits. Larger code is acceptable only when it is the simplest clear
implementation.

Implementation order is now binding: finish and prove Rust first, benchmark
realistic update-ipsets workflows, and demonstrate that the Rust SDK is a major
architectural improvement for update-ipsets. The pure-Go port must not begin
until the user accepts the Rust result. The interrupted uncommitted normal-range
workspace is preserved but is not a valid basis for continued implementation.

### 2026-07-25 - replacement pre-implementation gate

#### Problem and root cause

The current Rust crate has 60,827 production lines after test-only modules are
excluded, 47 source files, several production files above 10,000 lines, and no
usable file reader or writer in its public API. The normative binary-format
specification is 4,476 lines. The implementation grew by encoding each
intermediate ownership and cleanup proof as a separate mechanism instead of
making those states impossible in the format and write order.

The replacement is a clean implementation. Existing code and tests are evidence
for required behavior and past failures; they are not architecture to preserve.
The interrupted normal-range workspace remains untouched until the old
implementation can be removed without losing uncommitted work.

#### Evidence reviewed

- User decisions 4-68 in this SOW, with signing and general multi-file algebra
  remaining in SOW-0017 and SOW-0018.
- `.agents/sow/specs/binary-format-v4.md`,
  `.agents/sow/specs/design-iprange-engine.md`, and
  `.agents/sow/specs/update-ipsets-v4-adoption-findings.md`.
- Current Rust public surface in `v4/rust/iprange-livedb/src/lib.rs`; only value,
  key, error, and cardinality types are public.
- Current production-size and complexity evidence recorded in this SOW and the
  architecture-reset status above.
- `firehol/update-ipsets @ e593366f7b0a`: roughly 421 configured plain,
  retention, and merged feeds; feeds are processed independently and current
  retention is an I/O-heavy cohort workflow.
- `LMDB/lmdb @ 389e1009a86c`:
  `libraries/liblmdb/mdb.c:872-960` uses an external reader table,
  `libraries/liblmdb/mdb.c:1361-1390` uses committed meta pages, and
  `libraries/liblmdb/mdb.c:2695-3070` performs COW page allocation/touch.
  Its large in-memory dirty/free page lists are not suitable for this format's
  file-size-independent heap requirement.
- `cberner/redb @ fe0141159c73`:
  `src/tree_store/btree_mutator.rs:460-925` and
  `src/tree_store/page_store/page_manager.rs:192-224` confirm that generic COW
  trees and immediate reuse of uncommitted pages are proven patterns. Redb's
  table types, savepoints, caches, durability modes, and generic value system
  are explicitly out of scope.

#### Minimal architecture

- One generic slotted-page COW B+tree implementation serves the range map, both
  catalog indexes, both membership indexes, transaction deltas, and retirement
  batches. Tree identifiers and codecs enforce each root's key/value contract.
- A zero root is the only empty tree. Reachable non-root leaves and branches are
  never empty. Deletion removes an empty child; sparse nonempty pages are legal
  and compact snapshots repack them. This removes empty-subtree traversal and
  rebalancing machinery from normal reads and writes.
- A writer changes a committed page by copying it once. The parent immediately
  points to that transaction-private page, which can then be updated in place.
  Therefore no file-sized old-page-to-new-page map is required.
- Unordered ranges are applied directly, in arrival order, to the
  transaction-private range tree. Ordered rebuild paths stream directly into
  final COW pages. No normal workflow creates a sorting or spill file.
- Retired committed page numbers stream into same-file transaction-private batch
  pages. The retirement tree is updated last, so pages replaced while updating
  that tree append to the still-open batch without a recursive fixed-point
  planner.
- The persistent hierarchical free bitmap remains the allocation authority. A
  small fixed array of page numbers in the meta page is reserved solely for
  copying allocator paths. This breaks allocator self-reference without a large
  heap workspace. Unused reserve pages carry forward; exhaustion falls back to
  aligned tail growth.
- Abort discards private roots. Reused free pages remain free in the committed
  bitmap, and aligned tail growth is truncated immediately or by the next writer
  open. No cleanup ledger or later commit can publish a failed prefix.
- Commit has only two durable phases: synchronize all private pages, then write
  and synchronize the alternate meta page. Failure before meta writing is
  `NotCommitted`; failure after it begins is `OutcomeUnknown`. There is no
  fallible post-publication phase.
- Live coordination uses one fixed sidecar. A reader holds an OS byte-range lock
  on its slot for its lifetime. Registration uses a shared gate lock; commit
  uses the exclusive gate while scanning stable slot transaction IDs and
  publishing the meta. Process death releases locks automatically, eliminating
  PID/start-token reaping and stale-slot state transitions.
- Metadata remains one optional zlib-compressed payload, at most 1 MiB before
  compression, replaced as a whole in private COW pages.
- Default open and normal operations perform only bounds and arithmetic checks
  needed for safe access. Full page CRC and ownership checks remain explicit
  validation/recovery work.

#### Separation and size review

The new Rust implementation begins in an isolated crate so the interrupted old
tree is not modified. Its intended production modules are:

1. public types and errors;
2. exact meta/page codecs;
3. positional file access and page ownership;
4. one generic B+tree;
5. range semantics;
6. allocation and retirement;
7. feed catalog and memberships;
8. live reader coordination;
9. public readers/writers and exact workflows;
10. explicit validation, recovery, and snapshots.

The initial production-code target is roughly 5,000 lines. Exceeding it is not
a failure, but every material increase must identify the requirement and
concrete omitted failure that justify it. Files aim for roughly 500 lines and
functions normally have one purpose under the same review philosophy.

#### Risk and blast radius

- This replaces all unreleased v4 bytes and all current Rust implementation
  internals. No compatibility reader or migration is allowed.
- The highest risks are COW split/delete correctness, crash ordering, allocator
  self-reference, reader-generation reclamation, full-space IPv6 arithmetic,
  membership refcounts, source failure, and accidental implicit validation.
- The old Rust and Go implementations remain isolated until replacement
  behavior is proven. They must not be treated as fallback production SDKs.
- No production or customer data is needed. Tests and benchmarks use synthetic
  addresses, feed names, paths, metadata, and injected I/O failures.

#### Implementation and validation order

1. Exact public types, wire codec, empty create/open, and immutable reader.
2. Generic COW tree plus direct lookup, cursor, sequential assignment, abort,
   and durable commit.
3. Free bitmap, streamed retirement, live sidecar, reclamation, and crash tests.
4. Opaque JSON metadata.
5. Feed catalog, SDK-owned membership interning, and advanced membership
   operations.
6. Named-feed replacement, direct replacement, retention refresh, import, and
   snapshot workflows.
7. Explicit validation and recovery.
8. Fault injection at every durable boundary; corruption, same-failure,
   allocation, descriptor, RSS, and scaling checks.
9. update-ipsets-shaped benchmarks and integration proof.

Every slice must be reachable through the public Rust API, keep warnings denied,
and pass formatting, unit, integration, and SOW checks. No Go implementation
work begins in this sequence.

#### Artifact impact and open decisions

- `AGENTS.md` now contains the permanent philosophy and Rust-first gate.
- The architecture and exact binary-format specifications must be replaced by
  the concise proven contract as the new bytes land; the C ABI remains later in
  the Rust sequence and must be a thin adapter.
- End-user SDK documentation and a concrete project skill are created only from
  the proven public workflow.
- SOW-0017 and SOW-0018 remain pending and unchanged.
- No product decision is open. Page layout, module boundaries, and exact
  Rust naming are implementation details. Any newly discovered caller-visible
  behavior, compatibility, recovery, or risk choice stops implementation for a
  numbered user decision.

## Requirements

### Purpose

Deliver the one current, unreleased unsigned Phase-1 v4 format, its Go and Rust
engines, and the Rust-provided C ABI as the correct, durable, bounded-memory
storage and comparison foundation for
update-ipsets. The result must be natural as one format: the same portable bytes
serve a live COW database or a compact distributable snapshot, with external live
reader coordination, explicit validation/recovery, direct or named-feed
membership values, optional opaque compressed JSON, and compact unsigned
snapshots. Snapshot signing is a separate Phase-2 feature tracked by SOW-0017.

This SOW also removes the unreleased predecessor experiment and every obsolete
v4 revision/compatibility path. Released C v1/v2 remain the only legacy binary
formats; no unreleased format has a compatibility obligation.

### User Request

The original request created a test-first adversarial audit: add broad permanent
Go and Rust tests, expose failures, and do not repair implementation in that
first pass. After the audit exposed contradictions, the user directed this SOW
to reconcile the complete product and format design, remove the unreleased
predecessor experiment, accept only the latest exact v4, and then implement and
verify every approved decision without compatibility constraints.

The user resolved the original product questions, the final implementability
questions, and the implementation-sequencing correction. Decisions 4-68 below
are binding; decision 45 moves decisions 39 and 42 to Phase 2. Decisions 1-3
preserve the test-first audit history.

### Assistant Understanding

Facts:

- No v4 revision has been released. The existing implementation and goldens are
  disposable experimental state; the released C v1/v2 files are the only legacy
  binary contract.
- The last clean pre-Round-7 baseline passed 294 existing Go tests and 315
  existing Rust tests. The permanent Round-7 suite then exposed 35 Go failures
  and 51 Rust failures across traversal, durability, coordination, corruption,
  resource bounds, external sorting, and algorithmic scaling.
- Empty reachable leaves stop legal traversals; full-space IPv6 cardinality
  overflows; failed migration/feed operations can leak partial state into a later
  commit; current reader-table coordination and openers accept unsafe states;
  several operations allocate in proportion to file size or spill count; and
  nested sequential range-assignment normalization is quadratic.
- No previous test opened the committed cross-language tree goldens. Those files
  encode obsolete layouts and cannot prove current cross-read behavior.
- The former specs and four older current SOWs describe incompatible format
  generations, validation defaults, value models, allocators, and metadata.
  They are historical evidence, not final authority; SOW-0016 is now the sole
  active SOW.
- The actual update-ipsets scheduler may prepare multiple feeds concurrently but
  completes feeds independently and permits mixed success/failure. Phase-1 v4 feed
  replacement therefore serializes one feed lifecycle transaction per committed
  generation rather than imposing a scheduler-wide atomic batch.

Inferences:

- Repairing the old v4.3 layout in place would preserve contradictions. The safe
  route is a clean exact-v4 wire contract followed by lockstep Go/Rust
  implementation and regenerated semantic conformance artifacts.
- Old tests remain valuable evidence, but expectations that conflict with the
  final decisions must be reclassified. In particular, corruption classification
  belongs to explicit `Validate`/recovery; ordinary open and hot paths still must
  enforce bounds, checked arithmetic, and memory safety without general page CRC
  work. Decision 43 adds only the narrow amortized integrity check required
  before committed allocator metadata authorizes destructive page reuse.
- Correctness, crash safety, reader coordination, bounded memory, and scaling are
  one acceptance bar. Passing happy-path unit tests alone cannot establish
  production readiness.

Unknowns:

- No unresolved product, behavior, compatibility, recovery, performance, or
  caller-responsibility decision remains. Decisions 47-68 are resolved either by
  the user or as direct technical consequences of established requirements.
- The normative specification has been reconciled with those decisions and
  independently checked for contradictions. Exact byte offsets, page encodings,
  lock transitions, error types, and implementation decomposition must follow
  that contract; Go, Rust, and C must not invent different semantics.

### Acceptance Criteria

- One complete normative unsigned Phase-1 exact-v4 specification implements the
  applicable portions of decisions 4-68 with
  no old scope modes, minor-version negotiation, per-scope KV, or compatibility
  aliases. These Phase-1 bytes remain unreleased and may be revised by SOW-0017;
  after the first v4 release, future incompatible bytes are v5.
- The predecessor experiment, its import/export paths, tests, fixtures, specs,
  build dependencies, and current-tree references are removed without rewriting
  Git history. Released C v1/v2 documentation and support remain explicitly
  separated from v4.
- Go and Rust implement the same public semantics and cross-open each other's
  valid files. Semantic contents match; internal tree shapes, membership IDs,
  and zlib streams need not be byte-identical.
- Mixed Go/Rust subprocesses coordinate safely on the same live database in both
  directions. Reader/writer slots, process tokens, reclamation, sidecar identity,
  reservation phases, and SHA-512-bound publication/transition resolution are
  interoperable, not merely independently tested.
- The stable Rust-provided C ABI exposes the complete Phase-1 Rust behavior
  through opaque handles, caller-owned buffers or bounded callbacks, explicit
  ownership/lifetime rules, exact structured errors/results, and a panic-proof
  boundary. A generated/checked public header and native C integration tests
  compile, link, and exercise the ABI.
- Direct and membership files implement the exact value, tag, empty-membership,
  canonical bitmap, membership-ID lifecycle, feed catalog, feed handle, name,
  allocation, and lifecycle rules in decisions 18-19 and 26-36.
- Optional JSON metadata implements the exact uncompressed limit, zlib-only
  encoding, byte preservation, absence distinction, and pre-commit COW staging
  in decisions 7-11.
- The persistent hierarchical free-page bitmap, reader-protected retirement
  batches, strict external reader table, database identity, and live/immutable
  reader modes satisfy decisions 6 and 20-22 without file-sized/history-sized
  heap materialization.
- A live-open failure after slot/lease publication either proves exact cleanup or
  returns an identity-bound opaque cleanup guard; interrupted guard-owned
  claim, transaction-update, or clear state 2 is retryable only with retained
  in-process provenance. Established handles retain the same authority across a
  failed close, stale-owner reap, or writer-lease update, so no failed reader,
  writer, validation, recovery, or snapshot path silently loses its only cleanup
  authority.
- Public mutations, exact migrations, commit durability outcomes, poisoning,
  cleanup, and retry rules satisfy decisions 13 and 24-25 at every injected
  failure point. Decision 41's commit nonce prevents another writer's reuse of
  an unpublished transaction ID from falsely resolving an unknown attempt. No
  failed operation can be published by a later commit.
- Main-file bootstrap is bounded and constant-time; live registration is
  proportional only to the caller-sized reader capacity. Normal operations do
  not perform general data-page CRC validation; the allocator-reuse precondition
  follows decision 43. All ordinary paths remain bounds-safe
  and overflow-safe. Explicit validation and recovery satisfy decisions 14-17.
- Every cardinality API uses the exact fixed 129-bit type and represents the full
  IPv6 space exactly.
- Retention refresh performs the exact full-snapshot delta in decision 37,
  including partial range splits, stable old timestamps, removal, and reappearance.
- `SnapshotTo` produces a compact unsigned ordinary v4 generation with bounded
  memory and truthful per-artifact preparation/publication residue for its
  private final-output inode and reservation. It creates no sorting spill and performs no implicit
  full source or final-output validation. Its required sequential publication
  digest pass, replacement policy's separate previous-destination digest pass,
  and combined replacement throughput are measured and reported separately from
  construction. All signing work is absent and tracked by pending SOW-0017 as
  required by decision 45.
- All valid adversarial findings have permanent mirrored tests. Obsolete tests
  are rewritten against the final contract, not deleted merely to obtain green.
- Full Go/Rust tests, race/static checks, conformance, fault injection, fuzzing,
  crash recovery, file-descriptor bounds, allocation bounds, and scaling
  benchmarks pass. No known correctness, durability, security, resource, or
  asymptotic failure remains.
- Specs, AGENTS.md, project workflow memory, conformance documentation, public
  API docs, and SOW lifecycle all describe the same final reality. SOW-0016 is
  completed and moved to `done/` only with implementation and artifacts in the
  same closing commit.

## Analysis

Sources checked:

- All Go and Rust v4 implementation, test, benchmark, conformance, and fixture
  surfaces under `v4/`.
- Every specification under `.agents/sow/specs/`, all pending/current/done SOWs,
  and `AGENTS.md`, audited against decisions 4-68.
- The Round 4-7 adversarial tests and control-flow findings preserved in this
  SOW's execution log and validation evidence.
- The released C v1/v2 reader/writer sources and legacy-format specification.
- The update-ipsets consumer at `firehol/update-ipsets @ e593366f7b0a`, including
  scheduler batching, feed-local result handling, and per-feed finalization.

Current state:

- The code implements an obsolete scope-mode/v4.3 model, append-only free-list
  history, stale per-scope metadata remnants, and incompatible goldens. Those
  bytes and APIs cannot be promoted into the exact unsigned Phase-1 v4 contract.
- Correct foundations exist and should be reused where their invariants survive:
  4 KiB page access, endian-safe IP keys, COW range-tree operations,
  transaction-private page mutation, double-meta publication, positional reader
  I/O, mmap-backed writer access, cursors, external-sort scaffolding, fault
  stores, and mirrored tests.
- The permanent red suite is evidence, not itself the final contract. Tests tied
  to obsolete scope modes or implicit validation need semantic rewrites; tests
  for traversal, arithmetic, durability, cleanup, poisoning, coordination, and
  resource bounds remain direct repair requirements.
- Repository authority is consolidated in this SOW. Phase-1 specs and code must
  be derived from decisions 4-68 rather than from old completed-SOW claims.

Risks:

- This is a complete unreleased format replacement across two implementations.
  Silent Go/Rust drift, stale fixtures, or a partially removed old API can create
  two de facto formats despite one version number.
- Allocator and reader-table mistakes can overwrite pages visible to old readers;
  commit mistakes can make durability unknowable; recovery mistakes can turn
  unreadable coverage into false deletion. These require fault and process tests,
  not only unit tests.
- A no-validation default can be misimplemented as no safety. Every byte-derived
  offset, length, count, page number, and arithmetic operation still needs a
  checked bound before access or allocation.
- Variable membership bitmaps, catalogs, sparse files, compaction, recovery, and
  external sort can accidentally allocate according to logical/file size.
  Allocation and file-descriptor bounds must be measured at adversarial scale.
- Premature optional features can stall the unproven core SDK. Decision 45 keeps
  snapshot signing out of every Phase-1 wire, API, dependency, and test gate.
- Existing dirty work belongs to the user. Deletion and mechanical cleanup must
  target only approved obsolete-format artifacts and exact stale references.

## Pre-Implementation Gate

Status: open. All user/product decisions through decision 68 are resolved, the
normative specification is synchronized and independently contradiction-audited,
and the decision-45 unsigned/signing phase split is resolved. Implementation is
in progress under this gate.

Problem / root-cause model:

1. Successive unreleased implementations accumulated mutually incompatible
   assumptions: separate sealed/live formats, minor-version compatibility,
   scalar/inline/indirect scope modes, caller-associated feed bits, per-scope KV,
   append-only free history, implicit validation, and byte-identical writers.
   Decisions 4-68 replace those assumptions with one exact unsigned Phase-1 v4
   contract and isolate signing in pending SOW-0017.
2. The present wire identity cannot distinguish several incompatible historical
   layouts, and committed goldens were never cross-opened. Compatibility code
   would preserve ambiguity, so obsolete formats, fixtures, and aliases must be
   removed before final goldens are generated.
3. Current transaction and migration paths can mutate shared pending state before
   fallible input completes, return errors without discarding drafts, truncate or
   publish in the wrong durability order, and permit later commit. The API lacks
   a complete publication-outcome model.
4. The append-only free list and reader table do not establish safe page reuse
   across processes. Open/rebuild paths also materialize file-sized state. The
   final allocator therefore needs a COW free bitmap plus reader-protected
   retirement batches and strict external coordination.
5. The old scope representation cannot naturally express the decided two-kind
   value model, named-feed catalog, canonical unbounded membership bitmaps,
   reusable feed indexes, or snapshot-local membership IDs. This is a format and
   API replacement, not a terminology-only rename.
6. Normal open/lookup historically mixed structural safety, CRC validation, and
   trust policy. The Phase-1 contract separates O(1) bootstrap,
   always-mandatory bounds safety, optional explicit graph validation, and
   best-verifiable recovery.
7. Audit failures prove unbounded allocation/file descriptors, empty-leaf
   traversal errors, full-IPv6 arithmetic failures, spill leaks, and quadratic
   normalization. The implementation is not production-ready until each class is
   fixed in both languages with stable regression and scaling evidence.

Evidence reviewed:

- Binding product decisions 4-68 in `## Implications And Decisions` below.
- All current specifications and SOW authority; exact contradictions are recorded
  by the 2026-07-17 authority audit that initiated this consolidation.
- Go/Rust range tree, reader, writer, wire, OS, reader-table, free-list, scope,
  overlap, migration, feed migration, external-sort, cursor, query, conformance,
  robustness, and benchmark code under `v4/`.
- Round 4-7 permanent tests and the detailed findings at `## Execution Log`,
  including legal empty leaves, full-space IPv6, partial migration publication,
  malformed sidecars/openers, obsolete unopened goldens, bounded-memory/FD
  failures, spill cleanup, and quadratic normalization.
- Released legacy format evidence in `src/ipset_binary.c`,
  `src/ipset6_binary.c`, and `.agents/sow/specs/legacy-binary-format.md`.
- Consumer evidence: `firehol/update-ipsets @ e593366f7b0a`,
  `pkg/scheduler/processing_loop.go:47-68`,
  `pkg/engine/run_pipeline.go:40-136`, and
  `pkg/engine/finalize.go:41-62`.
- Consumer access-control evidence from the same checked commit:
  `internal/fileutil/fileutil.go:18-23,120-143,168-200` defines `0700` generated
  directories, `0600` generated files, and chmod-before-rename;
  `pkg/engine/output_comparison.go:642-663` treats wrong-mode output as stale;
  and `install.sh:383-388` fixes service `UMask=0077`. V4 replacement therefore
  needs an explicit access contract rather than inheriting accidental defaults.
- Consumer cancellation evidence from the same checked commit:
  `pkg/iprange/range_source.go:92-126,624-698` checks `context.Context` during
  long materialization, counting, and equality scans, while
  `pkg/engine/process.go:54-128` carries the run context through each feed's
  processing/commit path. An uninterruptible v4 bulk API would regress this
  operational contract.
- External COW/MVCC reference evidence used only to reality-check reader-age
  reclamation and explicit validation separation:
  - `LMDB/lmdb @ be8f614899fa`,
    `libraries/liblmdb/mdb.c:790-826` and
    `libraries/liblmdb/mdb.c:2637-2653`: registered reader transactions protect
    old pages and the writer finds the oldest reader by scanning the table.
  - `erthink/libmdbx @ b516dcacdbfa`, `README.md:151-164`: COW readers prevent
    retired-page recycling and long-lived readers can grow retained storage.
  - `cberner/redb @ adf035308761`,
    `src/transaction_tracker.rs:77-124`, `src/db.rs:545-575`, and
    `src/db.rs:750-784`: live read transactions are tracked separately, checksum
    verification is explicit, and pending free-page draining is an explicit
    maintenance/compaction step.
- OS process-domain evidence: Linux
  [`pid_namespaces(7)`](https://man7.org/linux/man-pages/man7/pid_namespaces.7.html)
  states that PID-taking syscalls use the caller-visible namespace and that the
  same numeric PID can exist in different namespaces; FreeBSD
  [`jail(8)`](https://man.freebsd.org/cgi/man.cgi?manpath=FreeBSD+14.3-STABLE&query=jail&sektion=8)
  identifies jails in a system-wide JID space. Therefore a sidecar binds the OS
  process-identity domain before PID/start-token death proof is allowed.
- Windows namespace evidence: Microsoft's current
  [`FILE_RENAME_INFO`](https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-file_rename_info)
  contract permits a retained directory handle in `RootDirectory` for a simple
  relative target name. Its
  [`CreateFileW` caching contract](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew#caching-behavior)
  explicitly states that `FILE_FLAG_WRITE_THROUGH` flushes rename metadata for
  NTFS. Phase-1 durable Windows publication is therefore limited to local NTFS
  unless another filesystem's equivalent semantics are separately proved.
- Windows cleanup evidence: Microsoft's
  [`FileDispositionInformationEx`](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fscc/2e860264-018a-47b3-8555-565a13b35a45)
  removes the visible link only when the POSIX-delete handle closes, while
  [`FlushFileBuffers`](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers)
  flushes a specified file handle and does not document parent-namespace delete
  durability. `FILE_FLAG_WRITE_THROUGH` explicitly flushes NTFS metadata caused
  by a rename request, but the reviewed Microsoft contracts do not establish an
  equivalent crash-durable final unlink. The specification must therefore
  distinguish removal from an authoritative name from inert housekeeping
  residue instead of claiming proof that no name can reappear after power loss.
- Stable C-ABI reference evidence: `DataDog/libdatadog @ d7980db6be51`,
  `libdd-common-ffi/src/slice.rs:15-34,94-105`,
  `libdd-common-ffi/src/error.rs:24-32,101-120`,
  `libdd-common-ffi/src/utils.rs:7-23,38-53`, and
  `build-common/src/cbindgen.rs:97-130`. This production Rust FFI uses explicit
  C layouts, validates pointer/length slices, owns and destroys error values,
  catches unwind panics at the boundary, and generates public headers. It is
  pattern evidence only; iprange still needs its own versioned ABI and exact
  cleanup/result semantics.

Affected contracts and surfaces:

- Exact unsigned Phase-1 v4 magic/static identity, meta pages, page types, roots,
  generations, and every reachable on-disk graph.
- Go, Rust, and Rust-provided C ABI Reader/Writer creation and open modes,
  direct/membership range
  APIs, feed catalog and handles, JSON metadata, allocator/retirement, reader
  sidecar, transactions, commit resolution, cursors, primitive queries,
  normalization, exact replacements/import, retention, validation, recovery,
  compaction, and unsigned snapshot publication. Detailed multi-file overlap and
  algebra remain in pending SOW-0018.
- Public error taxonomy, exact 129-bit cardinality, checked conversions, resource
  bounds, and poisoned/closed handle state machines.
- All v4 tests, fuzz/property targets, benchmarks, conformance schemas, generated
  goldens, README claims, build features, workflows, and packaging references.
- Specifications, AGENTS.md commands/authority, project runtime skills,
  end-user/operator documentation, and this SOW lifecycle.
- update-ipsets adoption contracts and production-shaped test workloads; this SOW
  does not modify the separate update-ipsets repository.

Existing patterns to reuse:

- Inclusive, sorted, disjoint dual-stack range trees; endian-safe key helpers;
  cursor traversal; transaction-private COW pages; double-meta publication; and
  fixed-buffer positional reader I/O plus mmap-backed writer handles, after
  re-proving them against the final wire spec.
- Synthetic page images, checksum restamping, independent interval-map oracles,
  fault-injecting page stores, commit crash points, temporary-directory process
  tests, allocation counters, descriptor limits, and deterministic benchmark
  operation counts from earlier hardening rounds.
- Mirrored Go/Rust scenario names and shared semantic fixtures. Cross-language
  tests compare observable contents, not physical page layout or compressed bytes.
- Bounded run generation and k-way external merge scaffolding may be reused only
  by explicit validation/recovery scratch paths; normal mutation, ingestion,
  import, and snapshot paths must not call it.
- The released C v1/v2 parser evidence remains isolated as legacy support; it is
  not a template for Phase-1 v4 compatibility/version negotiation.

Risk and blast radius:

- High, but contained to unreleased Go/Rust format libraries and their artifacts.
  The wire layout, most public v4 APIs, persistent page graphs, and test fixtures
  will change. No released v4 consumer requires migration compatibility.
- Data-loss risk centers on COW ownership, free-page reuse, old-reader visibility,
  meta publication, post-publication cleanup, and `OutcomeUnknown` resolution.
- Coordination risk includes cross-operation deadlock: source registrations or
  lifetime locks must be released after final source rechecks and before any
  blocking destination lock/reservation acquisition.
- Security risk centers on symlink/path replacement, stale reader slots,
  unchecked hostile lengths/offsets, decompression bombs, and recovery treating
  unverifiable bytes as live data.
- Performance risk centers on file-sized heap, per-record allocation, unbounded
  descriptors, first sparse mutation, membership interning/reverse lookup,
  compaction, the unsigned snapshot publication-digest pass, and quadratic
  normalization. Benchmarks must prove bounds and curves, not merely report one
  fast small case.
- Compatibility risk is the inverse of normal migration work: retaining any old
  parser, feature flag, golden, or alias would violate the one-exact-v4 decision.
- Cross-language drift is prevented by a normative byte spec, shared semantic
  corpus, bidirectional cross-open tests, mixed-language live coordination and
  publication-resolution subprocesses, and identical error-class/state-machine
  expectations where public surfaces match.

Sensitive data handling plan:

- Implementation and durable evidence use synthetic ranges, generated database
  IDs, temporary paths, and synthetic feed names/JSON.
- No production feed history, customer/community data, credentials, private keys,
  private endpoints, non-private customer-identifying IPs, or operational server
  details may enter SOWs, specs, tests, docs, skills, fixtures, or comments.
- If production-shaped workloads are needed, preserve only public or synthetic
  distributions/counts and sanitize source paths. The final sensitive-data scan
  covers every changed durable artifact before close.

Implementation plan:

0. **Authority and removal.** Make SOW-0016 the sole active SOW; write the exact
   normative v4 and API contracts; update project authority; inventory and remove
   the predecessor experiment plus obsolete v4 compatibility, fixtures, features,
   and references. Preserve released C v1/v2 separately. No implementation chunk
   may invent bytes absent from the normative spec.
1. **Wire/static foundation.** In Go and Rust together, replace version/minor and
   scope-mode bootstrap with exact v4 identity, `database_id`, family,
   `value_kind`, 16-byte `value_tag`, root fields, page registry, checked
   geometry, O(1) meta selection, aligned-file checks, and exact 129-bit
   cardinality/error types. Regenerate only foundation fixtures after both readers
   agree on the spec.
2. **Range core and safe traversal.** Adapt records to fixed `value:u32`; preserve
   COW range operations/cursors; fix empty-leaf traversal, family boundaries,
   checked arithmetic, cyclic/depth guards, and ordinary-path bounds safety.
   Remove final public/internal scope vocabulary. Normal operations must not
   perform general data-page CRC validation.
3. **Allocator and live coordination.** Replace tombstone history with the
   persistent hierarchical COW free bitmap and transaction-grouped retirement
   batches. Implement lowest-safe-page allocation, streaming release, safe tail
   truncation, strict `.readers` binding/locking/registration/reaping/reset, live
   versus immutable reader open modes, failed-open slot/lease cleanup guards, and
   old-reader page protection. Prove no file/history-sized heap and no path-reopen/
   symlink/replacement race. Apply the exact narrow allocator-reuse integrity
   policy in decision 43.
4. **Transaction and durability state machine.** Implement clean/private drafts,
   whole-pending-transaction abort after post-mutation failure, cleanup poisoning,
   bounded exact residue ledgers for unpublished tails,
   durable data-before-meta ordering, attempted transaction IDs, structured
   `NotCommitted`/`Committed`/`OutcomeUnknown`, and reopen resolution. Run every
   fault point before building higher lifecycle APIs.
5. **Membership dictionary and feed catalog.** Implement canonical variable LE
   `u64` bitmaps, ID-zero absence, snapshot-local reusable membership IDs, checked
   reference counts with bounded delta aggregation, `feed_index_limit`, structured
   name/index catalog, deterministic lowest-free allocation, engine-issued
   generation-bound handles, enumeration/lookup, and atomic create/replace/delete/
   rename. Remove inline-32/fixed-256/caller-bit paths and ensure one feed lifecycle
   operation per generation.
6. **Opaque JSON metadata.** Implement optional whole-object zlib storage with an
   exact 1 MiB pre-compression/decompression bound, absent-versus-empty semantics,
   exact byte round trip, private `SetMetadataJSON`/`ClearMetadataJSON` staging,
   unchanged-root reuse, and atomic publication with the other generation roots.
7. **Normalization and exact workflows.** Implement bounded, non-quadratic
   same-file normalization with zero external sorting files; advanced direct and
   membership transactions; named-feed/direct replacement; name-based membership
   import; exact workflow statistics; primitive queries/cursors with 129-bit
   counts; and the direct+`retention` full-snapshot delta. A valid empty desired
   stream is distinct from source failure and, for feeds, from catalog deletion.
8. **Explicit validation and recovery.** Build non-mutating full graph validation
   for ranges, catalogs, dictionary counts, metadata, allocator, retirement, and
   ownership; provide detailed coverage/error reporting; rebuild a new database ID
   from only fully verifiable source records/metadata; never scan unreachable pages
   as live or guess from failed pages; release source protection after final source
   rechecks and before destination locks. Rewrite old implicit-open tests here.
9. **Compact unsigned snapshots.** Implement bounded-memory `SnapshotTo` from a
   pinned generation, canonical empty allocator/retirement state, preserved
   logical identity/generation/catalog indexes/JSON, no implicit full validation,
   truthful per-artifact preparation residue and cleanup for the private final
   output and reservation, the explicit publication-identity digest pass, source-before-destination
   lock release/order, and failure-safe destination publication.
10. **C ABI.** Add the stable Rust-provided C ABI only after the Rust behavior is
    proven: opaque handles, explicit allocation/ownership, caller buffers or
    bounded callbacks, exact error/result structs, panic containment, generated
    header checks, and native C integration coverage for the complete Phase-1 API.
11. **Conformance, performance, and artifact closure.** Generate bidirectional
    Go/Rust semantic goldens that are actually opened; complete adversarial,
    property/fuzz, multi-process, crash, sparse-file, and resource matrices;
    prove allocation/FD/asymptotic bounds at update-ipsets-shaped scale;
    update specs, AGENTS.md, project skills, conformance docs, public docs, build/
    CI references, and SOW validation; search for every removed name/path/format.

Dependencies are strict: 0 precedes all code; 1 precedes 2-9; 2-4 precede 5-9;
5-6 precede lifecycle/validation/snapshot semantics that consume their graphs;
the C ABI in 10 follows proven Rust behavior through 9; and 7-10 precede the
cross-language/performance/artifact closure in 11. Within each chunk, add or
adapt failing tests first, implement Rust and Go in lockstep, then run targeted
and shared validation before advancing.

Validation plan:

- For each chunk: prove the targeted regression fails for the intended reason,
  implement both languages, run the targeted matrix, then the unaffected normal
  suites. Do not weaken an assertion merely because the old format cannot pass it;
  explicitly map each changed assertion to a final decision.
- Run Go tests normally and under race detection, `go vet`, formatting, fuzz
  smoke/long campaigns, allocation benchmarks, descriptor-limit subprocesses,
  multi-process reader/writer tests, and crash/fault matrices.
- Run Rust workspace tests with all features and serialized process-sensitive
  targets, formatting, Clippy with warnings denied, fuzz/property tests, Criterion
  compilation/runs, allocation tests, descriptor-limit subprocesses, and crash/
  fault matrices.
- Cross-open every committed Go and Rust golden in both languages and compare
  ranges, direct/membership values, catalog names/indexes, decompressed JSON,
  cardinalities, snapshot state, and error classes. Physically different valid
  layouts and zlib streams are accepted.
- Freeze one cross-language status/error registry before public API
  implementation: stable numeric codes, required fields, handle-state effects,
  and operation precondition/error precedence. Generate Go/Rust/C checks from
  that authority so no implementation invents a different meaning for generic
  exhaustion, overflow, conflict, cancellation, sink, or cleanup failures.
- Run mixed Go/Rust subprocess matrices in both directions against one live
  database: reader registration/release, writer exclusion, process-token death
  proof, oldest-reader reclamation, sidecar replacement, every reservation/
  sidecar write-ahead phase, and SHA-512-bound publication/transition inspection
  and resolution.
- Exercise stored-zero/current-nonzero and stored-nonzero/current-zero start
  tokens, unrepresentable PID/thread values, Linux PID-namespace mismatch, and
  FreeBSD jail-ID mismatch. No mismatch may reap a possibly live owner; an
  unprovable process domain returns `LiveCoordinationUnsupported`. Reboot or
  recreate the PID namespace/jail and prove ordinary live open fails closed,
  while caller-certified offline `ResetLiveCoordination` treats old slots as
  opaque, atomically replaces the exact old sidecar, and publishes the current
  domain without interpreting an old PID.
- On POSIX, fork with an active reader, writer, armed cleanup guard, residue
  handle, and persistent child handle through both native and C-ABI surfaces. Every child
  method rejects `ForkedHandle` before inherited mutex/lock use; child destruction
  only closes its copies and the parent remains authoritative/protected.
- Inject every fallible reader/writer/internal-registration open step after slot
  or lease claim. Prove exact clearing before an ordinary error, or an opaque
  identity-bound cleanup guard whose idempotent retry clears only its own claim
  or its own provenance-backed interrupted claim/update/clear state 2,
  recognizes a later valid nonce as proof that the old claim was cleared and
  safely reused, survives path replacement safely, and is exposed through the C
  ABI.
- Inject every established-reader close, established-writer close, and
  post-publication writer-lease update transition, plus every stale writer/reader
  reap from an opener, handle, resolver, and reclamation scan. Prove the
  non-consuming handle or transferred cleanup guard retains retry authority after
  each failure and follows the exact explicit-destruction policy selected by
  decision 49; no hidden transition may lose the only state-2 provenance.
- Run every transition/create/publication resolver with its exact recorded
  parent and with a replaced or different directory. Prove parent-identity
  mismatch is rejected before absence classification, artifact inspection, or
  cleanup; reconstructed attempts bind to the retained authoritative directory.
- For every Create/Initialize/Reset/publication resolver, apply a result created
  for basename A to absent and present basename B in the same retained directory;
  require `DestinationNameMismatch` before canonical/private inspection. Copy or
  rename a reservation/sidecar from A to B and prove ordinary open and no-result
  resolution fail closed. Independently corrupt encoding kind, length, digest,
  and returned exact bytes with recomputed CRCs; a valid later owner at B is never
  later reuse for A. Cross-language cases include POSIX non-UTF-8 bytes, valid
  non-ASCII Windows UTF-16, and rejection of every unpaired-surrogate boundary.
- Corrupt both canonical publication-reservation header blocks and prove the
  explicit offline residue remover requires the same-process retained inspection
  handle and quiescence, exclusively locks and never changes the main, rejects a
  valid live sidecar, and reaches synchronized canonical absence or recognizes a
  fully valid later canonical reuse without pathname/age guessing. Force POSIX
  inode-number reuse and prove a serialized old identity cannot authorize
  deleting the later canonical inode. Cover inspect-only Close/C destroy,
  failed-remove retry on the same non-consuming residue handle, and ordinary
  descriptor-only destruction, plus canonical absence at inspection and
  canonical replacement between inspection and removal. Present every valid
  reservation kind 1-5 and sidecar origin 1-3 to both residue removers and prove
  all selectable records are rejected in favor of their operation-specific
  resolver; only neither-header-selectable coordination is eligible.
- Corrupt a late retirement-list page/entry after earlier entries remain valid
  and prove the complete first pass fails before the second pass releases or
  overwrites the first listed page. Cover bad CRC, length, ordering, duplicate,
  range, batch identity, and reader-threshold cases in both languages.
- Prove O(1) open by page-touch/allocation instrumentation; prove ordinary paths
  do not invoke data-page CRC; prove bounds-safety independently with malformed
  lengths/counts/offsets. Exercise identical transaction metas, adjacent
  transactions in canonical physical parity, swapped adjacent metas, a sole
  canonical-parity valid meta, and a sole wrong-parity valid meta. A sole valid
  meta may be exposed only by the allowed immutable/offline diagnostic path with
  explicit `SoleMeta0`/`SoleMeta1` selection status; every live-current writer,
  mutation, destructive cleanup, and commit resolver must fail
  `CurrentGenerationUnprovable`. Prove explicit
  validation deterministically streams
  stable reason-coded findings, continues independent roots/siblings after an
  untraversable child, reports extent/unknown bounds without treating unreachable
  bytes as live, distinguishes factual invalidity from operational/sink/budget
  failure, and returns identical summaries through Go/Rust/C.
- Inject every allocation, write, sync, meta-publication, truncate, unmap, close,
  spill, compression, and snapshot failure. Assert the exact commit
  outcome, old/new generation authority, poisoning, bounded complete unresolved
  cleanup ledger, and retry rule.
- At every phase-3/4 commit fault, prove `OutcomeUnknown` always leaves the
  original writer close-only with `RetainedWriterCloseRequired`; Close reselects
  metas and either truncates only the still-selected old tail or recognizes the
  attempted/later generation as safe supersession before clearing the lease.
- Verify validation/recovery scratch, private-output, and publication-reservation
  names never reuse ordinals; simulate unlink success followed by directory-sync failure and
  prove the identity-bound idempotent absence-plus-sync cleanup retry reaches
  durable absence, while parent-directory replacement returns conflict.
- On Windows local NTFS, inject every authoritative-name-to-housekeeping rename,
  write-through completion, recheck, truncate/flush, delete-on-close, and crash
  boundary for each artifact kind. Prove the selected decision-54 correctness
  cleanup/housekeeping result never loses a two-name ambiguity, never treats an
  inert GC name as resolver authority, and removes it only through exact streamed
  listing plus directory/name/header/identity checks.
- Prove public main/destination names reject the ASCII-case-insensitive
  `.iprange-` prefix and `.readers` suffix on every platform. On Windows cover
  case/trailing-dot aliases and alternate data streams. Present valid-v4 and
  arbitrary-file lookalikes at every private/spill pattern and prove invalid or
  mismatched spill ownership headers and equal main/coordination identities are
  never opened as authority or auto-removed.
- Inject unpublished-tail cleanup across exact target, later-generation, same-
  transaction/different-nonce, unexpected growth, and replaced-parent/main
  identities. Prove exact truncate+sync or safe satisfied-by-supersession with no
  stale-ledger truncation of a newer generation.
- Crash a live writer before and after meta publication with an unpublished
  aligned tail, then inject tail truncate/sync and lease-clear failures while
  every opener/resolver path reaps it. Prove the dead lease is never cleared
  before tail cleanup, and that the exact tail authority plus proven-dead source
  remains in a retryable coordination guard on every failure.
- Exercise 128-bit claim-nonce generator failure and all-zero rejection, while
  documenting the explicit `2^-128` independent-draw collision assumption, and every
  reservation retirement race. Cross-language tests cover valid later
  reservation reuse, `DesiredContentPresentThenLive`, active-later-writer
  `WriterBusy`, malformed later coordination conflict, and resolver retry while
  retaining the private-output lifetime lock.
- Run opposite-direction snapshot/recovery replacement subprocesses (`A -> B`
  concurrent with `B -> A`) plus same-path immutable `ReplaceExisting`
  compaction, immutable `FailIfExists` rejection, and live same-path rejection
  variants with watchdogs; prove source protection is released before destination
  blocking and no lock-order deadlock occurs. Inject source-registration release
  failure after final source checks and prove it returns a pre-publication
  preparation failure with its source guard and private-artifact ledger, never a
  post-boundary result while retaining the source lifetime lock.
- Exercise metadata-only commit, metadata plus ordinary incremental mutation,
  the one optional stage after every exact draft, repeated-stage rejection as
  selected by decision 52, identical-bytes Set, and
  already-absent Clear through Go/Rust/C with allocation and fault injection.
- Exercise metadata reads through absent, empty, maximum-size, corrupt, staged
  Set, staged Clear, and committed-base states. Prove the two-call core returns
  exact required decompressed length, writes no partial bytes to a too-small
  buffer, preserves Reader generation pinning and Writer read-your-writes, and
  never mutates a draft on read failure.
- Exercise every validation mode selected by decision 53: valid current live and
  immutable generations, bootstrap-only reports with no selectable meta, an
  explicit previous/unordered offline recovery candidate, unsafe live
  coordination, sink/resource failure, and exact cleanup-guard transfer.
- Benchmark increasing file size, feed count, bitmap width, dictionary size,
  nested intervals, spill count, sparse growth, reader count, and retirement
  history. Record curves/operation counts and enforce bounded memory/FDs and
  non-quadratic normalization rather than flaky absolute wall-time thresholds.
- Benchmark per-record adapters against the mandatory bounded-batch source/sink
  path at increasing range counts in native Go/Rust and the C ABI. Assert bounded
  callback count, no ownership transfer, legal empty input, exact source/sink
  error propagation, Stop policy by operation class, callback-panic containment,
  and deterministic cancellation-versus-callback precedence.
- Run same-handle concurrency matrices under Go race detection, Rust and native
  C: concurrent Reader lookups/scans; prohibited Reader Close races; parent Close
  with live cursor/view/feed children; child creation during Close; fail-fast
  reentrancy of every mutable/cleanup handle; callback reentry; and independent
  handles under database locks. Measure point lookup and warmed-cursor throughput
  with the selected contract to prove no hidden synchronization was added to the
  hot path.
- For every engine-created artifact type, vary umask, inherited/default ACL,
  POSIX effective UID, Windows primary/impersonation token, crash/restart
  resolver principal, replacement destination access, and legitimate
  post-Published chmod/chown/ACL widening. Prove the attempt binds the original
  normalized CreatorOnly commitment before publication, later access is reported
  orthogonally without changing byte identity or republishing, old residue can be
  cleaned safely, and ordinary existing-file opens never mutate access.
- Cancel each long operation before start, between bounded batches/pages/reader
  slots, while waiting on an interruptible lock, immediately before every
  ambiguity boundary, and just after it. Prove O(reader-capacity) opens/CreateLive
  have bounded checkpoints, callback outcomes win once invoked, post-boundary
  resolution ignores the signaled token until factual outcome/cleanup authority
  is secured, and tests do not claim wall-clock interruption inside uninterruptible
  OS I/O.
- Benchmark sparse reader tables with zero/one active reader and increasing
  configured capacity; record open/commit operation-lock hold time separately so
  O(capacity) coordination cost cannot hide behind active-reader counts.
- Measure unsigned `SnapshotTo` construction and its required sequential
  publication-digest pass separately on representative artifact sizes. For
  `ReplaceExisting`, separately measure the old-destination digest and the
  combined end-to-end replacement. Report throughput, wall time, allocations,
  RSS, lock hold time, and bytes read/written.
- Exercise update-ipsets-shaped synthetic retention and independent per-feed
  replacement workflows end to end, including empty feeds, mixed success/failure,
  reader pinning after selected commits, compact unsigned snapshot, reopen,
  validation, and recovery report.
- Recover sparse catalogs with high indexes and reject the highest-index feed;
  prove both languages preserve every accepted `(name,index)` and the selected
  non-shrinking `feed_index_limit`, leave rejected indexes free, and rebuild
  memberships only from accepted active bits.
- Compile and link the generated C header against the Rust library; exercise
  every ABI family from native C, including errors, buffer sizing, callbacks,
  handle lifetime, and proof that no Rust panic crosses the boundary.
- Run native Linux validation locally and, after obtaining the user's explicit
  authorization for each remote system, run the Go/Rust/C compile and process/
  crash matrix on Windows, macOS, and FreeBSD. Cover no-follow opens, canonical
  process-start/death proof, lock auto-release, sidecar interoperability, atomic
  no-replace/replace, file+namespace synchronization, and explicit unsupported-
  filesystem errors. On Windows, prove the complete publication/crash contract
  on local NTFS and rejection of other filesystems unless equivalent semantics
  were separately established. Remote execution is a close-gate requirement,
  not a blocker for local implementation chunks.
- Run same-failure searches across both implementations and every nearby public
  API after each fix; at final close search for obsolete paths, format identities,
  scope vocabulary outside historical evidence, compatibility aliases, stale
  goldens, disabled tests, ignored cleanup, unchecked arithmetic, and generic
  commit errors.

Artifact impact plan:

- `AGENTS.md`: replace stale format/library commands and authority pointers; state
  exact-v4, explicit validation, semantic conformance, and final test workflows.
- Runtime project skills: create or update concrete project skills for the final
  binary-format invariants and Go/Rust conformance/crash/benchmark workflow once
  those procedures are stable; do not create generic filler.
- Specs: replace obsolete v4.3/scope/minor-version documents with one complete
  exact-v4 normative byte/API contract; rewrite product architecture and
  update-ipsets adoption findings; retain released v1/v2 as a separate legacy
  fact specification; remove the predecessor experiment and obsolete v5 note.
- End-user/operator docs: document final library open modes, explicit validation,
  recovery, feed lifecycle, JSON, unsigned snapshot behavior, and legacy
  boundaries wherever the public SDK/README/wiki surfaces expose them.
- End-user/operator skills: none currently exist; if downstream SDK/operator
  skills are introduced or found, update them with the same contracts.
- Tests/build/CI: remove obsolete features, directories, import/export paths,
  fixtures, goldens, README claims, workflow filters, and dependencies; add final
  conformance generation/verification workflows.
- SOW lifecycle: close and move SOW-0011/0013/0014/0015 as superseded without
  claiming their old gates complete; delete or close obsolete pending experiment
  SOWs as authorized; keep SOW-0016 as the sole current SOW until implementation,
  artifacts, and final validation close together; keep signing separately open
  and pending in SOW-0017.

Open-source reference evidence:

- No external repository was needed for the original adversarial-test round.
  The later product-contract reconciliation also reviewed the actual consumer:
  `firehol/update-ipsets @ e593366f7b0a`. Its scheduler drains several queued
  feeds into one run (`pkg/scheduler/processing_loop.go:47-68`), its workers
  process feeds independently and may report a mixture of successes and failures
  (`pkg/engine/run_pipeline.go:40-136`), and each feed's current artifact is
  finalized independently (`pkg/engine/finalize.go:41-62`).
- Before finalizing implementation details, any allocator/database techniques
  actually reused must be recorded as `owner/repo @ commit` with
  repository-relative evidence. External designs are evidence only; decisions
  4-68 remain authoritative.

Open decisions:

- None. Decisions 4-68 define the final unsigned Phase-1 behavior. Mechanical
  API spelling and implementation decomposition are engineering work, not new
  product decisions. If specification reconciliation exposes a genuine new
  behavior, safety, performance, compatibility, recovery, or caller-
  responsibility fork, implementation stops and that specific fork is presented
  to the user.
- Decision 45 moved all signing design and implementation to pending SOW-0017;
  it is not an open Phase-1 decision. The resolved decisions are synchronized
  into the normative spec, the contradiction audit passed, and the implementation
  gate is open.

## Implications And Decisions

1. User decision: create a new SOW rather than extending the previous audit SOW.
2. User decision: add all useful corner-case tests, including tests that already
   pass, because permanent contract coverage is the goal.
3. User decision: expose failures first; implementation repair is a separate
   subsequent pass.
4. User decision (2026-07-16): the predecessor format was an unreleased
   experiment. Delete its
   implementations, importers/exporters, tests, fixtures, specifications, build
   dependencies, and current-tree references. Do not preserve a compatibility
   path. This does not authorize rewriting Git history.
5. User decision (2026-07-16): released C v1/v2 remain the only legacy formats.
   That predecessor and earlier v4 revisions receive no compatibility support;
   the final latest v4 is the sole accepted v4 format.
6. User decision (2026-07-16): there is one v4 file format. The same file bytes
   may be used as a live mutable database or distributed as a read-only
   artifact. Live reader coordination is external and ephemeral; immutable
   snapshot readers do not create or use it, and it is never shipped, signed,
   or treated as part of the portable v4 format.
7. User decision (2026-07-16): replace the pending per-scope KV design with one
   file-level opaque JSON payload. The engine reads and writes the payload but
   does not parse, normalize, validate, merge, or impose a schema on it. This
   application payload is separate from engine-defined structural metadata such
   as the membership feed catalog; it does not replace that catalog.
8. User decision (2026-07-16): the JSON payload has an exact 1 MiB
   (1,048,576-byte) maximum measured before compression.
9. User decision (2026-07-16): when metadata is present, it has one storage
   encoding: a whole-object zlib/DEFLATE stream. There is no automatic raw
   fallback and no Zstandard variant. Decompression must reproduce the exact
   publisher-supplied bytes and enforce the uncompressed limit.
10. User decision (2026-07-16): `SetMetadataJSON` compresses and stages the new
    payload in private COW pages. `Commit` atomically publishes the staged
    metadata root and does not perform compression. Metadata that was not
    changed in the transaction keeps its existing root and is not rewritten.
11. User decision (2026-07-16): file-level JSON metadata is optional. A zero
    metadata root means absent metadata and the read API returns `None`/`nil`.
    A publisher-supplied `{}` is stored as those exact compressed bytes and is
    distinct from absence. `ClearMetadataJSON` stages the absent state.
12. User decision (2026-07-16): v4 cross-language compatibility is semantic,
    not byte-for-byte writer identity. Go and Rust must cross-open each other's
    valid files and expose identical ranges, membership values, feed catalogs,
    and decompressed metadata.
    Valid internal tree layouts and zlib streams may differ while conforming to
    the same single wire format.
13. User decision (2026-07-16): exact bulk replacement operations such as
    `Migrate` and `MigrateFeed` require a clean writer with no pending changes.
    They stage their own private draft. Any pre-commit failure automatically
    discards that complete draft and preserves the previous committed state; a
    cleanup failure makes the writer unusable and requires close/reopen.
14. User decision (2026-07-16): recovery is separate from normal migration.
    Provide non-mutating detailed validation and a recovery operation that reads
    the damaged source and rebuilds a new v4 file. Normal migration has no
    `best_effort` flag because unreadable input must not be mistaken for an
    intentional deletion.
15. User decision (2026-07-16): automatic recovery copies only records and
    metadata that remain fully verifiable. It reports rejected and unknown
    coverage; it never guesses from checksum-failed pages or scans unreachable
    physical pages as live data. The caller decides whether to keep the old
    feed, retry the source, or use the verified recovered subset.
16. User decision (2026-07-16): reader and writer open never perform an implicit
    full-file validation. `Validate`/detailed validation is an intentional
    caller operation. Open performs only bounded, constant-time bootstrap safety
    and compatibility checks required before mapping or mutating a file; it does
    not walk data, membership-dictionary, feed-catalog, allocator,
    metadata-payload, or other page graphs.
17. User decision (2026-07-16): normal lookup, scan, and mutation paths do not
    calculate or verify data-page checksums. Page CRC verification belongs only
    to explicit validation and recovery. Bounds checks, checked arithmetic, and
    memory-safety guards remain mandatory. A caller that skips validation
    explicitly accepts that plausible corruption may yield an incorrect value
    or miss without being classified as corruption. The later implementability
    audit exposed destructive allocator reuse as a distinct old-generation
    durability hazard, not lookup classification; decision 43 makes committed
    allocator pages the single narrow, amortized CRC exception.
18. User decision (2026-07-16): the single v4 format has two generic range-value
    semantics: direct and membership. A direct value is an opaque `u32`; the
    caller owns and understands its meaning (for example timestamp, ASN, country,
    or another application-defined identifier), while the engine stores and
    compares it without interpreting it. A membership value represents an
    arbitrary-width feed bitmap; the record's fixed `u32` identifies the
    in-file interned bitmap rather than imposing a 32-feed inline limit. The
    caller does not assign bitmap positions: a structural in-file feed catalog
    owns the name-to-position mapping as specified by decision 28.
19. User decision (2026-07-16): the static file metadata includes a structural
    `value_kind` (`direct` or `membership`) and an immutable, caller-defined
    `value_tag` field of exactly 16 bytes. A tag contains at most 15 bytes followed
    by a mandatory NUL byte; any remaining bytes after the first NUL are zero.
    The exact tag `retention` identifies direct `u32` values with the retention
    API's defined timestamp semantics. Retention operations must check both
    `value_kind == direct` and `value_tag == retention` before staging any change;
    any other tag returns a typed value-kind/tag mismatch. Other tags remain
    caller-defined and engine-opaque.
20. User decision (2026-07-16): replace the append-only, tombstone-based free-list
    history with a persistent hierarchical COW free-page bitmap. A leaf bit is
    one only when that page is currently free and safe to overwrite; summary
    levels permit a bounded search for the lowest free page using `u64`
    nonzero/trailing-zero operations without scanning from page zero. Bitmap and
    summary mutations are private until the same atomic meta publication as the
    data root. Pages no longer live in the current generation but still visible
    to an older registered reader remain in on-disk transaction-grouped retirement
    batches, not in the free bitmap. Once the oldest reader makes a batch safe,
    its pages are streamed into the bitmap and the batch is removed. These batches
    contain only outstanding reader protection, not permanent allocation history;
    allocator open and normal operation must not materialize either structure in
    heap according to file size or history.
21. User decision (2026-07-16): use one strict external reader-table sidecar for
    live concurrent access. Its canonical name is the database path plus
    `.readers`; it has a versioned fixed-layout header bound to the database
    identity and an explicit reader capacity. Open must reject symlinks,
    non-regular files, incompatible headers, incorrect size, and path replacement.
    Every handle and guard retains the originally opened file descriptor and never
    reopens the pathname. Registration, update, removal, stale reaping, and the
    writer's oldest-reader scan use one defined cross-process locking protocol.
    Implementation clarification (2026-07-25): each reader holds an
    open-description lock on its slot for its complete lifetime and the
    registration/publication gate covers meta selection plus slot publication.
    Therefore the selected transaction is written directly; no transaction-zero
    state or PID liveness inference is needed. Lock availability is the operating
    system's proof that stale slot bytes are unowned. Commit prevents new
    registration through atomic meta publication without blocking established
    reads. Normal live open never silently recreates a missing, malformed, or
    replaced table. Reset is an explicit offline operation which requires proof
    that no live users remain.
22. User decision (2026-07-16): add an immutable, nonzero 128-bit random
    `database_id` to the v4 static identity. It is stored identically in both meta
    pages and in the live-reader sidecar header; disagreement is an O(1) bootstrap
    rejection. Live handles retain and check the local operating-system
    identities of the main and sidecar so path replacement cannot redirect an
    established handle. A
    newly created database and a recovery output receive a new ID. A byte-for-byte
    copy preserves the logical database ID, while its local file identity and
    sidecar remain independent. Rename preserves the ID. This field is identity,
    not validation, and does not authorize any implicit page-graph scan.
23. User decision (2026-07-16): every public address-cardinality API uses one
    exact, fixed-size unsigned 129-bit value represented as bit 128 plus high and
    low 64-bit words. This covers all per-family counts and combined IPv4/IPv6
    totals without heap allocation or arbitrary-precision arithmetic. It replaces
    `u64` overlap counts and saturating `u128` cursor counts. Checked convenience
    conversions to `u64` and `u128` return a typed overflow error; they never wrap
    or saturate. Shared conformance data represents cardinalities as exact decimal
    strings, including `2^128` for the full IPv6 space.
24. User decision (2026-07-16): public mutations are transaction-atomic, not
    savepoint-atomic. An input or state error proven to occur before mutation
    leaves the pending transaction unchanged. Any failure after an operation may
    have changed pending state automatically aborts the entire pending transaction
    back to the last committed generation, including earlier uncommitted changes,
    and returns a typed transaction-aborted error. The writer becomes ready again
    only if rollback and unpublished-growth cleanup both succeed. Storage I/O
    failure, detected corruption in committed data, or rollback/cleanup failure
    makes the writer unusable and requires close/reopen plus explicit validation
    when corruption is suspected. No later commit may publish any part of a failed
    operation. Exact migration retains its stricter clean-writer/private-draft
    contract and therefore has no unrelated pending changes to lose.
25. User decision (2026-07-16): `Commit` exposes a structured durability outcome
    with the attempted transaction ID: `NotCommitted`, `Committed`, or
    `OutcomeUnknown`. A failure before publication-meta writing begins is
    `NotCommitted`; the previous generation remains authoritative, but a storage
    error still requires close/reopen before retry. Once publication-meta writing
    begins, any write or synchronization failure is `OutcomeUnknown`; the caller
    must reopen the same `database_id` and resolve the attempted transaction ID
    before retrying. Successful synchronization of the new meta is the durable
    publication point. Any later truncation or cleanup failure is reported as
    `Committed` with cleanup failure, and the transaction must not be retried.
    Every non-clean outcome makes that writer handle unusable. A generic commit
    error which hides publication state is forbidden.
26. User decision (2026-07-16): empty membership is canonical absence. In a
    membership file, value ID zero means no membership and is never a persisted
    dictionary entry or stored range value. Interning an empty or all-zero bitmap
    returns zero without allocating; setting a range to empty has delete semantics;
    clearing the final membership bit removes the covered range. Trailing zero
    `u64` words are removed from every nonempty bitmap before interning. Direct
    files are different: opaque direct value zero remains valid and may be stored.
    The range map does not encode “checked but intentionally belongs to no feeds”;
    a caller needing that provenance places it in the file-level JSON metadata.
27. User decision (2026-07-16): membership bitmap capacity is proportional to
    each file's committed feed-index high-water limit; there is no separate
    64 KiB, 1 MiB, or other arbitrary bitmap ceiling. Feed indexes are `u32`.
    The committed `feed_index_limit` is represented with checked `u64` semantics
    so it can express the full zero-through-`u32::MAX` index domain, including a
    count of `2^32`.
    A bitmap is a canonical variable-length sequence of architecture-neutral
    little-endian `u64` words. It grows in 64-bit logical steps, and physical
    trailing zero words are omitted. Increasing the committed feed-index limit
    logically zero-extends existing memberships without rewriting them. Runtime
    byte lengths, offsets, and page calculations use checked `u64` arithmetic.
    Variable-sized bitmap records are packed within COW pages; logical eight-byte
    growth does not change the format's page-sized physical allocation. The
    current fixed 256-byte inline reservation is superseded and must be removed.
28. User correction and decision (2026-07-16): a membership file is a named-feed
    database, not an unnamed bitmap pool. V4 therefore contains an engine-defined,
    structured, COW feed catalog which maps every active, unique feed name to its
    engine-assigned `u32` feed index. The catalog is part of the binary format,
    is distinct from the opaque file-level JSON payload, and is published in the
    same atomic generation as the range tree, membership dictionary, and JSON.
    Public APIs enumerate feeds and look up a feed name to obtain its assigned
    bit; callers never choose or directly assign bit positions. Feed indexes may
    be reused: after feed X is transactionally removed, its index may be assigned
    to feed Y. The committed `feed_index_limit` is a non-shrinking high-water
    mark, so reuse does not enlarge or rewrite existing bitmaps. Catalog
    reassignment and the corresponding membership replacement must be atomic.
    Readers pin the
    catalog, ranges, membership dictionary, and JSON from one generation, so an
    old reader sees X and its old memberships while a new reader sees Y and its
    new memberships. A bitmap or bit index has no standalone cross-generation
    meaning without the catalog from the same snapshot. Applications may impose
    a stricter no-reuse policy, but the v4 format and engine permit reuse.
29. User decision (2026-07-17): each catalog feed name is a unique, machine-safe
    ASCII identifier of 1 through 255 bytes. Valid names use only lowercase
    `a`-`z`, digits `0`-`9`, underscore, hyphen, and period, and must begin and
    end with a letter or digit. Lookup is an exact byte comparison; uppercase is
    invalid rather than case-folded. The same rules and results apply in every
    language implementation. Human-facing titles, descriptions, and other
    publisher annotations belong in the optional opaque JSON payload, not in the
    structural feed-name field.
30. User decision (2026-07-17): the engine assigns the lowest currently free
    `u32` feed index when adding a named feed. If no hole exists below the
    committed `feed_index_limit` high-water mark, it assigns the index at that mark
    and increments the count with checked arithmetic. A slot freed by a committed
    deletion can be reassigned by a later `CreateFeed`; the
    one-lifecycle-per-transaction rule intentionally provides no public
    delete-and-create transaction. Allocation is deterministic and identical
    across language implementations; callers cannot request a specific index. If all `2^32`
    indexes are active, adding a feed returns a typed exhaustion error before
    mutation.
31. User decision (2026-07-17): public membership mutations never accept a bare
    caller-supplied bit index. Bulk operations identify a feed by its catalog
    name. Repeated incremental operations may use an opaque engine-issued feed
    handle which binds the name, assigned index, database identity, and catalog
    generation to one writer transaction; stale, foreign, removed, reused,
    committed, aborted, or reopened-writer handles fail before mutation. Internal
    engine primitives continue to operate on indexes. Read APIs expose feed names
    and assigned bits for fast bitmap tests, and may provide reader-issued handles
    bound to one pinned snapshot. A reader handle remains valid until that reader
    closes even when a newer committed generation removes the feed or reuses its
    bit.
32. User decision (2026-07-17), refined by decision 35: named-feed lifecycle
    initially defined three explicit exact operations, each requiring a clean
    writer and staging a private draft.
    `CreateFeed(name, stream)` requires an absent name, assigns the lowest free
    index, and creates the catalog entry plus its complete membership.
    `ReplaceFeed(name, stream)` requires an existing name, retains its assigned
    index, and exactly replaces its complete membership. An empty stream is valid
    for either operation and leaves a cataloged feed with zero IPs.
    `DeleteFeed(name)` requires an existing name and atomically clears all of its
    memberships, removes its catalog entry, and frees its index. Already-existing
    and not-found precondition failures are typed and occur before mutation. Any
    stream, validation, mutation, or cleanup failure follows the previously
    decided whole-draft discard/poisoning rules; no partial lifecycle change may
    later be committed. There is no implicit upsert and an empty feed is distinct
    from a deleted feed. Decision 35 later added `RenameFeed` as the fourth exact
    lifecycle operation; it does not replace or weaken these three.
33. User decision (2026-07-17): the final v4 format and public APIs remove the
    ambiguous `scope` vocabulary. The generic fixed-width range field is `value:
    u32`. In a direct file it is the opaque direct value; in a membership file it
    is a `membership_id` indexing the interned membership dictionary. A named
    catalog feed has a `feed_index`, and that index is its bit position in every
    membership bitmap. The non-shrinking logical bitmap-width high-water field is
    `feed_index_limit`; active feed count is distinct and comes from the catalog.
    Final APIs, specs, tests, and implementation names do not expose `scope`,
    `scope_id`, `scope_mode`, `ScopeIntern`, or equivalent legacy terminology.
    Historical audit evidence may quote those names only when identifying the
    current superseded implementation.
34. User decision (2026-07-17): the `u32` `membership_id` namespace represents
    nonempty membership combinations live in one committed snapshot, not every
    combination observed over the database lifetime. Each committed dictionary
    entry has a checked `u64` count of current range records referencing it.
    Mutations aggregate reference-count deltas with bounded memory; they do not
    scan the complete range tree on every commit or retain a file-sized in-memory
    count map. A combination whose count reaches zero is absent from the new
    dictionary, its pages follow normal reader-safe COW retirement, and its
    nonzero ID becomes reusable. ID zero remains permanently reserved for empty
    membership. Membership IDs are internal and snapshot-local; old readers use
    their pinned old dictionary and remain valid when a newer generation reuses
    an ID. Explicit validation recomputes and checks dictionary reference counts;
    open does not. Thus the database retains no unreachable membership history.
35. User decision (2026-07-17): `RenameFeed(old_name, new_name)` is an explicit
    catalog mutation for the same feed, not delete/create. It requires the old
    valid name to exist and the new valid name to be absent; typed precondition
    errors occur before mutation. A successful rename preserves the feed index
    and every membership, invalidates writer handles issued under the old catalog
    generation, and publishes atomically. It may share a pending transaction with
    an opaque JSON replacement. Because the engine does not parse that JSON, the
    caller is responsible for updating any application-level references to the
    old name before commit. Old pinned readers retain the old name; new readers
    see only the new name. Aliases are not supported: the catalog remains a
    one-to-one mapping between active names and feed indexes.
36. User decision (2026-07-17, narrowed 2026-07-21): the high-level named-feed
    workflow does not provide a general atomic multi-feed batch such as
    `ApplyFeedBatch`. One high-level feed lifecycle operation is one private
    transaction and committed generation. It may also stage the opaque JSON
    payload describing that feed change, but not a second high-level lifecycle
    operation. update-ipsets may prepare feeds concurrently, but successful v4
    feed replacements are serialized through one writer and committed
    independently. A failed feed keeps its previous committed membership and does
    not roll back feed updates already committed successfully; subsequent
    aggregate comparisons use a reader pinned after the selected feed commits
    finish. This constraint applies only to the safe high-level workflow. The
    advanced logical membership transaction selected by decision 47 may
    intentionally manipulate several feeds and membership combinations atomically;
    its existence does not change update-ipsets' feed-local commit behavior.
37. User correction and decision (2026-07-17): the retention API performs an
    exact full-snapshot delta, not a sequence of independent add/remove events.
    It is valid only for a direct file tagged `retention`, requires a clean writer,
    and stages one private draft. On the first refresh at caller-supplied value
    `t1`, every address in the complete downloaded set is inserted with value
    `t1`. On every later refresh at value `tN`, the API merge-joins the complete
    newly downloaded set with the complete committed retention map: addresses in
    both retain their existing value unchanged; addresses only in the new set are
    inserted with `tN`; and addresses only in the old map are deleted. Partial
    overlaps split ranges as needed, so this rule applies to every address, not
    merely to whole stored records. A refresh never changes the retained value of
    an address that remained present. An address removed by one committed refresh
    and appearing again in a later refresh is new and receives that later refresh's
    value. The result is committed atomically under the exact-migration failure and
    discard rules; no partial delta may be published.
38. User decision (2026-07-17): the supported distribution path is an explicit
    compact `SnapshotTo` operation. It pins one committed source generation and
    streams a new ordinary v4 file containing only that generation's reachable
    range trees, feed catalog, membership dictionary, and exact decompressed JSON
    payload. It does not copy unpublished growth, free pages, reader-protected
    retirement batches, unreachable pages, or deleted historical bytes; its free
    bitmap and retirement state begin canonically empty. The operation uses bounded
    working memory and may run while the source writer continues, with the source
    generation protected exactly like any other registered live reader. The output
    preserves the source `database_id`, transaction ID, static value kind/tag, feed
    indexes, and logical contents because it is a clean representation of the same
    committed generation. Internal membership IDs and physical tree/page layout may
    be rebuilt without semantic change. The destination has its own local operating-
    system file identity and no reader sidecar; converting it later for live
    mutation requires the explicit offline `InitializeLive` transition, while
    ordinary live open never creates coordination. A failed snapshot leaves the
    source untouched and never publishes a partial destination.
39. User decision (2026-07-17; Phase-2 intent superseded for this SOW by
    decision 45): compact distributable v4 snapshots support an
    embedded Ed25519ph signature with a publisher key ID. Live databases and
    private snapshots may be unsigned, but the update-ipsets public distribution
    policy requires signed snapshots. `SnapshotTo` first completes the compact
    output with zero signature storage, then performs the normative second
    sequential SHA-512/Ed25519ph message pass and backpatches the embedded signature
    without introducing a detached artifact.
    The signed content includes the format/static identity, `database_id`, transaction
    ID, complete committed range/catalog/dictionary state, and compressed JSON bytes;
    only the defined signature storage bytes are excluded to avoid circularity.
    Signature verification is an explicit full-file `VerifySignature` operation
    against caller-supplied trusted keys and is never performed implicitly by normal
    reader or writer open. Verification proves publisher authenticity and byte
    integrity but does not replace structural `Validate`; either may fail independently.
    Opening a signed snapshot for mutation is permitted, but the first successful
    mutation commit atomically publishes an unsigned generation rather than retaining
    a stale signature. The SDK owns trusted-key distribution and rotation and rejects
    replay by tracking the highest accepted transaction for each trusted database ID,
    subject to an explicit publisher-authorized database replacement workflow.
40. User decision (2026-07-17): the final wire identity is one exact format
    version, `v4`. The format does not expose or negotiate major/minor compatibility,
    and final readers and writers accept only the exact final v4 layout and its
    required static fields. All v4.0-v4.3 files, compatibility aliases, mixed-minor
    parsing paths, obsolete goldens, and migration/import paths are unreleased
    experimental state and must be removed rather than supported. A future
    incompatible on-disk layout is v5, not another interpretation of v4.
    Decision 45 clarifies timing: the current unsigned Phase-1 bytes are still
    unreleased experimental v4 and may be replaced—not supported in parallel—by
    Phase 2 before the first release. After that release, this v5 rule applies.
41. User decision (2026-07-17): every committed generation carries a
    cryptographically random nonzero 128-bit `commit_nonce`, generated for that
    exact attempt and returned with `CommitResult`. An `OutcomeUnknown` attempt
    is resolved as committed only by an exact
    `(database_id, txn_id, commit_nonce)` match in a retained bootstrap-valid
    meta followed by successful synchronization of the reopened main file. A
    lower selected transaction or the same
    transaction with a different nonce proves the attempt was not committed. If
    later commits have overwritten both possible meta copies and no exact match
    remains, resolution is `SupersededUnknown`; a later transaction ID alone
    must never be reported as proof that the caller's attempt committed.
42. User decision (2026-07-17; Phase-2 intent superseded for this SOW by
    decision 45): signed public publication uses one explicit fused
    `ValidateAndSignSnapshotTo` operation. It pins one exact source
    generation and combines complete source validation with bounded snapshot
    construction; it does not run a separate source-validation pass. Any source
    validation or build failure attempts to discard the private output; any
    cleanup failure must report exact residue truthfully. After signature
    backpatch, the exact isolated output is fully validated and its embedded
    signature verified before atomic publication. `update-ipsets` invokes this
    once after the selected independent feed commits, not once per feed. This
    operation does not make validation implicit for open, lookup, mutation,
    commit, or unsigned snapshot creation.
43. User decision (2026-07-17): before committed allocator metadata authorizes
    destructive page reuse, the writer verifies CRC and local structural
    invariants along the selected free-bitmap root-to-leaf path and target bit,
    and verifies every committed retirement-tree/blob page that releases a page
    through reclamation. Each committed allocator page is checked at most once
    per transaction, when it is first read into the transaction's existing COW
    state; subsequent allocations use that verified private state. This is not
    full `Validate` and adds no CRC work to lookup or scan. It detects ordinary
    torn/corrupt allocator metadata but cannot prove the global live/free
    partition against deliberately self-consistent corruption; callers needing
    that assurance explicitly invoke full validation.
44. User decision (2026-07-17): recovery exposes the newest and, when
    recovery-readable, previous retained meta as separately labeled candidates.
    Newest is the default and the only candidate allowed by `RecoverLive`.
    Previous-generation recovery requires an immutable copy or caller-certified
    `RecoverOffline` quiescence and an explicit candidate selection bound to the
    source identity, database ID, transaction, nonce, and meta page. Candidates
    have independent reports and outputs; the engine never merges them,
    silently rolls back, or chooses one by estimated recovered yield. Technical
    safety clarification from the final authority audit: `Newest` exists only
    when both physical metas prove generation order and the proven current meta
    is recovery-readable. A sole readable meta is never promoted over a damaged
    possibly-later meta. When order is unprovable, immutable/offline inspection
    may expose explicitly selected `UnorderedMeta0`/`UnorderedMeta1` candidates,
    with no default; `RecoverLive` returns a typed no-safe-current-candidate
    error.
45. User decision (2026-07-17): the core SDK must be implemented, proven
    reliable, and measured before snapshot signing is designed or implemented.
    Signing is Phase 2 and is not on SOW-0016's implementation critical path:
    Phase 1 contains no signing API, signing-key handling, signing-specific
    crypto dependency, signature page or root, signature verification,
    signed-state mutation, signing conformance case, or signing performance
    requirement. Phase 1 still uses SHA-256/SHA-512 for membership identity and
    crash-resolvable namespace publication. In particular, unsigned `SnapshotTo`
    performs one sequential full-output digest pass; this is neither signing nor
    validation, but its end-to-end cost is a mandatory Phase-1 benchmark. The Phase-1
    v4 bytes remain unreleased and may be revised by Phase 2 before any v4
    release; no compatibility is owed to Phase-1 experimental files. Decisions
    39 and 42 are retained only as Phase-2 product intent and do not authorize
    work in this SOW. For that later phase, the selected key boundary is option
    45A: a signing-provider interface that exposes the matching public key and
    performs the fixed signing operation, while the core engine never requires
    raw private-key bytes. Exact signing wire/API details must be reassessed from
    the proven SDK and measured performance rather than finalized now. The work
    is tracked by pending SOW-0017.
46. User decision (2026-07-20, corrected 2026-07-21): Phase 1 implements and
    proves the v4 format and its minimal format-facing SDK; it does not implement
    or freeze the detailed multi-file algebra API. Unordered-input normalization,
    canonical feed construction/replacement, and the specialized retention
    refresh are core Phase-1 responsibilities, not Phase-2 algebra. Phase 1 also
    performs a preliminary feasibility check so the format does not obstruct the
    intended Phase-2 API.
    The agreed high-level direction is:
    - a result feed is always a newly materialized/published v4 file, never an
      in-memory feed object; bounded streams and k-way merges are internal
      implementation techniques only;
    - set-producing operations include merge/union, intersection, and exclusion,
      and return terminal statistics together with the resulting v4 file;
      analytical operations such as comparison, equality, overlap, and counting
      return only counters when no useful result set exists;
    - a multi-feed result can preserve global named feeds or flatten coverage into
      one caller-named feed;
    - feed names are global logical identities across the supplied operands. If
      file A and file B both contain feed `Y`, enumeration treats `A.Y` and `B.Y`
      as one virtual `Y` by merging their ordered views directly. It does not
      create a physical temporary merged-Y file, and source bit indexes have no
      cross-file identity;
    - Phase 1 must prove that catalog enumeration/name lookup, ordered per-feed
      cursors, standard v4 construction/mutation/publication, exact cardinality,
      and explicit resource budgets are sufficient to implement that direction
      without an operation-specific page type, persisted derived statistics, or
      another file format.
    Exact operation signatures, arity, direct-value semantics, statistics schema,
    preserved-feed projection for intersection/exclusion, result database
    identity, batching, cancellation, and error precedence remain Phase-2 design
    work based on the measured core SDK. This deferred work must be tracked by a
    real pending SOW and must not delay Phase-1 format implementation.
47. User decision (2026-07-21): Phase 1 exposes two public semantic layers over
    one private transaction engine. The advanced layer is a mode-specific logical
    v4 map API: direct transactions manipulate semantic direct values and ranges;
    membership transactions manipulate the feed catalog, SDK-issued feed and
    membership references, and ranges, and may intentionally change several
    feeds atomically. Advanced membership callers may specify the semantic set of
    feed references covering a range, but the SDK exclusively constructs,
    interns, deduplicates, stores, translates, reference-counts, and reclaims the
    bitmap combination. Physical pages, roots, COW paths, membership IDs,
    dictionary hashes/refcounts, allocator/retirement state, and meta publication
    are never public. Feed indexes and bitmap words are exposed only as read-only
    observations bound to one pinned reader; callers cannot assign them, persist
    them as cross-file identity, or use them as mutation authority.

    The high-level layer provides single-use, operation-oriented workflows for
    named-feed creation/replacement, exact retention refresh, direct-map
    replacement, exact snapshot/copy, and multi-feed import. A live writer owns
    the writer lease and permits at most one active high- or low-level operation.
    High-level ingestion follows `Begin -> AddRanges` (repeated bounded batches)
    `-> FinishInput ->` optional JSON metadata staging `-> Commit` or `Abort`.
    Its single-feed `AddRanges` takes no value or membership argument.
    `FinishInput` declares that the complete input snapshot has arrived and
    returns final statistics before commit, allowing caller-created metadata to
    publish in the same generation when data changed. A `NoChange` result ends
    the workflow cleanly; a later metadata update is a separate metadata-only
    transaction. The engine accepts unordered, duplicate, and
    overlapping ranges and internally produces the canonical normalized tree in
    the same caller-facing operation.

    Exact logical copy uses `SnapshotTo`. Import into another membership database
    is a dedicated multi-feed operation: the SDK maps source feed names to
    destination feeds once, translates and caches each distinct source membership
    into a destination-owned membership, and streams the ordered address merge
    directly into the private final root. The caller never handles combinations,
    bitmap storage, or numeric IDs during this workflow. Both public layers obey
    the same whole-operation failure rule: every tentative change belongs only to
    that operation, and any input, normalization, storage, cancellation, or abort
    failure makes it uncommittable and prevents later partial publication.

    The advanced logical surface has one semantic vocabulary across bindings.
    Direct transactions assign a caller-semantic direct value to ordered range
    batches or clear ordered range batches. Membership transactions ensure,
    look up, enumerate, rename, and delete feeds through SDK-issued `FeedRef`
    values; incrementally construct an operation-bound `MembershipRef` from feed
    references; and apply that membership to ordered range batches with the five
    operations below. Go and Rust may use idiomatic spelling, while decision 58's
    C ABI manifest freezes exact C symbols; spelling does not create additional
    semantics or expose physical identifiers.

    Multi-feed membership import has one natural collision rule derived from the
    global-name identity decision: an exact existing feed-name match is the same
    logical feed and source coverage is unioned into it; a missing source feed is
    created; destination-only feeds and coverage remain unchanged. Source feed
    indexes and membership IDs are always translated internally. Incompatible
    family/value-kind/value-tag inputs fail before mutation. Destination JSON is
    retained unless the caller explicitly uses the transaction's one metadata
    replacement/clear stage. Direct-map import into an existing direct map is not
    a Phase-1 high-level operation because conflicting opaque values have no
    generic merge meaning; exact direct copy uses `SnapshotTo`, direct replacement
    uses its dedicated workflow, and advanced callers assign values explicitly.

    The advanced membership range primitive applies one destination-operation-
    bound `MembershipRef` `M` to the current membership `S` with an explicit
    operation: `Replace` (`M`), `Union` (`S ∪ M`), `Difference` (`S − M`),
    `Intersection` (`S ∩ M`), or `Xor` (`S △ M`). The rule is applied per
    address across every supplied range; an empty result removes coverage. The
    SDK performs all overlap splitting, bitmap operations, canonicalization,
    interning, refcount maintenance, and adjacent coalescing. Callers supply only
    SDK-issued feed/membership references and never a bitmap, bit position, or
    membership ID. This complete algebra belongs only to the advanced logical
    layer; high-level single-feed `AddRanges` remains value-free.

    Generic direct-range input has one fixed sequential-assignment rule, not a
    selectable conflict policy. Every supplied range is applied exactly in the
    order received, including order within and across `AddRanges` batches. A later
    range overwrites only the addresses inside its own inclusive interval;
    earlier values remain on both uncovered sides. Splitting and adjacent
    equal-value coalescing occur after each logical assignment or with an exactly
    equivalent order-preserving implementation. Internal batching or locality
    optimization MUST NOT reorder conflicting assignments or change the
    per-address result. A source failure aborts the whole private operation, so no
    applied prefix can later be committed.
    Normal feed/direct/retention ingestion, metadata staging, transaction
    bookkeeping, commit, and abort MUST create no external scratch or sorting
    files. They normalize and maintain allocator/refcount state directly in
    operation-private COW pages inside the destination database. Whole-file
    creation, snapshot, compaction, recovery, and later set-producing operations
    MAY build the actual final inode under a private destination name before
    atomic publication; that inode is the output, not sorting scratch. Only
    explicit `Validate` and recovery graph-safety work MAY use separate bounded
    scratch files, and only under a caller-supplied nonzero temporary-storage
    budget. With zero temporary-storage budget they either complete within the
    heap budget or return a typed insufficient-budget error before exceeding it.
    Snapshot construction and ordered multi-file algebra do not receive sorting
    spill permission merely because they publish a new output.
48. User decision (2026-07-21): the Phase-1 matrix includes FreeBSD 14,
    while section 20.1 requires atomic no-replace publication of a prepared
    private inode. The official
    [FreeBSD 14.3 `renameat(2)` manual](https://man.freebsd.org/cgi/man.cgi?query=renameat&sektion=2&manpath=FreeBSD+14.3-RELEASE)
    has no `renameat2`/`AT_RENAME_NOREPLACE`; the official
    [FreeBSD 14.3 `linkat(2)` manual](https://man.freebsd.org/cgi/man.cgi?query=linkat&sektion=2&manpath=FreeBSD+14.3-RELEASE)
    defines atomic destination-if-absent hard-link creation with `EEXIST`.
    Use the portable POSIX fallback:
    `linkat(private,canonical)`, synchronize the directory, unlink the private
    alias, then synchronize/recheck again. The only legal link-count-two crash
    state has the exact attempt-derived private and canonical names resolving to
    the same recorded inode under the attempt lock; resolvers collapse that
    state. Reservation acquisition and main publication do not report a ready or
    `Published` primary outcome until the private alias is removed, the second
    directory sync succeeds, link count is one, and identities are rechecked;
    an interruption remains resolvable at the namespace boundary. Every ready
    main/reservation/sidecar requires link count one. This is the normative
    FreeBSD-14 no-replace publication path; implementations and crash tests must
    cover the temporary two-name/same-inode state and its exact cleanup.
49. User decision (2026-07-21): destructors/finalizers never begin a slot or
    lease transition. Explicit `Close`/guard cleanup is mandatory and remains
    non-consuming on failure so the caller can retry with the required
    in-process provenance. Dropping an unclosed handle leaves its valid active
    claim fail-closed until process exit or caller-certified offline reset.
    Dropping a cleanup-only handle/guard after explicit cleanup failed in armed
    state 2 abandons that provenance and requires caller-certified offline
    reset; C-ABI destroy refuses that state. Phase 1 has no process-global hidden
    cleanup registry. This is the surgical Phase-1 choice: safe and simple, with
    the explicit risk that forgotten or abandoned cleanup can block more live
    use.
50. User decision (2026-07-21): resolution reports independent facts rather
    than a compound enum for every combination. Current destination content is
    classified independently from any later valid reservation/live owner and,
    when safely knowable, live lineage. Legal later reuse proves only that the
    old reservation retired; it is reported and never displaced, modified, or
    claimed as authority for old-attempt cleanup. Active or uncertain later live
    ownership returns `WriterBusy` before physical inspection. A later owner in
    another process domain is reported as unavailable without inspecting slots,
    hashing the main file, or mutating anything. `ResolvePublication`,
    `ResolveCreateLive`, and `ResolveLiveTransition` share this orthogonal
    reporting contract while retaining operation-specific primary outcomes.
    This preserves transparent reporting without a brittle cross-product of
    Go/Rust/C result codes.
    The user also clarified that mechanical representation choices at this
    level are not product decisions to escalate. Future questions must be
    limited to genuine alternatives that materially change public behavior,
    performance, recovery/data-loss risk, compatibility, or caller
    responsibility. Dominated implementation representations must be resolved
    from the established requirements and documented without consuming a user
    decision round-trip.
51. User decision (2026-07-21): `Reclaim(max_batches,max_pages)` is one explicit,
    clean-writer, auto-publishing maintenance operation. It uses the writer's
    existing transaction resource budget; both work limits are nonzero, and it
    selects only complete oldest eligible batches fitting both limits. If the
    oldest eligible batch alone exceeds `max_pages`, it returns typed
    `WorkLimitTooSmall` with the required count before mutation; after selecting
    one or more batches, it stops before the next oversized batch. Reader-safety
    selection, the bounded second pass, COW finalization, and publication occur
    while holding the operation lock, and the internal commit path must use that
    already-owned lock rather than reacquire it. The result is `NoChange`, which
    performs no commit, or reclaimed batch/page counters plus the exact
    `CommitResult`. Reclamation is not staged into a caller data transaction and
    is not restricted to being a side effect of later data commits.
52. User decision (2026-07-21): a clean writer may perform a metadata-only
    commit, and an ordinary pending transaction may stage at most one
    `SetMetadataJSON` or `ClearMetadataJSON` alongside its range/feed mutations.
    Exact bulk/lifecycle drafts that finish with `Changed` retain one optional
    post-operation metadata stage. `FinishInput(NoChange)` terminates that
    workflow cleanly; a later metadata update is a separate metadata-only
    transaction. A second metadata stage is rejected before mutation rather than using
    last-call-wins behavior. `SetMetadataJSON` is an explicit replacement and
    always stages the supplied payload, even when it equals the committed
    decompressed bytes; the SDK does not implicitly read, decompress, and compare
    the old payload. Clearing already-absent metadata is an O(1) `NoChange`: it
    starts no clean-writer transaction and changes no existing draft root.
    Changed metadata follows normal whole-transaction failure and cleanup rules.
53. Derived requirement (2026-07-21) from user decisions 14-17: validation modes
    are explicit.
    `LiveCurrent` uses the recovery-style transaction-zero registration under the
    operation lock before bootstrap selection, then updates it to the selected
    transaction for a full graph scan. `ImmutableCurrent` holds the shared
    lifetime lock and requires sidecar absence. `OfflineCandidate` holds the
    exclusive lifetime lock under caller-certified quiescence and may bind an
    exact recovery-candidate token, including a previous or unordered candidate.
    When current bootstrap is inspectable but no generation can be selected,
    immutable/offline-current validation returns a completed bootstrap-only
    invalid report: generation/roots are absent, the graph is untraversable, and
    unknown coverage is explicit. `LiveCurrent` may return that report only when
    trustworthy static main/sidecar OS identities bind the sidecar and it holds
    the operation lock continuously through bootstrap inspection and cleanup of
    its temporary transaction-zero claim; it never claims that an unselectable
    generation was pinned. Ambiguous live identity/current-generation binding or
    failure to take the required lock is an operational coordination error. This
    preserves detailed bootstrap/meta findings and reports the extent that is
    actually knowable, including corruption that prevents selecting a normal
    generation. Restricting validation to successfully selected generations
    would contradict decisions 14-15 by hiding the first and most important class
    of corruption and its bounded extent; it is therefore not a genuine
    unresolved product choice.
    **Superseded coordination detail (2026-07-25):** decision 21's later
    open-description-lock simplification removes transaction-zero slot state.
    `LiveCurrent` holds the gate exclusively while it scans slots, selects the
    proven current meta, and directly claims that exact nonzero transaction. A
    bootstrap-only invalid report keeps the gate through inspection and
    publishes no slot. The reporting behavior above is unchanged.

54. User decision (2026-07-21): the Windows contract can durably rename an
    exact owned artifact away from a correctness-authoritative name on local NTFS,
    but the reviewed Microsoft contracts do not prove that a subsequent
    close-triggered deletion of the last housekeeping name is crash-durable. The
    current generic definition would therefore make successful Windows cleanup
    remain `ResiduePossible` forever, or else falsely claim a stronger guarantee
    than the OS documents.
    **54A (selected, long-term-best):** separate correctness cleanup from
    housekeeping for every private output, spill, reservation, coordination file,
    and other cleanup-obligated Windows artifact. Using retained write-through
    handles, atomically rename the exact owned artifact from every authoritative
    name to a distinct attempt-bound inert GC name and durably resolve any
    two-name rename ambiguity before declaring correctness cleanup complete. An
    indeterminate rename remains a correctness-level namespace-transition record
    containing both exact names and one identity until resolved. Once proved, the
    GC name is never canonical publication/live authority or a blocker, but it
    remains authenticated cleanup/housekeeping evidence for that exact old
    attempt; it is never guessed by age. Every cleanup-obligated artifact is
    created with, or durably paired to, a fixed GC authority envelope containing
    two independently selectable sequence/CRC-protected header copies that bind
    attempt ID, artifact kind, authoritative and GC basename commitments, local
    identity, and payload identity. One torn header rewrite cannot become
    deletion authority. At least one selectable header survives any safe payload
    truncation; if neither copy selects, automatic removal is forbidden and the
    artifact is only reported for explicit offline inspection. Then attempt
    POSIX-style deletion best-effort. Report orthogonal factual housekeeping as
    `None | CrashReappearancePossible | Visible`, with a bounded list for visible
    artifacts. Visible GC names remain charged to heap/file/temp-byte budgets;
    a proved-currently-absent name may reduce to `CrashReappearancePossible` and
    is rediscovered by streamed offline enumeration if it reappears after crash.
    Every destination/temp directory that may own one is independently required
    to be same-volume local NTFS. Expose exact identity-bound housekeeping removal
    but make no power-loss guarantee for final unlink. `Clean` means no
    correctness/retry-blocking obligation, not a directory guaranteed free of
    inert names. Risks include crash-left names, quota use, and confidential old
    ranges/JSON when authenticated payload truncation itself does not complete.
    **54B (rejected, platform-reducing):** make Windows mutation, reclamation,
    live transition, recovery publication, and snapshot publication unsupported;
    allow only immutable reads/validation. This preserves the stronger cleanup
    meaning but removes the promised Windows SDK capability.
    Process clarification (2026-07-21): a mechanical SOW edit/context failure is
    not an approved implementation-plan failure. Re-read the exact text, retry
    the intended documentation-only edit, and continue. Escalate only a genuine
    plan, design, scope, safety, or implementation failure.
55. Derived requirement (2026-07-21): `SnapshotTo` and recovery publication need
    an explicit destination overwrite contract. The current draft already
    selected policy without user authority and did not say whether replacement
    may repair corrupt/non-v4 bytes.
    **55A (selected, long-term-best):** `SnapshotTo` requires explicit
    `FailIfExists` or `ReplaceExisting`; convenience defaults to `FailIfExists`
    and never switches policy. Recovery is forensic and supports only
    `FailIfExists`, so it never overwrites existing evidence or output.
    `FailIfExists` requires absent main and sidecar. Snapshot
    `ReplaceExisting` requires an existing no-follow regular,
    link-count-one, sidecar-free destination under the caller's explicit authority,
    but need not require valid v4 bytes; this permits atomic repair of a corrupt
    artifact and deliberate replacement of another regular file. Under
    caller-certified absence of non-cooperating modifiers, record its identity,
    exact byte length, and stable digest before replacement; synchronize/read it
    successfully before the state-2 ambiguity boundary. The sole same-inode exception
    is `SnapshotTo(immutable_path, same_path, ReplaceExisting)` for compaction;
    live snapshot sources, every recovery mode, and `FailIfExists` reject
    same-inode source/destination.
    This policy is deliberately destructive and therefore never inferred/default.
    **55B (rejected):** both operations support only `FailIfExists`; callers
    replace externally, losing engine-resolved replacement and compaction.
    **55C (rejected):** use 55A snapshot behavior and also let recovery explicitly
    `ReplaceExisting` at a distinct sidecar-free destination. This is convenient
    repair publication but can destroy pre-existing forensic evidence.
56. Derived requirement (2026-07-21) from decisions 36 and 47: several writer
    feed handles from one base
    currently may mutate one ordinary draft, creating an implicit multi-feed
    atomic batch despite decision 36 and update-ipsets' feed-local commit model.
    **56A (rejected as contradicting decision 47):** the first logically changing
    `AddFeedRange`/`RemoveFeedRange` binds an ordinary membership transaction to
    that one feed name/index. Further incremental calls may use only that feed
    until commit/abort; once bound, any call through another feed rejects before
    even evaluating whether it would be a no-op. A no-op on a clean
    writer does not bind/start a transaction, and a metadata-only draft may bind
    its first changing feed later. Lookup stays non-mutating, so callers may hold
    several handles but change only one feed per generation. Commit/abort
    invalidates every handle from that writer base, not only the bound feed.
    **56B (selected):** allow several handles
    from the same base to change one atomic draft. This creates a real multi-feed
    incremental transaction whose feeds succeed/abort together; update-ipsets
    does not currently need it.
57. Derived requirement (2026-07-21): `CreateLive(path, parameters, capacity)`
    leaves parameters, resource-budget ownership, and returned-handle behavior
    undefined.
    **57A (selected, long-term-best):** expose
    `CreateLive(path, family, value_kind, value_tag, reader_capacity)`, where the
    tag is zero through 15 non-NUL bytes and capacity is nonzero. Create only the
    canonical empty transaction-1 live pair: fresh database ID/nonce,
    `page_count == 2`, identical metas, absent metadata, no range/free/retirement
    roots, all logical counts/roots zero, and for membership only
    `membership_id_limit == 1`; synchronize the empty reader table. Accept no
    writer budget, feeds, ranges, or metadata and return only `CreateResult`.
    Validate family/kind/tag/capacity and checked sidecar-size/host limits before
    any reservation creation. `CreateResult` never carries a lease; even
    `Created` plus cleanup/housekeeping residue is followed by explicit
    `OpenLiveWriter` with its
    normal budget. One extra open only at creation keeps publication cleanup and
    writer-lease ownership separate.
    **57B (rejected):** take a writer budget and return both `CreateResult` and an already
    open writer on `Created`, saving one open but coupling creation publication,
    reservation cleanup, writer claim, and two fallible ownership lifecycles.
58. Derived requirement (2026-07-21): Phase 1 promises a stable Rust-provided C
    ABI, but no exact versioning, path/IP encoding, error ownership, callback,
    struct-evolution, null, close/destroy, cardinality layout, or panic rule
    exists.
    **58A (selected, long-term-best):** define ABI generation 1 under the
    exclusive coexistence-safe `iprange_v4_abi1_` symbol prefix and expose a
    fixed `uint32_t iprange_v4_abi1_version(void)` returning 1. The generated
    header defines export and `__cdecl`/platform call macros. Use
    opaque handles for stateful objects/variable results; fixed-width numeric
    statuses and structs; and `abi_version`, `struct_size`, reserved-zero fields
    on extensible structures. Inputs use checked pointer-plus-`uint64_t` length
    slices (null only at zero); IPs use family plus 4/16 network-order bytes;
    paths use raw POSIX bytes or explicit well-formed Windows UTF-16 units, never
    locale strings. Fallible calls return stable numeric status and may return an
    owned opaque typed error; never use thread-local last error. Memory crosses
    allocators only through opaque values with matching destroy; bulk output uses
    caller buffers or decision 60's sink. Callbacks receive borrowed values with
    exact continue/stop/error semantics and cannot reenter the same handle.
    Explicit close follows decisions 49/59; close/destroy returns status and an
    unresolved handle is not freed. Validate every pointer/alignment/length;
    build the boundary with unwind enabled, catch unwind panics, and map them
    without crossing C. Allocator abort/OOM is documented as non-recoverable;
    callbacks cannot `longjmp`, throw, or unwind and report failure only through
    their return contract. `Cardinality129` has the fixed layout
    `{ uint8_t bit128; uint8_t reserved[7]; uint64_t hi; uint64_t lo; }`, with
    offsets 0/8/16, total size 24, `bit128` restricted to 0 or 1, and all
    reserved bytes zero.
    Generate/check one C/C++ header, freeze a
    symbol/layout/numeric-code manifest, and compile/link native C coverage.
    **58B (rejected, unstable):** mirror native Rust types, use
    `size_t`/NUL strings and thread-local last error, and regenerate as Rust
    evolves. This is initially smaller but is not a credible stable ABI.
    **58C (rejected):** defer C ABI to a separate SOW, removing it from Phase-1 acceptance.
59. Derived requirement (2026-07-21): `Abort` is named as a legal transaction
    terminator, but neither its result nor `Close` on a healthy pending writer is
    defined.
    **59A (selected, long-term-best):** `Abort()` is explicit/non-consuming.
    A clean writer returns `NoPendingTransaction`. An active but empty advanced
    operation returns clean `Aborted` and terminates its stored cancellation and
    child authority. A pending/private exact draft
    immediately invalidates every transaction/feed handle, discards unpublished
    COW/private-page state, and proves the committed generation/tail. `Aborted` is
    factual once rollback is proved. `Aborted + Clean` keeps the existing lease
    and returns the writer clean/reusable. `Aborted` plus independent exact
    unpublished-tail residue reports that residue orthogonally and makes the writer
    close-only until `Close` clears the lease and transfers or returns every
    remaining unpublished same-file tail ledger. Only unresolved main-generation/tail proof is `AbortIncomplete`
    and retains an abort/close-only writer for retry. Explicit
    `Close()` on a healthy pending writer runs the
    same abort protocol, then clears the lease—never commits—and is non-consuming
    on either failure. If abort completed but lease clear failed, the result says
    abort is complete and retains a close-only writer with
    `RetainedWriterCloseRequired`. `OutcomeUnknown` commit rejects Abort and uses
    only meta-aware Close/Resolve. Decision-49 destructors/finalizers never start
    this work.
    **59B (rejected):** `Close()` returns `PendingTransaction`
    unchanged; the caller must successfully `Abort()` first. The same explicit
    abort state machine is still required.
60. Derived requirement (2026-07-21): spill-backed operations currently let each
    implementation choose pushed callbacks or caller-owned pull streams, while
    reader feed handles/materialization are optional. This creates different
    Go/Rust/C APIs and residue ownership.
    **60A (selected, long-term-best):** one synchronous bounded sink is the
    mandatory cross-language core for Phase-1 normalization, validation findings,
    recovery candidate reports/unknown envelopes, and other streamed outputs.
    Future Phase-2 set algebra/overlap must reuse the same contract rather than
    invent another streaming model. The core is batched, not one callback per range: the engine lends a
    bounded record buffer to one synchronous pull source, which returns
    `Batch(nonzero_count) | End | Error`; output is delivered as a nonempty
    borrowed batch to a synchronous sink returning
    `Continue | Stop | Error`. Batch capacity is bounded by the operation budget
    and the ABI contract, records are never retained, immediate End means legal
    empty input, and source errors propagate exactly and abort the private draft.
    Go/Rust use a sink interface/closure and C uses the decision-58 callback;
    borrowed output batches expire on sink return. Single-record adapters are
    native conveniences, not the mandatory ABI path. `Stop` returns distinct `StoppedBySink`:
    read-only enumeration may report a truthful partial result, while validation,
    recovery, snapshot/build, and mutation remain incomplete and cannot publish.
    `Error` returns `SinkFailed` plus the caller cause. Native callback panic is
    contained by the same cleanup path. The engine checks cancellation before a
    source/sink invocation; once invoked, that callback's returned outcome wins
    for that invocation and cancellation is observed at the next checkpoint.
    The one terminal `RecoveryResult` is returned normally rather than streamed.
    Cleanup/ledger is complete before return. No public iterator owns authorized scratch.
    Each logical `AddRanges` or advanced range call drains one finite source to
    End; repeated calls concatenate those sources in exact batch/record order,
    and only `FinishInput` ends the complete workflow. Native slice/iterator
    forms are adapters over that same core.
    Feed enumeration/name lookup returning
    `{name,index}` and lazy `MembershipView` are mandatory. Opaque reader feed
    handles, heap materialization, and native iterators are non-core convenience
    wrappers only when they preserve the same ownership/error contract.
    **60B (rejected):** require a pull-stream handle in all APIs. This is natural for
    backpressure but early stop requires `Finish`/`Cancel`, failures transfer
    spill cleanup, and the C ABI gains another cleanup-only handle state machine.
61. Derived Phase-1 scope requirement (2026-07-21): exact basename binding correctly makes a
    moved live main/sidecar pair fail ordinary open, but the draft vaguely says
    live rename/relink is an offline transition without defining an API, durable
    record, result, or resolver.
    **61A (selected, surgical Phase-1):** direct live rename/relink is not
    supported. To relocate, snapshot to a new immutable path, explicitly
    `InitializeLive` there, and switch the application. Phase 1 provides no
    engine-owned old-pair deletion/retirement API or crash guarantee; after
    certified quiescence, external deployment tooling may remove the old pair at
    its own operational risk. Remove the vague promise; ordinary open and reset
    never silently rebind a basename commitment.
    **61B (rejected from Phase 1):** design a dedicated multi-name live-relocate
    transition with its own record/result/resolver and cross-directory durability
    rules now. This materially expands Phase 1 and is not required by update-ipsets.

62. Derived requirement (2026-07-21): the draft describes immutable-reader,
    live-reader, and live-writer open algorithms but never defines their public
    constructors or parameters. Auto-detection can be made race-safe with the
    lifetime lock, but it hides caller intent and produces more complex errors.
    **62A (selected, long-term-best):** expose three explicit constructors:
    `OpenImmutableReader(path)`, `OpenLiveReader(path)`, and
    `OpenLiveWriter(path, transaction_resource_budget)`. Immutable open requires
    sidecar absence and exact immutable file length; live reader discovers the
    fixed capacity from the bound sidecar and registers; live writer requires the
    configured transaction budget at open and claims the dedicated lease. None
    performs general validation—only constant-time bootstrap/bounds safety plus
    the mandatory live coordination scan. Query and validation operations take
    their own explicit per-operation budget; only explicit validation/recovery
    graph-safety work may receive external scratch authority. Snapshot creates no
    external scratch and is
    path-based and requires explicit `Immutable | Live` source mode plus
    destination policy and operation budget; it does not borrow an already-open
    reader whose registration/lifetime cannot be released before destination
    locking. No constructor
    auto-creates, repairs, resets, initializes, or switches mode.
    **62B (rejected):** expose one auto-detecting `OpenReader(path)` that selects live mode
    when a sidecar is present and immutable mode otherwise, plus a writer open.
    This is more convenient, and can be made safe only by taking the same lifetime
    lock before the sidecar decision, but it hides whether sidecar absence is an
    expected immutable contract or a deployment mistake.

63. Derived performance requirement (2026-07-21): section 15.4 refers to a per-handle
    serialization contract that does not exist. This affects hot-path overhead,
    Close races, Rust `Send`/`Sync`, Go safety, and C callers.
    **63A (selected, performance-first long-term-best):** Reader point lookups
    and independent scans may run concurrently with no per-lookup active counter,
    mutex, or atomic. The caller must ensure Reader `Close` does not race any
    Reader call; Rust ownership enforces this where possible, while Go/C make it
    an explicit lifetime rule. Persistent children such as cursors and lazy
    membership views take a parent borrow at creation and release it at child
    Close/destroy; parent Close returns `HandleBusy` while a child exists and
    admits no new child once Close begins. Writer feed handles and every result/
    residue view that borrows a parent follow the same rule. Each Writer, cursor,
    membership view, feed handle, cleanup/residue handle, and mutable result
    handle is otherwise caller-serialized and protected at the FFI boundary by a
    fail-fast non-reentrant gate returning `HandleBusy` before mutation; it never
    silently waits. Different handles may run concurrently subject to database
    locks. Sink/source callbacks cannot reenter the originating handle. This
    keeps the point-lookup hot path free of synchronization, with the explicit
    implication that racing Reader Close is caller misuse in Go/C.
    **63B (rejected for Phase 1):** require explicit cloned Reader leases. Each clone retains the shared
    mapping/registration and may serve concurrent lookups; children retain a
    clone. Closing one clone is race-safe with work through another, but the same
    clone still cannot race Close. This adds atomic/refcount work only at clone/
    child lifecycle boundaries and a larger reader API, not per lookup.
    **63C (rejected for hot-path cost):** internally gate every Reader call so Close racing any operation is
    reported safely. This is easiest for Go/C callers but adds atomic/cache-line
    contention to every point lookup and warmed cursor step; its cost must not be
    accepted without benchmarks.
64. Derived security requirement (2026-07-21): creation/publication never defines file
    permission or ACL behavior. Atomic replacement publishes a new inode and can
    silently change access. The current update-ipsets consumer deliberately uses
    mode `0600`, directories `0700`, and service `UMask=0077`, so OS defaults are
    not an acceptable unstated policy.
    **64A (selected, surgical/security-first):** every engine-created main,
    sidecar, private output, reservation, spill, and Windows GC artifact is fixed
    CreatorOnly; never rely on or mutate the process umask. POSIX effective mode
    is exactly `0600`, inherited extended ACL grants are neutralized, and the
    retained descriptor is verified/synchronized before publication. Windows
    uses a protected non-inheriting DACL for the current token user SID (ordinary
    OS privilege bypass remains OS policy), verified before publication. The
    creator principal is captured at attempt start—POSIX effective UID, and the
    Windows effective security token used for creation, including impersonation
    when present—and never redefined by the principal running a later resolver.
    The reservation/result durably binds a commitment to the exact normalized
    CreatorOnly security state applied to every prepared inode. The fixed policy
    must be applied, verified, and synchronized before the canonical boundary;
    failure to prove it returns `AccessPolicyUnsupported` before that boundary.
    Resolution reports current access independently as
    `CreatorOnly | ChangedOrUnproven`. A legitimate post-`Published` chmod,
    chown, or ACL change never changes exact byte-content classification, never
    permits republishing, and does not prevent safe cleanup of exact old
    artifacts; a resolver never silently restores the original permissions.
    Ordinary opens, existing inputs, and files passed to `InitializeLive` never
    enforce or mutate CreatorOnly. `ReplaceExisting` deliberately does not copy prior owner/group/
    mode/ACL/xattrs/security descriptor: the replacement becomes CreatorOnly.
    Applications widen/chown only after factual `Published` when needed. This
    exactly matches update-ipsets' current `0600`/`UMask=0077`, keeps internal
    coordination private, and has no access-policy API. The implications are
    that replacing a previously shared artifact makes it private; both live main
    and sidecar are usable only by the creating principal unless the application
    widens both after publication; and initializing a shared pre-existing main
    creates a private sidecar, so other principals cannot participate until the
    application deliberately changes access.
    **64B (rejected for Phase 1 as unnecessary surface):** internal coordination/
    scratch remains CreatorOnly, but creation/publication accepts explicit final
    main and sidecar access policies, with platform-specific mode/ACL validation,
    pre-boundary application, and replacement-preservation rules. This supports
    cross-principal live readers directly but substantially expands the Go/Rust/C
    security API and cross-platform tests.
    **64C (rejected as insecure):** inherit OS/directory defaults everywhere. This is simplest but can
    expose JSON/ranges under a permissive umask/ACL and can change replacement
    access without an explicit caller decision.
65. Derived requirement (2026-07-21): metadata write semantics are defined, but
    the receiver of `GetMetadataJSON` and a writer's pending view are not.
    **65A (selected, long-term-best):** Reader Get returns the exact optional
    bytes from its pinned generation. Writer Get is explicit read-your-writes:
    staged Set returns the new exact bytes, staged Clear returns absent, otherwise
    it returns the writer's committed base generation. The stable core is a simple
    two-call contract: query presence/exact decompressed length, then fill one
    caller buffer; too-small storage returns required size with no partial bytes.
    Go/Rust may provide a bounded allocation convenience because the object is at
    most 1 MiB. No parsing or streamed chunk protocol is needed. A failed read
    never changes the draft.
    **65B (rejected):** Writer Get always reads only the committed base, even after staging.
    This avoids a staged view but is surprising and can make publisher metadata
    verification read the wrong object.
    **65C (rejected):** expose Get only on Reader; callers must commit/open a reader to verify.
66. Derived operational requirement (2026-07-21): potentially long normalization, lifecycle,
    validation, recovery, and snapshot operations have no cancellation
    API. Sink Stop cannot help while an operation is scanning/sorting without
    emitting output, and update-ipsets already relies on context cancellation.
    **66A (selected, long-term-best):** every potentially long operation takes
    an explicit cancellation probe/token—idiomatic Go context and Rust/C adapters
    with identical semantics and bounded documented work checkpoints. This
    includes O(reader-capacity) live opens/CreateLive and interruptible blocking
    lock acquisition as well as normalization, lifecycle, validation, recovery,
    and snapshot work. Future Phase-2 overlap/algebra inherits the same contract.
    A token bounds engine work between checkpoints;
    it cannot promise wall-clock interruption while an OS call such as fsync is
    executing. Cancellation
    before mutation/publication aborts and cleans with typed `Cancelled`; after
    pending mutation it uses whole-draft abort. The implementation checks once
    more before a commit/publication ambiguity boundary. After that boundary it
    must ignore the already-signaled token while it finishes/resolves the
    boundary and return the factual durability/publication outcome with
    cancellation only as a cause; it never disguises `Committed`, `Published`, or
    `OutcomeUnknown` as `Cancelled` and never abandons cleanup authority.
    **66B (rejected):** omit cancellation in Phase 1. Calls may remain uninterruptible through
    long scans, spill merges, validation, recovery, and snapshot construction.

67. User decision (2026-07-21): decision 47 requires final workflow statistics,
    and the fields become a stable Rust/Go/C SDK contract. All options compute
    counters during the already-required normalization/merge and add no file pass.
    **67A (selected, long-term-best):** one fixed full semantic report with
    workflow/change; input/before/after record counts; exact 129-bit input/before/
    after/unchanged-value/changed-value/added/removed address counts; and import-
    only source/matched/created feed plus source/translated-membership counts.
    This gives publishers complete delta observability with one report shape, at
    the cost of a larger permanent API and zero-valued fields outside import.
    **67B (compact):** keep only workflow/change, input/before/after record and
    address counts, and added/removed addresses. This is smaller, but direct-value
    replacements can change values without changing coverage and the report will
    not quantify that change; import translation behavior is also invisible.
    **67C (operation-specific):** return separate feed, direct/retention, and
    import report shapes. This avoids irrelevant zero fields, but creates more
    public structs/getters and cross-language branching for little runtime gain.
68. Derived memory-safety requirement (2026-07-21): ordinary live access does
    not run `Validate`, while the committed free bitmap may authorize immediate
    overwrite of a page. Plausible self-consistent corruption can make a selected
    tree pointer name that same page. The existing requirement to expose
    ordinary Rust references into a whole selected mapping would then permit a
    concurrent writer to mutate bytes behind a shared reference, which is
    undefined behavior rather than a typed corruption result.
    **68A (selected, long-term-best):** ordinary Rust and Go live readers use
    fixed cursor-/caller-owned page buffers with positional reads. A raw mapping
    is only a later internal optimization if it can copy without creating a
    language-level shared reference to concurrently mutable file bytes. This
    keeps open constant-time, memory bounded, validation optional, and malformed
    content memory-safe. The path is benchmarked before acceptance.
    **68B (rejected as unsafe):** expose page-specific references from a raw
    whole-file mapping based only on ordinary root traversal. Without the full
    graph/allocation proof that the user explicitly made opt-in, a corrupt child
    pointer can reach a concurrently reusable page.
    **68C (rejected):** run full validation before every live open. This violates
    the explicit no-default-validation decision and makes open proportional to
    database size.
69. User decision (2026-07-23): exact-scope sealed cleanup must prove the
    complete ordered global/scope AVL deletion sequence before authoritative
    mutation without scanning unrelated foreign/global membership. The current
    cleanup result carries no scratch large enough for a target-local evolving
    tree overlay.
    **69A (selected, long-term-best):** extend the private, unreleased cleanup
    lifecycle with caller-owned, transaction-budgeted overlay scratch sized to
    the checked `O(k log N)` worst case for `k` target pages. Preparation seals
    capacity and rejects aliases before use; preflight simulates the exact later
    delete order against the evolving overlay; apply is deterministic,
    allocation-free, lookup-free, and mechanically infallible. No permanent
    bytes are added to every pool slot.
    **69B (rejected):** add intrusive shadow AVL fields to every private-pool
    slot. This preserves the current cleanup signature but permanently adds
    substantial per-slot memory and validation surface for scratch touched only
    during terminal cleanup.
    **69C (rejected):** perform fallible live deletion and roll back on detected
    corruption. Checkpoint start and rollback change epochs, mutation counters,
    and historical checkpoint scratch, so this does not satisfy the exact
    pre-mutation/byte-for-byte atomicity contract.

## Plan

1. Consolidate SOW/spec authority and remove obsolete-format artifacts and
   references.
2. Lock the exact unsigned Phase-1 v4 normative byte and public semantic contract.
3. Implement wire/static identity, 129-bit cardinality, and bounded safe open.
4. Implement safe range traversal, hierarchical allocator, retirement, and strict
   live reader coordination.
5. Implement transaction abort/poisoning and structured commit durability.
6. Implement membership dictionary, structured feed catalog/lifecycle/handles,
   and opaque compressed JSON.
7. Implement bounded same-file normalization, exact high-level workflows,
   name-based membership import, primitive queries, and exact retention refresh.
8. Implement explicit validation, detailed recovery, and compact unsigned
   snapshots. Signing remains entirely in pending SOW-0017.
9. Add the stable Rust-provided C ABI and native C integration tests after the
   Rust Phase-1 API is proven.
10. Regenerate and genuinely cross-open semantic conformance artifacts; close all
   correctness, fault, resource, and scaling tests in both languages.
11. Update all durable artifacts, run final same-failure/sensitive-data/SOW gates,
    and close implementation plus SOW lifecycle in one commit.

The detailed dependencies, test-first rule, files/surfaces, and validation
requirements are normative in the Pre-Implementation Gate above.

## Execution Log

### 2026-07-14

- Independently revalidated commit `20a15fc`: normal Go/Rust suites and Go race
  suite pass.
- Measured Go statement coverage at 79.6% and identified untested public paths.
- Completed initial control-flow audit and created this test-only SOW.
- Added 26 Go tests, 25 Rust tests, and mirrored all-to-all scaling benchmarks.
  No Go or Rust implementation source was changed.
- Added exact inline/overflow boundaries at 256, 257, 4,076, 4,077, and
  130,433 bytes. The 130,433-byte case deterministically proves that the
  unrelated 32-level tree guard truncates a valid 33-page overflow chain.
- Added 13 malformed overflow-chain encodings, exclusive ownership checks,
  live-page alias checks, scope separator validation, committed overlap use,
  accumulated-count overflow, and reopen/rebuild preservation.
- Added fault-injected transaction tests for truncate ordering, old-generation
  survival, allocation/truncate poisoning, and corruption discovered after
  open. The previous generation is currently truncated before the first
  durability barrier and cannot be reopened after the injected sync failure.
- Expanded free-list reachability coverage to data branch/leaf, scope
  branch/leaf/overflow, metadata, and free-list chain pages. Reachable data
  leaves, scope leaves, and scope overflow pages are currently accepted as
  authoritative free state.
- Added external-sort invalid-configuration, spill poisoning, finish-after-
  failure, multi-pass cleanup, and owned-run lifecycle tests. Failed multi-pass
  merges currently leak all 40 fixture runs.
- Added legal exhaustion tests for `scope_id == u32::MAX` and
  `txn_id == u64::MAX`. Go wraps to reserved scope ID 0 / transaction 0; Rust
  panics on both additions.
- Added writable-open tests for committed data-leaf CRC failure, reversed
  ranges, record-count mismatch, and branch-separator mismatch. Core and
  file-backed writable opens currently accept all four corrupt images.
- Added green direct coverage for merged CIDR queries, the IPv6 family maximum,
  file-backed migration and overlap wrappers, deterministic sorted pair output,
  and overflow-scope preservation through four reopen/rebuild generations.
- Benchmarked pair aggregation. Go grew from 18.3 microseconds and 1.5 KiB at
  8 feeds to 1.28 milliseconds and 208 KiB at 64 feeds. Optimized Rust grew
  from 0.37 microseconds at 8 feeds to 484 microseconds at 64 feeds, with pair
  throughput falling from about 75 million/s to 4.2 million/s.

### 2026-07-15 - Re-audit after Round 5 repairs

- Rebased the audit on commit `c4dbde5e60b1d8e53148a9c2a39255268685cc43`,
  which fixes the original Round 5 red matrix. Independently confirmed the new
  baseline passes 278 Go top-level tests and 294 Rust test functions before
  adding this round's cases. The commit message's `333 Rust` / `611` aggregate
  claim is not reproducible with Cargo: a clean clone at the commit lists 294
  Rust test functions, so the directly verified language total is 572.
- Re-reviewed the repaired control flow instead of only rerunning the previous
  tests. Added 16 new Go top-level tests and 16 new Rust top-level tests. The
  existing writable-corruption matrix also gained one mirrored corruption
  case. No implementation source was changed.
- Confirmed 14 additional defect classes:
  1. Rust free-list reachability decodes IPv6 data branches with `Ipv4Key`, so
     a reachable non-first IPv6 child can be accepted as authoritative free
     state (`v4/rust/iprange-livedb/src/free_list.rs:212-243`).
  2. Scope-table rebuild reclamation stops after `TREE_HEIGHT_MAX`, orphaning
     overflow pages 37 and 38 from a valid 35-page chain in both languages
     (`v4/go/writer.go:955-987`, `v4/rust/iprange-livedb/src/writer.rs:871-909`).
  3. Rust `ScopeRegistry` panics at the legal `u32::MAX` boundary in both
     construction and minting paths (`v4/rust/iprange-livedb/src/scope_table.rs:106,147-155`).
  4. Overflow-scope overlap resolves and allocates the same bitmap once per
     record. At 100 records, Go performs 101-102 allocations and Rust performs
     101, instead of remaining constant (`v4/go/overlap.go:307-338`,
     `v4/rust/iprange-livedb/src/overlap.rs:154-195`).
  5. Validation accepts an empty reachable leaf whose unused body contains
     nonzero data because the empty-record return precedes the tail check
     (`v4/go/reader.go:559-580`, `v4/rust/iprange-livedb/src/reader.rs:498-515`).
  6. File-backed writable open copies the entire committed database into heap
     for validation. Opening a sparse 64 MiB database allocates about 128 MiB
     in Go and 64 MiB in Rust (`v4/go/os.go:273-278`,
     `v4/rust/iprange-livedb/src/os.rs:236-249`).
  7. Core writable open trusts unvalidated `total_pages` when reserving writer
     state. A two-page store claiming a 32 GiB logical database allocates about
     34.6 MiB before rejection in both languages
     (`v4/rust/iprange-livedb/src/writer.rs:199-215`; mirrored Go writer setup).
  8. Free-list validation builds a hash set containing every reachable live
     page. Heap therefore grows with database size in both languages
     (`v4/rust/iprange-livedb/src/free_list.rs:181-205`; mirrored Go traversal).
  9. Cheap Reader traversal does not enforce the committed tree depth. A cyclic
     branch returns fabricated lookup data; Go Scan overflows the stack and Rust
     Scan interprets branch bytes as leaf data (`v4/go/reader.go:206-230` and
     mirrored Rust traversal).
  10. Go Scan accepts a cross-family call and decodes the records with the
      caller-selected key width. Rust already rejects this case
      (`v4/go/reader.go:185-189`).
  11. Checksum-valid malformed scope `entry_count` values can panic Reader scope
      resolution/listing instead of returning an error in both languages
      (`v4/go/reader.go:196-204`; mirrored Rust Reader scope APIs).
  12. External sorter `Finish` re-materializes a single spill run. Going from
      100 to 100,000 records increases Finish allocation from about 3.5 KiB to
      7.0 MiB in Go and from 9.4 KiB to 9.5 MiB in Rust
      (`v4/go/extsort.go:958-1055`,
      `v4/rust/iprange-livedb/src/extsort.rs:88-180`).
  13. Committing one new indirect scope reads/materializes and rebuilds the
      complete committed scope table. At 20,000 existing scopes the measured
      heap is about 4.1 MiB in Go and 2.8 MiB in Rust
      (`v4/go/scope_table.go:339-355`; Rust scope rebuild in
      `v4/rust/iprange-livedb/src/writer.rs:341-446`).
  14. `ForeignVsAll` first collects every pending leaf page number. Allocation
      grows from tens of bytes to about 33 KiB between 1,000 and 500,000 records
      in both languages (`v4/go/overlap.go:159`,
      `v4/rust/iprange-livedb/src/overlap.rs:311`).
- Added permanent mirrored tests in `v4/go/round6_*_test.go` and
  `v4/rust/iprange-livedb/tests/round6_*.rs`; extended the existing Round 5
  overflow, free-list, limit, allocation, and writable-corruption matrices for
  findings that belong to those contracts.

### 2026-07-16 - Re-audit after Round 6 repairs

- Rebased the audit on commit `08393e37e869e668fee2ba08d946c773462ebd01`,
  which repairs the Round 6 failures. Independently confirmed the new baseline
  contains and passes 294 Go top-level tests and 315 Rust test functions before
  adding Round 7 coverage.
- Added 40 Go top-level tests, 56 Rust test functions, four Go benchmarks, and
  four mirrored Rust Criterion benchmark groups. No Go or Rust implementation
  source was changed.
- Corrected the cross-language test model before retaining it: mutable trees are
  required to be semantically cross-readable, not byte-identical. Complex
  branchy IPv4 and IPv6 round trips pass in both implementations. Exact-byte
  tests were removed because they would lock an implementation-specific tree
  shape rather than the format contract.
- Confirmed the following correctness and transaction failures:
  1. A legal empty data leaf terminates traversal early in `ForeignVsAll`,
     forward and backward cursors, cursor range queries, generic migration, and
     feed migration. Consecutive empty leaves fail as well.
  2. `MigrateFeed` accepts unsorted and overlapping desired streams. A later
     commit can publish a partial result after malformed input or a source
     stream error. The tests accept any safe implementation strategy:
     prevalidation, rollback, or writer poisoning.
  3. `MigrateFeed` ignores the requested change callback
     (`v4/go/feed_migrate.go:27` and
     `v4/rust/iprange-livedb/src/feed_migrate.rs:31`).
  4. Feed add/remove range can delete matching records before rejecting an
     invalid feed bit; a later successful commit publishes the deletion.
  5. Poisoned writers still accept scope interning and bitmap mutation.
  6. Trailing-zero-equivalent scope bitmaps receive different identities,
     including after reopen, so logically identical feed sets can multiply.
  7. Public in-memory sorted streams accept reversed ranges in both languages;
     Go's one-shot external sorter also accepts them. Rust's one-shot sorter
     safely rejects them.
  8. Counting the full IPv6 address space wraps to zero in Go and panics in
     Rust in both all-to-all and foreign-vs-all overlap paths
     (`v4/go/overlap.go:558-566` and
     `v4/rust/iprange-livedb/src/overlap.rs:435-445`).
- Confirmed the following bounded-memory and scaling failures:
  1. Foreign-vs-all retains every distinct overflow bitmap and linearly searches
     the cache (`v4/go/overlap.go:393-426`; mirrored Rust path).
  2. Writable open materializes the persistent free list, and scope validation
     retains every overflow page number. Heap scales with file metadata rather
     than active work.
  3. Feed add/remove range materializes every matching record. Commit and sparse
     compaction also allocate despite the zero-allocation contract.
  4. The first mutation of a sparse file allocates writer page state by absolute
     page number, so heap scales with sparse file length.
  5. Free-list cycle rejection work scales with untrusted `total_pages`.
  6. Spill-stream consumption allocates per emitted record; spill I/O performs
     unbuffered record-sized operations. Go measured roughly two allocations per
     record at both 10,000 and 100,000 records.
  7. Committed scope deduplication and pending-scope resolution are linear in
     scope count. Go measured about 5.0 microseconds at 1,000 committed scopes
     versus 62.5 microseconds at 10,000; optimized Rust measured about 2.6 versus
     40.6 microseconds. Pending resolution showed the same tenfold relationship.
  8. Adding one scope rescans the complete committed scope table for CRC and
     rebuild preparation; writable file open performs duplicate full validation
     scans.
  9. Rust all-to-all pair aggregation remains quadratic in the number of output
     pairs because pair updates linearly search accumulated results.
  10. Nested sequential-assignment external-sort normalization is quadratic despite linear
      output. Optimized Rust measured about 4, 16, and 65 milliseconds for
      2,000, 4,000, and 8,000 inputs. Go measured about 2.8, 9.3, and 35.5
      milliseconds for the same progression.
  11. Go's one-shot external sorter exceeds an `RLIMIT_NOFILE=64` budget with
      100 one-record runs. Rust's corresponding bounded-descriptor test passes.
- Confirmed the following file and reader-coordination failures:
  1. Read-only and writable file openers accept unknown metadata flags, static
     identity disagreement between valid meta pages, and unaligned file sizes.
  2. Rust multiplies untrusted `total_pages` before validating the product and
     panics on `u64::MAX` in debug builds (`v4/rust/iprange-livedb/src/os.rs:168`).
     Go safely rejects this case.
  3. Go and Rust derive different ReaderTable companion names. Both follow an
     existing symlink and accept a malformed one-byte fixed-layout sidecar.
  4. Replacing the ReaderTable path while a reader is active splits the mapped
     slots and registration lock across different inodes. A writer can then see
     no active reader.
  5. `ReapStale` mutates slots without the registration lock and can race slot
     reuse (`v4/go/readers.go:219-229` and
     `v4/rust/iprange-livedb/src/readers.rs:217-227`).
  6. Dropping a Rust `FileWriter` without explicit close leaves a 270,336-byte
     growth region in the fixture, versus 8,192 bytes after close.
  7. Rust leaves a partial external-sort run after a spill write failure. Go
     removes the corresponding partial file.
- Source review found additional ReaderTable risks which are not yet claimed as
  deterministic failures: cross-process slot publication uses plain memory
  writes rather than release/acquire atomics; `EPERM` from process liveness
  checks is treated as dead; a concurrent opener can observe the zero-length
  interval between exclusive create and resize; and close/unmap/truncate errors
  are discarded. These require implementation-level protocol review and
  process tests during repair.
- Added executable semantic checks for every committed tree golden. This exposed
  that the conformance README's cross-read claim was not implemented: no prior
  Go or Rust test referenced any committed `.iprdb` tree file. All 16 committed
  tree goldens are v4.0-era artifacts rejected by the v4.3 Go reader (`bad
  meta_size` or `record_size mismatch`); Rust rejects the first for the same
  reason. The metadata README is also stale: it describes tests and v4.1 APIs
  that do not exist in the current v4.3 test suite, while the authoritative
  v4.3 specs mark KV metadata as pending.
- The canonical live-DB spec still says writers produce byte-identical files at
  `.agents/sow/specs/design-iprange-v4-livedb.md:145`, while
  `v4/conformance/README.md:37-41` and the locked Go-port decision require
  semantic cross-read because mutable tree shape may differ. This contradiction
  must be resolved during repair; the tests enforce semantic results only.

### 2026-07-17 - Final-contract implementability audit

- Three independent read-only audits mapped decisions 4-41 to the normative
  contract and tested whether Go and Rust could implement every byte, lock,
  transaction, recovery, and publication rule without inventing policy.
- Repaired technical gaps before code work: exact empty-subtree predecessor
  traversal; same-transaction/different-nonce commit resolution using either
  retained meta; exact sidecar transition state/CRC ordering; writer meta
  reselection under the operation lock; transaction-scoped scratch budgets;
  reader-stable allocator finalization; writer-lease update after publication;
  exact Windows lock ranges; per-handle lock mutexes; platform process-start and
  death-proof rules; dedicated damaged-live recovery registration; conservative
  recovery-envelope evidence; sidecar-before-main `CreateLive`; locked
  `ReplaceExisting`; exact publication attempt identities; and exact
  file/namespace synchronization primitives. The then-drafted signing material
  was subsequently removed from Phase 1 by decision 45.
- Removed the contradictory obsolete v4.3 specification and every obsolete
  shared conformance corpus/golden. Replaced the conformance README with the
  final semantic cross-read requirements; missing opposite-language fixtures
  are failures, not skips. The embedded obsolete Go corpus and both old harnesses
  are inventoried for deletion/rewrite in implementation chunk 11; until then
  the deleted shared corpus intentionally makes the old Rust conformance target
  unusable rather than falsely claiming Phase-1 v4 coverage.
- Deleted completed SOW-0006 and SOW-0007 under decision 4 rather than retaining
  misleading current-tree history: both make the deleted predecessor snapshot
  and v4-to-predecessor exporter central requirements throughout, so removing
  only a few references would rewrite their historical meaning. Their committed
  originals remain available in Git history; no history rewrite was authorized
  or performed. Later retained v4 SOWs remain historical evidence but are not
  normative authority.
- The audit proved that page-local checks cannot make authenticated output
  equivalent to an unvalidated source: a false CRC-valid subtree summary can hide
  an unvisited child, after which a smaller rebuilt output validates internally.
  This Phase-2 evidence is preserved in pending SOW-0017; decision 45 removes it
  from the Phase-1 implementation gate.
- The audit exposed two additional product choices: allocator metadata integrity
  before destructive reuse (now decision 43), and whether an older retained meta
  is offered as a separate recovery candidate (now decision 44). The gate was changed
  from ready to blocked; no Phase-1 v4 implementation began.
- A follow-up namespace/crash audit also repaired technical, non-product gaps:
  one canonical `.readers` reservation is held from the beginning of every
  publication and converted in place for live creation; CRC-protected reservation
  and sidecar phases make namespace attempts recoverable; a proven-dead
  pre-publication writer lease is reapable after durable meta advancement; exact
  attempt-derived private names make orphan mains/reservations discoverable and
  safely removable; replacement records exact desired and previous content
  digests and resolves the destination postcondition under retained locks and
  durability sync. The former signed-output branch is not part of Phase 1.
- Replaced the single mutable coordination header with two 512-byte records,
  each CRC-protected across its own 4 KiB storage block and selected by a
  monotonic sequence. Every phase transition writes and synchronizes the
  alternate block before acting, so a torn block write cannot destroy the only
  durable attempt record. On Windows,
  handle-relative `NtCreateFile`/`SetFileInformationByHandle` operations,
  delete-sharing, write-through handles, and explicit flushes replace the
  contradictory pathname-only publication wording; unsupported filesystems
  fail with a typed durability error rather than silently weakening the contract.
- Made publication, live creation, initialization, and reset resolution
  exhaustive and identity-bound. Offline reset now synchronizes a complete
  private replacement record before touching the old sidecar, atomically swaps
  that exact inode without an absent-name window, and gives both `Complete` and
  `Remove` deterministic postconditions for every old/new/private/absent state;
  missing required identity evidence is `Unresolvable`, while foreign or
  phase-inconsistent state is `Conflict`. If the retained old live database
  advances after a reset crash, the stale reset is explicitly superseded:
  `Complete` cannot apply it, while `Remove` cleans only its exact private residue
  and reports that old coordination was retained and advanced.
- Corrected every live-transition resolver to treat a ready state-1 sidecar as
  creation/initialization provenance rather than a permanent whole-file digest.
  The selected database/transaction/nonce tuple identifies the committed
  generation after live use becomes possible; changed physical bytes at the same
  tuple are reported separately because an unpublished tail, inactive-meta
  write, private COW draft, or damage can all produce that state. Incomplete
  reservation/state-2/state-3 phases still require the exact recorded length and
  digest.
- Stabilized online `ResolveCreateLive` classification: for a ready live pair it
  now holds the shared main lifetime lock and sidecar operation lock, strictly
  scans/reaps the writer lease, returns `WriterBusy` unless the lease is proven
  free, and retains both locks through tuple selection, any whole-file pass,
  synchronization, and final rechecks. Registered readers may still coexist.
- Made two previously optional recovery/open outcomes deterministic across Go
  and Rust: live readers accept safe aligned unpublished tails, and membership
  recovery salvages every independently verified conflict-free catalog pair.
  The older-meta recovery audit also proved that post-hoc live registration
  cannot retroactively protect already freed pages; any older candidate selected
  by decision 44 is therefore offline/copied-source only.
- The user initially resolved decisions 42-44 as fused authenticated publication,
  amortized allocator-metadata checks before destructive reuse, and separately
  labelled newest/previous recovery candidates. Decision 45 then moved all of
  decision 42 to Phase 2 while retaining decisions 43-44 in Phase 1.
- Measured the performance-sensitive primitive on the local Intel i9-12900K:
  Go's standard hardware-accelerated CRC32C over a cached 4 KiB page measured
  109.4-117.1 ns (34.97-37.44 GB/s) across five two-second samples. The decided
  allocator check touches at most four bitmap pages and each committed page at
  most once per transaction; this is primitive evidence, not an end-to-end
  performance claim. The ad hoc signing-era SHA-512 microbenchmark was discarded
  as non-authoritative. Phase 1 must measure its actual unsigned publication
  digest pass end to end; Phase 2 will later measure signing separately.
- Decision 45 selected a future signing-provider boundary but moved every signing
  wire/API/dependency/test/performance requirement to pending SOW-0017. The
  normative Phase-1 format is unsigned and reserves no signature root or page.
- The final authority corrections also made unsigned `SnapshotTo` explicitly
  free of implicit full source/final-output validation, gave failed temporary
  cleanup a truthful residue-bearing error, and made `RecoverLive` require proof
  that its candidate is the actual current generation. Ambiguous or older
  candidates are immutable/offline only.
- At the ready-gate checkpoint, `go test ./... -run '^$' -count=1` still compiles
  the obsolete Go implementation/tests. Rust `cargo test --workspace
  --all-features --no-run` fails only because the deliberately removed obsolete
  `v4/conformance/cases.json` is still included by the old harness; replacing
  both harnesses and their corpus is an explicit implementation chunk, not a
  reason to restore obsolete fixtures.

### 2026-07-18 - Phase-1 public-contract closure audit

- Continued read-only authority and implementability review after the user asked
  to proceed. No implementation source was changed. The review found decisions
  46-66 as the remaining product/API choices; signing remains wholly in pending
  SOW-0017.
- Tightened address algebra to the released coverage model, including exact
  interval/address summaries and membership whole-file versus named-feed views.
  Value-aware cross-file conflict policy is not silently inferred.
- Replaced per-record bulk callbacks with bounded borrowed batches and added
  source/sink/stop/error/cancellation precedence. The terminal recovery result is
  returned normally; only candidate findings and unknown envelopes are streamed.
- Removed the proposed per-lookup Reader active counter from the recommended
  concurrency contract. The performance-first option keeps point lookups free of
  synchronization, makes Reader Close racing a call explicit caller misuse in
  Go/C, retains persistent-child parent borrows, and fail-fast gates mutable/FFI
  handles. The validation plan now requires hot-path measurement.
- Made `Abort` outcomes disjoint: clean factual rollback is reusable; factual
  rollback with independent residue is close-only; unresolved committed-tail
  authority is `AbortIncomplete`. Close never commits.
- Added a fixed CreatorOnly creation choice grounded in update-ipsets' existing
  `0600`/`0700`/`UMask=0077` behavior. The recommended contract captures the
  original creation principal/security-state commitment before publication and
  reports later deliberate access changes orthogonally without rewriting ACLs or
  changing byte-content truth.
- Added exact dual-copy authenticated Windows GC authority requirements, explicit
  destination replacement policy, explicit open modes, metadata read buffers,
  cancellation coverage for reader-table scans and lock waits, and the stable C
  ABI's exact `Cardinality129` layout. A normative numeric status/error registry
  and precondition precedence remain mandatory technical follow-through after
  the user chooses; they are not an extra product choice.
- Re-ran `git diff --check` and `.agents/sow/audit.sh`; both pass. The gate remains
  intentionally blocked only on decisions 47-66 and their synchronization into
  the normative specification.

### 2026-07-21 - Unsigned Phase-1 decision closure and specification reconciliation

- Recorded all remaining product decisions and derived the dominated technical
  choices without further user questions. No unresolved Phase-1 design fork
  remains; mechanical SOW-edit failures are retried and do not stop the approved
  plan.
- Reconciled the normative binary and architecture specifications with the two
  semantic API layers, internal unordered normalization, sequential direct
  assignments, specialized retention refresh, name-based multi-feed import,
  explicit opens/validation/recovery, zero external sorting files for normal
  work, destination publication policy, abort/Close, cancellation, access policy,
  orthogonal resolvers, FreeBSD publication, and Windows housekeeping.
- Added the normative C ABI generation-1 boundary specification. Exact exported
  symbols remain generated and frozen only after the corresponding Rust API is
  proven, as required by implementation ordering; the binding contract, required
  semantic surface, layouts, ownership, callback, error, panic, and validation
  rules are already fixed.
- Corrected the update-ipsets adoption analysis, Phase-2 algebra tracker, and
  conformance requirements so retention and exact single-feed workflows remain
  Phase 1, while only detailed multi-file algebra remains Phase 2.
- Decision 67 selected the full common `FinishInput` statistics contract; the
  binary-format specification now consistently calls it one common semantic
  report rather than an operation-specific result. Three independent focused
  reviews found no remaining semantic, layered-API, or
  Windows publication/cleanup blocker after reconciliation.
- `git diff --check`, the v3/reference removal search, and
  `.agents/sow/audit.sh` pass. The normative unsigned Phase-1 contract is now the
  implementation authority and the pre-implementation gate is open.

### 2026-07-21 - Exact-v4 implementation cutover

- Implemented the first Rust wire/static foundation: exact 256-byte meta codec,
  O(1) two-meta classification/selection, checked file geometry, 16-byte value
  tags, exact 129-bit cardinality, and the exact 32-byte non-meta header. The
  focused library suite has permanent field-matrix, transaction/parity,
  identity-conflict, length, metadata-bound, CRC, and full-IPv6 tests.
- The predecessor Rust v4.3 modules are one inseparable dependency cluster: its
  reader/writer depend directly on the old 16-byte header, scope modes/table,
  append-only free list, migration/external-sort path, fixed reader table, and
  corresponding tests/benchmarks. Preserving any part as a compatibility layer
  would violate decisions 3 and 34 and keep the wrong public API active.
- Before deleting that cluster, its still-valid test intent was mapped to the
  implementation plan: range/boundary/cycle/empty-leaf cases to chunk 2;
  allocator/reader-table/MVCC/sparse-memory cases to chunk 3; transaction,
  fault, durability, and writable-corruption cases to chunk 4; feed/scope/limit
  cases to chunk 5 but rewritten around named feeds and SDK-owned memberships;
  migration/retention/normalization/source-failure cases to chunk 7; explicit
  corruption validation, recovery, snapshot, and cross-open cases to chunks
  8-11. Old external-sort tests become proof that normal ingestion creates no
  external sort files. Detailed multi-file algebra remains tracked by SOW-0018.
- The cutover therefore removes the complete obsolete Go and Rust
  module/test/benchmark clusters at one compile-green foundation barrier.
  Deleted tests are historical evidence only; each valid failure class must
  return as an exact-v4 test in its mapped implementation chunk before this SOW
  can close.
- Independent review of the first Rust range slice caught a shared-format
  blocker before any fixture was frozen: IPv6 had inherited the experiment's
  high-limb-first encoding instead of the normative little-endian `u128`
  encoding. Rust and Go now store the low `u64` limb first, with unequal-limb
  raw-wire tests. The same review found missing ancestor fallback and reusable
  cursor state after traversal errors; the Rust cursor now resumes at the
  nearest earlier nonempty ancestor sibling and makes every failed movement
  terminal without publishing a partial path. No-default-feature unit tests now
  run as well as compile, and the physical key codec trait is no longer public.
- Completed the mirrored private Go and Rust range readers. Both use a fixed
  32-frame path, accept legal empty leaves and all-empty subtrees, resume
  predecessor search at the nearest earlier ancestor sibling, make structural
  and record-decode errors terminal for that cursor, skip ordinary page CRCs,
  and count the full IPv6 space exactly with `Cardinality129`. Go passed its
  normal, race, vet, and 32-bit suites; Rust passed all-feature and no-default-
  feature tests, Clippy, and warning-free no-default-feature documentation.
- Began allocator implementation with the Rust hierarchical bitmap page and
  bounded-search foundation. It distinguishes free-page one-bit search from
  feed/membership zero-bit allocation, treats absent used-bitmap children as
  logical zeroes, excludes membership ID zero, enforces the minimum root level,
  and provides the separate CRC/local-invariant-verified root-to-leaf search
  required before destructive free-page reuse. The ordinary inspection path
  deliberately does not calculate page CRCs.
- Mirrored that bitmap foundation in Go and added adversarial bounds checks for
  every nonzero child pointer on a verified branch, not only the selected child.
  Both implementations document the remaining writer obligation: a transaction
  must retain each verified committed path in its COW state and must not rescan
  it before every allocation.
- Added the exact generic blob page and bounded zero-copy streaming foundation in
  Rust for membership bitmaps and retirement page lists. Review found and fixed
  two defects before integration: retirement page numbers now must be in
  `[2,page_count)`, and ordinary lookup no longer scans unselected entries or
  reserved page tails. Explicit verified mode still checks normalized CRCs,
  every local entry, bounds, ordering, and reserved tails; regression tests prove
  that separation and terminal cursor failure.
- Added the pure Rust sidecar header/slot codec and ready-image inspection
  foundation. It checks exact double-header selection, file size, database and
  local identities, process domain, basename commitment, writer generation,
  every reader slot, host PID/task representability, CRCs, and malformed stable
  states without allocating per slot. OS-retained-descriptor opening and locking,
  transition provenance, claim/update/clear, death proof/reaping, and reset remain
  part of implementation chunk 3 and are not claimed by this codec layer.
- Added the Rust retirement-tree page and bounded two-pass streaming foundation.
  It selects only the oldest complete eligible prefix under caller work limits,
  verifies every selected tree/blob path and complete page list before yielding
  any page, preserves exact transaction/root identity between passes, and uses
  caller-owned batch scratch plus fixed traversal stacks. Retirement page and
  blob values are range-, level-, order-, count-, CRC-, and geometry-checked;
  destructive free-bitmap application remains pending writer integration.
- Added the exact Rust `IPR4RSV1` namespace-reservation codec and mixed
  reservation/sidecar conversion selector for all five operation kinds. Review
  exposed an ambiguity in the coordination sequence text: two valid alternating
  blocks can only be byte-identical at one sequence or exactly adjacent. The
  normative spec now says this explicitly; reservation, mixed-conversion, and
  sidecar-only selectors reject gaps, wrap-shaped pairs, equal disagreement, and
  origin-invalid forward/reverse phase changes.
- Mirrored the corrected generic blob foundation in Go, including fixed-stack
  zero-copy traversal, lightweight ordinary selected-path reads, explicit local
  verification, exact stream geometry, terminal errors, and strict in-range
  retirement lists. The normal, race, vet, and 32-bit Go gates pass.
- Independent review found no correctness, safety, or bounded-memory defect in
  the Rust retirement foundation. A separate Go blob review did identify a hot-
  path performance defect: copying the cursor before every chunk copied about
  1.8 KiB twice. The same-failure search found the pattern in both languages'
  blob/range cursors and the Rust retirement cursor; all now mutate their
  private state in place and make an error terminal. No matching draft-copy
  pattern remains. Rust formatting and Clippy pass with and without default
  features; the full Rust and Go test gates remain green after the change.
- Mirrored the exact reservation/sidecar codecs and bounded two-pass retirement
  reader in Go. The Go retirement cursor initially reintroduced the already-
  identified full-cursor draft copy; the same-failure gate caught and removed it
  before integration. Normal, race, vet, and 32-bit Go validation pass.
- Independent coordination review found that the first Rust ready-sidecar
  inspection compared slot transactions before OS death proof. That made the
  valid crash window after durable meta publication but before writer-lease
  update unrecoverable. Both languages now separate allocation-free structural
  slot inspection from post-reaping transaction validation and cover the stale-
  writer and future-reader precedence explicitly.
- Resolved the state-2 retry contradiction: an armed transition continuously
  retains the exclusive operation lock until exact target or all-zero readback.
  Releasing and reacquiring that lock could let two claim attempts both believe
  the same transient slot was theirs. The Rust transition foundation now keeps
  prepared and armed provenance move-only, rejects every write after disarming,
  treats a different nonce as foreign mutation rather than impossible reuse,
  and binds owned/proven-dead source, role, slot, header, target, and exact write
  order. OS retained-lock ownership and positional I/O were the next pending
  integration at that checkpoint.
- Added allocation-free Linux process-identity and conservative death-proof
  foundations in both languages. `/proc/<pid>/stat` is parsed after the final
  command-field parenthesis with checked nonzero `u64` arithmetic; only `ESRCH`
  or a mismatch between two available nonzero start tokens proves POSIX death.
  Missing/unreadable tokens, `EPERM`, and every uncertain result remain alive.
- Added retained-directory and retained-regular-file Linux adapters in Rust and
  Go using final-component no-follow opens, close-on-exec descriptors,
  link-count-one checks, independent open-description `flock`, exact positional
  I/O, retained descriptor/path identity rechecks, PID-namespace identity, and
  CSPRNG nonzero claim nonces. FIFO opens are nonblocking before regular-file
  rejection, and entropy failure/all-zero output are injectable tests.
- Independent OS review found and closed four portability/safety gaps before
  live-open integration: every retained file now passes the supported-local-
  filesystem check and must share the retained directory device; filesystem
  magic is normalized correctly across signed/unsigned 32/64-bit ABIs;
  reserved main names use exact ASCII case folding; and caller path bytes are
  length-checked before the one fallible owned copy. Remote/userspace and
  otherwise unproved filesystems fail closed.
- Added one allocation-free transition executor per language. It alone emits
  state 2, body, and final state in order, re-reads the exact target, stores
  armed provenance in caller-owned handle state before the first write, and
  preserves that provenance plus the continuously held operation lock on every
  failure. Retry cleanup emits state 2, zero body, and state 0 and releases
  authority only after exact all-zero readback. The Linux retained-descriptor
  adapters now reselect the exact ready header and source slot under the
  exclusive operation lock before invoking this executor; integration tests
  cover unlocked rejection, successful claim, interrupted state-2 cleanup, and
  success-only provenance removal.
- Go review found and closed a hidden per-page allocation in normalized CRC
  verification: a four-byte local zero buffer escaped once per verified page.
  The codec now uses one immutable package buffer, and a permanent test performs
  128 CRC calculations per run with exactly zero allocations. A separate review
  found the Go no-copy marker lacked `Unlock`, so `go vet` could not recognize
  it as a lock; the complete marker and shared authority state now protect both
  prepared and armed transition values.
- Independent transition review found that caller-owned armed provenance could
  outlive the exact operation-lock acquisition and retained descriptor. Both
  Linux adapters now store provenance inside the retained sidecar object and
  refuse unlock while it is armed. A Rust allocation-counting regression test
  proves the complete executor, including the armed interval, performs no heap
  allocation.
- Added the bounded retained-file ready-sidecar scan/reap orchestration in both
  languages. It first completes a read-only structural scan of every slot, then
  classifies liveness and clears only proven-dead readers, and only then scans
  survivors for writer/reader transaction consistency and oldest-reader state.
  Exact header and Linux PID-domain identity are reselected between passes;
  permanent tests prove that a malformed later slot prevents every earlier
  reap and that dead/future-owner precedence is correct.
- Dead writers are not cleared by the reader reaper. Their exact header, raw
  slot image, active owner, and canonical death proof remain attached to the
  locked retained sidecar descriptor, which refuses unlock and unrelated work
  until the separate main-tail cleanup protocol resolves the obligation.
- Added exact retained-descriptor two-meta bootstrap in both languages. It reads
  only the fixed 8 KiB bootstrap region, obtains current physical geometry and
  identity from the already-open descriptor, and applies the requested reader
  or writer selection rules without reopening a path or scanning data pages.
- Added the exact SHA-256 basename commitment and retained live-pair binding in
  both languages. The binding covers the caller's canonical main basename,
  main/sidecar local identities, database ID, sidecar ID, process domain,
  supported local filesystem, link counts, and continuously held lifetime and
  operation locks. POSIX bytes and well-formed Windows UTF-16LE have a shared
  hard-coded digest golden; malformed Windows surrogate sequences are rejected.
- Added the Rust composite dead-writer cleanup authority. One object retains the
  directory, main shared lifetime lock, sidecar exclusive operation lock, exact
  dead-writer proof, and any unpublished-main-tail obligation. Generic writer
  clearing is rejected; the composite records a long tail before truncation,
  synchronizes even at exact committed length, rechecks the complete generation,
  pair binding, and writer image, and only then arms the exact lease clear. Tail,
  synchronization, state-2/body/final/readback, and post-zero path failures keep
  their correct retry authority or factual outcome. An independent review found
  no defect; 134 all-feature tests and focused Clippy passed.
- Mirrored the composite dead-writer protocol in Go. Review found that its first
  opener revision reused a bootstrap read from before lock acquisition; it now
  performs an initial bootstrap before opening the sidecar and separately
  reselects the main bootstrap after both retained locks before pair binding.
  Permanent tests cover tail truncate/sync retries, exact-length synchronization,
  unexpected growth and generation/source changes, armed cleanup, post-zero path
  failure, and the required double-bootstrap order. Normal, race, vet, and 32-bit
  Go gates pass.
- Added the private live-reader slot foundation in both languages. A successful
  complete scan retains the exact bootstrap and lowest free reader slot; claim
  consumes that scan once, generates its nonce only after capacity and binding
  preflight, publishes transaction zero, reselects current meta, updates the same
  owner/nonce to the selected transaction, and unlocks only after generation,
  binding, source, role/index, and transaction cross-checks. Cleanup either
  retries continuously locked armed provenance or reacquires the lock and clears
  the exact move-/pointer-owned reader claim. It rejects missing authority and,
  after exact zero, independently reports main and sidecar path replacement.
  Review found and closed missing no-authority, post-zero path, and transaction-
  binding checks. Rust passes 138 all-feature tests and both Clippy modes; Go
  passes normal, race, vet, and 32-bit gates. Public reader content I/O,
  cancellation/result wrapping, cleanup guards, and Close lifecycle remain
  pending chunk-3 integration.
- A final independent Go reader-slot review found no remaining defect and reran
  normal, vet, 32-bit, and repeated focused race tests successfully.
- Added private cancellation-aware live coordination in both languages. Blocking
  lock acquisition uses nonblocking retries with bounded cancellation checks;
  every sidecar pass checks once per slot and again after a death proof before
  mutation. Tests prove cancellation while lock-contended and immediately before
  reaping leaves no lock ownership or slot mutation. Public cancellation/result
  wrapping remains part of the pending reader handle.
- Resolved a memory-safety contradiction before exposing live reader content.
  Ordinary access intentionally does not validate the complete graph/allocation
  partition, so corrupt content can make one page both reachable and immediately
  reusable. A shared Rust reference into that live mapping could then be mutated
  by a conforming writer. Decision 68 and the normative open sequence now require
  fixed-buffer positional reads (or an equivalent no-alias raw-copy mechanism),
  keep validation opt-in, and retain constant-time open. Reference checks:
  `LMDB/lmdb @ be8f614899fa`, `libraries/liblmdb/mdb.c:790-826,3285-3360,5108-5163`;
  `meilisearch/heed @ 14e3e4914ad5`,
  `heed/src/envs/env_open_options.rs:200-233`,
  `heed/src/mdb/lmdb_ffi.rs:42-47`; and `cberner/redb @ adf035308761`,
  `src/tree_store/page_store/file_backend/optimized.rs:56-89`.
- Added the Rust transaction-private hierarchical free-bitmap COW core. It uses
  fixed four-page path storage, caller-provided replacement/candidate ledgers,
  lowest-page selection, CRC verification before committed allocator reuse,
  bottom-up cloning, canonical summaries, and empty-path collapse without heap
  allocation. A later independent Go-parity review exposed quadratic repeated
  ledger scans in both languages. Rust now prepares a caller-owned AVL page index
  and available-slot stack before the operation lock: construction is
  `O(N log N)`, per-removal lookup is deterministic `O(log N)`, candidate order
  checks and slot selection/release are `O(1)`, and every success/error path under
  the lock allocates zero. Permanent tests cover all typed errors, adversarial AVL
  orders, word bits 63/64, forbidden bits 0/1, sibling/path/collapse behavior,
  committed corruption, `u32::MAX`, and 512-to-4,096 doubling behavior.
- Mirrored the transaction-private free-bitmap COW core in Go and rejected its
  first quadratic ledger lookup and allocation-bearing error implementation.
  The accepted core prepares one fixed 18,432-byte caller-independent object
  before the operation lock, builds an AVL page index and available-slot stack,
  performs deterministic `O(log N)` removals, and allocates zero on every hot
  success and typed error path. Production carries no test probe accounting.
  Permanent tests exhaust all eleven no-allocation page-header failure classes,
  exact evidence fields, word and page boundaries, conflicts, corruption,
  capacity, and adversarial AVL orders. A timing-independent structural proof
  checks every prepared tree from 512 through 8,192 entries, while normal CI
  pins the constructor to exactly one allocation and removal to zero. Two
  independent reviews found no remaining defect; normal, race, vet, 32-bit,
  repeated allocation, benchmark, and compiler escape gates pass.
- Completed the Rust logical free-page reservation planner above the COW core.
  Committed bitmap coverage is distinct from the pending private extent;
  verified candidates are reserved strictly lowest-first and may fund the same
  atomic COW plan that clears their bits, while appended slots are authorized
  only after the candidate cursor proves exhaustion. Review rejected the first
  closure because it budgeted final rather than peak intermediate metadata and
  could fail after a partially applied prefix; it also found cached no-read paths
  that bypassed the creator/access check. The accepted planner checks access once
  before any plan/application mutation and reserves the exact safe maximum of
  every ordered-prefix metadata peak versus payload plus final metadata. Height-
  three immediate/later-prefix cases, budget-minus-one atomic failures, sparse
  paths, boundaries, `u32::MAX`, allocation, and scaling tests are permanent.
  Independent re-review found no blocker; 239 all-feature and 161 no-default
  Rust tests passed on the accepted slice.
- Completed Rust free-bitmap insertion, root growth/demotion, and reservation
  finalization. The first green implementation was rejected because plans could
  be applied after another mutation or to another draft, cached verified pages
  were revalidated without enforcing their original logical position,
  committed roots could use a pending-only level, and root demotion could strand
  newly available reservations. The accepted move-only plan exclusively borrows
  its exact COW draft and owns `apply`; committed/verified roots use the exact
  committed level, verified evidence is reused only at the same page/base/level,
  and every demoted candidate/appended page joins the same preflighted bounded
  insertion plan. Mutation begins only after complete preflight and the apply
  prefix is infallible. Permanent tests cover all three level boundaries,
  verify-once reuse and alias rejection, demotion reinsertion, atomic insufficient
  scratch, and no stranded available page. Independent re-review accepted the
  repair; 43 focused tests in both modes, 270 all-feature and 192 no-default full
  tests, strict Clippy, formatting, and whitespace gates pass.
- Mirrored the complete reservation, insertion, growth/demotion, and
  finalization path in Go. The planner returns the sole consuming COW directly,
  reserves verified committed candidates strictly lowest-first, permits those
  candidates to fund their own atomic removal, budgets the exact maximum live
  metadata across every ordered prefix, and appends only after the committed
  candidate cursor is exhausted. Review found and closed one retained 4 KiB
  temporary, unchecked host-`int` totals, and mutation-epoch wraparound paths.
  Every package-callable mutation is now access-first; direct apply binds the
  exact COW and epoch, prechecks all epoch and arithmetic headroom, and performs
  no durable mutation on failure. Finalization reinserts every demoted reserved
  page through the same plan and leaves no available page stranded. Permanent
  tests cover all growth boundaries, stale/cross-COW use, verified page/base/
  level identity, `u32` and host-`int` exhaustion, access precedence, exact
  epoch advances, atomic scratch shortage, and zero allocation. Independent
  review accepted the slice; fifty repeated focused runs, full and race suites,
  vet, 32-bit, compiler escape, formatting, whitespace, and forbidden unsafe/
  linkname/noescape gates pass.
- Completed the private Rust retirement-tree writer and fixed-point ownership
  boundary. It supports bounded oldest-prefix deletion, newest-batch insertion
  or replacement, and their combined edit with caller-owned page storage,
  checkpoint/rollback/recycling, exact pending-transaction ordering, and no
  fallible mutation after its complete preflight. Repeated zero-trust reviews
  rejected green implementations that could mix committed and private blob
  generations, omit newly replaced metadata, lose an old same-transaction
  payload, recycle a still-referenced private blob, or accept a promoted-root
  back-reference while retiring that root. The accepted editor gives every
  private page an origin, generation, phase, and explicit retained-versus-retired
  provenance; exact references and selections are each consumed once per phase,
  and the replacement ledger, prior protected prefix, current discovered
  replacements, and old same-transaction payload all participate in the same
  monotonic fixed point. Permanent tests cover cross-subtree aliases, valid
  level-two promotion, the promoted-root cycle, rollback, access precedence,
  and zero allocation. Independent review accepted the final slice at SHA-256
  `935ae9b60debf82f0f46874ff68864e5f66d19d66606f2d51f7c189ba48ad1ce`;
  32 focused tests in both modes, 286 all-feature and 208 no-default full tests,
  both strict Clippy modes, formatting, and whitespace gates pass.
- Mirrored the accepted retirement-tree writer in Go with caller-owned arena,
  build and scan scratch, replacement/release ledgers, and a copy-safe arena/
  epoch token. The port preserves exact pending-transaction insertion and same-
  transaction replacement, oldest-prefix deletion, combined delete/upsert,
  committed-versus-private residence, uniform blob generations, retained-versus-
  retired phase roles, one-use reference/selection authority, prior protected
  prefixes, current discovered replacements, and private-release exclusion.
  Self-review and independent review closed a 32-bit role-index overflow, a
  same-transaction old-payload omission, token cleanup binding, and hidden page/
  frame escapes. Permanent parity tests include cross-subtree recycling aliases,
  valid level-two promotion, the deeper promoted-root back-reference, right-edge
  convergence, global cross-leaf order, rollback, access precedence, exact
  scratch budgets, and zero-allocation blob/append/replace/delete/combined hot
  paths. Independent review accepted source SHA-256
  `2d1881a2717762522bc4bf962730f07545e2904309f01228247b9d52f538c0a1`;
  repeated normal, race, 32-bit, full-module, vet, formatting, whitespace,
  forbidden-mechanism, and compiler-escape gates pass.
- Added the Rust positional page source and range reader integration. Live pages
  are copied through checked offsets into one cursor-owned page buffer; no
  reference into mutable live file storage escapes. Independent review found and
  closed a fork-safety bypass where cached cursor content could avoid the creator-
  process check. Every cursor content entry now checks access before consulting
  its cache, a direct hit proves one read per root/leaf, and a gap lookup records
  the bounded ancestor reread honestly. The resulting Rust gates pass 155 all-
  feature tests, 134 no-default tests, both Clippy modes, and formatting.
- Mirrored the positional page source and range traversal in Go, then rejected
  two performance regressions found by independent review. The accepted path
  fills the cursor's single 4 KiB buffer directly through `pread`, uses one
  creator check per public operation plus one per actual OS read, performs no
  checks on cache hits, and avoids returned-page temporaries, pooling, or global
  synchronization. Point lookup and reusable cursors are zero-allocation;
  compiler escape and assembly evidence show no second 4 KiB frame. Normal,
  race, vet, and 32-bit gates pass. Final review restored a true prefix/mutate/
  suffix torn-read fixture and added retained-file `pread` benchmarks: direct
  hit measures 975-1,203 ns/op and a cross-leaf gap 3,371-3,810 ns/op, both with
  zero bytes and zero allocations per operation.
- Extended the same Rust positional safety boundary through the committed free-
  bitmap reader/COW core, generic blob reader, and retirement reader. One typed
  `CommittedPageSource` now copies into caller-owned 4 KiB buffers and preserves
  exact fork, short-read, and OS-I/O evidence; cursor metadata and retirement
  page numbers are copied, so no source-backed reference survives a subsequent
  read. Ordinary versus CRC-verified modes, strict cross-leaf page ordering, and
  the retirement two-pass identity recheck remain intact. Permanent tests cover
  torn/mutating sources, buffer reuse, maximum depth, zero warmed allocations,
  and no release before complete first-pass verification. Independent review
  found no positional defect; 214 all-feature and 152 no-default tests plus both
  strict Clippy modes passed. That review separately confirmed the next planned
  allocator prerequisite: committed bitmap coverage and the pending reserved
  extent must be distinct so appended or logically reserved free candidates can
  fund their own atomic COW removal.
- Mirrored that positional allocator boundary in Go, then rejected the first
  implementation because retained `os.File.ReadAt` and pointer-wrapped errors
  allocated on real failures while the future operation lock was held. The
  accepted Linux path uses raw `pread` and carries scalar fork, errno, short-
  read, bitmap, blob, retirement, and sink statuses through caller-reused
  workspaces; ergonomic error objects are constructed only by convenience
  wrappers outside the lock. Permanent retained-file tests prove zero
  allocations for PID mismatch, raw `EBADF`, exact 17-byte truncation at every
  reader stage, and one versus thirty released pages; a second-pass read failure
  reaches the release sink zero times. Independent code and escape review found
  no hidden interface/error allocation or unsafe compiler bypass. Normal, race,
  vet, 32-bit, twenty repeated allocation matrices, and Darwin/FreeBSD/Windows
  compile gates pass. Continuous operation-lock ownership from reader-threshold
  selection through release and publication remains the next integration step,
  not part of this accepted positional-reader slice.
- Added the private Rust Linux live-reader lifecycle over retained descriptors.
  Open performs complete scan/dead-writer cleanup, preflights all possible view
  setup, claims transaction zero, pins the selected generation, rechecks and
  unlocks, then serves copied positional content without implicit validation.
  Failed open and established Close retain non-consuming cleanup authority;
  exact-zero absence is idempotent and canonical path replacement is reported
  independently after cleanup. Review found and closed late family validation,
  lost failed-open path evidence, inconsistent cancellation typing, and rejected
  already-zero first Close. Thirteen lifecycle tests plus the complete 176/142
  Rust feature matrices and both strict Clippy modes pass.
- Mirrored the private Linux live-reader lifecycle in Go. Open performs the
  cancellable retained-pair scan, dead-writer recovery, preclaim view setup,
  transaction-zero claim, selected-generation pin, final rechecks, and copied
  positional content access without implicit validation. Cleanup guards and
  established Close keep pointer-owned retry authority and exact-zero
  idempotence; creator-process checks precede content or cleanup work. Review
  found and closed one error-truth defect: a path replacement detected after a
  dead-writer lease reached exact zero was being hidden behind the older scan
  error. The terminal cause and both independent path outcomes are now retained,
  with deterministic tests for that post-zero case and scan-origin cancellation.
  A raw-fork Go method test is intentionally absent because the Go runtime does
  not support executing arbitrary Go after raw fork and before exec; the native
  PID-first tests cover the Go boundary, while actual inherited-handle fork
  coverage belongs to Rust and the later C ABI. Two independent reviews and
  normal, race, vet, 32-bit, repeated lifecycle, and allocation gates pass;
  warmed retained lookup remains zero-allocation at 1.12-1.29 microseconds.
- Added private retained-descriptor Linux live-writer Open/Close lifecycles in
  Rust and Go without exposing transaction mutation or a public API. They claim
  slot zero at the fully scanned selected transaction, retain the complete
  selected generation, freeze unpublished-tail authority before exposure, and
  resolve/synchronize that exact tail before clearing a lease. Close becomes
  retry-only before cleanup and remains non-consuming and idempotent; failed
  opens return the original cause plus either proven cleanup outcomes or a guard
  that retains exact retry authority.
- Zero-trust writer reviews rejected several initially green implementations.
  The accepted implementations now reject same-transaction/different-generation
  replacement, never widen a frozen tail, preserve proven-dead generation/tail
  authority through every interrupted writer Clear phase, reselect the retained
  sidecar header immediately around truncation and clearing, and retain factual
  cancellation at the dead-writer boundary. Exact-zero is accepted without the
  obsolete main generation only after a fresh header/zero-slot proof plus retained
  file type, link, identity, and exact physical-length proof; unaligned `+1` and
  short files retain cleanup authority. Permanent matrices cover all transition
  interruptions, malformed/torn/changed headers, full generation mutations,
  growth, path replacement, guard retry, and reader/writer entry paths. Final
  independent reviews found no blocker. Rust passes 236 all-feature and 158 no-
  default tests plus both strict Clippy modes; Go passes repeated focused/race,
  full/race, vet, and 32-bit gates.
- Added the private continuous writer operation barrier in Rust and Go. After
  writer exposure it reacquires the exact retained sidecar lock, serializes the
  handle with a real in-process mutex, rechecks the retained and canonical pair,
  sidecar header, selected bootstrap, and exact owned lease before and after a
  complete strict reader-table scan, then retains the same authority for later
  allocator/retirement finalization and publication. Review rejected stale
  reader-threshold snapshots that survived successful unlock. Rust now binds the
  facts to its mutex-guard-owning barrier and Go binds every token copy to the
  exact writer epoch; failed unlock is non-consuming, while successful unlock
  invalidates every fact and stale token. Creator-PID checks precede inherited
  mutexes and locks, the exact owned writer cannot be classified dead, malformed
  late slots are reported before any reap, and close cannot consume a held
  barrier. Rust's complete barrier is allocation-free through 1,024 slots and
  passes 300 all-feature and 208 no-default tests plus both strict Clippy modes.
  Go initially allocated per active `/proc` observation and an attempted raw-
  pointer syscall workaround was rejected. The accepted safe two-phase design
  observes and deduplicates PIDs before the flock, retains only exact proven-dead
  candidates, and under the lock reuses a proof only when slot index and complete
  active identity still match; unmatched slots may use only allocation-free
  `kill(..., 0)`/`ESRCH` proof and otherwise remain alive. The locked Go phase is
  a fixed 29 allocations for retained identity checks at both one and 1,024
  readers with zero per-slot growth; safe preflight costs 24 bytes and one
  allocation per unique PID outside the flock. Independent reviews accepted
  Rust `live_writer.rs` SHA-256
  `e1b0d9f303e5de256acf70d198c4037d16a588ef7772b6d84618253dd759c845`
  and Go `live_writer_linux.go` SHA-256
  `c8bee8955d1e73579cf6b999f1afb1164e0783787ead24086406864e085b3ded`;
  repeated focused, race, full, 32-bit, cross-compile, vet, lint, formatting,
  escape, allocation, and forbidden-mechanism gates pass.
- Unified the previously separate Rust and Go bitmap/retirement private backing
  into one transaction-owned physical-page pool per language. The accepted base
  carries exact pool identity, pending transaction, owner, origin, generation,
  slot epoch, checkpoint, and rollback authority; uses caller-backed AVL indexes
  rather than collision-prone or quadratic scans; shares physical pages across
  bitmap and retirement phases without aliasing their semantic ownership; and
  performs no heap allocation in the mutation paths. Zero-trust review rejected
  invalid-tag panic, partial rollback, quadratic role lookup, stale checkpoint
  generations, insufficient aggregate mutation preflight, and a Go retirement
  token-overflow check that occurred after private commit. The final Go repair
  rejects token wrap before checkpoint creation and its permanent regression
  proves exact pool/scalar/input/scratch preservation with zero allocation plus
  successful use of the last nonzero token. Accepted Rust milestone hashes were
  `private_page_pool.rs`
  `3fbbd874c0ed78e27b4b668f930f83a417459556118669517fce3abe1b9c0b6f`,
  `bitmap_cow.rs`
  `5b2ea045521db06453df23a5a9b114686ca9a15cf9acee2b0f3b28c44ec4aa38`,
  and `retirement_writer.rs`
  `0e5f0d72e0d13d2b6bc1f39ed5013fee862b5fae58a159c437642bce3f8e8c3f`.
  Accepted Go milestone hashes are `private_page_pool.go`
  `b4e62d1db83d6b87f3265b94da7f02d94e7f8e44763c24bd9d5de3012e8149c7`,
  `bitmap_cow.go`
  `e489e6cf064ffc55cf115675178ebfa561a3bb7f0d20425900c1118a4738eeed`,
  `bitmap_insert.go`
  `d8a64b48f182bc36535637315027e622fb647c00f9d09d2edfefa1073e83905e`,
  and `retirement_writer.go`
  `e563e85743ef9860d578f022c877d4ea8e3d35faceaec41f0f184fc5beacd0e1`.
  Rust passed focused/default/all-feature/no-default, strict Clippy, formatting,
  and allocation/scaling gates; Go passed repeated focused, full, race, vet,
  32-bit, compiler-escape, formatting, and forbidden-mechanism gates. The active
  side-effect-free plan/apply refactor legitimately supersedes the Rust source
  hashes above; they record the independently accepted pool base, not a final
  file freeze.
- Completed and independently accepted the Rust side-effect-free retirement
  planning prerequisite. A narrow pool helper copies one exactly owned page
  without issuing authority or changing any pool/slot epoch, generation, state,
  counter, or byte. Move-only Upsert, Delete, and Combined plans exclusively own
  their arena/token, path scratch, replacement/release ledgers, and role index;
  stage bytes and ledger tails without changing logical lengths; and recheck
  source access, full-page scratch fingerprints, every identity, capacity,
  destination, release, and arithmetic/epoch headroom before opening one pool
  checkpoint. Combined planning uses a virtual delete overlay and performs no
  intermediate apply. After the checkpoint, only the mechanically prevalidated
  claim/write/release/commit prefix remains. The compatibility wrappers now
  route only through plan then apply, and the mutation-first editor helper is
  gone. Permanent tests cover complete side-effect snapshots, stale source/
  pool/arena/ledger/blob/scratch identities, late owner drift, combined
  invisibility, zero allocation, and deterministic sparse-pool scaling. Frozen
  accepted hashes are `private_page_pool.rs`
  `69164c49c9812c4c9703527749559de850774d33fd5168ce018fca1def4c04d1`
  and `retirement_writer.rs`
  `ecfa2303c7c80e5b684e67396f7de38fad01f47d7c1aac967fe6f7ba914b9e10`.
  Focused tests repeated fifty times, 237 default, 329 all-feature, and 237
  no-default tests, strict Clippy in both feature modes, formatting, whitespace,
  forbidden-mechanism, allocation, and scaling gates pass.
- Fixed-point design analysis found that the existing reservation planners bind
  appended page numbers too early. Safely reclaimed pages become eligible only
  after the locked reader scan and complete two-pass retirement verification;
  they may be lower than a speculative appended page and must therefore fund
  allocator and retirement metadata first. The implementation is split into a
  pre-lock capacity plan and a locked physical-binding step. The shared pool
  must support indexed vacant-slot bind/unbind, reservation-scope ownership,
  rollback of binding/index/pending-tail state, and selective scope finalization.
  Global “release every unused pool page” is forbidden because another
  fixed-point step may own an available page. Permanent tests must prove lowest
  eligible selection, no speculative growth, exact rollback/token invalidation,
  appended-hole versus suffix handling, zero locked-path allocation, and
  non-quadratic 512-to-4,096 source selection. A line-level Go audit confirmed
  that this crosses five coupled modules, not four: `private_page_pool.go` needs
  vacancy/scope/dynamic-index/checkpoint primitives; `bitmap_cow.go` must stop
  assuming every capacity slot is bound and must synchronize post-bind page
  count; `bitmap_reservation.go` becomes capacity-only before the lock and
  merges committed/reclaimed sources after two-pass proof; `retirement_writer.go`
  allocates only inside the exact scope; and `bitmap_insert.go` finalizes only
  that scope with explicit hole-versus-contiguous-suffix handling. Implement and
  freeze those slices in that dependency order before integrating the monotonic
  fixed-point caller.
- Completed and independently accepted Go late-binding dependency step 1. The
  private pool now supports caller-owned vacant capacity, exact reservation
  scopes, global and per-scope intrusive AVL indexes, O(1) scoped vacancy,
  checkpointed bind/unbind, pending-tail growth/suffix shrink, and complete
  rollback of bindings, roots, links, aggregates, scope headers, bytes, and
  pending page count while monotonically invalidating touched tokens. The first
  green version was rejected because forward work could consume rollback/commit
  epoch headroom and legacy global helpers could bypass scope ownership. The
  accepted repair prospectively reserves forward plus one terminal cleanup step
  and an extra slot epoch for each first-touched slot; terminal passes verify the
  remembered/reserved count. Global operations reject while any scope exists,
  and exact scoped borrow/claim/return/transfer/generation-release paths validate
  pool, incarnation, pending transaction, scope ID, and anchor. Permanent tests
  cover later-slot and prepared-path exhaustion, exact rollback and terminal
  commit cleanup, every legacy/cross-scope denial, selective release, active-
  scope corruption, reverse binding, arbitrary deletion, appended holes/suffix,
  zero allocation, and deterministic 512/4,096 scaling. Frozen accepted hashes
  are `private_page_pool.go`
  `3eb1c53c8a759d1b17e1ac5e499ac3cbceadf6baaadaf626a57b1722f3963cca`
  and `private_page_pool_binding_test.go`
  `03b16cc5d6bede19441cb65831733164c59198b29b9ac1684010902539c234fd`.
  Twelve focused tests repeated fifty times, full, race, vet, 32-bit, formatting,
  whitespace, compiler-escape, allocation, scaling, and forbidden-mechanism
  gates pass.
- Mirrored and independently accepted late-binding dependency step 1 in Rust.
  The pool preserves the legacy all-bound constructor while adding vacant
  caller-owned capacity, move-only exact scope authority, global and per-scope
  AVL indexes, O(1) scoped vacancy, bind/unbind and pending-tail transitions,
  complete checkpoint restoration, prospective forward/terminal epoch
  headroom, strict rejection of every unscoped legacy operation while any scope
  exists, and monotonic invalidation of every checkpoint-touched capability.
  Restoring the old authority epoch was deliberately removed because it
  resurrected authority after intervening transitions (ABA); bitmap and
  retirement callers reacquire short-lived authority and do not depend on that
  behavior. Independent review rejected the first green version because
  rollback restored page state but left global and scoped AVL aggregates stale.
  The repair gives commit and rollback one infallible zero-allocation terminal
  rebuild: one anchor scan, one bottom-up global traversal, and disjoint active-
  scope traversals, bounded by `2N` unscoped and `3N` fully scoped. Deep global
  and permutation-rotated scoped tests now compare every link, height,
  aggregate, lowest result, restored byte/state, and stale capability after
  claim/transfer/return rollback. Frozen accepted `private_page_pool.rs` hash is
  `ba1f6a108c76984981c3e2bd99d48b203df2c11fc6c7421f3508bba9c53b68c3`.
  Twenty-nine focused tests repeated fifty times, 250 default, 250 no-default,
  342 all-feature tests, strict Clippy, benches, formatting, whitespace, explicit
  zero-allocation rollback, 512/4,096 scaling, and forbidden-mechanism gates
  pass; bitmap, reservation, and retirement files were unchanged.
- Completed and independently accepted Go late-binding dependency step 2. A
  scoped bitmap COW now starts over vacant capacity, synchronizes exact physical
  bindings and pending page count after the locked bind step, isolates foreign
  scopes, uses exact scoped operation authority for repeated direct removal, and
  consumes only the caller-selected 0/partial/all prefix of verified committed
  candidates. A selected candidate reversibly changes from PlannedCandidate to
  Arena so it can fund the COW that clears its own free bit; unselected
  candidates remain planned, and unbind/rollback plus resynchronization restores
  the exact role. Stable caller-owned arena-binding metadata separates storage-
  node identity from active-node identity. The first green version was rejected
  because sync trusted mutable `poolSlot`/`storageNode` fields and accepted
  duplicate aliases that could reuse an AVL node and create a cycle. The repair
  derives canonical mapping from the live pool on every validation/sync: the
  k-th ascending exact scope member must name that pool slot and constructor-
  fixed storage node, while ordinary and candidate active nodes each have one
  exact independently derived identity. The permanent alias matrix covers
  duplicate/reordered pool slots and storage nodes, ordinary/candidate active-
  node aliases, candidate fingerprint corruption, and storage/candidate
  masquerading; every case returns within a bound, preserves complete COW/pool/
  scratch state, and succeeds after repair. Frozen accepted hashes are
  `private_page_pool.go`
  `65d3a3718831bdccfc4533486ede87a95e26805b381a6a7fd04f6bf37596a290`,
  `private_page_pool_binding_test.go`
  `e7a74505776e1f27ac2d775361e929dcdfa06bb0612457a9a04bc1388a327059`,
  `bitmap_cow.go`
  `1be63bc361f5722f0915925fe1ef5ec594ae21d04273d15d95fc20fa6fbef14d`,
  and `bitmap_cow_scoped_test.go`
  `76da5268520c569f59bd2b534bec7167f8780439986302be56f825056e0fab37`.
  Focused tests repeated fifty times, full, full race, vet, 32-bit, formatting,
  whitespace, compiler-escape, zero-allocation, 512/4,096 scaling, and forbidden-
  mechanism gates pass.
- Mirrored and independently accepted late-binding dependency step 2 in Rust.
  Scoped bitmap COW now supports vacant and partially bound exact scopes,
  post-bind page-count synchronization, foreign-scope isolation, selected
  committed-candidate prefixes, reversible PlannedCandidate/Arena remapping,
  and repeated direct operations through move-only exact-scope authority. A
  freeze audit found that valid caller node numbers were not yet proven to have
  their exact roles; the repair fixes ordinary active nodes to their storage
  nodes, selected candidates to their dedicated candidate nodes, and requires
  every unused storage node to be empty. The expanded alias matrix rejects
  moved ordinary/candidate nodes, occupied candidate storage, and candidate/
  storage masquerading without changing COW state, then succeeds after repair.
  Independent source review confirmed that canonical mappings are rederived
  from live scoped pool slots, every fallible check precedes deterministic
  mutation, operation authority is single-use, and foreign, stale, aliased, and
  replayed state cannot enter apply. Frozen accepted hashes are
  `private_page_pool.rs`
  `b47e1b47278f1791d5019210df7b89b3c4503b47c8cbbdb4383f22b1f3464c24`
  and `bitmap_cow.rs`
  `984f0feb9b7f56edeb613d4caf06157b8f916c2823512b7cb7db0a15f8624ca8`;
  protected `reservation.rs` and `retirement_writer.rs` remained unchanged.
  Sixteen focused tests repeated fifty times, 264 default, 264 no-default, 356
  all-feature tests, strict Clippy, bench/all-target compilation, formatting,
  whitespace, explicit zero-allocation repeated removal, and zero-allocation
  512/4,096 balanced-index scaling pass. Scoped insertion/finalization remains
  deliberately disabled until step 5.
- A read-only dependency audit fixed the exact boundary for late-binding step 4.
  The current Go and Rust retirement arenas are still global-pool clients, so
  they can select or classify pages owned by another fixed-point participant.
  Step 4 will bind each retirement arena to one already-bound exact reservation
  scope, enumerate that scope in ascending page-number order, fingerprint every
  planned destination tuple (scope, storage slot, page number, and binding
  epoch), and preflight exact-scope destinations, releases, transfers, scratch,
  ledgers, epochs, and rollback headroom before one global pool checkpoint. The
  global checkpoint remains only the rollback journal; every claim, read,
  write, return, transfer, and generation release is scope-qualified. Global
  bound-page visibility is read-only and is used only to reject committed-tree
  aliases with any private page. Unbound slots are skipped. Selective scope
  closing/finalization remains a later fixed-point integration step. Permanent
  tests must cover interleaved and reverse-bound scopes, foreign-lowest-page
  isolation, own-scope exhaustion despite foreign capacity, binding-epoch
  drift, foreign destination/release/transfer rejection, rollback after each
  mutation class, exact-scope generation cleanup, page-zero/unbound slots,
  global collision detection, zero allocation, and linear or `O(k log n)`
  512/4,096 scaling. No separate scoped checkpoint or allocator is required.
- Go step 3 exposed a real shared reservation-bound defect before freeze. The
  old planner bounded `payload + final surviving bitmap metadata` and the peak
  metadata count separately, which can under-reserve a partial committed-
  candidate prefix. With payload two and committed candidates 5 and 9, clearing
  only 5 leaves one bitmap page alive and requires three simultaneous private
  pages even though clearing both candidates later collapses that page. The
  correct bound is the maximum of `payload + surviving metadata` over every
  prefix. Go now tracks that exact prefix peak; Rust has the same formula in its
  still-unmodified reservation planner and must mirror the correction in its
  step 3. The permanent Go case also merges committed `{5,9}` with proven
  reclaimed `{3,7}` and requires globally lowest `{3R,5C,7R}`, zero speculative
  append, and capacity three.
- Independent review rejected the first frozen Go step-3 implementation despite
  74 focused cases, full/race/32-bit/vet, scaling, and zero-allocation gates.
  Its `finishCapacity` still initialized and replaced the whole caller pool and
  immediately reserved its only scope. It therefore could not attach to the one
  already-initialized transaction pool after the coordinator reserved foreign
  bitmap/retirement scopes; reuse would wipe them, while separate pools would
  duplicate physical ownership and violate global-lowest allocation. The
  mandatory repair splits pool-independent capacity/source planning from a
  non-mutating attachment to an existing exact vacant scope after all scopes are
  reserved. A permanent foreign-scope sentinel must prove that attach/bind
  preserves foreign slots, bytes, roots, scope identity, epochs, pending tail,
  and page ownership. Review also found that staged selected-page scratch could
  alias live replacement scratch: selection cleared the live ledger before a
  later fingerprint rejected it. The repair must reject every same-typed plan/
  live/stage/proof slice overlap before any stage write and prove exact-state
  preservation. Representative deep and post-bind insertion/finalization tests
  must use the real capacity/attach/proof/bind path rather than the test-only
  all-bound adapter. The rejected hashes are evidence only, not accepted
  milestones.
- The repair keeps one exact reservation/work-unit scope for the pages described
  by one capacity plan. Its `privatePages` is the worst simultaneous bitmap COW
  metadata plus that call's explicit payload pages; the step-4 retirement arena
  consumes available payload from this same scope, and step-5 finalization runs
  only after those consumers finish. The shared transaction pool may contain
  foreign scopes for other reservation work units, all of which attach/bind and
  finalization must preserve. Retirement payload already counted in a plan is
  not counted again in a second scope. Sequencing multiple work-unit scopes and
  forwarding the remaining eligible-source authority belongs to the later
  fixed-point coordinator; step 3 must expose the attachment boundary without
  claiming that integration is already complete. Step-3 attachment accepts only
  the exact initial predecessor: the committed bitmap/root, pool incarnation and
  mutation epoch, unchanged eligible candidates, and pending tail equal to the
  planned committed page count. A previously consumed eligible candidate or a
  changed tail is a typed stale-predecessor rejection; step 3 must not pretend it
  can continue by silently skipping that source. Step 6 must implement the real
  linear chain: single-consume each predecessor, replan/resynchronize from the
  current draft, exclude pages already owned by earlier scopes, append from the
  live tail, bind proofs to the exact work unit, and issue one successor for the
  next unit. Two independently preplanned work units must never be allowed to
  select the same physical page.
- The read-only step-6 audit proved that the successor chain cannot reuse a
  committed-only page reader. A later draft can reference private pages owned by
  several earlier scopes, so the coordinator needs a provenance-aware draft
  source that classifies each copied page as selected-generation committed or as
  an exact private work-unit/scope/slot/binding. Replaced committed pages enter
  the transaction's retirement/direct-free outcome; replaced earlier private
  pages return only to their owning scope. Carried source entries already
  consumed by an earlier unit are skipped monotonically, but a page that the
  current draft still advertises as free while already owned is an invariant
  failure, not another skip. Later work units receive only a pre-lock worst-case
  capacity bound; exact source selection occurs under the live lock after their
  predecessor exists, with no allocation. Planning/preflight failure leaves the
  predecessor retryable. Any failure after private mutation that is outside one
  composite rollback journal aborts the unpublished transaction and issues no
  successor. The normative allocator wording now distinguishes pre-reserved
  capacity/scratch from physical page identities and defines current-draft
  provenance and single-use sequencing.
- The read-only step-5 audit proved that selective finalization is a lifecycle
  transition, not a scope-filtered release loop. The current Go and Rust
  finalizers scan the whole pool, omit unused safely reclaimed pages, update the
  COW page count without synchronizing the shared tail, use unscoped mutation
  helpers, and retain an active candidate-prefix invariant that cannot represent
  a legal terminal scope. Step 5 must therefore use one shadow/preflight and
  mechanically infallible exact-scope apply: reinsert unused committed and
  reclaimed pages plus appended holes, recursively handle COW demotions, truncate
  only the globally contiguous unused appended suffix in descending order, and
  repeat to a stable fixed point. It then seals rather than closes the scope,
  bumps every retained binding/authority generation, invalidates all earlier
  bitmap/retirement/mutation handles, preserves read-only output bindings and
  non-tail returned-free ownership, empties active candidate/available state,
  and synchronizes the COW page count from the shared pool. Later cleanup unbinds
  retained outputs and closes the empty scope. The sealed result issues only an
  opaque single-use successor seed; Step 6 may read earlier sealed scopes but
  never reopens their mutation authority. Preparation failures are exactly
  retryable and mutation-free; anything not provably covered by the composite
  prepared apply aborts the unpublished transaction.
- Two independent reviews rejected the second frozen Go step-3 candidate despite
  full/race/32-bit/static/allocation/scaling gates. The real last source callback
  occurred inside preflight after the exact shared-pool generation/mutation fence
  and after shadow construction. A successful callback could therefore mutate a
  foreign scope or an in-use staged page; own-scope validation did not recheck the
  proof-bound global epoch, and staged page bytes were copied live without a
  post-callback identity check. The existing failure test stopped at callback
  five inside shadow construction and never exercised vulnerable callback six.
  The repair must make the source check immediately after proof consumption the
  genuinely last callback, use callback-free after-access shadow construction,
  and perform every pool/live/stage/proof fence after it. Review also proved that
  production `reserveCandidate` still required the legacy all-bound `arena` even
  though only the `_test.go` adapter uses it, and the returned capacity value
  retained the legacy pool/arena pointers. Production capacity planning must
  succeed with both absent and must sanitize them from its returned authority.
  Permanent tests must mutate a foreign non-source scope and poison stage scratch
  on the last successful callback, prove the post-callback fence/rebuild and exact
  live atomicity, prove that no callback sees a constructed in-use shadow, and
  attach/bind a nil-legacy-storage plan to an external shared pool. The rejected
  hashes are evidence only and are not an accepted milestone.
- The first independent review accepted the next Go step-3 repair and confirmed
  that both prior blockers were removed, but the second independent review found
  another real post-callback authority gap. The final source callback can mutate
  caller-reachable attachment state. The immediate stage validator did not first
  prove nonnegative bounds or a non-nil/live COW pool, so corrupting
  `privatePages` or `cow.pool` could panic after the reclamation proof had been
  consumed instead of returning a typed atomic rejection. The same gap let a
  callback advance a foreign scope and rewrite the attachment's mutable cached
  pool generation/mutation fields to conceal that drift because preflight did
  not bind those fields back to the proof-bound reclamation request. The repair
  must run one nil- and bounds-safe complete attachment-invariant check
  immediately after the genuinely last callback, authenticate all scalar and
  pointer identities plus pool-generation fields against immutable request or
  attachment commitments, and only then slice or dereference mutable state.
  Permanent check-four callback tests must cover negative/private capacity,
  nil or substituted pool/source pointers, and concealed foreign-scope drift,
  proving typed failure without panic, exact live preservation, and deterministic
  retry behavior. The rejected hashes are evidence only, not an accepted
  milestone.
- A read-only Rust step-3 audit confirmed that the current implementation still
  uses the rejected eager model: its capacity plan owns physical arena storage,
  assigns speculative appended page numbers, constructs a separate all-bound
  pool, uses the old final-prefix capacity formula, and can invoke source access
  again while applying the planned reservation. Rust step 3 therefore requires
  the full capacity-plan -> existing exact-scope attachment -> move-only
  reclamation proof -> final source fence -> stack-local commitment -> callback-
  free shadow -> prepared composite live-bind pipeline. The accepted Rust pool
  and scoped-COW foundations are sufficient, but the pool first needs an opaque
  exact-scope commitment, a read-only global collision lookup legal with active
  scopes, and one preflighted composite bind whose mutation suffix has no
  fallible branch. The Rust mirror must correct the prefix peak, preserve foreign
  scope semantics, make proof/request capabilities single-use and non-`Copy`,
  bind every mutable identity to callback-inaccessible local state, and prove
  zero allocation plus deterministic 512/4,096 scaling. Step 4 and step 5 are
  not prerequisites; step 3 must return one valid active scoped COW with its
  payload capacity for those later consumers.
- Completed and independently accepted Go late-binding step 3. Capacity
  planning is pool-independent, retains no legacy pool/arena authority, uses
  the maximum `payload + surviving metadata` over every committed-candidate
  prefix, and attaches non-mutatingly only to the exact initial vacant scope in
  the coordinator-owned shared pool. The locked bind consumes one authenticated
  reclamation proof, merges committed and verifier-proven reclaimed pages by
  globally lowest page number, appends only the remaining deficit, constructs a
  callback-free shadow from verified pages, then runs one preflighted
  mechanically infallible live apply. The final source access now runs through
  a source value sealed in callback-inaccessible stack state. Before proof
  consumption, one complete bounds-safe seal commits the plan, live/staged
  slice headers and backing identities, COW authority, source identity, pool and
  exact-scope state, request, ticket, and proof. Immediately after the callback,
  nil/scalar/pointer/header checks precede every mutable slice or dereference,
  followed by content and live-epoch verification. This closes the reported
  panic, pointer substitution, cache-concealment, and post-callback stage-poison
  paths while keeping the locked path allocation-free. Permanent tests cover
  negative capacity, nil/substituted pool and source, non-comparable immutable
  source identity, legal foreign-scope mutation with every mutable cache edited
  to conceal it, proof terminal/replay behavior, exact failure atomicity, and a
  fresh retry.

  One independent review initially rejected direct raw mutation of a foreign
  private pool slot. After contract adjudication it withdrew that rejection:
  the only production committed sources are package-private and hold no pool
  authority; external packages cannot implement the interface's unexported
  method; and every legal pool mutation changes a sealed generation, mutation
  epoch, or lifecycle field. Hashing every foreign page for every work unit
  would scale with the complete shared pool and could make the multi-work-unit
  chain quadratic, contradicting the exact-scope bounded-work contract. The
  durable encapsulation invariant is that a committed source must never receive
  raw pool or page-storage authority; arbitrary same-package field writes or
  data races are not an allocator operation or supported recovery boundary.
  Frozen accepted hashes are `bitmap_reservation.go`
  `8ff622ea21a33fe74d597e36de4c25cc0e1f027e165a84834d5f8ff4f3e3eb38`,
  `bitmap_reservation_late_binding_test.go`
  `e71cd22658fbcc164a2623b000152010ab88e3a192fed497d8196e44acfc3226`,
  `bitmap_cow.go`
  `e0f8d4d7e6915829baf88435af9b74ac53b3744ae1afb4294732aa8c9b5f8c46`,
  `private_page_pool.go`
  `0b7924c2e2769629b6f2ac340d6fc440d4066a8a1d1de7c1481d246d805604cc`,
  `bitmap_reservation_insert_test.go`
  `2111f1449cc1789d7efaf3ef676de4e04ce7308f3c8fb81ded57d87c4a0b7f20`,
  and `bitmap_reservation_all_bound_compat_test.go`
  `0b722236fe1aeb4c8d156c908155df3e38f9e6b69d0cfbf117b358c484377c7f`.
  Two final source reviews accept the frozen bytes. Focused tests repeated fifty
  times, full, full race, vet, 32-bit, formatting, zero-allocation, proof/replay,
  and adversarial callback-mutation gates pass.
- A later Rust step-3 implementation and two independent reviews exposed a
  performance defect that also invalidates the performance portion of the
  accepted Go step-3 milestone. In both languages, production exact-scope COW
  attachment discovers a small work-unit scope by scanning the complete shared
  transaction pool. Candidate removal also revalidates the complete pool and
  target scope for every selected committed page, making the locked path
  `Theta(k * (N + s))` instead of bounded by the exact scope and logarithmic
  indexes. The existing 512/4,096 scaling test did not expose the second defect:
  reclaimed pages sorted before the committed pages, so `committed_rank` stayed
  at one instead of growing with the test size. Rust attach additionally records
  vacant slots in global slot order while composite bind requires the scope's
  canonical vacant-chain order; a previously unbound/reordered scope can
  therefore consume the reclamation proof and final callback before rejecting
  the bind without mutation.

  The repair is required in both implementations before step 3 is accepted for
  production: the pool must expose an exact-scope enumerator in canonical bind
  order without scanning foreign slots; locked candidate removal must perform
  one complete scoped validation followed by preverified logarithmic removals,
  not one complete validation per page; attach must prove bindable scope order
  before it issues the one-shot request; and permanent scaling tests must cover
  a tiny target scope inside a 512/4,096-page foreign pool plus an all-committed
  512/4,096 case where `committed_rank == k`. The rejected Rust hashes are
  evidence only: `private_page_pool.rs`
  `914eda83e71190e3b47460a8a4c4910cc6c437bf3224372f3772ea18a86685de`
  and `bitmap_cow.rs`
  `0d75d5593bcf1c94d94c6921db842a767b88796c9a8676615052770f45fed55f`.
- The first Rust repair of that defect fixed exact attachment and batch removal
  but was rejected again because it measured too late. Live shared-pool
  `reserve_scope` still walked all slots once to find capacity and again to
  assign it; `close_scope` likewise used two complete shared-pool scans. The
  new tiny-scope test started its counter only after creating all scopes, so it
  proved attachment cost while hiding lifecycle cost. Creating and closing `k`
  small work-unit scopes in an `N`-slot transaction therefore remained
  `Theta(kN)`. Both languages must maintain an indexed unscoped-vacancy source,
  reserve in deterministic slot order without a foreign scan, and close through
  the exact immutable member chain. Lifecycle counters must include reserve,
  attach/use, and close. The second rejected Rust hashes are evidence only:
  `private_page_pool.rs`
  `4d68aa9253988813295d55519b81daa4d919c20779b0b62aacbb81e2727a74b2`
  and `bitmap_cow.rs`
  `b5bbcddd11615240559f7fab08f27dfd527f82b95d5c9689dacbca012c36cc11`.
- The next Rust lifecycle repair replaced those full scans with an intrusive
  vacancy FIFO and exact member chains, but final review rejected the frozen
  green candidate. Reserve and authorize could publish an unvalidated adjacent
  vacancy after removing the checked node or prefix; ordinary scoped bind and
  unbind had the same boundary-transition defect. Scope close also validated
  the vacancy and member chains independently without proving that they were
  the same exact slot set, so a retagged foreign vacancy could be substituted
  and laundered. Each transition needs `O(1)` canonical successor/predecessor
  preflight, while close needs an exact `O(k)` permutation proof and atomic
  marker cleanup. The rejected hashes are `private_page_pool.rs`
  `7ee4a2d0a5e09a08c72db8c13046429307a7e2dcbc39e9d00f80d5703c662286`
  and `bitmap_cow.rs`
  `896ccbf9fdeeec75a7bed02ccb10ab5d5fca12fc6f7a2d52ecdff04fdc9f4a07`.
- Two independent final reviews accept the repaired Rust Step-3 lifecycle and
  late-binding pipeline. Every queue transition validates the exact adjacent
  state it will publish before mutation; close proves exact equality of the
  immutable member and mutable vacancy sets with reversible ordinal markers;
  nonmonotonic scope reuse retains stable ordinals; and an active-foreign-scope
  512/4,096 test measures the complete two-page lifecycle at constant exact
  visits with zero allocation. Request/proof authority remains move-only, the
  final callback precedes reauthentication and callback-free staging, and the
  composite live-bind suffix is mechanically infallible. Default and
  no-default suites pass 296 tests each, all features pass 388, focused late
  binding passes fifty repetitions, and strict Clippy/format/hash gates pass.
  Accepted hashes are `private_page_pool.rs`
  `66345790dfa1d7e91277760482c139975f1f27942151b293468a4daf3c2b515b`
  and `bitmap_cow.rs`
  `b67cb266dff2282c2bbc45a02bc330715317e82879ec81a320d3f9e67a1261e5`.
  Legacy global checkpoint/finalization helpers still contain pool-wide loops;
  later shared-pool steps must replace rather than reuse them.
- Two independent reviews rejected the first Go step-4 scoped-retirement
  candidate despite focused tests repeated fifty times plus full, shuffle,
  race, vet, 32-bit, benchmark-build, allocation, and scaling gates. The low-
  level exact-scope page selection, foreign-capacity isolation, tuple
  fingerprinting, scoped generation cleanup, and complexity were sound, but the
  operation boundary was not. Combined delete/upsert opened the pool checkpoint,
  wrote delete results, and only then performed further fallible source checks,
  blob scans, upsert planning, and headroom validation. Standalone upsert and
  delete also performed their final source callback after validating mutable
  arena/scope/path/ledger state but before opening the checkpoint; a callback
  could substitute another valid scope or alter caller scratch, after which
  apply could write and commit the wrong scope. In the combined path the same
  substitution could make rollback target the wrong scope and leave a partial
  active checkpoint.

  Review also found that adjacent scoped bitmap code still borrowed a scoped
  token and then used global read/write/release helpers, while bitmap-to-
  retirement transfer used global borrow/transfer and therefore deterministically
  failed whenever the required Step-3 scope was active. The scoped retirement
  writer's `writePage` ignored lookup success, silently returned on a scope
  mismatch, and discarded its prepared-write error instead of mechanically
  encoding an infallible destination. Finally, exact-scope budget failures
  reported global pool capacity, which is false in the presence of foreign
  scopes. The repair must mirror the already accepted one-shot retirement
  architecture: side-effect-free standalone and combined plans, a virtual
  delete overlay, every source read and final access check before one immutable
  stack-sealed apply commitment, no callback or fallible discovery after the
  checkpoint starts, exact prevalidated destination descriptors with infallible
  writes/terminal commit, exact-scope budget evidence, and scope-qualified
  bitmap read/write/release/transfer throughout. Permanent tests must exercise
  real scoped upsert, delete, and combined operations plus scope/path/ledger
  mutation at every callback boundary. The rejected hashes are evidence only,
  not accepted milestones.
- Review of the in-progress Go step-4 repair found that its initial callback
  guard was still unsafe and globally scaled. It hashed every shared-pool slot
  for each source callback, dereferenced `arena.pool` before proving that a
  callback had not nulled or substituted it, omitted the blob token's arena
  pointer, and omitted the role index's plan-sequence/active-plan authority.
  Those omissions could respectively reintroduce complete-pool work, panic on a
  failing callback, stabilize or discard the wrong token arena, or let a
  callback forge plan lifecycle state. The repair candidate cannot freeze until
  it uses a nil-safe exact-scope commitment, authenticates every pointer/header
  before dereference, binds the post-consumption lifecycle state, and has
  permanent final-callback tests for each mutation class.
- The next green Go step-4 candidate fixed those callback, one-shot apply, and
  shared-pool scaling defects, but implementation self-audit rejected it before
  independent review. Scope reservation validates the selected prefix of an
  indexed unscoped-vacancy queue and scope closure follows the exact immutable
  member chain, so lifecycle work is now `O(k)` rather than `O(N)`. However,
  closure did not prove that its mutable scope-vacancy chain contained every
  exact member exactly once, and reserve/close did not fully reject stale active
  checkpoint tags or noncanonical reusable payload state. A close could
  therefore launder corrupted allocator state into the global vacancy queue.
  The repair must validate the exact member/vacancy permutation and every
  lifecycle-relevant reusable-slot invariant in `O(k)`, reject corruption before
  mutation, and retain the no-default-whole-pool-validation rule. The frozen
  green hashes are rejection evidence only: `private_page_pool.go`
  `95c209546a77d7a252b0d1f1bde750307ad17aa72f092b7a724f029c70f1e9c5`,
  `retirement_plan.go`
  `ac0f861e38480a71b8e6e9e8fb0a13c087466b089ee3844209e8f882dd700786`,
  and `retirement_writer_scope_test.go`
  `79bf65d8aac25dd234c71b63e8689b87ab9b285cb33dd5464d8779a55da0f2b1`.
- Review then rejected the repaired Go lifecycle candidate because reservation
  validated the removed prefix but not the first unselected slot before making
  it the new global vacancy head. The same-failure search found equivalent
  unchecked successor publication in ordinary scoped bind and unbind. The
  repair must preflight every newly authoritative queue boundary before the
  first write, remain `O(k)` and allocation-free, and prove atomic rejection
  with corruption immediately beyond the selected prefix. The rejected
  `private_page_pool.go` hash is
  `f95db7e8eeae0ef7bac4eaa55c309491fc7c55a81f2d73d877b7a079b3884a19`.
- Independent review accepted that queue-boundary repair and the scoped
  retirement one-shot plan, but rejected late bitmap binding's terminal step.
  It still called the global checkpoint commit, which scans every shared slot
  and rebuilds the complete global and per-scope indexes. Repeated small-scope
  binds therefore remained `Theta(kN)` even though the visible lifecycle and
  candidate counters were linear. The repair needs an exact touched-node
  terminal journal or an equally bounded preflighted non-checkpoint apply; the
  existing scoped commit is insufficient because global AVL rotations may
  touch foreign-scope index nodes. The rejected hashes are
  `private_page_pool.go`
  `918ea5f74109e5dba1001f34f54eaa402eed30c44d059a0504a42eb2c17b2aff`
  and `bitmap_reservation.go`
  `342942835e4157b71d26d8d5ca21bf431e11dd8ef3b92092c9dd8269692e27f1`.
- Two independent final reviews accept the repaired Go shared-pool lifecycle,
  late bitmap binding, and scoped retirement pipeline. An intrusive checkpoint
  journal records each global or scoped AVL node exactly once before its first
  mutation, including foreign-scope nodes on global rotation paths. Terminal
  commit walks only that journal, the exact target scope, and its anchor; it
  performs no global commit or index rebuild. The complete attach-and-bind test
  keeps large prefix/suffix foreign scopes active at 512 and 4,096 slots,
  measures bounded exact work, verifies every checkpoint tag/link is cleared,
  and preserves foreign semantics and valid AVL counts. The queue lifecycle
  remains `O(k)`, close proves exact member/vacancy equality, candidate removal
  uses one scope validation, and scoped retirement retains its callback-safe
  one-shot plan. Full Go, race, vet, 32-bit, formatting, focused repetition, and
  hash gates pass; the corpus contains 410 top-level and 879 total test cases.
  Accepted hashes are `private_page_pool.go`
  `de9d1087ce9b9c14f0919029f3437522fa3d2ce1341cbd7df66b190290d94fab`,
  `private_page_pool_binding_test.go`
  `907f02d8246b921b8f03bbf8d7c4429d49859b918fd3ac8eabc727b2dbf23d89`,
  `bitmap_reservation.go`
  `6ad4de999145cc8319567d435aa9fdfd0b17d100f4420d3529cefcf5d5fd503c`,
  `retirement_plan.go`
  `b87383c88f588958062ff34ed7ba0af5d1329785d32b43db5915879ccb860b97`,
  and `bitmap_reservation_late_binding_test.go`
  `724e8e498507abfe3ad890f46ebfe76aa1d9f7a6347fa118a13418a9ee6a98d7`;
  `bitmap_cow.go`
  `62653d772c62a75ca85b485ce58640a7eba0c8829351a0259d3a51e3a510b595`,
  `bitmap_insert.go`
  `08584240ff33008bcd35d5c17c6d0fc4059d813c77be04fcec816361b23507c8`,
  `retirement_writer.go`
  `8edcd5351b2869d214c15a44540087d3d545e98e34c38799feaf653450416f0d`,
  and `retirement_writer_scope_test.go`
  `79bf65d8aac25dd234c71b63e8689b87ab9b285cb33dd5464d8779a55da0f2b1`.
  Legacy unscoped global commit/rollback/rebuild helpers remain and must not be
  reused by later shared scoped paths.
- Rust validation also proved that the project command using
  `--features slow-tests` was stale after the obsolete implementation was
  removed: the exact-v4 crate exposes only `std`, `alloc`, and `os`, and Cargo
  rejects that feature name. `AGENTS.md` now uses `--all-features`, which runs
  the broader supported feature matrix; the rejected command is not an
  implementation failure.
- Selective-finalization integration reopened part of the accepted Go scoped
  milestone. Scoped bitmap insertion changed a released slot's authority epoch
  without refreshing its binding, so the next exact operation rejected its own
  valid draft. The finalizer also demonstrated that terminal state needs a
  separate candidate-free index and exact sealed-scope cleanup rather than the
  active candidate-prefix invariant. Same-failure analysis then found that the
  Go retirement callback guard, and the in-progress Rust port of it, hash the
  complete target scope and caller scratch around every committed-page read.
  Large retirement blob work is therefore quadratic in target work even though
  foreign-pool scaling remains bounded. The accepted hashes above remain useful
  historical evidence, but Go bitmap insertion/scoped retirement are reopened
  until binding refresh, constant-time callback guards, large-batch scaling,
  and the full prior acceptance matrices are independently revalidated.
- The first frozen Rust scoped-retirement repair passed formatting, strict
  Clippy, 304 default tests, 304 no-default tests, 396 all-feature tests, and
  focused repetition, but independent structural review rejected it. Each
  scoped private metadata-page read revalidated the complete exact-scope
  commitment, including every bound page's 4 KiB payload. Traversing a private
  retirement blob with target scope size `k` therefore performed `Theta(k^2)`
  hashing. The existing scaling tests covered a fixed three-page target with a
  growing foreign scope and a growing committed unscoped blob, so neither
  exposed this target-scope defect. Rejected hashes are
  `private_page_pool.rs`
  `afe3a0e8e8e8eeaea384c0436c7c80b809ed277ff5a635d6a45ccc926c9f907c`,
  `retirement_writer.rs`
  `b30a12b8398c4d48e6a5efa445df9b3db8b591edd06dd0f868372f760816d734`,
  and `bitmap_cow.rs`
  `22a66bd03decd4a91093180189f8be94d68b54a027ab9d821a3a466daf25438b`.
  Rust Step 4 remains open until exact-scope commitment is established at a
  bounded operation fence rather than per private-page read, a real 512/4,096
  private-target test proves linear or `O(k log k)` work with zero allocations,
  and the complete matrix and independent reviews pass again.
- The second frozen Rust repair removed that quadratic scan and added a real
  active shared-scope private-blob planner proof, but both independent reviews
  rejected its replacement fence. The new constant-time read check assumed
  every legal pool mutation advances the pool epoch; the safe scoped mutable
  page guard can change and restore page bytes without doing so. A source with
  retained valid authority could therefore expose transient private metadata to
  planning, restore it at the final callback, and evade both the epoch guard and
  final full seal. The same review also found that scoped reads searched the
  global page AVL (`O(log N)`) rather than the exact scope AVL (`O(log k)`).
  Rejected hashes are `private_page_pool.rs`
  `48b42e55a8058daabb9df8fc9fc6147d4689629eff37a56d1eb1fca00b991a5c`,
  `retirement_writer.rs`
  `80c6c73e06fc23da8c8b96b085e85255ff2209eac7803d95d1391e2cd45e4183`,
  and `bitmap_cow.rs`
  `22a66bd03decd4a91093180189f8be94d68b54a027ab9d821a3a466daf25438b`.
  Rust Step 4 remains open until every safe mutable-page path is epoch-visible
  or excluded by an exact lease, scoped reads use target-local lookup, safe
  mutate/restore and foreign-size scaling tests pass, and new frozen bytes pass
  the complete matrix plus independent review.
- The mutation repair follows the existing global invariant rather than adding
  a retirement-only lease: successfully acquiring any mutable page guard
  advances the pool epoch before writable bytes are exposed. This conservative
  dirty-on-borrow rule may invalidate a snapshot even when the caller does not
  ultimately change bytes, but it is simple, globally auditable, and makes the
  already documented `mutation_epoch` contract true. Advancing only on first
  `DerefMut` was rejected because it adds hidden guard state and side effects;
  a planning-only read lease was rejected because it leaves the pool-wide
  invariant false. All mutable-borrow callers and exact epoch-headroom formulas
  must be audited and tested before this repair can be accepted.
- The first frozen Go selective-finalization candidate passed normal, race,
  vet, 32-bit, allocation, scaling, formatting, escape-analysis, and repeated
  adversarial gates, but independent structural review rejected it. Terminal
  cleanup and appended-tail unbind removed touched slots from the scope AVL
  before scoped checkpoint commit traversed that AVL to clear slot checkpoint
  tags. The returned global vacancies therefore retained live checkpoint IDs
  and failed canonical reuse. This also exposed the same missing touched-slot
  discovery in ordinary scoped unbind/rollback. Review found two additional
  blockers in the same frozen bytes: source errors were returned before the
  complete live/cache fence could give persistent callback drift precedence,
  and caller-owned finalization scratch was written without proving it did not
  alias live candidate, replacement, validation, or index buffers. Rejected
  hashes are
  `private_page_pool.go`
  `777869c8d63ff7fd9c39f48a56faee89dc4957d1ea91feb44f9d30b66adb3a6b`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `cfc765942e0f6d86da570ad9eb4e3bd985615fbd43a040a4838ec471f28c2766`,
  `bitmap_finalize.go`
  `b7926ae732eefe42229c95360e724215d8ca7c2568e4e3fdb91ee996f1084026`,
  and `bitmap_finalize_test.go`
  `c1e3c9fa83a1feda7942b60437e7be0b02c877ae97f23164dbc042eb18e7c51e`.
  Go Step 5 remains open pending an exact intrusive touched-slot journal (or an
  equally bounded complete mechanism), pre-write scratch alias rejection,
  callback drift/error precedence, canonical post-cleanup reuse tests,
  tail-unbind coverage, and renewed full matrices plus independent reviews.
- The third frozen Rust scoped-retirement candidate implements the selected
  pool-wide dirty-on-mutable-borrow rule, exact checkpoint write/cleanup
  headroom, target-scope AVL lookup, callback mutate/restore rejection, and
  fixed-target/foreign-size work accounting. The implementer reported clean
  formatting, strict all-target/all-feature Clippy, 311 default tests, 311
  no-default tests, 403 all-feature tests, and fifty repetitions of the scoped
  retirement and mutate/restore regressions. Independent reproduction confirmed
  the three hashes, formatting, strict Clippy, and all three feature matrices.
  Frozen hashes are `private_page_pool.rs`
  `ef97482d16c241518eeb30743bb626d143967e61a36e7b5e352bd56aea914f5b`,
  `retirement_writer.rs`
  `df3016af31440068dfdc72d98b02628a20fab1fea6f1dec4dde0c59eabd23e28`,
  and `bitmap_cow.rs`
  `440de270a7e4961085de982b0fdf9c0a4e66dcca2a23cc76772dadfe4083790a`.
  Zero-trust source review rejected these bytes despite the green matrices.
  Unscoped retirement edit apply reserves two epoch steps per staged release,
  but the actual safe path spends three: authority refresh, pending return, and
  checkpoint commit finalization. At the exact `u64::MAX` boundary the short
  reservation can therefore begin the checkpoint, mutate, and reach an
  internal `expect` on exhaustion instead of rejecting atomically with a typed
  error. Existing unscoped boundary coverage had an output page but no staged
  release. Rust Step 4 remains open until unscoped releases reserve three steps
  while scoped releases retain two, exact/one-short staged-release tests pass,
  and new frozen bytes pass the complete matrices plus independent reviews.
- The fourth frozen Rust candidate corrects that confined rejection. Unscoped
  staged release now reserves three epoch steps per page; the scoped prepared
  path remains two. A real two-release test succeeds from `u64::MAX - 8` exactly
  at `u64::MAX`, while `u64::MAX - 7` returns typed `EpochExhausted` before
  checkpoint creation and preserves epoch, generation, page bytes/state,
  ledgers, and in-use count. Same-failure analysis found edit apply is the only
  non-empty staged-release caller; blob construction commits an empty release
  list. The implementer and independent reproduction both passed formatting,
  strict all-target/all-feature Clippy, 312 default tests, 312 no-default tests,
  and 404 all-feature tests. New frozen hashes are `private_page_pool.rs`
  `ef97482d16c241518eeb30743bb626d143967e61a36e7b5e352bd56aea914f5b`,
  `retirement_writer.rs`
  `c0f54e6e50738de8312dcbbc678eb223690b7524cd7743caba57ed03cd87e950`,
  and `bitmap_cow.rs`
  `440de270a7e4961085de982b0fdf9c0a4e66dcca2a23cc76772dadfe4083790a`.
  Two independent zero-trust reviews accepted these exact bytes. One reviewer
  rechecked the confined unscoped-release correction in debug and release
  builds; the other traced every mutable guard, prepared write, callback fence,
  epoch formula, scoped lookup, corruption bound, and 512/4,096 scaling path.
  Both recomputed unchanged hashes and made no edits. The Rust half of Step 4 is
  accepted; the combined step remains open pending Go acceptance.
- The second frozen Go selective-finalization candidate replaces scope-tree
  cleanup discovery with intrusive touched-slot, global-index, and scope-header
  journals. Scoped completion requires exact target semantic/header journals
  while permitting foreign structural ancestors in the shared global AVL;
  ordinary global checkpoints retain multi-scope commit/rollback. Finalization
  now rejects insufficient or same-type-aliased scratch before writes, fences
  persistent live/cache/stage drift before returning source errors, and replays
  from a sealed callback-free cache. Permanent tests cover tail unbind
  commit/rollback/reuse, two-scope global checkpoints, foreign structural versus
  semantic journal behavior, journal corruption, scratch capacity/alias cases,
  callback mutate-and-fail precedence/retry, and 512/4,096 zero-allocation work.
  The implementer and independent reproduction passed normal and race suites,
  vet, 32-bit tests, formatting, and focused regressions. Frozen hashes are
  `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `b3a7ddd130666ba8f47470af6b848dc70d69d5947b8f16778aac189a6b0f275a`,
  and `bitmap_finalize_test.go`
  `76b68ac9510a1ec767d4e8d2b31506b1fd19e956e66caf4547dca33857de5ca1`.
  Zero-trust review rejected these exact bytes despite the green matrices.
  Sealed-scope cleanup required two remaining slot-epoch increments for every
  scope member, but only the bound prefix is first unbound and then closed.
  Already-unbound suffix members need only the close increment. A tail slot can
  legally reach the cleanup as an unbound member at `u64::MAX - 1` after
  finalization unbinds and refreshes it, then close exactly at `u64::MAX`; the
  candidate rejected that legal state. Go Step 4 remains open until cleanup
  preflights bound members for two increments and already-unbound members for
  one, exact/one-short tests cover both classes, and new frozen bytes pass the
  complete matrices plus independent reviews.
- The third frozen Go selective-finalization candidate makes that exact
  correction without broadening the prior repair. Cleanup now reserves two
  slot-epoch increments for each still-bound prefix member and one for each
  already-unbound suffix member. A permanent test constructs the real
  post-finalization state and proves both exact boundaries succeed at
  `u64::MAX`, while each one-short boundary fails with typed
  `MutationEpochExhausted` before changing the pool epoch, active-scope count,
  slots, or bindings. Same-failure analysis confirmed ordinary all-unbound
  scope close already charges one increment per member and finalization apply
  already charges its own increment. Frozen hashes are
  `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `1d97c90782cd644c5d8ab18bfb618a6de44401d7fe2fed84e69435bf4307840f`,
  and `bitmap_finalize_test.go`
  `2b29614274999148776103cfc5f1b58b78f4c6c67e9e3a642dea329a2e591866`.
  The implementer and independent reproduction passed normal and race suites,
  vet, 32-bit tests, formatting, and fifty shuffled repetitions of the exact
  boundary and 512/4,096 scaling regressions. Zero-trust review accepted the
  confined epoch repair but rejected these bytes at the next structural
  blocker. Cleanup validates an already-unbound member only as an unbound
  binding and slot with page number zero; it does not prove the remaining
  canonical reusable-slot fields before enqueuing that slot globally. A member
  whose state, ownership, origin, bytes, index fields, or pending metadata is
  corrupt can therefore pass preflight and be published into the global vacancy
  queue, where a later reservation detects the latent noncanonical slot. The
  existing corruption test changed only the member-chain link and did not cover
  this semantic class. Go Step 4 remains open until already-unbound members are
  proved to be in their exact legal pre-close state, all relevant corruption
  classes fail before mutation, later global reuse succeeds, and new frozen
  bytes pass the complete matrices plus two independent reviews.
- The fourth frozen Go selective-finalization candidate closes that structural
  gap. Before mutation, cleanup now requires the exact scoped-vacancy head and
  suffix order and proves every already-unbound member with the shared canonical
  vacant-state predicate. It does not erase corruption. Seventeen permanent
  semantic/structural corruption cases plus corrupt vacancy-head and link cases
  return `ArenaPageConflict` without changing pool state; valid mixed and fully
  unbound scopes clean up and their slots can be reserved again globally. The
  same-failure audit found only two scoped-to-global transitions: ordinary
  `closeScopeCounted` already validates canonical state and exact member/vacancy
  permutation, while sealed finalization was the repaired gap. Frozen hashes
  are `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `409ad45cb6bc2b9c73cc15c7679030f3190e4f80168975c5d8d47609e084acc7`,
  and `bitmap_finalize_test.go`
  `1276fe1d5d7c0a9af0b901d093b46800e10fa6195f10a1623e5b4f3f12f2c9e2`.
  The implementer passed the full, race, vet, 32-bit, shuffled, focused,
  stress, scaling, formatting, and diff gates. Independent reproduction matched
  all hashes and passed formatting, full, race, vet, and 32-bit gates. Zero-trust
  review accepted the canonical suffix repair but rejected these bytes at the
  next bound-prefix blocker. Preflight proves bound binding identity and page
  order but not the complete global-AVL and exact-scope-AVL deletion paths.
  Terminal unbind then ignores global lookup and both delete results after the
  checkpoint has begun. A missing/corrupt global root can therefore panic after
  journal mutation; a missing/corrupt scope root can do so after global deletion
  has already mutated state. Go Step 4 remains open until bounded preflight
  proves every bound target's complete deletion paths, all root/path corruption
  returns a typed error before mutation, prepared deletion cannot panic on an
  unchecked premise, and new frozen bytes pass the complete matrices plus two
  independent reviews.

- A read-only gap audit prepared the next transaction/durability chunk without
  starting it. Existing reusable primitives cover exact meta identity and
  selection, retained no-follow file/directory identities, positional writes,
  live operation/lifetime locks, writer lease transitions, unpublished-tail
  cleanup, two-pass retirement selection, scoped private pages, and fixed-width
  replacement ledgers. No transaction owner/state shell, structured commit or
  abort result, cleanup ledger, provenance-aware fixed-point coordinator,
  durability orchestrator, writer-lease phase-5 update, or reopen resolver exists
  yet. Range mutation is also still read-only. The dependency order is:
  allocator/finalizer acceptance; mirrored private transaction/result contracts;
  draft ownership and abort; provenance-aware fixed point; allocation-free
  retained durability primitives; composed commit; independent resolver; then
  abort/close/reclaim and cross-language conformance. The audit also found a
  direct acceptance blocker: section 14.3 requires zero heap allocation after
  commit preparation, while the current accepted Go operation barrier measures
  29 locked-phase allocations and its test permits up to 64. The barrier must be
  made allocation-free before commit acceptance; bounded allocation is not an
  acceptable reinterpretation of the specification. No new product decision is
  required by these findings.
- Decision 69A unblocked the fifth frozen Go selective-finalization candidate.
  Caller-owned cleanup scratch now contains a compact hash-backed COW overlay
  for only the global/scope AVL nodes touched by the exact descending delete
  sequence. The checked node budget is at most six maximum-valid AVL heights per
  target, capped by pool capacity; hash, path, and target capacities are checked
  separately with overflow and alias rejection. Finalization seals and transfers
  that scratch to the single-use cleanup authority. Preflight simulates evolving
  search, detach-min, and single/double rotation paths with bounds, cycle,
  ordering, height, and cached-count checks before checkpoint start. Apply copies
  prepared roots/links/caches and resets exact targets without lookup, recursive
  deletion, allocation, or fallible discovery. Permanent tests cover scratch
  capacity/alias/drift/ownership, both AVL root/link/cycle/cache corruption,
  successor-only, rotation-only, and later-delete-only paths, exact failure-state
  preservation, interleaved foreign scopes, post-cleanup global reuse, accepted
  suffix/epoch boundaries, and zero-allocation scaling. Fixed target eight takes
  no more than twice the work with 4,096 versus 512 foreign nodes; 4,096 versus
  512 targets stays within a twelvefold `O(k log N)` bound. The implementer passed
  full, race, vet, 32-bit, shuffled, fiftyfold adversarial, twentyfold scaling,
  formatting, and diff gates. Independent reproduction matched all hashes and
  passed formatting, full, race, vet, and 32-bit gates. Frozen hashes are
  `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `ef1e28eebba2bcf8f9fd76e4a256f5feb44fb4edff972f5075ec664e3cb6d498`,
  and `bitmap_finalize_test.go`
  `5b6065a331010c3c2ca6ab298f57c557c85280096576c30b52ff73c5b34c7b9b`.
  Zero-trust review rejected these exact bytes despite the green matrices. The
  overlay dictionary uses linear probing in a `2m+1` table for `m = O(k log N)`
  scratch nodes. Valid allocator slot identities can collide independently of
  AVL page-number order, producing a quadratic probe sum along one legal path.
  The scaling tests counted validated AVL nodes but omitted dictionary probes,
  so they were false-green for this cost. Go Step 4 remains open until overlay
  identity resolution has a deterministic worst-case bound, every lookup unit is
  included in work accounting, adversarial slot-identity collisions satisfy the
  checked `O(k log N)` contract, and new frozen bytes pass the complete matrices
  plus two independent reviews.
- The sixth frozen Go selective-finalization candidate removes the rejected
  dictionary entirely. Global and scope overlays now use separate tagged
  references: nonnegative values identify immutable original pool slots, `-1`
  is nil, and values at most `-2` directly identify scratch nodes. Materializing
  an original immediately rewrites its unique reachable parent/root reference;
  rotations and detach-min move encoded references, so each `(tree,slot)` is
  copied at most once and every resolution is constant-time. Global and scope
  copies of one slot are intentionally independent and apply disjoint fields.
  Scratch is bounded by six valid AVL heights per target and at most two copies
  per pool slot. Every resolve, materialize, set, path, and final normalization
  operation increments measured work; the checked ceiling derives from fewer
  than 64 operations per tree level plus normalization and is conservatively
  `192 * targets * height + 2`. Permanent tests include the exact slot family
  that forced quadratic probing in the rejected candidate and now prove linear
  work, per-tree/slot uniqueness, evolving rotations/successors, zero allocation,
  ownership, invalid-state atomicity, fixed-target/foreign independence, and
  logarithmic target scaling. The conversion tests found and repaired a missing
  inner-root materialization before double rotation. The implementer passed the
  complete full, race, vet, 32-bit, shuffled, fiftyfold adversarial, twentyfold
  scale/collision, formatting, and diff gates. Independent reproduction matched
  all hashes and passed formatting, full, race, vet, and 32-bit gates. Frozen
  hashes are `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `fad41591bd0533110088ff03c7feb155e4ed72e7e6274e374b5a73b24337d714`,
  and `bitmap_finalize_test.go`
  `44332f667b493b3cc2ab128f4dbb6315fb2820924fb310146b3797f04ac31452`.
  Zero-trust review accepted the tagged-reference complexity repair but rejected
  these exact bytes at the next checkpoint-journal blocker. Checkpoint preflight
  validates empty journal heads but not stale per-slot tags. A bound target,
  scope header, rotation/successor node, or foreign global ancestor whose slot,
  index, or scope checkpoint ID already equals the next checkpoint ID makes the
  corresponding remember operation silently skip journaling. Terminal commit
  then cannot reach and clear that tag, so cleanup can report success while
  leaving a noncanonical vacancy or poisoned foreign node. Existing tests covered
  unbound suffix journal state but not bound/touched next-ID tags. Go Step 4
  remains open until every exact apply-touched checkpoint field/link is canonical
  before checkpoint start, all three tag classes on every touch role reject
  byte-for-byte atomically without foreign/global scans, and new frozen bytes pass
  the complete matrices plus two independent reviews.
- The seventh frozen Go selective-finalization candidate closes that exact
  checkpoint-collision gap. One shared canonical predicate requires all three
  checkpoint `(ID,next)` pairs to be `(0,-1)` while allowing inert historical
  snapshot payloads. After overlay preparation and before checkpoint start, each
  terminal caller checks precisely the slots it will journal: the scope anchor,
  every target, each dirty global/scope overlay node including rotation,
  successor, and foreign ancestors, and finalization's retained bindings. It
  scans no unrelated nodes. Two caller-specific matrices provide 54 permanent
  subtests over all six tag/link fields and target, anchor, retained,
  rotation-only, successor-only, and foreign-ancestor roles. Next-checkpoint-ID
  collisions and nonnil links return typed `ArenaPageConflict`, preserve pool and
  scratch byte-for-byte, and succeed after explicit test repair. Same-failure
  audit mapped every prepared terminal remember/remember-index/remember-header
  site to this exact preflight. The implementer passed full, race, vet, 32-bit,
  shuffled, fiftyfold checkpoint/core, twentyfold scaling, formatting, and diff
  gates. Independent reproduction matched all hashes and passed formatting,
  full, race, vet, and 32-bit gates. Frozen hashes are `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `85ed1476353bca482d1d638f4de7dfe9c4a64effa736ea89faed17ffab618b3f`,
  and `bitmap_finalize_test.go`
  `f56c6d2add091d029ee51186c7fa73bbce3be7405599eed287d538dc0d035895`.
  Zero-trust review rejected these exact bytes at one remaining exact-touch gap.
  Finalization copies every retained slot and refreshes its global and scope
  root-to-page paths. Those refreshes journal non-dirty ancestors that are not
  necessarily targets or delete-overlay nodes; with no tail deletion the delete
  overlay may be empty. A foreign refresh-only ancestor carrying the next
  checkpoint ID can therefore bypass preflight and retain its stale tag after a
  reported success. The new foreign-ancestor test selected a dirty delete-path
  ancestor and was false-green for this distinct role. Go Step 4 remains open
  until preflight proves the exact prepared-final paths for every retained-page
  refresh, counts that work even with zero delete targets, covers global/scope
  refresh-only ancestors with and without tail deletion, and new frozen bytes
  pass the complete matrices plus two independent reviews.
- The eighth frozen Go selective-finalization candidate closes that finalization-
  refresh gap. Prepared delete trees retain tagged references until preflight
  walks the exact post-delete global and scope root-to-page paths for every
  retained binding. Each level charges and validates the current plus both child
  references, key bounds, exact target/scope identity, and canonical checkpoint
  pairs on precisely the slots later journaled by retained refresh. Apply then
  converts the already-proved tagged roots/links mechanically before copying and
  refreshing retained slots; it discovers no path from mutable live structure.
  The checked work bound is the existing delete term when targets exist plus
  `6 * retained * height`, so zero-tail/nonzero-retained finalization has an exact
  nonzero budget without extra scratch. An eight-case permanent matrix covers
  zero and nonzero tail deletion, non-dirty global and scope ancestors, exact
  next-ID and nonnil-link collisions, byte-identical typed rejection, canonical
  scratch, and repair/retry. Same-failure audit found retained copy/refresh is the
  sole finalization refresh caller; sealed cleanup only applies prepared deletes
  and unbinds. The implementer passed full, race, vet, 32-bit, shuffled,
  fiftyfold collision/refresh/core, twentyfold scaling, formatting, and diff
  gates. Independent reproduction matched all hashes and passed formatting,
  full, race, vet, and 32-bit gates. Frozen hashes are `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `72e2831f5c49425e451904dd4e2bd6b7b7200b61a55223948fe9441ee8f955e7`,
  and `bitmap_finalize_test.go`
  `52a06051fa730d2dcad341ecdfa4b8994ce2faa359545da332fe4bb755e8b9c4`.
  Zero-trust review accepted exact refresh-path identity/checkpoint coverage but
  rejected these bytes at the next local-invariant gap. Refresh preflight does
  not validate the node state or height/free/in-use caches that terminal
  `refreshSlotIndexes` reads from each path node and both children. A corrupt
  path cache or off-path child summary can therefore be silently propagated,
  and unchecked aggregate additions can wrap after checkpoint start. The new
  tests mutate only checkpoint pairs, so they are false-green for state/cache
  corruption. Go Step 4 remains open until preflight proves every local input and
  checked bottom-up cache result used by retained refresh—without recursively
  scanning unrelated sibling subtrees—apply consumes only prepared values, the
  corresponding state/cache/overflow matrices pass atomically, and new frozen
  bytes pass the complete matrices plus two independent reviews.
- The ninth frozen Go selective-finalization candidate removes live retained
  index refresh from terminal apply. Tagged overlay nodes now carry modeled
  per-tree self counts. Preflight processes retained copies in apply order,
  materializes their exact post-delete global/scope paths, validates legal state,
  child refs/order/scope, immediate child height/count ranges, checked aggregate
  arithmetic, AVL balance, and each current local cache equation, then computes
  exact final caches bottom-up. Unrelated sibling subtrees are not recursively
  scanned; only summaries actually consumed by the local equation are proved.
  Apply installs prepared links/caches once and copies retained slot state without
  discovery or arithmetic. Scratch is checked as `(6*k + 2*r) * height`, capped
  at two copies per pool slot; work is the delete term plus `12*r*height`, with
  zero terms omitted and all arithmetic checked. Forty permanent atomic cases
  cover zero/nonzero tail, global/scope path state/height/free/in-use, off-path
  child summaries, maximum-count overflow, and balance `+2/-2`; positive tests
  prove complete final-tree cache equations. The implementer passed full, race,
  vet, 32-bit, shuffled, repeated checkpoint/refresh/cache/core/scaling,
  formatting, and diff gates. Independent reproduction matched all hashes and
  passed formatting, full, race, vet, and 32-bit gates. Frozen hashes are
  `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `3e7e889d549a4e48458edfc1eeac619ff0df1c8a4d0ba96782bfd6c0f1271cde`,
  and `bitmap_finalize_test.go`
  `145caf59aab5f62dc70736da375ba6d836b07d2dc1a5683390ee8e7eb73ceaa7`.
  Zero-trust review accepted prepared cache handling but rejected these exact
  bytes at the zero-work edge. Legal all-unbound output has no delete targets and
  no retained pages. Preparation then returns unvalidated live roots, while
  terminal apply still decodes values below `-1` as overlay references after
  checkpoint start. A forged tagged root can silently become slot zero or index
  outside empty scratch and panic. Existing all-unbound tests use only canonical
  roots; corrupt-root tests use a nonempty plan and are false-green here. Go Step
  4 remains open until zero-work roots are proved canonical, all tagged roots and
  links are bounds-normalized to ordinary final indexes before checkpoint, apply
  performs no tag decoding, zero-work tagged/out-of-range root tests reject
  byte-for-byte atomically, and new frozen bytes pass the complete matrices plus
  two independent reviews.
- The tenth frozen Go selective-finalization candidate repairs that zero-work
  edge. `normalizePreparedPrivatePageReferences` is now the last tagged-reference
  consumer: before checkpoint it checked-decodes every prepared node link and
  both roots against the prepared node count, tree identity, and live slot
  bounds, then stores only ordinary `-1` or in-range slot indexes. It also
  enforces that a scope with zero final bound pages has root `-1`. Terminal apply
  contains no tagged-reference decoder and returns without rewriting roots or
  the scope header when both the prepared node and target counts are zero.
  Six permanent all-unbound cases cover global and scope roots containing the
  first tag, a larger out-of-range tag, or an ordinary out-of-range slot; each
  proves typed rejection, byte-exact atomicity, repair, and retry. A positive
  case proves that a canonical unrelated global root survives byte-for-byte.
  The checked work bound is `2 + 192*k*height + 16*r*height`, including mandatory
  root and final-normalization work. Full, race, vet, 32-bit, shuffled, repeated
  checkpoint/refresh/cache/core/scaling, formatting, and diff gates passed.
  Frozen hashes are `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `57f012744a1828715d900d8aa92e4abb395344a624825ab5aa92b003f543ce1b`,
  and `bitmap_finalize_test.go`
  `982bec249e792df6a6e859e88fa4e90a1c96eea6e91e6620cbaf333fdce55069`.
  Independent reproduction matched all five hashes and passed formatting, full,
  race, vet, and 32-bit gates. Zero-trust review rejected these exact bytes.
  With no targets, preparation preserves an ordinary in-range global root and
  normalization proves only its slot bound, not that the slot has a legal
  global-index role. An all-unbound result can therefore forge `indexRoot` to
  one of its valid in-range scoped-vacancy slots; cleanup accepts it, closes the
  scope, and leaves the global root pointing to the now-unscoped vacant slot.
  The permanent malformed-root matrix used only an out-of-range ordinary root,
  so it was false-incomplete for this wrong-role case. Go Step 4 remains open
  until zero-target preflight proves the semantic role and canonical local
  state of every ordinary root it preserves, an in-range wrong-role root rejects
  byte-for-byte atomically and succeeds after repair, and new frozen bytes pass
  the complete matrices plus two independent reviews.
- The eleventh frozen Go selective-finalization candidate adds that preserved-
  root proof only for a zero-node, zero-target plan. Before checkpoint it proves
  the ordinary global and scope roots are bound index nodes with legal page and
  scope roles, ordinary immediate links with valid ordering, valid child
  summaries, and a checked local AVL/count equation. The fixed base work charge
  is six: two root references plus at most four immediate child references. New
  global and scope cases assert that the injected in-range slot is a canonical
  scoped vacancy, then prove typed byte-exact rejection, root repair, and retry
  through the same predecessor. The valid unrelated-root case remains unchanged
  and green. Full, race, vet, 32-bit, shuffled, repeated focused, and scaling
  gates passed. One existing `AllocsPerRun` cache-scaling assertion
  intermittently observed one runtime allocation in isolated repeats, but the
  same test passed an independent 100 consecutive runs and the new preserved-
  root branch is unreachable on that nonzero-node cache path; no code or test
  was changed without causal evidence. Frozen hashes are
  `private_page_pool.go`
  `4e85b228a62cd8ecea4c8b973be0eea2ed63b1343f002c1f9701f7137acc9486`,
  `bitmap_insert.go`
  `265f171c01f9d1e753018422daad539b1ab8b34fd382c0a80f977b7877d9cb96`,
  `bitmap_reservation.go`
  `8f5d6ec05fad1d349e1752fde5398b5700790ba2262e6114a07fb163a20d3973`,
  `bitmap_finalize.go`
  `040644c2d999eb30a3ea60c86165b6daf19c637ffda128570fca8ebba41170ac`,
  and `bitmap_finalize_test.go`
  `4c9693a31dee63ad091250a166ec9061b8bd3815d5e171862af3d2b7a3364109`.
  Independent reproduction matched all five hashes and passed formatting, full,
  race, vet, 32-bit, and 100-repeat wrong-role gates. Go Step 4 remains open
  pending two zero-trust reviews of these exact bytes. Both reviews accepted.
  One reviewer initially treated a locally valid foreign leaf substituted as
  the global root before cleanup as a blocker because local proof cannot show
  that it reaches every unrelated foreign page. That objection was withdrawn
  after applying decisions 16, 17, and 69: the substitution has already
  disconnected those pages, cleanup leaves the retained foreign root/tree
  byte-for-byte untouched, plausible corruption may cause misses without
  explicit `Validate`, and cleanup is expressly forbidden from scanning
  unrelated global membership. Both reviewers independently confirmed that the
  operation-created hazard is closed: direct roots and immediate links into the
  retiring target scope reject before checkpoint; no target-scope dangling
  reference is created; six work units cover the exact maximum; tagged/fallible
  discovery ends before checkpoint; and exact-scope isolation, rollback,
  scratch, and atomicity contracts hold. The Go half of Step 4 is accepted on
  the five frozen hashes above.
- The post-Go Rust selective-finalization audit found no design decision but a
  missing implementation lifecycle. Accepted Rust scoped retirement remains
  reusable, but `release_unused_reservations` is only a test-called unscoped
  prototype: it scans the complete pool for release discovery, scans it again
  for tail cleanup, rebuilds the available list globally, and its global
  mutation snapshot rejects active scopes. Rust has no sealed output,
  generation-invalidating scope seal, one-shot successor seed, or exact-scope
  retained-output cleanup. Existing exact-scope enumeration, commitment/fence,
  scoped checkpoint/rollback, closure, epoch budgeting, bitmap late binding,
  and retirement prepared apply are reusable. The Rust mirror will preserve
  accepted `retirement_writer.rs` unless integration tests prove a defect; add
  private selective-finalization/overlay modules, caller-owned Decision-69
  scratch, small pool/COW lifecycle hooks, the same callback-free fixed-point
  preparation and terminal apply order as Go, and permanent foreign-scope,
  malformed-tree, rollback, stale-handle, fixed-point, allocation, and
  `O(k log N)` gates. Protected starting hashes are `private_page_pool.rs`
  `ef97482d16c241518eeb30743bb626d143967e61a36e7b5e352bd56aea914f5b`,
  `bitmap_cow.rs`
  `440de270a7e4961085de982b0fdf9c0a4e66dcca2a23cc76772dadfe4083790a`,
  and `retirement_writer.rs`
  `c0f54e6e50738de8312dcbbc678eb223690b7524cd7743caba57ed03cd87e950`.
- The first frozen Rust selective-finalization candidate now mirrors the
  accepted Go lifecycle. New private pool and bitmap child modules contain the
  target-local global/scope AVL overlay, caller-owned checked scratch, sealed
  scope generation, read-only output, one-shot successor, exact retained-output
  cleanup, and two-lifetime finalization. Discovery records the expected fixed
  point, the genuinely last source callback runs, exact pool/cache fences run
  before source-error disposition, staging is reset, and a fresh callback-free
  shadow replay must reproduce the same root, page count, and result before
  checkpointed apply/seal. A proposed reuse of the pre-callback discovery shadow
  was rejected during implementation because it would reopen the accepted Go
  stage-poisoning defect. Pre-terminal failure returns the same move-only
  reservation with the error; terminal apply has no fallible suffix. Permanent
  cases cover exact scratch minus one plus same-authority retry, genuinely last
  callback failure/retry, source error plus foreign drift precedence, stale
  shadow exclusion, generation/epoch boundaries, old-handle invalidation,
  sealed read, successor replay, exact cleanup, zero allocation, foreign-scope
  isolation, and 512/4,096 target-local work. The tests exposed and repaired
  retained insert scratch between fixed-point iterations, the missing scoped
  shadow claim/return path, a leftover scope-header checkpoint tag, and missing
  per-binding/scope-generation exhaustion preflight. The accepted retirement
  writer remains unchanged. Default and no-default suites pass 317/317;
  all-features passes 409/409; strict all-target/all-feature Clippy, formatting,
  and diff checks pass. Frozen hashes are `private_page_pool.rs`
  `b464278dd380f350aa0b7b1abd923ef44b1831790a927f80326c9817c9a2588a`,
  `private_page_pool/selective_finalization.rs`
  `226b7db8fad31a78fa4cac9eff7a4f753f03872506a13c60472438aa2995a6fa`,
  `bitmap_cow.rs`
  `e408ef0b5aafe12d9b76026bca5106878563aad01fea22d05a3b7ab93e150142`,
  `bitmap_cow/selective_finalization.rs`
  `0f79768008df443159e7367d9441aeeeae240e164ebaed7fa6bb5f083e6d278c`,
  and unchanged `retirement_writer.rs`
  `c0f54e6e50738de8312dcbbc678eb223690b7524cd7743caba57ed03cd87e950`.
  Independent reproduction matched all five hashes and passed formatting,
  default 317/317, no-default 317/317, all-features 409/409, and strict
  all-target/all-feature Clippy. Zero-trust review rejected these exact bytes.
  `SealedFreeBitmapOutput::cleanup` consumes both the sealed output that owns
  cleanup scratch and the already-consumed successor's predecessor, but every
  repairable pre-checkpoint failure returns only an error. The sealed scope then
  remains active and bound with its successor marked consumed, while no
  move-only authority remains to retry cleanup after repair. Existing permanent
  calls covered only successful cleanup. Rust Step 4 remains open until every
  pre-terminal cleanup failure returns both exact authorities with the error, a
  permanent failure/repair/same-authority retry case proves the scope remains
  recoverable, the terminal suffix remains mechanically infallible, and new
  frozen bytes pass the complete matrices plus two independent reviews.
- The second frozen Rust selective-finalization candidate makes cleanup
  ownership-preserving: every pre-checkpoint failure returns the exact sealed
  output, predecessor, and error, while no error path exists after checkpoint.
  Internally used overlay scratch is restored to canonical form before returning
  the authorities. A permanent test proves that both a wrong predecessor and
  poisoned cleanup scratch preserve the exact sealed pool commitment and return
  their move-only inputs; after explicit scratch repair the returned valid
  authorities close the scope successfully. Finalize/cleanup allocation and
  512/4,096 target-local scaling gates remain green. Default and no-default
  suites pass 318/318, all-features passes 410/410, and strict Clippy,
  formatting, and diff checks pass. Frozen hashes are `private_page_pool.rs`
  `b464278dd380f350aa0b7b1abd923ef44b1831790a927f80326c9817c9a2588a`,
  `private_page_pool/selective_finalization.rs`
  `226b7db8fad31a78fa4cac9eff7a4f753f03872506a13c60472438aa2995a6fa`,
  `bitmap_cow.rs`
  `9016d4fd5ee74eb8c5492bc46467797c7b4aadc27e2570259d51b7640d0a6068`,
  `bitmap_cow/selective_finalization.rs`
  `586532da16461fc7eeecb75fd0114d4bd1ee5b08b18d4c3bf66dd962a69a498a`,
  and unchanged `retirement_writer.rs`
  `c0f54e6e50738de8312dcbbc678eb223690b7524cd7743caba57ed03cd87e950`.
  Independent reproduction matched all five hashes and passed formatting,
  default 318/318, no-default 318/318, all-features 410/410, and strict
  all-target/all-feature Clippy. Zero-trust review rejected these exact bytes.
  Selective cleanup preflights exact member epochs but not the current
  unscoped-vacancy destination boundary. After target deletion is committed,
  terminal sealed-scope close appends returned members through the unchecked
  current vacancy tail. A tail equal to the slot count therefore passes
  preflight, permits partial authoritative mutation, and then panics while both
  move-only authorities are consumed. This is an `O(1)` direct write-destination
  proof, not forbidden unrelated-membership validation; ordinary scope close
  already performs the required boundary check. Rust Step 4 remains open until
  selective preflight proves the exact unscoped vacancy header/tail and capacity
  needed by terminal close, a permanent malformed-boundary case rejects before
  checkpoint and returns both authorities for repair/retry, and new frozen bytes
  pass the complete matrices plus two independent reviews.
- The third frozen Rust selective-finalization candidate reuses the ordinary
  scope-close `O(1)` unscoped-vacancy boundary proof before checkpoint, proves
  count plus returning-scope capacity fits the destination, and proves the
  active-scope decrement. Nine permanent cases cover zero/wrong count,
  missing/out-of-range head, tail equal to slot count, invalid head predecessor,
  invalid tail successor, and both adjacent backlink/forward-link corruptions.
  Every case returns the exact output and predecessor with a typed stale-scope
  error, preserves the exact sealed commitment, and closes successfully after
  repair/retry. A terminal-index audit found no other unproved external
  destination: anchor and complete member-chain indices are preflighted, the old
  vacancy tail now passes the boundary proof, and later tails are the
  just-validated appended members. Default and no-default suites pass 319/319,
  all-features passes 411/411, and strict Clippy, formatting, allocation,
  sealed-lifecycle, and 512/4,096 scaling gates pass. Frozen hashes are
  `private_page_pool.rs`
  `8ecfb71f2e539478588d7a68ef7411bd92338a76614b542249266c79cfe6cf8b`,
  `private_page_pool/selective_finalization.rs`
  `c1845791ba5184f86ca9498e85ad1f52b2b2c3bfaf323daad8634d52813a830c`,
  `bitmap_cow.rs`
  `2732d3ec600bc9236d24c54f15e4f5f1575b9c80cac6a9415d4d0181c87bfad7`,
  unchanged `bitmap_cow/selective_finalization.rs`
  `586532da16461fc7eeecb75fd0114d4bd1ee5b08b18d4c3bf66dd962a69a498a`,
  and unchanged `retirement_writer.rs`
  `c0f54e6e50738de8312dcbbc678eb223690b7524cd7743caba57ed03cd87e950`.
  Independent reproduction matched all five hashes and passed formatting,
  default 319/319, no-default 319/319, all-features 411/411, and strict
  all-target/all-feature Clippy. Zero-trust review rejected these exact bytes.
  The repaired destination-boundary proof is correct and its nine-case matrix is
  genuine, but the same terminal close assumes every already-unbound exact-scope
  member has canonical vacant payload. Preflight checks member identity,
  ordinal, and epoch only; terminal close uses a debug assertion and appends the
  payload unchanged into the globally reusable vacancy list. A corrupted page
  number, state, authorization, or bytes can therefore panic after mutation in
  debug builds or publish a noncanonical reusable slot in release builds. This
  member is a direct cleanup destination and exact-scope validation is allowed.
  Rust Step 4 remains open until preflight proves canonical vacant payload for
  every member terminal close will publish, permanent payload-field corruption
  cases reject before checkpoint and return both authorities for repair/retry,
  every remaining terminal debug assertion/index/write assumption is mapped to
  an explicit preflight proof, and new frozen bytes pass the complete matrices
  plus two independent reviews.
- The fourth frozen Rust selective-finalization candidate completes an
  independent line-by-line terminal-proof audit. Cleanup now proves every
  already-unbound member's full canonical vacant payload; bound targets prove
  untouched validation markers, exact count, and projected canonical unbind
  state. It also proves vacancy boundary/head/tail/capacity, active-scope,
  authorized-count, pending-tail, scope-generation, binding-epoch, and decrement
  arithmetic. Finalization proves retained page-number and authorization
  equality, rejects transient pending-return desired state, and explicitly
  proves the scope is not already sealed. Terminal unbind no longer calls the
  global aggregate traversal: it publishes `available_count` directly from the
  normalized final root, leaves the unscoped minimum unchanged for scoped-only
  mutation, and uses a selective commit that cannot refresh or traverse.
  Remaining terminal indexes are normalized overlay targets/nodes, the exact
  member chain, or internally built unique journal links; the shared-pool
  backing invariant has a local constructor/lifecycle proof. Permanent tests
  cover every one of 25 canonical vacant-payload fields, bound validation-marker
  corruption, the nine destination-boundary cases, presealed same-authority
  retry, and an unrelated foreign search child equal to slot count. Critical
  cases pass both debug and release, prove exact raw whole-pool preservation,
  repair, and retry. Default and no-default suites pass 322/322, all-features
  passes 414/414, and strict Clippy, formatting, allocation, sealed-lifecycle,
  and 512/4,096 scaling gates pass. Frozen hashes are
  `private_page_pool.rs`
  `54a443c7a772cbf5b5f035fc6619e85f4aba6d969794e1bb50154a7b4387285c`,
  `private_page_pool/selective_finalization.rs`
  `580c51cf952fe79820a9d22c378d36ff1d8e9ee1d12652950fa76e3499c92805`,
  `bitmap_cow.rs`
  `4474740f61c021941c8507e06119e3746e063f2a1b9b72f078f38bbdb98a1830`,
  `bitmap_cow/selective_finalization.rs`
  `5b3a1327f8b3858034bbb844162366c3d51509d025bece3c76fe69ea108b2141`,
  and unchanged `retirement_writer.rs`
  `c0f54e6e50738de8312dcbbc678eb223690b7524cd7743caba57ed03cd87e950`.
  Independent reproduction matched all five hashes and passed formatting,
  default 322/322, no-default 322/322, all-features 414/414, strict
  all-target/all-feature Clippy, and the five critical cleanup/seal cases in
  release mode. Zero-trust review rejected these exact bytes. Cleanup itself is
  ownership-preserving, but the preceding move-only successor seed still uses
  `consume(self) -> Result<Predecessor, Error>`. A transient active checkpoint
  or pool borrow conflict returns before the pool marks the successor consumed,
  yet drops the only seed. After the transient condition is removed, the sealed
  scope is valid and its successor remains unconsumed, but no authority can
  obtain the predecessor or close it. The existing replay case duplicated a
  test seed before successful consumption and did not cover failure ownership.
  Rust Step 4 remains open until every pre-consumption failure returns the exact
  seed with the error, checkpoint-active and borrow-conflict cases prove
  failure/repair/same-seed retry, and new frozen bytes pass the complete
  matrices plus two independent reviews.
- The exhaustive Rust move-authority audit found a second Step-4 blocker in the
  same seed: production code exposes `share(&self) -> Self`, so an allegedly
  move-only cleanup capability is duplicable; the existing replay test used that
  duplicate and masked failure-time ownership loss. Production duplication must
  be removed, with any malformed duplicate helper confined to tests. The audit
  also classified broader private-pool API defects that are not reached by the
  selective terminal suffix but remain mandatory before core-SDK acceptance:
  fallible scoped-operation finish can lose its operation token after earlier
  mutations; generic scoped/unscoped checkpoint commit and rollback can lose the
  only checkpoint token while the checkpoint remains active; and generic
  scoped/unscoped page transfer/return can lose the exact page authority on
  pre-mutation errors. Disposable capacity planning, attachment/bind,
  composite-bind, and insertion plan objects are not persistent-authority
  defects because every error precedes live mutation and the established
  contract is fresh replanning. The transaction/durability foundation must
  repair and permanently test the generic checkpoint/operation/page-authority
  cases before any claim that the pool or SDK is generally retry-safe; they are
  not hidden follow-up work and do not alter the current selective-finalization
  freeze boundary.
- The fifth frozen Rust selective-finalization candidate makes successor
  consumption ownership-preserving: active-checkpoint, borrow-conflict, and
  every other failure before the consumed mark return the exact seed with the
  typed error. Permanent debug/release cases remove the transient condition,
  retry with the same seed, reject a duplicate after successful consumption,
  and close the sealed scope. Production `share` is removed; only a
  test-confined malformed duplicate helper remains. The classified audit found
  no other Step-4 move-authority loss: finalize and cleanup return all persistent
  authorities on preterminal failure, and the terminal suffix and scratch
  conversion are infallible. Default and no-default suites pass 323/323,
  all-features passes 415/415, and strict Clippy, formatting, allocation,
  sealed-lifecycle, release-focused, 512/4,096 scaling, and diff gates pass.
  Frozen hashes are `private_page_pool.rs`
  `89a22796769cfcba194258c48c3d8a4811610caf4f54e13c59da2365734a87f5`,
  `private_page_pool/selective_finalization.rs`
  `580c51cf952fe79820a9d22c378d36ff1d8e9ee1d12652950fa76e3499c92805`,
  `bitmap_cow.rs`
  `00604bb1051ac64166a1a0653a18c146628305d7bdf53f27b2b79364dfb9f5d1`,
  `bitmap_cow/selective_finalization.rs`
  `0efee3f5a17912edeae4c56d1cd199b251784e9fc1c2ad2f58916c5a73d7a196`,
  and unchanged `retirement_writer.rs`
  `c0f54e6e50738de8312dcbbc678eb223690b7524cd7743caba57ed03cd87e950`.
  Independent reproduction matched all five hashes and passed formatting,
  default 323/323, no-default 323/323, all-features 415/415, strict
  all-target/all-feature Clippy, and the successor transient-failure retry case
  in release mode. Both independent zero-trust reviews accepted these exact
  bytes. Each confirmed that successor failure returns the exact seed with typed
  checkpoint/borrow errors, production duplication is absent, the exhaustive
  terminal proof remains valid, and no operation-caused Step-4 blocker remains
  under decisions 16, 17, and 69. Independent reviewer validation also passed
  release selective cases 11/11. The Rust half of Step 4 is accepted on the five
  frozen hashes above. With the previously accepted Go hashes, exact scoped
  retirement and selective finalization are complete in both languages.
- The read-only Step-5 authority/dependency audit found no design decision and
  fixed the implementation order. First, Rust generic private-pool consuming
  APIs must mirror the accepted ownership-preserving result shape:
  `finish_operation_in_scope`; scoped and unscoped checkpoint commit/rollback;
  and scoped and unscoped page transfer/return return the exact consumed
  token/authority on every error. Callers must propagate the returned authority
  or enter whole-draft abort; no rollback result may be discarded. Permanent
  cases cover borrow conflict, wrong scope/owner/disposition, incomplete
  operation, checkpoint mismatch, journal corruption, and epoch boundaries;
  every failure proves exact-state preservation or retained partial-operation
  ownership, repair, same-token retry, and stale replay after success. The
  accepted selective terminal helpers remain infallible and are not widened.
  Go tokens are copyable and retain retry authority on equivalent errors, but Go
  has a semantic stranding defect: `abortOperation` clears only the active
  marker after fallible COW code may already have claimed/written/released
  private pages. The Go transaction owner must therefore distinguish
  pre-mutation failure from post-mutation abort-required state; post-mutation
  failure retains all cleanup authority and permits only whole-draft `Abort`,
  never local continuation. These Rust and Go repairs are the first Step-5
  implementation chunk.
- After authority repair, Step 5 proceeds in dependency order: mirrored private
  transaction/resource/result/cleanup-ledger contracts; writer-owned
  draft/identity plus non-consuming abort/close shell; provenance-aware
  multi-work-unit fixed-point coordination over the shared pool and sealed-scope
  predecessor chain; allocation-free retained grow/write/sync/meta and
  writer-lease transition primitives; five-phase commit with exact
  NotCommitted/OutcomeUnknown/Committed boundary; independent `ResolveCommit`;
  then reclaim, abort/close integration, crash/fault matrices, and
  cross-language conformance. The Go locked reader barrier is a known direct
  blocker before composed commit: its current test permits 64 allocations and
  the SOW measured 29, while the normative post-preparation durability phases
  require literal zero. Rust already has a zero-allocation barrier test.
  Accepted Step-4 hash baselines were independently rechecked before this plan;
  Step 5 intentionally supersedes only files changed by each frozen authority
  repair.
- The first frozen Step-5 Rust authority-repair candidate changes all nine
  consuming private-pool APIs to return the exact operation, checkpoint, or page
  authority on every error: scoped operation finish; scoped/unscoped checkpoint
  commit and rollback; and scoped/unscoped transfer and return. Every
  validation, borrow, owner/scope/disposition, epoch, journal, path, and
  pending-return check precedes the first write; terminal suffixes are
  mechanically infallible. Generic commit proves the complete current private
  global/scope AVL projection, and generic rollback proves the complete restored
  projection including saved bindings, links, roots, heights, strict key
  bounds, and reachable counts. Strictly decreasing stored `u8` heights bound
  malformed recursion to 255; scoped selective paths remain target-local.
  Every caller now propagates returned authority or explicitly aborts its
  disposable local stage; no production rollback result is ignored. Permanent
  matrices cover borrow conflict, wrong scope/owner/disposition, incomplete
  operation with retained partial state, pool/checkpoint mismatch, journal
  head/count corruption, current/restored AVL corruption, pending-return
  corruption, exact epoch failures, same-token repair/retry, stale replay, and
  zero allocation. Default and no-default suites pass 327/327, all-features
  passes 419/419, release authority cases pass 4/4, target-local 512/4,096
  scaling remains green, and strict Clippy, formatting, and diff gates pass.
  Frozen hashes are `private_page_pool.rs`
  `3ccf2cd2336f92f2110ce687b0e158dd0b3229c9886b8d42900acda93ddbc966`,
  unchanged `private_page_pool/selective_finalization.rs`
  `580c51cf952fe79820a9d22c378d36ff1d8e9ee1d12652950fa76e3499c92805`,
  `bitmap_cow.rs`
  `9bc1db0f749358d9f28bd1fc1734a2d3b992eed8ca5d23cfc340d1bce67f6e4f`,
  `bitmap_cow/selective_finalization.rs`
  `89d926cf0f6b82388bdd3f31397e3595c748e5a2bf3b2c7d8d63d37f32962197`,
  and `retirement_writer.rs`
  `383533a98b6073dd07209bc58c495586a5702d2bad375dc644e6bae79e580843`.
  Independent reproduction matched all five hashes and passed formatting,
  default 327/327, no-default 327/327, all-features 419/419, strict
  all-target/all-feature Clippy, and release authority cases 4/4. Step-5 chunk
  1A remained open pending two zero-trust reviews of these exact bytes. Both
  reviews accepted. They independently confirmed that all nine APIs and every
  production caller preserve exact authority, no rollback result is ignored,
  generic current/restored projection and terminal epoch proofs complete before
  mutation, post-write suffixes are infallible, scoped work remains
  target-local, and Step-4 selective behavior is unchanged. Reviewer validation
  additionally passed release selective cases 11/11. The Rust generic
  move-authority foundation is accepted on the five frozen hashes above.
- The frozen Step-5 Go transaction/abort-required foundation is internal-only:
  it does not expose a public writer or publish data. An operation records its
  starting pool mutation epoch. A pre-mutation operation failure remains
  locally recoverable; any post-mutation failure retains the active operation,
  marks the whole draft abort-required, and blocks new operations,
  checkpoints, operation commit, and writer commit. Whole-draft `Abort`
  preflights before its first scrub, is non-consuming and retry-safe, preserves
  selected Meta, scrubs all private pages and scope/checkpoint/operation state
  in exact O(P), invalidates old handles through a new pool incarnation, and
  permits buffer/core reuse only after the fixed cleanup ledger is empty.
  Prepared initialize, begin, and abort paths allocate zero heap objects. A
  damaged post-mutation abort token poisons the pool before returning its
  identity error, so local continuation remains impossible. Disposable shadow
  rollback results are no longer ignored: rollback failure destroys the whole
  shadow and takes error precedence; otherwise the original error is
  preserved. Permanent tests cover failures after claim, write, and release;
  damaged-token poison; abort-only gates; selected-Meta preservation; abort
  preflight failure and same-handle repair/retry; cleanup-ledger retry without
  rescrub; exact O(P) visits; stale handles; fresh reuse; and zero allocation.
  Frozen hashes are `private_page_pool.go`
  `50b4ff392ce86e07763bbafe1efd08dc6d2e6bbddaa497b7730af2bd1c2e8ec6`,
  `bitmap_cow.go`
  `fc4bdc17ecd985a0919596936b751f89bcd41b73a2519ebe14305e1705a0199f`,
  `bitmap_reservation.go`
  `8604d46c65736513233bcd279fcb23c4cbabf57e83fe2fddf740bb7b5be7d933`,
  `bitmap_finalize.go`
  `9293ab4b982122fc0786d8221e1e8b43e800af2b8ff11b03cbcec3a5522a2443`,
  `retirement_plan.go`
  `e60bc85802e42d536b4c0d7bd2994adbf00f33e18108592d17ae9eada8aafa74`,
  `writer_transaction_core.go`
  `34e170d359ee08e7b06f315bfc22106a9ce8d675e136539d5721c206bddac7a2`,
  and `writer_transaction_core_test.go`
  `a41d6296ec33f8526a3654bc307a770b3379aa92190e88d61b8af9aeb819135b`.
  The implementer matrix passed the full Go suite, exact-v4 race suite, vet,
  386 suite, three shuffled exact-v4 repetitions, compile-only suite,
  formatting/diff checks, and the ignored-rollback search. Independent
  reproduction matched all seven hashes and passed formatting, the full Go
  suite, exact-v4 race suite, vet, 386 suite, three shuffled exact-v4
  repetitions, twenty focused transaction/abort repetitions, compile-only
  checks, the diff gate, and the ignored-rollback search. Both zero-trust
  reviews rejected these exact bytes for writer-handle ABA defects that the
  matrix missed. First, `abort` incremented `handleEpoch` after scrubbing
  without preflighting overflow; enough begin/abort cycles could wrap the
  counter and later revive an ancient handle. Second, supported clean-core
  reinitialization reset `handleEpoch` to one, so the immediately following
  `begin` could recreate the epoch of a retained handle from the prior
  incarnation. Repair must preserve monotonic handle identity across clean-core
  reinitialization, reserve the invalidation epoch before admitting a new
  transaction, preflight Abort before its first scrub, and add permanent
  boundary and same-core-reinitialization cases. The first candidate is
  rejected; its seven hashes are retained above as historical evidence only.
- The repaired second Go candidate mirrors the private-page pool's proven
  process-global monotonic incarnation pattern for writer handles. `Begin`
  atomically reserves two identities: the returned handle and its mandatory
  Abort invalidation. If both cannot be reserved, it returns transaction
  exhaustion before pool or draft mutation. Clean same-core reinitialization
  preserves the last local invalidating identity, while a complete storage
  reset at the same address still cannot recreate an old handle because future
  handles come from the global allocator. Abort verifies the exact reserved
  successor before its first scrub; a damaged reservation preserves core state,
  target Meta, scope/checkpoint/page authority, and page bytes. The
  post-preflight identity assignment and existing scrub suffix are infallible,
  and cleanup retry performs no further identity transition. New permanent
  cases cover both rejected counterexamples, reset/reinitialize ABA, near-Max
  clean rejection, the final legal Begin/Abort identity pair, subsequent
  exhaustion, and defensive abort-reservation corruption with exact authority
  preservation. The unchanged first five hashes remain
  `private_page_pool.go`
  `50b4ff392ce86e07763bbafe1efd08dc6d2e6bbddaa497b7730af2bd1c2e8ec6`,
  `bitmap_cow.go`
  `fc4bdc17ecd985a0919596936b751f89bcd41b73a2519ebe14305e1705a0199f`,
  `bitmap_reservation.go`
  `8604d46c65736513233bcd279fcb23c4cbabf57e83fe2fddf740bb7b5be7d933`,
  `bitmap_finalize.go`
  `9293ab4b982122fc0786d8221e1e8b43e800af2b8ff11b03cbcec3a5522a2443`,
  and `retirement_plan.go`
  `e60bc85802e42d536b4c0d7bd2994adbf00f33e18108592d17ae9eada8aafa74`.
  Repaired hashes are `writer_transaction_core.go`
  `b1e83e818e191365b28f136d35d8d0f582f2179ca8c42b2f75a0607d5c87d4cd`
  and `writer_transaction_core_test.go`
  `abd624af57ccbf09ad7bbb0f3bf873d3e5da56fbcc33e69b70a11ca8284bf86b`.
  The repair matrix passed the full Go suite, exact-v4 race suite, vet, 386
  suite, five shuffled exact-v4 repetitions, one hundred focused
  identity/overflow/abort/cleanup repetitions, ten zero-allocation repetitions,
  benchmark compilation, formatting/diff gates, and the ignored-rollback
  search. Independent reproduction matched all seven repaired hashes and passed
  formatting, the full Go suite, exact-v4 race suite, vet, 386 suite, five
  shuffled exact-v4 repetitions, one hundred focused
  identity/overflow/abort/cleanup repetitions, ten zero-allocation repetitions,
  compile-only checks, the diff gate, and the ignored-rollback search. Two
  reviews of the second exact candidate rejected it for two same-class terminal
  pool defects, while confirming the writer-handle ABA repair itself. First,
  the process-global pool-incarnation allocator could issue `MaxUint64` to a
  new vacant pool, but whole-draft Abort requires `pool.epoch + 1`; Begin could
  therefore admit a draft which no Abort retry could scrub. Second, normal
  forward pool mutations reserved only checkpoint-cleanup headroom, not the
  `len(pool.slots)` mutation-epoch steps required by whole-draft scrub; legal
  mutations could consume that headroom and likewise make Abort permanently
  impossible. Repair must reserve the pool invalidation identity before Begin
  succeeds and preserve an exact whole-draft scrub mutation reserve across
  every normal and checkpointed forward mutation. Permanent tests must reach
  both terminal boundaries through normal admitted paths and prove clean
  rejection or successful Abort without manual state repair. The second
  candidate is rejected; its repaired hashes remain historical evidence only.
- The third Go candidate adds transaction-only pool reserves without changing
  standalone pool boundary behavior. Draft-pool initialization atomically
  reserves a process-global `(active epoch, invalidation epoch)` pair; the last
  legal pair is `(MaxUint64-1, MaxUint64)`, and insufficient pair headroom
  rejects Begin before pool or slot mutation. Each transaction pool also holds
  an exact whole-draft mutation reserve equal to its slot count. Common normal
  and checkpoint forward preflights subtract that permanent reserve, with
  checkpoint rollback/commit cleanup reserved in addition. Existing aggregate
  prepared/terminal paths inherit the reserve through their complete
  preflights. Abort verifies the exact successor identity and exact scrub-step
  reserve before touching data, then consumes precisely one mutation epoch per
  slot and the reserved invalidation identity; the clean pool carries neither
  reserve into the next Begin. Permanent tests prove atomic terminal-pair
  rejection, the final legal pair and successful Abort to `MaxUint64`, the last
  legal normal mutation plus atomic one-step-over rejection, and checkpoint
  forward/rollback cleanup plus successful terminal Abort without manual
  repair. Frozen hashes are `private_page_pool.go`
  `ab76370c72849e9be67ade4add11052771564513560753e1425ab5a2c17ad604`,
  unchanged `bitmap_cow.go`
  `fc4bdc17ecd985a0919596936b751f89bcd41b73a2519ebe14305e1705a0199f`,
  `bitmap_reservation.go`
  `ef6d6b56d243bf5022e5a614333c1d73f12789060141c97ec6b4960de3a69bb0`,
  unchanged `bitmap_finalize.go`
  `9293ab4b982122fc0786d8221e1e8b43e800af2b8ff11b03cbcec3a5522a2443`,
  `retirement_plan.go`
  `36f9d7aabf238183a3662aecdff9911b7766b8d001e4e5c020504668c4d16b94`,
  `writer_transaction_core.go`
  `1a1f260d24d0022d9446e19fd127874e6af102ff0f9e105f0cb929bb244a3dba`,
  and `writer_transaction_core_test.go`
  `d51d766ed417cb73d3f0c633f5ff0e0e11d518483a2d30569c7904891176f006`.
  The third-candidate matrix passed the full Go suite, exact-v4 race suite, vet,
  386 suite, five shuffled exact-v4 repetitions, one hundred focused
  terminal/identity/abort/cleanup repetitions, ten zero-allocation repetitions,
  benchmark compilation, formatting/diff gates, and the ignored-rollback
  search. Independent reproduction matched all seven third-candidate hashes and
  passed formatting, the full Go suite, exact-v4 race suite, vet, 386 suite,
  five shuffled exact-v4 repetitions, one hundred focused
  terminal/identity/abort/cleanup repetitions, ten zero-allocation repetitions,
  compile-only checks, the diff gate, and the ignored-rollback search. Two
  reviews accepted the exact third candidate. The first mapped all 42
  production `advanceMutationPrepared` sites and confirmed that direct,
  prepared, and terminal bitmap and retirement mutations inherit the permanent
  whole-draft reserve, while checkpoint cleanup is budgeted in addition. The
  second independently confirmed transaction-only pair uniqueness and
  exhaustion, unchanged standalone pool boundary semantics, complete aggregate
  preflight coverage, exact discard consumption, retry authority, writer-handle
  ABA closure, disposable-shadow rollback precedence, O(P) work, zero
  allocation, and unchanged Step-4 behavior. Both matched all seven hashes
  before and after review and passed full, race, vet, 386, shuffled, focused,
  allocation, compile-only, formatting, and same-failure gates. Go Step-5 chunk
  1B is accepted on the seven third-candidate hashes above.
- The read-only Step-5 contract audit fixed the next dependency boundary without
  exposing a product decision. Both languages already have equivalent
  wire-neutral local identity kinds/encodings, basename validation and
  commitments, attempt tuples, main-tail authority, stable error codes, and
  move-only live cleanup guards. Neither has the section-14.2 commit result or
  section-14.4 cleanup-artifact/result aggregate. Go's accepted transaction
  foundation also still contains an explicitly temporary callback cleanup list:
  it stops at the first failure, silently treats a nil callback as success, and
  names individual private pages as external artifacts even though the
  normative artifact kinds are private output, private reservation, owned
  coordination, authorized scratch, and unpublished main tail. The placeholder
  must be replaced, not extended. Cleanup evidence must remain fixed data;
  retry authority belongs in a separate non-copyable owner slot. Exact raw
  basename bytes use caller-provided fixed storage with checked offsets and
  lengths, because the spec defines a bounded component but no invented global
  byte limit.
- Step-5 chunk 2 is split into independently frozen mirrored subchunks. **2A**
  adds the common private/no-public core in both languages: the exact immutable
  four-limit transaction budget, checked simultaneous-use counters with atomic
  acquire/release, caller-slice fixed cleanup-obligation storage, retry of every
  independent obligation with stable compaction and exact cause preservation,
  derived `Clean`/`ResiduePossible` state, and move-only/take-once coordination
  ownership. It performs no filesystem cleanup and no destructor/finalizer
  cleanup. **2B** adds the exact wire-neutral descriptive contracts in both
  languages: local/optional identities, presence-explicit creation-security and
  unpublished-tail groups, exact cleanup artifact kinds/directory roles,
  caller-arena basename records, commit attempt/durability/result, abort
  outcome/result, and invariant matrices. Artifact records contain no
  descriptors, callbacks, closures, or guards; owner slots remain separate.
  `Clean` is computed exactly when the artifact ledger is empty and coordination
  cleanup is `None`; definitive durability remains orthogonal to residue; and
  `OutcomeUnknown` requires `RetainedWriterCloseRequired` plus
  `ResiduePossible`.
- After 2A and 2B are accepted, **2C** adds the Rust transaction-only pool
  identity pair, permanent O(P) Abort mutation reserve, writer-owned
  `Clean(caller slots) | Draft(pool)` state, operation-failure poison, and
  non-consuming retry-safe whole-draft Abort shell matching the accepted Go
  semantics. Rust uses borrowed caller slices and `Option<T>` owner slots:
  no `Vec`, `Box`, trait-object allocation, unsafe lifetime extension, or
  cleanup in `Drop`. Each subchunk receives exact-limit/one-over/overflow and
  atomicity tests, zero-allocation tests, move/drop/take-once ownership tests,
  same-failure searches, full language matrices, and two exact-hash reviews.
  Advanced-empty transitions, random nonce/cancellation storage, actual OS
  artifact cleanup, retained descriptor charging, public result APIs, commit
  preparation/durability phases, Close/ResolveCommit, and C ABI projection
  remain in their recorded later dependency chunks.
- The Rust Step-5 chunk-2A implementation candidate is frozen for independent
  review. It adds only the private
  `writer_transaction_contract` module and its private declaration; it does not
  expose a public API or perform filesystem cleanup. The frozen SHA-256 values
  are `fa5467f41909c170a5275ee210030173429ed47bc3732662f650f10e1cc08912`
  for `v4/rust/iprange-livedb/src/lib.rs` and
  `ccdb28d74694f2a9f380044ebbb9b02889a6b77f48fa095b172ba58e8f400124`
  for
  `v4/rust/iprange-livedb/src/writer_transaction_contract.rs`. Implementer
  validation passed 340 default tests, 340 no-default tests, 432 all-feature
  tests, 13 focused all-feature release tests, strict Clippy for no-default and
  all-feature all-target builds, formatting, ten repeated zero-allocation
  success/failure runs, and searches excluding production allocation, unsafe,
  destructor cleanup, pool/draft logic, and public export. This records a
  candidate, not acceptance; exact-hash independent reviews remain mandatory.
- The first Rust Step-5 chunk-2A candidate above is rejected. One independent
  review accepted its normal-path resource, cleanup, ownership, allocation, and
  private-surface behavior. The second review found two valid authority defects:
  the generic retry aggregate retained only the first failure and did not
  require an exact error to remain with every later failed obligation, and an
  unexpected executor unwind after an earlier success could leave `None` inside
  the active prefix before `len` was repaired, making the ledger unretryable.
  Repair must separate immutable cleanup evidence from mutable retry/error
  state, retain one exact typed failure per unresolved entry, and keep the
  active prefix structurally retryable at every unwind point, with adversarial
  mutation and first/middle/last unwind tests. The review's missing C-handle
  destroy finding is not a 2A blocker: explicit result-handle refusal belongs
  to 2B's terminal result/opaque-handle projection, while 2A must continue to
  provide passive destructor behavior and move-only/take-once ownership.
- The repaired Rust Step-5 chunk-2A candidate is frozen for fresh independent
  review. `v4/rust/iprange-livedb/src/lib.rs` remains at SHA-256
  `fa5467f41909c170a5275ee210030173429ed47bc3732662f650f10e1cc08912`;
  `v4/rust/iprange-livedb/src/writer_transaction_contract.rs` is now
  `8cc6695ce08871b6e787a44f2a031b23c10e70dfd8ffcb22f4a7f15ed54a2c5a`.
  The repair separates immutable obligation evidence, mutable retry ownership,
  and one exact retained error per entry. It marks a callback attempt before
  invocation, leaves a contiguous caller-owned prefix on unwind, identifies the
  exact interrupted entry, refuses retry until the outer panic boundary records
  a typed error without consuming authority, and performs one measured stable
  linear compaction. Implementer validation passed 343 default, 343 no-default,
  435 all-feature, and 16 focused release tests; strict no-default/all-feature
  Clippy, formatting, diff, private-surface/prohibited-pattern searches, ten
  repeated expanded zero-allocation runs, and linear work checks at 1, 64, and
  1024 entries also passed. This is a repaired candidate, not acceptance; both
  exact-hash reviews must be repeated.
- Two fresh independent reviews accept the repaired Rust Step-5 chunk-2A
  candidate on the exact two hashes above. Both reproduced the 343
  default/no-default, 435 all-feature, 16 focused release, strict Clippy,
  formatting, zero-allocation, private-surface, and unchanged-hash gates. They
  independently confirmed immutable evidence versus mutable retry ownership,
  one exact retained error per failed entry, a borrowed aggregate first cause,
  first/middle/last unwind containment with exact interrupted-entry recording
  and retry refusal, stable O(n) compaction, checked atomic resource accounting,
  derived cleanup state, take-once guard transfer, and no destructor cleanup.
  Rust Step-5 chunk 2A is accepted on the repaired hashes.
- The Go Step-5 chunk-2A candidate is frozen for independent review. Its exact
  SHA-256 values are
  `857a44b0039a39700a242d227958e9992e93e7445a2fb9dd814abd7558ed3366`
  for `writer_cleanup_contract.go`,
  `0af4ec87c5471969d0dc4bec208967f0db17bfce75caa4996d7a9eff311de314`
  for `writer_cleanup_contract_test.go`,
  `e078008c7a7a0b65c44e80ddb274d1e5a19130343319d80d34d0a02eb07549f4`
  for `writer_resource_contract.go`,
  `2f63d0c879456ded5563768def28539d21ec5f74cec040e0383512b99fee794a`
  for `writer_resource_contract_test.go`,
  `73f81c3ba4e22612d344097ab9b0ad6c4866312b4bce5667f2f67c91668a2bc3`
  for `writer_transaction_core.go`, and
  `aa9dba9844e7a20b67d2fc8ebccb7c42dfba9ea238052d06ac0f079f6cfca60f`
  for `writer_transaction_core_test.go`. Implementer validation passed the
  full, race, vet, 386, five-shuffle, compile-only, benchmark-compile, formatting,
  static-surface, and one-hundred-repeat focused gates. Permanent tests cover
  exact per-owner errors, nil-error preservation, wrong/zero executor identity
  normalization without authority loss, first/middle/last panic recovery,
  virgin resource initialization and charged reinitialization refusal, unique
  coordination transfer and resolution, and success/failure/recovery
  zero-allocation paths. The accepted Step-1B prerequisite hashes are unchanged.
  This records a candidate, not acceptance; two exact-hash reviews remain
  mandatory.
- The first Go Step-5 chunk-2A candidate above is rejected. Review confirmed
  every cleanup, panic, coordination, abort, allocation, scope, and
  prerequisite-hash gate, but found one exact typed-arithmetic mismatch:
  `openFiles` and its limit are `u32`, while Go widened the addition to `u64`.
  Consequently `u32::MAX + 1` was atomically rejected as insufficient budget
  instead of arithmetic overflow, unlike the mirrored Rust checked-`u32`
  contract. Repair is limited to checked native-width open-file addition plus a
  permanent atomic maximum-plus-one test; all six candidate hashes must then be
  replaced and both reviews repeated.
- The repaired Go Step-5 chunk-2A candidate is frozen for fresh independent
  review. Cleanup and transaction files remain at
  `857a44b0039a39700a242d227958e9992e93e7445a2fb9dd814abd7558ed3366`,
  `0af4ec87c5471969d0dc4bec208967f0db17bfce75caa4996d7a9eff311de314`,
  `73f81c3ba4e22612d344097ab9b0ad6c4866312b4bce5667f2f67c91668a2bc3`,
  and
  `aa9dba9844e7a20b67d2fc8ebccb7c42dfba9ea238052d06ac0f079f6cfca60f`;
  repaired `writer_resource_contract.go` is
  `5b9f9664dbb43284d8ff82a09ec13779a5d896681c56a15b630df039b6af4f31`
  and its test is
  `f4147ccb8cd484d6340c05a99d0c5f6b5e5787c94fc683754045742978b7eaec`.
  Checked native-`u32` open-file addition now precedes budget comparison, so
  maximum-plus-one returns arithmetic overflow without mutating usage.
  Permanent boundary and zero-allocation failure coverage was added. The full,
  race, vet, 386, five-shuffle, one-hundred-repeat focused, compile-only,
  benchmark-compile, formatting, and static-surface gates pass after repair;
  the five non-superseded Step-1B hashes remain exact. This is a repaired
  candidate, not acceptance; both exact-hash reviews must be repeated.
- Two fresh independent reviews accept the repaired Go Step-5 chunk-2A
  candidate on the six exact hashes above. Both reproduced the native-`u32`
  maximum-plus-one overflow precedence and atomicity, full/race/vet/386,
  five-shuffle, one-hundred-repeat focused/allocation, compile, formatting,
  static-surface, unchanged-prerequisite, and unchanged-hash gates. They also
  independently reconfirmed fixed caller storage, immutable evidence versus
  mutable ownership, exact per-entry errors, nil preservation, malformed
  executor-error normalization, reentry and panic recovery, stable O(n)
  compaction, derived cleanup state, address-bound coordination/guard transfer,
  explicit resolution before guard-state reuse, and whole-draft cleanup retry
  without a second scrub. Go Step-5 chunk 2A is accepted. With the repaired Rust
  acceptance above, mirrored Step-5 chunk 2A is complete.
- Step-5 chunk 2B has no unresolved product decision. Its implementation map is
  frozen before code. Go adds one private result/artifact contract and tests
  under `v4/go/internal/exactv4/`; Rust adds one private
  `writer_result_contract` module and its private `lib.rs` declaration. Both
  reuse the accepted local-identity encodings and validators, exact basename
  encoding/validation, stable Phase-1 error codes, and the accepted 2A fixed
  cleanup ledger and coordination owner. No public SDK or C ABI symbol, file
  operation, descriptor owner, callback, closure, allocator, destructor
  cleanup, commit execution, or abort execution is added.
- The mirrored private descriptions are: required and explicitly optional local
  identity groups; explicitly optional creation-security and unpublished-tail
  groups; the five exact cleanup artifact kinds and three directory roles;
  caller-arena basename records with checked `u64` offsets and `u32` lengths;
  immutable cleanup-artifact evidence; a view that combines that evidence with
  the exact retained per-entry stable error code; the exact section-14.2 commit
  attempt and durability; and commit and abort terminal results. Presence is
  never inferred from all-zero payload bytes. Present identities use the
  existing canonical-padding validation; the format does not define an
  all-zero identity payload as absent, so 2B does not invent that sentinel.
  Creation security accepts only the defined POSIX/Windows kinds. An absent
  group has canonical zero storage. Basenames remain raw encoded bytes and are
  never converted through a language string.
- Artifact constructors enforce the normative matrix. Private output and private
  reservation use `Destination`; owned coordination uses `Destination` or
  `MainFile` because creation/publication attempts retain a supplied destination
  while `InitializeLive` retains the existing main-file parent; authorized
  scratch uses `ScratchDirectory`; unpublished main tail uses `MainFile`.
  Every separately created artifact requires creation-security evidence and
  forbids tail authority. A main-tail artifact forbids creation-security
  evidence and requires exact artifact identity plus nonzero database ID,
  transaction ID, commit nonce, page-aligned committed target length, and a
  strictly greater page-aligned observed end. Returned terminal artifacts
  require one retained stable cleanup error per entry; immutable identity is not
  mixed with mutable retry ownership or error state.
- Result constructors enforce one shared Go/Rust matrix. Cleanup state remains
  derived solely from ledger emptiness and coordination disposition.
  `NotCommitted` and `Committed` each permit clean or residue-bearing results;
  residue never changes either factual durability. Their possible coordination
  dispositions are `None`, `CleanupGuard`, or
  `RetainedWriterCloseRequired`; commit cannot report
  `RetainedReaderCloseRequired` because the originating non-consuming handle is
  a writer. `OutcomeUnknown` requires exactly
  `RetainedWriterCloseRequired` and therefore `ResiduePossible`, with or
  without artifact entries. The optional cause does not determine durability.
  Abort remains the exact prose contract rather than inventing an unapproved
  attempt schema: `Aborted` permits the clean reusable case or residue with
  retained-writer close authority; `AbortIncomplete` requires retained-writer
  close authority and `ResiduePossible`. Abort permits no cleanup-guard or
  retained-reader disposition, and an outcome-unknown writer cannot produce an
  Abort result.
- Result ownership remains explicit and passive. Go result storage is
  address-bound and shallow copies reject operations. Rust result/owner/guard
  types are move-only and implement neither `Copy` nor `Clone`. A cleanup guard
  can be taken exactly once; its reported disposition remains `CleanupGuard`.
  Explicit destroy before take returns `HandleBusy` without changing or
  consuming the complete result; explicit destroy after take succeeds even
  while the transferred guard is unresolved. Ordinary Go/Rust destruction
  performs no cleanup. The later C handle projects this explicit contract; 2B
  does not expose it publicly.
- Permanent mirrored tests use the same case matrix for optional-group
  canonicality, artifact kind/role/tail rules, commit durability versus
  cleanup, abort outcome versus retained authority, exact/one-over caller
  basename storage, checked offset overflow and 32-bit conversion, raw POSIX
  bytes, well-formed Windows UTF-16LE, stable artifact/error association after
  cleanup compaction, invalid-construction authority return, guard take/destroy,
  Go copy rejection, Rust move-only behavior, and warmed zero allocation.
  Validation includes full/default, no-default, all-feature, race, vet, 386,
  strict Clippy, formatting, focused repetition, prohibited-surface searches,
  exact hashes, and two independent reviews per language.
- Early review of the evolving Go 2B draft found four valid pre-freeze defects,
  so that draft is not a candidate. It copied an address-bound coordination
  value without rebinding its self identity; rescanned the full cleanup ledger
  from every indexed artifact access, making enumeration quadratic; allowed
  caller-arena bytes to change from one valid basename to another without
  detecting retargeted cleanup authority; and retained callable aliases to the
  ledger and arena after terminal-result construction, unlike Rust's ownership
  move. The repair must rebind coordination in place, seal ledger/arena
  ownership to the exact address-bound result until explicit destroy, reject
  append/retry/reinitialization through old aliases while sealed, bind every
  basename record to its exact existing SHA-256 basename commitment, and perform
  the full invariant scan only once at construction. Indexed access then checks
  only that exact artifact/name/error, so complete enumeration is linear in the
  artifact and basename bytes. Failed destroy or copied-result operations must
  leave both seals and all authority intact; successful explicit destroy
  releases the seals but performs no cleanup.
- The same early review noted that Go cannot type-enforce Rust-style moves
  against code in the same package that retains and directly writes a backing
  slice. This is not solved with allocation, a full-ledger digest on every
  access, or a Merkle structure: each would violate the selected fixed-storage,
  zero-allocation, or simple linear-enumeration contract. The private
  constructor is therefore an exclusive ownership transfer, matching accepted
  2A: all supported ledger/arena mutators are sealed, the exact basename bytes
  are commitment-bound, and later public handles do not expose the typed
  backing slices. Any caller-provided raw scratch has an exclusive
  caller-serialization lifetime. Direct same-package memory corruption is
  outside this private safe contract, just as direct mutation of any unexported
  Go handle field is; permanent tests cover every supported mutation path.
- The first frozen Rust 2B candidate added private
  `writer_result_contract.rs` at SHA-256
  `734561a7c88f6092ccec6aebcdf97e496fed147217954d8575097d426a77f819`
  and changed `lib.rs` to
  `636ac34653ac1be5f6e2c2e1dcf50bad508fe0cc1f9f7d872dd432130ed71077`;
  accepted 2A remained exactly
  `8cc6695ce08871b6e787a44f2a031b23c10e70dfd8ffcb22f4a7f15ed54a2c5a`.
  Its default/no-default/all-feature matrices passed 357/357/449 tests, the
  14 focused release tests passed, and strict Clippy, formatting,
  zero-allocation, and prohibited-surface gates passed. It is nevertheless
  rejected: an independent Rust-1.74 review proved that the new basename-arena
  constructor unnecessarily used a `const fn` taking `&mut [u8]`, which Rust
  1.74 rejects as unstable `const_mut_refs`. Repair is the exact mechanical
  removal of `const` from that constructor followed by complete revalidation,
  new hashes, and both reviews again.
- That MSRV review also proved a separate pre-existing repository issue:
  accepted earlier Rust test/instrumentation files use `const { ... }` inline
  blocks that plain Rust 1.74 rejects before reaching 2B. The reviewer isolated
  the 2B failure by enabling only that pre-existing compiler feature. The 2B
  repair must add no new MSRV violation; before SOW close, the repository must
  either remove every pre-existing post-1.74 construct or explicitly resolve
  the declared `rust-version` contract. This remains work inside SOW-0016 and
  cannot be omitted from final validation.
- The repaired Rust Step-5 chunk-2B candidate changes only that rejected
  constructor from `const fn` to ordinary `fn`.
  `writer_result_contract.rs` is frozen at SHA-256
  `7d880dcd781726717e69debf78820a54f5494c5f787522b984d36896d56ca46a`;
  `lib.rs` remains
  `636ac34653ac1be5f6e2c2e1dcf50bad508fe0cc1f9f7d872dd432130ed71077`;
  and accepted 2A remains
  `8cc6695ce08871b6e787a44f2a031b23c10e70dfd8ffcb22f4a7f15ed54a2c5a`.
  The repaired candidate passes 357 default, 357 no-default, 449 all-feature,
  and 14 focused release tests; strict no-default and all-feature Clippy,
  formatting, and diff gates also pass. Rust 1.74, with only the repository's
  pre-existing `inline_const` feature enabled, compiles and passes all 14
  focused 2B tests without enabling `const_mut_refs`. This records a repaired
  candidate, not acceptance; both independent exact-hash reviews must accept it.
- The Go Step-5 chunk-2B candidate is frozen at
  `2d16d8b12c951469343a049da5298cbce028c8147b960180985bfd8344c67cca`
  for `writer_cleanup_contract.go`,
  `21548dd95d6e6943a257236d017462fc0082452d6460a188d8288ef5a8f90e81`
  for its test,
  `0f19a72617415392d4e0370389b70c76ba613b725f09e3f2372449dd143dda72`
  for `writer_result_contract.go`, and
  `c829bfe56865c6479fca46ea0c03c3cd1bd13f94bb3509fb18c892c7d9d8dd4b`
  for its test. Accepted 2A resource and transaction files remain on their four
  recorded hashes. The candidate binds each basename to its exact SHA-256
  commitment, seals transferred ledger and arena ownership until explicit
  result destruction, rejects copied-result operations, validates the complete
  aggregate once at construction, and performs O(1) indexed access plus linear
  enumeration. Full, race, vet, 386, five-shuffle, one-hundred-repeat focused,
  zero-allocation, compile-only, benchmark-compile, formatting, diff, and
  static-surface gates pass. This records a candidate, not acceptance; both
  independent exact-hash reviews remain mandatory.
- Two fresh independent reviews accept the repaired Rust Step-5 chunk-2B
  candidate on the three exact hashes above. Both reproduced 357 default, 357
  no-default, 449 all-feature, and 14 focused release tests; strict Clippy,
  formatting, diff, repeated zero-allocation, prohibited-surface, and unchanged
  hash gates. With only the separately recorded pre-existing `inline_const`
  baseline enabled, Rust 1.74 passes the same unit matrices and focused 2B
  tests without `const_mut_refs`. The reviews independently reconfirmed exact
  optional-group canonicality, artifact role/platform/tail matrices, stable
  per-artifact errors after compaction, commit durability independent of
  residue, the exact unknown-outcome and Abort matrices, invalid-construction
  authority return, move-only passive destruction, take-once guard transfer,
  linear work, and absence of public, filesystem, OS, unsafe, allocation, and
  destructor-cleanup surfaces. The earlier request for a Commit destination
  basename was withdrawn because section 14.2 and the recorded authority
  deliberately describe an already-open writer. Rust Step-5 chunk 2B is
  accepted on the repaired hashes.
- Read-only preparation found no unresolved product decision for Rust Step-5
  chunk 2C. The exact implementation surface is
  `private_page_pool.rs`, its existing `private_page_pool/` implementation
  submodules and in-module tests, one new private
  `writer_transaction_core.rs`, its tests, and one private `lib.rs`
  declaration. The pool gains an atomically
  reserved active/invalidation identity pair, a transaction-only vacant-pool
  constructor that returns caller slots on every failure, an exact O(P)
  permanent Abort epoch reserve, reserve-aware preflight on every direct,
  scoped, binding, close, checkpoint, rollback, and commit mutation path, and a
  consuming whole-draft discard that preflights completely before changing the
  first slot. A failed discard returns the original pool and authority
  unchanged; success scrubs every slot, reports exact visits, invalidates old
  capabilities, and returns the caller slice.
- The new private Rust transaction shell mirrors the accepted Go state machine:
  `Clean`, `Pending`, `AbortRequired`, and `AbortIncomplete`; a move-only active
  handle; exact typed errors; selected and optional target meta; immutable
  budget/current resource use; the accepted fixed cleanup ledger; and exclusive
  `Clean(caller slots) | Draft(pool)` ownership. Begin reserves both identities
  before mutation and accepts a checked caller-supplied nonzero commit nonce.
  A provably pre-mutation failure is neutral; any possibly mutated failure,
  including malformed post-mutation evidence, poisons the whole draft. Commit
  preflight checks state only. Abort scrubs at most once; later cleanup,
  interrupted-cleanup recording, or resource-release retry cannot scrub again.
  Permanent tests mirror the accepted Go poison, exact-limit/one-over,
  permanent-reserve, additive checkpoint reserve, failure atomicity, retry,
  stale-capability, exact O(P), move-only/passive-drop, and warmed
  zero-allocation matrices, plus a same-failure search across every Rust epoch
  mutation site.
- Step-5 chunk 2C does not add advanced-empty transitions, nonce generation,
  cancellation storage, OS cleanup, descriptors, public APIs, commit
  preparation/publication/durability, `Close`, `ResolveCommit`, or C ABI
  projection. It uses borrowed caller slices and existing 2A ownership; no
  `Vec`, `Box`, trait-object allocation, unsafe lifetime extension, or
  per-slot allocation is permitted. Mirrored 2B acceptance remains the gate
  before this recorded plan may be implemented.
- Two fresh independent reviews accept the Go Step-5 chunk-2B candidate on all
  eight candidate and prerequisite hashes above. Both independently confirmed
  the exact optional/security/tail and artifact matrices, per-artifact stable
  error association after compaction, SHA-256 detection of valid same-length
  basename retargeting, ledger/arena ownership seals, address-copy rejection,
  invalid-construction authority preservation, take-once guard and explicit
  destroy behavior, exact Commit/Abort matrices, derived cleanup state, linear
  enumeration, zero allocation, and passive destruction. Fresh full, race with
  zero reports, vet, 386, five-shuffle, focused repetition, allocation,
  compile-only, formatting, diff, unchanged-hash, and prohibited public,
  filesystem, OS, unsafe, and finalizer gates passed. One reviewer completed
  the full 100-repeat matrix including the internal linear-scaling benchmark.
  Direct same-package backing-slice writes remain outside the explicitly
  recorded private safe contract. Go Step-5 chunk 2B is accepted; with the Rust
  acceptance above, mirrored chunk 2B is complete and the recorded Rust-only
  chunk-2C plan is unblocked.
- The first Rust Step-5 chunk-2C implementation is frozen for validation, not
  review acceptance, at
  `a693817e73a2449242fc5331b58ced8e491e625cf1454b9f895980b0004bce70`
  for `private_page_pool.rs`,
  `833e9a02f1664bca21f7a5f6fee2427798cc67f82ec837605de6c4098195197c`
  for `private_page_pool/selective_finalization.rs`,
  `716cc2fd5842d40cf11474c46ef44f4d72d89464ae1871f10c6230b432a8388a`
  for `writer_transaction_core.rs`, and
  `3c9003b5fb86dd45494523bb9ee24b403d5e391ce65571506d8025aea0582b19`
  for `lib.rs`. Default/no-default/all-feature suites pass 372/372/464 tests,
  the 15 focused tests pass in release mode, and strict Clippy, formatting,
  zero-allocation, exact O(P), ownership, atomicity, and prohibited-surface
  gates pass.
- Full release validation nevertheless rejects this as a review-ready
  candidate: six existing bitmap reservation tests fail individually with
  `ArenaPageConflict`, while the same tests pass in debug mode and in release
  mode with debug assertions enabled. Root cause is a pre-existing release-only
  side effect in `bitmap_cow.rs`: the test-only reservation finalizer performs
  the required `PlannedCandidate -> Arena` index replacement only inside
  `debug_assert_eq!`, so optimized builds remove the mutation and carry stale
  index state into apply. Same-failure search found two production paths,
  insertion and removal, that likewise perform required
  `Verified -> Replacement` mutations only inside `debug_assert_eq!`.
  Validation cannot dismiss any of the three defects merely because 2C did not
  create them. Repair all three sites by executing the mutation unconditionally,
  assert only on the stored result, add release-mode regression proof for all
  three state transitions, rerun the complete
  debug/release matrices, and only then freeze the expanded candidate for
  review. This mechanical repair changes no product design.
- The repaired expanded Rust Step-5 chunk-2C candidate adds
  `bitmap_cow.rs` at SHA-256
  `3e5da57344310b7b0dcb244c4d6cb252b8772c02156493bec0afb7722b8f74ac`;
  the four frozen 2C hashes above remain unchanged. All three index replacements
  now execute unconditionally and only their returned prior state is
  debug-asserted. Permanent assertions cover planner
  `PlannedCandidate -> Arena` plus production insertion and removal
  `Verified -> Replacement` transitions. The six original optimized failures
  pass individually; the complete all-feature release suite passes 464/464.
  Default, no-default, and all-feature debug suites pass 372/372/464; strict
  Clippy in both feature modes, formatting, focused release repetition,
  zero-allocation, linear-work, same-failure, prohibited-surface, unchanged-hash,
  and clean lockfile gates pass. This expanded five-hash candidate is now
  review-ready; two independent exact-hash reviews remain mandatory.
- Both independent reviews reject the first expanded Rust Step-5 chunk-2C
  candidate despite its green validation. First, Rust scoped-operation tokens
  are bound only to caller plan storage; the pool records no active operation.
  A caller can perform one prepared mutation, retain or drop the unfinished
  token, and then receive successful writer commit preflight because the core
  checks only an active checkpoint. Rust ownership does not prevent this, and
  it contradicts the accepted Go active-operation refusal. Repair must register
  one exact active operation in the pool, reject concurrent operations, require
  exact registered identity on every prepared step, clear it only on successful
  finish or proven-unmutated abandon, preserve it and poison after any possibly
  mutated failure (including malformed evidence), make commit preflight reject
  it, and let
  whole-draft Abort discard it. Permanent tests must cover unfinished
  zero-step and partially mutated operations, retained and dropped tokens,
  forged identities, concurrent begin, finish, abandon, commit refusal, and
  discard invalidation.
- Second, the transaction Abort shell adds an unapproved `E: Clone` bound and
  clones 2A's borrowed exact first cleanup cause. Arbitrary error cloning may
  allocate, panic, have side effects, or be unavailable; a trivial cloneable
  test error did not prove the generic zero-allocation contract. Repair must
  preserve the exact retained cause by borrow, add no clone/copy/allocation
  requirement, and keep all cleanup ownership in the ledger. Tests must use a
  non-`Clone` error plus an allocation/panic-sensitive clone trap and prove
  exact borrowed cause, retry, interruption recording, and zero allocation.
  The five rejected hashes remain historical evidence only; the complete
  repaired candidate must be revalidated, rehashed, and reviewed twice from
  scratch.
- Third, the restored Rust `Cargo.lock` was stale relative to the already
  reconciled workspace manifests. Ordinary Cargo commands silently regenerated
  it, while a clean `cargo test --locked` failed before compiling. Therefore
  the earlier clean-lockfile claim meant only “no working-tree difference,” not
  a usable reproducible lock. The regenerated lock correctly removes obsolete
  v3/benchmark dependencies and records the current v4 dependencies; locked
  metadata now succeeds. Preserve this manifest-derived lock update, validate
  every final test/Clippy/build command with `--locked`, and include the
  lockfile hash in the repaired candidate. Restoring the stale lock again is
  forbidden.
- The rejected-2C repair has no unresolved product decision and mirrors the
  accepted Go lifecycle exactly. `PrivatePagePool` gains zero-initialized
  operation sequence, active-operation ID, operation-start epoch, and
  abort-required state. The move-only scoped-operation token gains its exact ID
  and next generation. Begin fully validates scope, slots, duplicates,
  bindings, permanent headroom, next operation ID/generation, and borrowability
  before registering infallibly. Every prepared step requires the exact
  registered token; every non-operation mutation/checkpoint/scope path rejects
  an active operation or pool poison. Successful finish consumes its reserved
  terminal epoch, installs the operation generation, and clears registration.
  Proven-unmutated abandon clears it without advancing epoch. Any failure after
  possible mutation, including malformed evidence, retains registration and
  poisons before returning. A malformed zero-mutation token also retains the
  real registration, so commit and other mutations remain refused until an
  exact proven-unmutated abandon or whole-draft Abort; it need not invent pool
  poison before mutation. A forged token can never clear the real operation.
  Writer commit preflight rejects core poison, pool poison, active operation,
  then active checkpoint. Whole-draft discard intentionally accepts and
  invalidates active/poisoned operation state while retaining exact O(P) work.
- `PrivateWriterTransactionError` treats its generic parameter as an arbitrary
  cause payload. Ordinary methods continue to use a cause-free payload; Abort
  returns `PrivateWriterTransactionError<&E>`, directly borrowing 2A's retained
  first cause. Both `E: Clone` and `.cloned()` are removed. A lexical cleanup
  scope ends the ledger borrow before later resource/coordination checks on
  success, while an error intentionally borrows the core until the caller drops
  it. Interrupted recording remains owned-in/owned-back on failure and moves
  the cause into the ledger on success. Tests cover non-`Clone` errors, a
  panicking/side-effecting clone trap, exact borrowed pointer identity, retry
  after borrow release, interruption, and warmed zero allocation.
- Active-operation tests cover retained/dropped zero-step and partially mutated
  tokens, concurrent begin, forged identity/generation/epoch/scope/counters,
  finish/replay, exact abandon, poison, all conflicting mutation/checkpoint
  surfaces, commit refusal, discard invalidation, terminal headroom, zero
  allocation, and unchanged linear work. Final validation regenerates and keeps
  the current manifest-derived lock, then runs all debug/release/Clippy/build
  gates with `--locked`. Any deviation from this mapping stops implementation.
- Live same-failure review found that an infallible prepared checkpoint begin
  could otherwise apply a stale preflight after an intervening operation in
  optimized builds. Do not replace this with a release assertion or panic.
  `begin_checkpoint_prepared` becomes fallible and runtime-revalidates exact
  pool identity, start epoch, generation, idle operation, poison, and checkpoint
  state before mutation. Its four existing call sites in
  `bitmap_cow/selective_finalization.rs` and `retirement_writer.rs` propagate
  the typed stale/pool error without changing their algorithms. Selective
  prepared plans carry and revalidate the expanded pool seal before terminal
  prepared paths. These two exact caller files are added to the 2C repair
  surface solely for typed propagation and seal validation; no broader bitmap
  or retirement behavior is authorized.
- The repaired Rust Step-5 chunk-2C candidate is frozen for fresh review on
  eight exact hashes:
  `Cargo.lock`
  `3c85efb104433b9cd3329cb8c87aad98fae1cc8b0ae88f8c89ef6a8dea0db1ff`;
  `private_page_pool.rs`
  `6adb93eb01799b675e29cf52fd33c83e305eb595be96a8967fd1c721edeb9c6f`;
  `private_page_pool/selective_finalization.rs`
  `392da02bbaacc420de9383eb8ffcbc00e37c8980afd7096b14f4117920cbfb76`;
  the protected `bitmap_cow.rs`
  `3e5da57344310b7b0dcb244c4d6cb252b8772c02156493bec0afb7722b8f74ac`;
  `bitmap_cow/selective_finalization.rs`
  `ead2448848e8caafe8be86d362e09cfccfbb42d2e64a5131f4191906524b0086`;
  `retirement_writer.rs`
  `2b3fe2b5eb88cf9f452cc134d31663459bf22d9ddc7327c5330bb9c111debe93`;
  `writer_transaction_core.rs`
  `f7c780f9cd4ec5b789a33ab45a8afbdfa34b4914332aed31a32a77f6dc861296`;
  and `lib.rs`
  `3c9003b5fb86dd45494523bb9ee24b403d5e391ce65571506d8025aea0582b19`.
- Locked validation passes 381 default, 381 no-default, and 473 all-feature
  tests in both debug and release builds. Both strict all-target Clippy modes,
  all-feature bench compilation, formatting, diff, locked metadata,
  active-lifecycle and borrowed-cause focused repetitions, discard-invalidation
  repetitions, zero-allocation, exact O(P), no-Clone/no-`.cloned()`,
  same-failure, and prohibited-surface scans pass. Permanent tests now cover
  exact operation registration/finish/abandon/poison/commit exclusion,
  forged-token neutrality, stale-token discard invalidation across a successor
  transaction, composite stage and selective seals, fallible prepared
  checkpoints, shadow checkpoint exclusion, non-`Clone` and clone-trap cleanup
  causes, exact borrowed pointer identity, retry after borrow release, and
  zero-allocation error reporting. This records a repaired candidate, not
  acceptance; both independent exact-hash reviews must restart.
- Two independent fresh reviews accepted the repaired Rust Step-5 chunk-2C
  candidate on all eight frozen hashes without edits or blockers. Each reviewer
  rechecked every hash before and after review and independently passed the
  locked 381 default, 381 no-default, and 473 all-feature test suites in debug
  and release, strict all-target Clippy in both feature modes, formatting,
  all-feature benchmark compilation, locked metadata/no-run builds, focused
  lifecycle/error/allocation/scaling repetitions, diff checks, and prohibited
  surface scans. Both reviews specifically verified the exact active-operation
  registry and token lifecycle, commit exclusion, failure-atomic O(P) discard
  and token invalidation, fallible prepared-checkpoint validation at all four
  typed callers, borrowed non-`Clone` cleanup causes with exact pointer
  identity, and unconditional optimized bitmap-index mutations. Rust Step-5
  chunk-2C is accepted. The regenerated `Cargo.lock` with hash
  `3c85efb104433b9cd3329cb8c87aad98fae1cc8b0ae88f8c89ef6a8dea0db1ff`
  is part of the accepted candidate and must not be restored to its stale
  predecessor.
- The immediate post-2C coordinator implementation uses a narrow private seam;
  it does not reinterpret the ordinary committed-page source or globally
  refactor committed-generation fields. A new private fixed-point coordinator
  owns the move-only transaction predecessor/successor chain, monotonic eligible
  source position, fixed caller-backed sealed-work ledger, current root/tail,
  and replacement disposition while borrowing the transaction-owned pool. A
  separate internal draft-page source copies a page together with exact
  `SelectedCommitted` or earlier sealed-scope private provenance. Only the COW
  source-miss path is extended to consume that provenance: committed pages are
  decoded at the selected generation and enter retirement/direct-free
  accounting; earlier private pages are decoded at the pending generation and
  are preflighted and returned to their exact owning sealed scope. This
  distinction participates in capacity, clone, operation-step, and release
  preflight; it is not rediscovered after mutation.
- Initial-predecessor capacity planning and attachment remain unchanged.
  Coordinator-specific later-work preparation consumes the exact predecessor
  under the live lock, plans from the draft source, reserves one vacant exact
  scope, binds against the current root and live tail, and validates the
  predecessor, scope, and pool commitments before mutation. Existing bound COW,
  finalization, sealed output, and finalizer-successor mechanics are reused; the
  coordinator consumes the finalizer successor and issues its own transaction
  successor, while the finalization predecessor remains cleanup authority for
  its sealed scope. Pool extensions are limited to exact lookup/copy of a page
  bound in a recorded sealed scope and prepared return to that exact owning
  scope, without exposing scope internals or scanning all scopes. Writer-core
  integration only owns/validates the coordinator and maps failures after
  private mutation to the existing whole-draft `AbortRequired` state. This
  implementation seam resolves no new product choice and preserves the
  previously approved coordinator contract.
- A sealed output cannot remain an immutable historical reader after a later
  work unit returns one of its pages: its retained binding/index metadata and
  exact-bound cleanup proof would become stale. Finalization therefore
  consuming-hands each sealed output to a generic-free coordinator record in
  caller-provided fixed storage. The record retains the exact scope, nonce,
  root/tail, binding/index state, cleanup scratch, and cleanup ownership. A
  prepared prior-private return atomically updates the pool and that exact
  record, refreshes its commitment, and leaves the scope sealed; it never
  reopens the scope or uses ordinary active-scope mutation authority. This is
  the required resynchronization of every touched earlier scope and prevents
  recursive output chains and stale cleanup metadata.
- Retirement traversal uses the same draft-source provenance seam. Its internal
  page residence distinguishes selected committed, current-scope private, and
  prior-sealed-scope private pages instead of treating every page outside the
  current arena as committed. Both private origins are decoded at the pending
  transaction; only selected committed replacements enter retirement history,
  current-scope replacements use the existing local release path, and
  prior-scope replacements enter the coordinator's exact owning-scope return
  disposition. Parent/child validation consults this provenance. The change is
  confined to retirement read/residence/retire helpers; tree algorithms and
  wire readers remain unchanged.
- Coordinator acceptance additionally requires: scope-local record identity
  plus one global predecessor epoch so later mutations do not force an O(kN)
  refresh of all sealed commitments; global indexed page lookup plus O(1)
  caller-backed slot-to-record resolution; preservation of finalizer cleanup
  authority across three or more later work units; complete callback, capacity,
  alias, exact-plan, source, and mutation-step preflight before the single-use
  predecessor consume boundary; and no callback after the final predecessor,
  pool epoch/tail, root, touched-record, and scratch-seal fence. After consume,
  the prepared suffix either finalizes and publishes one new root/tail successor
  or enters whole-draft `AbortRequired` with no successor. Tests must distinguish
  a monotonically skipped carried candidate from an owned page still advertised
  by the current bitmap, replace prior private bitmap branch/leaf and retirement
  tree/blob pages, prove atomic touched-record resynchronization and byte-exact
  foreign-record preservation, exhaust every caller scratch class before
  mutation, prove Rust move-authority and Go copy-safe single-consume semantics,
  and prove existing transaction abort discards coordinator-created active and
  sealed scopes without stale replay. No live-lock allocation, work-unit scan,
  file/descriptor action, or unbounded discovery is allowed.
- Real second-work-unit integration independently exposed the same producer
  defect in Go and Rust. A first finalized unit legitimately reinserts unused
  committed-free page 9 into its draft free bitmap, but finalization keeps page
  9 globally bound in the sealed scope as `ReleasedFree`/`ReturnedFree`. The
  next unit enumerates page 9 as free while the pool still reports it owned and
  correctly rejects the draft as stale/inconsistent. This is not a fixture
  issue and must not be hidden by a coordinator skip. Go evidence:
  `bitmap_insert.go` marks reinserted arena pages released-free; the terminal
  prefix acceptance/copy in `bitmap_finalize.go` retains that bound state.
  Rust evidence: the finalization fixed point returns the page free, then
  terminal binding reconstruction and prefix copying retain it in the sealed
  scope. The previous finalization tests rejected `Available` but did not reject
  this released-free advertised-and-owned state.
- The contract-compatible repair reopens only this finalization producer
  boundary; the accepted Step-5 chunk-2C authority/abort repair remains valid.
  Finalization discovery classifies every arbitrary non-tail returned-free
  binding plus every truncated appended-tail binding as an unbind target.
  Preflight covers the complete target set with the existing caller-owned
  selective overlay/path/target machinery, including recomputed capacity,
  mutation steps, alias checks, and short-scratch rejection before mutation.
  Shadow/apply delete those targets from the pool's global and scope indexes,
  canonicalize their slots as vacant, stable-compact only retained in-use output
  bindings, and remap every page-index arena/storage/active reference. One
  checkpoint suffix applies deletes, compaction, vacancy/member changes, scope
  sealing, successor state, and the new cleanup proof; only retained reachable
  pages remain bound. Tests cover low/middle/high returned-free positions, a
  page advertised free and absent from pool ownership, three or more units,
  branch/leaf reads, successful later cleanup, scratch exhaustion/alias
  atomicity, foreign byte preservation, zero allocation, and work proportional
  only to the touched scope. Cross-scope transfer and transaction-wide
  quarantine are rejected because they preserve dual ownership or add a second
  fixed point and cleanup state without benefit.
- Producer repair exposed the allocator's legal one-page exhaustion base case.
  When a later unit removes the free tree and the only otherwise returnable page
  is the page needed to represent that free tree, returning it would require
  advertising the bitmap's own storage page as free, while dropping it with root
  zero would leak an unreachable page. Finalization therefore promotes that
  page to a reachable legal all-zero level-0 free-bitmap leaf, preserves the
  selected-candidate target, and converges with the page retained as allocator
  metadata rather than advertised free. This is permitted by the normative
  bitmap rule that all-zero leaves should, but need not, be omitted. Permanent
  tests cover the one-page/root-zero transition and reject looping, leakage, or
  self-advertisement. Both bitmap leaf parsers/traversals must therefore accept
  a structurally canonical all-zero leaf and report no free bit; rejecting it
  would turn the normative `SHOULD` into an unauthorized `MUST` and reintroduce
  the audit's legal-empty-leaf defect.
- Final Go same-failure review found that duplicate work-unit detection scanned
  prior records and that caller record scratch could alias and overwrite an
  earlier active record before predecessor consumption. The coordinator repair
  uses a monotonic O(1) last-work-unit fence, rejects every live-record/scratch
  overlap before any write, and validates record capacity, identity, and seals
  before consuming the predecessor. Neutral failures preserve caller scratch
  and predecessor authority. Permanent tests cover nonmonotonic/replayed work
  units, live-record aliasing, record exhaustion, and consumed-seed scratch
  reuse. These are acceptance blockers already implied by indexed lookup,
  bounded work, alias rejection, and the atomic consume boundary; they introduce
  no new product choice.
- The Go provenance-aware multi-work-unit coordinator candidate is frozen for
  independent review on 18 exact hashes:
  `writer_fixed_point.go`
  `483b856283da2e475f8bc1bd282c6145f34efa8d87c68d53983a2ac5ee221c53`;
  `writer_fixed_point_test.go`
  `ee3aa1bae28c4d143db7b6124d342e8566f5b3f003c0e5dce0f96a696a5ac74e`;
  `writer_fixed_point_core_test.go`
  `e05773910b4a3125c74d4bec7332246cce24c0ce8ba80a062e545c310416f5d4`;
  `writer_transaction_core.go`
  `aac4473d47b57937a0c87585aff50d66da58305fa063e6dd6a74c34402ab5876`;
  `retirement_writer.go`
  `41e4c8e62e27f00b0daf81ac3d350fefc48a1225c9674cb2037df57ff37ed0e3`;
  `retirement_plan.go`
  `1cbc6f97dad5d2278318db3df3c24d0c07f12f8941d23214fb6d2c03289c4450`;
  `retirement_fixed_point_test.go`
  `01f58781c45c7889d0a2209fa6c98ea65b1e298cf5eb4be1050f814d6373a9cb`;
  `bitmap_finalize.go`
  `ca4f8fca08dc5848b9b140f8dd313c4c9d44a89498e907c146884765e4ac0f13`;
  `bitmap_finalize_test.go`
  `de7105290e6d203ccd46eef7a30d9f2d24bba2bdde6b37fa112bb2b029e4666e`;
  `bitmap_finalize_released_free_test.go`
  `48285074c0a6147e3982efa3a75c488de402f21eff8afc1383dfd6714a9f2445`;
  `bitmap_reservation.go`
  `bbce0d57926cb5a5aea7a24e43c77746323e7bd582e44307dc3f2aa710886c94`;
  `bitmap_insert.go`
  `ac5db5a58658603e4a36ff4147b0c21c60b83946cbce5e69c131ba35eef3e35f`;
  `bitmap_cow.go`
  `dd3ea31db2f382c9ceeefbeded2c67f16d938b5632c5b94bd6d404bd5738fb89`;
  `private_page_pool.go`
  `96745e5aaee3d73ead422e494be7fc890713f0d05d8d2ca22db726954a248d5c`;
  `bitmap_page.go`
  `38ace2bedb6a8eb254014d23be5d6dd7aa2a347d0447a9e2b5c8b05515ce78e9`;
  `bitmap_page_test.go`
  `4bbce5ff36efec315b56e6e6cd0eafd4f0d862d7f24b63c8291f521a24e519c8`;
  `bitmap_reservation_all_bound_compat_test.go`
  `1833b6dcbdf3045d3ef0559daad323909857b3f0122f202b122d49869f489076`;
  and `bitmap_reservation_insert_test.go`
  `1abb8804c870073bbdee3b03a82cff0056c11fcbfdd5b336cd4c490fb9c69eb0`.
  Frozen validation passes ten acceptance-focused callback/scratch/alias/core/
  fixed-point/retirement repetitions, full Go tests, vet, 32-bit tests, race,
  and ten allocation/scaling repetitions. Static review finds no public
  exposure, file/descriptor/temporary-file action, unsafe path, coordinator
  allocation, per-work-unit scan, or per-record liveness scratch. This records
  a candidate, not acceptance; independent exact-hash reviews must still pass.
- The Rust provenance-aware multi-work-unit coordinator candidate is frozen for
  independent review on ten changed source files plus the preserved accepted
  lockfile:
  `Cargo.lock`
  `3c85efb104433b9cd3329cb8c87aad98fae1cc8b0ae88f8c89ef6a8dea0db1ff`;
  `lib.rs`
  `e1b8983748ecb26d52bc220d26fa789849ef497d1a61f4ca11de957437ff61e3`;
  `writer_transaction_core.rs`
  `ab6bbab057efab3f49e5bacb2d58b2f8cefe35fcfe23ce22b98c5ae8c7dc6fe2`;
  `private_page_pool.rs`
  `0e72fc6410227b7b9b29ec14d0c81ee1ebbb2dfbe6d79b0c2278bf0dbd642854`;
  `private_page_pool/selective_finalization.rs`
  `b4b12092f5c89a0fd01d68dd7ca57fa7bf3da6cc560ba3ea6d351096fb1b16aa`;
  `bitmap_page.rs`
  `5172f50250061f134a81ce2a532bf80703a8173aeee9bdd02628ad32a8263a1a`;
  `bitmap_reader.rs`
  `4edd619ec92067f5a84d6e6c102fcdce3083b69096ce174d76739f7fce4e3af8`;
  `writer_fixed_point.rs`
  `4621faa825441ff3f7697e3bc9715232e03d16fa5889e13ac10dd789a080e637`;
  `bitmap_cow.rs`
  `60c7e1b720ce3b8e737e9dc8b6d0b360ba6f37c8113e29c0fa225953f75ce86b`;
  `bitmap_cow/selective_finalization.rs`
  `8aa2da76a068469ac8106ca143731755cbf41c1d411a290e55ba451865dde2f6`;
  and `retirement_writer.rs`
  `29fba971c5cebd05ab0389104a18329144a0aa1b4cfbdf239bc645a8290727d4`.
  `page_source.rs`
  `316f70c17075516190af152525b5d90e707ab671604f95805800f155e9f6bc2b`
  was touched during a rejected broad-trait attempt and restored byte-exact; it
  is preservation evidence, not part of the changed candidate. Frozen
  validation passes 394 no-default and 486 all-feature tests, formatting, and
  strict all-target Clippy. Focused evidence covers three-unit provenance,
  legal empty-root continuation, O(1) sealed ledger lookup/work-unit fencing,
  caller-storage alias rejection, scope-exact retirement tree/blob reads,
  retry-safe zero-allocation staged prior returns, mixed committed/prior
  disposition, writer-core commit/abort fences, and reverse cleanup. This
  records a candidate, not acceptance; independent exact-hash reviews must pass.
- Both independent exact-hash reviews reject the frozen 18-file Go candidate.
  All hashes and broad validation remained green, but the coordinator only
  accepts an already-finalized object after mutation. It does not issue and own
  the authority under which planning, reservation, binding, prior returns,
  mutation, and finalization occur. `acceptFinalized` does not bind the result
  to the predecessor's exact input root/tail or to a coordinator-issued prepared
  plan; `result.output` and `successor.output` are separately copyable, so a
  valid successor can accompany a substituted output root. Tests even construct
  and finalize a result before the coordinator exists. Scratch from a new unit
  is not checked against earlier retained records before mutation, no explicit
  monotonic carried-source cursor exists, post-mutation classification trusts a
  caller-supplied boolean, and the commit fence does not reject an active scope
  that was never accepted into the coordinator chain. An owned page still
  advertised by the current bitmap is also reported as retryable stale state
  instead of whole-draft inconsistency. Passing tests missed these authority
  failures.
- The mandatory Go repair moves the complete work-unit lifecycle under one
  coordinator-issued single-use authority. Before consume, the coordinator
  validates the exact predecessor root/tail/source cursor, pool and retained
  record commitments, capacity, every caller scratch seal/alias, scope vacancy,
  and the genuinely last source callback. It emits a prepared work authority
  bound to that predecessor and exact scope. Consuming it registers one active
  coordinator work unit in pool/writer-core state; the prepared suffix alone may
  plan exact physical pages, bind, mutate bitmap and retirement state, apply
  prior-private returns, finalize, and produce one inseparable sealed result.
  The result carries the input predecessor seal and coordinator work identity;
  output and finalizer cleanup/successor authority cannot be independently
  substituted. Every pre-consume failure returns the exact predecessor and
  leaves scratch/state unchanged. After consume the coordinator itself knows
  whether private mutation began; any fallible outcome outside the prepared
  composite rollback enters `AbortRequired` and issues no successor. The pool/
  writer commit fence rejects registered work, active or unaccepted scopes,
  checkpoints, operations, poison, and incomplete cleanup. The coordinator owns
  a monotonic carried-source cursor and distinguishes already-consumed carried
  entries from a page that the live draft still advertises while pool-owned;
  the latter poisons the whole draft. Permanent tests cover wrong-root/tail
  results, finalized-before-authority results, two competing plans, copied/
  substituted output and successor values, scratch alias before consume,
  source-cursor replay, unaccepted active scope at finish/commit, advertised-
  owned `AbortRequired`, every callback/scratch fence, and abort after every
  registered-work phase. No repair begins until this map is recorded; the
  rejected hashes remain evidence only.
- The independent exact-hash Rust review also rejects all 11 frozen hashes.
  `advance` accepts caller-supplied root/tail/source scalars with no finalized
  record, exact scope proof, plan, or ledger binding; a writer-core test advances
  arbitrary values and passes commit. Commit ignores the pool's active-scope
  state. Carried authority stores only a count, so a later unit can regress from
  page 9 to page 5. Bitmap planning reads through the ordinary committed-source
  callback and loses prior-private provenance; the test manually rediscovers
  and returns the page after finalization instead of preserving disposition
  through preflight/apply. Finalizer authority is consumed before ledger scratch
  alias rejection, and ledger insertion scans every prior record. All debug/
  release, feature, lint, format, benchmark, allocation, and scaling gates pass
  because they do not test these missing authority links. The Rust candidate
  requires the same lifecycle redesign as Go: coordinator-issued pre-mutation
  work authority, exact plan/result binding, provenance carried through the
  engines, registered-work/active-scope commit exclusion, value-bearing
  monotonic source authority, O(1) record registration, and pre-consume alias/
  scratch fencing. The rejected Rust hashes remain evidence only.
- The cross-language lifecycle redesign is fixed as the staged hybrid; it is the
  only architecture that preserves the normative rule that two speculative
  plans may inspect one predecessor while only one may consume it. A monolithic
  operation requires the same borrowed preparation phase and therefore reduces
  to this design. Threading a token through independent primitives cannot prove
  the aggregate final-callback, alias, result-identity, or commit fences without
  adding the same coordinator state and also reduces to this design. No new
  user/product decision exists.
- The coordinator state machine is `Ready -> Prepared -> Active -> Sealed ->
  Ready/Finished`. `Ready` owns one exact predecessor identity, generation,
  nonce, sequence, root, pending tail, value-bearing carried-source cursor, and
  global epoch. `Prepared` is an address-bound caller slot that borrows but does
  not consume the predecessor and owns every callback result, exact bitmap and
  retirement plan, future scope reservation, proof, capacity/mutation budget,
  scratch range/seal, and expected input/output commitment. `Active` begins only
  when the prepared token and predecessor are atomically consumed and one exact
  work ID/phase is registered in coordinator, pool, and writer core. A
  callback-free prepared suffix alone may reserve the exact scope, bind physical
  pages, mutate bitmap and retirement state, apply prior-private returns,
  finalize, resynchronize records, and seal. `Sealed` writes directly into one
  coordinator-owned canonical record; output, cleanup authority, finalizer seed,
  and successor are not separately substitutable. It then issues one opaque
  successor or consumes the final predecessor into `Finished`. Any unexpected
  post-consume failure without the same complete rollback journal sets
  whole-draft `AbortRequired` and issues no successor.
- Go tokens are copy-safe opaque coordinator/slot/generation/nonce identities;
  all single-use state is canonical in coordinator slots. Rust authorities are
  move-only, but persistent pool/core registration—not destructor behavior—is
  the commit fence. Prepared slots are self-address-bound and sealed. Before
  prepare succeeds, the coordinator validates predecessor/root/tail/cursor/
  epoch, monotonic work ID, record capacity, canonical-zero scratch, every
  bounds/alias/capacity/proof/source/scope plan, and mutation headroom. It owns
  every source read and performs the genuinely last source callback, then
  immediately revalidates pointer/bounds, content hashes, pool/record/root/tail/
  cursor commitments, and scratch seals. Consume revalidates these commitments,
  increments one predecessor generation to invalidate sibling plans in O(1),
  and registers work before the first live mutation. No callback is legal in the
  active suffix.
- Caller storage is transaction-start prevalidated and partitioned with checked
  monotonic offsets. One canonical-zero preparation workspace is reusable;
  every sealed record owns a disjoint retained binding/index/cleanup partition,
  and the coordinator owns those partitions until cleanup/abort. This makes
  alias proof O(1); arbitrary per-work slices that require scanning all prior
  records are forbidden. The O(1) slot-to-record map is the liveness authority.
  A prior-private return atomically updates only its owning record, slot map,
  binding/index representation, cleanup proof, and commitment. The carried
  source state stores the ordered source identity, ordinal/cursor, last page, and
  source epoch—not merely a count. Only the carried leg skips entries before the
  cursor. A page freshly enumerated from the current draft while pool-owned is
  `AdvertisedOwnedPage` and poisons the whole draft.
- Pool state gains coordinator session identity/generation, registered work
  identity/generation/phase, work start epoch, mutation-started state, exact
  scope identity, unaccepted-scope count, and cleanup-pending count; writer core
  mirrors registered work/phase. Direct scope reservation, later attachment,
  binding, coordinated bitmap/retirement mutation, prior-private return, and
  finalization reject use on a coordinator-owned pool without the exact internal
  work fence. Existing direct primitives remain available only for standalone
  non-coordinator pools and foundational tests. Commit/finish reject registered
  work, active operation/checkpoint, poison, any active/unaccepted scope,
  cleanup residue, or root/tail/cursor/epoch mismatch. Abort/discard clears all
  coordinator/work/record/map state and changes generation so every saved token
  becomes stale.
- The exact Go split points are: scope-reservation preflight before
  `private_page_pool.go`'s first counted reservation mutation; bitmap bind
  preparation through `preflightRealApply` before the live apply in
  `bitmap_reservation.go`; finalization preparation through
  `preflightFinalizationApply` before the live checkpoint/apply in
  `bitmap_finalize.go`; retirement preparation through all callback, mutation,
  and headroom checks before `retirement_plan.go` live apply; and prior-private
  return converted from its standalone mutation in `writer_fixed_point.go` into
  a prepared cross-scope operation. Rust mirrors these semantic boundaries with
  borrowed preparation and move-consuming execution.
- Ordered implementation chunks are:
  1. pool/core coordinator session and registered-work state, opaque tokens, and
     strict commit/abort fences;
  2. prepared scope reservation and direct-bypass rejection;
  3. prepared-work storage/seals, value-bearing carried cursor, final-callback
     fence, and competing-plan semantics;
  4. prepared bitmap bind/mutation/finalization writing the canonical record and
     inseparable result;
  5. prepared prior-private returns with record/index/cleanup resynchronization;
  6. retirement planning/apply under the same work authority;
  7. final record cleanup/write transition, finish, and abort invalidation;
  8. migrate high-level tests while retaining standalone low-level tests only on
     non-coordinator pools; and
  9. full cross-language, race/32-bit, debug/release/features, lint/format/
     benchmark, repeated zero-allocation/scaling, static-bypass, and two fresh
     exact-hash review gates.
  Go files are `writer_fixed_point.go`, `writer_transaction_core.go`,
  `private_page_pool.go`, `bitmap_reservation.go`, `bitmap_cow.go`,
  `bitmap_insert.go`, `bitmap_finalize.go`, `retirement_writer.go`, and
  `retirement_plan.go` plus their focused tests. Rust mirrors the coordinator,
  writer core, private pool/selective finalization, bitmap COW/finalization, and
  retirement writer modules. Permanent matrices cover wrong/substituted root,
  tail, cursor, output, result, and successor; finalized-before-authority; two
  competing plans; copied/forged/address-copied authorities; every callback and
  scratch failure/alias; direct primitive bypass; unaccepted scope at finish/
  commit; carried skip versus advertised-owned abort; prior-private bitmap
  branch/leaf and retirement tree/blob disposition; exact touched/foreign record
  behavior; abort after every active phase; three or more units, reverse cleanup,
  zero allocation, and bounded scaling.
- Go staged-hybrid chunks 1-3 are frozen for fresh exact-hash review:
  `writer_fixed_point.go`
  `4a010bdb31a27f75aafc441610d2d6c47c33d8a71ae17dec339c33e11d95cbf7`;
  `writer_fixed_point_authority.go`
  `dbdc26b1686a5045f383abbf7af9a3cd44de733974ebf32a0a1eb3c4bfc3d909`;
  `writer_fixed_point_authority_test.go`
  `ad451d7faf2dedb18da635e23bb577f55f8295f0af0f2463fe632bac8db0265c`;
  `writer_transaction_core.go`
  `b4db04e583ccdd9eff5a2efa97e33b27faf06b8dc5a009f690f36609dfbf3073`;
  `private_page_pool.go`
  `e2ae584efea5785bfe6d2394ee7386c25d1048e2540d7031380801249a5c29f1`;
  and `bitmap_reservation.go`
  `a17b0e4e1ac63a0b5512aca37fbe26c5baf9e937558c9120680582d31e268504`.
  The candidate adds persistent coordinator session/registered-work state,
  prepared address-bound scope authority, copy-safe single-consume tokens,
  value-bearing carried-source state, final-callback scalar/slice/pool/scratch
  seals, direct reservation bypass rejection, strict finish/commit/abort fences,
  and abort invalidation. Eight permanent authority tests and the complete Go,
  race, vet, 32-bit, zero-allocation lifecycle, and static bypass gates pass.
  Staticcheck's 28 repository findings are pre-existing and none originate in
  the new authority/core files. Chunks 4-6 are intentionally absent. This
  records a candidate, not acceptance; two fresh exact-hash reviews must pass.
- Both fresh reviews reject the six-hash Go chunks 1-3 candidate despite all
  mechanical gates passing. First, consume reserves and mutates scope slots/
  epochs before pool and writer core register the active work; registration must
  precede the first live write. Second, the pool accepts a forgeable boolean
  authorization and a permanent test itself uses that bypass to mutate a
  coordinator pool without a prepared work capability. Third, prepared seals
  cover slice identity/length/capacity but not pool-slot, record, slot-map, plan,
  or prepared-slot contents, and the complete coordinator seal captured around
  the last callback is not stored and revalidated at consume. Fourth, the caller
  supplies `nextSource`, candidate ordinal/page, and carried classification;
  consume installs those values rather than deriving them from coordinator-owned
  source enumeration. The carried tuple check also uses a conjunction, so one
  regressed component can escape. Fifth, prepared-mode failure can still call
  the legacy caller-boolean mutation classifier and obtain a retryable result
  after consume.
- The chunks 1-3 repair is exact: consume first atomically registers one work ID
  in coordinator, pool, and writer core with phase `registered` and no scope;
  only that exact unforgeable work identity may call a private prepared
  reservation apply, after which pool state records the exact scope and advances
  phase. A boolean authorization is deleted. Preparation stores and consume
  revalidates the complete scalar, pointer, bounds, plan, pool-slot, record,
  slot-map, scratch-content, and prepared-slot seals captured immediately after
  the last callback. Scratch/slot contents are canonical-zero or hashed within
  their bounded regions; identity-only seals are insufficient. Carried-source
  progress is derived exclusively from the coordinator-owned ordered source and
  callback result; the request cannot supply ordinal/page/classification, and
  every ordinal/page/source-epoch component must advance consistently. Prepared
  work failure classification reads persistent registered phase, pool mutation
  epoch, and mutation-started state; the legacy boolean path rejects coordinator
  mode. Tests prove registration before the first slot/epoch write, forged
  boolean removal, direct raw operation/bind rejection, post-prepare content
  mutation of every sealed arena/slot/map/record, complete seal retention,
  arbitrary cursor jumps/omissions/misclassification, one-component tuple
  regression, and false mutation reporting after consume. The rejected six
  hashes remain evidence only.
- The repaired Go chunks 1-3 candidate is frozen for fresh review on six exact
  hashes:
  `writer_fixed_point.go`
  `4691953e9099de41458173a17602dd3582440bc480544453ae6a517f1facdfbe`;
  `writer_fixed_point_authority.go`
  `edca3b880b2c425ea24a395e834a258b54114e67a2172b1de8c0260f1d31afe3`;
  `writer_fixed_point_authority_test.go`
  `33ab677b843cc0d5f0db7c53b050d78d337a76b0277d24f62b8656242f1762b4`;
  `writer_transaction_core.go`
  `9e41dbeda70b327d410a6cda2dd65ea14c062f3afa226a5b0babdbd56ac8b5c6`;
  `private_page_pool.go`
  `78b60186e5e8f0be071cbb9c454a4edda065fdf0ff9010009d68f12726ddf3a5`;
  and unchanged-from-first-candidate `bitmap_reservation.go`
  `a17b0e4e1ac63a0b5512aca37fbe26c5baf9e937558c9120680582d31e268504`.
  The boolean reservation bypass and unchecked entry are removed. Exact
  coordinator/pool/core registration precedes scope mutation; prepared work
  stores and revalidates full bounded content seals; source progress is
  coordinator-derived; raw and preflight checkpoint/operation/bind/claim/commit
  bypasses reject coordinator pools; and prepared failure derives from
  persistent registered phase/mutation state. Thirteen authority tests plus
  submatrices, full/race/vet/32-bit, repeated zero-allocation, diff, and static
  bypass gates pass. Staticcheck retains exactly 28 pre-existing findings and
  none affect the authority/core paths. Chunks 4-6 remain absent. This is a
  repaired candidate, not acceptance; two fresh exact-hash reviews must pass.
- Both fresh reviews reject the repaired Go chunks 1-3 hashes. Callback content
  hashes are first computed after the final callback, so callback mutations can
  become the accepted baseline; the source-state pool pointer is not sealed;
  equal last-page replay is accepted; complete vacancy-chain/scope-exhaustion
  validation is deferred until after predecessor consume; and coordinated
  prepared pool apply helpers still lack the exact work fence. The candidate
  also hashes the entire pool arena, complete retained-record arena including
  4-KiB page buffers, and whole slot map at prepare and consume. Repeated work is
  therefore at least quadratic in retained state; zero-allocation tests do not
  prove bounded work.
- Arbitrary caller-retained writable aliases and O(1) mutation detection are
  mathematically incompatible: detecting an untracked write requires rereading
  all aliased bytes or OS page protection. The long-term Go design therefore
  uses an opaque SDK-owned `WriterWorkspace`, created once from explicit checked
  capacity budgets outside the operation path. Its pool pages, records, slot
  map, prepared slots, and scratch arrays are unexported and never returned as
  writable slices; callbacks receive value-only source/cursor inputs and no
  workspace reference. Operations remain bounded and zero-allocation. Caller-
  slice constructors remain only for standalone non-coordinator foundational
  tests. Rust may enforce equivalent exclusivity with ownership/borrowing, but
  production SDK construction still hides internal workspace storage.
- Workspace layout is partitioned once with checked monotonic offsets and an
  O(1) layout generation. Public/coordinated writer mutation rejects callback
  reentry. The final-callback fence compares coordinator/predecessor/cursor
  generations, pool mutation epoch, ledger/slot-map/layout versions, root, and
  tail before and immediately after the callback. Each immutable retained record
  gets a generation/commitment when its touched output is sealed; a prior return
  updates only that exact record and slot-map entries. Current plans seal only
  their own touched partitions. Arbitrary memory-corruption discovery belongs to
  explicit `Validate`, not hot-path full-arena scans.
- Scope reservation is split into: read-only preflight over exactly the
  requested `count` vacant nodes, including complete chain/boundary/epoch/
  headroom/scope-sequence proof; atomic installation of one canonical pointer-
  identity work fence in coordinator, writer core, and pool; exact revalidation
  of those same touched nodes before mutation; mutation-started marking; and a
  callback-free mechanically infallible apply. The canonical fence contains
  self/session/work/generation/phase/scope identity. Every coordinated prepared
  checkpoint/operation/bind/claim/write/commit/close helper requires that exact
  pointer and phase; infallible apply helpers are hidden behind the pool module.
  Standalone and coordinated entry points are separate—`nil`, booleans, and
  copyable value tokens never authorize coordinator mutation. Equal page replay
  is rejected, the source-state pool pointer is sealed, and callback-time
  mutation tests use only sanctioned APIs/version counters because callbacks
  cannot access opaque backing memory. The twice-rejected hashes remain evidence
  only.
- Rust staged-hybrid chunks 1-3 are frozen for fresh exact-hash review:
  `private_page_pool.rs`
  `92298c30f0d958471e3876b8972c9a4762758c8fce3cd2b4e1c805d1d93d897c`;
  `writer_fixed_point.rs`
  `3938a6e557b458eb4c287ae620355c6a4904a4fd0c0d5d99e97df13c403ea7a6`;
  and `writer_transaction_core.rs`
  `6bab64169f88396986824bc4f53c52339d9a923eda97d1ded724ba6617f1f3df`.
  The accepted lockfile remains
  `3c85efb104433b9cd3329cb8c87aad98fae1cc8b0ae88f8c89ef6a8dea0db1ff`.
  The candidate enforces exclusive callback backing isolation; pre/post callback
  version and current-content commitments even on callback error; exact
  pre-consume vacancy/boundary/binding-epoch/scope-sequence/epoch-headroom
  preflight; address/content seals; strictly advancing value-bearing carried
  source; persistent writer-core/coordinator/pool work identity and phase;
  production scope-helper exact Active/Sealed scope identity; raw unscoped and
  checkpoint bypass rejection; mutation-derived failure; and cfg(test)-only
  completion/direct adapters. Debug and release pass 410 no-default and 502
  all-feature tests; strict all-target Clippy, formatting, and diff checks pass.
  Bitmap, prior-return, and retirement helper signatures intentionally remain
  chunks 4-6 integration work; production ownership makes them unreachable
  outside active work in chunks 1-3, while foundational direct helpers are
  cfg(test)-only. This records a candidate, not acceptance; two fresh exact-hash
  reviews must pass.
- Rust staged-hybrid chunks 1-3 are accepted at the frozen hashes above after
  two independent exact-hash reviews. Both reviewers matched every source hash
  and the accepted lockfile before and after review. They independently passed
  locked debug and release no-default tests (410 each), all-feature tests (502
  each), strict all-target Clippy, formatting, benchmark compilation, focused
  authority/reentry repeats, and the zero-allocation 4,096-slot preparation
  case. Authority review confirmed pre-mutation registration and exact vacancy
  preflight, exclusive callback backing, strict value-bearing source progress,
  pre/post callback seals even on callback failure, identical core/coordinator/
  pool work identity and phase, commit and abort fences, and production
  rejection of raw or test-only completion paths. Performance review confirmed
  that chunks 1-3 scan only the requested scope, supplied scratch, and carried
  input; the modules remain private and add no file, OS, unsafe, or public API
  surface.
- This acceptance is limited to chunks 1-3 and records two mandatory chunks 4-6
  repairs. `FixedPointSealedLedger::push` still scans prior records and its
  rollback scans the slot map (`writer_fixed_point.rs:396-440`), while existing
  scoped-operation duplicate detection is quadratic
  (`private_page_pool.rs:4497-4502`). Production completion remains
  intentionally test-only (`writer_fixed_point.rs:1259-1313` and
  `writer_transaction_core.rs:375-406`), so the accepted foundation cannot yet
  publish a coordinator result. Chunks 4-6 must remove those scaling defects
  while integrating bitmap/finalization, prior-private returns, and retirement
  under the accepted authority; they must not reopen chunks 1-3 without a new
  frozen-hash review.
- The repaired Go staged-hybrid chunks 1-3 candidate is frozen for two fresh
  exact-hash reviews:
  `bitmap_reservation.go`
  `52c3e79c321bc4abecf4c1df22701e480aee048966730cb1329bf6feea7dea6e`;
  `private_page_pool.go`
  `893ade540e0c1fb0ebbda3a04f0fba8908e22cd0ef616a21a1b157fae82876cf`;
  `writer_fixed_point.go`
  `b3a5593aba02223cc470f47b53baf7ae16b5e28fe36c5979ef9f5e9293225281`;
  `writer_fixed_point_authority.go`
  `2e5f5f276c50576b9363512c30d5a078d3821b85b68e0de82018add9064ebd9b`;
  `writer_fixed_point_authority_test.go`
  `4859d72914acd71322d6764f68f0f63b48b3a8afcae9dd78c98c1467c3ce2d68`;
  `writer_transaction_core.go`
  `906665a69098fed74e9130959d1b3b3ebbf0937fe8c854d1535868cfbfa239c5`;
  and new `writer_workspace.go`
  `6c2cf802632fce8007d70d3ec4a9d8388fa41bd0b3adebcc75c0757f9424453e`.
  The candidate replaces caller-owned preparation backing and full-arena hashes
  with one checked SDK-owned opaque workspace, callback-reentry rejection,
  immediate scalar/version/source-pool fences, strict carried-page progress,
  touched-only vacancy preflight and revalidation, separate standalone and
  coordinated reservation paths, and one embedded canonical pointer-identity
  work fence installed in core, coordinator, and pool before infallible scope
  apply. Full Go tests, focused prepared tests repeated 20 times, race, vet,
  `GOARCH=386`, and allocation tests pass. Full staticcheck still reports 28
  pre-existing findings outside the changed workspace/authority/core files;
  the changed files are clean after repairing one candidate-introduced SA4006
  finding. This records a candidate, not acceptance; both exact-hash reviews
  must independently confirm authority, bypass, failure, and scaling behavior.
- Both independent reviews reject the frozen Go chunks 1-3 candidate; its seven
  hashes remain evidence only. The authority review proved that the canonical
  fence is validated only by initial scope application
  (`writer_fixed_point_authority.go:677-784`). Once Active, generic close,
  write, origin, return, release, and generation-release helpers mutate
  coordinator pool state without the exact session/work pointer and phase
  (`private_page_pool.go:1560-1691`, `:2414-2447`, `:2549-2579`,
  `:3746-3795`, `:3835-3847`, and `:3939-3965`). Commit also omits generic
  `activeScopes` from its rejection fence
  (`writer_transaction_core.go:691-704`). Green tests therefore did not prove
  the required rule that every coordinator-capable checkpoint/operation/bind/
  claim/write/commit/close mutation requires the canonical fence.
- Performance review independently found the same unfenced prepared mutation
  class (`private_page_pool.go:2115-2176` and `:2854-2937`) and two additional
  blockers. `writer_workspace.go:82-145` computes a separate byte limit but
  allocates nine independent backing arrays, while
  `writer_transaction_core.go:99-123` checks only pool-page count and never
  charges retained workspace bytes to the writer's single transaction heap
  budget. The claimed 4,096-slot permanent scaling proof actually uses the
  fixture's 32-slot pool (`writer_fixed_point_authority_test.go:53-68` and
  `:684-773`). The repair must use one checked monotonic workspace partition
  charged to the transaction resource budget, add a real 4,096-slot
  zero-allocation test, add exact-fence negative tests around every mutating
  helper class, and make commit reject all active scopes. Chunks 4-6 must use
  fenced replacements for prior-private return, bitmap/finalization, and
  retirement rather than trusting package visibility. Full Go, repeated
  focused/allocation tests, race, vet, `GOARCH=386`, formatting, and diff checks
  passed; staticcheck retained exactly the recorded 28 pre-existing findings.
- Workspace “allocated once” and “partitioned once” means one SDK-owned opaque
  logical workspace whose complete retained capacity is checked, charged once
  to the transaction resource budget, and reused without hot-path allocation.
  It does not require one physical Go heap object. A proposed
  `reflect.ArrayOf`/`reflect.StructOf` backing object was rejected during repair:
  capacity-dependent reflected types remain in runtime caches, and mixing large
  no-pointer page storage with pointer-bearing records in one object needlessly
  expands garbage-collector scanning. Go must use ordinary separately allocated
  typed backing slices created once at workspace construction, keep all backing
  opaque to callbacks/callers, retain one checked monotonic logical layout and
  total charged bytes, and preserve zero-allocation work. Layout validation must
  reject totals or element counts not representable by the current
  architecture's `uintptr`/`int` before any `make`, including on 32-bit builds;
  invalid budgets must return an error rather than panic. The charged contract
  is the exact logical retained capacity, not allocator-private size-class
  rounding. Workspace reset must clear retained state once, not duplicate a
  full page-arena clear in both workspace and core initialization.
- The second repaired Go staged-hybrid chunks 1-3 candidate is frozen for fresh
  exact-hash review:
  `private_page_pool.go`
  `b7ada0e9f70c8cb1de05c993784abd285be9c9423f4868e77cc552c52e195709`;
  `writer_fixed_point_authority.go`
  `6b35d5e565b9f8408bf729fa846ef657cc85b30879af8b67fbca6c12ffd12dc7`;
  `writer_workspace.go`
  `5850e8dbf419b5f11c73bd0964638ec25aeacab6d83351d45837fcf80bc3f866`;
  `writer_transaction_core.go`
  `880f0ff810ee996bca3e4b1eda0e0740981230c42f8e6e47e210d986b8417719`;
  `writer_fixed_point_authority_test.go`
  `982a18d6aad7bd912ad27d3948d662e07a97db31422a0ab275f57777d3fc3432`;
  `writer_fixed_point_core_test.go`
  `e3a672fa115e57163758ba058e42353305cd3e101e23a73aed73d2360364613f`;
  and `writer_transaction_core_test.go`
  `0a57db67c804da76b8cd19875e1ffc0107255541ff41e4246da976ce09587ed3`.
  The repair requires the exact Active canonical fence, plus scope/slot identity
  where applicable, across every coordinator-capable begin/commit/rollback/
  close/bind/claim/write/origin/return/release helper; standalone raw paths
  reject coordinator pools. Commit rejects generic active scopes. Workspace
  uses ordinary typed private slices, checked `int`/`uintptr` logical layout,
  exact logical retained-capacity charge against the transaction heap budget,
  and one page reset. The permanent scaling test uses an actual 4,096-slot pool
  with 130 prepared works, exact touched-node visits, and zero hot-path
  allocations. Full Go, full internal race plus focused external-race proof,
  full `GOARCH=386`, vet, 20 repeated focused runs, formatting, and diff checks
  pass. Staticcheck now reports 27 baseline findings and none on changed lines;
  the former 28th `changeOrigin` U1000 warning disappeared because a permanent
  missing-fence test now exercises it. This is a candidate, not acceptance;
  both fresh exact-hash reviews must pass.
- Both fresh reviews reject the second Go chunks 1-3 candidate; its seven hashes
  remain evidence only. A valid canonical fence plus its correct scope can still
  mutate an arbitrary foreign slot because terminal operation and retirement
  helpers authorize the work/scope but do not prove that the supplied slot
  belongs to that scope/operation (`private_page_pool.go:3036-3061`,
  `:3481-3507`, and `:3510-3534`). Terminal operation/checkpoint helpers also
  accept caller-authored identifiers, generations, start epochs, or checkpoint
  state instead of consuming a sealed preflight object
  (`private_page_pool.go:1884-1903`, `:1972-1991`, and `:3967-3984`).
  `preflightCommit` does not reject every residual core/pool/coordinator work
  identity, generation, phase, fence, scope, or active-prepared marker
  (`writer_transaction_core.go:683-748`). The repair must bind each terminal
  slot/checkpoint/operation mutation to its sealed prepared journal and exact
  authorized scope, add valid-fence/foreign-slot and substituted-journal tests,
  and make commit require canonical zero/Ready state for every mirrored work
  marker.
- Workspace review found that construction, core initialization, and `Begin`
  still reset workspace arrays repeatedly and traverse/clear the 4-KiB page
  arena before `Begin` overwrites every slot
  (`writer_workspace.go:219`, `writer_transaction_core.go:116-176`, and
  `private_page_pool.go:495-521`). The repair must define one initialization
  owner and perform one necessary pass per transaction. The claimed 130-work
  proof allocates 130 prepared slots but executes 128 measured calls plus the
  `testing.AllocsPerRun` warm-up, only 129 preparations
  (`writer_fixed_point_authority_test.go:1056-1068`); the permanent test must
  execute the stated 130 works explicitly while separately measuring
  allocations. Static `reflect.TypeOf`/`Size`/`Align` use for checked layout is
  not the rejected capacity-dependent runtime-type design: no
  `ArrayOf`/`StructOf` or generated type remains, ordinary typed slices keep
  page storage GC-noscan, and the reviewer confirmed sizing/charging is
  otherwise sound. Removing this static reflection is optional only if a
  simpler safe, portable, non-`unsafe` sizing implementation exists. All
  mechanical gates passed and staticcheck reproduced the 27 recorded baseline
  findings; green gates do not override the authority and initialization
  defects.
- Rust chunks 4-6 reached a green intermediate matrix but are not frozen or
  complete. Bitmap canonical terminal output is wired and scaling repairs pass,
  but chunk 5 still constructs/checks its selective prior-private delete
  checkpoint after Active, and chunk 6's retirement planner still requires an
  already-live bound scope. Callback-free failure is insufficient: the accepted
  contract requires a mechanically infallible Active suffix. The aggregate
  prepared authority must therefore own the complete prior-return cross-scope
  delete journal and a prepared/shadow retirement plan (or equivalent complete
  terminal-page export) before predecessor consumption. Active may only replay
  those sealed journals under the exact work fence. The intermediate 414
  no-default and 506 all-feature debug/release matrices, strict Clippy,
  formatting, benchmark compile, repeated focused tests, and unchanged
  lockfile are progress evidence only.
- The third repaired Go staged-hybrid chunks 1-3 candidate is frozen for fresh
  exact-hash review:
  `private_page_pool.go`
  `1bd7891749bfc95344df3119b9f1793c9ae6188a464e3be54f2b819a63e0a06e`;
  `writer_fixed_point_authority.go`
  `041f449c3889e25e4684b0be7272239eb1b933c1f49c847ad32d3c384fe0f15a`;
  `writer_workspace.go`
  `cc7ec480c1f0d8c2cca04dc1d39ac4acac8dc0fafc50d088a02e7f4ad0846119`;
  `writer_transaction_core.go`
  `84ff89022ef45c895f49184477abef4e86af59810a3ec5936f9e5770bf5d3685`;
  `writer_fixed_point_authority_test.go`
  `03162f90fb8c96c6d7741ab3dd7fc5f21511688e48a77af0f0622546a5765781`;
  `writer_fixed_point_core_test.go`
  `e3a672fa115e57163758ba058e42353305cd3e101e23a73aed73d2360364613f`;
  and `writer_transaction_core_test.go`
  `0a57db67c804da76b8cd19875e1ffc0107255541ff41e4246da976ce09587ed3`.
  Terminal slot membership is sealed into each prepared work and proved in
  O(1) from exact scope identity/anchor plus the slot's applied scope identity;
  valid-fence foreign/negative/past-end slots reject without panic or mutation.
  SDK-produced phase-checked lifecycle journals replace caller-authored
  operation/checkpoint state. Commit tests every mirrored residual work marker.
  `Begin` is the sole transaction reset owner: constructor/core preserve
  workspace state, then `Begin` performs one non-page reset and one page
  initializer pass. Permanent scaling executes exactly 130 work items; its
  allocation measurement is separate from `AllocsPerRun` warm-up. Same-failure
  audit also fenced pointer-only release, checkpoint leaf cleanup, checkpoint
  unbind/claim/lowest/transfer, raw operation claims, generic scoped release,
  and scoped write. Full Go, full race, repeated focused tests, `GOARCH=386`,
  vet, formatting, and diff checks pass; staticcheck reproduces exactly 27
  baseline findings and none on changed lines. This is a candidate, not
  acceptance; two fresh exact-hash reviews must pass.
- Rust staged-hybrid chunks 4-6 are frozen as one aggregate candidate:
  `private_page_pool.rs`
  `6c8de99077b3aa2341bdb64b85bf6ac6f14a39fb016d3ec0d2aec4e090a8633b`;
  `private_page_pool/selective_finalization.rs`
  `6d72a7596beda1724ca1f78fbfde6debba7a227efa8b638e41244c3c852a1f1a`;
  `bitmap_cow.rs`
  `a08b635f479def37e656ef4c6987ebef5c00d926f32d1c63be7b1ff1ec79018c`;
  `bitmap_cow/selective_finalization.rs`
  `d0c0965c3c255bfb136a70946ae5d10118370f1645a78d79b303c551275fae85`;
  `writer_fixed_point.rs`
  `3adf0ee2ef6ca5e92b31d9f5fc1cfe07b26bdfad2159a1877c92339f1950416f`;
  `retirement_writer.rs`
  `0653b8ea7617911082e92675b468a913bc072f144abf1db1be696931c9a37f7d`;
  and `writer_transaction_core.rs`
  `411b396cd3a572ef035216480a5dc1c5691b3c7038d85b4e757c96cc77553878`.
  `Cargo.lock` remains
  `3c85efb104433b9cd3329cb8c87aad98fae1cc8b0ae88f8c89ef6a8dea0db1ff`.
  The aggregate prepare-before-consume authority now carries the canonical
  bitmap terminal result, complete cross-scope prior-private return
  journal/resynchronization, and caller-backed shadow retirement tree/blob
  export. Bitmap and retirement pages merge in O(n) into one sorted, owner-
  checked, inseparable terminal journal; Active only replays sealed journals.
  Failure returns unchanged caller scratch, prior records/source/slot maps are
  resynchronized under sealed-scope nonce authority, and legacy post-Active
  coordinated return/retirement bypass names are absent. Debug and release pass
  416 no-default and 508 all-feature tests; strict all-target Clippy passes both
  modes, formatting, all-feature benchmark compilation, diff and prohibited-
  bypass scans pass, and focused prior-return, failure-restoration, combined
  retirement replay, and 4,096-slot scaling repeat 20 times. This is a
  candidate, not acceptance; two fresh exact-hash reviews must pass and must
  verify that no preparation, callback, allocation, fallible discovery, or
  substitutable subset remains in Active.
- Both fresh reviews reject the third Go chunks 1-3 candidate; its hashes remain
  evidence only. Mutation-time `validateWorkFence` omits mirrored active
  registry invariants such as core session/work-active state, pool mutation and
  start epoch, registered scope identity/anchor, unaccepted-scope count, and
  abort-required state (`writer_fixed_point_authority.go:750-780`). A valid
  canonical fence can therefore authorize terminal mutation after one of those
  markers is changed. Lifecycle journals are address-bound but not content-
  sealed: authorization compares a mutable canonical journal against itself, so
  mutating its operation/checkpoint fields first establishes a new accepted
  baseline (`writer_fixed_point_authority.go:871-942`). Coordinated
  `releaseGenerationInScope` rejects the coordinator before checking its exact
  fence, making the claimed positive path unusable
  (`private_page_pool.go:4511-4525`). The repair must seal immutable journal
  content independently of caller-writable storage, validate every mirrored
  active registry invariant at each mutation, and add positive/negative
  generation-release coverage.
- Go workspace review also disproved the single-initialization claim. `Begin`
  clears non-page workspace arrays, but prepared fixed-point startup again
  clears all records and slot-map entries, scans all scratch, and rewrites all
  prepared slots (`writer_workspace.go:248-255`,
  `writer_fixed_point.go:408-409`, and
  `writer_fixed_point_authority.go:314-339`). The existing reset test stops
  after `Begin`. `Begin` must be the only full-capacity initialization owner;
  fixed-point startup may initialize only current/touched state. Extend the
  permanent visit-count test through prepared fixed-point startup. The real
  4,096-slot test now correctly executes 130 explicit preparations, and all
  mechanical gates remain green; these positives do not override the authority
  and repeated-traversal defects.
- The first exact-hash authority review rejects the Rust chunks 4-6 candidate.
  The alleged aggregate authority is still separable: terminal pages,
  retirement result, and prior returns are distinct independently applied
  objects (`writer_fixed_point.rs:697-875`), so prior returns can be omitted or
  substituted. Production has no bitmap-engine exporter binding terminal pages
  to an actual canonical bitmap result; raw crate-writable pages enter through
  `with_terminal_pages`, and a test-only helper synthesizes CRC-valid pages
  accepted as canonical (`writer_fixed_point.rs:945-971`, `:2091-2130`, and
  `:2233-2313`). After predecessor consumption, terminal and prior-return replay
  still perform Result-returning validation/borrows and record/index discovery
  (`private_page_pool.rs:3243-3421`,
  `writer_fixed_point.rs:1858-1972`, and
  `bitmap_cow/selective_finalization.rs:647-730`). Ledger index binding is
  checked only after live mutation, and production completion remains
  test-only. The repair must create one truly inseparable prepared object,
  producer-bind canonical bitmap output, pre-bind the exact ledger index, and
  move every validation/borrow/index construction before predecessor consume;
  Active must be an infallible replay with no omittable authority. A second
  independent Rust review is still required before the repair contract is
  considered exhaustive.
- The second independent exact-hash review confirms the Rust chunks 4-6
  rejection and completes the repair contract. Production core consumes only
  the bare base prepared work (`writer_transaction_core.rs:321-372`), while
  terminal pages, retirement result, record scratch/index, and prior-return
  authority remain separate types/calls (`writer_fixed_point.rs:540-935` and
  `:1762-1977`). `with_terminal_pages` accepts raw crate-internal pages and no
  production bitmap-engine exporter exists; the only constructor proving the
  advertised flow is test code that fabricates CRC-valid bitmap pages
  (`writer_fixed_point.rs:946-1020` and `:2110-2130`). Retirement export binds
  only the retirement side and cannot authenticate arbitrary bitmap input
  (`retirement_writer.rs:2524-2648`). Active terminal/prior-return paths return
  `Result`, borrow live pool state, rescan vacancy, and revalidate fingerprints/
  epochs after consume; canonical record/index construction and ledger index
  checks remain later fallible steps. Chunk-7 production completion may remain
  absent, but chunks 4-6 cannot be accepted until production can construct and
  consume one move-only aggregate prepared object whose producer-bound bitmap
  result, retirement result/pages, prior returns, exact scope/slot mapping,
  pre-bound ledger index/record scratch, cleanup/resync, commitments, and
  budgets are all inseparable before Active. Active replay must be type-level
  infallible: helpers used there cannot return `Result`, acquire new borrows,
  sort, scan, look up, discover, allocate, call external code, or validate
  caller-authored content. The aggregate itself needs zero-allocation/O(n)
  4,096-scale proof. Both reviewers matched all hashes and the lockfile before
  and after; all 416/508 debug/release matrices, Clippy, formatting, benchmark
  compile, focused repeats, and diff checks remained green.
- A rejected Rust repair draft tried to make Active infallible by cloning every
  live `PrivatePagePoolSlot` into caller scratch during preparation and copying
  the entire image back during replay (`private_page_pool.rs:1341-1402` at the
  audit checkpoint). Each slot embeds a 4-KiB page, so every work became
  `Theta(total pool capacity * 4 KiB)` in retained scratch and two full-capacity
  copies. This repeats the rejected full-arena work class and violates the
  touched/requested-scope rule. It also risked copying slot bytes before exact
  live Active registration was installed. The draft must be removed. The valid
  repair is a sparse sealed after-image journal containing only the exact
  vacancy prefix, terminal assigned slots, prior-return slots, every affected
  global/scope AVL path or rotation node, and predetermined scalar deltas.
  Preparation uses caller scratch sized by the explicit touched-slot/path budget
  with an O(n) duplicate/index map; Active installs exact live work registration
  first, retains/consumes the live guard, and copies only unique touched after-
  images plus scalar deltas. Permanent 4,096-scale counters must prove O(touched)
  visits and zero hot-path allocations. Typed producer bitmap export from the
  draft is independently valid and may remain; no full-pool simulation or copy
  may remain in production or tests claiming the aggregate path.
- The replacement Rust sparse overlay removes the whole-pool copy and Active
  replay registers before copying first-touch after-images, but a pre-freeze
  audit found additional mandatory repairs. Aggregate prepare/execute definitions
  initially had no production or test caller, so an end-to-end production call
  graph and permanent test are required. Preparation wrote replay hash/slot
  scratch, new source locations, and record scratch incrementally without
  restoring them on later failure; all preparation failures must return every
  caller-owned buffer and live state unchanged, using two-pass validate-then-
  write or bounded rollback proven at each failure point. The open-addressed
  dedup table used modulo linear probing and could degrade to O(n^2) for
  adversarial congruent slots. The selected execution-ready stage already holds
  exclusive live workspace authority, so use a generation-stamped direct
  slot-to-overlay-entry index allocated once under the checked transaction/pool
  budget. Speculative read-only plans never mutate it; selected preparation
  gets O(1) lookup/update and resets only touched indices through generation
  stamps/list, with exhaustion preflight outside the hot path. This preserves
  strict O(touched) total work without a per-plan pool-sized table or an
  O(touched log touched) tree. Prove adversarial 4,096-slot exact visit bounds
  and zero allocation. Active
  may not index caller-derived ledger/source arrays with `expect`; exact entries
  and bounds must be prebound so replay cannot panic. Legacy non-aggregate
  coordinator executors must be removed, test-only, or hard-reject production
  coordinator use. Aggregate 4,096-scale and failure-restoration tests must
  exercise the actual production constructor/executor, not isolated primitives.
- Rust safe-prebinding audit rejects replacing Active panics with conditional
  `get_mut`: the in-progress candidate skipped missing/provenance-mismatched
  destinations and still incremented the ledger length
  (`writer_fixed_point.rs:1101-1150` at the audit checkpoint), allowing partial
  publication to report success. The current flat `[Option<Record>]`,
  `[Option<Source>]`, and index-slice containers cannot encode a dynamic set of
  prevalidated destinations as an allocation-once reusable, non-self-
  referential safe-Rust journal. Generational handles or whole-slice guards
  still require Active lookup; `RefCell` reintroduces runtime failure/panic;
  per-operation `split_at_mut` references cannot live in reusable workspace.
- The selected long-term-best internal representation is a fixed-capacity stable
  workspace backing plus a hidden transaction `CoordinatorSession`. Ledger
  record slots expose cell-backed Copy state (`Vacant`, `Prepared`, or
  `Live(generation)`), cell-backed cleanup epoch/digest, immutable binding/index
  descriptors and partition ranges, and one `Cell<bool>` returned tombstone per
  sealed binding. Sealed indexes are delete-only: reads, provenance, source
  enumeration, registration, and cleanup ignore tombstoned bindings, so prior
  return is one prebound tombstone write rather than dynamic AVL deletion.
  Source and slot-map slabs use stable cell-backed slots. The new record is
  fully prepared in a stable inactive slot; Active writes all dependent cells
  through a move-only journal of prebound shared cell references, flips record
  state to Live last, and updates prebound ledger scalars. Journal vectors/
  buffers reserve checked capacity once in the session and are not stored
  self-referentially inside the backing.
- Final scope/cleanup commitment must also remain O(touched). The current exact
  fingerprint scans full scope capacity and borrows/panics during Active. Add
  composable count/revision/digest aggregates to the scope AVL/vacancy nodes and
  update them on the already-touched path/rotation nodes. Preparation derives
  the final commitment in O(1) from the root/anchor aggregate and journals a
  prebound cell replacement. Active consumes iterators of prebound cell writes
  only—no index, lookup, conditional skip, `Option`/`Result`, borrow, allocation,
  validation, scan, callback, panic, or `unsafe`. Permanent tests must cover
  multiple prior records/returns plus a new record, visibility-last ordering,
  byte-exact cancel/drop, tombstone exclusion from every read/enumeration/
  cleanup path, digest drift/rotations, and 4,096-scale visits proportional only
  to replay entries plus touched AVL paths with zero allocations.
- The fourth repaired Go staged-hybrid chunks 1-3 candidate is frozen across
  nine files:
  `private_page_pool.go`
  `3cc1936458953961f968e93225659357ee843389bbc04e30ed6ce6af73beda00`;
  `writer_fixed_point_authority.go`
  `9f3c355d3b66560bb0940b2d1dd2beafdef9175e38ca071227e844ed5b33bc40`;
  `writer_workspace.go`
  `cc7ec480c1f0d8c2cca04dc1d39ac4acac8dc0fafc50d088a02e7f4ad0846119`;
  `writer_transaction_core.go`
  `c1e0232de05afd04585f493479a8274a0d2aec3437b27786c62d0693d593acc9`;
  `writer_fixed_point.go`
  `2e4793c22cd72b8005e47652456a92e08e70ec6bf34c39b3b0a26792747c3051`;
  `writer_fixed_point_authority_test.go`
  `d12d1885177f37e90e0c036b0e9e0fa3a15602d846a5a5f9dd54739eda6bafcc`;
  `writer_fixed_point_core_test.go`
  `e3a672fa115e57163758ba058e42353305cd3e101e23a73aed73d2360364613f`;
  `writer_transaction_core_test.go`
  `0a57db67c804da76b8cd19875e1ffc0107255541ff41e4246da976ce09587ed3`;
  and `writer_fixed_point_test.go`
  `ee3aa1bae28c4d143db7b6124d342e8566f5b3f003c0e5dce0f96a696a5ac74e`.
  Mutation-time authorization now compares every mirrored core/pool/coordinator
  active registry invariant. Independently retained lifecycle commitments—not
  the mutable canonical journal—bind operation/checkpoint content before begin,
  throughout Active, and at consumed-journal close. Correct scoped generation
  release succeeds while missing/wrong fence or generation rejects. Prepared
  startup uses generations/current-slot initialization and leaves far-tail
  sentinels untouched after `Begin`; the 4,096-page/130-record test proves no
  second full-capacity traversal. Same-failure review added mutation-epoch
  lower-bound validation, complete sealed-state comparison, and close/release
  substitution tests. Full Go, full race, `GOARCH=386`, vet, focused authority/
  scaling repeated 20 times, formatting, and diff checks pass; staticcheck
  reproduces exactly 27 baseline findings. This is a candidate, not acceptance;
  two fresh exact-hash reviews must independently validate the full mutation
  graph and bounded initialization.
- Both exact-hash reviews reject the fourth Go chunks 1-3 candidate; its hashes
  remain evidence only. Mutation-time work authorization still omits the live
  pool lifecycle registry: transaction/pool epoch and generation, operation and
  checkpoint sequences, `activeOperationID`, `operationStartEpoch`, and
  `activeCheckpointID` are not compared by `validateWorkFence`
  (`writer_fixed_point_authority.go:803-883`). Terminal operation/checkpoint
  authorizers compare journal content and commitment but not those live fields,
  then slot claim/write helpers mutate directly
  (`private_page_pool.go:2275-2287`, `:3089-3146`, and `:3298-3360`). Clearing
  or substituting a live ID after valid begin therefore still permits mutation.
  The repair must retain the predicted lifecycle registry in the independent
  commitment and compare it against the actual live pool in O(1) for every
  Ready/OperationActive/CheckpointActive/Consumed mutation; add one-field tests
  for pending transaction, pool epoch/generation, sequences, active IDs, and
  start epoch, proving zero slot/epoch mutation.
- The claimed positive scoped generation-release proof is vacuous: its
  canonical case uses an empty scope and explicitly expects no epoch change
  (`writer_fixed_point_authority_test.go:1211-1242`). Add a real matching page
  and prove the canonical fence releases it and advances exact lifecycle state,
  while wrong fence/generation/scope/page reject without mutation. Both reviews
  otherwise accept bounded initialization/workspace behavior: `Begin` is the
  sole full initializer, prepared startup is touched-only, and the real
  4,096-slot/130-work zero-allocation proof is valid. All mechanical gates and
  the 27-finding staticcheck baseline remain green.
- The fifth repaired Go staged-hybrid chunks 1-3 candidate is frozen:
  `private_page_pool.go`
  `957b8533ab9e63eb58f1b2496e5edb4a57a96c0142f7c73fdc2a9c89d6e6a871`;
  `writer_fixed_point_authority.go`
  `b1a4605ece3b9fb8010f35996dfd49128c5f5a13454cf2f3ead0dd5e0028106a`;
  `writer_workspace.go`
  `cc7ec480c1f0d8c2cca04dc1d39ac4acac8dc0fafc50d088a02e7f4ad0846119`;
  `writer_transaction_core.go`
  `c1e0232de05afd04585f493479a8274a0d2aec3437b27786c62d0693d593acc9`;
  `writer_fixed_point.go`
  `2e4793c22cd72b8005e47652456a92e08e70ec6bf34c39b3b0a26792747c3051`;
  `writer_fixed_point_authority_test.go`
  `6a84f614ddf0fa362117a519272999955c1e918682134fffcad48f844227c89d`;
  `private_page_pool_test.go`
  `3d8699cebc5c7a84d0489de0c5b41b89d6df8fc84763011a62dfdb7becf75d60`;
  `writer_transaction_core_test.go`
  `0a57db67c804da76b8cd19875e1ffc0107255541ff41e4246da976ce09587ed3`;
  and `writer_fixed_point_test.go`
  `ee3aa1bae28c4d143db7b6124d342e8566f5b3f003c0e5dce0f96a696a5ac74e`.
  Independent commitments now retain and compare live pending transaction, pool
  epoch/generation, operation/checkpoint sequences, active operation/start,
  active checkpoint, and phase for seven terminal paths. The permanent matrix
  covers 56 phase/field corruptions without slot or epoch mutation. Real scoped
  generation-release coverage binds and claims a bitmap page, proves canonical
  release advances pool and slot epochs exactly once, and proves wrong fence/
  generation/scope/page reject unchanged. Same-failure review covers terminal
  claim/write/release, operation/checkpoint lifecycle, consumed close, and
  rollback's distinct generation state. Full Go, race, `GOARCH=386`, vet,
  focused lifecycle/release/journal repeated 20 times, formatting, and diff
  checks pass; staticcheck remains exactly 27 baseline findings. This is a
  candidate, not acceptance; two fresh exact-hash reviews must pass.
- Go staged-hybrid chunks 1-3 are accepted at the fifth candidate's nine exact
  hashes after two independent reviews. Authority review traced the complete
  coordinated mutation graph and confirmed that independently retained
  lifecycle commitment covers pending transaction, pool epoch/generation,
  operation/checkpoint sequences, active IDs/start, phase, work/generation, and
  nonce; all terminal authorizers compare actual live state before mutation.
  The 56-case matrix is genuinely seven paths by eight separately corrupted
  live fields, and the real release test binds/claims page 7 then proves exact
  pool/slot epoch advancement and Available state. Performance review confirmed
  one checked workspace construction, one full non-page reset and page
  initialization per `Begin`, O(1)/touched-only startup and seals, a real
  4,096-page/130-preparation zero-allocation proof, and no full workspace/pool/
  record/map scan or hash per work. Both reviews independently passed full Go,
  full race, `GOARCH=386`, vet, focused repetitions, benchmark compilation,
  formatting, diff checks, and reproduced exactly 27 baseline staticcheck
  findings with none in the reviewed authority/workspace/core paths. All hashes
  matched before and after; no review edits occurred. This acceptance is
  strictly chunks 1-3. Raw standalone `returnUnowned` remains state-blocked
  rather than session-fenced, but cannot mutate in the accepted lifecycle
  (Ready has no bound pages; Active has an active scope); chunks 4-6 must use
  exact-fenced replacements and may not make this raw path production-reachable.
- Early Go chunks 4-6 audit found mandatory corrections before aggregate
  prepare/execute may turn green. Bitmap and retirement producer outputs cannot
  be copyable package structs authenticated only by recomputable content hashes;
  the actual engine must issue an address/generation/nonce-bound, one-use
  producer authority consumed by aggregate preparation. Tests must try a
  recomputed valid-looking substitute, not only mutate content under an old
  seal. Detached bitmap/retirement staging cleanup/discard authority must be
  owned by guards so every post-finalize export error restores output scratch
  and destroys or returns staging state deterministically; success alone cannot
  own cleanup. Retirement needs a real engine producer/exporter, not a sealed
  raw `RetirementTreeEditResult`.
- Go aggregate workspace is part of the same transaction heap budget as the
  accepted base workspace. It must consume only the core's remaining charged
  budget, or be one partition of that workspace; comparing both independently
  with full `maxHeapBytes` can retain almost twice the configured limit.
  Aggregate layout must reuse checked aligned monotonic `int`/`uintptr` sizing
  before every `make`, including 32-bit overflow rejection. Partitions must
  include exact cleanup nodes/path/targets and seals required by the canonical
  live bitmap record, not only bindings/index nodes. Prior return cannot reuse
  the old helper that merely clears slot-map ownership and refreshes one binding
  epoch; it must prebind and resynchronize returned tombstones, immutable index
  visibility, binding/cleanup proof, record commitment, source registrations,
  and slot-map state in the inseparable aggregate. Permanent tests cover every
  post-finalize failure, recomputed producer substitution, combined base+
  aggregate budget overflow, `GOARCH=386` layout overflow, and cleanup after
  later commit/abort.
- Go chunks 4-6 prepare/execute scaffold audit rejects its first sparse
  implementation before acceptance testing. `shadow := c.pool` copied only the
  slice header, so scope/bind/release/checkpoint preparation mutated the live
  slot backing before Active (`writer_fixed_point_aggregate.go:742-756` at the
  audit checkpoint). Path nodes were discovered after those mutations and
  recorded as “before” images from the same shared backing
  (`:496-500`, `:808-854`), so success/failure restoration replayed after-images
  and could not restore byte-exact live state. The replacement must be a true
  sparse detached overlay over immutable live reads: first touch captures the
  live before-image once, all simulated writes/reads go through overlay
  after-images, and neither live slot bytes nor live scalars change until exact
  registration and Active replay.
- Scaffold `clearPrepared` cleared every full-capacity partition on each
  prepare/failure (`:446-458`); reset only used prefixes and touched generation
  entries. Aggregate commitments covered only scalar lengths/root/page count,
  not caller-retained replay indices/after-images, prior destinations, output
  bindings/index, pool-after state, retirement result, or backing addresses.
  One canonical private aggregate slot must independently bind address,
  generation, exact bounds/destinations, and content commitments for every
  replay partition; caller mutation between prepare/execute rejects preconsume.
  Prior return must tombstone/resynchronize the old record's index, binding,
  cleanup proof, commitment, source and slot-map state—not merely clear
  `slotRecords` and refresh one epoch. The new record must own cleanup
  nodes/path/targets and their seal before a cleanup predecessor is constructed.
- Producer export must emit strict canonical page-number order from the actual
  index, or sort in bounded producer scratch and seal that order; arena binding
  order is not sufficient. Permanent multi-page tests prove order. Remove the
  dummy `pageIndexInsertExistingPrechecked`/`new(int)` loop that inserted and
  cleared fake index state; no preparation proof may allocate or panic. Scope
  replay must include exact `pendingTxn` and all accepted scope fields. Expand
  tests beyond one empty/single-page case to dual-AVL rotations, multiple prior
  records, real retirement, new-record cleanup, every post-finalize/prepare
  failure, caller workspace substitution with recomputed scalar seals,
  byte-identical live state after prepare-before-execute, and 4,096-scale
  allocation/visit bounds.
- Follow-up Go overlay audit accepts first-touch detached simulation itself but
  found more mandatory integration repairs. The new live record stored output
  binding/index slices that still aliased reusable aggregate workspace; the
  next prepare's clear/overwrite would corrupt prior active record/source/
  cleanup authority (`writer_fixed_point_aggregate.go:1267-1285` and
  `:1366-1374` at the checkpoint). Record partitions must transfer to stable
  record-owned storage and cannot return to preparation workspace until that
  record is retired. Nonempty output also lacked cleanup nodes/path/targets and
  used full-capacity rather than used-prefix binding slices, so later cleanup
  could reject or index empty targets. Construct every output partition as its
  exact used prefix with matching capacity/seal.
- Go aggregate preparation must preflight every exact epoch/generation/
  checkpoint/pending-count delta; unchecked increment/wrap is forbidden.
  Rejecting a competing second prepare must preserve the first aggregate and
  its token byte-exact—failure cleanup cannot call a full clear on occupied
  canonical state. After `consumeFixedPointWork` installs Active registration
  and scope, no returned-scope validation or conditional `AbortRequired` path
  may remain; every such comparison occurs before consume and Active executes
  an infallible private replay. These cases require permanent exhaustion,
  competing-aggregate, and postconsume-static-call-graph tests in addition to
  the previously recorded commitment/resync/producer/budget/scaling matrix.

### 2026-07-24 - Rust coordinator restart baseline and detached overlay

- Committed the reconciled exact-v4 cutover as checkpoint `93ea0ff`; generated
  build output remains untracked and excluded. This is an in-progress recovery
  point, not a claim that the Phase-1 SDK is complete.
- Repaired the Rust no-default coordinator build by replacing its heap `Vec`
  journals with caller-provided fixed journals of prebound cell writes. The
  journal replays only its used prefix and Active remains allocation-free.
- Repaired rollback/aggregate consistency: stale checkpoint index and scope
  scratch now clears completely; scope aggregates no longer include the
  deliberately changing stale-authority epoch; authority and transfer refresh
  their owning aggregate paths.
- Replaced the per-slot sparse-replay generation/index fields with a
  caller-owned generation-stamped direct sidecar. The sidecar is checked against
  pool capacity, changes only touched entries, and is cleared on cancellation,
  drop, and Active replay. Preparation no longer changes a live pool slot.
- Added permanent 4,096-slot coverage for undersized-sidecar atomic rejection,
  byte-exact live-pool restoration after cancellation, sidecar cleanup after
  both cancellation and replay, and zero-allocation/touched-path bounds. Removed
  the remaining debug assertion from the Active sparse replay and made the
  static Active-suffix test reject debug assertions too.
- Verified `cargo check --workspace --no-default-features`, formatting,
  `git diff --check`, and both full Rust suites: 419 no-default tests and 511
  all-feature tests pass. The coordinator still has only test-local aggregate
  construction; production workspace/lifecycle wiring remains the next required
  implementation chunk.

### 2026-07-24 - Rust sealed-record handoff plan

- The next internal boundary is not durable publication. A completed aggregate
  must first verify that its exact workspace record, sealed scope, root, page
  tail, and retirement result still agree; it then accepts that sealed scope
  into the coordinator record and releases the work registration while retaining
  the page bytes in the transaction pool. The returned successor is the sole
  authority for a later work unit.
- This is required to make the typed bitmap/retirement result part of the real
  writer-core lifecycle instead of a test-only terminal object. The later
  terminal writer still must stream the retained records to the private output,
  clean their scopes, finish the input, and publish the meta pair. Until that
  later step exists, `preflight_commit` must continue to reject the draft due to
  retained cleanup work. No commit-success behavior is authorized by this
  handoff.
- Completion validates every fallible condition before accepting the sealed
  scope. A mismatch after Active is a whole-draft `AbortRequired` condition;
  it never issues a successor or permits partial publication. The permanent
  test will cover the accepted record, target-meta handoff, successor issuance,
  retained commit fence, zero allocation, and explicit cancellation plus abort.

### 2026-07-24 - Rust sealed-record handoff implementation

- Implemented the private Sealed-to-record handoff. It verifies the workspace
  record identity, sealed scope, root, pending tail, retirement result, and
  every retained bitmap-page provenance before accepting the scope and releasing
  the coordinator work registration.
- The retained record uses the same constant-work scope/vacancy aggregate that
  sparse replay sealed. It deliberately does not make ordinary lifecycle paths
  scan the whole scope: caller-requested full validation remains separate.
- The core updates only its private target metadata and returns the next
  predecessor after acceptance. The sealed page bytes and record remain in the
  transaction pool. `preflight_commit` is still blocked by the active retained
  scope, so this change cannot publish a partial or non-durable update.
- Extended the production-shaped cross-owner test to prove the accepted record,
  metadata handoff, successor, zero allocations, retained commit fence, and
  explicit workspace cancellation followed by whole-draft abort.
- Validation: 419 Rust no-default tests, 511 Rust all-feature tests, Rust
  formatting, Clippy with warnings denied, Rust benchmark compilation, all Go
  tests, Go vet, `git diff --check`, and the project SOW audit pass.

### 2026-07-24 - Rust canonical-record all-page correction

- Inspection of the first production-shaped combined bitmap/retirement handoff
  found that its sealed scope correctly retains every terminal page, but the
  canonical workspace record indexed only `Bitmap` owner pages. A later output
  drain could therefore omit retirement bytes, while scope cleanup would reject
  the incomplete record because its binding count was smaller than the sealed
  scope's bound count. The test fixture at
  `v4/rust/iprange-livedb/src/retirement_writer.rs` already creates this exact
  one-bitmap/two-retirement scope.
- This is an implementation gap in chunk 7, not a new format or public-API
  decision. The approved canonical-record rule already requires output, cleanup
  authority, and the complete sealed result to remain inseparable.
- The derived repair is to retain/index every terminal page in page-number
  order, regardless of owner, in the existing transaction-budgeted record
  partition. Record handoff must prove exact full-scope coverage; later private
  page lookup, output streaming, prior-page return, and cleanup use that same
  authoritative set. Bytes remain in the bounded private-page pool until the
  later output drain; no heap copy or external scratch is introduced.
- The already-added internal `finish_fixed_point_input` boundary remains valid:
  it consumes only the final successor after all aggregates are accepted, while
  retained records continue to block commit until the future output-drain and
  scope-cleanup transition succeeds. It does not authorize publication.
- Validation for this correction must prove the mixed-owner scope is fully
  retained, `FinishInput` leaves commit blocked by its live scope, cancellation
  returns all workspace authority, and no allocation occurs in the Active and
  handoff paths. No normative specification update is required because the
  public storage and lifecycle contract is unchanged.

### 2026-07-24 - Rust canonical-record validation

- The permanent cross-owner lifecycle test now reserves a three-page scope
  containing one bitmap and two retirement pages. It proves handoff accepts the
  scope only when the canonical record covers every bound page, retains the
  target metadata/successor, consumes `FinishInput` with zero allocation, keeps
  commit blocked by the live scope, and permits explicit workspace cancellation
  followed by whole-draft abort.
- Validation passed: `cargo test --manifest-path v4/rust/Cargo.toml --workspace
  --no-default-features` (419 tests); `cargo test --manifest-path
  v4/rust/Cargo.toml --workspace --all-features` (511 tests); `cargo fmt
  --manifest-path v4/rust/Cargo.toml --all -- --check`; `cargo clippy
  --manifest-path v4/rust/Cargo.toml --workspace --all-features --all-targets
  -- -D warnings`; `cargo check --manifest-path v4/rust/Cargo.toml --workspace
  --all-features --benches`; `go -C v4/go test ./...`; `go -C v4/go vet ./...`;
  `git diff --check`; and `./.agents/sow/audit.sh`.

### 2026-07-24 - Rust private-output drain and sealed-cleanup plan

- Inspection after the all-page handoff found a production-only lifecycle
  defect: after `complete_sealed_work` releases the active coordinator work,
  ordinary scope/checkpoint validation intentionally rejects the retained
  sealed scope. Test builds relax that guard, so the existing generic record
  cleanup can appear to work in tests but cannot be the production drain.
  Also, accepting a sealed scope increments `coordinator_cleanup_pending`, but
  no successful cleanup currently decrements it. A later commit can therefore
  never pass its coordinator fence.
- The required repair is internal and follows the already-approved state
  machine. A move-only, exact-scope cleanup guard will temporarily authorize
  only one accepted sealed scope after input completion. It permits the
  existing preflighted selective cleanup machinery without reopening ordinary
  scoped mutation. Successful closure consumes exactly one pending-cleanup
  count; any preflight failure drops the guard without changing the pool.
- The workspace will add a bounded private-output drain. For each canonical
  record it checks every retained page's sealed provenance, sends the original
  page bytes to a caller callback, and only then cleans that record's scope.
  No page copy, sort, heap allocation, or external temporary file is added.
  The callback failure path leaves unpublished bytes irrelevant, marks the
  entire draft abort-required, and preserves explicit workspace cancellation
  followed by whole-draft abort.
- This is not a public writer API and does not claim durable file publication:
  the later OS writer still owns file synchronization and target-meta
  publication. It establishes the necessary in-memory authority boundary so
  that future durable output cannot omit pages or commit after a failed sink.
- Permanent coverage must prove all bitmap and retirement pages reach the
  callback, successful drain clears every retained scope and unblocks the
  internal commit fence, a sink failure blocks commit and permits cancellation
  plus abort, and both paths allocate zero heap bytes.

### 2026-07-24 - Rust private-output drain and sealed-cleanup implementation

- Implemented a move-only exact-scope cleanup guard in the private-page pool.
  After coordinator work finishes, it opens only the accepted sealed scope
  being drained, serializes that cleanup against every other retained record,
  and releases exactly one `coordinator_cleanup_pending` count only after the
  scope is closed. A failed cleanup drops the temporary authority and leaves
  the draft fenced for cancellation/abort.
- Split the sealed coordinator record's caller scratch lifetime from its live
  pool lifetime. The record can now return its original fixed scratch after
  successful cleanup without extending the writer borrow or allocating.
- Added the internal fixed-point private-output drain. It validates each
  retained page's sealed provenance, invokes a caller callback with the
  original page bytes, and then performs the exact selective scope cleanup.
  It returns caller scratch to the workspace only after cleanup. A callback,
  record, or workspace error marks the transaction abort-required; no retry or
  commit can reuse a partially emitted draft.
- The production-shaped mixed-owner test covers one bitmap plus two retirement
  pages. It proves byte-exact, zero-allocation success output, scope release,
  internal fence/preflight success, and explicit cancellation plus abort. Its
  second run fails the sink after the first page, proves the stable `SinkFailed`
  classification and abort-required state, retains the live record for
  cancellation, and rejects preflight commit.
- No normative specification update is required: no durable byte layout,
  public API, or publication ordering changed. No runtime project skill or
  end-user documentation is affected because this remains an unpublished
  internal writer lifecycle boundary.
- Validation passed: targeted mixed-owner drain coverage; `cargo test
  --manifest-path v4/rust/Cargo.toml --workspace --no-default-features` (419
  tests); `cargo test --manifest-path v4/rust/Cargo.toml --workspace
  --all-features` (511 tests); Rust formatting; Clippy with warnings denied;
  Rust benchmark compilation; `go -C v4/go test ./...`; `go -C v4/go vet
  ./...`; `git diff --check`; and `./.agents/sow/audit.sh`.

### 2026-07-24 - Rust commit-preparation ordering plan

- The output-drain milestone deliberately stops before durable publication, but
  inspection found one ordering gap to close before an OS sink can use it:
  the existing `preflight_commit` succeeds only after retained records are
  drained. A future file sink could therefore write a private page before the
  transaction core has frozen its pre-publication state. That would violate
  section 14.2 phase 1 even if later meta publication were correct.
- The next internal change is a move-only commit-preparation capability. It
  preflights the pending target, quiescent coordinator, workspace identity, and
  no-active-operation/checkpoint state before any page callback is permitted.
  The callback drain requires that capability; after successful scope cleanup,
  one final preflight consumes it, releases the workspace resource, and returns
  a distinct meta-publication authorization containing the exact prepared
  target.
- This introduces no file write, metadata write, synchronization, durable
  outcome, public API, or validation-on-open behavior. It makes the later OS
  writer unable to use the current coordinator sink before the core's required
  phase-1 checks. A callback failure remains whole-draft abort-required.
- Permanent tests will prove the phase boundary blocks normal page/coordinator
  access before callback invocation, a prepared success path returns the exact
  target authorization with zero allocation, stale/substituted authority fails
  closed, and every failed completion remains non-publishable and abortable.

### 2026-07-24 - Rust commit-preparation implementation

- Implemented the exact pre-output phase-1 fence in
  `writer_transaction_core.rs`. It checks the pending target, coordinator
  quiescence, registered-work mirrors, workspace identity, active
  operation/checkpoint state, transaction sequence, target metadata, and the
  coordinator commit fence before a callback is permitted.
- Preparation closes the ordinary draft-pool and coordinator accessors because
  those internals can mutate through shared references. It also blocks every
  core work/aggregate/input entry point until the transaction is aborted. The
  drain requires the preparation capability; successful drain changes the
  phase, and finalization consumes the capability only after releasing the
  exact workspace reservation and rerunning commit preflight.
- Finalization yields a private target-metadata authorization, not a published
  metadata page. There is deliberately no file write, file sync, metadata-page
  write, lease update, cleanup, public API, or durable outcome claim in this
  change. A sink failure remains abort-required and cannot yield authorization.
- Extended both production-shaped mixed-owner paths. They prove the access
  fence, byte-exact zero-allocation drain, exact target authorization, and that
  a failed drain remains non-publishable and abortable.
- Validation passed: targeted mixed-owner lifecycle test; Rust no-default
  matrix (419 tests); Rust all-feature matrix (511 tests); Rust formatting; and
  Clippy with warnings denied. The pre-commit repository validation below will
  also run benchmark compilation, Go tests/vet, diff check, and the SOW audit.

### 2026-07-24 - Linux durable-commit integration plan

- The next physical-commit work has two proven prerequisites. First, the Linux
  operation barrier currently records reader protection but no allocator or
  retirement finalizer consumes it (`os/linux/live_writer.rs:70-80,437-439`),
  while the current fixed-point integration completes before any live barrier
  exists (`retirement_writer.rs:6756-6776`). Publishing from that path would
  violate section 14.2 phase 1, which requires the final fixed point under the
  operation lock after the stable reader scan.
- Second, current writer cleanup proves one exact pre-publication bootstrap and
  requires the owned slot transaction to match it
  (`os/linux.rs:1691-1704,2047-2113`). Once a target meta is durable, that
  proof is intentionally no longer true. A simple target-meta write would make
  `Close` reject the new generation or, worse, tempt a future implementation to
  truncate it as an old unpublished tail. No physical target-meta writer is
  permitted until source/attempt/target cleanup authority is explicit.
- The implementation order is therefore: (1) add a bounded Linux
  source/attempt/target state model and exact Close proof for pre-meta,
  outcome-unknown, and committed states; (2) bind the final allocator/retirement
  work to the held operation barrier and its stable reader facts; (3) compose
  the already-fenced private-page drain with phase-2 sync, target-meta
  write/sync, and writer-lease update; and (4) complete the transaction core
  only after the durable outcome and retain close-only authority for every
  interrupted post-publication path.
- Each stage will use in-process fault injection for write, sync, meta-byte,
  lease-transition, truncate, and unlock failures. Required proof is factual:
  old meta plus exact tail cleanup before phase 3 is `NotCommitted`; every
  phase-3/4 interruption is close-only `OutcomeUnknown`; phase-4 success is
  `Committed` even if phase-5 cleanup fails. No public SDK surface is added
  until this private path is complete and mirrored where required.

### 2026-07-24 - Linux source/attempt/target cleanup state

- Implemented only the first prerequisite in
  `v4/rust/iprange-livedb/src/os/linux.rs`: an in-memory bounded source,
  target, and phase record. It is created before any target-meta byte, records
  the old tail at the phase-3 boundary, confirms the exact target only after
  the caller's phase-4 sync, and transfers the writer lease only through the
  exact source-to-target sidecar transition.
- `Close` now truncates only when the exact source remains selected. When the
  attempted target or a bootstrap-valid later generation is selected, it
  preserves those bytes and clears only the exact retained writer authority.
  A target selected before the phase-3 record, a source observed after phase-4
  confirmation, a malformed/different transition, or any unproved selection
  remains close-only and fails without truncation or lease removal.
- Eleven Linux unit tests cover those cases, including a phase-5 state-2
  interruption, a mismatched armed update, valid later-generation supersession,
  and refusal to begin a new commit while old transition provenance is armed.
  This is not a physical commit implementation: no page write, metadata write,
  synchronization, result classification, or public API is connected yet.

### 2026-07-24 - Trusted reclamation-to-allocator handoff

- Implemented the internal authority repair in
  `retirement_reader.rs` and `bitmap_cow.rs`. Production bitmap late binding
  accepts `RetirementReclaimedPages`, which only the verified second pass can
  construct; a raw numeric selection ID and arbitrary page slice are available
  only to `#[cfg(test)]` fixtures.
- The first verification pass and the second pass now enforce global strict
  order across all selected retirement batches. The bounded `second_pass_into`
  API rejects inadequate caller scratch before it writes and returns exactly the
  selected page prefix.
- Focused tests prove the scratch bound, exact output, cross-batch duplicate
  rejection, and allocator handoff. Full Rust no-default and all-feature
  matrices pass with 421 and 522 tests respectively. This adds no file write,
  metadata publication, public SDK API, or default validation.
- The type boundary alone does not prove the barrier remains held after bitmap
  binding, because no physical live-commit path invokes this code yet. That
  remains the next integration requirement rather than a false completion claim.

### 2026-07-24 - Linux physical-publication implementation plan

- The next implementation is a private, move-only Linux publication attempt
  that owns the already-acquired operation barrier. It will begin the existing
  source-to-target attempt record before any output page write, expose only a
  bounded page sink for pages `2..target.page_count`, synchronize non-meta
  output, write the parity-selected target meta page, synchronize it, confirm
  the target, and update the retained writer lease while the same barrier stays
  held.
- Its error authority will preserve the exact phase boundary: failures before
  target-meta writing are `NotCommitted`; any failure from the first target-meta
  byte through failed phase-4 confirmation is `OutcomeUnknown`; a failed
  phase-5 lease update is `Committed` but close-only. No destructor will clear
  a lease or discard a tail. Every post-attempt failure will mark the writer
  close-only before returning its retained barrier; the caller drops that
  barrier before `Close` performs the existing retained-descriptor cleanup.
- This layer will remain private and callback-based. It does not invent a
  public writer API or duplicate fixed-point logic. The following core-wiring
  slice will pass the core's bounded private-page drain through this sink and
  accept transaction success only after the publisher returns a durable target.
- Permanent Linux tests will inject private-page write, phase-2 sync,
  target-meta write, phase-4 sync/confirmation, and phase-5 update failures;
  they will assert old-meta cleanup, close-only outcome handling, and durable
  target retention exactly at the phase boundaries.

### 2026-07-24 - Linux physical-publication implementation

- Implemented the private Linux publication state machine in
  `v4/rust/iprange-livedb/src/os/linux/live_writer.rs`. While one already-held
  operation barrier remains live, it records the source-to-target attempt,
  accepts only complete non-meta pages inside `2..target.page_count`, syncs the
  main file, writes the parity-selected complete meta page, syncs again,
  confirms the target, and updates the exact writer lease.
- `RetainedRegular::set_len` now performs retained-descriptor growth, so phase
  2 synchronizes both output bytes and the target file length. The
  page sink intentionally permits an authorized reusable page below the old
  file length: v4 allocation may reuse only a committed-free or
  reader-safe-reclaimed page, and the still-pending core integration owns that
  proof.
- A failure before target-meta writing is returned as `NotCommitted`; phase-3
  write, phase-4 sync, and target-confirmation failures are `OutcomeUnknown`;
  phase-5 lease-update failures are `Committed`. Every recorded attempt is
  automatically close-only. A target that reached phase 4 can also be made
  close-only if later core completion fails, and an unlock failure after phase
  5 has the same protection.
- This is deliberately not a public SDK API. At that checkpoint it had no
  core-to-publisher caller; the private bridge below now owns that connection.
  It still does not claim that the core's allocator/retirement fixed point runs
  under this barrier.

### 2026-07-24 - durable core-completion boundary plan

- A phase-4-confirmed target must change the transaction core's selected
  generation only after the physical publisher has proved durability. The core
  must then scrub its private slots and invalidate the finished transaction
  handle before another transaction can begin.
- If that in-memory scrub or retained cleanup cannot complete after the target
  is durable, the outcome remains `Committed`: aborting would describe the
  wrong on-disk reality. The core therefore enters a distinct committed
  cleanup-required state that blocks normal work and can only finish its own
  cleanup; the Linux writer is made close-only by the physical layer.
- This is a direct consequence of section 14.2's committed-after-phase-4 rule,
  not a new public API. Targeted tests will prove exact authorization matching,
  selected-generation advancement only after durable confirmation, handle
  invalidation, retryable committed cleanup, and refusal to abort a committed
  transaction.

### 2026-07-24 - durable core-to-Linux publication integration

- Implemented the private bridge from `PrivateWriterTransactionCore` to the
  Linux publisher. It acquires one operation barrier, verifies that the core's
  exact selected metadata equals the file's selected metadata, prepares the
  fixed-point output, streams only its authorized terminal pages to the Linux
  page sink, then accepts core success only after phase-4 confirmation.
- The core now has distinct `OutcomeUnknown` and
  `CommittedCleanupRequired` states. A phase-2 failure remains explicitly
  abortable. A phase-3/4 failure poisons the core before the barrier is
  released, so no normal operation or abort can reinterpret an ambiguous target
  as absent. A phase-5 failure is still committed: the core advances to the
  target before its fallible in-memory cleanup and never reopens abort.
- The bridge rejects a full selected-generation mismatch before any page output.
  It also fails closed if a broken internal path loses its exact core
  publication authority after an ambiguous physical attempt.
- The composed path remains private; no user-facing writer or SDK surface was
  added. This does **not** satisfy the remaining section 14.2 requirement that
  allocator selection, verified retirement reclamation, and final fixed-point
  work themselves occur while the same Linux operation barrier is held. The
  bridge only holds the barrier from existing private-output preparation through
  physical publication. That allocator/finalizer integration is the next
  critical slice.

### 2026-07-24 - lock-scoped finalization-context plan

- Evidence: the current `retirement_reclaim_fence` returns a borrow-bound
  reader-safety value, but a later allocator can reduce a verified reclamation
  result to page numbers. There is no live entry point that supplies both the
  exact retained selected-page source and that reader-safety value under one
  unbroken operation barrier.
- The next narrow repair adds a private callback context to an already-held
  Linux operation barrier. It supplies only the selected bootstrap, its pinned
  retained page source, and a move-only retirement reclaim fence. The context
  cannot escape the callback, so the selection, second pass, allocator binding,
  and finalizer work that consume it must complete before the barrier can be
  released or consumed for physical publication.
- The loose fence accessor will be removed. Tests will use the context to open
  a real retirement reader over the retained selected file and prove that the
  operation lock remains held through and after the callback. This establishes
  the safe live entry point; the following slice must make the actual
  allocator/retirement fixed point consume it.

### 2026-07-24 - lock-scoped finalization-context implementation

- Implemented the private Linux callback context. It creates the exact pinned
  source from the retained selected file and its stable reader-table reclaim
  fence under the already-held operation barrier. Its higher-ranked callback
  cannot return either borrow into later unlocked work.
- A reclamation result now carries an opaque operation-barrier guard from
  selection through bitmap late binding and the bound reservation. The guard is
  retained until that reservation is finalized or discarded; the old proof
  could retain only a page slice and therefore lost this lifetime.
- The former raw Linux fence accessor is removed. The direct pre-finalized
  publisher remains the only current composed path, so this is a type-safe
  finalization prerequisite, not a claim that live allocator/retirement work is
  already invoked by the transaction core. The next slice must require that
  finalizer before the existing page-drain/publication path begins.

### 2026-07-24 - finalizer-to-publication bridge plan

- The current composed publisher takes pre-finalized private output. Although
  it happens to acquire the operation barrier before its drain, a future normal
  caller could finish allocator/retirement work before acquiring that barrier.
  This violates section 14.2 phase 1.
- Replace the normal private publisher with a callback that receives the
  lock-scoped finalization context, transaction core, handle, and prepared
  workspace, and can return only a typed transaction error. It must finish
  before the publisher can prepare, drain, sync, write metadata, or release the
  barrier. A context/source setup failure gets its own typed pre-publication
  result and releases the barrier normally.
- Keep a no-op pre-finalized adapter only under Rust test builds for the
  existing physical-failure fixtures. The normal library build will have no
  route that skips the required finalizer callback. A new composed test will
  prove the callback reads the exact retained source, consumes the reclaim
  fence, observes the held sidecar lock, and then publishes successfully.

### 2026-07-24 - finalizer-to-publication bridge implementation

- Replaced the normal composed publisher with
  `finalize_and_publish_fixed_point_private_output`. It acquires the Linux
  operation barrier, verifies the selected generation, gives its callback the
  exact lock-scoped context, and calls `prepare_fixed_point_private_output`
  only after that callback succeeds. Therefore physical drain, data sync, meta
  write, and lock release cannot happen first.
- The old pre-finalized publisher and fault-injection variant are now compiled
  only for Rust tests. The non-test benchmark/library compilation has no
  pre-finalized public-to-the-crate publication route. The lower raw page
  publisher is private to the Linux barrier implementation.
- A callback failure releases the barrier before returning the exact core
  error; a context-construction failure has a separate typed error. Neither
  case starts physical publication. The caller retains the pending core and
  must explicitly cancel/abort its fixed-point workspace as required by the
  existing transaction contract.
- The composed Linux test now proves both outcomes: a successful callback reads
  the exact retained page source, consumes the reader fence through the
  retirement tree, observes the sidecar exclusive lock, and publishes the
  target; a failing callback leaves the selected file and pending transaction
  unchanged until explicit cancellation. This is the mandatory lock-bound
  bridge, not yet the concrete allocator/retirement finalizer itself.

### 2026-07-24 - lock-bound aggregate execution plan

- Evidence: the strongest mixed bitmap/retirement Linux path constructs a
  `FixedPointPreparedAggregateWork`, executes it, updates the target, and
  finishes fixed-point input before calling the Linux publisher
  (`retirement_writer.rs:7644-7734`). Its later publisher call therefore tests
  only page drain and durable publication, not the aggregate transition that
  creates the final pending coordinator record.
- Move that aggregate construction and execution into the required finalizer
  callback. The callback will take the exact `PinnedPageSource` from the held
  context, build the aggregate against it, execute the aggregate, complete its
  canonical record, update the transaction target, and finish fixed-point
  input before the publisher may prepare/drain pages. The existing pre-lock
  terminal-export construction remains unchanged in this narrow slice.
- The permanent test will prove that the aggregate cannot have run before the
  callback, that it reads through the held retained source, and that no physical
  output begins until the callback has completed. This closes the core
  aggregate-to-publication ordering gap without falsely claiming that the
  remaining physical-page selection planners are fully deferred.

### 2026-07-24 - lock-bound aggregate execution implementation

- Moved the successful aggregate preparation, execution, canonical-record
  completion, target update, and fixed-point-input completion into the normal
  Linux publisher callback in
  `retirement_writer.rs:7659-7736`. Before that callback starts, the test
  proves the coordinator remains non-quiescent and the workspace remains idle.
- The aggregate now receives the exact `PinnedPageSource` supplied by the held
  Linux context. It must quiesce the coordinator and retain the expected scope
  fence before `finalize_and_publish_fixed_point_private_output` may prepare or
  drain private pages.
- This is deliberately not a claim that all bitmap or retirement physical-page
  planning is lock-bound yet: the fixture's typed terminal exports are still
  prepared before the barrier. The next repair must split those physical
  planner/finalizer paths without changing the bounded-scratch contract.

### 2026-07-24 - deferred physical-output preparation plan

- Evidence: the non-test coordinator API executes `final_callback` while it
  prepares a scope and stores the concrete bitmap root and target page count in
  `FixedPointPreparedWorkSlot` before a Linux barrier exists
  (`writer_fixed_point.rs:3361-3525`). Later terminal binding can only compare
  its pages with that pre-stored output (`writer_fixed_point.rs:2769-2824`).
  This makes a correct lock-bound allocator impossible: it must know concrete
  page identities before it is allowed to select them.
- The bitmap capacity planner has the same shape: it walks free-page candidates
  and retains their numbers in its capacity plan
  (`bitmap_cow.rs:2189-2208,2551-2693`). The final implementation must retain
  bounded capacity and scratch before the barrier, but choose those page
  numbers only after the stable reader scan.
- First split the coordinator state, without changing its bounded workspace:
  a normal-build reservation API will prepare only the exact scope,
  predecessor, and caller scratch. A typed terminal export, made by the real
  bitmap finalizer, will install and seal the root/page-count output only when
  it is bound under the live finalizer callback. Existing pre-final-output
  helpers remain test-only fault-fixture adapters.
- The permanent coverage will prove an unfinalized reservation cannot execute
  or publish, the late terminal export supplies the only accepted target, and
  the normal non-test build has no callback route that determines physical
  output before lock-bound finalization. The following slice will then use this
  boundary to run the bitmap/retirement planners on the retained page source.

### 2026-07-24 - deferred physical-output preparation implementation

- Split normal coordinator preparation into `FixedPointReservedWork` and the
  existing executable prepared-work state. The normal reservation records only
  the predecessor facts, exact coordinator scope, caller-owned scratch, and
  bounded carried input; its output slot remains empty.
- Only `with_finalized_produced_terminal_export` can install the bitmap root
  and target page count. It verifies the late typed terminal pages against the
  unchanged core pool, binds those unbound pages to the reserved scope, and
  then seals executable work. The old callback that selects an output before
  finalization is now Rust-test-only fixture support.
- Focused permanent tests prove a normal reserve/cancel leaves the output slot,
  prepared-scope slot, pool fence, and predecessor unchanged, while a stale
  reservation still releases its scope. This is the required coordinator
  boundary; it does not yet claim that the real bitmap/retirement planners are
  called through the Linux finalizer.

### 2026-07-24 - lock-bound physical planner plan

- Evidence: `FreeBitmapReservationPlanner::plan_capacity` walks the committed
  free bitmap and stores concrete candidates and their source kinds before it
  can attach a private scope (`bitmap_cow.rs:2189-2208,2551-2693`). The strongest
  Linux publication fixture likewise builds typed bitmap/retirement terminal
  output before its finalizer callback (`retirement_writer.rs:7286-7658`). Both
  conflict with the section 13/14.2 rule that physical page choice happens only
  after the stable reader scan under the operation lock.
- Keep capacity and caller scratch preallocated, but make the normal bitmap
  entry point consume `RetirementReclamation`. That move-only value can arise
  only from the held reader-table fence and keeps the barrier authority through
  bitmap binding/finalization. Existing free-standing capacity-plan entry
  points remain Rust-test fixtures only.
- Rework the composed Linux fixture so its callback uses the exact pinned
  selected source to select free pages, build a shadow pool, construct the
  retirement batch and bitmap COW output, and bind the resulting unbound typed
  terminal export to `FixedPointReservedWork`. Its arrays and finalization
  scratch are allocated before the barrier; the callback performs no heap
  allocation or temporary-file work.
- Permanent coverage will prove that the core output slot remains empty before
  the callback, that the actual free-bitmap reader and no-change retirement
  fence are consumed while the sidecar lock is held, and that the emitted
  terminal pages are then the exact pages durably published. A true reclaimed
  retirement batch remains a separate next slice; this one establishes the
  no-change allocator/retirement path without fabricating a reclamation proof.

### 2026-07-24 - lock-bound physical planner implementation

- `FreeBitmapReservationPlanner::plan_under_reclamation` now takes ownership
  of `RetirementReclamation` and returns a move-only plan that retains the
  live-operation authority through exact shadow-scope binding
  (`bitmap_cow.rs:781-792,1163-1179,2260-2270`). The unguarded physical
  capacity-plan entry point is test-only; normal builds cannot use it.
- The Linux integration finalizer now starts with only reserved coordinator
  work. While the sidecar operation lock is held, it reads the pinned selected
  source, obtains the real no-change retirement fence, chooses free-bitmap
  pages, builds the bitmap and retirement terminal pages in one shadow scope,
  binds the late export, and durably publishes it
  (`retirement_writer.rs:8243-8694`). It proves the callback allocates zero
  heap objects, another writer cannot acquire the sidecar lock, and the exact
  terminal page bytes are the bytes written to the database.
- A shared shadow scope contains both bitmap and retirement pages. The
  retirement exporter now counts only retirement-owned pages
  (`retirement_writer.rs:615-638`), preventing a bitmap page from making its
  terminal export appear stale.
- The integration exposed an independent bounded-work defect: finalizing a
  fully retained shared scope exhausted an undersized selective-finalization
  quota. The quota now covers both AVL-tree refreshes and normalization, with
  permanent no-allocation coverage at 3, 512, and 4096 retained pages
  (`private_page_pool/selective_finalization.rs:263-291`,
  `private_page_pool.rs:13970-14027`).
- This slice deliberately proves the real no-change retirement path only. A
  finalizer that selects and consumes an existing retired batch is still
  pending; it must use the same lock-bound authority rather than a synthetic
  reclamation proof.

### 2026-07-24 - selected retirement reclamation composition plan

- The first real selected-batch finalizer fixture reached the shared shadow
  pool with verifier-proven retired pages, then failed while deleting the old
  retirement batch: `RetirementTreeEditor` registered those same page numbers
  as unavailable historical entries and rejected their new private ownership.
  This is a composition gap, not a valid safety rejection: section 13 permits
  exactly those verified pages to be reused under the held operation lock.
- Preserve a typed reclamation authority through bitmap binding. It will retain
  the selected retirement identity, complete-prefix count, last transaction,
  and exact verified page sequence; the bound reservation will retain the
  operation-lock guard beside it. The retirement editor will accept it only for
  the matching selected oldest prefix, verify that relationship while deleting,
  and treat only its exact pages as safely reclaimed rather than historical
  list entries. No public API will expose an arbitrary reclaimable page slice.
- The permanent Linux test will retain a real reader pinned at the selected
  transaction, reclaim the eligible oldest batch, reuse its exact pages in the
  bitmap/retirement output, replace the old retirement metadata, and publish
  the resulting terminal pages without allocation while the operation lock is
  held.

### 2026-07-24 - selected retirement reclamation implementation

- `RetirementReclamationAuthority` now stays attached to the bound bitmap
  reservation with its selected retirement identity, verified oldest-prefix
  count, final retirement transaction, and exact sorted page sequence. That
  same reservation retains the live-operation guard alongside the authority.
  The retirement editor receives only a borrow of the authority; normal builds
  have no raw-page-list entry point.
- The new internal reclaimed-prefix editor verifies the caller's complete
  selected-metadata identity (database ID, transaction, nonce, page count,
  root, and batch count) against both the selected state and authority,
  verifies that it deletes exactly the selected prefix, and permits only those
  already-bound verified pages to transition from the deleted batch into new
  private retirement metadata. Every other historical list page keeps its
  normal conflict behavior.
- The permanent Linux integration test now keeps a real reader pinned at
  transaction 2, verifies and reclaims the oldest batch, reuses exactly pages
  21 and 23, replaces the retirement batch, publishes the resulting pages, and
  proves the wrong commit nonce is rejected before editing. It also retains the
  prior assertions that the operation lock is held, finalization allocates no
  heap memory, and the durable file contains the exact terminal bytes.
- This is an internal lifecycle repair consistent with the existing section
  13/14.2 contract. It changes neither the v4 byte format nor the public SDK.

### 2026-07-24 - reusable locked reclamation protocol plan

- The selected-batch integration fixture still spells out the same critical
  sequence inline: select under the live fence, verify every selected batch,
  perform the second pass into bounded page scratch, then hand the opaque result
  to the bitmap planner. That is correct in the fixture, but it leaves a future
  normal caller able to accidentally omit one required step while still holding
  a raw finalization context.
- Add one private `RetirementTree` callback boundary that owns the complete
  select/verify/second-pass sequence. It accepts the already-held fence, both
  nonzero work limits, caller-owned batch/page scratch, and a synchronous
  consumer. It passes the consumer only a no-change or verified reclamation
  authority plus exact batch/page counters. A read/verification failure invokes
  no consumer; a consumer failure is preserved without publishing anything.
- Route the real Linux selected-batch fixture through this boundary. This does
  not yet create public `Reclaim`, resource-budget construction, or generic
  allocator/retirement fixed-point geometry. It makes the required proof
  sequence reusable so that later writer work has one normal internal path.

### 2026-07-24 - reusable locked reclamation protocol implementation

- `RetirementTree::with_reclamation` now owns selection, full first-pass
  verification, and the bounded second pass before its synchronous consumer can
  receive a no-change or verified reclamation authority. It reports the exact
  selected batch/page counters and preserves a consumer error without starting
  publication. A corrupt selected tree/blob proves the consumer is never
  invoked.
- The selected-batch Linux finalizer fixture now uses that one protocol instead
  of spelling out selection, verification, and second-pass sequencing inline.
  Its allocator binding still receives the same opaque authority and retains
  the held operation barrier through finalization.
- This is a Rust private-lifecycle consolidation only. Go has no corresponding
  lock-bound finalization caller yet, so this checkpoint does not claim a Go
  reclamation-operation implementation or public cross-language SDK parity.

### 2026-07-24 - reclamation fixed-point preflight plan

- The selected-batch fixture still used hard-coded replacement page numbers.
  A generic `Reclaim` cannot do that: it must discover every currently
  committed retirement-tree/blob page that the selected-prefix delete and
  next-batch append will replace, combine that list with free-bitmap COW
  replacements, then build the new protected list before the real edit.
- The bitmap binder also selected a sorted union of ordinary free candidates
  and reclaimed pages without requiring every reclaimed page to fit. Lower
  ordinary candidates could therefore displace a verified reclaimed page from
  the bound scope. That is a page-loss/corruption risk, not an allocation
  preference.
- Add one private, bounded probe that performs the actual delete-plus-append
  structural traversal without building the new blob or mutating its page
  arena. Make all verified reclaimed pages mandatory during bitmap binding;
  insufficient scope capacity must fail before live-pool mutation. The next
  slice still must derive and charge complete blob/tree/bitmap fixed-point
  capacity, then wire this into the real clean-writer operation.

### 2026-07-24 - reclamation fixed-point preflight implementation

- `RetirementTreeEditor::probe_reclaimed_oldest_and_append_newest` now checks
  the exact selected identity/verified prefix and records every committed
  retirement-tree/blob replacement for the real edit. It performs no heap
  allocation and does not mutate its supplied page arena. The caller must use
  fresh edit scratch after combining this dedicated probe ledger with bitmap
  replacements into the new blob list.
- `FreeBitmapReservationAttachment::bind` now binds every verified reclaimed
  page. It chooses only the remaining number of lowest ordinary candidates,
  preserving the required ascending physical binding order. A reclaimed set
  larger than the pre-reserved scope returns typed `ArenaPages` budget failure
  before any live binding instead of silently omitting pages.
- This remains an internal Rust foundation. It does not claim a public
  `Reclaim`, generic Linux lifecycle wiring, a new v4 format rule, or Go
  parity.

### 2026-07-24 - dynamic selected-reclaim composition

- The selected Linux lifecycle now proves the protected list from live state:
  it combines the bitmap COW's actual committed replacements with a separate
  read-only retirement probe ledger, sorts that bounded list, and only then
  builds the next retirement blob. The old `[11, 12, 13]` list is an assertion
  about the fixture, not an input to the operation.
- A count-only `RetirementBlobBuilder::required_private_pages` exposes exact
  blob geometry without an input list or page allocation. It shares the same
  checked limits and branch math as `build`.
- A probe may run both before bitmap binding and against a fully bound shadow
  scope. When any selected reclaimed page is present in that scope, every
  selected page must be present; a partial set returns
  `ReclaimedPageNotConsumed` before the scan can use it. This prevents the
  probe from treating a partially attached authority as a valid reclaim input.
- The remaining generic operation still needs a bounded fixed-point preview:
  inserting unused safe pages into the bitmap can replace additional bitmap
  pages, and those exact pages must join the protected list before the final
  retirement blob/tree edit. No public `Reclaim` or generic lifecycle is
  claimed by this checkpoint.

### 2026-07-24 - reclamation bitmap-finalization capacity plan

- A normal bitmap reservation needs space only for the committed bitmap path
  verified while it selects its initial private pages. A locked reclamation
  reservation has one further bounded phase: finalization can return any
  unused private page to the free bitmap, and each such page can touch at most
  one `FREE_PATH_CAPACITY`-bounded bitmap path. Its later COW replacements are
  still committed pages that must enter the same protected batch.
- Keep ordinary/test-only capacity planning unchanged. Only
  `plan_under_reclamation` will reserve replacement-ledger and page-index
  capacity for `verified_path_pages + private_scope_pages *
  FREE_PATH_CAPACITY`. The same capacity is reserved in its detached stage
  shadow. This is bounded by the operation's pre-reserved private scope, not
  file size, free-page count, or retirement history.
- Add a permanent two-leaf regression: initial allocation rewrites the first
  leaf, then finalization returns a verified reclaimed page in the second leaf.
  The locked plan must reserve enough capacity before binding, and finalization
  must record the second committed leaf as a replacement without heap
  allocation. A shortage in either live or stage capacity must be a typed
  pre-binding resource failure. The later read-only fixed-point preview will
  consume this capacity; this slice does not claim to have completed that
  preview or the generic clean-writer operation.

### 2026-07-24 - reclamation bitmap-finalization capacity implementation

- `plan_under_reclamation` now expands only its replacement ledger and
  page-index budget by the bounded finalization allowance. Normal reservation
  planning retains its exact initial-path capacity.
- The live and detached-stage ledgers use the same expanded capacity. The
  planner checks both buffers before it binds a private scope, reporting typed
  `ReplacementPages` or `IndexNodes` budget exhaustion rather than incorrectly
  classifying the caller's undersized storage as stale state.
- Permanent two-leaf coverage proves that finalization can record a second
  bitmap leaf that initial allocation did not replace. Separate coverage proves
  an undersized replacement ledger fails before binding. This is capacity
  preparation only: it does not yet preview that later leaf before constructing
  the retirement record, and it does not claim a generic clean-writer
  operation.

### 2026-07-24 - read-only bitmap-finalization preview plan

- Extract the existing staged-shadow construction so finalization and preview
  execute the same COW logic. A private preview will run the existing
  discovery/replay checks, return the complete post-finalization bitmap
  replacement list into caller-provided bounded storage, and leave the live
  pool, reservation scope, and live bitmap COW unchanged.
- Require output capacity equal to the already reserved replacement ledger and
  reject shortage before source traversal with a typed resource error. Clear
  cached source slots before every preview return so the same fixed scratch can
  be retried or used by the real finalizer.
- Add coverage that a two-leaf preview sees the later leaf, does not alter the
  live commitment, and agrees with a subsequent real finalization. This is a
  necessary bitmap component only: prospective retirement blob/tree output
  still creates a separate bounded fixed-point problem, so this slice cannot
  claim generic reclamation completion.

### 2026-07-24 - read-only bitmap-finalization preview implementation

- The terminal finalizer and preview now share one detached-stage bitmap COW
  construction path. Preview runs the same bounded discovery/replay checks and
  copies the complete post-finalization bitmap replacement list only after the
  replay agrees.
- Preview validates caller output capacity before it traverses the committed
  source. It changes only detached stage scratch, clears cached source slots on
  success and failure, and leaves the live pool commitment, scope, and live
  bitmap COW unchanged.
- Permanent tests cover the previously missed second leaf, exact agreement with
  a subsequent real finalization using the same scratch, zero preview
  allocations, pre-traversal output shortage, and cache/output cleanup after a
  late source-access failure. This is private Rust infrastructure; no public
  API, Go parity, retirement-output preview, or generic clean-writer operation
  is claimed.

### 2026-07-24 - stage-aware reclamation preview plan

- Bitmap-only preview cannot model a future retirement blob/tree: before that
  output exists, its reserved pages look unused and bitmap finalization would
  incorrectly return them as free. Add one private stage-aware preview hook
  that gives a caller the detached pool and exact copied scope before each
  discovery/replay finalization pass.
- The caller will construct the prospective blob/tree only in that detached
  scope, using preallocated scratch, and return an equality witness. The helper
  must run the callback twice and reject differing witnesses, so a future
  generic fixed point cannot accept a one-pass or unstable retirement preview.
- Use it first to replace the selected Linux fixture's hard-coded protected
  list. The later production clean-writer implementation will reuse this exact
  primitive; this step does not expose a public callback API or allow a stage
  callback to touch live pages.

### 2026-07-24 - stage-aware reclamation preview implementation

- `preview_terminal_replacements_with_stage` is private Rust infrastructure.
  It constructs the same detached bitmap state used by terminal finalization,
  gives the callback only that detached pool/scope and the immutable verified
  reclamation authority, then runs the callback before both bitmap
  discovery/replay passes. The callback returns an equality witness; a
  differing witness or bitmap replay becomes typed stale-plan failure. The
  existing bitmap-only preview is now its no-op wrapper.
- The selected Linux reclaim fixture now starts from the live bitmap and
  retirement-probe replacement lists, repeatedly stages the actual retirement
  blob/tree edit with fixed caller scratch, and stops only when the sorted,
  deduplicated protected list is unchanged. It checks the staged retirement
  replacement ledger against the prior exact probe and checks that the real
  terminal bitmap plus retirement replacement union equals the converged list.
  The fixture no longer supplies `[11, 12, 13]` as operation input.
- A permanent two-leaf test forces the first staged pass to discover a later
  bitmap leaf after the staged retirement blob/tree consumes two private pages.
  It reserves the operation's full five-page payload budget before binding,
  requires exactly two fixed-point iterations, proves zero allocation inside
  each preview operation, and proves the converged list equals real terminal
  output. This establishes the capacity boundary that a future clean writer
  must preflight; it does not infer that budget automatically.
- No public `Reclaim`, generic clean-writer operation, format change, or Go
  parity is claimed. The prospective stage remains private until the complete
  production lifecycle has one bounded capacity model and error contract.

### 2026-07-24 - bounded terminal-capacity plan

- `PrivatePagePool::preflight_coordinator_terminal` currently requires the
  exact final terminal-page count to equal the scope count reserved before the
  Linux operation lock. That makes a generic finalizer impossible without
  choosing physical output before the stable reader scan; the current Linux
  fixture hides the issue by hard-coding a three-page scope and three-page
  output.
- Section 13 instead requires a checked bounded reservation before the lock
  and exact physical page selection/fixed-point output under the lock. The
  internal coordinator must therefore treat its reserved scope count as a
  maximum, not an exact output count. It must accept a nonempty terminal prefix
  no larger than that capacity, retain the unused suffix as vacant scope state,
  and still close the entire scope during normal cleanup.
- Keep all capacity bounded and caller-owned. Terminal-page buffers are sliced
  to the actual output count; a larger-than-reserved output fails before live
  mutation. Update the prepared epoch proof to charge movement of the full
  reserved scope plus binding of only the actual output prefix. Add permanent
  tests for a smaller output, appended-tail accounting, exact cleanup of unused
  slots, and unchanged rejection of empty/oversized output. This is an internal
  transaction representation repair, not a public API or format change.

### 2026-07-24 - bounded terminal-capacity implementation

- A prepared coordinator scope is now a checked maximum. A terminal journal
  must be nonempty and may bind only its actual prefix when that prefix fits
  inside the pre-reserved scope. Its epoch proof charges movement of the entire
  scope, three binding steps per output page, and sealing.
- Sparse replay retains the unused suffix as canonical scoped vacancy and
  closes every reserved slot during selective cleanup. The test-only direct
  adapter now maintains the same tree and vacant-suffix aggregates, including
  later prior-return accounting, so its exact commitment remains valid for a
  shorter terminal output.
- Permanent tests use a four-page reservation with two output pages, including
  one appended tail page. They prove prefix binding, bounded/allocation-free
  sparse replay, exact scope cleanup, valid direct-path commitment, and
  no-mutation rejection of empty and oversized journals. No public API, file
  format, generic reclaim operation, or Go parity is claimed.

### 2026-07-24 - private lock-bound reclamation reservation plan

- Evidence: the Linux selected-reclamation fixture performs the correct
  selected-source identity construction, fence-held selection/verification,
  bitmap capacity planning, exact shadow-scope binding, and planned-reservation
  application inline in `retirement_writer.rs:8915-8998`. It is production-like
  behavior trapped in test code, so a later normal writer could duplicate or
  omit part of that required sequence.
- Extract that common prefix into one non-test, crate-private helper. It accepts
  only the selected metadata, pinned committed source, held reclaim fence,
  bounded limits, caller-owned verification/planner scratch, and a pre-reserved
  shadow scope. It returns a bound bitmap reservation only after the full
  retirement select/verify/second-pass protocol and planned bitmap reservation
  have succeeded. The returned reservation retains the operation-barrier guard.
- This narrow extraction deliberately does not invent a public `Reclaim` API,
  an error mapping for the eventual public writer SDK, or generic fixed-point
  geometry. The remaining test-only retirement/blob/fixed-point body will be
  moved in later bounded chunks after this shared lock-bound input boundary is
  proven.

### 2026-07-24 - private lock-bound reclamation reservation implementation

- Added non-test private module `reclamation_finalizer.rs`. Its only entry
  point builds the selected retirement identity from the supplied metadata,
  runs `RetirementTree::with_reclamation`, creates the locked bitmap plan,
  binds it to the supplied shadow scope, and applies its planned reservation
  before its consumer can run. The consumer receives the move-only bound
  reservation, which retains the operation-barrier guard.
- All backing storage remains caller-owned: bitmap planner/stage buffers,
  verified retirement batches/pages, and the shadow pool/scope. Zero batch,
  page, or bitmap-payload limits fail before source access or shadow mutation;
  read, planner, and consumer failures remain distinct typed internal errors.
- The two Linux end-to-end cases now call that helper. They continue to prove
  no-change and selected-prefix behavior over the retained source under the
  held sidecar operation lock; the later test-only blob/tree/fixed-point body
  is intentionally unchanged for the next extraction.

### 2026-07-24 - private lock-bound reclamation reservation ownership correction

- The first extraction passed the bound reservation through a generic consumer
  callback. Although the current fixture returned it successfully, a future
  consumer error would drop the only retry authority while leaving its
  caller-owned shadow scope mutated. That is incompatible with the existing
  move-only finalizer failure contract.
- The helper now returns one `LockedReclamationBitmapReservation` directly:
  exact pass counters plus the move-only bound bitmap reservation. Later
  finalization receives that value and must retain or explicitly return it on
  a pre-terminal error. There is no callback-owned reservation to discard.

### 2026-07-24 - selected-reclaim protected-list extraction plan

- Evidence: after the shared lock-bound reservation is created, the selected
  Linux fixture still contains the whole read-only fixed-point calculation
  inline in `retirement_writer.rs:9011-9154`: probe the retirement replacement
  pages, union them with bitmap replacements, stage the prospective retirement
  blob/tree before each bitmap preview pass, and iterate until the protected
  list is stable. This is the exact input needed by a future clean `Reclaim`,
  but it is still test-only logic.
- Extract only that read-only selected-reclaim protected-list calculation into
  non-test private machinery. It will accept an already-bound selected
  reservation and caller-owned probe, stage, list, and finalization scratch;
  it returns the converged sorted page list without mutating the live bitmap
  scope. Capacity shortages, unexpected probe releases, malformed replacements,
  or non-convergence remain typed failures before terminal finalization.
- Keep no-change out of this helper. Decision 51 says public clean `Reclaim`
  returns `NoChange` and starts no commit when no retired batch is eligible;
  the fixture's no-change path represents a different ordinary finalization
  scenario. Mixing them would hide that semantic distinction and falsely imply
  that a no-change reclaim should write a retirement batch.

### 2026-07-24 - selected-reclaim protected-list implementation

- Added `preview_selected_reclamation_protected_pages` as normal private Rust
  machinery. It probes the selected retirement edit, unions its replacement
  pages with the bound bitmap replacements, stages the prospective
  blob/tree edit in both bitmap preview passes, and returns only a converged,
  sorted protected list.
- The helper deliberately takes no independent source or retirement metadata.
  It derives both from the already-bound reservation and its verified
  reclamation authority. This prevents an internal caller from accidentally
  combining proof from one selected generation with pages from another.
- All input and work storage remains caller-owned. Short lists, malformed page
  numbers, missing selected identity, changed probe evidence, and a failed
  fixed point return typed errors before terminal finalization. The Linux
  fixture keeps its prior inline calculation as an independent oracle and
  proves the private helper returns the same list without allocation.
- No public `Reclaim`, generic lifecycle behavior, format change, or Go parity
  is claimed by this extraction.

### 2026-07-24 - selected-reclaim retirement-capacity plan

- Evidence: the protected-list helper obtains the exact retirement-tree
  private-page upper bound from
  `RetirementTreeEditor::probe_reclaimed_oldest_and_append_newest`, but
  currently drops it. The later test-only finalizer discovers its blob/tree
  capacity only while constructing output. A normal lock-bound finalizer must
  know its bounded scope requirement before it starts that real mutation.
- Preserve the probe's tree-page bound with the converged protected list. Once
  the list is stable, calculate its exact blob-page geometry and require their
  checked sum to fit the already-bound shadow scope's remaining private pages.
  Return that immutable capacity fact with the list for the later terminal
  stage; do not select more pages, resize storage, or change public behavior.
- A short scope must return the existing typed retirement budget error before
  real blob/tree construction. This is a private Rust preflight boundary, not
  the public `Reclaim` operation or a new file-format rule.

### 2026-07-24 - selected-reclaim retirement-capacity implementation

- The protected-list helper now returns a small immutable result containing
  the converged pages, exact blob-page count, exact safe tree-page bound, and
  their checked total. It compares that total with the remaining pages in the
  bound bitmap shadow scope before any caller can build real retirement output.
- A short scope reports `PrivatePageBudgetTooSmall`; arithmetic remains
  checked. The existing selected Linux case proves the exact two-page
  retirement budget equals both its blob-plus-tree parts and the available
  bound scope. Focused tests cover the pure exact geometry and short-scope
  error path.
- This only preserves and proves capacity already discovered by the selected
  probe. It does not mutate a terminal scope, expose `Reclaim`, or alter v4
  bytes or Go behavior.

### 2026-07-24 - selected-reclaim retirement-stage plan

- Evidence at planning time: after capacity was known, the Linux fixture still
  directly created the retirement blob, applied the verified delete-and-append
  edit, exported its terminal pages, and synchronized the bitmap view
  (`retirement_writer.rs:9220-9300`). This is the first real mutation of the
  isolated shadow attempt and must not be reimplemented independently by a
  future writer.
- Add one normal private helper that accepts an already-bound selected
  reservation, the exact shadow pool/scope that created it, the protected-list
  result, and caller-owned edit/export scratch. It must derive source and
  identity from the reservation, verify the exact scope before and after the
  edit, build the blob, apply the verified reclamation edit, and return a typed
  retirement terminal export.
- Validate blob and terminal-buffer capacity before mutation. A stage failure
  is reported distinctly; the caller still owns the outer pending transaction
  and discards this isolated shadow attempt, so it can retry from a fresh
  finalizer attempt or abort through the existing transaction contract. Do not
  create a public `Reclaim` operation or change format/API semantics here.

### 2026-07-24 - selected-reclaim retirement-stage implementation

- Added one private selected-reclaim retirement stage. It accepts only the
  already-bound bitmap reservation and its exact shadow pool/scope, derives
  the source and retirement identity from that reservation, preflights all
  caller-owned buffers, creates the blob, applies the verified delete/append,
  exports the exact terminal page prefix, then synchronizes the bound bitmap
  view.
- The terminal export count is the checked sum of blob pages and tree pages;
  the tree edit result alone intentionally excludes the blob. The Linux
  end-to-end fixture exposed this distinction before publication and now proves
  both output pages are exported.
- Scratch now separates the one returned output slice from temporary work
  slices. This permits the caller to reuse probe/finalization/replacement
  buffers immediately after preview or staging without allocation or an
  unsafe lifetime extension.
- The selected Linux fixture keeps the prior independent fixed-point replay,
  compares its protected list with the normal helper, then uses the normal
  stage for the real mutation. It also proves short blob/terminal buffers and
  a same-valued but different scope fail before mutating the shadow scope.
- This remains Rust-private lifecycle work. It adds no public `Reclaim`, SDK
  API, format byte, default validation, or Go behavior.

### 2026-07-24 - exact bitmap-terminal-count plan

- Evidence: `SealedFreeBitmapOutput::prepare_terminal_export` requires a
  destination slice whose length is exactly the number of bitmap-owned pages
  (`bitmap_cow/selective_finalization.rs:2774-2810` and
  `private_page_pool.rs:5434-5522`). The selected Linux fixture currently
  hard-codes a one-page bitmap journal. A normal bounded finalizer cannot
  safely select that length before finalization or allocate a second buffer.
- The existing finalization pass already classifies every retained scope member
  as bitmap or retirement while it computes the stable retained partition.
  Preserve the exact bitmap-owned count in the sealed output there. This adds
  one checked counter increment in existing work, not an additional scope scan
  or a format/API field.
- The later composite finalizer will use this count to slice its caller-owned
  maximum bitmap journal. Add permanent coverage that a retained retirement
  page is excluded from the bitmap count and that an exact prefix exports only
  bitmap pages. No public `Reclaim`, new allocation, or Go behavior is part of
  this narrow step.

### 2026-07-24 - exact bitmap-terminal-count implementation

- The existing retained-scope partition now records the exact number of pages
  still owned by the current bitmap transaction. The count travels only through
  private finalization state into `SealedFreeBitmapOutput`; it does not change
  the on-disk format, a public API, or the full scope record used by the
  coordinator.
- The normal selected Linux fixture slices a caller-owned maximum journal to
  that exact count. Permanent tests prove a retained retirement page in the
  same scope is excluded and that an unused caller-owned suffix remains empty.
- The change adds one checked increment during work already required to build
  the retained partition. It adds neither allocation nor a second scope scan.

### 2026-07-24 - exact bitmap-terminal-count validation

- Focused coverage passes for exact bitmap terminal export, retained retirement
  ownership in the same scope, and both selected/no-change Linux finalizers.
- Rust workspace matrices pass: 449 no-default tests and 564 all-feature
  tests. Warnings-denied all-target Clippy and all-feature benchmark
  compilation pass.
- Go `test ./...` and `vet ./...` pass. Formatting, diff checking, and the
  project SOW audit are run for the checkpoint. Go behavior is unchanged: this
  is private Rust output sizing for a later composition step.

### 2026-07-24 - selected-reclaim terminal-composition plan

- Evidence: after the normal retirement stage returns, the selected Linux
  fixture still directly finalizes the bitmap, slices its journal, merges it
  with retirement output, and binds the result to the coordinator
  (`retirement_writer.rs:9372-9483`). That is the last mutable composition
  sequence trapped in test code before the existing coordinator handoff.
- Extract one crate-private selected-reclaim terminal helper. It consumes the
  move-only lock-bound reservation, runs the normal retirement stage, seals
  the bitmap, uses the sealed exact bitmap-page count to export only the needed
  bitmap prefix, and produces the existing sorted combined terminal export.
  It will not bind or publish to the writer core in this step.
- Preflight every caller-owned terminal buffer before the retirement edit:
  bitmap and combined journals must hold the bounded shadow-scope maximum and
  their usable prefixes must be empty; bitmap finalization scratch is checked
  before the edit. This keeps capacity/input failures retryable. Once the
  retirement edit starts, a later failure explicitly discards this isolated
  shadow attempt; the outer transaction remains pending and follows its
  existing abort/restart contract.
- Rewire only the selected Linux fixture to the helper and add permanent tests
  for pre-mutation combined-journal rejection and exact bounded prefix use.
  No public `Reclaim`, SDK API, format byte, default validation, Go parity, or
  direct writer-core binding is part of this step.

### 2026-07-24 - selected-reclaim terminal-composition implementation

- Added one private terminal-composition helper. It owns the selected
  reservation until it either returns a prepared combined export or reports a
  failure. Pre-mutation failures return the unchanged reservation for retry;
  post-mutation failures return only a discard outcome, so callers cannot
  accidentally reuse a changed shadow attempt.
- It checks finalization scratch and maximum journal capacity before the
  retirement edit. After sealing, it uses the exact bitmap count already
  recorded by finalization, slices caller-owned bitmap and combined journals to
  their exact used prefixes, and merges the existing producer-bound exports.
- The selected Linux lifecycle fixture now uses this helper. It proves short
  combined and bitmap journals leave the scope unchanged, return retry
  authority, and then succeeds with that same authority. The maximum three-page
  bitmap journal keeps its unused two-page suffix empty after publication.

### 2026-07-24 - selected-reclaim terminal-composition validation

- Focused selected and no-change Linux finalizer tests pass. The selected case
  exercises both retryable short-journal failures, reuses the returned
  reservation, preserves the established fixed-point replacement oracle, and
  remains inside the finalizer-wide zero-allocation assertion.
- Rust workspace matrices pass: 449 no-default tests and 564 all-feature
  tests. Warnings-denied all-target Clippy and all-feature benchmark
  compilation pass.
- Go `test ./...` and `vet ./...` pass. Rust formatting, diff checking, and
  the project SOW audit pass. No normative spec update is needed: this remains
  private Rust lifecycle composition with no public API or on-disk change.

### 2026-07-24 - selected-reclaim coordinator-bind plan

- Evidence: the selected Linux lifecycle fixture now obtains one typed,
  complete terminal export, but still manually extracts its bitmap root and
  pending page count before calling the reserved coordinator bind
  (`retirement_writer.rs:9622-9642`). That separation makes the caller
  responsible for keeping three inseparable facts together.
- Add one crate-private method on the typed selected-reclaim export. It will
  consume the already-reserved coordinator work, use only the root/page count
  retained by that export, and invoke the existing late terminal bind. It stops
  before aggregate execution, target mutation, page draining, and publication.
- A failed bind will return both the untouched reserved work and the original
  selected export. The coordinator's bind preflight runs before it assigns live
  pool slots, so this preserves an exact retry path without rebuilding the
  shadow output or silently continuing a partial transaction.
- Rewire only the selected Linux fixture. It will deliberately reject nonce
  zero, retain both returned authorities, and retry them successfully with the
  normal nonce. No public `Reclaim`, SDK API, on-disk byte, default validation,
  Go behavior, or publication behavior is part of this step.

### 2026-07-24 - selected-reclaim coordinator-bind implementation

- Added a crate-private move-only bind method to the typed selected-reclaim
  export. It carries its own exact bitmap root, pending page count, retirement
  result, bitmap proof, and combined terminal journal into the existing
  `FixedPointReservedWork` late-bind boundary.
- A rejected bind returns the same reserved coordinator work and complete
  selected export. This makes retry explicit without rebuilding the already
  changed shadow attempt; successful binding continues into the existing
  aggregate path and does not itself execute, drain, or publish.
- The selected Linux fixture now deliberately supplies nonce zero, confirms the
  complete terminal journal is unchanged, and then binds the returned values
  with the normal nonce before its existing aggregate/publication assertions.
  The no-change fixture keeps its separate ordinary-finalization path.

### 2026-07-24 - bound-terminal cancellation plan

- Evidence: a successful late bind converts the reserved scope into a prepared
  terminal work unit (`writer_fixed_point.rs:1900-1938`). Before aggregate
  execution, that work has not changed the live private pool, but it owns both
  the prepared scope slot and a terminal-page journal whose assigned pool slots
  make the caller backing non-reusable. Neither normal type had a cancellation
  path; dropping it would leave stale preparation backing and make a later
  whole-draft abort/retry depend on incidental scratch replacement.
- Add one crate-private cancellation path for prepared terminal work. It will
  reset the temporary terminal journal, release the unactivated prepared scope,
  clear the prepared work slot and scratch, and report stale evidence without
  withholding that cleanup. The produced-terminal wrapper will consume its
  bitmap proof and use the same path.
- This is only the pre-Active cancellation boundary. Once aggregate execution
  starts, existing core/pool abort semantics remain authoritative. No public
  `Reclaim`, SDK API, format byte, validation default, Go behavior, or durable
  publication behavior changes in this slice.

### 2026-07-24 - bound-terminal cancellation implementation

- `PrivatePagePreparedCoordinatorTerminal` now retains its caller journal as a
  mutable borrow but exposes only immutable views during normal execution. Its
  consume-only discard path resets every entry before the prepared scope can
  become active. Successful test-only terminal application returns the immutable
  journal only after consuming that mutable authority.
- Added crate-private cancellation to prepared work, prepared terminal work,
  and produced terminal work. It attempts to release the unactivated scope and
  clears the work slot/scratch even when its evidence is stale, while still
  returning the stale result. A produced-terminal cancel also consumes its
  bitmap proof, so it cannot be confused with a retryable late-bind failure.
- Added permanent no-allocation tests for direct bound-terminal cancellation,
  stale-evidence cleanup, and the produced-terminal wrapper. They prove no live
  pool mutation, no retained assigned slot in the caller journal, and a usable
  predecessor after cancellation.

### 2026-07-24 - aggregate-scope lifecycle correction

- Evidence: aggregate preparation simulated the private-pool transition with a
  borrowed prepared scope, then `FixedPointPreparedWork::into_aggregate_base`
  discarded the only move-only reservation (`writer_fixed_point.rs:3105`). The
  sparse replay neither owned nor cleared that reservation
  (`private_page_pool.rs:1249`). A successful aggregate therefore left its
  caller scope slot occupied, while a pre-Active execution failure had no
  aggregate-level cancellation route to release the scope, terminal journal,
  and prepared workspace record together.
- Retain the scope reservation in aggregate state. Preflight it against the
  prepared sparse replay immediately before Active, then consume and clear it
  in the replay's mechanically infallible suffix. Add one aggregate cancellation
  path that discards the terminal journal, releases replay/workspace backing,
  clears the prepared scope/work slot/scratch, and reports stale evidence only
  after that cleanup.
- This is a private Rust lifecycle repair. It adds no public `Reclaim` or SDK
  API, no on-disk format byte, no validation-default change, and no Go behavior.

### 2026-07-24 - aggregate-scope lifecycle validation

- Permanent 4,096-slot sparse-replay coverage now proves normal replay consumes
  the prepared scope slot (`writer_fixed_point.rs:4680`). A new aggregate-cancel
  test proves zero allocation, an unchanged live pool, an empty terminal
  journal/work slot/scope slot, an idle workspace, and a successful outer core
  workspace release plus abort (`retirement_writer.rs:7171`).
- Full validation passes: Rust workspace tests 452 without optional features
  and 567 with all features; warnings-denied all-target/all-feature Clippy;
  all-feature benchmark compilation; Go `test ./...` and `vet ./...`; Rust
  formatting; whitespace checking; and the project SOW audit.

### 2026-07-24 - Reclaim owner construction plan

- Evidence: the only complete selected-reclamation path is the Linux test
  fixture at `retirement_writer.rs:9186-9955`. It begins a private core draft
  and reserves coordinator workspace before it enters the operation barrier.
  That cannot become `Reclaim`: the normative contract requires an eligible
  batch decision under the same barrier as publication, and `NoChange` must
  start no draft or generation (`binary-format-v4.md:1089-1104`). The generic
  Linux publisher also acquires the barrier with `|| false`
  (`os/linux/live_writer.rs:734`) and therefore cannot carry the caller's
  cancellation probe.
- The exact shadow scope cannot be reserved at a guessed size. Bitmap binding
  requires `scope.capacity == private_pages`
  (`bitmap_cow.rs:1100-1105`), while its lock-bound plan already knows that
  exact count before binding (`bitmap_cow.rs:1217-1234`). The current helper
  hides that point by immediately binding a caller-chosen shadow scope
  (`reclamation_finalizer.rs:388-452`).
- First implementation slice: split lock-held Reclaim selection/planning from
  shadow binding. Its typed result is either `NoChange`, with no bitmap plan or
  private transaction, or one move-only selected bitmap plan that retains the
  reader fence and exposes its exact required shadow capacity. Binding and all
  existing finalization behavior remain unchanged. This is private Rust
  plumbing; it changes no on-disk bytes, public API, validation default, or Go
  behavior.
- The existing reader helper deliberately makes a selected result
  non-escapable: `RetirementReclaimedPages` borrows a stack-local selection and
  `with_reclamation` therefore uses a higher-ranked callback
  (`retirement_reader.rs:185-276,1000-1047`). The first slice will preserve the
  same full verification and second-pass order, but make the returned result
  own copied selected facts plus the move-only barrier guard. It can then carry
  the verified page-scratch borrow into the bitmap capacity plan without
  extending a stack-local borrow or weakening reader protection. The old
  callback remains a wrapper over that private prepared result.
- The following owner slice will use that result in one private operation:
  acquire the cancellable barrier; select; release immediately on `NoChange`;
  allocate/bind only the checked workspace partitions; build the shadow terminal;
  begin the clean core and reserve the live scope only after final output is
  known; execute, drain, and publish while retaining the same barrier. Every
  pre-Active authority is explicitly cancelled before the workspace is released
  and whole-draft abort runs. Any post-mutation failure takes the existing
  abort route; publication ambiguity keeps its factual result.
- The first owner implementation keeps the already-existing
  `FixedPointCoordinatorWorkspace` as a crate-private borrowed input so the
  operation can prove the real lock/draft/publication order without exposing a
  temporary SDK contract. It constructs the transaction core only after a
  selected terminal is complete. This is an implementation staging boundary:
  callers outside the crate cannot supply the workspace, and the next slice
  replaces this private input with the decided opaque SDK-owned workspace before
  any public Reclaim API exists.
- The operation will replace the fixture's caller-owned backing with the
  already-decided opaque SDK-owned workspace before it becomes a production SDK
  entry point. That workspace remains one checked, transaction-budgeted logical
  partition created outside the operation path; no hot-path allocation or
  caller-writable scratch is acceptable.

### 2026-07-24 - private clean-writer Reclaim owner

- Rust now has a crate-private Linux Reclaim owner
  (`os/linux/live_writer/live_reclaim.rs:274`). It acquires the cancellable
  operation barrier first, performs selection and verification while that exact
  barrier is held, returns `NoChange` without constructing a transaction core,
  and otherwise binds the exact selected shadow capacity before building the
  terminal output. Only then does it create the private transaction, replay the
  terminal pages, and publish through the held barrier.
- The owner maps every pre-publication cancellation to a factual cancellation
  result after whole-draft abort. A failed barrier release after `NoChange` has
  its own failure class; it is not misreported as cancellation
  (`live_reclaim.rs:150-159,846-889`). Physical publication still preserves the
  existing `NotCommitted`, `OutcomeUnknown`, and committed-after-phase-five
  distinction (`live_reclaim.rs:901-1069`).
- This remains a private staging owner, not an SDK entry point. Its raw scratch
  is crate-private and single-use only; the next slice must replace it with the
  resolved opaque SDK-owned workspace before exposing Reclaim outside the
  crate. No v4 bytes, Go behavior, public API, or validation default changed.

### 2026-07-24 - opaque Reclaim workspace implementation plan

- Evidence: the new owner still receives one borrowed coordinator workspace and
  57 independently borrowed scratch partitions (`live_reclaim.rs:64-145,273-279`).
  That is not an SDK-owned workspace, even though the type is crate-private.
  The current coordinator workspace also cannot simply become an owning Rust
  struct: its fixed journals store references to mutable cells and each retained
  record borrows its scratch arrays (`writer_fixed_point.rs:98-102,229-321`).
  Putting those references beside their targets would create a self-referential
  object.
- The repair remains internal. Rework the coordinator journals into typed,
  prevalidated destination indexes. Before Active, aggregate preparation will
  validate every index and apply those workspace-only changes; Active then has
  no indexed lookup or fallible mutation. This preserves the existing
  mechanically-infallible Active suffix without a self-referential or raw-pointer
  workspace.
- Add one opaque Linux Reclaim workspace that owns every vector once, checks its
  complete logical retained capacity before allocation, and constructs one
  short-lived borrowing coordinator view for each operation. The view is only
  an implementation adapter: the operation receives no caller-owned array, and
  its charged byte count is the complete opaque workspace rather than just the
  old coordinator subset. Workspace reset will occur at most once before a
  subsequent attempt, never both in the owner and in the transaction core.
- Permanent Linux coverage will replace the raw fixture backing with that owner,
  prove zero allocations after construction, exact complete-resource charging,
  reuse after cancellation, and unchanged `NoChange`/abort behavior. This adds
  no public SDK method, format byte, default validation, or Go behavior.

### 2026-07-24 - opaque SDK-owned Reclaim workspace

- The raw 57-partition test fixture is gone. `LinuxLiveWriterReclaimWorkspace`
  now owns every Reclaim vector and fixed slot
  (`os/linux/live_writer/live_reclaim.rs:68-1185`); it checks every multiplied
  capacity and the complete logical retained-byte charge before the first
  allocation. An attempt validates its requested batch/page/payload limits
  before it takes the Linux operation barrier.
- A temporary borrowing coordinator view is built only inside the operation.
  Its transaction charge is raised from the coordinator subset to the opaque
  workspace's complete charge, and a debug invariant verifies the exact value
  at that handoff (`live_reclaim.rs:956-1010`). The workspace is canonical at
  construction and resets its retained partitions exactly once before a later
  attempt, never inside the live transaction path.
- Fixed-point journals now retain typed prevalidated indexes rather than
  references into caller storage (`writer_fixed_point.rs:229-536`). Their
  workspace writes are checked and applied before Active; an unexpected
  internal mismatch rolls those writes back as a typed pre-Active failure
  rather than panicking (`writer_fixed_point.rs:2168-2314`). This is what makes
  the owning workspace safe without raw pointers or a self-referential Rust
  object.
- The real Linux Reclaim tests now construct the opaque workspace outside the
  allocation counter. They prove no-change, selected publication, both
  cancellation positions, zero operation-time allocations, and retry from the
  same workspace after a whole-draft cancellation
  (`retirement_writer.rs:9973-10200`). Dedicated workspace tests cover invalid
  and overflowing capacities plus out-of-capacity limits before an attempt
  (`live_reclaim.rs:2101-2153`).
- This is internal Rust lifecycle work only. It changes no v4 on-disk byte,
  public SDK signature, validation default, Go behavior, or Phase-2 scope.

### 2026-07-24 - semantic range-construction foundation plan

- A fresh public-surface audit shows that neither engine has a usable v4 SDK
  yet. Rust exports only primitive value types from `lib.rs:72-75`, and Go
  exports only the same semantic foundation from `types.go:6-55`. The existing
  Linux reader/writer and Reclaim path are crate-private test infrastructure,
  not an externally usable format implementation.
- The exact range tree is also reader-only today. Rust decodes leaves and
  branches in `range_page.rs:64-206`, while all physical range-page creation is
  test-local helper code (`range_reader.rs:650-722`); Go has the matching
  reader-only split in `internal/exactv4/range_page.go:46-180`. No generic
  range-page encoder, ordered tree builder, or page-backed sequential mutation
  engine exists in either implementation.
- The next work therefore begins at the shared semantic-data foundation, not a
  public wrapper around incomplete internals. First add a bounded, allocation-
  free range leaf/branch encoder in Rust and Go: it must enforce the exact
  family, transaction, capacity, ordering, non-overlap, membership-zero, page
  summary, reserved-byte, and CRC rules. This is only the physical codec used by
  later builders; it does **not** accept unordered input as a public workflow.
- Then add an ordered bottom-up range-tree builder and, separately, the
  operation-private page-backed sequential assignment engine required by
  decision 47. The latter is the first component allowed to accept unordered,
  overlapping direct or membership input; it must mutate private COW pages in
  arrival order and create no sorting file or per-record heap allocation. Only
  after that engine exists can direct replacement, retention refresh, named-feed
  workflow, or public `AddRanges` be honestly exposed.
- This sequencing follows the normative range layout (`binary-format-v4.md:434-
  525`) and no-external-scratch/arrival-order rules (`binary-format-v4.md:1600-
  1668`). It introduces no new public behavior or product decision; the first
  encoder chunk is an internal, cross-language testable prerequisite.

### 2026-07-24 - canonical range-page encoders

- Rust and Go now have private exact encoders for one already-canonical range
  leaf or branch (`v4/rust/iprange-livedb/src/range_page.rs` and
  `v4/go/internal/exactv4/range_page.go`). They write the fixed layout, zero
  unused bytes, and seal the completed page with CRC-32C.
- Both encoders validate all supplied records or child summaries before they
  touch the destination page. They reject invalid transaction IDs, capacity
  overflow, reversed/overlapping ranges, uncoalesced equal adjacency,
  membership value zero, invalid branch geometry, duplicate child pages,
  invalid fences, and invalid summaries.
- The branch encoder receives an inherited lower fence and an optional
  exclusive upper fence; no upper value represents the endpoint past the
  family maximum. This proves that every child record start belongs to that
  child. A record endpoint may still cross a later fence, as the format permits.
  Legal empty children remain encodable.
- This does not normalize unordered input, build a tree, change metadata roots,
  allocate temporary sorting files, or expose a public SDK API. It is only the
  safe page-writing primitive required by those later layers.

### 2026-07-24 - ordered range-tree builder plan

- The next private component is an ordered canonical-tree packer, not a second
  storage implementation. It accepts already-normalized records in increasing
  address order and writes each completed page through a narrow allocator-owned
  page sink. The real transaction allocator will own page selection, uniqueness,
  and rollback; the packer owns no free-list, page map, or temporary file.
- It retains one leaf record buffer and two branch buffers at each compact tree
  level. The second branch buffer is required because a completed branch cannot
  be sealed until the next branch supplies its exclusive upper fence. At input
  end it rebalances a singleton final group with its predecessor, then collapses
  a one-child root. This produces the compact canonical shape rather than a
  legal-but-redundant chain.
- Six branch levels are sufficient for writer-created compact trees: the
  smallest range-branch fanout is 50 (IPv6), `50^6` exceeds the maximum
  `u32`-addressable non-meta page population, and a canonical writer never
  creates an otherwise avoidable one-child branch. Readers will continue to
  accept the full format maximum depth for older/legal malformed-shape files.
- The workspace is fixed and supplied by the owning transaction/SDK layer;
  streaming records allocate neither heap storage nor an external sort/spill
  file. Every record is rechecked across leaf boundaries for family/order,
  overlap, and equal-value adjacency. Sink failure leaves the private draft for
  the existing whole-operation abort path.
- Tests will construct leaf, branch, and multilevel trees in both languages,
  reopen them through the existing readers, prove final-group rebalancing and
  root collapse, check cross-leaf rejection and sink-bound errors, and measure
  zero allocations after workspace construction. This remains below public
  `AddRanges`; the later sequential assignment engine alone will accept
  unordered overlapping input in arrival order.

### 2026-07-24 - ordered range-tree builder implementation

- Rust (`v4/rust/iprange-livedb/src/range_builder.rs`) and Go
  (`v4/go/internal/exactv4/range_builder.go`) now contain matching private
  bounded packers for an already-canonical ordered range stream. They use the
  existing exact leaf/branch encoders and return only the root page, root level,
  and exact record count required by a later meta-page writer.
- The narrow page-sink contract receives a complete page and returns an
  allocator-selected page number. The sink must preserve the page before it
  returns; it remains responsible for uniqueness, physical authorization, and
  whole-draft rollback. The packer owns none of those concerns and is not yet
  connected to the existing private-page pool, whose owner taxonomy does not
  yet include range-tree pages.
- A caller-owned fixed workspace retains one leaf, one pending and one current
  branch group at each of six levels, plus one reusable page buffer. It has no
  vector, map, spill file, or per-record allocation. Input is rechecked across
  leaf boundaries for reversed ranges, overlap, equal-value adjacency, and
  membership zero; any failure poisons the builder so its enclosing draft must
  be discarded.
- Completed branches wait for their next sibling's lower fence. At end of
  input, a singleton final non-root group borrows its predecessor's last child;
  one-child roots collapse to their child. This emits compact writer-created
  trees while preserving the reader's acceptance of every legal format shape.
- New private tests in both languages cover empty input, single-leaf roots,
  IPv4/IPv6 leaf splits, multi-level final-group rebalancing, reader reopening,
  cross-leaf rejection, sink failures and invalid returned pages, start bounds,
  and zero allocation after workspace construction. No public SDK API,
  metadata root, normalizer, temporary sort, or on-disk contract changed.

### 2026-07-24 - range page-pool sink plan

- The ordered packer currently proves only its narrow sink boundary. A direct
  audit of the shared private allocators found that Rust recognizes only
  `Bitmap` and `Retirement` owner bytes when it validates a candidate terminal
  page (`private_page_pool.rs:4066-4094`), while Go likewise permits only those
  owner/origin pairs (`private_page_pool.go:20-33`). Reusing either label for a
  range page would make allocator ownership evidence false.
- Add one private `Range` owner in both implementations. Rust's owner tag is
  the address family (`4` or `6`) and its pool validation must require the
  matching range page type and `aux`. Go adds the matching `Range` origin.
  These are transaction-private bookkeeping values, not portable v4 bytes or
  public identifiers.
- Add a checkpoint-bound adapter implementing the ordered packer's existing
  page-sink boundary. It verifies that the requested born transaction equals
  the pool's pending transaction; each write claims the lowest authorized page
  as `Range`, copies the complete CRC-sealed page into that actual pool slot,
  and returns its physical page number. It retains no parallel page store,
  heap journal, or external scratch.
- The pool checkpoint remains the sole cleanup owner. A claim/write failure
  leaves the checkpoint for the caller's existing whole-draft rollback; a
  successful adapter build is still private and unpublished. This chunk does
  not invent range-root publication, allow direct mutation, or bypass the
  allocator's later fixed-point/retirement work.
- Tests will prove exact pool ownership/type/family checks, direct range-tree
  construction into real pool bytes, rollback after a partial build, capacity
  failure without a hidden alternate store, and zero allocations after fixed
  storage construction in both languages.

### 2026-07-24 - range page-pool sink implementation

- Added the private `Range` owner in both page pools. Rust tags it with `4` or
  `6`, and its terminal-page proof now requires that same family in both the
  range-page header and `aux`; Go uses its matching private `Range` origin.
  No physical identifier or owner value reaches a public API or v4 bytes.
- Added checkpoint-bound range-page sinks in Rust and Go. Each builder output
  claims the lowest authorized pool page, copies the finished 4 KiB page into
  that exact slot, and returns the physical page number to the builder. A
  constant-time checkpoint capability check happens before the claim, so a
  stale token cannot strand a page; it does not inspect a file or validate a
  tree.
- Rust's pool requires checkpoint epoch headroom before streaming begins. The
  adapter therefore preflights its already-fixed pool capacity (three steps per
  possible page plus two boundaries), which reserves no memory and prevents a
  late counter failure during a bounded build or its rollback. Go's existing
  checkpoint accounting performs the equivalent check per mutation.
- The Go sink boundary now passes the reusable fixed `[4096]byte` page directly
  rather than a slice. This removes a needless conversion at the direct pool
  write boundary. The work remains private and unpublished: no metadata root,
  feed API, normalizer, scratch file, or live writer behavior changed.

### 2026-07-24 - page-backed sequential-assignment engine plan

- The old experimental external-sort implementation is not reusable: it made
  normal ingestion depend on spill files and still became quadratic for nested
  overwrites. The current exact-v4 requirements instead demand arrival-order
  semantics, no external sort, and bounded non-quadratic work
  (`binary-format-v4.md:1841-1850,3011-3018`).
- The private engine will use a sparse binary prefix tree over the fixed IPv4
  or IPv6 address width. Each node covers one binary address prefix and holds
  only its own checked arrival ordinal plus either an opaque `u32` assignment
  or a clear marker. A later whole-prefix assignment writes that node directly;
  older descendant tags remain harmless because the final walk selects the
  highest ordinal on each path. A partially covered interval descends only its
  two boundary paths and their bounded canonical cover, rather than scanning
  every older final interval. This is a standard MSB-prefix-trie mechanism;
  the checked reference was `cilium/cilium @ 4d55bb2db33c`,
  `pkg/container/bitlpm/trie.go:1-115`, adapted here for range assignment and
  fixed private storage rather than Cilium's heap-backed longest-prefix lookup.
- Nodes are fixed 32-byte records packed into actual transaction-private 4 KiB
  pool pages. A caller-owned private workspace retains only the page
  identities/counts, a bounded 32/128-level traversal, a pending output record
  for adjacent-value coalescing, and explicit assignment/page/work/mutation
  limits. No node map, input list, output list, temporary file, or per-record
  heap allocation exists. Pool pages have a distinct private `Normalization`
  owner and must all be returned before terminal publication; a leaked working
  page is therefore rejected instead of becoming an unvalidated live page.
- Each accepted direct/membership assignment receives the next nonzero `u64`
  ordinal after endpoint and value-kind checks. Clear is an internal absence
  marker, so direct value zero remains valid while membership zero remains
  rejected. Ordinal/work/page exhaustion or any pool write failure marks the
  private engine failed and leaves its active checkpoint for the existing
  one-path rollback. The later writer-transaction integration must consume that
  failed state and perform the required whole-draft abort before publication.
- Finalization walks the sparse tree in address order, resolves ancestor versus
  node ordinals, coalesces equal adjacent effective values, and immediately
  feeds the existing ordered range-tree builder and its real pool sink. Thus
  the normalized result is written straight into its final v4 range pages; the
  normalizer never materializes a separate result feed or sorted record array.
  This first slice remains private and generic over direct/membership value
  rules. Catalog interning, five membership operations, high-level workflows,
  retention, metadata, and public SDK methods remain later work.
- Tests will prove empty input, partial overwrite preservation, clear semantics,
  nested alternating ranges, an IPv6 full-space boundary, zero direct values,
  membership-zero rejection, deterministic per-address arrival-order output,
  final-tree reopening, page/work exhaustion rollback, and linear/bounded work
  counters under doubling nested input. They will also prove zero allocation
  after private workspace construction in both languages.

### 2026-07-24 - page-backed sequential-assignment implementation

- Added private Rust and Go sparse-prefix normalizers. Each consumes arrival
  order directly, stores only fixed 32-byte nodes in `Normalization` pool pages,
  and emits the final canonical range stream directly to the existing range
  page-pool sink. It creates no input-sized heap collection, sorted record
  array, or temporary file.
- `Value` and `Clear` are distinct internal tags. Direct value zero is retained;
  membership zero fails before a page claim. Empty input emits root zero without
  claiming a normalizer or range page. Equal adjacent final values coalesce.
- A failed input/finalization makes the engine non-finalizable. This prevents a
  caller from ignoring the error and asking this component to emit its partial
  state. The component is not yet attached to the writer transaction core, so
  that core still needs to convert its failed state into the required
  whole-draft abort/poisoning behavior before any public API uses it.
- Rust rejects any live `Normalization` page as a terminal page owner. Go now
  recognizes the matching private owner/origin so normalizer pages can use the
  common pool safely; terminal fixed-point integration remains later work.
- No normative specification update is needed: this private implementation
  realizes the existing ordered-normalization contract without adding a public
  API, default validation, or new v4 bytes.

### 2026-07-24 - transaction abort-latch plan

- The new normalizer records its own failed state, but it has no live writer
  transaction owner yet. The existing cores only poison drafts through
  operation-specific paths: Go `writer_transaction_core.go:277-295,601-643`
  and Rust `writer_transaction_core.rs:1134-1186`. A generic failed component
  therefore cannot currently make the enclosing transaction uncommittable.
- Add one private, handle-checked `requireAbort`/`require_abort` operation to
  the Go and Rust transaction cores. It is valid only for a pending,
  pre-publication draft. It marks both the core and its underlying private page
  pool abort-required, so fresh core calls and already-borrowed pool
  capabilities reject further work. `Abort` remains the sole recovery path.
- The latch must not reinterpret a stale handle, an incomplete abort, an
  outcome-unknown transaction, or a committed-cleanup transaction. Those
  states retain their existing typed result and are never changed to an
  abortable draft.
- Tests in both implementations will prove an explicit latch blocks draft
  access and commit preflight, rejects a pool operation held before the latch,
  aborts every private slot, invalidates the old handle, and permits a fresh
  transaction afterward. This is a transaction-private prerequisite only: the
  later normalizer/workflow integration will call it on every normalizer error;
  this slice does not claim that integration or expose an SDK method.

### 2026-07-24 - transaction abort-latch implementation

- Go and Rust transaction cores now expose one crate/package-private,
  handle-checked abort latch. It accepts only an active pre-publication draft,
  marks the core abort-required, and marks the underlying private pool
  abort-required. This blocks both a later commit attempt and a pool capability
  that was borrowed before the latch.
- Rust keeps the latch unavailable once publication could be in progress and
  preserves its existing `OutcomeUnknown` and committed-cleanup results. Go
  preserves its stale-handle boundary and has no publication phase in this
  private core. In both implementations only whole-draft `Abort` restores
  reusability and invalidates the old handle.
- This creates the required transaction safety boundary for later normalizer
  integration. The normalizer is still not handed live writer allocation or
  range-root authority, so no public workflow is claimed and no partial
  normalizer result can yet reach publication.

### 2026-07-24 - logical range-page staging plan

- The first sequential-assignment implementation writes its final range pages
  directly through `RangeTreePoolSink`, which returns final physical page
  numbers. That is valid for its isolated pool tests but cannot be the live
  transaction path: the core attaches a coordinator before exposing its draft,
  raw allocation/checkpoint access is then deliberately rejected, and the
  format requires physical page identities to be selected only during the
  lock-bound allocator/retirement finalization
  (`binary-format-v4.md:978-1025,1206-1221`).
- Add a private, fixed-capacity logical range-page staging sink in Rust and Go.
  The existing ordered builder receives temporary IDs `2..N+1`, stores its
  already CRC-sealed pages in caller-owned fixed slots, and returns a logical
  root. These IDs are never v4 file page numbers, reader results, or public
  API values.
- Add a one-pass materializer that accepts allocator-chosen, strictly
  increasing physical page assignments in that same logical-page order. It
  replaces every branch child temporary ID with its assigned physical page,
  reseals the CRC, fills `Range` terminal-page ownership, and returns the
  physical root/count for the later coordinator terminal bind. It rejects a
  missing/duplicate/out-of-order assignment, invalid staging header/CRC,
  unknown child ID, or final-page-count overflow before any terminal page is
  handed to the coordinator.
- This stage uses no external file, no heap allocation after caller workspace
  construction, and no physical pool mutation. The existing direct pool sink
  remains a low-level test adapter; it is not the live normalizer path. The
  following slice will move normalizer node storage and final emission onto
  this staging boundary, then bind the materialized result through the existing
  transaction coordinator and range-root target.
- Tests must cover empty/single/multilevel IPv4 and IPv6 trees, non-contiguous
  physical assignments, every remapped child reference, CRC resealing,
  rejection atomicity, zero post-setup allocations, and reader reopening only
  after physical materialization. No public SDK, metadata root, default
  validation, or portable v4 byte is changed by this internal bridge.

### 2026-07-24 - logical range-page staging implementation

- Added matching private logical staging sinks in Rust
  (`v4/rust/iprange-livedb/src/range_staging.rs`) and Go
  (`v4/go/internal/exactv4/range_staging.go`). The ordered builder receives
  only temporary IDs and stores each sealed 4 KiB page in fixed caller-owned
  slots.
- The materializers accept strictly increasing allocator-selected physical
  assignments, rewrite every branch child ID, reseal branch CRCs, and produce
  the existing `Range` terminal-page representation. Rust produces the
  coordinator journal type directly; Go produces the equivalent fixed-point
  terminal page. Neither path binds a live pool slot or publishes a root.
- Preflight rejects invalid capacity/result/assignment/output state, physical
  bounds or ordering, bad staging headers or CRCs, bad page geometry, and
  unknown or forward logical child references before changing terminal output.
  It deliberately checks only the internal staged-page facts needed for safe
  translation; it does not perform default file validation or rescan every
  range record.
- The direct pool sink remains untouched as isolated test infrastructure. The
  normalizer is still not integrated with this staging sink, coordinator, or
  metadata root, so no partial normalizer result can publish through this
  slice.

### 2026-07-24 - normalizer logical-workspace and staging plan

- Evidence: `SequentialAssignmentEngine` still claims `Normalization` pages
  from the raw private pool for its temporary sparse-prefix nodes
  (`v4/rust/iprange-livedb/src/sequential_assignment.rs:193-705` and
  `v4/go/internal/exactv4/sequential_assignment.go:128-546`). That conflicts
  with the deliberate post-coordinator boundary: raw checkpoint/allocator
  operations are rejected after a draft attaches its coordinator, while final
  range-page identities may be selected only in the later lock-bound
  finalization (`binary-format-v4.md:978-1025,1206-1221`).
- Replace each normalizer workspace slot with a caller-owned fixed page of
  packed node bytes plus its initialized-node count. Node references remain
  logical `(workspace page, node)` coordinates; no normalizer node has a file
  page number, pool owner, checkpoint authority, or publication path. The
  constructor will retain the fixed resource budgets and reject zero birth
  generations, occupied workspaces, invalid value kinds, and oversized logical
  workspace counts before input is accepted.
- Finalization will write only through `RangeTreeStaging`, using its temporary
  logical-page bound, and return its sealed logical result. It will clear the
  normalizer workspace on successful staging but retain the sealed range
  staging pages for the later coordinator materialization. A failed input or
  staging build remains poisoned; the enclosing whole-draft abort path will
  explicitly scrub both caller-owned workspaces before reuse.
- Add private abort-discard helpers for staged pages and normalizer nodes. They
  erase only caller-owned transient memory; they do not validate a file, touch
  physical page allocation, publish a root, or make an error recoverable
  without the required whole-draft abort.
- Tests will preserve arrival-order/direct-zero/membership/IPv6/oracle/work
  bounds and add proof that normalizer staging uses no private pool slots,
  keeps logical output unpublished until materialization, rejects an occupied or
  exhausted fixed workspace, scrubs on explicit abort, and allocates nothing
  after fixed workspace construction. The direct pool sink remains separately
  tested; it is removed from this live-normalizer boundary.

### 2026-07-24 - normalizer logical-workspace and staging implementation

- Reworked the private sequential-assignment engines in Rust and Go so their
  sparse prefix nodes live in fixed caller-owned 4 KiB logical workspace pages.
  They retain only logical node coordinates and initialized-node counts; they
  no longer accept a pool/checkpoint, claim a physical page, borrow a physical
  page, or perform an allocator/checkpoint validation during normal input.
  Workspace setup checks only fixed-slot occupancy; every node slot is fully
  encoded before its first read, so it does not scan caller memory as file
  input.
- Finalization now writes only to `RangeTreeStaging`, uses the staging logical
  page limit, and returns a sealed `RangeTreeStagedResult`. Successful staging
  clears the normalizer node workspace; the logical range pages remain retained
  for later materialization. Failed input or output poisons the normalizer and
  stays unusable until the enclosing draft aborts and calls the private discard
  helpers.
- Added matching staging abort-discard helpers and Rust staged-result accessors
  needed by the later terminal handoff. The helpers erase only unpublished
  caller-owned pages and deliberately leave the stale staging object finished.
- Removed the obsolete `Normalization` physical-page owner/origin from both
  private pools. The only former caller was the normalizer, so this closes the
  raw physical-page route rather than leaving an unused bypass for a future
  caller.
- The direct range-pool sink remains independently tested as a low-level
  adapter. This slice does not attach a normalizer to a transaction core,
  reserve final physical assignments, bind a range root, or publish any file
  state.

## Validation

### 2026-07-24 - logical range-page staging validation

- New Rust and Go tests cover empty, single-leaf, and multilevel IPv4/IPv6
  trees; non-contiguous physical mappings; all branch child rewrites; CRC
  resealing; reader reopening only after translation; and zero allocation after
  fixed workspace setup.
- Both suites prove failure atomicity for duplicate and missing assignments,
  invalid final page counts, invalid logical children, and a corrupted staging
  page: the terminal output remains untouched on every rejected input.
- `go -C v4/go test ./... -count=1`, `go -C v4/go vet ./...`, and
  `go -C v4/go test -race ./internal/exactv4 -count=1` pass. Rust passes 491
  tests without optional features and 612 with all features; formatting,
  warnings-denied all-target/all-feature Clippy, and all-feature benchmark
  compilation pass.
- This is private staging only. No normative specification, public SDK,
  default-validation behavior, or portable v4 byte changed. The next pending
  step remains moving normalizer output and workspace ownership to this
  boundary, then connecting it to the live coordinator and range-root target.

### 2026-07-24 - normalizer logical-workspace and staging validation

- New Rust and Go normalizer cases prove arrival-order overwrite/clear
  semantics, direct zero, membership-zero rejection before a node write, empty
  input, full-space IPv6, a per-address arrival-order oracle, coalescing,
  bounded nested work, occupied/exhausted workspace rejection, explicit abort
  scrubbing, and zero allocation after fixed setup.
- New end-to-end private cases build an oversized canonical IPv4 stream through
  the normalizer into three logical range pages, materialize it with sparse
  physical assignments, verify branch child rewrites and CRC resealing, and
  prove that the normalizer's logical root is not a physical file page before
  materialization.
- Same-failure search finds no remaining normalizer use of raw private-pool
  allocation, checkpoint authority, or `Normalization` page owner/origin in
  either language. The direct range-pool sink remains separately covered.
- `go -C v4/go test ./... -count=1`, `go -C v4/go vet ./...`, and
  `go -C v4/go test -race ./internal/exactv4 -count=1` pass. Rust passes 494
  tests without optional features and 615 with all features; formatting,
  warnings-denied all-target/all-feature Clippy, and all-feature benchmark
  compilation pass.
- No public API, metadata root, default file validation, physical page
  allocation before the lock, or portable v4 byte changed. The next required
  slice is transaction-core integration: bind allocator-selected range terminal
  pages through the existing coordinator and update the private range-root
  target, with whole-draft abort on every post-mutation failure.

### 2026-07-24 - staged range payload finalization plan

- The sealed normalizer result already materializes to a strict, unbound
  `Range` terminal-page journal in both engines, but it has no reservation-scope
  owner yet (`range_staging.rs` and `range_staging.go`). Conversely, the
  free-bitmap planner deliberately reserves caller-declared payload capacity,
  but finalization currently retains only bitmap and retirement-owned slots
  (`bitmap_cow/selective_finalization.rs:2517-2540` and
  `bitmap_finalize.go:2783-2977`). Leaving the two paths unconnected would
  return a materialized range page to the free bitmap.
- The next internal sequence is therefore fixed by the existing allocator
  contract, not a new public design choice: (1) merge independently produced
  terminal journals with no sorting or allocation; (2) under the existing
  lock-bound shadow reservation, consume exactly the staged range-page count
  from the payload capacity, materialize into those ordered physical pages,
  and mark the same shadow slots `Range`; (3) retain those range slots through
  bitmap finalization and combine range, bitmap, and retirement journals before
  the coordinator binds them; and only then (4) extend the private target-meta
  handoff with the materialized range root/count.
- Start with the journal merger as a separate no-mutation milestone. It will
  accept only individually strict unbound journals, reject duplicate physical
  pages and dirty/wrong-sized output before writing it, and merge in linear
  time using caller-owned output. Rust currently has a retirement-plus-bitmap
  merger in `retirement_writer.rs`; Go hard-codes the same two-way merge in
  `writer_fixed_point_aggregate.go`. Replacing those with one shared private
  primitive removes the two-source assumption before a third `Range` source is
  introduced.
- The following payload bridge will use the free-bitmap reservation's existing
  available-slot order, verify its exact physical ordering, and never sort or
  allocate. Any error before a shadow slot is claimed leaves the attempt
  reusable. Any error after a shadow mutation is an explicit whole-draft
  abort/discard condition, matching the transaction rule already recorded in
  sections 13 and 14 of `binary-format-v4.md`.
- This plan adds no public operation, on-disk field, default validation, or
  scratch file. Tests must cover two- and three-source ordering, duplicate and
  dirty-output rejection without output mutation, zero post-setup allocations,
  and later exact range-slot retention through finalization.

### 2026-07-24 - terminal journal merger implementation and validation

- Rust and Go now share the same fixed three-source private merge boundary for
  `Range`, `Bitmap`, and `Retirement` terminal journals. It accepts only strict
  ascending source streams, requires an exactly sized empty caller output,
  rejects cross-source duplicate physical pages before touching that output,
  and then performs a linear merge with no sorting or heap allocation.
- The existing retirement-plus-bitmap paths now delegate to that primitive;
  their caller-visible behavior and error mapping remain unchanged. The merger
  deliberately verifies only terminal identity, owner, physical order, and
  output state. It does not decode page payloads or turn ordinary commit into
  implicit file validation.
- New tests in both implementations cover three-source ordering, duplicate
  rejection, dirty/wrong-sized output rejection with byte-for-byte unchanged
  caller storage, and zero allocations after setup. The pre-existing
  retirement/fixed-point tests continue to cover the bitmap-plus-retirement
  integration path.
- `go -C v4/go test ./... -count=1`, `go -C v4/go vet ./...`, and
  `go -C v4/go test -race ./internal/exactv4 -count=1` pass. Rust passes 496
  tests without optional features and 617 with all features; Rust formatting,
  warnings-denied all-target/all-feature Clippy, and all-feature benchmark
  compilation pass. `git diff --check` and `./.agents/sow/audit.sh` pass.
- Same-failure search confirms that the old hard-coded two-source merge exists
  nowhere in the active Go or Rust fixed-point paths. This checkpoint still
  does not reserve or retain range payload pages, update a range root, or
  publish a file; those remain the next bounded implementation slice.

### 2026-07-24 - range payload reservation and retention implementation plan

- The current bitmap reservation already holds a fixed payload budget under
  the finalization lock. Its available-slot order is deliberately the reverse
  of physical page order: consuming from its tail yields ascending physical
  page numbers, exactly the order required by the staged range-tree terminal
  journal. The staged range builder already verifies only its own pages and
  can patch those selected numbers without opening or validating the file.
- The next checkpoint is private only. In both engines, a helper attached to
  the lock-bound bitmap reservation will: validate an exact caller-owned
  range-payload scratch set; select exactly the staged page count from the
  payload budget; prove slot identity, authorization, epoch, availability,
  and ascending physical order; materialize the range tree into a clean
  unbound terminal journal; and claim the same shadow slots as `Range` using
  the pool's prepared checkpoint suffix. It will then reconcile the bitmap
  scope so those slots cannot be selected as free again.
- The helper must return a retryable error until the first prepared shadow
  mutation. A failure after that point is explicitly classified as discard:
  the caller must abandon the whole shadow attempt and use the existing outer
  draft-abort/retry path. It must never create a temporary file, sort page
  numbers, allocate heap memory after workspace setup, or add default reader
  or writer validation.
- Rust bitmap terminal finalization will recognize only a current-transaction
  `Range` page with the valid IPv4/IPv6 tag (`4`/`6`) as retained payload. Go
  carries the equivalent private owner/origin pair, installed only after its
  range materializer has verified the page geometry. Neither treats a range
  page as bitmap output. This preserves the free-page bitmap invariant while
  keeping the range journal separate for the already-committed three-source
  merger.
- This checkpoint stops before the coordinator/root handoff. It neither
  updates target metadata nor publishes a root; the following checkpoint will
  combine the retained range journal with bitmap/retirement output and extend
  the private target-meta handoff.
- Tests will prove exact lowest-page selection and strict journal ordering,
  IPv4 and IPv6 materialization, zero-page input, insufficient/dirty scratch
  rejection without pool/output mutation, post-setup zero allocations, range
  retention through bitmap finalization, and the explicit post-mutation
  discard classification. Rust changes are expected in `range_staging.rs`,
  `bitmap_cow.rs`, and `bitmap_cow/selective_finalization.rs`; Go changes are
  expected in `range_payload.go`, `range_staging.go`, and
  `private_page_pool.go`, with focused internal tests in each engine.

### 2026-07-24 - range payload reservation and retention implementation

- Implemented the private bridge in both engines. A completed logical range
  tree now claims exactly its lowest reserved shadow-pool slots under the held
  scope, patches those page numbers into its caller-owned terminal journal,
  and consumes the matching payload budget. No public SDK API, file format,
  default validation behavior, temporary file, or sorting path changed.
- The claim is protected by exact scope, page, vacancy, and slot-epoch proof.
  All normal errors finish before the first pool mutation. The only later
  reconciliation failure is marked as a discard path and latches the Rust or
  Go shadow draft so it cannot be reused or committed.
- Empty staged trees are a true no-op: no checkpoint generation, scope state,
  page budget, or terminal output changes. Range pages survive bitmap
  finalization but remain separate from bitmap terminal output and the later
  three-source terminal-journal merger.
- Added focused Rust and Go tests for lowest-page selection, nonascending
  allocator order, dirty terminal scratch, stale slot epoch, wrong transaction,
  empty-tree no-op, finalization retention, and zero allocations after caller
  workspace setup. Existing range-staging tests continue to cover IPv4 and
  IPv6 page materialization and multilevel child remapping.
- Validation passed: `go -C v4/go test ./... -count=1`, `go -C v4/go vet
  ./...`, `go -C v4/go test -race ./internal/exactv4 -count=1`, `cargo test
  --manifest-path v4/rust/Cargo.toml` (503 tests), `cargo test
  --manifest-path v4/rust/Cargo.toml --all-features` (624 tests), Rust format,
  Clippy with warnings denied, benchmark compilation, `git diff --check`, and
  `.agents/sow/audit.sh`.

### 2026-07-24 - range-root retirement prerequisite plan

- A materialized replacement range root cannot be handed to target metadata by
  itself. Replacing a committed range root makes every page in the selected old
  range tree unreachable. The v4 contract requires every such page to enter
  the reader-protected retirement batch or the directly-free set; it cannot be
  reused by the same draft (`binary-format-v4.md:935-1025`). The current Rust
  and Go fixed-point target handoffs update only allocator and retirement
  fields, so adding `range_root` there now would leak old range pages.
- An old range tree is traversed in key order, not necessarily physical page
  number order. A valid file is not required to have range pages allocated in
  that traversal order, while a retirement blob requires strictly increasing,
  unique physical page numbers (`binary-format-v4.md:1045-1085`). Collecting
  all old pages in a `Vec`/slice then sorting would reintroduce the
  input-sized-memory defect; an external sort file is forbidden.
- First add a private fixed-page `u32` ordered set in Rust and Go. It will use
  caller-provided logical 4 KiB workspace pages, contain dense sorted leaf
  entries and bounded branch pages, accept arbitrary insertion order with set
  semantics, and visit its values in strictly increasing order. All workspace
  capacity is explicit before input; an insufficient budget rejects before the
  insertion changes state. It creates no file, physical page identity,
  terminal-journal entry, heap growth after setup, or public API.
- The following slices will make a narrow range-tree ownership walker insert
  each selected old range page into that index, teach the retirement blob
  builder to consume its sorted stream without a full slice, and only then
  bind the new range root/count plus old-page retirement into the existing
  lock-bound fixed point. This order prevents a partial root handoff from
  being mistaken for an implementation milestone.
- Tests for this first prerequisite cover arbitrary/permuted insertion,
  duplicates, ascending traversal, dense and sparse growth, exact
  pre-mutation capacity rejection, stale-workspace rejection and scrub, and
  zero allocations after fixed workspace setup. No default validation,
  portable byte, or public SDK behavior changes in this slice.

### 2026-07-24 - range-retirement ordered-index implementation and validation

- Added matching private `PageNumberIndex` implementations in Rust and Go.
  Each uses caller-provided logical 4 KiB pages, dense sorted `u32` leaves,
  and bounded branch pages. Three branch levels cover the complete `u32`
  page-number space. It accepts arbitrary order, preserves one copy of each
  number, and visits the result in strictly increasing order.
- Every insert calculates the exact split pages before changing a node. A
  one-page workspace that has a full leaf rejects the next insert with a typed
  capacity error and byte-for-byte unchanged workspace/index state. Abort
  scrub clears only this unpublished workspace and makes it reusable.
- The normal insertion path checks only constant-time private-workspace shape
  and bounds facts. It does not rescan a node for global ordering or act as
  implicit file validation; the index is private state created by the active
  writer. Binary branch selection keeps large index growth logarithmic rather
  than adding a hidden scan of each full branch.
- Focused tests in both engines cover permuted inputs, duplicates, dense
  reverse growth, a full branch-root split into a second branch level,
  increasing replay, exact pre-mutation capacity failure, stale workspace,
  explicit scrub, and zero heap allocations after workspace construction.
- Validation passed: `go -C v4/go test ./... -count=1`, `go -C v4/go vet
  ./...`, `go -C v4/go test -race ./internal/exactv4 -count=1`, `cargo test
  --manifest-path v4/rust/Cargo.toml` (509 tests), `cargo test
  --manifest-path v4/rust/Cargo.toml --all-features` (630 tests), Rust format,
  warnings-denied Clippy, benchmark compilation, `git diff --check`, and
  `.agents/sow/audit.sh`.
- This checkpoint deliberately does not inspect an old range tree, change a
  retirement blob, update target metadata, or publish a file. It is the
  bounded sorted primitive required before any of those steps can be correct.

### 2026-07-24 - selected range-tree ownership-walk plan

- Add matching private walkers that receive one selected committed range root,
  its selected transaction/family/value mode/page limit, a committed-page
  source, a caller-owned fixed-height traversal scratch, a bounded work limit,
  and the ordered index above. The walker will add every reachable range branch
  and leaf page to the index, including children described as empty by a branch
  summary: those pages remain reachable and must retire with their root.
- The walk uses the ordinary-path checks already defined for v4: source
  access, page bounds, common header, range page type/family/geometry, branch
  child bounds, and exactly decreasing child level. Its stack is bounded by
  `MAX_TREE_LEVEL + 1`; work is explicitly capped so a malformed aliasing graph
  cannot turn a normal commit into unbounded traversal. It deliberately does
  not verify page CRCs, scan range records, prove fence/cross-page ordering, or
  prove global alias freedom. Those are explicit `Validate` responsibilities.
- A root-zero map contributes no pages only when its record count is zero. A
  nonzero root can legally contain all-empty subtrees and is still walked. Any
  source, local-structure, work-budget, or ordered-index failure returns a
  typed private error; later workflow integration will apply the existing
  whole-draft abort rule rather than publish a partial walk.
- This slice has no allocator mutation, range-root replacement, retirement
  blob change, target-meta update, or public API. Tests will cover empty roots,
  physical page numbers deliberately unrelated to key order, multilevel and
  empty children, wrong child type/level, bounded work failure, source failure,
  and zero post-setup allocations in both engines.

### 2026-07-24 - selected range-tree ownership-walk implementation and validation

- Added matching private Rust and Go walkers. They visit every selected
  reachable range branch and leaf, including legal empty children, and insert
  physical page numbers into the private ordered index. The resulting index is
  independent of range-key traversal order and is ready for later retirement
  blob streaming.
- Both use exactly `MAX_TREE_LEVEL + 1` caller-owned page buffers and frames.
  Each visited page consumes one explicit work-budget unit, so a malformed
  aliasing graph cannot cause an unbounded normal commit. Source failures,
  wrong page kind, family, geometry, level, root/count shape, or index capacity
  return typed internal errors before this slice changes allocator, metadata,
  or publication state.
- The walkers intentionally retain ordinary-path semantics: they do not check
  CRCs, scan range records, prove fences, or prove global alias freedom. Those
  checks remain the explicit `Validate` operation rather than a hidden
  per-commit cost.
- Focused matching tests cover arbitrary physical IDs and empty children,
  multilevel IPv4, IPv6, exact maximum depth, invalid child kind/level, work
  and source failures, empty-root/count rules, ordered-index output, and zero
  post-setup heap allocations.
- Validation passed: `go -C v4/go test ./... -count=1`, `go -C v4/go vet
  ./...`, `go -C v4/go test -race ./internal/exactv4 -count=1`, `cargo test
  --manifest-path v4/rust/Cargo.toml` (516 tests), `cargo test
  --manifest-path v4/rust/Cargo.toml --all-features` (637 tests), Rust format,
  warnings-denied all-target Clippy, all-feature benchmark compilation,
  `git diff --check`, and `.agents/sow/audit.sh`.
- This checkpoint deliberately does not build a retirement blob, update target
  range metadata, or publish a root. Those changes remain unsafe until the
  ordered index can be consumed directly by the existing bounded retirement
  writer.

### 2026-07-24 - ordered-index retirement-blob plan

- Add matching private `build from ordered index` paths to the Rust and Go
  retirement-blob writers. The existing slice-based helper remains intact for
  its current callers; the new path accepts the private page-number index
  directly and never creates a page-number slice or temporary file.
- The new path has two index passes. Before any arena checkpoint or draft
  mutation, it derives geometry from the index's declared count, verifies
  every visited page is in the selected committed range and strictly increasing,
  proves the visited count exactly matches that declaration, and checks output
  scratch and private-page capacity. Only then does it reserve private output
  pages. A second pass streams each value straight into one fixed 4 KiB blob
  leaf buffer, seals the leaf at its known boundary, and rechecks the exact
  count before branch construction.
- An invalid/private-index traversal must remain distinguishable from a bad
  retirement value stream. A count mismatch is a typed internal failure. Any
  error after reservation rolls the arena checkpoint back; later root-handoff
  integration will use the existing whole-draft-abort path. The index itself
  remains caller-owned unpublished scratch and is not reset on a retryable
  retirement-builder failure.
- Tests will cover sorted output from reverse index insertion, bounds rejection
  before arena mutation, too-small output scratch, exact metadata/root geometry,
  successful discard, and zero post-setup heap allocations. The slice path,
  v4 bytes, public SDK, default validation behavior, and publication path are
  deliberately unchanged in this slice.

### 2026-07-24 - ordered-index retirement-blob implementation and validation

- Added matching private Rust and Go retirement-blob builders that accept the
  `PageNumberIndex` directly. The established slice builder remains unchanged
  for its existing callers. The new path never creates a second input-sized
  page-number list and uses no temporary file.
- Each build first visits the index without an arena checkpoint, checks the
  exact declared count, strict order, selected-committed-page bounds, output
  scratch, and private-page capacity. It then reserves output pages and makes
  a second index visit that writes and seals one 4 KiB blob leaf at a time.
  It rechecks every streamed value and the final count before constructing
  branch pages. A later error rolls the private arena checkpoint back.
- Added typed internal distinction for a declared-count mismatch and an index
  traversal failure. These are not reader/writer default validation; they are
  checks on the transaction-private input used to create a required retirement
  object. A caller-owned index remains intact after a retryable builder error.
- Focused matching tests insert the input in reverse order, prove a two-leaf
  branch/blob with valid CRCs and exact values, prove invalid bounds and short
  output scratch leave the arena and caller output scratch unchanged, and prove
  successful build/discard performs zero heap allocations after setup. Go also
  exercises declared-count and failed-index rejection directly.
- Validation passed: `go -C v4/go test ./... -count=1`, `go -C v4/go vet
  ./...`, `go -C v4/go test -race ./internal/exactv4 -count=1`, `cargo test
  --manifest-path v4/rust/Cargo.toml` (518 tests), `cargo test
  --manifest-path v4/rust/Cargo.toml --all-features` (639 tests), Rust format,
  warnings-denied all-target Clippy, all-feature benchmark compilation, and
  `git diff --check`. The project SOW audit is rerun with the final checkpoint
  commit preparation.
- This remains an internal prerequisite. It has not changed range metadata,
  retirement-tree editing, allocator ownership, publication, v4 bytes, or any
  public API. The next slice must combine the old range-tree list with the
  existing fixed-point retirement/direct-free result before a new range root
  can be handed to target metadata.

### 2026-07-24 - bounded protected-set fixed-point preparation plan

- A range-root replacement starts with an old-tree page-number index, but the
  fixed point must repeatedly compare that seed plus newly discovered bitmap
  and retirement replacements. Turning the seed into a `u32` slice would
  reintroduce input-sized memory exactly where the ordered index was added to
  remove it.
- Add matching private index primitives in Rust and Go: an allocation-free
  in-order cursor, exact value-by-value equality, and an O(n) clone from one
  validated index into a clean caller-owned index workspace. Clone preflights
  the complete dense leaf/branch geometry from the source count before writing
  destination pages. A source/index failure scrubs the destination workspace;
  no transaction pool, terminal journal, target metadata, or file byte changes.
- The clone output will let the next slice retain a stable old-range seed while
  alternating two caller-owned candidate workspaces. Each preview can build a
  retirement blob directly from its current index, add newly discovered
  replacement pages to the alternate index, and test convergence exactly
  without a sort file, a full page-number slice, or probabilistic hashes.
- The cursor performs only private-index shape checks. It does not add reader
  or writer validation and does not inspect v4 data pages. Tests will cover
  differently shaped but equal indexes, mismatch detection, dense multi-level
  traversal, exact capacity rejection before destination mutation, source
  corruption cleanup, and zero allocations after workspace setup.

### 2026-07-24 - bounded protected-set fixed-point preparation implementation

- Added matching private Rust and Go index cursors, exact equality, and dense
  clone operations. The cursor has only the fixed maximum tree-depth stack;
  clone preflights its complete leaf/branch geometry and destination capacity
  before it writes a destination logical page.
- Equality drains both indexes even after it has found unequal values. This
  preserves a useful distinction: an unequal candidate is an ordinary `false`
  result, while a malformed private source anywhere in either stream remains a
  typed error rather than being hidden by an earlier mismatch.
- Clone requires a distinct clean caller workspace. It streams the source
  exactly once, writes dense leaf and branch pages directly, and scrubs the
  destination on any source failure after streaming starts. It does not
  allocate a page-number list, sort file, or heap traversal stack.
- Matching tests prove a split multi-level source clones into a differently
  shaped dense tree with identical values, unequal values are detected,
  delayed source corruption is not masked, exact capacity rejection changes
  nothing, a source failure scrubs a partially written destination, and the
  warmed clone/equality paths allocate no heap memory.
- This is private writer scratch only. It does not change public APIs, v4
  bytes, publication, reader/writer default validation, allocator ownership,
  target metadata, or transaction state. The next slice can use alternating
  index workspaces to form and compare bounded protected-set candidates.
- Validation passed: `go -C v4/go test ./... -count=1`, `go -C v4/go vet
  ./...`, `go -C v4/go test -race ./internal/exactv4 -count=1`, `cargo test
  --manifest-path v4/rust/Cargo.toml`, `cargo test --manifest-path
  v4/rust/Cargo.toml --all-features`, Rust format, warnings-denied all-target
  all-feature Clippy, all-feature benchmark compilation, `git diff --check`,
  and `.agents/sow/audit.sh`.

### 2026-07-24 - bounded protected-set convergence plan

- Add one private, caller-scratch-only convergence helper in both engines. It
  starts from the validated old-range index, alternates between two distinct
  clean index workspaces, clones the current candidate into the alternate
  workspace, and permits the preview to add only selected committed page
  numbers. Previous candidate pages therefore cannot disappear between
  iterations.
- The helper will validate the seed and every newly added page against
  `[2, selected page_count)`, use an explicit nonzero iteration limit, and
  compare complete ordered streams before accepting stability. A preview
  failure, malformed index, capacity failure, or convergence-limit failure
  scrubs both candidate workspaces while preserving the old-range seed.
- This is deliberately not the live bitmap/retirement preview yet. It changes
  no pool, arena, terminal journal, target metadata, or file bytes. The next
  integration slice will run existing detached bitmap/retirement previews
  through this helper and then materialize only the converged candidate.
- Tests will cover monotonic multi-step convergence, complete comparison after
  an early mismatch, invalid committed-page additions, callback failure and
  limit cleanup, seed preservation, and zero allocations after setup.

### 2026-07-24 - bounded protected-set convergence implementation

- Added matching private Go and Rust convergence helpers. They seed one
  candidate from the old range-page index, densely clone it into the alternate
  caller-owned workspace for each preview, and return only the workspace whose
  complete ordered page stream is stable. The protected set can therefore only
  grow across iterations.
- The preview receives an add-only capability, not a replacement index. It
  rejects both reserved pages and pages at or beyond the selected committed
  `page_count`; the seed is checked by the same bound before the first preview.
  Preview, index, capacity, and iteration-limit failures scrub both candidate
  workspaces. A malformed seed is marked failed by the existing private-index
  safety rule and is never used as a successful input.
- Added matched tests for three-step convergence, lower and upper bound
  rejection, arbitrary preview failure, limit cleanup, invalid-seed rejection
  before callback execution, seed preservation, and zero warmed-path heap
  allocation. Existing equality tests prove a later malformed page is still
  reported after an earlier mismatch.
- Removed one no-op Rust cursor `drop` from the preceding dense-clone helper;
  current Clippy rejects it and non-lexical lifetimes already end the borrow.
  This does not change byte, allocation, or transaction behavior.
- This remains private transaction scratch only. It does not yet connect live
  bitmap/retirement previews, range metadata, allocator ownership,
  publication, a public SDK API, or v4 bytes.

### 2026-07-24 - range-root transaction bridge plan

- The next missing behavior is now concrete. `stageRangePayload` /
  `RangeTreeStaging::reserve_payload` can materialize a new range root into
  private `Range` terminal pages, and the selected old root can be walked into
  a `PageNumberIndex`. Neither result may change target metadata alone: every
  selected old range page, every committed bitmap replacement, and every
  committed retirement-tree/blob replacement must enter the same monotonic
  protected set before the new root is eligible for publication
  (`binary-format-v4.md:935-1025`).
- The bridge will therefore be built as one private transaction proof, not as
  an empty-tree shortcut. It will retain: (1) the selected range-root identity
  and materialized replacement root/count, (2) the strict unbound `Range`
  terminal journal, and (3) the converged protected-page index. The proof is
  produced only after detached bitmap/retirement previews have added every
  selected committed replacement page to the index. A root handoff without
  that proof is rejected.
- Rust already exposes the necessary detached preview boundary in
  `bitmap_cow/selective_finalization.rs`, and its retirement blob builder can
  stream a `PageNumberIndex` directly. Go has the same finalization shadow
  state but currently keeps that discovery/replay sequence inside
  `freeBitmapReservationAttachment.finalize`; the first implementation slice
  must extract an equivalent private read-only preview there. It must preserve
  the current two-pass source fence and never mutate the live scope.
- Once both engines can produce the proof, the next slice will stage exactly
  one final retirement blob/tree from the converged index, merge Range, bitmap,
  and retirement journals through the existing three-source merger, and then
  authorize the target `range_root` / `range_record_count` update. The root
  handoff and terminal ownership must be one indivisible private result.
- This work adds no public API, format byte, implicit validation, temporary
  file, or special handling for an empty selected root. Tests will cover an
  old nonempty root, a legal empty selected root, replacement pages discovered
  over multiple iterations, direct-free versus reader-protected disposition,
  failed detached preview cleanup, and zero post-setup allocations.

### 2026-07-24 - detached Go bitmap finalization preview

- Added a private `previewTerminalReplacements` path for the Go bitmap
  attachment. It executes the existing two-pass, source-fenced finalization
  entirely in detached shadow storage and returns the exact terminal bitmap
  replacement-page sequence through caller-owned output. It never applies the
  shadow to the live pool, scope, COW state, terminal journal, metadata, or
  file bytes.
- Output capacity and ordinary slice-alias failures are rejected before any
  shadow work; later failures never write output. Preview always clears the
  cleanup overlay before returning, so the same finalization scratch can be
  retried after a source failure. The eventual range-root bridge can now use
  this preview as one fixed-point producer without mutating bitmap state.
- The reachable-empty-bitmap-root path had one stack temporary whose address
  escaped to the Go heap. It now reuses the shadow COW's fixed output page and
  clears it before encoding. This preserves the page bytes while making the
  legal-empty-root preview allocation-free after setup.
- New tests prove exact equivalence with real finalization, live-state
  immutability, output capacity and alias rejection, source failure cleanup
  and retry, and zero warmed-path heap allocations. Validation passed:
  `go -C v4/go test ./...`, `go -C v4/go vet ./...`,
  `go -C v4/go test -race ./internal/exactv4 -count=1`, and
  `cargo test --manifest-path v4/rust/Cargo.toml`,
  `cargo test --manifest-path v4/rust/Cargo.toml --all-features`,
  `cargo fmt --manifest-path v4/rust/Cargo.toml --check`,
  `cargo clippy --manifest-path v4/rust/Cargo.toml --all-targets --all-features
  -- -D warnings`, and `./.agents/sow/audit.sh`.

### 2026-07-24 - staged detached bitmap preview for the range-root bridge

- Extended the Go detached bitmap preview with a caller-owned staged attachment.
  This is deliberately not a raw pool callback: `stageRangePayload` also
  maintains the COW binding ledger, so a raw claim could look valid in the pool
  while leaving the detached COW inconsistent. The staged attachment preserves
  that ledger and copies its complete detached state back into the shadow before
  each finalization pass. It exposes only the detached COW and scope, never live
  reservation buffers, a reclamation ticket, or terminal work storage.
- A prospective range/retirement producer now runs once for discovery and once
  for replay, returns a comparable caller witness, and cannot mutate live state
  or publish output. A witness mismatch is rejected as a stale insertion plan;
  a producer failure remains distinct from a bitmap failure. The required stage
  scratch is supplied by the caller, cleared on every exit, and keeps the warmed
  preview path allocation-free.
- Tests exercise real `Range` payload staging through both passes, unstable
  witnesses, producer errors, missing stage scratch, source-failure cleanup and
  retry, live-state immutability, scratch reuse, and zero warmed-path heap
  allocation. Validation passed: `go -C v4/go test ./...`,
  `go -C v4/go vet ./...`, `go -C v4/go test -race ./internal/exactv4 -count=1`,
  `cargo test --manifest-path v4/rust/Cargo.toml`,
  `cargo test --manifest-path v4/rust/Cargo.toml --all-features`,
  `cargo fmt --manifest-path v4/rust/Cargo.toml --check`,
  `cargo clippy --manifest-path v4/rust/Cargo.toml --all-targets --all-features
  -- -D warnings`, `git diff --check`, and `./.agents/sow/audit.sh`.
- This remains private transaction plumbing: it changes no public SDK, v4 byte,
  metadata/root publication, implicit validation, or temporary-file behavior.
  The next slice connects this producer with selected-old-root collection and
  prospective retirement replacements to form the protected-page proof.

### 2026-07-24 - range-root proof construction plan

- The coordinator currently accepts only bitmap and retirement terminal
  producers; its three-way merger receives `nil` for the range source. This is
  intentional until one private proof binds three facts together: the selected
  old range-root identity, the materialized replacement range journal/root, and
  the converged protected-page index. Passing a materialized root alone would
  still permit an unsafe metadata handoff.
- The next bounded slice adds that proof in both engines. It will consume the
  existing old-range seed and alternating `PageNumberIndex` workspaces through
  the convergence helper, retain which candidate workspace converged, and
  verify that the strict unbound range journal exactly describes its materialized
  root/count and has no page in the selected committed protected set. The proof
  is private scratch, not target metadata authority.
- The preview remains a caller-supplied private producer at this boundary. The
  later composition slice will run actual detached bitmap/range/retirement work
  through it, stage one final retirement output from the proven index, and only
  then extend the coordinator's three-source merge. Separating proof formation
  prevents a synthetic or partial terminal journal from becoming publishable.
- Tests will cover a multi-step protected-set convergence, old nonempty and
  legal-empty selected roots, range-journal/root/count mismatch, overlap with a
  protected committed page, failed preview cleanup, stale candidate rejection,
  and zero warmed-path allocation. It changes no range metadata, terminal
  ownership, allocator state, file byte, public API, validation default, or
  temporary-file behavior.

### 2026-07-24 - range-root protected-page proof implementation

- Added matching private Go and Rust proof constructors. Each first walks the
  selected old range tree into caller-owned bounded index scratch, then runs
  the existing monotonic convergence helper. The returned proof retains the
  selected root identity, replacement materialized root/count, strict unbound
  `Range` journal, converged candidate identity, and a deterministic seal over
  all of that private state.
- The proof rejects a malformed replacement journal, a root not present in its
  journal, non-`Range` journal pages, and any new range page that would collide
  with a protected selected-generation page. Failed ownership walking or
  preview convergence scrubs all three caller-owned indexes; successful abort
  cleanup does the same. Rust keeps exclusive borrows of those indexes while
  the proof exists, and Go checks the same scratch identity and seal before a
  later consumer can use it.
- Added narrow visibility only where the Rust proof needs existing private
  index-range and unbound-journal checks; it introduces no new public API.
  Tests cover multi-step convergence from a nonempty tree containing a legal
  empty leaf, a legal empty selected root, malformed journal/root, protected
  overlap, preview failure/retry cleanup, post-creation scratch mutation, and
  zero warmed-path heap allocation in both engines.
- The proof remains intentionally incomplete transaction plumbing: it does
  not yet run actual bitmap/retirement preview producers through the fixed
  point, stage a retirement list, bind terminal ownership, modify target
  metadata, or publish a range root. Those are the next composition boundary.

### 2026-07-24 - range-root protected-page proof validation

- Focused Go and Rust tests pass for a selected nonempty tree containing an
  empty child leaf, a legal empty selected root, multi-step fixed-point growth,
  missing journal root, journal page-count mismatch, non-`Range` terminal
  owner, selected-page overlap, truncated old-tree source cleanup, preview
  failure/retry cleanup, stale-scratch rejection, and zero warmed-path heap
  allocation.
- Full gates pass: `go -C v4/go test ./... -count=1`, `go -C v4/go vet ./...`,
  `go -C v4/go test -race ./internal/exactv4 -count=1`, Rust's 534 default and
  655 all-feature tests, `cargo fmt --check`, warnings-denied all-target
  all-feature Clippy, `git diff --check`, and `./.agents/sow/audit.sh`.
- Same-failure search confirms the only active aggregate handoff still has no
  range terminal source and no target `range_root` update. This proof therefore
  cannot accidentally be mistaken for publication; the next slice must extend
  that one coordinator boundary rather than add a parallel route.

### 2026-07-24 - sealed range/bitmap terminal-journal split plan

- Concrete gap: a real range payload can already be staged into the exact
  bitmap-finalization scope (`range_payload.go` / `range_staging.rs`) and is
  deliberately retained by finalization. The live handoff, however, exports
  only bitmap-owned pages. Go rejects every non-bitmap owner while extracting
  its producer; Rust has only a bitmap exporter. A later range-root handoff
  would otherwise need to reconstruct or invent a second terminal journal.
- The next slice will consume that one sealed scope once and expose two
  private, typed, strictly ordered journals: `Range` and `Bitmap`. Both are
  derived from the same finalization nonce/authority. The range result is
  checked against the materialized root, record count, and page count before
  either journal becomes usable. A staged retirement-owned page remains a
  rejection at this boundary: its actual producer must be added with the
  retirement fixed-point stage, rather than silently omitted.
- Go will partition the existing bounded producer-page scratch after first
  validating/counting every sealed binding. It will not allocate or sort.
  Rust will add the matching typed range-scope exporter and use the existing
  bounded in-place journal ordering rule. Both paths must clear caller output
  on rejection and retain the existing whole-draft cleanup behavior.
- This is deliberately still not the root transaction. It does not consume
  the range-root proof, construct retirement pages, merge three journals,
  modify `range_root` / `range_record_count`, bind coordinator output, change
  file bytes, add a public API, enable implicit validation, or create a
  temporary file. Its only purpose is to make real range payload ownership
  available to that later indivisible composition boundary.
- Tests will stage a real one-page range tree into a real bitmap reservation,
  finalize it, prove that the two output journals are disjoint/ordered and
  retain exact owner bytes, reject mismatched materialization and unexpected
  owners without leaving a usable terminal authority, and preserve zero
  warmed-path allocation. Full Go/Rust format, lint, race, and SOW gates run
  before this checkpoint is committed.

### 2026-07-24 - sealed range/bitmap terminal-journal split implementation

- Go now finalizes one exact scope into a nonce-linked `Bitmap` producer and
  `Range` producer. The two journals share one existing caller-owned page
  buffer, partitioned only after a complete owner/count pass; no new producer
  allocation or sorting path was added. The range producer seals the
  materialized root/count/page-count and exact terminal bytes, and rejects a
  root or page outside the final pending-page range.
- The old bitmap-only caller remains explicit: it requests the canonical empty
  range result and discards the empty private range token. A live `Range` page
  on that older path is rejected rather than silently omitted.
- Rust now counts retained `Range` pages during the same finalization pass,
  exports them through a typed range-scope journal, and returns a paired
  range/bitmap terminal authority. The pair rejects a sealed scope containing
  a retirement page because its third journal is not yet present; it cannot
  silently publish only two of three owners. The range exporter reuses the
  existing in-place strict-order check and does not allocate.
- Tests use a real staged range page through actual finalization, verify exact
  bytes, ownership, shared finalizer authority, ordered merge, materialization
  mismatch cleanup/retry, and rejection of a scope containing retirement
  ownership. No target metadata, range-root proof consumption, retirement
  construction, three-source coordinator merge, public API, v4 byte, default
  validation, or temporary-file behavior changed.

### 2026-07-24 - sealed range/bitmap terminal-journal split validation

- Focused tests pass in both engines. They exercise one real range payload,
  exact range/bitmap owner separation, strict three-source journal merge,
  range-materialization mismatch with clean caller output and retry, and a
  retirement-owned page that the two-source pair rejects rather than omits.
- Full gates pass: `go -C v4/go test ./... -count=1`, `go -C v4/go vet ./...`,
  `go -C v4/go test -race ./internal/exactv4 -count=1`, Rust's 534 default and
  655 all-feature tests, `cargo fmt --check`, warnings-denied all-target
  all-feature Clippy, all-feature benchmark compilation, `git diff --check`,
  and `./.agents/sow/audit.sh`.
- Self-review confirms this change does not update target range-root metadata
  and that the only aggregate coordinator still consumes bitmap plus retirement
  journals; no accidental publication route was added. No normative spec,
  project skill, or end-user/operator documentation update is required because
  this is private, pre-publication transaction plumbing. The active SOW remains
  open for the actual retirement output and three-source composition.

### 2026-07-24 - range-root retirement and three-source composition plan

- The sealed range/bitmap pair is necessary but insufficient. A replacement
  range root removes the selected old range-tree pages from the generation. If
  an older reader can still observe that generation, those exact pages must be
  encoded in one new retirement batch before any root handoff. The current
  proof already owns the sorted protected-page index, but it does not retain
  the selected retirement root/count needed to safely edit that tree, and no
  production-shaped path consumes its index.
- Extend the private proof identity in both engines with the selected
  retirement root and batch count, including the existing zero-root/count and
  selected-page-bound checks. The proof seal will cover those fields, so a
  retirement edit cannot be paired with a proof from another selected meta
  state. This is transaction-local input checking, not default reader or
  writer validation.
- Add one private reader-protected staging helper. It will verify the proof,
  stream its converged index directly into the existing retirement blob builder,
  append that blob as the pending transaction's retirement batch inside the
  exact bitmap reservation scope, and resynchronize that scope before bitmap
  finalization. It will use only caller-owned blob/path/ledger/role/journal
  scratch and never materialize the protected set as a `u32` slice. Errors
  before scope mutation retain retry authority; an error after a private
  mutation is an explicit whole-draft-discard condition.
- Replace the current two-owner terminal boundary with a private three-owner
  boundary: one sealed scope yields strictly ordered `Range`, `Bitmap`, and
  `Retirement` journals, all tied to the same finalizer authority. It validates
  the range materialization and the retirement result/root before returning a
  combined journal. It does not accept caller-supplied terminal pages, sort,
  allocate, or omit an unexpected owner.
- The aggregate coordinator will be extended only far enough to retain and
  merge all three typed journals. It will keep the range proof/materialization
  private and will not update `range_root`, `range_record_count`, target meta,
  or file bytes in this slice. Direct-free disposition remains an explicit
  later composition path; it must not be silently treated as a retirement
  batch or vice versa.
- Matching tests will construct a nonempty selected range tree and a legal
  empty selected root, stage a real range payload plus retirement blob/tree in
  one scope, prove exact three-way ordering/ownership and zero post-setup heap
  allocation, reject selected-retirement identity substitution and short/dirty
  output scratch before mutation, and prove post-mutation failure requires the
  existing discard path. Full cross-language test, lint, race, format, and SOW
  gates run before the next checkpoint commit.

### 2026-07-24 - selected retirement identity checkpoint

- The private range-root proof now seals the selected retirement root and
  batch count in both engines. It rejects the same retirement invariants used
  by bootstrap: zero root/count disagreement, root outside the selected page
  extent, and more batches than the selected transaction permits. A later
  retirement-tree edit therefore cannot be paired with a proof made for a
  different selected meta state.
- Each proof now exposes only a checked private retirement state plus its
  converged protected-page index. It still has no allocator, metadata, file,
  or publication authority. The later composition must obtain its reader from
  the already-bound bitmap reservation, so no caller can substitute an
  arbitrary source at that boundary.
- Go briefly retained the generic source inside the proof, but that caused a
  heap allocation for value-backed sources and violated the existing warmed
  path allocation test. The implementation deliberately does not retain it:
  the bound bitmap reservation remains the sole live source authority in the
  later composition, while the proof seals the selected meta identity and
  protected pages.
- Focused proof tests and both default language suites pass: `go -C v4/go
  test ./internal/exactv4 -run TestRangeRootTransactionProof -count=1`,
  `go -C v4/go test ./... -count=1`, and `cargo test --manifest-path
  v4/rust/Cargo.toml`. `git diff --check` also passes. The next checkpoint
  remains the actual same-scope retirement stage and three-owner export.

### 2026-07-24 - proof-bound private range-root retirement staging

- Both engines now consume a sealed range-root proof only through the exact
  bound bitmap reservation. The selected old range pages remain in their
  page-backed protected index and are streamed directly into one retirement
  blob, then appended as the pending transaction's newest retirement batch in
  that same private scope. No input-sized page-number slice, alternate source,
  metadata change, range-root handoff, or file write is involved.
- The returned private stage seals the proof identity, selected and pending
  generations, reservation scope, retirement result, terminal-page count, and
  live pool state. Rust retains its private arena until the future terminal
  composition consumes it; Go keeps the equivalent caller-owned arena scratch
  sealed through the shared scope. A legal empty selected range root produces a
  valid zero-page stage.
- The mutation boundary is explicit in both engines. Short input scratch fails
  before mutation and can be retried. Once the retirement blob has entered the
  shared scope, any later failure poisons the whole draft; all later staging or
  verification attempts reject that poisoned scope. This prevents an error from
  being followed by a partial later commit.
- Permanent tests cover a real selected range tree, exact scope ownership,
  selected-retirement identity substitution, legal empty roots, zero warmed-path
  heap allocation, retryable short scratch, and a deliberately too-small scope
  that fails after blob creation and requires whole-draft abort in both engines.
- Validation passes: focused Go/Rust proof tests; `go -C v4/go test ./... -count=1`;
  `go -C v4/go vet ./...`; `go -C v4/go test -race ./internal/exactv4 -count=1`;
  `GOARCH=386 go -C v4/go test ./internal/exactv4 -count=1`; Rust default and
  all-feature tests; Rust formatting; warnings-denied all-target/all-feature
  Clippy; all-feature benchmark compilation; and `git diff --check`.
- This is still private transaction plumbing. The next slice must export the
  staged retirement pages and combine the sealed `Range`, `Bitmap`, and
  `Retirement` journals under one terminal authority before any later metadata
  handoff can be considered.

### 2026-07-24 - three-owner terminal composition implementation plan

- Extend each sealed-finalization result with an explicit retirement-owned page
  count. The existing range/bitmap pair remains strict and continues to reject
  a retirement-owned page; a new private triple path is the only path allowed
  to consume one.
- The triple path takes the already-sealed range-root retirement stage, not
  caller-supplied retirement pages or a caller-supplied edit result. It checks
  the proof-bound stage before finalization, then rechecks its exact scope,
  pending generation, retirement root/result, and three owner counts against
  the sealed output. It uses caller-owned typed buffers and the existing fixed
  three-source merger in physical-page order; it neither sorts nor allocates.
- The Go coordinator gains a strict triple producer/aggregate route. It mints
  linked range, bitmap, and retirement capabilities from the same finalizer
  authority, replays all three journals into the existing private coordinator
  scope, and retains the materialized range result only privately. The older
  bitmap-only route stays unchanged.
- Rust uses the same typed boundary before its existing generic prepared-work
  binder. The stage remains the sole authority for the retirement result, while
  the sealed finalizer remains the sole authority for every exported page.
- Tests must cover real nonempty and legal-empty selected roots, exact
  three-owner ordering, forged/stale stage rejection, short or dirty caller
  journals before terminal mutation, no warmed-path heap allocation, and the
  fact that the existing two-owner path still rejects retirement ownership.
  This slice does not update range metadata, target metadata, the file, or any
  public API.

### 2026-07-24 - three-owner terminal composition milestone

- Go and Rust now expose an internal, proof-bound three-owner terminal path.
  It accepts only the sealed range-root proof and its exact retirement stage,
  then carries `Range`, `Bitmap`, and `Retirement` pages through fixed
  caller-owned journals. The generic two-owner path remains separate and still
  rejects retirement ownership.
- A zero-page bitmap journal is legal only for an empty output root or for the
  exact root sealed at reservation time. A nonzero terminal bitmap root still
  requires an owned bitmap page. This preserves the legal case where a safely
  reclaimed page funds the range result without rewriting the selected
  free-page bitmap.
- Cross-language review exposed a real selection mismatch: Rust retained every
  proven reclaimed page before ordinary bitmap candidates, while Go could drop
  a later reclaimed page when a lower ordinary candidate filled the fixed
  scope. Go now matches the Rust contract: it rejects a reclaimed set larger
  than the private scope before live-pool mutation, retains every proven
  reclaimed page, and uses ordinary candidates or appended pages only for the
  remaining capacity.
- The Go coordinator keeps the range materialization and proof private through
  the aggregate record. Rust carries the matching typed provenance through its
  prepared terminal export. Neither engine writes file bytes, updates range or
  target metadata, exposes a public API, or claims a durable commit in this
  milestone.
- Regression coverage includes: nonempty three-owner output, dirty retirement
  scratch rejection before finalization, legal empty selected range roots,
  unchanged selected bitmap roots, strict physical journal ordering, forged
  proof rejection, oversized reclaimed sets, and deterministic 512/4096-page
  reclaimed-source selection.
- Validation passed: `go -C v4/go test ./... -count=1`; `go -C v4/go test
  -race ./... -count=1`; `GOARCH=386 go -C v4/go test ./... -count=1`; `go -C
  v4/go vet ./...`; `cargo test --manifest-path v4/rust/Cargo.toml
  --no-fail-fast`; `cargo test --manifest-path v4/rust/Cargo.toml
  --all-features --no-fail-fast`; `cargo fmt --manifest-path v4/rust/Cargo.toml
  --all -- --check`; `cargo clippy --manifest-path v4/rust/Cargo.toml
  --all-targets --all-features -- -D warnings`; `cargo check --manifest-path
  v4/rust/Cargo.toml --all-features --benches`; and `git diff --check`.

### 2026-07-24 - private target-metadata handoff plan

- The completed three-owner aggregate already proves one exact pending page
  count, free-bitmap root, replacement range root/count, and retirement
  root/count. Leaving any of those fields at the selected generation would
  make a future meta-page publication describe a mixed generation. The Go
  aggregate currently changes only `PageCount`; Rust changes allocator and
  retirement fields but carries no range result through its sealed aggregate.
- Before the first live aggregate mutation, each engine will derive and seal
  one private target-meta replacement from the exact terminal authority. The
  normal bitmap/retirement path preserves the selected range fields. The
  proof-bound three-owner path additionally replaces `RangeRoot` and
  `RangeRecordCount`; it may not supply either field independently.
- Terminal execution will install that prevalidated complete replacement only
  after the canonical coordinator record has accepted the same sealed pages.
  A substituted target base, invalid range result, forged proof/export, or
  target-preflight failure must reject before Active state and leave the live
  target unchanged. Commit preflight will retain the exact final private target
  state rather than checking only page count.
- Rust will carry an internal optional range-target contribution alongside the
  typed produced terminal export. It is populated only by the existing
  proof-bound three-owner exporter and is rechecked against the exact terminal
  range pages before the coordinator can bind them. Go will store the matching
  before/after target pair in its sealed aggregate slot.
- Tests will prove a nonempty replacement updates all five affected target
  fields together, a legal empty selected range root remains valid, normal
  non-range aggregation retains selected range metadata, and malformed or
  substituted range facts fail before target/live-pool mutation. This remains
  private transaction plumbing: no file byte, metadata page, sync, public API,
  implicit validation, or durability outcome is introduced here.

### 2026-07-24 - private target-metadata handoff implementation and validation

- Go now seals the exact target metadata before and after an aggregate with
  its producer authority. The normal bitmap/retirement path retains the
  selected range root and record count; the proof-bound three-owner path
  replaces range root/count together with page count, free-bitmap root, and
  retirement root/count. Execution rejects a substituted target before it
  consumes the prepared aggregate, and final fixed-point/commit fences retain
  that exact complete target.
- Rust carries an optional range contribution only from the proof-bound
  three-owner terminal exporter. It checks that contribution against the exact
  terminal range pages before binding. The core derives and stores one exact
  base-to-target handoff before its coordinator enters `Active`, then installs
  it only after canonical record acceptance. Completion, output preparation,
  and commit preflight all require the same target. A target update also
  enforces the retirement-count-versus-target-transaction metadata invariant
  already enforced by the Go implementation and bootstrap readers.
- Permanent tests cover nonempty and legal-empty range roots, malformed range
  facts before pool mutation, ordinary aggregates preserving a nonzero selected
  range target, all five changed target fields in one proof-bound aggregate,
  and substituted Go target metadata before consumption. The private Rust and
  Go paths remain bounded and allocation-free after their fixed workspaces are
  established.
- Validation: focused target/range tests; `go -C v4/go test ./...`; `go -C
  v4/go vet ./...`; `go -C v4/go test -race ./...`; `GOARCH=386 go -C v4/go
  test ./...`; `cargo test --manifest-path v4/rust/Cargo.toml --no-fail-fast`
  (545 tests); `cargo test --manifest-path v4/rust/Cargo.toml --all-features
  --no-fail-fast` (666 tests); Rust formatting; all-target all-feature Clippy
  with warnings denied; all-feature benchmark compilation; `git diff --check`;
  and the SOW audit all pass. No normative-spec update is needed because no
  file format, public API, default validation behavior, or durable publication
  behavior changed.

### 2026-07-24 - normalizer lock-bound no-reclamation plan

- Evidence: the arrival-order normalizer already emits only logical staged
  range pages (`sequential_assignment.rs`, `range_staging.rs`), and the
  allocator already assigns their physical pages only through a bound bitmap
  reservation (`bitmap_cow.rs:1798-1832`). The normal production planner,
  however, accepts only a `RetirementReclamation` that retains the Linux
  operation-barrier guard (`bitmap_cow.rs:2451-2463`). A range replacement
  deliberately must not select and consume an unrelated eligible retirement
  batch merely to obtain allocator authority.
- Rust already has a valid `NoChange` reclamation shape which retains that
  guard, but it can currently be produced only as a result of reading a
  retirement tree (`retirement_reader.rs:426-472`). The held fence itself is
  therefore unable to authorize an ordinary non-reclaiming mutation. This
  blocks the first normalizer-to-live finalizer bridge even when all required
  range payload pages are available.
- The next narrow implementation adds a crate-private consuming conversion
  from an already-held `RetirementReclaimFence` to the existing typed
  no-reclamation result. It will be usable only by the normal lock-bound
  planner and will preserve the same operation-barrier borrow until the bound
  reservation is finalized or discarded. It will not read, alter, or reclaim
  the retirement tree.
- Tests will run the normal production planner through this exact authority,
  prove the empty reclaim facts and held guard survive binding, and prove a
  stale attachment still fails before pool mutation. This is Rust-only for
  this checkpoint: Go has a reader barrier but no equivalent live
  range-finalizer path yet, so adding a zero-valued Go token here would not
  establish any real lock lifetime. No public API, v4 byte, default
  validation, file publication, or normalizer result publication is added.

### 2026-07-24 - normalizer lock-bound no-reclamation implementation

- `RetirementReclaimFence::into_no_reclamation` now consumes the held Rust
  fence into the existing `RetirementReclamation::NoChange` authority. The
  authority still owns the operation-barrier guard; it carries no page slice,
  selected batch, or retirement-tree mutation authority.
- The normal production bitmap planner now has a tested no-reclamation input
  path. It binds a normal payload reservation under a barrier with registering
  readers, reports zero reclaimed pages and zero retirement facts, and can
  apply the planned bitmap reservation. A separately dirtied shadow scope is
  rejected as stale without any additional pool mutation.
- Focused Rust coverage passes for explicit no-reclamation authority and both
  normal/stale bitmap binding cases. Full validation passes: Rust default
  (548 tests) and all-feature (669 tests) suites, formatting, warnings-denied
  all-target Clippy, and all-feature benchmark compilation; Go tests, vet,
  race, and 32-bit tests; whitespace checking; and the SOW audit. The
  normalizer is still not connected to this path: the next slice must calculate
  its full range-replacement and retirement capacity under the held barrier
  before it assigns any physical range page. No Go source changed for the
  reason stated in the plan.

### 2026-07-24 - append-only retirement capacity-probe plan

- Evidence: `stage_range_root_retirement` can build the new retirement blob and
  append it to the selected tree, but it discovers a short exact scope only
  after it has claimed private shadow pages (`range_root_proof.rs:677-827`).
  The selected-reclaim finalizer already avoids that class of late discovery
  with `RetirementTreeEditor::probe_reclaimed_oldest_and_append_newest`
  (`retirement_writer.rs:5621-5744`), which validates the selected tree and
  reports the required replacement-tree capacity without allocating pages.
- That existing probe is intentionally wrong for an ordinary range replacement:
  it first deletes a proven oldest retirement prefix. A normal range update
  must preserve every existing retirement batch and append exactly one new
  batch for the selected transaction's successor.
- Add one crate-private append-only counterpart. It will read and locally
  validate the exact selected retirement-tree append path, record the committed
  tree pages that the append will replace, and return its checked private tree
  page budget. It will not build a blob, choose a physical page number, mutate
  the pool, consume a reclamation batch, change metadata, or expose a public
  operation.
- The following normalizer bridge will combine this fact with range ownership
  and bitmap-preview evidence to reserve its bounded shadow capacity before it
  materializes the final range payload. Tests here must prove an empty tree and
  a nonempty tree, exact agreement with the real append edit, malformed input
  rejection before pool mutation, and zero allocations after workspace setup.

### 2026-07-24 - append-only retirement capacity-probe implementation

- Added `RetirementTreeEditor::probe_append_newest` in Rust. It uses the same
  selected-source checks and append-path planner as the real tree edit, but a
  placeholder successor batch supplies only the transaction key needed for
  structural planning. The probe records replacement-tree facts in dedicated
  caller scratch and leaves its arena generation and in-use count unchanged.
- The result contains only the replacement-entry count and the exact private
  tree-page budget. It has no blob-page estimate because that depends on the
  converged protected-page list, which remains the next fixed-point step.
- Focused tests cover the legal empty retirement tree, a nonempty leaf with
  one existing batch, agreement with the real append edit, corrupted-tree
  rejection before arena mutation, and zero allocations after setup.
- Validation passed: Rust default (551 tests) and all-features (672 tests),
  Rust formatting, warnings-denied all-target/all-feature Clippy, and
  all-feature benchmark compilation; Go tests, vet, race, and 32-bit tests;
  `git diff --check`; and the SOW audit. No normative spec, public API,
  default-validation behavior, format byte, or file publication path changed.

### 2026-07-24 - ordinary range-replacement protected-set plan

- Evidence: a real retirement append rejects any committed tree page it
  replaces unless that page is already listed in the new retirement blob
  (`retirement_writer.rs:12944-12984`). The range-root proof currently seeds
  only old range-tree ownership before it enters its monotonic fixed point
  (`range_root_proof.rs:854-929`). A normal range replacement with a nonempty
  retirement tree therefore needs the new append-only probe's replacement
  entries in that seed before its first prospective append.
- Extend the private proof constructor with an explicit caller-owned initial
  replacement stream. It will add those selected committed page numbers after
  range-ownership collection and before the first candidate clone; the old
  entry point remains a strict empty-stream wrapper.
- Add one Rust-private ordinary-range proof preparer. It will derive its source,
  transaction facts, and held scope only from the already-bound no-reclamation
  bitmap reservation; probe the selected retirement append; seed those exact
  replacement pages; then, during every fixed-point preview, build a temporary
  blob from the candidate index, append it in the detached bitmap-finalization
  scope, and add the resulting bitmap replacements. The append witness must
  match the initial probe on every preview pass.
- This is preparation only. It will not change target metadata, materialize a
  new range page, reclaim an existing retirement batch, mutate the live shadow
  scope during preview, bind a coordinator terminal, publish a file, add a
  public API, or introduce a temporary file. A later bridge will call it after
  staging the logical normalizer output and before the one real range/retirement
  finalization sequence.
- Tests must cover legal empty and nonempty selected range/retirement roots,
  fixed-point inclusion of old range, bitmap, and retirement replacements,
  mismatched bound generation/scope rejection before mutation, malformed
  retirement input, insufficient preview scratch, replay-witness mismatch, and
  zero allocations after fixed workspace setup.

### 2026-07-24 - ordinary range-replacement protected-set implementation

- Rust now has a private initial-replacement extension of the existing
  range-root proof. Its original entry point remains an empty-stream wrapper,
  so pre-existing callers retain their exact behavior. The extension adds only
  already-proven committed retirement-tree replacements before fixed-point
  convergence starts.
- `prepare_range_root_replacement_proof` derives the selected source,
  generation, and scope from the held bound reservation. It first runs the
  append-only retirement probe without allocating a page, then replays the
  actual append in each detached bitmap-finalization preview. A changed
  replacement list, tree-page budget, or private release rejects the proof.
- The successful proof includes every old range page, every selected
  retirement-tree page that the new append replaces, and every prospective
  bitmap replacement. The helper uses only caller-provided scratch and leaves
  the live pool unchanged; it creates neither a temporary file nor a durable
  output.
- This does not yet invoke the real retirement stage/finalizer or publish a
  normalizer result. The next bridge must consume this proof once, stage the
  real retirement/blob/tree output, finalize the bitmap, and bind the existing
  target/terminal coordinator record under the same held lock.

### 2026-07-24 - ordinary range-replacement protected-set focused validation

- Focused Rust tests prove both an empty selected retirement tree and a
  nonempty selected retirement leaf converge correctly. The latter proves the
  old retirement leaf enters the initial seed and the final protected set
  alongside old range pages and the bitmap replacement. Both paths make zero
  heap allocations after caller scratch setup and leave the private pool's
  mutation snapshot unchanged.
- The append-probe checkpoint already covers malformed selected retirement
  input and zero-allocation probe behavior. Generation/scope rejection,
  short-preview scratch, and replay-witness failure remain explicit tests for
  the following real staging/finalizer bridge, where those errors can be
  checked against one abort-latched shared draft rather than an isolated proof.
- Full checkpoint validation passes: Rust default (554 tests) and all-feature
  (675 tests) suites; Rust formatting; warnings-denied all-target/all-feature
  Clippy; and all-feature benchmark compilation. Go `test ./...`, `vet
  ./...`, race tests, and 32-bit tests also pass. `git diff --check` and the
  SOW audit are rerun immediately before commit. No normative specification,
  public API, default-validation behavior, v4 byte layout, or file-publication
  path changed.

### 2026-07-24 - ordinary range-replacement terminal-composition plan

- Evidence: after `stage_range_payload` has claimed the new range pages in the
  shared shadow scope, the remaining steps are real mutation, not a harmless
  preview: `stage_range_root_retirement` appends the protected-page batch,
  `finalize_range_root_retirement` seals the bitmap, and the three-owner
  exporter/merger produces the only terminal journal accepted by the fixed-point
  coordinator (`range_root_proof.rs:727-827`,
  `bitmap_cow/selective_finalization.rs:2885-3158`, and
  `retirement_writer.rs:3107-3182`). The existing tests repeat that sequence
  inline, so a future live caller could accidentally return after one stage
  while leaving a partially changed private scope.
- Add one crate-private, consuming terminal-composition helper. It will accept
  only the already-bound reservation, replacement proof, exact shared scope,
  caller-owned finalization/export scratch, and the range payload that was
  already staged there. It will perform the real retirement append, bitmap
  finalization, typed three-owner export, and sorted journal merge as one
  sequence, returning only the proof-bound produced terminal authority.
- Because the range payload exists before this helper starts, every failure in
  this helper is post-mutation for the enclosing operation. It will mark the
  shadow pool abort-required, clear its supplied export/merge journals, and
  scrub the proof indexes before returning a typed stage/finalize/export/merge
  error. The immutable range-payload journal remains evidence for the outer
  whole-draft abort; this helper cannot make it look reusable. It will not
  offer an unsafe partial retry path.
- This boundary deliberately stops before the live transaction core/coordinator
  consumes the produced terminal authority. The following slice will bind that
  authority to the core and use the existing Linux durable publisher while the
  same operation barrier remains held. No public API, data-format byte,
  temporary file, or implicit validation is added here.
- Tests must cover a nonempty selected range/retirement tree, legal empty old
  range/retirement shapes, every injected post-payload failure class, zero heap
  allocations after caller scratch setup, proof-index scrubbing, and an
  abort-required pool on every returned failure.

### 2026-07-24 - ordinary range-replacement terminal integration finding and repair plan

- The new real terminal test exposed a contradiction that proof-only tests did
  not reach: a legal selected retirement leaf reaches
  `finalize_range_root_retirement` with one committed retirement-tree
  replacement and is rejected as stale (`range_root_proof.rs:2700-2900`,
  `bitmap_cow/selective_finalization.rs:2901-2905`). The stage itself remains
  valid, and its protected-page proof already contains that old tree page.
- This committed replacement is normal copy-on-write behavior. The append
  editor replaces the selected tree path and refuses success unless every such
  page is listed in the newly built protected-page blob
  (`retirement_writer.rs:4886-5008`, `retirement_writer.rs:6732-6771`). The
  finalizer and its sealed-stage recheck must therefore allow committed
  replacements. They must continue to reject prior-private replacements: an
  ordinary one-shot range replacement has no prior retirement draft to reuse.
- Repair only that false exclusion, keep the exact proof/stage/terminal checks,
  and make the real nonempty-retirement test assert that the resulting terminal
  contains the expected committed replacement. This is an internal correctness
  repair; it changes no format, public API, validation default, or durable
  publication behavior.

### 2026-07-24 - ordinary range-replacement terminal composition implementation

- Rust now has one crate-private consuming helper for the post-payload path.
  It runs the real protected retirement append, proof-bound bitmap
  finalization, typed three-owner export, and sorted terminal merge, returning
  only the existing produced-terminal authority for the later coordinator.
- Every returned error marks the shared pool abort-required, clears supplied
  export/merge journals, and scrubs the proof indexes. It does not publish a
  file, alter metadata, expose a public API, or add a temporary file.
- The false nonzero-committed-replacement rejection is removed from both
  terminal rechecks. Existing proof-bound retirement editing remains the
  authority that proves each replaced selected page was recorded in the new
  retirement batch. Prior-private retirement replacements remain rejected as
  stale for this one-shot operation.

### 2026-07-24 - ordinary range-replacement terminal composition validation

- Focused Rust tests now execute a real terminal sequence for both empty and
  nonempty selected retirement trees. The nonempty case proves a legal
  committed retirement-tree replacement reaches the produced terminal.
- One failure matrix proves bad retirement scratch, bad bitmap-finalization
  scratch, an undersized combined journal, a dirty range journal, and a dirty
  combined journal all latch abort, clear export/merge scratch, and scrub every
  page-index workspace. The warmed success and failure paths allocate zero
  heap bytes after caller scratch setup.
- Validation passed: Rust default (555 tests) and all-features (676 tests),
  warnings-denied all-target/all-feature Clippy, all-feature benchmark
  compilation, Go tests/vet/race/32-bit tests, Rust formatting, `git diff
  --check`, and the SOW audit. No normative spec, public API,
  default-validation behavior, v4 byte layout, or durable file-publication
  path changed.

### 2026-07-24 - ordinary range-replacement core/publication integration plan

- The terminal helper now produces the exact three-owner authority that the
  coordinator accepts, but no real range-root terminal has crossed the whole
  remaining boundary. The Linux publisher has substantial fault coverage, yet
  its existing end-to-end producers are synthetic or retirement-reclaim-only
  (`os/linux/live_writer.rs:584-974`,
  `os/linux/live_writer/live_reclaim.rs:1280-2086`). The range path has only
  stopped after `finalize_range_root_replacement_terminal`
  (`range_root_proof.rs:955-1105`). Treating those independent tests as an
  end-to-end normal replacement would be a false completion claim.
- First add one production-shaped Linux test that builds a real nonempty direct
  range replacement from logical staging, obtains the no-reclamation allocator
  authority under the held operation barrier, creates the protected range and
  retirement terminal, binds it to the transaction core, drains it through the
  existing durable publisher, and reopens the selected target. It must prove
  the committed meta contains the new range root/count, bitmap root, retirement
  root/count, exact terminal page bytes, and a release of the same operation
  barrier only after core completion.
- The test will also exercise the existing whole-draft path after a late
  terminal/core failure: no selected metadata or file bytes may publish, all
  caller-owned journals and indexes must be scrubbed, and the core must return
  to a clean reusable state only through its explicit abort. It uses fixed
  caller-owned arrays established before the barrier and deliberately adds no
  public writer, `AddRanges`, normalizer API, temporary file, or default
  validation.
- This is a narrow integration proof, not the final SDK workspace. Once it
  establishes the exact core/publisher seam, the following slice will move the
  same bounded backing into an opaque normal-range workspace and expose it only
  through the already-resolved high-level/advanced transaction layers.

### 2026-07-24 - ordinary range-replacement core/publication success path

- Added a Linux-only, production-shaped test in
  `v4/rust/iprange-livedb/src/retirement_writer.rs` that starts with a real
  direct range leaf, a real free bitmap, and a nonempty retirement tree. It
  prepares a replacement logical range before the operation barrier, then
  under that same barrier takes no-reclamation authority, proves the protected
  replacement, materializes the range, produces the three-owner terminal,
  binds the transaction core, publishes, and reopens the result.
- The test proves the new range root/count, bitmap root, retirement root/count,
  transaction generation, byte-for-byte terminal-page publication, and barrier
  exclusion while finalization is active. Allocation counting reports zero
  allocations from finalization through publication; all mutable storage exists
  before the barrier.
- The integration exposes one real workspace constraint: the coordinator's
  `new_locations` journal must have the exact final terminal length, not merely
  a larger maximum. This fixture has a proven four-page terminal and therefore
  supplies four entries. The future opaque normal-range workspace must obtain
  that exact length from the bounded finalization plan before it hands the
  journal to the coordinator; it must not rely on an oversized loose buffer.
- Validation: the focused Linux test, the Rust default suite (555 tests), and
  the all-feature suite (677 tests) pass. Formatting, warnings-denied
  all-target/all-feature Clippy, whitespace checks, and the SOW audit also
  pass.
- The late-failure/whole-draft-abort companion is recorded below. These tests
  remain an integration proof only: no public API, format byte, default
  validation behavior, temporary file, or production writer surface changed.

### 2026-07-24 - ordinary range-replacement late-failure integration

- The same real Linux operation now has an injected late core-bind failure
  after the protected range/bitmap/retirement terminal exists. It rejects a
  zero terminal nonce, marks the shadow pool abort-required, clears all three
  typed terminal journals and the merged journal, and proves every fixed-point
  proof index is clean before the callback returns its typed error.
- The Linux publisher releases the operation barrier without writing the main
  file. The test compares every main-file byte with its original image, checks
  that the selected metadata is unchanged, and checks that the private base
  target did not receive any range, bitmap, or retirement replacement. (A
  pending transaction legitimately has a private next-generation base target;
  it is not published metadata.)
- The canceled coordinator reservation and workspace are then released, and
  only explicit whole-draft abort returns the core to `Clean`. A fresh
  transaction proves the old abort latch and handle do not contaminate the
  next draft.
- Focused success and late-failure tests pass. Rust default (555 tests) and
  all-feature (678 tests) suites, warnings-denied all-target/all-feature
  Clippy, all-feature benchmark compilation, whitespace checks, and the SOW
  audit pass for this companion.

### 2026-07-24 - bounded dynamic terminal-journal capacity plan

- Evidence: the coordinator currently requires `new_locations.len()` to equal
  the final terminal-page count (`v4/rust/iprange-livedb/src/writer_fixed_point.rs:2980-2993`).
  The real ordinary-replacement proof therefore uses a hard-coded four-entry
  array solely because that fixture happens to produce four pages
  (`v4/rust/iprange-livedb/src/retirement_writer.rs:11044-11064`). In a real
  normalizer operation, the terminal count is known only after the held-lock
  range/bitmap/retirement finalization has completed
  (`v4/rust/iprange-livedb/src/range_root_proof.rs:1012-1048`).
- The bounded-memory contract requires all backing to exist before the
  operation lock, but it does not require unused capacity to become a commit
  journal. Keep one caller-owned, transaction-budgeted capacity backing. Once
  terminal composition supplies its exact page count, the coordinator will
  prove the complete backing is clean, borrow only the exact leading prefix,
  and use that prefix exclusively for provenance, record digest, replay, and
  cleanup. The tail remains neutral and inaccessible to the aggregate.
- A terminal larger than capacity remains a pre-aggregate typed failure with
  no coordinator/pool mutation. This does not weaken exact terminal binding:
  the active journal is still exactly one entry per produced terminal page; it
  only removes the accidental requirement that its preallocated backing have
  the same length before that count can be known.
- Update the real Linux ordinary-range integration proof to retain maximum
  bounded backing instead of a fixture-specific four-entry array. It must
  prove a four-page terminal uses only the exact prefix and that the unused
  tail remains empty after both durable publication and the injected
  late-failure/whole-draft-abort path. Add focused coordinator coverage for
  oversized clean backing and undersized backing, with zero warmed-path heap
  allocation.
- This is a private Rust workspace correction only. It does not change a v4
  byte, normalizer semantics, public SDK/API, default validation, temporary
  file behavior, or the bounded resource contract.

### 2026-07-24 - bounded dynamic terminal-journal capacity implementation

- `FixedPointPreparedProducedTerminalWork::prepare_aggregate` now accepts a
  clean `new_locations` backing whose capacity is at least the produced
  terminal-page count. It validates the whole backing first, then reborrows
  only `[..terminal_page_count]`; every later provenance write, digest input,
  replay handoff, error cleanup, and retained aggregate record sees that exact
  prefix only.
- The Linux ordinary-range proof now reserves its full fixed eight-entry
  backing before the barrier even though this fixture produces four terminal
  pages. It proves the unused capacity is clean after success and after the
  injected late core-binding failure.
- A new Linux failure case gives the coordinator only three entries for the
  same four-page terminal. It receives the typed
  `SourceScratchTooSmall { required: 4, actual: 3 }` result before aggregate
  replay, clears every caller-owned terminal journal, latches whole-draft
  abort, leaves the main file and selected metadata unchanged, and permits a
  clean later transaction only after explicit cancellation and abort.
- The change remains an internal capacity/prefix distinction. The actual
  terminal journal, terminal page order, target metadata, and durable output
  remain exact; no maximum-capacity tail can enter the commit record.

### 2026-07-24 - bounded dynamic terminal-journal capacity validation

- Focused Linux tests pass for durable ordinary replacement, injected late
  core-bind failure, and the new undersized three-entry coordinator backing.
  The normal four-page terminal succeeds with eight retained entries; the
  undersized case fails before aggregate replay and leaves the main file
  unchanged.
- Rust passes 555 default and 679 all-feature tests. Rust formatting,
  warnings-denied all-target/all-feature Clippy, and all-feature benchmark
  compilation pass.
- Cross-language regression checks remain green: `go -C v4/go test ./... -count=1`
  and `go -C v4/go vet ./...`. `git diff --check` and the project SOW audit
  pass. No changed source has a user-visible API or format contract to add to
  the Go implementation at this private Rust live-writer boundary.

### 2026-07-24 - opaque normal-range pre-lock workspace plan

- Evidence: the real ordinary-range publication proof still constructs its
  normalized logical tree from fixture-local arrays
  (`v4/rust/iprange-livedb/src/retirement_writer.rs:10990-11019`). The
  sequential engine itself correctly emits a sealed logical result without
  physical page allocation (`v4/rust/iprange-livedb/src/sequential_assignment.rs:475-533`),
  but `RangeTreeStaging` currently retains its finished state only in a
  stack-local borrowing object (`v4/rust/iprange-livedb/src/range_staging.rs:155-251`).
  A real operation workspace must own those fixed pages across the gap between
  pre-lock normalization and lock-held physical materialization.
- Add a crate-private, generic Rust workspace that owns fixed-capacity
  normalizer pages, logical range staging pages, and the existing stack-free
  range-tree build workspace. Construction is the only allocation point; its
  limits are explicit assignment/work/mutation budgets. It supports an empty
  replacement, so zero logical-page capacities remain valid when no input is
  accepted.
- Add a private sealed-staging reattachment constructor. It accepts only the
  workspace's own previously sealed result, rebuilds the immutable staging
  view without changing page bytes, and relies on the existing materializer to
  perform its narrowly scoped output-geometry checks before any terminal page
  is changed. It is not a reader validation path and does not inspect a file.
- Preparation checks the selected family, carries its value kind, and derives
  the next transaction generation before accepting input. On every input or emission
  failure it scrubs both owned logical partitions and leaves no prepared
  result. A caller may discard a prepared result only after its enclosing
  transaction has been abandoned; the helper erases only unpublished logical
  memory and does not make a failed live draft independently reusable.
- This checkpoint deliberately stops before the full Linux allocator/core
  operation. The subsequent slice will put its coordinator, bitmap, proof,
  terminal, and abort handling behind the same opaque owner, using the
  proven exact-prefix journal rule. No public SDK method, v4 byte, default
  validation, temporary file, or Go surface changes in this isolated Rust
  owner step.
- Tests will prove arrival-order output survives the sealed reattachment,
  zero allocations after workspace construction, deterministic capacity
  rejection with complete scrubbing, and an empty replacement. The existing
  normalizer, staging, and materializer tests remain the detailed semantic
  coverage; this new coverage proves their real workspace lifecycle seam.

### 2026-07-24 - opaque normal-range pre-lock workspace implementation

- Added `live_normal_range.rs`, a crate-private generic Rust owner for fixed
  normalizer pages, fixed logical range pages, the existing range-tree build
  workspace, explicit assignment/work/mutation budgets, and retained-byte
  accounting. Its two variable partitions allocate only during construction;
  normal input, sealing, reopening, and reset allocate nothing.
- The workspace accepts only an arrival-order callback over the existing
  sequential engine. It checks the selected address family and next
  generation, retains the sealed logical result, and exposes only a temporary
  one-time borrow of the reattached staging view for later allocator
  materialization. Reopening consumes that prepared marker, so one logical
  result cannot be materialized twice through this owner. It never exposes a
  page number, bit combination, physical pool, file handle, or public API
  value.
- `RangeTreeStaging::reopen_sealed` now recreates a finished immutable view of
  workspace-owned logical output. It verifies the retained logical result fits
  the owned partition and that no hidden trailing page exists; the existing
  materializer remains responsible for narrow page-geometry and CRC checks
  before terminal output changes.
- Any normalizer or staging error clears both logical partitions and leaves no
  prepared result. Success has separate after-publication and after-abort
  reset methods so the future enclosing live operation retains responsibility
  for transaction abort, lock release, and durable-outcome handling.
- The production-shaped ordinary replacement proof now uses this workspace
  instead of fixture-local ordered-builder storage. It feeds an arrival-order
  direct range through the reattached staging view into the same held-lock
  bitmap/range/retirement finalizer and atomic Linux publisher used before.
  Its success and both late failure paths clear the logical owner at the
  corresponding terminal state.
- This is still an internal owner, not an SDK writer operation. The full
  coordinator/bitmap/proof/terminal backing remains in the focused proof
  fixture until the next slice moves all of it into one Linux operation owner.
  No public API, v4 byte, default validation behavior, temporary file, or Go
  contract changed.

### 2026-07-24 - opaque normal-range pre-lock workspace validation

- Three focused workspace tests prove an overlapping arrival-order direct
  stream survives sealed reattachment and materializes as the expected
  canonical ranges; a post-mutation invalid input scrubs all owned logical
  pages; and an empty replacement succeeds with zero normalizer and staging
  pages. The warmed preparation/reopen/materialization path reports zero heap
  allocations. A separate staging test rejects a nonempty page beyond the
  sealed logical result, so hidden logical output cannot enter a later
  materialization attempt.
- The existing Linux ordinary replacement success, late core-bind failure,
  and undersized coordinator-journal cases now all consume that workspace and
  pass. They continue to prove no allocation from finalization through
  publication, no file mutation on failure, and explicit whole-draft abort
  before failed-workspace reuse.
- Validation passed: Rust default suite (556 tests), Rust all-features suite
  (683 tests), Rust formatting, warnings-denied all-target/all-feature Clippy,
  and all-feature benchmark compilation. The unchanged Go surface also passes
  `go -C v4/go test ./... -count=1` and `go -C v4/go vet ./...`.

### 2026-07-24 - owned lock-held normal-range backing plan

- Evidence: after the new pre-lock owner, the ordinary publication proof still
  declares the coordinator journals, live slots, bitmap planner arenas,
  shadow slots, payload journals, proof indexes, finalization scratch, and
  terminal journals as many fixture-local arrays
  (`v4/rust/iprange-livedb/src/retirement_writer.rs:11006-11189`). This keeps
  the correct path test-only and leaves its retained-memory accounting limited
  to the coordinator view rather than the complete operation owner.
- Add a second crate-private, fixed-capacity owner for those lock-held
  partitions. Its explicit capacity will cover maximum terminal/live pages,
  bitmap replacement/index slots, logical range payload pages, and
  page-number-index pages. All derived lengths use checked arithmetic before
  the first allocation; invalid/overflowing capacity fails before a writer
  barrier or transaction draft exists.
- Store every variable-sized partition in fallibly reserved vectors and reset
  every partition to its canonical empty value before a later attempt. A
  temporary borrowing scratch view may expose internal slices only to the
  crate-private finalizer; it does not expose any SDK or caller-controlled
  page/bitmap value.
- Make the ordinary Linux integration proof obtain every former large local
  array from this owner. It will set the fixed-point workspace's transaction
  retained-byte charge to the complete owner size, while the coordinator still
  uses only its exact terminal journal prefix. This proves the resource budget
  cannot undercount hidden planner/proof/terminal memory.
- Keep the actual operation orchestration in the proof for this slice. The
  following slice will move that finalizer callback and its explicit
  pre-publication abort handling behind the same owner. No public API, v4
  byte, validation default, temporary file, or Go behavior changes here.
- Tests will cover invalid and arithmetic-overflow capacity before allocation,
  canonical reset across attempts, full retained-byte charging, the real Linux
  success/failure paths using owned backing, and zero allocations after
  workspace construction.

### 2026-07-24 - transaction abort-latch validation

- New Go and Rust tests prove the explicit latch blocks commit preflight and
  both fresh and already-held pool operations; whole-draft abort visits every
  private slot, invalidates the old handle, and permits a clean new
  transaction. Rust additionally proves the latch preserves resolve-only and
  committed-cleanup transaction states.
- Go `test ./...`, `vet ./...`, and the exact-v4 race suite pass. Rust passes
  484 tests without optional features and 605 with all features. Rust
  formatting, warnings-denied all-target Clippy, and all-feature benchmark
  compilation pass. Whitespace checking and the project SOW audit pass.
- No normative spec, public SDK, default-validation, or v4-byte change is
  needed: this is private transaction control flow. The next boundary remains
  live allocation/range-root integration for the normalizer, followed only then
  by direct and membership workflows.

### 2026-07-24 - page-backed sequential-assignment validation

- Focused Go and Rust tests pass. They prove empty input, arrival-order partial
  overwrites, clear, direct zero, membership-zero failure, canonical coalescing,
  full IPv6 maximum handling, bounded page/work failure followed by rollback,
  deterministic 256-address oracle equivalence, linear doubling behavior, zero
  warmed-path allocations, actual private-page output, and reopening that
  output through each language's range reader.
- Full Go `test ./...` and `vet ./...` pass. Rust passes 483 no-default-feature
  tests and 604 all-feature tests; formatting, warnings-denied all-target/all-
  feature Clippy, and all-feature benchmark compilation pass. Whitespace
  checking and the project SOW audit pass.
- The whole-writer abort bridge is intentionally not claimed by this validation:
  no public normalizer or writer transaction currently invokes this internal
  component. That bridge is the next implementation boundary in this active
  SOW, followed by direct/membership workflow integration.

### 2026-07-24 - canonical range-page encoders

- Focused Rust and Go encoder tests prove exact IPv4/IPv6 capacity, page
  layout/CRC round trips, zeroed tails, and atomic rejection. The branch tests
  also prove inherited-fence rejection without changing the existing page.
- Full Go `test ./...` and `vet ./...` pass. Rust passes 456 no-default-feature
  tests and 577 all-feature tests, formatting, warnings-denied all-target
  Clippy, and all-feature benchmark compilation. `git diff --check` and the
  project SOW audit pass.
- No normative spec change is needed: this private implementation applies the
  existing range-page contract. It adds no public API, default validation, or
  on-disk format behavior beyond that already specified.

### 2026-07-24 - ordered range-tree builder validation

- `go -C v4/go test ./...` and `go -C v4/go vet ./...` pass, including the new
  zero-allocation and reader-reopen tests.
- `cargo test --manifest-path v4/rust/Cargo.toml --no-default-features` passes
  466 tests; `cargo test --manifest-path v4/rust/Cargo.toml --all-features`
  passes 587 tests. Formatting, warnings-denied all-target/all-feature Clippy,
  and all-feature benchmark compilation pass.
- The packers are intentionally not attached to a live transaction yet. The
  next implementation boundary is a range-tree page-pool owner/sink adapter,
  followed by the page-backed sequential assignment engine; neither can be
  substituted by this ordered-only component.

### 2026-07-24 - range page-pool sink validation

- New Rust and Go tests construct a split range tree directly in real private
  pool slots, prove `Range` ownership and exact range header/family bytes, and
  verify CRCs. They also prove wrong transactions and stale Go checkpoints
  claim nothing, a page-capacity failure leaves its partial build for the one
  checkpoint rollback path, and the warmed build/rollback path allocates zero
  heap memory.
- Full Go `test ./...` and `vet ./...` pass. Rust passes 471 no-default-feature
  tests and 592 all-feature tests, formatting, warnings-denied all-target/all-
  feature Clippy, and all-feature benchmark compilation. Whitespace checking
  and the project SOW audit pass.
- No normative specification update is needed: this is private writer plumbing
  that applies the existing range-page contract and explicitly adds no default
  reader/writer validation or on-disk behavior.

### 2026-07-24 - selected-reclaim coordinator bind

- Focused Linux tests pass for selected reclaim, no-change finalization, exact
  bitmap terminal export, and retained retirement pages. The selected test
  proves failed binding preserves both retry authorities and the terminal bytes
  before successfully retrying inside its existing zero-allocation assertion.
- Rust workspace matrices pass: 449 tests without optional features and 564
  with all features. Warnings-denied all-target Clippy and all-feature benchmark
  compilation pass.
- Go `test ./...` and `vet ./...`, Rust formatting, whitespace checking, and
  the project SOW audit pass. No specification update is needed: this remains
  private Rust lifecycle plumbing with no public API or on-disk change.

### 2026-07-24 - bound-terminal cancellation

- Focused cancellation tests pass in no-default and all-feature Rust builds.
  They prove the direct and produced terminal paths allocate zero heap memory,
  clear all caller terminal entries, release the prepared scope, and preserve a
  usable predecessor. The stale-evidence case returns `StalePredecessor` only
  after the same cleanup.
- The all-feature fixed-point test module and both held-lock Linux reclaim
  integration cases pass, confirming normal terminal execution/publication
  retains its immutable journal behavior. Formatting and whitespace checks
  pass. No normative specification update is needed: this remains private Rust
  transaction lifecycle repair with no public API or on-disk change.
- Full Rust workspace matrices pass: 451 tests without optional features and
  566 with all features. Warnings-denied all-target/all-feature Clippy,
  all-feature benchmark compilation, Go `test ./...` and `vet ./...`, Rust
  formatting, whitespace checking, and the SOW audit pass.

### 2026-07-24 - Reclaim selection/planning boundary

- Verified reclamation now owns its copied selected facts and lock guard rather
  than borrowing a stack-local selection. This preserves selection, full
  verification, and the second pass, while allowing a selected bitmap plan to
  expose its exact shadow capacity before physical binding.
- Permanent tests prove `NoChange` performs zero allocations and does not touch
  the bitmap planner or a shadow scope. The held-lock Linux selected-batch test
  now plans first, proves the reserved shadow scope is exactly the reported
  size, then binds and completes the existing end-to-end finalizer path.
- Validation passes: Rust tests 454 without optional features and 569 with all
  features; warnings-denied all-target/all-feature Clippy; all-feature benchmark
  compilation; Go `test ./...` and `vet ./...`; Rust formatting; whitespace
  checking; and the project SOW audit.
- No normative specification update is needed. This is private Rust ownership
  plumbing: public API, on-disk bytes, validation defaults, and Go behavior are
  unchanged.

### 2026-07-24 - private clean-writer Reclaim owner

- New Linux end-to-end tests use real v4 metadata pages, a registered reader,
  free bitmap, retirement tree, and sidecar: `NoChange` leaves the complete
  main file byte-for-byte unchanged; one selected batch publishes exactly one
  new generation; cancellation before locking creates no draft; and
  cancellation after `core.begin` aborts the private draft, returns all slots,
  clears prepared scope/workspace state, and leaves the main file unchanged
  (`retirement_writer.rs:10452-10475`). Every path asserts zero heap allocation.
- Full validation passes: Rust workspace tests 454 without optional features
  and 573 with all features; warnings-denied all-target/all-feature Clippy;
  all-feature benchmark compilation; Go `test ./...` and `vet ./...`; Rust
  formatting; whitespace checking; and the SOW audit.
- No normative specification update is needed. This is the internal lifecycle
  order required by the existing Reclaim contract; it does not yet expose the
  public SDK operation or its opaque owned workspace.

### 2026-07-24 - opaque SDK-owned Reclaim workspace

- Focused evidence passes: the two new workspace construction/preflight tests,
  all four end-to-end Linux Reclaim tests, and all 29 fixed-point lifecycle
  tests. The cancellation-after-draft test performs a second selected Reclaim
  on the same workspace and confirms it publishes without a heap allocation.
- Full Rust matrices pass: 454 tests without optional features and 575 with all
  features. Warnings-denied all-target/all-feature Clippy and all-feature
  benchmark compilation pass. Rust formatting and whitespace checks pass.
- Cross-language regression checks remain green: Go `test ./...` and `vet ./...`
  pass. The project SOW audit passes with the single active SOW and no durable
  sensitive-data findings.
- No normative specification or project skill update is needed: this remains a
  private Rust implementation boundary. The format, public SDK, operator docs,
  and Phase-2 signing/high-level-algebra SOWs are unaffected.

### 2026-07-24 - selected-reclaim retirement stage

- The Linux selected-reclaim finalizer now executes the normal retirement
  stage under the held operation lock. Its prior independent fixed-point replay
  still agrees with the protected list, and the published terminal journal
  contains the blob and tree pages exactly once.
- The same fixture proves a short blob buffer, short terminal journal, and
  same-valued replacement scope all fail before shadow-scope mutation. The
  successful path remains allocation-free under the existing finalizer-wide
  allocation assertion.
- Rust workspace tests pass: 449 without optional features and 564 with all
  features. Formatting, warnings-denied all-target/all-feature Clippy, and
  all-feature benchmark compilation pass. Go `test ./...` and `vet ./...`,
  `git diff --check`, and the project SOW audit pass.
- No normative specification change is required: this is internal Rust
  finalizer composition, with no public API, on-disk format, validation
  default, or Go behavior change.

### 2026-07-24 - bounded terminal capacity

- Focused Rust tests pass for direct terminal binding, sparse replay with a
  four-page reservation/two-page result, selective cleanup of all four slots,
  and empty/oversized journal rejection before mutation.
- Full Rust workspace matrices pass: 444 no-default-feature tests and 559
  all-feature tests. Formatting, all-target/all-feature Clippy with warnings
  denied, and all-feature benchmark compilation pass.
- Go `test ./...` and `vet ./...`, `git diff --check`, and the project SOW
  audit pass. The binary-format specification already required bounded
  pre-reservation with physical selection under the lock, so no specification
  change was needed. This remains Rust-private infrastructure; Go has no
  corresponding lifecycle implementation yet.

### 2026-07-24 - private lock-bound reclamation reservation

- Focused Rust coverage passes for invalid-limit rejection before source access
  or shadow-scope mutation, plus both real Linux finalizer cases (no-change and
  selected retired batch) through the extracted private helper.
- Full Rust workspace matrices pass: 445 no-default-feature tests and 560
  all-feature tests. Formatting, all-target/all-feature Clippy with warnings
  denied, and all-feature benchmark compilation pass.
- Go `test ./...` and `vet ./...`, `git diff --check`, and the project SOW
  audit pass. This is a Rust-private lifecycle consolidation only: no public
  SDK, byte-format, validation-default, specification, or Go behavior changed.

### 2026-07-24 - selected-reclaim protected-list extraction

- Focused Rust tests pass for sorted/deduplicated protected-page merging,
  short and out-of-bounds inputs, invalid reclaim limits before source access,
  and both Linux lock-held finalizer cases. The selected case compares the new
  private helper against the established independent fixed-point calculation
  and remains allocation-free across finalization and publication.
- Full Rust workspace matrices pass: 447 no-default-feature tests and 562
  all-feature tests. Formatting, all-target/all-feature Clippy with warnings
  denied, and all-feature benchmark compilation pass.
- Go `test ./...` and `vet ./...`, `git diff --check`, and the project SOW
  audit pass. No normative specification update is needed: this is private
  Rust lifecycle machinery with no new public API or on-disk behavior.

### 2026-07-24 - selected-reclaim retirement capacity

- Focused Rust coverage passes for exact blob-plus-tree capacity accounting,
  short-scope rejection before mutation, and the selected Linux finalizer's
  exact capacity relationship. The selected path remains allocation-free.
- Full Rust workspace matrices pass: 449 no-default-feature tests and 564
  all-feature tests. Formatting, all-target/all-feature Clippy with warnings
  denied, and all-feature benchmark compilation pass.
- Go `test ./...` and `vet ./...`, `git diff --check`, and the project SOW
  audit pass. This is private capacity accounting only; no specification,
  public API, byte-format, or Go behavior changed.

### 2026-07-24 - stage-aware reclamation preview

- Focused Rust tests pass: bitmap preview read-only/replay agreement,
  forced two-leaf post-retirement fixed point, and both Linux finalizer
  reclamation cases.
- Full Rust matrices pass: 443 no-default-feature tests and 558 all-feature
  tests. `cargo fmt --check`, all-target/all-feature Clippy with warnings
  denied, and all-feature benchmark compilation pass.
- Go `test ./...` and `vet ./...`, `git diff --check`, and the project SOW
  audit pass. The Go result validates no regression only; this Rust-private
  implementation has not been mirrored into Go.

### 2026-07-24 - Linux source/attempt/target state

- Targeted Linux writer lifecycle tests: 11/11 pass. They inject source,
  attempted-target, later-target, phase-5 state-2, and mismatched-transition
  states without calling a physical commit API.
- Rust: `cargo fmt`, no-default workspace tests (419), all-feature workspace
  tests (520), all-feature benchmark compilation, and all-target Clippy with
  warnings denied pass.
- Go: `go test ./...` and `go vet ./...` pass. `git diff --check` and the
  project SOW audit pass.
- This validates the in-memory ownership proof only. It does not claim phase-2
  or phase-4 durability, a physical metadata writer, commit-result mapping, or
  a public writer API.

### 2026-07-24 - Linux physical publication

- Targeted tests cover successful publication, preflight reuse, a private-page
  sink failure after a real page write, phase-2 sync failure, phase-3 meta-write
  failure, phase-4 sync and confirmation failures, phase-5 update failure,
  explicit post-publication close-only handling, and an injected unlock failure.
  They prove source-tail truncation only when the source remains selected and
  target preservation once the target meta is selected.
- The physical publisher is allocation-free at reader capacities 1, 64, and
  1024. The full Rust no-default matrix passes 421 tests and the all-feature
  matrix passes 533 tests.
- Rust Clippy with warnings denied and all-feature benchmark compilation pass.
  Go `test` and `vet` also pass. This validation still does not make a public
  writer, does not add default full validation, and does not prove the required
  core-to-barrier finalization integration.

### 2026-07-24 - durable core-to-Linux publication integration

- Focused core tests prove exact durable authorization, selected-generation
  advancement only after confirmation, committed-cleanup retry, stale-handle
  invalidation, and refusal to abort or begin while a durable cleanup remains.
- The composed Linux test drives real mixed bitmap/retirement terminal pages
  through the bridge and reopens the file to prove byte-exact page output and
  the target metadata. It also injects a selected-generation mismatch, phase-2
  `NotCommitted`, phase-4 `OutcomeUnknown`, and phase-5 `Committed` failure.
  The assertions prove the core's corresponding abortable, resolve-only, and
  committed states rather than only the physical writer classification.
- Final checkpoint validation passes: Rust no-default matrix 426 tests;
  Rust all-feature matrix 538 tests; warnings-denied all-target Clippy;
  all-feature benchmark compilation; Go `test` and `vet`; Rust formatting;
  `git diff --check`; and the project SOW audit.

### 2026-07-24 - lock-scoped finalization context

- Focused Rust coverage proves the no-change reclamation path retains an
  allocator authority, a verified reclaimed-page path carries that authority
  through bitmap binding, and Linux exposes the actual retained selected source
  with the stable reader facts only while its sidecar operation lock remains
  held.
- Final checkpoint validation passes: Rust no-default workspace matrix 427
  tests; Rust all-feature workspace matrix 540 tests; warnings-denied
  all-target Clippy; all-feature benchmark compilation; Go `test` and `vet`;
  Rust formatting; `git diff --check`; and the project SOW audit.
- This validates the lifetime boundary and source/fence pairing. It does not
  yet prove a production allocator or retirement finalizer is invoked through
  that context before the existing core page-drain path.

### 2026-07-24 - finalizer-to-publication bridge

- `cargo test --manifest-path v4/rust/Cargo.toml -p iprange-livedb
  --all-features`: pass, 540 tests. The composed Linux transaction test covers
  the successful lock-bound callback, its retained-source/fence/sidecar-lock
  assertions, and a callback failure that performs no publication.
- `cargo check --manifest-path v4/rust/Cargo.toml --workspace --all-features
  --benches`: pass. This is the important non-test build check: the old no-op
  pre-finalized adapters are absent outside test compilation.
- Final checkpoint validation passes: Rust no-default workspace matrix 427
  tests; Rust all-feature workspace matrix 540 tests; warnings-denied
  all-target Clippy; all-feature benchmark compilation; Go `test` and `vet`;
  Rust formatting; `git diff --check`; and the project SOW audit.

### 2026-07-24 - lock-bound aggregate execution

- `cargo test --manifest-path v4/rust/Cargo.toml -p iprange-livedb
  --all-features retirement_writer::tests::scoped_arena_preserves_scope_isolation_through_linux_publication`:
  pass. This exercises the normal publisher callback and the exact retained
  page source while the aggregate changes the pending transaction state.
- Final checkpoint validation passes: Rust no-default workspace matrix 427
  tests; Rust all-feature workspace matrix 540 tests; warnings-denied
  all-target Clippy; all-feature benchmark compilation; Go `test` and `vet`;
  Rust formatting; `git diff --check`; and the project SOW audit.

### 2026-07-24 - deferred physical-output preparation

- `writer_fixed_point::tests`: 27/27 pass, including the new reservation
  boundary test.
- Full checkpoint validation passes: Rust no-default workspace matrix 429
  tests; Rust all-feature workspace matrix 542 tests; warnings-denied
  all-target Clippy; all-feature benchmark compilation; Go `test` and `vet`;
  Rust formatting; `git diff --check`; and the project SOW audit.
- The normal all-feature benchmark/library build compiles with the old
  pre-finalized callback APIs excluded by `#[cfg(test)]`. No real planner has
  been wired to the late export yet, so this checkpoint proves the boundary,
  not the final lock-bound allocator implementation.

### 2026-07-24 - lock-bound physical planner

- Focused tests pass: the Linux finalizer selects physical free-bitmap pages
  under the held operation lock and durably publishes the exact terminal bytes;
  the fully retained selective-refresh regression passes at 3, 512, and 4096
  pages; and the existing shared bitmap/retirement finalization test still
  passes.
- Final checkpoint validation: Rust no-default workspace matrix 430 tests;
  Rust all-feature workspace matrix 544 tests; warnings-denied all-target
  Clippy; all-feature benchmark compilation; Go `test` and `vet`; Rust
  formatting; `git diff --check`; and the project SOW audit.
- This no-change checkpoint uses the production Linux writer and real pinned
  source/fence, but remains an internal integration path. No public writer API
  is claimed; selected retirement-batch reclamation is covered separately
  below.

### 2026-07-24 - selected retirement reclamation

- Focused Linux coverage passes: the no-change and real selected-batch
  finalizers both run under the held operation lock. The selected case pins a
  real transaction-2 reader, verifies its eligible oldest batch, reuses pages
  21 and 23, rejects a mismatched commit nonce, and publishes exact terminal
  bytes with zero finalizer allocations.
- Full checkpoint validation passes: Rust no-default workspace matrix 430
  tests; Rust all-feature workspace matrix 545 tests; warnings-denied
  all-target Clippy; all-feature benchmark compilation; Go `test` and `vet`;
  Rust formatting; `git diff --check`; and the project SOW audit.
- This closes the specific selected-batch composition gap. It remains an
  internal Linux lifecycle path; broader writer/SDK work is still pending and
  is not claimed by this checkpoint.

### 2026-07-24 - reusable locked reclamation protocol

- Focused Rust coverage passes for the complete selected/no-change protocol,
  read failure before consumer invocation, consumer-error preservation, and the
  Linux selected-batch finalizer using the shared boundary.
- Full checkpoint validation passes: Rust no-default workspace matrix 432
  tests; Rust all-feature workspace matrix 547 tests; warnings-denied
  all-target Clippy; all-feature benchmark compilation; Go `test` and `vet`;
  Rust formatting; `git diff --check`; and the project SOW audit.

### 2026-07-24 - reclamation fixed-point preflight

- Focused permanent Rust coverage proves that the replacement probe is
  allocation-free, leaves its arena unchanged, discovers the exact tree/blob
  pages needed by the real edit, and makes that edit succeed without a late
  replacement-list omission. Separate coverage proves lower ordinary bitmap
  candidates cannot displace verified reclaimed pages and oversize reclaimed
  input fails before scope binding.
- Rust workspace matrices pass: 435 no-default tests and 550 all-feature
  tests. The complete existing late-binding regression group also passes,
  including its 512- and 4,096-source bounded-memory cases. Warnings-denied
  no-default test Clippy passes.
- Go is unchanged because no Go lock-bound finalization caller exists yet.
  Full cross-language validation remains required for the next integrated
  lifecycle checkpoint.

### 2026-07-24 - dynamic selected-reclaim composition

- Focused Rust coverage proves the count-only blob boundaries, rejects a
  partially bound reclaimed authority, preserves the unbound allocation-free
  probe, and publishes the selected Linux fixture using the dynamically
  derived protected list under the held operation lock.

### 2026-07-24 - reclamation bitmap-finalization capacity

- Focused Rust tests pass for the two-leaf finalization path and its typed
  pre-binding storage failure, together with the existing locked-reclamation
  and Linux-finalizer groups.
- Rust workspace matrices pass: 439 no-default tests and 554 all-feature
  tests. Rust formatting, warnings-denied all-target Clippy, and all-feature
  benchmark compilation pass.
- Go `test ./...` and `vet ./...`, `git diff --check`, and the project SOW
  audit pass. Go behavior is unchanged; this remains Rust-only internal
  capacity preparation.

### 2026-07-24 - read-only bitmap-finalization preview

- Focused Rust coverage passes for the read-only/replay match, pre-traversal
  output capacity failure, source-failure cleanup, and the complete locked
  reclamation group.
- Rust workspace matrices pass: 442 no-default tests and 557 all-feature
  tests. Rust formatting, warnings-denied all-target Clippy, and all-feature
  benchmark compilation pass.
- Go `test ./...` and `vet ./...`, `git diff --check`, and the project SOW
  audit pass. This checkpoint remains a private prerequisite for the later
  retirement/blob/tree fixed point, not its completion.

### 2026-07-24 - bounded protected-set convergence

- Focused matched Go/Rust page-index tests pass for monotonic convergence,
  exact lower/upper committed-page bounds, callback and limit cleanup,
  invalid seed rejection, and zero heap allocations after workspace setup.
  The existing matched equality tests cover full-stream corruption detection
  after an earlier mismatch.
- Full validation passes: `go -C v4/go test ./... -count=1`, `go -C v4/go vet
  ./...`, `go -C v4/go test -race ./internal/exactv4 -count=1`; Rust 529
  no-default and 650 all-feature tests; Rust format, warnings-denied
  all-target/all-feature Clippy, and all-feature benchmark compilation.
  `git diff --check` and `.agents/sow/audit.sh` pass before the final SOW
  checkpoint update and are rerun with it.

### 2026-07-25 - lean Rust foundation

- The active Rust module graph is now the replacement implementation, not the
  previous 60,827-line internal graph. Its first foundation has 2,047 physical
  source lines including embedded unit tests; every active source and test file
  is below 500 lines.
- The first public slice is `ImmutableReader::open`. It opens without following
  the final symlink on supported Unix platforms, requires a regular aligned
  file, rejects a present canonical `.readers` sidecar, reads exactly the two
  meta pages, requires exact immutable length, and exposes the selected static
  identity, generation, counts, and factual meta selection.
- A proposed public `CreateImmutable` API was removed before commit because
  section 14 explicitly forbids it. Canonical empty-image encoding remains
  private test evidence until the approved `CreateLive` workflow owns creation.
- A permanent test proves ordinary immutable open succeeds even when the
  selected range root points at a malformed non-meta page. This is intentional:
  open performs O(1) bootstrap and does not silently run `Validate`.
- The active checksum implementation is a compact compile-time-table CRC-32C.
  The 270-line architecture-specific acceleration was not retained because no
  benchmark establishes that meta-page checksum speed matters. Hardware
  specialization may return only if representative validation benchmarks prove
  it useful.
- The active crate currently depends only on `libc`. Obsolete `no_std`/feature
  combinations and unused digest/random dependencies are not part of this
  slice. Safe non-follow open on non-Unix platforms currently returns
  `Unsupported`; Windows support remains required before final acceptance and
  will not silently follow reparse points.
- Validation passes on the current toolchain and the declared Rust 1.74 MSRV:
  34 unit tests and one public integration test. Warnings-denied all-target
  Clippy, formatting, and `git diff --check` pass.
- The prior implementation files remain present but are outside the compiled
  module graph. They will be removed only with the separately required explicit
  deletion authorization; the interrupted uncommitted normal-range file remains
  untouched and excluded from this milestone.

### 2026-07-25 - range-tree simplification plan

- The old exact contract permitted reachable empty leaves and compensated with
  per-child counts and endpoint summaries. That mechanism caused the audited
  traversal failures and conflicts with the replacement architecture's
  zero-root-only empty representation.
- The normative tree contract now uses nonempty slotted pages. Each branch key
  is the exact first key in its nonempty child; point lookup is the ordinary
  greatest-key-not-after-target descent at every level. Deleting the final leaf
  entry removes that child, and an empty complete tree becomes root zero.
- All ordered indexes use the same branch-key direction and slotted-page
  convention. Tree-specific codecs retain compact fixed range cells: IPv4 leaf
  cells are 12 bytes and branch cells 8 bytes; IPv6 cells are 36 and 20 bytes.
  The only common overhead is one two-byte slot per cell.
- Normal range access checks page number, header, type, family, level, slot, and
  selected-cell bounds but does not check page CRCs or scan unselected records.
  One fixed 4 KiB caller-owned buffer and positional reads keep point lookup
  allocation-free and independent of file size.

### 2026-07-25 - immutable direct lookup implementation

- The public immutable reader now exposes family-specific direct point lookup.
  It rejects the wrong family or membership value kind before page I/O.
- The range path is 182 physical lines. Its functions are each below 30
  non-comment lines and at or below cyclomatic complexity 9. It uses checked
  positional reads and one stack-owned page buffer; the selected path is bounded
  by the exact maximum tree level.
- Permanent tests cover empty roots, IPv4 gaps and inclusive boundaries, direct
  value zero, two-level real-first-key descent, full-space IPv6 through the
  maximum address, out-of-bounds children, selected malformed pages, and the
  deliberate absence of data-page CRC checks during ordinary lookup.
- A thread-local allocation counter proves a warmed successful point lookup
  performs zero heap allocations. A public integration test independently
  encodes a checksummed three-page IPv6 image, opens it, and reads the expected
  value.
- Validation passes on the current toolchain and Rust 1.74: 40 unit tests and
  one public integration test. Warnings-denied all-target Clippy, formatting,
  and `git diff --check` pass.

### 2026-07-25 - immutable direct cursor implementation

- The public immutable reader now opens family-specific forward and backward
  direct-range cursors. Seek returns a containing interval when one exists or
  the nearest interval in the chosen direction.
- A cursor retains one 4 KiB page and a fixed 31-frame ancestor path. It
  re-reads branch pages only when crossing a leaf boundary, performs no heap
  allocation during warmed movement, and performs the same selected-path safety
  checks as point lookup without implicit data-page CRC validation.
- Permanent tests cover both directions across leaf boundaries, forward and
  backward seek through ranges and gaps, end-of-tree behavior, and zero
  allocations on a step that crosses into another leaf. The public integration
  fixture exercises the exported IPv6 cursor.
- The cursor implementation is 319 physical lines; every function is at or
  below cyclomatic complexity 8. All active implementation and test files
  remain below 500 lines.
- Validation passes on the current toolchain and Rust 1.74: 43 unit tests and
  one public integration test. Warnings-denied all-target Clippy and formatting
  pass.

### 2026-07-25 - allocator and retirement simplification plan

- The free bitmap covers at most `2^32` pages. Its fixed radix makes the maximum
  root-to-leaf path four pages, so the meta now owns exactly four optional
  allocator-reserve page numbers. A transaction uses these before aligned tail
  growth when it first copies a committed bitmap path. This breaks allocator
  self-reference without a fixed-point planner or file-sized workspace.
- A reserve page is meta-owned scratch: it is neither reachable, free, nor
  retired, and its existing bytes are irrelevant. Abort leaves the selected
  meta authoritative, so an overwritten reserve remains safely reusable. A
  successful commit removes consumed reserve numbers and replenishes empty
  slots from aligned tail growth.
- Every replaced committed page is inserted into one ordered retirement tree
  under the target transaction, even when no old reader currently exists.
  `Reclaim` alone moves complete safe generations into the free bitmap. This
  gives one uniform commit path and deliberately permits a bounded reclamation
  delay instead of performing reader-dependent allocator finalization inside
  every data commit.
- Retirement leaves store canonical contiguous page extents keyed by
  `(retired_by_txn, first_page)`. They do not point to separate page-list blobs.
  The transaction updates the retirement tree after other roots; its first COW
  right-path copies are added to that same private tree, after which further
  inserts update private pages in place. This removes blob planning, sorting,
  deduplication workspaces, and recursive retirement-tree finalization.
- These are internal physical choices. Reader visibility, explicit reclamation,
  commit durability, public logical operations, and the absence of permanent
  history are unchanged.

### 2026-07-25 - live coordination simplification plan

- The previous sidecar protocol stored process IDs, process-start tokens,
  thread IDs, claim nonces, slot states, and per-slot checksums, then required
  recovery state for interrupted slot writes. None is needed when the slot's
  byte-range lock itself is held for the complete reader lifetime.
- The replacement sidecar has one checksummed 4 KiB header followed by fixed
  16-byte reader slots. A live reader holds an exclusive
  open-file-description lock on its slot and stores only
  `(selected_txn, bitwise_not_selected_txn)`. The operating system releases the
  lock when the last descriptor for that open description closes. Therefore an
  available slot lock is direct proof that no conforming reader owns the stale
  bytes; no PID liveness inference or persistent slot transition is required.
- One gate byte is shared while a reader selects a generation and claims or
  releases its slot. Commit and reclamation hold it exclusively from their
  reader scan through metadata publication. This makes transaction-zero
  registrations unnecessary: a writer cannot observe the interval between
  reader generation selection and slot publication.
- A separate open-description byte lock is the writer lease. A shared lifetime
  lock on the main file prevents live-to-immutable transition while a handle is
  open. Every critical mutation rechecks the originally opened main and sidecar
  identities against their canonical paths. Forked handles reject every
  explicit operation; destructors only close descriptors and perform no I/O.
- Linux and macOS expose the required open-file-description byte-range locks;
  Windows exposes equivalent per-handle byte-range locks. A platform without
  this automatic-release ownership model returns `Unsupported`; traditional
  process-associated POSIX locks remain forbidden. FreeBSD support requires
  separate proof of an equivalent primitive before final cross-platform
  acceptance.
- This is an internal physical simplification. It preserves explicit live
  mode, caller-sized reader capacity, one writer, pinned reader generations,
  fail-closed malformed active slots, crash-safe stale-slot reuse, and the
  commit/reclamation barrier. The obsolete detailed sidecar wire section must
  be replaced with this proven protocol before the SOW closes.

### 2026-07-25 - direct live vertical slice

- The obsolete reader-sidecar section was replaced by the exact compact
  protocol: one checksummed 4 KiB header, 16-byte transaction/complement slots,
  open-description slot ownership, one writer lock, and one
  registration/publication gate. The normative section shrank by 1,270 lines.
- Rust now exposes empty live creation, registered live readers, an exclusive
  live writer, ordered direct IPv4/IPv6 assignment and clear, whole-draft
  abort, and alternate-meta commit with
  `NotCommitted | Committed | OutcomeUnknown`.
- A public file-backed test proves two generations, exact nested overwrite
  order, old-reader pinning, reader-capacity exhaustion/reuse, writer exclusion,
  no-op/abort non-publication, and full-space IPv6 endpoints.
- The active Rust module graph is 6,387 physical source lines including
  embedded unit tests. Every active implementation file is at most 473 lines.
  This is inside the directional size envelope, not a completion claim.
- Current validation is green: 71 unit tests, four public integration tests,
  warnings-denied all-target Clippy, formatting, `git diff --check`, Rust 1.74,
  and the project SOW audit.
- This milestone is Linux-proven only. macOS uses the same native lock API but
  is not yet target-tested; Windows locking/opening is not implemented; FreeBSD
  still lacks a proven equivalent primitive. Membership, JSON metadata,
  reclamation, explicit validation/recovery, snapshots, crash injection,
  update-ipsets-shaped benchmarks, and final complexity review remain pending.

### 2026-07-25 - live durability and reclamation plan

- Crash proof uses child processes terminated at named creation and commit
  boundaries. The hooks exist only in Rust test builds; normal builds compile
  them to no-ops. Restart tests inspect the real files, open them through the
  public API, and prove that pre-publication state is invisible, an ambiguous
  meta write selects one complete generation, and synchronized publication is
  visible.
- Separate process-death tests prove that the operating system releases reader
  slots and the writer lease. The next conforming opener clears stale slot
  bytes while holding the slot lock; no process identity or liveness check is
  added.
- Known creation failures clean only paths that still name the exact retained
  inode. Failures after a path can no longer be proved to name that inode remain
  `OutcomeUnknown` with possible residue; cleanup never removes a replacement.
- `Reclaim(max_transactions, max_pages)` remains the approved clean-writer,
  auto-publishing operation. It holds the existing exclusive gate from the
  stable reader scan through commit, selects only complete oldest safe
  transaction groups, and streams their extents directly into the private free
  bitmap. It keeps no page list proportional to the file.
- Reclamation deletes selected retirement records in key order. Committed
  bitmap and retirement pages copied by that maintenance transaction are
  retired under its new transaction ID before the alternate meta page is
  published. A pinned older reader blocks unsafe groups; releasing it permits
  a later reclaim and subsequent allocation may reuse those pages.

### 2026-07-25 - live durability and reclamation implementation

- Rust now exposes the clean-writer `Reclaim` operation. It selects complete
  oldest safe transaction groups under both caller limits, reports the exact
  selected transaction/page counts, and uses the ordinary alternate-meta commit
  result. `WorkLimitTooSmall` reports the first safe group's exact page count
  before mutation.
- Reclamation keeps only counters, one extent, and fixed tree/bitmap paths. A
  pinned reader blocks unsafe groups; after that reader closes, reclamation
  auto-publishes one new generation. A resource-budget failure discards the
  complete private draft, leaves the old generation readable, and permits a
  later retry.
- Test-only child-process crash points cover creation, ordinary commit, and
  reclamation before private-page synchronization, after synchronization, after
  the alternate-meta write, and after meta synchronization. Restart selects
  only a complete generation. Separate child exits prove automatic reader-slot
  and writer-lease release without process-liveness metadata.
- Active malformed or future reader records fail closed; unlocked stale bytes
  are cleared before reuse. Creation now distinguishes failures that left no
  artifact from failures whose exact cleanup could not be proved, and retains
  both the original and cleanup causes.
- The active implementation graph is 6,935 physical lines, including embedded
  tests; every implementation file remains at or below 473 lines. All new
  creation, reclamation, and retirement functions are at or below cyclomatic
  complexity 9. Thirteen pre-existing algorithmic tree/bitmap/range-page
  functions remain above that review signal and require the final clarity
  review; the limit is not being waived.
- Validation passes on the current toolchain and Rust 1.74: 84 tests pass and
  one subprocess entry point is intentionally ignored by the parent harness.
  Warnings-denied all-target Clippy, formatting, `git diff --check`, and the SOW
  audit pass. Go remains unchanged. Injected short-write/sync errors,
  cancellation, cross-platform execution, and later format surfaces remain
  pending.

### 2026-07-25 - compressed metadata implementation plan

- The approved contract is one optional opaque byte string. The SDK does not
  parse JSON. Absence, empty bytes, and every other byte sequence remain
  distinct; the uncompressed limit is exactly 1,048,576 bytes.
- Rust will use `flate2` 1.1.9 with its default pure-Rust backend for normal
  RFC 1950 zlib compression and decompression. This release supports Rust 1.67
  and therefore the project's Rust 1.74 minimum. If normal compression would
  exceed the format's exact stored-block bound, a small local RFC 1950 stored
  encoder will produce the guaranteed bounded representation.
  Evidence: `rust-lang/flate2-rs @
  19ddb18bf11199858fbc6504d079448fafd1606e`,
  `Cargo.toml:1-8,21-30,41-64` and `src/mem.rs:163-190,450-490`.
- Writer compression is charged against `max_heap_bytes` before allocation and
  completes during `SetMetadataJSON`, never during commit. Compressed bytes are
  written to at most 260 forward COW pages using a fixed page-number array.
  Replaced committed chunks enter the existing retirement tree; clear needs no
  new metadata page.
- Reads walk only the selected metadata chain and stream zlib output directly
  into caller storage. They enforce exact chain geometry, one complete stream,
  Adler-32, declared input/output lengths, and no trailing stream or bytes, but
  deliberately do not perform data-page CRC validation. Too-small output is
  rejected before touching it.
- A draft records whether its single metadata stage was consumed. A second stage
  and oversized caller input fail before mutation. Compression, allocation,
  page-write, old-chain, or retirement failure follows the existing whole-draft
  abort path. Writer reads use the draft metadata root, giving exact
  read-your-writes without retaining a second uncompressed copy.
- Permanent tests will cover absent/empty/arbitrary bytes, highly compressible
  and incompressible maximum input, exact bounds, pinned generations,
  read-your-writes, equal-byte replacement, clear/no-change, one-stage state,
  too-small buffers, resource failure, malformed chains, malformed/trailing
  zlib streams, and ordinary reads with a bad page CRC. Cancellation remains a
  required common operation layer and will be added consistently rather than
  inventing a metadata-only token shape.

### 2026-07-25 - compressed metadata implementation

- Rust now exposes metadata presence/length, caller-buffer reads, and bounded
  allocation helpers on immutable readers, live readers, and the live writer.
  The writer exposes exact replacement and clear staging with read-your-writes.
  Empty bytes remain present; clear absence remains an O(1) no-op; equal-byte
  replacement still publishes a new generation.
- Metadata uses the exact type-13 forward COW chain. Reads stream each selected
  compressed chunk directly into caller output, reject malformed links,
  offsets, padding, zlib headers, dictionaries, checksums, concatenation,
  trailing bytes, and declared-length mismatch, and deliberately ignore page
  CRCs on this ordinary path. Replacement pages use the existing retirement
  tree and remain protected by pinned old readers.
- The exact stored-block output bound is the minimum writer heap requirement.
  With an additional fixed 512 KiB budget, the pinned pure-Rust miniz backend
  first attempts normal DEFLATE and falls back to a local stored-block encoder
  if needed. Linux allocation measurement observed 319,326 bytes of compressor
  overhead for the maximum repeated payload; a permanent test checks both
  maximum repeated and pseudo-random inputs remain within the 512 KiB charge.
  A maximum incompressible payload succeeds with exactly the 1,048,667-byte
  minimum budget and writes the full 260-page chain.
- Oversized input and a second metadata stage fail before mutation. Compression,
  heap budget, page allocation/write, old-chain, and retirement failures use
  whole-draft abort. Once metadata is staged, further data mutation is rejected;
  only commit or abort remains valid.
- Metadata-only crash tests cover every durable commit boundary and expose
  either absence or the complete value after restart. Public integration tests
  cover maximum-size storage, pinned-generation replacement/clear, reclamation,
  exact buffer errors, staged reads, equal replacement, and preservation of an
  existing draft after an oversized precondition error.
- Validation passes on the current toolchain and Rust 1.74 with 102 passing
  tests and one intentionally ignored subprocess entry point. Warnings-denied
  all-target Clippy, formatting, metadata complexity analysis, and locked
  dependency resolution pass. Every new function is at or below cyclomatic
  complexity 9. The active implementation graph is 7,623 physical lines
  including embedded tests; every active source and test file is at or below
  490 lines.

### 2026-07-25 - immutable named-feed catalog plan

- This is the first public slice of the approved membership implementation.
  Callers receive copied feed names and observable indexes; no mutation accepts
  an index, bitmap word, or membership ID as authority.
- Rust will expose one fixed-capacity `FeedName`, one copied `{name,index}`
  entry, exact name lookup, and an allocation-free cursor in ascending
  feed-index order on immutable and live readers. Empty catalogs are valid.
- Lookup and cursor traversal read only their selected root-to-leaf paths.
  They perform checked page, slot, record-length, name, ordering, child, and
  transaction bounds needed for safe access, but deliberately do not calculate
  page CRCs or cross-validate the complete pair of catalog trees. That remains
  explicit `Validate` work.
- The numeric cursor retains one fixed ancestor stack and one page buffer.
  Variable-length name records are decoded into the fixed public name value, so
  neither lookup nor cursor steps allocate through the heap.
- This mirrors the small proven cursor shape in
  `cberner/redb @ fe0141159c73`,
  `src/tree_store/btree_cursor.rs:85-193`, and the page-local binary search plus
  ancestor cursor used by `LMDB/lmdb @ 389e1009a86c`,
  `libraries/liblmdb/mdb.c:6700-6795,7860-7945`. v4 keeps fixed arrays rather
  than either library's general-purpose dynamic transaction machinery.
- Permanent tests will exercise boundary-length valid names, every invalid-name
  class, exact misses, multi-page/multi-level trees, malformed selected records
  and children, no implicit CRC check, ascending enumeration, end stability,
  fork rejection for live cursors, and zero warmed-path allocations. Catalog
  mutation, membership views, dictionary interning, and membership range
  changes remain the following slices.

### 2026-07-25 - immutable named-feed catalog implementation

- Rust now exposes a fixed-capacity validated `FeedName`, copied `FeedEntry`,
  exact name lookup, and an ascending feed-index cursor on both immutable and
  live readers. Invalid caller names and wrong database kinds fail before
  catalog page access.
- Name lookup follows only the selected variable-key tree path. Enumeration
  retains one fixed ancestor stack and one page buffer, detects declared-count
  and index-order disagreement as it walks, and returns copied names without a
  heap allocation. Neither path accepts an index back as mutation authority.
- Selected page, slot, variable-record, name, index-limit, transaction, level,
  and child bounds are checked. Ordinary reads deliberately ignore page CRC and
  do not cross-validate the complete name tree, numeric tree, or used bitmap;
  those are explicit validation responsibilities.
- Permanent tests cover empty and branched catalogs, exact misses, maximum
  255-byte names, every caller grammar class, malformed selected records and
  children, declared-count and index-order disagreement, bad ordinary-path CRC,
  live cursor process ownership, and zero warmed-path allocations.
- The current toolchain and Rust 1.74 each pass 111 tests with one intentionally
  ignored subprocess entry point. Warnings-denied all-target Clippy, formatting,
  `git diff --check`, the SOW audit, and complexity analysis pass. Every new
  function is at or below cyclomatic complexity 9. The active production-module
  graph is 8,172 physical lines including embedded tests; every active
  production and separate test file remains at or below 490 lines.

### 2026-07-25 - lazy membership view plan

- Membership address lookup will resolve the selected range's private
  membership ID inside the SDK and return a reader-borrowing
  `MembershipView`; the ID itself remains hidden.
- The view exposes the checked word count, random word reads, and sequential
  caller-buffer word batches. It never materializes a legal maximum bitmap and
  performs no heap allocation on a warmed successful read.
- Inline dictionary values retain only their ID-leaf location and re-read that
  selected page when words are requested. Blob values retain only the exact
  blob root and descend the fixed 16-byte offset tree directly into 4,048-byte
  chunks. Batched reads copy complete available runs from each selected chunk
  rather than performing one tree descent per word.
- Ordinary lookup checks selected range, dictionary, and blob bounds, record
  geometry, ownership, lengths, storage mode, and child levels. It deliberately
  does not verify page CRCs, recompute SHA-256, scan all dictionary entries,
  validate refcounts globally, or cross-check every set bit against the catalog;
  those remain explicit `Validate` responsibilities.
- Permanent tests will cover inline and multi-leaf blob values, random and
  batched reads, end truncation, wrong family/kind, absent addresses, malformed
  selected ID/blob records and children, ordinary bad CRCs, live process
  ownership, maximum declared word counts without allocation, and zero
  warmed-path allocations.

### 2026-07-25 - lazy membership view implementation

- Rust membership point lookup now resolves the range value through the
  selected ID tree and returns a reader-borrowing `MembershipView`; no public
  method reveals or accepts the internal membership ID.
- The view exposes checked word count, random words, caller-buffer batches, and
  read-only feed-index membership tests. Inline values re-read one selected ID
  leaf. Blob batches descend the fixed offset tree once per 4,048-byte chunk and
  cross chunk boundaries without materializing the complete bitmap.
- The selected path enforces ID namespace, refcount presence, word/byte bounds,
  storage geometry, child/page/level/transaction bounds, exact blob coverage,
  canonical nonzero final words when observed, and fork ownership. It does not
  calculate ordinary-path CRCs, hashes, global refcounts, or catalog-wide bit
  validity.
- Permanent tests cover inline and two-leaf blob values, truncated batched
  output, maximum 67,108,864-word declarations as constant-size views, missing
  IDs, trailing zero words, malformed selected ID/blob records and children,
  wrong kind/family, bad ordinary-path CRCs, live process ownership, and zero
  warmed lookup/read allocations.
- The database open tests moved to their own logical test file rather than
  allowing `database.rs` to grow past the file-size review target.
- The current toolchain and Rust 1.74 each pass 119 tests with one intentionally
  ignored subprocess entry point. Warnings-denied all-target Clippy, formatting,
  `git diff --check`, the SOW audit, and complexity analysis pass. Every new
  function is at or below cyclomatic complexity 9. The active production-module
  graph is 8,681 physical lines including embedded tests; every active
  production and separate test file remains at or below 490 lines.

### 2026-07-25 - feed-catalog mutation foundation plan

- The existing COW B+tree is the one local pattern to reuse, but it currently
  assumes fixed-width keys and leaves. The exact feed catalog instead has
  variable-length name records in both name-tree levels and numeric-tree
  leaves. Adding a second tree implementation would duplicate path copying,
  split, delete, retirement, and corruption handling.
- Generalize the existing tree codec so each format supplies record selection,
  key decoding, branch encoding, and maximum cell sizes. Splits remain a
  bounded page-local pass and balance actual encoded bytes rather than record
  counts; no heap allocation or scratch file is introduced.
- Variable first-key replacement can grow a catalog record. The generic tree
  will split that one private branch page when the replacement no longer fits,
  then propagate the split through the already bounded COW path. This avoids
  permanent slack and handles cumulative growth of different branch keys.
- Add permanent fixed- and variable-record tree tests before using the
  generalized tree for catalog mutation. Then add the two catalog codecs and
  update both indexes atomically through `DraftStore`, retiring every replaced
  committed path through the existing retirement mechanism.
- Feed-index ownership remains internal. The public mutation surface will be
  exposed only as one coherent transaction-bound `FeedRef` API; no temporary
  public raw-index method will be added. A used-index bitmap and feed creation,
  exact lookup, enumeration, and rename will be integrated before this slice is
  called public.
- Validation will include long names, mixed record sizes, multi-level splits,
  first-key growth, deletion, abort, durable reopen, index agreement, lowest
  reusable index, allocation counts, current and Rust 1.74 suites,
  warnings-denied Clippy, formatting, complexity, diff, and SOW audit checks.

### 2026-07-25 - membership interning and ownership plan

- Public callers identify feeds by transaction-bound `FeedRef` values. They
  never receive or supply feed indexes, membership IDs, raw bitmap words, or
  precomputed combinations.
- The SDK builds canonical membership bitmaps lazily from existing dictionary
  entries plus requested feed changes. SHA-256 is calculated in fixed-size
  batches, hash collisions are resolved by comparing every word, and large
  values are written directly into the final immutable blob tree. This requires
  neither a whole-bitmap allocation nor a sorting or spill file.
- Every newly interned membership ID starts with refcount zero. Range-tree
  insertion and removal record signed refcount changes in an operation-private,
  page-backed COW tree. Commit drains this bounded-memory delta tree, applies
  checked refcounts, and deletes zero-reference dictionary entries, reverse-hash
  entries, used-ID bits, and blob pages before publishing metadata.
- Operation-private delta pages are discarded, not retired. Committed
  dictionary/blob pages replaced or removed by COW remain protected through the
  existing reader-safe retirement path.
- The membership-ID namespace reuses the lowest free ID. It may grow to the
  full `u32` space and shrinks only when trailing IDs become unused; feed-index
  capacity remains monotonic while individual holes remain reusable.
- Permanent tests will cover duplicate interning, deliberate hash collisions,
  inline and multi-page blobs, lowest-ID reuse, reference-count changes caused
  by range split/coalescing, zero-reference cleanup, abort, commit/reopen,
  corruption and arithmetic failures, bounded heap behavior, current and Rust
  1.74 suites, warnings-denied Clippy, formatting, complexity, diff, and SOW
  audit checks.

### 2026-07-25 - advanced membership mutation implementation

- Rust now exposes one explicit advanced membership transaction. Callers use
  transaction-bound `FeedRef` and `MembershipRef` values; public methods neither
  accept nor reveal membership IDs, feed indexes, bitmap words, or caller-built
  combinations.
- Feed lookup, ensure, enumeration, rename, and delete operate on both catalog
  indexes and the sparse used-index bitmap in one private draft. Allocation
  chooses the lowest zero bit, a committed deletion makes that bit reusable,
  and deleting a feed removes its bit from every stored address membership
  before removing the catalog entry.
- The existing COW B+tree is now codec-driven for fixed and variable records.
  It balances splits by encoded bytes and handles first-key growth without a
  second tree implementation, whole-tree workspace, external sort, or temporary
  file.
- Membership construction hashes canonical words in fixed batches, compares
  all words after a SHA-256 collision, interns equal values, stores large values
  directly in the final unpublished blob tree, and returns only an opaque
  transaction reference. The page-backed private delta tree aggregates exact
  range-record refcount changes; preparation removes every zero-reference
  dictionary/hash/used-bit/blob entry before publication.
- `Replace`, `Union`, `Difference`, `Intersection`, and `Xor` apply per address.
  Overlapping inputs retain call order, changed intervals split only where
  needed, empty results disappear, and adjacent equal results coalesce. A
  200-step randomized non-idempotent reference test checks the range rewrite
  after every operation.
- Automatic transaction destruction performs no I/O. Dropping the transaction
  leaves its unpublished draft for explicit writer `Abort` or `Close`; a
  permanent test proves that `Abort` still has work to discard. Explicit commit
  of a builder-only logical no-op discards its unpublished pages, returns
  `NoPendingTransaction`, and leaves the writer reusable.
- Permanent tests cover 1,000 mixed-length feed names and multi-level catalog
  splits; commit/reopen and abort; every membership operation; dictionary
  deduplication; deliberate hash collision full comparison; inline and
  multi-page blobs; exact refcounts; unused-combination cleanup; membership-ID
  and feed-index reuse; feed deletion; stale-reference rejection before
  mutation; full range clearing; committed blob retirement; and operation-
  private delta cleanup.
- The compiled active Rust graph is 13,047 physical lines excluding separate
  test files; every active production file is below 500 lines and every function
  added or changed in this slice is at or below cyclomatic complexity 9. The
  graph is 78% smaller than the replaced 60,827-line implementation, but it is
  still above the directional 5,000-line goal and above its 10,000-line warning
  range. This is a safe functional checkpoint, not a claim that the final Rust
  SDK is lean enough. Remaining work must keep testing whether modules can be
  removed or shared without obscuring the format or weakening bounded behavior.
- Current and Rust 1.74 all-feature suites each pass 133 tests with one
  intentionally ignored subprocess entry point. Warnings-denied all-target
  Clippy, formatting, benchmark compilation, `git diff --check`, and the SOW
  audit pass. The complexity result was incorrectly recorded as zero-warning at
  this checkpoint: the next exact compiled-graph run found five active
  violations in the already-present free-bitmap and slotted-page code. They are
  corrected and validated in the next slice. Disconnected obsolete source is
  not included in the compiled-graph claim.

### 2026-07-25 - exact direct workflows and retention plan

- The next Rust slice implements the first update-ipsets adoption milestone:
  complete direct-map replacement and exact `retention` refresh for both address
  families. It does not add feed import, snapshots, validation, recovery, Go, or
  C ABI work.
- A workflow starts only on a clean matching writer and marks its draft as
  input-incomplete. Dropping the Rust workflow performs no I/O; the unfinished
  draft cannot be committed or mixed with another mutation and requires
  explicit writer `Abort` or `Close`.
- Repeated synchronous source calls preserve batch and record order. Direct
  replacement applies each supplied value immediately to an initially empty
  private range root. Retention first builds the desired address union with the
  new refresh value, then streams the committed ranges and overlays each old
  value only where desired coverage remains. No caller sorting, whole-input
  allocation, external scratch, or temporary file is used.
- `FinishInput` scans the committed and private canonical roots once to produce
  exact `Cardinality129` change classes and detect logical equality. A no-op
  discards all private pages and leaves the writer clean. A changed result
  retires the detached committed range tree through a fixed-depth page walk,
  marks the draft commit-ready, and permits only one optional metadata stage,
  `Commit`, or `Abort`.
- One explicit thread-safe cancellation token is checked before source pulls,
  between records, during retention overlay, comparison, and detached-tree
  retirement. Source failure, invalid source protocol, cancellation, malformed
  selected pages, budget exhaustion, or storage failure aborts the complete
  workflow under the existing writer cleanup rule.
- Permanent tests will cover unordered overlap/order, exact reports, empty/full
  IPv4 and IPv6, stable retention values, partial splits, removal and
  reappearance, no-op cleanup, metadata in the same generation, dropped-input
  commit rejection, source failure after earlier batches, cancellation, page
  retirement/reclamation, bounded allocation behavior, Rust 1.74, formatting,
  warnings-denied Clippy, benchmark compilation, complexity, diff, and SOW
  audit checks.

### 2026-07-25 - exact direct workflows and retention implementation

- `DirectReplacement` and the special `RetentionRefresh` workflow now implement
  the planned full-file semantics for IPv4 and IPv6. Input is applied directly
  to the unpublished destination tree in supplied order; neither workflow sorts,
  retains the complete input, creates spill files, or creates a temporary v4
  database.
- Retention preserves the committed timestamp for every address still present,
  assigns the refresh timestamp only to newly present addresses, removes absent
  addresses, and treats a later reappearance as new. It rejects databases whose
  15-byte value tag is not exactly `retention`.
- `FinishInput` returns one exact 18-field report. A logical no-op discards the
  complete draft; a changed result owns the draft until explicit commit or
  abort. Dropping an input or prepared handle performs no I/O and leaves only
  explicit writer abort/close available; bare mutation, metadata access, and
  commit cannot publish the abandoned operation.
- One cloneable cancellation token is checked through input, comparison,
  retention overlay, detached-tree retirement, optional metadata compression,
  private-page preparation, and the last safe point before publication. Source
  failure and cancellation discard the whole unpublished workflow.
- Permanent tests prove supplied-order overlap, exact reports, same-generation
  metadata, logical no-op cleanup, full-space IPv6 cardinality without overflow,
  retention full-delta behavior, source failure after accepted input,
  cancellation, abandoned handles, 2,000-record multi-level tree retirement and
  reclamation, and empty replacement.
- Two deterministic property tests execute 100 randomized direct replacements
  and 100 randomized retention refreshes against scalar 128-address reference
  maps. A counting allocator proves that ingestion and `FinishInput` allocate
  zero engine heap objects for a 1,000-record borrowed slice.
- Current and Rust 1.74 all-feature suites each pass 141 tests with one
  intentionally ignored subprocess entry point. Formatting, warnings-denied
  all-target Clippy, and all-feature benchmark compilation pass.
- Rechecking the exact compiled production graph exposed five older complexity
  violations that contradicted the previous recorded result. The free-bitmap
  mutation and page-validation functions were split by purpose, as was the
  566-line draft-storage module. The resulting graph has 63 files and 14,573
  physical lines; its largest file has 471 lines and Lizard reports zero
  functions above cyclomatic complexity 9. This is cleaner and easier to audit,
  but it is still materially above the directional whole-engine line goal and
  remains an explicit reduction pressure for later Rust slices.

### 2026-07-25 - exact named-feed workflow plan

- The next Rust slice implements `BeginCreateFeed` and `BeginReplaceFeed` for
  membership files plus the reusable ordered per-feed cursor required by the
  Phase-1 query surface. Delete, rename, whole-file import, snapshot, validation,
  recovery, Go, and C ABI work are outside this slice.
- Create checks that the exact name is absent, allocates the SDK-owned lowest
  free feed index, and unions every supplied value-free range into that feed.
  Replace checks that the name exists, preserves its index, clears only that
  feed's bit from the private destination map, then unions the complete supplied
  snapshot. Other feeds and metadata remain unchanged.
- Input is accepted in repeated borrowed batches and applied directly to the
  destination's unpublished COW tree. Duplicates, overlap, adjacency, and random
  order are accepted; reversed or wrong-family input aborts the whole workflow.
  There is no input sort, whole-feed buffer, spill file, or temporary database.
- Before reporting, the workflow finalizes its private membership refcount
  deltas and uses one allocation-free forward comparison of the committed and
  private projections of that named feed. The projection cursor filters
  membership IDs by the SDK-owned feed index and coalesces adjacent coverage
  even when other feed bits differ.
- The report's before/after record counts and address classes describe only the
  coalesced named-feed projection. `changed_value_addresses` is always zero.
  After-projection coverage is also the normalized input summary. Empty input is
  a valid cataloged empty feed. Create is always `Changed`; replace returns
  `NoChange` and discards all private work when projected membership is equal.
- The existing cancellation token and prepared-workflow handle are reused.
  Cancellation is checked before source pulls, between records, inside range
  transformations, while filtering raw membership ranges, during comparison,
  metadata staging, and commit. Source, membership, storage, budget, or
  cancellation failure aborts the entire private operation.
- Validation will cover empty and populated create, unordered/overlapping
  replace, preservation of other feeds, projection coalescing, exact reports,
  no-change cleanup, wrong preconditions/family, source failure, cancellation,
  metadata in the same commit, refcount cleanup, index stability, forward and
  backward public per-feed cursors, randomized scalar-reference properties,
  zero per-record engine allocations, Rust 1.74, formatting, warnings-denied
  Clippy, benchmark compilation, exact compiled-graph complexity, diff, and SOW
  audit gates.

### 2026-07-25 - exact named-feed workflow implementation

- `BeginCreateFeed` and `BeginReplaceFeed` now implement the planned
  one-feed-at-a-time membership workflows for both address families. Create
  allocates the lowest SDK-owned free index; replace preserves the existing
  index; empty input leaves an active empty catalog entry; exact precondition
  errors leave the writer clean.
- Replace removes only the selected feed from the unpublished destination map
  before applying the complete supplied snapshot. Other feed bits, other catalog
  entries, and metadata are preserved. Every unordered/duplicate/overlapping
  input range is unioned directly into the final private tree with no sorting,
  whole-input storage, spill file, or temporary v4 file.
- One shared comparison engine now handles direct maps and named-feed
  projections. The feed projection cursor lazily tests SDK-owned membership
  values, coalesces adjacent selected coverage across differing other-feed bits,
  supports forward and backward readers, and allocates nothing during warmed
  traversal. The workflow comparison counts projection intervals during the same
  sweep rather than rescanning.
- Create always reports a catalog change, including an empty feed. Replace
  reports `NoChange` only when the final named-feed projection is identical,
  then truncates every unpublished page and leaves no pending transaction.
  Changed workflows reuse the common prepared handle for one optional metadata
  stage and cancellable commit/abort.
- Source failure, reversed input, wrong-family input, cancellation, membership
  translation/refcount failure, storage failure, and budget failure abort the
  complete workflow. Cancellation is checked across source pulls, records,
  range transformations, projection filtering, comparison, membership-delta
  finalization, metadata, and commit preparation.
- Permanent integration tests cover populated and empty create, unordered
  replace, exact 18-field reports, same-generation metadata, stable indexes,
  preservation and coalesced projection of another feed, forward/backward
  cursors, no-change cleanup, missing/existing-name preconditions, source
  failure, wrong family, cancellation, abandoned input, and full-space IPv6.
- A deterministic property test executes 100 random replacements over a
  128-address scalar set while verifying every address of both the replaced feed
  and an independently preserved feed. A counting allocator proves zero engine
  heap allocations during 1,000-record borrowed-slice ingestion and
  `FinishInput`.
- Current and Rust 1.74 all-feature suites each pass 147 tests with one
  intentionally ignored subprocess entry point. Formatting, warnings-denied
  all-target Clippy, and all-feature benchmark compilation pass. The exact
  compiled production graph has 66 files and 15,481 physical lines; its largest
  file has 471 lines and Lizard reports zero functions above cyclomatic
  complexity 9. The line count remains above the directional goal and must keep
  exerting reduction pressure; this checkpoint does not claim otherwise.

### 2026-07-25 - delete and rename lifecycle plan

- The next Rust slice implements the remaining immediate one-feed lifecycle
  calls: `DeleteFeed(name)` and `RenameFeed(old,new)`. They require a clean
  membership writer, have no range input and no `FinishInput` report, and return
  one prepared lifecycle handle for optional metadata plus commit/abort.
- Delete requires the exact source name, clears only that feed's bit from every
  private membership with cancellation checkpoints, removes both catalog
  indexes, and makes its numeric index reusable only after commit. Every other
  feed and its coverage remains unchanged.
- Rename requires the old name present and the new name absent, changes only the
  catalog name, and preserves the feed index and every membership. There is no
  alias, upsert, or same-name no-op.
- Preconditions and a pre-cancelled token fail before creating a draft. Once a
  draft starts, cancellation, membership/refcount, storage, budget, or metadata
  failure aborts the complete lifecycle change. Dropping the prepared handle
  performs no I/O and leaves only explicit writer abort/close legal.
- The common prepared-operation ownership is factored once and shared with
  report-bearing workflows, avoiding duplicate metadata/commit/abort logic.
  Validation will cover catalog and membership preservation, exact index
  stability/reuse, empty feeds, metadata in the same generation, preconditions,
  cancellation, dropped-handle safety, IPv4/IPv6, Rust 1.74, all static/build
  gates, and exact compiled-graph size/complexity.

### 2026-07-25 - delete and rename lifecycle implementation

- Implemented immediate `delete_feed` and `rename_feed` operations in
  `v4/rust/iprange-livedb/src/live_writer/feed_lifecycle.rs`. Both require a
  clean membership writer and return one prepared handle limited to one optional
  metadata stage plus commit/abort.
- Delete uses the existing membership algebra over the complete address space,
  with cancellation checks between transformed ranges, then removes the exact
  catalog entry. Rename changes only the two catalog indexes. Successful delete
  makes the index the deterministic lowest-free choice for a later create;
  rename preserves the index and all coverage.
- Factored the shared prepared-operation ownership in
  `v4/rust/iprange-livedb/src/live_writer/workflow.rs`. A cancellation or
  metadata error now aborts the entire lifecycle draft for both report-bearing
  and reportless workflows; a dropped handle still requires explicit abort and
  cannot be published accidentally.
- Added five public integration tests in
  `v4/rust/iprange-livedb/tests/feed_lifecycle.rs`. They prove old-reader
  generation isolation, other-feed preservation, index stability and reuse,
  empty-feed deletion, metadata publication/clear, missing/existing-name
  preconditions, pre-cancellation, dropped-handle and invalid-metadata rollback,
  cancellation before publication, direct-mode rejection, and full-space IPv6
  deletion.
- Current and Rust 1.74 all-feature suites each pass 152 tests with one
  intentionally ignored subprocess entry point. Formatting, warnings-denied
  all-target Clippy, and all-feature benchmark compilation pass. The exact
  compiled production graph has 67 files and 15,627 physical lines; its largest
  file has 471 lines and Lizard reports zero functions above cyclomatic
  complexity 9. The implementation remains above the directional line-count
  goal; this checkpoint records that fact rather than weakening the goal.

### 2026-07-25 - membership import implementation plan

- Implement the remaining Phase-1 membership import exactly as section 16.5:
  one clean destination writer, one explicitly selected immutable or live pinned
  source reader, compatible family/tag, different local inode, no `AddRanges`,
  one `FinishInput`, then the common optional metadata plus commit/abort path.
- Rust exposes one source-mode enum borrowing either reader. The Rust lifetime
  prevents source close while the import handle exists; finishing consumes that
  borrow before returning a prepared destination handle. Source indexes and
  membership IDs remain private and are translated only by exact feed names.
- Enumerate the source catalog once, create only missing destination feeds, and
  preserve destination-only feeds. Stream the source range tree once and union
  translated source memberships directly into destination COW state. Empty
  source feeds still create catalog entries.
- Keep source-index, source-membership, translated-membership-deduplication, and
  sparse translated-word state in operation-private B+trees allocated through
  the existing destination page budget. Return those unpublished pages directly
  to the draft's private/free pool before preparation; they never enter the
  durable old-generation retirement list. This keeps heap use fixed, avoids
  per-feed expansion and quadratic bit stacking, and creates no external file.
- Source read/corruption failures abort the complete destination draft without
  poisoning an otherwise healthy destination writer. Destination corruption or
  I/O failures retain the existing unusable-writer rule. Cancellation is checked
  between feeds, source ranges, membership word batches, set bits, and private
  cache pages.
- Validate with exact IPv4 report fields and name-union semantics, empty-feed
  catalog-only change, no-change import, metadata isolation/staging, live and
  immutable source modes, source/destination and compatibility preconditions,
  source failure rollback, budget failure rollback, full-space IPv6, randomized
  reference-model coverage, Rust 1.74, static/build gates, and exact compiled
  graph size/complexity.

### 2026-07-25 - membership import implementation

- Implemented the public borrowed-source import workflow in
  `v4/rust/iprange-livedb/src/live_writer/membership_import.rs`. It accepts only
  an explicit immutable or live pinned source, rejects incompatible
  family/kind/tag and the same local inode before mutation, enumerates the
  source catalog once, translates by exact names, creates missing feeds, and
  unions source coverage while preserving destination-only feeds and metadata.
  A copied source with the same database ID on a different inode is accepted.
- Source-feed, membership, translated-membership, and sparse-word maps live in
  operation-private destination B+trees. Their postorder release returns every
  unpublished page directly to the draft private/free pool; scratch pages never
  enter the durable old-reader retirement list. Import creates no external file
  and open-handle processing performs zero measured heap allocations.
- Source read/corruption and cancellation failures discard the complete draft
  while leaving a successfully cleaned destination writer reusable. Destination
  storage/corruption retains the existing unusable-writer rule. A physically
  truncated source page is correctly reported as source corruption, not as an
  operating-system I/O error.
- The feed-scale test exposed and fixed a shared membership-builder defect:
  adding bit 64 to a one-word membership attempted an out-of-range read instead
  of extending the bitmap. Focused dictionary coverage now proves the
  63-to-64 boundary, and a public 140-feed import proves sparse remapping across
  multiple bitmap words without caller-managed indexes.
- Permanent integration coverage proves exact report counters and named-set
  union, live/immutable modes, old-reader isolation, metadata staging, clean
  no-change, empty feeds, same-database copies, every compatibility gate,
  cancellation, budget and source-read rollback, destination reuse,
  full-address-space IPv6, a deterministic 256-address reference model, and
  sparse high feed indexes.
- Current Rust and Rust 1.74 all-feature suites each pass 161 tests with one
  intentionally ignored subprocess entry point. Formatting, warnings-denied
  all-target Clippy, all-feature benchmark compilation, and diff whitespace
  checks pass. The exact compiled production graph has 70 files and 16,536
  physical lines; its largest file has 476 lines and Lizard reports zero
  functions above cyclomatic complexity 9. The implementation remains above
  the directional line-count goal; this checkpoint does not weaken that goal.

### 2026-07-25 - explicit validation implementation plan

- Implement section 18 as a separate, caller-invoked Rust operation. Normal
  open, lookup, cursor, workflow, mutation, commit, and reclamation paths remain
  non-validating. Invalid content is a completed factual report; source pin,
  budget, cancellation, sink, and cleanup failures remain operation errors.
- Expose one path-based `validate` entry point with explicit
  `ImmutableCurrent`, `LiveCurrent`, and exact offline-candidate modes, a
  caller-bounded validation budget, synchronous finding sink, cancellation,
  stable reason/object enums, and a complete result. Recovery-readable meta
  classification and candidate tokens are shared with the following recovery
  slice rather than duplicated.
- Correct the immutable-source lifetime boundary first. An immutable reader or
  immutable validation holds the main file's shared lifetime lock and rechecks
  the exact no-follow path identity and sidecar absence around bootstrap and
  graph access. Live validation holds the operation gate while it scans the
  table, selects the proven current transaction, and directly claims a slot for
  that transaction before releasing the gate for the scan.
- Use one checked packed claim bitmap to prove global page ownership and the
  reachable/free/retired/reserve partition. Use fixed-depth traversal stacks,
  one reusable page buffer, streamed tree records, and a preallocated flat
  membership-refcount table. No page, range, feed, or membership creates one
  heap allocation. Heap use is calculated and rejected before allocation.
- Validate page CRCs and complete zero/reserved/layout rules before trusting
  pointers. Traverse roots in fixed meta order, stop only the affected
  untrustworthy subtree, continue independent roots/siblings, emit every
  independently established defect once, and never invent logical bounds from
  damaged bytes.
- Prove range ordering/non-overlap/coalescing and exact counts; catalog
  ordering/bijection/used bits; membership canonical bytes, hashes, reverse
  index, unique interning, active feed bits, used IDs, and recomputed refcounts;
  metadata chain/zlib/length; bitmap summaries/limits; retirement
  ordering/coalescing; and the complete page partition.
- Add authenticated external scratch only for the same ownership/refcount
  work when the heap budget cannot hold it. Scratch is caller-authorized,
  bounded, exclusively created, identity-bound, and removed on every terminal
  path; normal operations and snapshots retain zero scratch authority.
- Validate clean direct and membership files, full IPv4/IPv6 spaces, every
  graph kind, bootstrap-only damage, isolated corrupt subtrees, alias/cycle,
  count/index/refcount/partition defects, deterministic finding order, sink
  stop/failure, cancellation, insufficient budgets, scratch cleanup, live pin
  concurrency, offline candidate binding, zero default validation, Rust 1.74,
  allocation/resource bounds, static/build gates, and exact compiled graph
  size/complexity.

### 2026-07-25 - current-generation explicit validation implementation

- Added the caller-invoked `validation::validate` surface and stable finding,
  object, progress, generation, budget, and failure types. Invalid bytes produce
  a completed factual report; cancellation, resource, sink, source, and cleanup
  failures remain separate operation failures with partial progress.
- Implemented immutable-current coordination with a lifetime lock, no-follow
  identity checks, and repeated sidecar-absence checks. Implemented live-current
  coordination with an exclusive gate, exact selected-transaction reader claim,
  bootstrap-only reporting without a transaction-zero slot, and exact slot/gate
  cleanup. Ordinary reader and writer paths still perform no graph validation.
- Implemented deterministic selected-generation checks for page identity, CRC,
  bounds, reserved bytes, ownership/aliasing, range semantics, both catalog
  indexes, feed and membership bitmaps, membership hashes/reverse mappings/
  refcounts, blobs, compressed metadata, retirement extents, and the complete
  reachable/free/retired/reserve allocation partition. Damaged pointers or
  lengths stop only the untrustworthy subtree.
- The ownership claims use two bits per declared page and the membership
  cross-check uses one preallocated open-addressed table. Both are charged
  against the caller's heap budget before allocation. This checkpoint is
  heap-only: recovery-candidate inspection/binding, external scratch, recovery
  output, and the full corruption/resource matrix remain in the next
  implementation slice of this same SOW.
- Permanent tests cover clean empty and populated direct/membership databases,
  factual CRC corruption, immutable and live bootstrap-only reports, exact live
  slot release, and sink-stop partial progress. Current and Rust 1.74
  all-feature suites each pass 170 tests with one intentionally ignored
  subprocess entry point. Warnings-denied all-target Clippy, benchmark
  compilation, formatting, diff checks, and the SOW audit pass.
- The validation files have zero Lizard functions above cyclomatic complexity
  9. Their largest two files are 527 physical lines (479 and 490 non-comment
  code lines) and have separate query/record submodules at real responsibility
  boundaries. The exact compiled production graph is now 88 files and 21,688
  physical lines, with zero functions above complexity 9. This is far above the
  directional whole-engine size goal and is not acceptable as a final
  simplicity result; each remaining slice must remove duplication or justify
  its retained mechanisms with a concrete contract failure.

### 2026-07-25 - recovery-candidate inspection and validation

- This slice implements only the already-approved section-18/19 candidate
  inspection and exact offline-candidate validation contract. It introduces no
  recovery-output behavior and does not change normal open, lookup, mutation, or
  commit paths.
- Candidate classification reads the two physical metadata pages independently.
  Generation-order readability is deliberately narrower than recovery
  readability. Equal identical creation metas and adjacent parity-correct metas
  prove order; swapped, gapped, disagreeing, damaged, or identity-mismatched
  pairs do not. A damaged proven current meta never promotes the previous meta.
- Inspection returns a fixed array of at most two opaque tokens, not a
  file-sized allocation. Immutable mode holds the shared lifetime lock and
  requires sidecar absence. Caller-certified offline mode holds the exclusive
  lifetime lock and may inspect a live/copy source without inferring safety from
  its sidecar. On POSIX, that exclusive byte-range lock requires opening the
  source read-write even though inspection performs no writes.
- Live inspection holds the shared main lifetime lock, strictly binds the
  existing sidecar, takes the operation gate exclusively, reclassifies the
  metas, and checks every active reader slot. Its reader-table scan does not
  clear stale bytes, so the operation is byte-for-byte non-mutating. It returns
  only a proven recovery-readable `Newest` token and has exact typed errors for
  unprovable order, unreadable current metadata, and unavailable coordination.
- `OfflineCandidate(token)` validation now reopens under exclusive lifetime
  coordination, checks the complete local identity and token classification
  before graph access, validates exactly the selected retained generation, and
  rereads/rechecks the token before returning. A stale token fails as
  `RecoveryCandidateChanged`; ordinary paths still perform no implicit
  validation.
- Permanent tests cover deterministic labels/order, unordered swapped metas,
  non-promotion of a damaged current meta, zero-candidate diagnostics, exact
  previous-generation validation, stale-token rejection before graph access,
  live sidecar byte preservation, resource/cancellation failures, and both
  required live current-generation errors.
- Current and Rust 1.74 all-feature suites each pass 185 tests with one
  intentionally ignored subprocess entry point. Warnings-denied all-target
  Clippy, benchmark compilation, formatting, diff checks, and changed-module
  complexity checks pass. The exact compiled production graph is 91 files and
  22,327 physical lines with zero functions above cyclomatic complexity 9; the
  three new production modules are 35, 199, and 227 physical lines. The whole
  implementation remains far above the directional size goal. Recovery output,
  bounded recovery scratch, snapshot publication, update-ipsets-shaped
  benchmarks, and the final simplification pass remain pending.

### 2026-07-25 - shared immutable-output writer plan

- Recovery and compact snapshots both produce one ordinary immutable v4 file.
  They will share one append-only output writer over the existing page codecs,
  trees, metadata chain, membership interning, and checksums. There will be no
  second format, temporary sorting database, or duplicate tree implementation.
- The writer owns only the private inode that can become the final result. It
  allocates pages sequentially, writes every non-meta page with the requested
  output transaction, enforces `max_output_pages` before allocation, and
  finalizes two identical metadata pages. Free, retirement, allocator-reserve,
  and unpublished state remain absent.
- Callers supply already ordered, accepted logical ranges. The writer rejects
  reversed, overlapping, out-of-order, or noncanonical adjacent records instead
  of silently applying last-wins mutation semantics. This keeps source
  traversal/recovery policy separate from deterministic output construction.
- Exact catalog `(name,index)` pairs and the non-shrinking feed-index limit are
  preserved. Membership words remain streamed in fixed batches; the writer
  verifies that every set bit names an accepted active feed, interns equal
  bitmaps, rebuilds IDs/hash/used indexes, and recomputes range-record
  refcounts. A legal maximum-width membership is never materialized.
- Metadata uses the existing bounded compressor and page-chain writer. The
  caller's heap budget covers the complete bounded metadata value; no
  per-range heap object or scratch file is introduced.
- This internal milestone stops at a complete private immutable file. Source
  pinning, section-20 publication/resolution, recovery damage traversal/reporting,
  and snapshot public APIs remain separate later slices. Permanent tests will
  reopen and explicitly validate direct and membership outputs, cover exact
  tuple/metadata preservation, page-budget refusal, malformed ordered input,
  membership deduplication/refcounts, and zero allocation per streamed record
  after setup.

### 2026-07-25 - shared immutable-output writer implementation

- One private append-only builder now constructs both direct and membership v4
  files from ordered accepted records. It reuses the existing range/catalog/
  membership/metadata codecs, allocates only sequential final pages, enforces
  the output-page budget before allocation, emits no free/retirement/reserve
  state, and writes two identical complete metadata pages only at finish.
- Any construction error permanently poisons that private output; it cannot
  later be finished or published. Reversed, overlapping, out-of-order, and
  adjacent-equal ranges fail explicitly rather than invoking rewrite behavior.
- Membership input remains caller-streamed. Fixed-size batch reads compare every
  supplied bit with the output feed-used bitmap, including across sparse bitmap
  leaf boundaries. Equal memberships are interned, large memberships go
  directly into final blob pages, and exact range-record refcounts are applied
  without a temporary delta tree or materialized maximum bitmap.
- Permanent tests cover empty direct and membership outputs, exact tuple and
  metadata preservation including present-empty metadata, full-space IPv6,
  sparse memberships crossing the 32,000-bit leaf boundary, inactive/trailing
  membership rejection, dictionary deduplication and refcounts, page and heap
  budget refusal, permanent failure state, malformed range order, a validated
  2,000-range multi-level output, and zero warmed-path heap allocations for
  both direct and streamed-membership records.
- Current and Rust 1.74 all-feature suites each pass 196 tests with one
  intentionally ignored subprocess entry point. Warnings-denied all-target
  Clippy, benchmark compilation, formatting, diff checks, and changed-module
  complexity checks pass. The exact compiled production graph is 94 files and
  23,029 physical lines with zero functions above cyclomatic complexity 9.
  The builder is 472 lines and its membership helper is 95 lines. The total
  implementation remains far above the directional size goal and still
  requires the final simplification pass.
- This milestone deliberately ends with a private finished inode. Durable
  fail-if-exists publication is the next dependency; source pinning, recovery
  policy/reporting, and compact-snapshot traversal will use this one builder
  rather than introduce another output engine.

### Historical adversarial-audit evidence

The evidence below records the original test-only rounds exactly as executed.
Red counts describe the obsolete implementation baseline, not the Phase-1 v4
acceptance contract. Tests whose old expected behavior conflicts with decisions
4-68 must be rewritten and mapped explicitly during implementation.

Acceptance criteria evidence:

- Permanent mirrored coverage exists for all confirmed Round 5 defect classes.
- Go: 26 new top-level tests; 5 green and 21 intentionally red.
- Rust: 25 new top-level tests; 5 green and 20 intentionally red.
- Test fixtures use only synthetic in-memory images, temporary directories, and
  deterministic injected storage failures.

Tests or equivalent validation:

- `go test ./... -run '^$'`: pass; all Go tests and benchmarks compile.
- `go vet ./...`: pass.
- `go test ./... -count=1`: expected failure, 21 Round 5 top-level failures.
- `go test -race ./... -count=1`: same 21 expected failures; zero race reports.
- Go statement coverage with the red suite completing all test functions:
  81.0%, up from 79.6% baseline.
- `cargo fmt --all -- --check`: pass.
- `cargo test --workspace --all-features --no-run`: pass.
- `cargo clippy --workspace --all-features --all-targets -- -D warnings`: pass.
- `cargo check --workspace --all-features --benches`: pass.
- Rust Round 5 targeted matrix: public API 4/4 green; overflow 1 green/7 red;
  external sort 0/2; free list 0/1; limits 0/2; transactions 0/7; writable
  corruption 0/1.
- `cargo check --workspace --all-features --benches`: pass.
- Go and Rust all-to-all pair-scaling benchmarks compile and run.
- `./.agents/sow/audit.sh`: SOW-0016 passes its gate, status/directory,
  sensitive-data, and framework checks. The repository-wide audit remains
  partial because pre-existing SOW-0013 and SOW-0014 lack current-template
  gate text; at that historical audit checkpoint, the original test-only pass
  did not modify those records.

2026-07-15 re-audit validation:

- Baseline at `c4dbde5`: `go test ./... -count=1` passes and `go test -list`
  lists 278 top-level tests; `cargo test --workspace --all-features --
  --test-threads 1` passes and Cargo `--list` reports 294 test functions.
- `go test ./... -run '^$' -count=1`: pass; all expanded Go tests compile.
- `go vet ./...`: pass.
- `go test ./... -count=1`: expected red result with 14 top-level failures.
  Thirteen are newly added top-level tests; the fourteenth is the existing
  writable-corruption matrix with its new empty-leaf case.
- `go test -race ./... -count=1`: the same 14 expected failures and zero race
  reports.
- `cargo fmt --all -- --check`: pass.
- `cargo test --workspace --all-features --no-run`: pass.
- `cargo clippy --workspace --all-features --all-targets -- -D warnings`: pass.
- `cargo test --workspace --all-features --no-fail-fast -- --test-threads 1`:
  expected red result with 16 failing test functions across 10 test targets.
  All unaffected unit and integration targets pass.
- The expanded normal suites now contain 294 Go top-level tests and 310 Rust
  test functions.
- `git diff --check`: pass before the SOW update; rerun at final validation.

2026-07-16 re-audit validation:

- Baseline at `08393e37`: all 294 pre-existing Go top-level tests and all 315
  pre-existing Rust test functions pass.
- `go test . -run '^$'`: pass; all Go tests and benchmarks compile.
- `go vet ./...`: pass.
- `go test ./... -count=1`: expected red result. The expanded suite has 334
  top-level tests: 299 pass and 35 fail. All 35 failures are newly added Round 7
  contracts; no pre-existing test fails.
- `go test -race ./... -run '^TestRound7' -count=1`: the same expected contract
  failures and zero race-detector reports.
- `cargo fmt --all -- --check`: pass on Rust 1.74-compatible test code.
- `cargo test --workspace --all-features --no-run`: pass.
- `cargo clippy --workspace --all-features --all-targets -- -D warnings`: pass.
- `cargo test --workspace --all-features --no-fail-fast -- --test-threads 1`:
  expected red result. All 315 pre-existing functions pass; the 56 new functions
  contain 5 passes and 51 failures. The nested helper output from the spill
  subprocess is not counted as a second test.
- Green Round 7 contracts include complex IPv4 and IPv6 round trips, Go's safe
  rejection of overflowing `total_pages`, Go spill-file cleanup, Rust bounded
  one-shot file descriptors, and Rust safe one-shot/reversed migration input
  handling where applicable.
- Go Round 7 benchmarks and Rust Criterion groups 13-16 compile and run. They
  cover committed-scope lookup, pending-scope lookup, spill round trip, and
  nested sequential-assignment normalization.
- `git diff --check`: pass before the SOW update; rerun at final validation.

Real-use evidence:

- Public/file-backed wrappers execute successfully in temporary files, but
  adoption readiness remains blocked by deterministic corruption, durability,
  lifecycle, exhaustion, and performance findings.

Reviewer findings:

- Local control-flow and coverage review identified the committed-tree writable
  validation gap and legal counter-exhaustion gaps in addition to the planned
  overflow, free-list, transaction, and external-sort cases.
- External reviewers were not requested for this test-only round.

Same-failure scan:

- Every shared defect was checked in both Go and Rust. Both implementations
  exhibit the overflow, overlap, free-list, durability, poisoning, no-op scope
  rebuild, writable validation, and exhaustion failures. The Rust suite already
  had zero-chunk configuration coverage from Round 4; Go needed the new direct
  case.

Sensitive data gate:

- Current SOW contains no sensitive data.

Artifact maintenance gate:

- Pending final implementation. The required artifact actions are defined in the
  Pre-Implementation Gate and must be evidenced here before close.
- SOW lifecycle consolidation began on 2026-07-17: SOW-0016 became the sole
  active authority; SOW-0011/0013/0014/0015 were closed as superseded without
  claiming their unfinished acceptance criteria completed.

Specs update:

- The normative Phase-1 specs implement the applicable portions of decisions
  4-68 and replace obsolete v4.3, scope-mode, per-scope KV, minor-version,
  two-format, and byte-identical-writer authority. Implementation discoveries
  must be reconciled into those specs before this SOW closes.

Project skills update:

- Required once the exact-v4 conformance/crash/benchmark and format-invariant
  workflows are concrete; otherwise record an evidence-backed reason no reusable
  skill was warranted.

End-user/operator docs update:

- Required for every final public SDK behavior exposed by this repository;
  pending implementation and API stabilization.

End-user/operator skills update:

- None currently known. Re-audit after spec/docs/API changes and record evidence.

Lessons:

- Every new persisted page graph needs validation, ownership, reclamation,
  crash-point, and consumer tests, not only a successful round trip.
- A 32-level B+tree traversal guard cannot safely double as an overflow-chain
  length bound.
- Writable open needs complete O(1) bootstrap geometry/identity safety before any
  file-backed mutation. Deep graph/CRC validation is explicit; ordinary mutation
  must remain bounds-safe and abort/poison if committed corruption is discovered.
- Legal maximum identifiers and generations need checked exhaustion behavior;
  debug-language panics and release-language wrapping are both unacceptable.
- Pair-count scaling must be benchmarked by feed cardinality; record-count-only
  benchmarks hide the aggregation complexity.

Follow-up mapping:

- All valid Phase-1 audit defects, the applicable portions of decisions 4-68,
  removal work, implementation, specs, tests, benchmarks, docs, skills, and
  lifecycle closure remain in this SOW. Snapshot signing is explicitly tracked
  by pending SOW-0017 and is not a hidden close requirement here.
- Nothing is deferred merely because it is difficult. Before close, every
  remaining `defer|later|follow-up|future|TODO|pending` entry must be implemented,
  explicitly rejected with evidence, or represented by a real SOW as required by
  the repository follow-up discipline.

### Phase-1 core-SDK production-hardening gate

Pending. Before SOW completion, this section must record:

- acceptance evidence for every applicable Phase-1 decision and criterion above;
- complete green Go/Rust suites, static/race/fuzz/property/process/crash results;
- bidirectional semantic cross-open of every committed golden;
- measured allocation, file-descriptor, page-touch, sparse-file, and asymptotic
  bounds at production-shaped scale;
- exact commit-outcome and recovery evidence for all injected failures;
- compact unsigned snapshot construction, failure, residue, publication, and
  resource-bound evidence;
- real-use retention and independent multi-feed update workflows;
- same-failure and removed-artifact/reference searches;
- reviewer findings and disposition when review is requested or required at the
  production-grade milestone;
- sensitive-data and complete artifact-maintenance gates;
- final SOW status/directory consistency and follow-up mapping.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

Pending SOW-0017 is the authorized Phase-2 snapshot-signing follow-up. Complete
the approved unsigned core-SDK scope here; map any other newly discovered valid
work under the repository follow-up discipline before closing this SOW.

## Regression Log

None yet.
