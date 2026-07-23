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
    `.readers`; it has a versioned fixed-layout header bound to the local database
    identity and an explicit reader capacity. Open must reject symlinks,
    non-regular files, incompatible headers, incorrect size, and path replacement.
    Every handle and guard retains the originally opened file descriptor and never
    reopens the pathname. Registration, update, removal, stale reaping, and the
    writer's oldest-reader scan use one defined cross-process locking protocol. A
    reader registers transaction zero before selecting a meta page, then publishes
    the selected transaction. Commit prevents new registration from the
    oldest-reader snapshot through atomic meta publication, without blocking
    already-registered reads. A slot is reaped only when the operating system
    proves its owner is dead; uncertainty is treated as alive. Normal live open
    never silently recreates a missing, malformed, or replaced table. Reset is an
    explicit offline operation which requires proof that no live users remain.
22. User decision (2026-07-16): add an immutable, nonzero 128-bit random
    `database_id` to the v4 static identity. It is stored identically in both meta
    pages and in the live-reader sidecar header; disagreement is an O(1) bootstrap
    rejection. The sidecar also records the local operating-system file identity
    so independent filesystem copies cannot accidentally share coordination. A
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

## Validation

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
