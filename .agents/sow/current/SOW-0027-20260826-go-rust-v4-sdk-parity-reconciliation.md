# SOW-0027 - Go/Rust v4 SDK Parity Reconciliation

## Review Gate (user directive 2026-08-26)

Every milestone and slice gate under this SOW uses exactly five reviewers,
all of the lead's own model, running the final-review skill in adversarial
mode, each with one distinct scope. The Rust implementation is the baseline:
the reviewers check the Go work in contrast to Rust for all aspects, while
ensuring optimal Go idioms, performance, accuracy, interoperability, and
compatibility.

1. Rust parity: Go must do exactly what Rust does; the smallest divergence is
   a performance or behavior red flag. This includes mmap-only access and
   memory safety and lifetime ownership.
2. Go idioms: the work must be natural Go despite being identical in logic,
   system calls, philosophy, and structure to Rust.
3. Performance: the absolutely most performant way; even one unnecessary
   branch in the hot path is expensive.
4. Wire format and integrity: Rust/Go interoperability of the file format on
   disk, locking, coordination, and the shared conformance corpus.
5. APIs and records: public APIs, docs, errors, and records all in sync with
   Rust.

Reviewers are level-1 only (no level-2 tier). They are spawned once and kept
open for incremental delta re-reviews; they are restarted only between
milestones. No reviewer model other than the lead's own is used. These rules
live here at the top of the SOW, not in SWARM.md.

## Status

Status: in-progress

Sub-state (2026-08-26): activated by user decision (Open Decision 1, option A);
SOW-0017 is paused as blocked on this SOW's prerequisite. The static gap
analysis at repository revision `07c0147bf18b4d1e976f460fc2624e90e91be8cc` was
independently re-verified by the lead against the code and the normative specs
(2026-08-26); every material claim was confirmed, including the off-contract
public writer, the direct-only live writer facade, the missing operations
(membership import, one-inode immutable feed, commit resolution, bounded
reclaim, clean-writer metadata read-your-writes), the omission of cancellation
and live join sources, the error/nil/enum contract defects, the in-process
non-Linux worker routing, the private Windows housekeeping, the asymmetric
fixture producers, the same-language immutable-only subprocess tests, the
missing benchmark matrix, and the incorrect SOW-0025 acceptance record.
Implementation starts with the parity ledger freeze (plan step 1).

Sub-state (2026-08-26): milestone 1 (parity ledger freeze) delivered at
`bff1f550`. Milestone 2 slice 2a (live-writer consolidation) is delivered at
`11ff5848` and its five-reviewer gate closed at `6853e4fd` (all five
reviewers PASS on round 3; rounds 1-2 findings all fixed and recorded below): all advanced transactions and
high-level workflows bind to the sidecar-bound `LiveWriter` through the
shared `mutationHost` (core owner, open/healthy probe, abort, discard, and
commit terminals); the live public surface is `LiveWriter.BeginDirect`,
`BeginMembershipTransaction`, `BeginStructuredTransaction`,
`BeginCreateFeed`, `BeginReplaceFeed`, `BeginDirectReplacement`,
`BeginFirstSeenRefresh`, `BeginLastSeenRefresh`, `ProjectHistory`,
`RenameFeed`, `DeleteFeed`, `Abort`, and `Close` with the captured
cancellation carried through every begin, mutation, metadata stage, commit,
and cleanup checkpoint; the `FinishedHistoryProjection` commit path is
unified through the host terminal (reader-table gate on the live path); the
end-to-end live test chain (CreateLive -> OpenLiveWriter -> BeginCreateFeed
-> FinishInput -> Commit -> SnapshotTo -> OpenImmutable) plus live
lifecycle, direct workflow, structured transaction, and live-source history
projection tests pass. The parity ledger flipped the implemented rows from
missing to present (68 present/remove-planned, 18 missing) and the gate test
now closes both the `Writer` and `LiveWriter` method surfaces. The full
battery (plain, v4work, vet, gofmt, race+checkptr) passes at the slice
commits.

Review round 1 (five reviewers: Rust parity, Go idioms, performance,
wire/integrity, APIs/records) found and the lead fixed:

- P1 (Rust parity, F1): `LiveDirectTransaction.Commit` passed a nil
  checkpoint; the captured cancellation token now checkpoints the commit
  attempt, the prepare-and-lock sequence, and the publication loop (Rust
  DirectTransaction::commit -> commit_operation parity).
- P2 (Rust parity, F2): the live direct range ops lacked the Rust
  run_transaction post-mutation checkpoint and the metadata stages lacked
  both checkpoints; pre+post checkpoints are now applied to
  AssignV4/AssignV6/ClearV4/ClearV6/SetMetadataJSON/ClearMetadataJSON.
- P2 (APIs/records): the parity ledger omitted the
  `LiveWriter.BeginDirectReplacement` row and had no tripwire for
  unrecorded `LiveWriter` methods; the row is added and the gate now closes
  both method surfaces (it also caught and recorded `LiveWriter.Abort`).
- P2 (APIs/records): the status record still claimed slice 2a was "under
  way at working tree `bff1f550+`" while the slice was delivered; rewritten
  to present state here.
- P3 (APIs/records): the structured begin probed healthy before the Rust
  structure-kind gate and its doc claimed cancellation-first; the shared
  helper now runs closed-probe, structure-kind, cancellation, healthy,
  value-kind (Rust begin_structured_transaction order) and the doc matches
  both hosts.
- P3 (wire/integrity): a stale Rust test-file citation in
  `live_workflow_public_test.go` was replaced with the real Rust suite name.
- P3 (Go idioms): dangling doc fragment, double coreOf probe, and
  plan-narrative sentences fixed in the slice commits.

Two outcomes remain recorded as accepted Go-shape divergences (P3,
documented in code, not yet a resolved SOW decision): workflow
`CommitResult`/`AbortResult` terminals carry fewer fields than Rust
(workflow commits omit directory/main identity; workflow abort returns
`error`), and mixed-language live fixtures stay a recorded slice-2c gap.

Sub-state (2026-08-26): slice 2b part 1 delivered (working tree
`532fa582+`, gate pending): clean-writer metadata read-your-writes
(`LiveWriter.MetadataJSONLen` / `ReadMetadataJSON` / `MetadataJSON` over
the staged-or-committed current generation, with the buffer-too-small
class; ledger rows flipped to present) and bounded reader-safe
reclamation (`LiveWriter.Reclaim` auto-publishes the selected oldest
retirement prefix through the commit terminal; the pinned-reader
block/release vector is ported from the Rust test). Remaining 2b items:
commit resolution, live join sources, and membership import.

## Requirements

### Purpose

Make the pure-Go v4 SDK a true public-behavior peer of the Rust v4 SDK under the
exact current unsigned v4 contract. The result must support the same logical
operations, live coordination, failure outcomes, recovery containment,
platform boundary, bounded-resource behavior, and cross-language evidence
needed by `update-ipsets` and downstream SDK consumers.

This work must correct implementation and evidence gaps rather than weaken the
normative specification to match the incomplete Go surface.

### User Request

Create a new SOW containing the verified static gap analysis of feature parity
between the Rust and Go v4 SDK implementations.

### Assistant Understanding

Facts:

- Rust was implemented first and is the reference implementation for v4
  semantics; the normative specifications and shared conformance contract are
  the cross-language authorities.
- The design requires completed Rust and Go SDKs to be public-behavior peers.
  Physical page placement, tree shape, internal IDs, and compressed bytes may
  differ when observable semantics agree.
- Both readers open and semantically check all eleven committed immutable
  conformance files. This is real file-format and reader-interoperability
  evidence for the cases represented by the corpus.
- Go's sidecar-bound `LiveWriter` exposes only information, one advanced direct
  transaction, and close. Most Go mutation operations are attached to the
  separate sidecar-free `Writer`, which blocks readers for its lifetime.
- The public sidecar-free mutable Go writer contradicts the exact v4 contract,
  which requires every public writer to be live and explicitly forbids offline
  in-place mutation.
- Go has no public membership-import, one-inode immutable-feed construction,
  exact commit-resolution, or bounded reader-safe reclamation operation.
- Go validation, candidate inspection, and recovery use the required worker
  process only on Linux/amd64. Other targets execute faultable mapped-file work
  in the application process.
- Go deliberately refuses creator-only artifact creation on macOS because its
  Darwin ACL machine is unavailable. Rust supports the specified macOS
  creator-only policy.
- The existing subprocess tests spawn another test process of the same language
  and inspect immutable files. They do not run cooperating Go and Rust
  processes on the same live database.
- SOW-0025 claimed the missing behavior and conformance evidence as completed,
  but its own delivery record states that the Go `LiveWriter` shipped only the
  direct transaction surface. The reviewed gap is an incorrect acceptance
  record, not evidence that later code removed formerly implemented behavior.
- SOW-0017 currently treats Go/Rust parity and platform completion as proven
  prerequisites for snapshot-signing design. This static review invalidates
  that prerequisite.

Inferences:

- The long-term-best correction is to converge Go on the Rust public contract,
  not to preserve two Go writer modes or narrow the specification after the
  fact.
- The existing Go mapped reader, writer core, live coordination, workflow,
  publication, validation, and recovery owners contain substantial reusable
  implementation. The correction should rewire and complete those owners
  rather than introduce another storage path.
- Static source presence is insufficient evidence of semantic parity. The
  repaired surface needs independent public-API tests, symmetric producer
  fixtures, true mixed-language live tests, native platform evidence, and
  matched release benchmarks.
- Snapshot signing should not proceed until this SOW restores the prerequisite
  unsigned SDK contract and evidence.

Unknowns:

- Whether macOS creator-only ACL removal and proof can be implemented under the
  existing pure-Go/no-cgo constraint without a new native boundary. Official
  Darwin interfaces and viable pure-Go mechanisms must be investigated before
  that platform chunk is designed.
- The smallest portable worker implementation that can preserve the exact
  POSIX `SIGBUS` and Windows in-page-exception containment rules on Darwin,
  FreeBSD, Windows, non-amd64 Linux, and supported architectures without
  violating the pure-Go boundary.
- The final Go/Rust performance delta after the public writer is consolidated
  and per-result adapter allocation is removed. Static inspection cannot
  establish the measured result.

### Acceptance Criteria

- The public Go writer contract exposes only the normative live writer. A
  sidecar-free immutable file cannot be opened for mutation, and no public
  `Create`/`OpenWriter` offline mutation path remains.
- Go `LiveWriter` exposes Rust-equivalent advanced direct, membership, and
  structured transactions; feed create/replace/rename/delete; exact direct
  replacement; first-seen and last-seen refresh; history projection; metadata
  mutation/read-your-writes; abort, close, commit, and bounded reclamation.
- Every advanced or high-level Go begin operation accepts and retains its own
  cancellation token through ingestion, normalization, metadata, commit, and
  cleanup checkpoints.
- Go implements name-based membership import from explicitly pinned immutable
  and live readers with the Rust compatibility, source-borrow, report,
  cancellation, and no-change semantics.
- Go implements one-inode immutable IPv4 and IPv6 feed construction with the
  Rust resource budget, normalization, metadata, publication, report, and
  cleanup contract.
- Go implements exact commit-outcome resolution for immutable and live modes,
  including same-file proof, superseded/unknown classification, and truthful
  cleanup evidence.
- Go implements bounded, transaction-grouped, oldest-reader-safe reclamation
  through the live writer and proves lowest-free-page reuse while readers are
  pinned.
- Go clean-writer metadata mutation and transaction metadata access provide
  the same staged read-your-writes semantics as Rust.
- Direct provider joins accept both immutable and live direct readers. Public
  mapped source adapters provide bounded reader-to-writer chaining equivalent
  to the Rust named-feed and direct-range sources.
- All public Go failures that belong to the stable SDK taxonomy are
  programmatically accessible as the exported Go error type. Public APIs never
  leak an unnameable `internal` error type.
- Invalid public Go inputs return typed errors rather than panic. Enum-like
  certifications reject undefined values before path access or mutation.
- Validation, candidate inspection, recovery, and retained-cleanup retry use a
  version-matched worker on every supported OS/architecture. No supported
  target silently executes faultable scans in the application process.
- Go creates and proves creator-only live and immutable publication artifacts
  on Linux, macOS, Windows, and the specified FreeBSD immutable boundary, or
  implementation stops for an explicit user decision if the pure-Go macOS
  contract is proven infeasible.
- Go publicly exposes the required Windows housekeeping list/removal APIs and
  their exact identity-bound evidence and terminal outcomes.
- Both implementations independently produce the required symmetric direct,
  membership, structured, metadata, cardinality, snapshot, and invalid-case
  corpus coverage through public normative paths. Go fixtures are produced
  through `CreateLive`, `LiveWriter`, close, and live `SnapshotTo`, not through
  an offline mutable writer.
- Mixed Go/Rust subprocess tests cooperate on the same live database in both
  directions and cover registration/release, writer exclusion, reclamation,
  stale slots, sidecar replacement, transition/reservation states,
  publication inspection, and resolution.
- Commit-resolution and compact-snapshot cases required by the normative
  conformance contract are cross-proven, including same-transaction/different-
  nonce attempts.
- Go aggregation/join adapters meet their documented bounded/allocation
  behavior. Public steady-state performance claims are backed by allocation
  pins and profiles of the actual public adapters.
- Go ports the representative Rust `update-ipsets` release benchmark matrix.
  Matched release measurements record throughput, allocation, RSS, file size,
  file descriptors, necessary work, and semantic results for both languages.
  Material differences are explained and accepted by the user.
- Current SDK and conformance documentation accurately describes implemented
  behavior, worker deployment, platform support, and evidence. Superseded
  milestone reports no longer appear to be active work records.
- The full Go codebase passes the mmap-only/page-copy/file-I/O adversarial gates,
  all supported compiler graphs, tests, race/checkptr analysis, static checks,
  native authorized platform tests, and independent final review with no open
  P0, P1, or P2 finding.
- SOW-0017 remains blocked until this SOW completes and the restored unsigned
  SDK prerequisite is accepted by the user.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/done/SOW-0025-20260811-pure-go-exact-v4-port.md`
- `.agents/sow/done/SOW-0026-20260825-v4-platform-completion.md`
- `.agents/sow/current/SOW-0017-20260717-v4-snapshot-signing-phase2.md`
- `v4/rust/iprange-livedb/src/lib.rs` and its exported reader, writer,
  lifecycle, workflow, query, publication, validation, recovery, and snapshot
  modules
- All compiled production Go files under `v4/go/`, including public facades and
  the internal writer, live-coordination, publication, routing, security,
  validation, recovery, and worker owners
- `v4/conformance/README.md`, `v4/conformance/cases.json`, Go/Rust fixture
  generators, conformance tests, and mixed-subprocess tests

Current state:

- **Substantial parity:** exact immutable format identity; reading committed
  direct, membership, and structured fixtures; metadata and cardinality
  observations; broad live-reader surface; membership queries, aggregation,
  global algebra, snapshots, lifecycle transitions, publication, validation,
  and recovery facades on the platforms where their required machinery exists.
- **Partial parity:** direct live transaction; direct-provider joins; public
  error translation; cancellation; platform creation; worker deployment;
  producer-fixture coverage; resource/performance evidence.
- **Not parity:** live membership/structured/feed/timestamp/history mutation;
  membership import; immutable single-feed creation; commit resolution;
  reader-safe reclamation; live writer metadata read-your-writes; public Windows
  housekeeping; real mixed-language live cooperation.

Evidence:

- Normative all-writers-live rule and offline-mutation prohibition:
  `.agents/sow/specs/binary-format-v4.md:1272-1279`.
- Explicit constructor contract:
  `.agents/sow/specs/binary-format-v4.md:2413-2421`.
- Cross-language public-behavior requirement:
  `.agents/sow/specs/design-iprange-engine.md:57-73,418-438`.
- Go's sidecar-free creation and mutation API:
  `v4/go/writer_public.go:52-97`.
- Go's real live writer is direct-only:
  `v4/go/live_writer_public.go:42-100`.
- Go workflows attached to sidecar-free `Writer`:
  `v4/go/direct_workflow_public.go:60-80`,
  `v4/go/feed_workflow_public.go:62-73,520-575`,
  `v4/go/membership_transaction_public.go:83`,
  `v4/go/structured_transaction_public.go:62`, and
  `v4/go/history_projection_public.go:64`.
- Rust `LiveWriter` equivalents:
  `v4/rust/iprange-livedb/src/live_writer/direct.rs:29`,
  `membership.rs:91`, `structured.rs:41`, `direct_workflow.rs:66-96`,
  `feed_workflow.rs:58-71`, `feed_lifecycle.rs:11-36`, and
  `history_projection.rs:57`.
- Missing-in-Go Rust operations:
  `v4/rust/iprange-livedb/src/live_writer/membership_import.rs:60-93`,
  `live_writer/reclaim.rs:24-37`, `commit_resolution.rs:86-99`, and
  `immutable_feed.rs:67-119`.
- Rust clean-writer metadata/read-your-writes:
  `v4/rust/iprange-livedb/src/live_writer.rs:143-189,211-229`.
- Go live direct operation omits cancellation:
  `v4/go/live_writer_public.go:68-79,270-282` and
  `v4/go/internal/live/live_writer.go:196-201`.
- Go direct joins accept only immutable sources:
  `v4/go/join_public.go:63-89`; Rust accepts immutable and live sources at
  `v4/rust/iprange-livedb/src/membership_query/join.rs:20-25,108-128`.
- Go public writer paths leak internal typed errors:
  `v4/go/writer_public_test.go:17-24`; the correct conversion boundary exists at
  `v4/go/reader_public.go:769-787`.
- Go public nil-input panic and unchecked certification paths:
  `v4/go/join_public.go:71-78,114-122`,
  `v4/go/membership_algebra_public.go:79-86`, and
  `v4/go/recovery_public.go:27-37,205-211`.
- Go aggregation/join adapter allocation contradicts its public comments:
  `v4/go/aggregation_public.go:1-10,80-108` and
  `v4/go/join_public.go:1-8,89-101,124-151`.
- Worker required for faultable operations:
  `.agents/sow/specs/binary-format-v4.md:3155-3206`.
- Linux/amd64 Go worker routing and other-platform in-process routing:
  `v4/go/internal/routing/routing_linux_amd64.go:1-40`,
  `v4/go/internal/routing/routing_other.go:1-50`, and
  `v4/go/cmd/iprange-v4-worker/main_other.go:1-15`.
- Go Darwin creator-only refusal:
  `v4/go/internal/security/acl_darwin.go:9-28` and
  `v4/go/internal/live/public.go:23-38`.
- Windows housekeeping is private in Go but public in Rust:
  `v4/go/internal/publication/maintenance_gc_windows.go:10-29`,
  `v4/go/publication_public.go:179-225`, and
  `v4/rust/iprange-livedb/src/publication.rs:36-47`.
- Normative mixed-language live conformance:
  `.agents/sow/specs/binary-format-v4.md:4145-4170`.
- Existing same-language immutable subprocess tests:
  `v4/go/subprocess_cross_open_test.go:27-171` and
  `v4/rust/iprange-livedb/tests/mixed_subprocess.rs:23-140`.
- Rust fixtures use live creation/writing/snapshot; Go fixtures use the
  sidecar-free writer:
  `v4/rust/iprange-livedb/tests/conformance_support/generate.rs:36-73` and
  `v4/go/conformance_generate_test.go:48-64,107-125,147-194,226-240,310-324`.
- Corpus asymmetry and stale snapshot statement:
  `v4/conformance/README.md:10-43`; Go `SnapshotTo` exists at
  `v4/go/snapshot_public.go:101`.
- Go has reader microbenchmarks but no port of the public Rust
  `update-ipsets` benchmark matrix:
  `v4/go/internal/reader/bench_test.go:25-141` and
  `v4/rust/iprange-livedb/benches/update_ipsets.rs:1-43`.
- SOW-0025 claimed complete parity while recording a direct-only live facade:
  `.agents/sow/done/SOW-0025-20260811-pure-go-exact-v4-port.md:2785-2817,7478-7484`.
- SOW-0026 records non-Linux worker refusal as accepted behavior:
  `.agents/sow/done/SOW-0026-20260825-v4-platform-completion.md:122-133`.
- SOW-0017 relies on the disproven parity prerequisite:
  `.agents/sow/current/SOW-0017-20260717-v4-snapshot-signing-phase2.md:7-25,114-118`.

Risks:

- Keeping the sidecar-free writer public would establish two incompatible Go
  mutation contracts and allow callers to build on behavior forbidden by the
  v4 specification.
- Consolidating the public writer affects every mutation facade, transaction
  handle, result type, test fixture, benchmark, and downstream caller. Partial
  migration could silently keep an off-contract path alive.
- Incorrect live workflow integration could publish partial drafts, reclaim
  pages still visible to readers, lose outcome-unknown evidence, or deadlock
  reader/writer coordination.
- In-process damaged-file scans can terminate or corrupt the consuming process
  on mapping faults. Worker portability is a correctness and containment
  requirement, not optional hardening.
- Incorrect ACL handling could publish artifacts with inherited access or make
  the macOS SDK unusable. A weakened creator-only policy is not acceptable.
- Removing exported Go APIs before all internal tests and callers migrate can
  cause build failures. The format is unreleased, so compatibility must not be
  preserved at the cost of the exact contract, but the migration must still be
  complete and atomic within this repository.
- Incomplete mixed-language testing can leave lock, sidecar, retirement, and
  resolver incompatibilities undiscovered even while immutable fixtures
  cross-open successfully.
- Benchmark work is potentially expensive. Every run expected to exceed two
  wall-minutes or ten core-minutes must be recorded with cost before execution
  and run under `nice`.

## Pre-Implementation Gate

Status: resolved (2026-08-26, user decision A on Open Decision 1)

Problem / root-cause model:

- SOW-0025 implemented a large Go semantic surface over the mapped writer core,
  then added a separate sidecar-bound live writer containing only direct
  transactions. The broader workflows were never migrated onto the live writer.
- The completion gate treated sidecar-free exclusive mutation as an accepted
  implementation mode even though the normative contract explicitly forbids
  it. The conformance fixtures and subprocess tests then exercised that
  incomplete architecture rather than the required live cross-language path.
- Several Rust exports, platform requirements, and conformance cases were
  recorded as completed without compiled Go public entry points or equivalent
  evidence.
- Later platform work preserved the accepted gaps, including non-Linux worker
  refusal, while SOW-0017 was unblocked on the resulting completion claims.

Evidence reviewed:

- Exact current design and binary-format specifications.
- Complete public Rust export inventory and the implementation modules owning
  every material operation.
- Complete compiled Go public facade inventory and targeted internal owners for
  writer/live coordination, security, routing, workers, publication,
  validation, and recovery.
- Shared corpus manifest, producer paths, immutable conformance tests, and
  subprocess tests in both languages.
- SOW-0025, SOW-0026, and the active SOW-0017 prerequisite record.
- Repository revision
  `07c0147bf18b4d1e976f460fc2624e90e91be8cc`; the working tree was clean before
  this SOW file was created.

Affected contracts and surfaces:

- Go public package API, public types, typed errors, cancellation, writer and
  transaction handles, lifecycle, recovery, validation, publication, and
  platform behavior.
- Internal Go writer core, live sidecar owner, mapped sources, immutable output,
  worker client/server, security, publication maintenance, and platform seams.
- Rust and Go fixture generators and conformance tests.
- Shared immutable corpus files and manifest.
- Mixed-process coordination harnesses.
- Go/Rust benchmarks, work counters, profiles, and release evidence.
- SDK README/package documentation, worker deployment documentation, platform
  support statements, specifications if investigation exposes a real contract
  ambiguity, runtime project skills, AGENTS.md gates, SOW-0017 prerequisites,
  and SOW lifecycle records.
- `update-ipsets` suitability and any downstream code compiled against the
  unreleased Go API.

Existing patterns to reuse:

- Rust `LiveWriter` and its direct, membership, structured, workflow, metadata,
  reclaim, and result modules are the semantic reference.
- Go `internal/writer.Core` and `internal/live.LiveWriter` are the existing
  physical and coordination owners; workflows must compose them rather than
  copy persistent logic.
- `HistoryProjectionSource` already demonstrates a Go tagged source that can
  bind immutable or live readers.
- `publicError` is the established root-package conversion boundary for typed
  errors.
- Rust public source enums demonstrate explicit immutable/live modes for
  membership import, joins, snapshots, validation, and recovery.
- Existing Go lifecycle/publication result translations demonstrate how
  platform-neutral public evidence should wrap internal owners.
- Rust fixture generation through live writer plus `snapshot_to` is the
  normative producer pattern.
- The project full-codebase mmap-only/page-copy/file-I/O reviewers and the
  `project-final-review` skill are the acceptance-review pattern.

Risk and blast radius:

- Behavioral/API compatibility: high inside the unreleased Go SDK because the
  off-contract public writer must disappear or become private.
- Data loss/corruption: high if commit, abort, reclaim, resolver, or workflow
  integration is incorrect.
- Concurrency: high for reader slots, gates, writer lease, close, and page reuse.
- Platform: high for signal/exception containment, ACLs, file identity, locks,
  durability, and Windows housekeeping.
- Security: high for creator-only access and untrusted damaged-file handling.
- Performance: high on writer, query adapter, worker, and publisher hot paths.
- Released compatibility: none for v4; the format and SDK remain unreleased.
- Existing legacy C CLI: expected to be unaffected; validation must confirm no
  build or packaging coupling changed.

Sensitive data handling plan:

- Use only synthetic database paths, ranges, feed names, metadata, identities,
  fault cases, and benchmark inputs in durable artifacts.
- Do not record operational FireHOL infrastructure, credentials, access tokens,
  production feed names, literal operational ranges, customer/community data,
  personal data, private endpoints, or identifying IP addresses.
- Real-use `update-ipsets` replays, if explicitly authorized, record only
  sanitized aggregate counts, sizes, timings, and profiles.

Implementation plan:

1. **Activate and freeze the parity ledger.** Resolve SOW sequencing, mark
   SOW-0017 blocked on SOW-0027, inventory every Rust public export and normative
   operation against a compiled Go symbol, owner, test, platform, and benchmark,
   and record the exact API removals/additions before code changes.
2. **Consolidate one live writer.** Extend `internal/live.LiveWriter` over the
   existing `internal/writer.Core`; bind all advanced transactions and
   high-level workflows to the live writer; carry per-operation cancellation;
   migrate result/cleanup translations; then remove or privatize public
   `Create`, `OpenWriter`, `Writer`, and their duplicate transaction surface.
3. **Complete missing semantics.** Port membership import, one-inode immutable
   feed construction, commit resolution, bounded reclaim, clean-writer metadata
   mutation/read-your-writes, immutable/live mapped source adapters, and live
   direct-provider joins through the existing owners.
4. **Repair the public Go contract.** Convert every stable SDK failure to the
   exported error type, validate nil and enum-like inputs, remove per-callback
   result/name allocation where the documented contract requires reuse, and add
   external-package compilation/behavior tests that cannot import Go `internal`
   packages.
5. **Complete platform containment and security.** Implement and package the
   version-matched worker for every supported platform/architecture, port exact
   POSIX/Windows fault ownership, implement the specified macOS creator-only
   policy within the approved pure-Go boundary, and expose Windows housekeeping.
   Stop for a user decision before introducing cgo, assembly, another native
   boundary, weakening creator-only security, or dropping a specified platform.
6. **Replace conformance evidence.** Regenerate symmetric Go fixtures through
   live creation/writer/snapshot, add the missing producer and invalid cases,
   and build true Go-to-Rust and Rust-to-Go live subprocess harnesses covering
   the complete section-21 contract.
7. **Prove bounded performance.** Port the representative Rust benchmark matrix,
   add public-adapter allocation pins and necessary-work counters, profile every
   material delta, remove actionable waste, and record matched release evidence.
8. **Close the full artifact and review gate.** Update current SDK docs,
   conformance docs, affected specs/skills/instructions, retire misleading
   pending milestone reports, run the complete Go and Rust gates, obtain native
   platform evidence with explicit authorization, run the required four
   full-codebase mmap/I/O reviewers plus independent semantic/platform/API
   reviewers, resolve all P0-P2 findings, and complete SOW lifecycle records.

Validation plan:

- Generate a machine-checkable Rust-export/normative-operation to Go-symbol
  parity manifest and fail CI for missing or extra prohibited public writer
  operations.
- Run all Go tests under `nice`, with and without `v4work`, plus `-race`,
  `checkptr=2`, vet, formatting, supported cross-builds, mmap storage/runtime
  gates, worker protocol/crash gates, and external-package API tests.
- Run Rust architecture, mmap storage/runtime, source-graph, complete workspace
  tests, Clippy, formatting, documentation, MSRV, and affected sanitizer/Miri
  gates under the project-v4-rust workflow.
- Regenerate all Go and Rust conformance artifacts only through explicit public
  fixture-generation commands; verify both readers open and semantically compare
  every file and reject missing/extra/stale/malformed entries.
- Run true mixed-language live processes in both directions for the complete
  normative coordination/resolution matrix.
- Add deterministic scalar/property models for every newly live-bound Go
  transaction/workflow and compare results with Rust authority vectors.
- Add crash/fault matrices for commit outcome, reclaim, transitions,
  reservations, publication, validation, recovery, worker identity/protocol,
  SIGBUS, and Windows in-page errors.
- Add same-failure searches for sidecar-free public mutation, leaked internal
  errors, unvalidated enum conversions, nil dereferences, in-process faultable
  routing, unexported maintenance operations, and per-result adapter allocation.
- Run authorized native tests on Windows, macOS, and FreeBSD only after the user
  explicitly approves access to those systems. Cross-compilation is recorded as
  compilation evidence only.
- Before each performance command expected to exceed two wall-minutes or ten
  core-minutes, record the exact command, purpose, expected cost, and stop
  criteria here; run every build/test/benchmark under `nice`.
- At each implementation milestone, run four complete Go-codebase adversarial
  reviews: two for complete-page copies into/out of mappings and two for
  persistent content I/O outside mappings. Run independent API/semantic,
  platform/security, conformance, and performance reviews at the final gate.
- Final acceptance uses `.agents/skills/project-final-review/SKILL.md` and
  requires no open P0, P1, or P2 finding.

Artifact impact plan:

- AGENTS.md: update only if this work establishes a new durable parity,
  conformance, platform, or benchmark guardrail not already recorded.
- Runtime project skills: update `project-v4-rust` if the symmetric Go/Rust
  conformance or worker workflow becomes reusable project procedure; add no
  generic skill solely for framework completeness.
- Specs: preserve the current contract when implementation is brought into
  conformance; update specs only for a user-approved contract decision or a
  real ambiguity discovered during implementation.
- End-user/operator docs: update Rust README, Go package documentation, worker
  installation, platform support, public APIs, and conformance instructions to
  describe the delivered present state.
- End-user/operator skills: none currently exist; record whether any are added
  before close.
- SOW lifecycle: keep SOW-0027 open in `pending/` until SOW-0017 is paused,
  completed, or superseded by user decision. Preserve SOW-0025 and SOW-0026 as
  historical evidence of incorrect acceptance rather than rewriting their
  narratives. When activated, SOW-0017 must be marked blocked/paused on this
  prerequisite. Remove or relocate the unnumbered SOW-0025 milestone reports
  from `pending/` only with explicit user approval because deletion is outside
  this SOW's automatic authority.

Open-source reference evidence:

- No external open-source repository was used for this static parity finding.
  The exact local normative specifications and the two implementations are the
  authoritative evidence. Platform implementation planning must review official
  OS/runtime documentation and relevant open-source language-runtime patterns
  before code changes.

Open decisions:

1. **SOW sequencing — RESOLVED (2026-08-26, user decision A).**
   Pause SOW-0017 and activate SOW-0027. Signing decisions and implementation
   wait; no signed-format work builds on a false SDK premise. Options B and C
   recorded but not selected.

Resolved decisions:

- The user requested a new SOW rather than modifying SOW-0025. SOW-0027 is the
  corrective tracker because the evidence shows an incorrect acceptance at the
  time of SOW-0025 completion, not a later removal of previously delivered
  behavior.
- The recommendation class is long-term-best: match the Rust authority and
  current specification; do not retain off-contract public compatibility for
  an unreleased SDK.
- The Go implementation remains pure Go with no cgo. If platform investigation
  proves the exact contract infeasible within that boundary, implementation
  stops and presents evidence-backed options to the user.

## Implications And Decisions

1. **New corrective tracker** — resolved by user request on 2026-08-26.
   SOW-0027 records the complete gap and preserves SOW-0025/SOW-0026 as
   historical acceptance evidence.
2. **Implementation order** — pending user selection from Open Decision 1.
   No production code may change under SOW-0027 until this is resolved and
   recorded.
3. **Contract direction** — resolved as long-term-best. Go converges on Rust and
   the normative specifications; specifications are not weakened to legitimize
   incomplete implementation.
4. **Pure-Go boundary** — resolved from SOW-0025. No cgo or new native boundary
   is introduced without a new explicit user decision supported by feasibility
   evidence.

## Plan

1. Resolve SOW sequencing and activate SOW-0027 without running two SOWs.
2. Freeze a complete public API/semantic/platform/evidence parity ledger.
3. Consolidate Go mutation behind the one sidecar-bound live writer.
4. Port every missing operation and repair public cancellation/error/input
   contracts.
5. Complete portable worker isolation, macOS creator-only security, and Windows
   housekeeping.
6. Replace asymmetric/off-contract fixtures and same-language subprocess smoke
   tests with the complete symmetric conformance proof.
7. Prove resource and performance parity on public `update-ipsets`-shaped
   workloads.
8. Update all affected artifacts and pass independent full-scope acceptance
   review before closing.

## Execution Log

### 2026-08-26

### Status (2026-08-26) - milestone 1 (parity ledger freeze) delivered

- Delivered in this commit: the parity ledger and its enforcing gate.
- `v4/go/parity_manifest.tsv` (81 curated rows, tab-separated:
  class, Rust reference, Go symbol, status, note) records every material
  Rust public operation and its Go symbol or missing status, organized by
  class: writer-offcontract (19 rows, remove-planned or missing), live-writer
  (21 rows), live-direct (7), reader (8), live-reader (4), cancellation (4),
  join (1), publication/scratch maintenance (7), snapshot (1), validation (1),
  recovery (4), worker (1), platform (1), cards/types (1). Statuses:
  present = the Go symbol exists and is the correct owner; remove-planned =
  the symbol exists but is off-contract and must disappear when the live
  writer consolidation lands; missing = the operation has no public Go
  equivalent (or only a wrong-owner off-contract one, recorded in the note).
- `v4/go/parity_gate_test.go`: the machine-checkable tripwire (SOW-0027
  acceptance: "fail CI for missing or extra prohibited public writer
  operations"). It parses the compiled root-package surface (go/parser, no
  subprocess) and fails when: a present/remove-planned symbol is absent; a
  missing row's symbol appears; or any public `Writer.*` method exists that
  is not recorded in the ledger (the off-contract writer surface is closed).
- `v4/go/parity_rust_public.tsv`: the regenerable raw Rust public export
  inventory (507 symbols) that the curated manifest maps; the uncurated
  long-tail rows are explicitly out of the gate's scope until later
  milestones record them.
- Ledger facts confirmed by the gate at this revision: 54 present/
  remove-planned, 32 missing. The gate additionally exposed two acceptance
  records that did not match reality, now recorded truthfully: the Go public
  surface has no `RemoveCheckpointedScratch` export (the internal recovery
  machine exists; SOW-0026's D record claimed the public export), and
  `Writer.DeleteFeed`/`Writer.RenameFeed` exist on the off-contract writer
  (live-owner equivalents absent).
- Battery: root plain and `-tags v4work` suites green (the gate runs in both),
  `go vet ./...` clean, `gofmt -l` empty for the new files, under `nice`.
- Next milestone 1 item: extend the ledger to the full 507-row Rust export
  inventory with name-variant resolution (the long tail), then milestone 2
  starts the live-writer consolidation with the removal list fixed by this
  ledger.


- User decision: activate SOW-0027 (option A), pause SOW-0017 as blocked on the
  unsigned-SDK prerequisite; recorded in Open Decision 1 and in SOW-0017.
- Lead re-verification of the static audit: every material claim confirmed
  against the code and the normative specs; evidence list recorded in the
  status sub-state above.
- Performed a read-only static parity audit at
  `07c0147bf18b4d1e976f460fc2624e90e91be8cc`.
- Reviewed the normative specifications, Rust public authority, all Go public
  production facades, relevant Go internal owners, shared corpus, producer
  paths, subprocess tests, completed SOW-0025/SOW-0026, and active SOW-0017.
- Created this pending corrective SOW at the user's request.
- No production code, specification, SDK documentation, skill, or active SOW
  was changed. No test or benchmark was run because the request was to record
  the completed static analysis.
- Ran `nice .agents/sow/audit.sh`. It validates SOW-0027 as `open` under
  `pending/`, the pre-implementation gate, regression-section placement,
  sensitive-data guardrail, and project-skill structure. The overall audit
  remains partial because the pre-existing SOW-0025 document has no canonical
  top-level status block and is reported as `Status: ###` in `done/`; this SOW
  records that historical artifact but does not rewrite it.
- Checked the new file for template placeholders, personal names, and trailing
  whitespace with `rg`; none was found.

## Validation

Acceptance criteria evidence:

- Pending implementation. The static gap evidence and intended proof conditions
  are recorded above.

Tests or equivalent validation:

- Static source/specification analysis only. No dynamic test was required to
  create this tracker, and no test result is claimed.
- `nice .agents/sow/audit.sh`: SOW-0027 status/directory and required gates pass;
  repository verdict remains partial only for the pre-existing SOW-0025 status
  layout described in the execution log.
- New-file hygiene scan for template placeholders, personal names, and trailing
  whitespace: pass.

Real-use evidence:

- Pending. No runtime path was exercised for SOW creation.

Reviewer findings:

- The originating static review returned FAIL. Material findings are preserved
  in Requirements, Analysis, and the Pre-Implementation Gate.

Same-failure scan:

- The static audit searched the complete compiled Go public production surface
  for Rust-equivalent writer, import, immutable-feed, commit-resolution,
  reclamation, metadata, join-source, worker, platform, housekeeping, and
  conformance operations. Implementation-time same-failure scans remain
  pending.

Sensitive data gate:

- This SOW contains repository paths, public API names, synthetic contract
  descriptions, and a public source revision only. It contains no secrets,
  credentials, bearer tokens, SNMP communities, community member names,
  customer names, personal data, customer-identifying IP addresses, private
  endpoints, or proprietary incident details.

Artifact maintenance gate:

- AGENTS.md: unchanged; SOW creation does not change project-wide workflow.
- Runtime project skills: unchanged; `project-v4-rust` already contains the
  relevant authority and proof workflow.
- Specs: unchanged; the current specs are evidence for the implementation gaps,
  not the source of the discrepancy.
- End-user/operator docs: unchanged at SOW creation; affected stale docs are
  explicitly required implementation artifacts.
- End-user/operator skills: none exist.
- SOW lifecycle: SOW-0027 is `Status: open` under `pending/`; SOW-0017 remains
  the sole in-progress SOW pending the sequencing decision; SOW-0025 and
  SOW-0026 remain historical records.

Specs update:

- No update at SOW creation: implementation must conform to the current specs.

Project skills update:

- No update at SOW creation: no new proven reusable workflow exists yet.

End-user/operator docs update:

- Pending implementation. Rust README, Go package docs, worker deployment,
  platform support, and conformance documentation are in scope.

End-user/operator skills update:

- None currently exist; reassess before close if documentation changes create a
  downstream operator workflow.

Lessons:

- Completion evidence must be generated through the normative public path. An
  immutable file that cross-opens does not prove that live mutation,
  coordination, recovery, or failure semantics match.
- A parity ledger must map every required public operation to a compiled symbol,
  test, platform proof, and benchmark before a port can be accepted.

Follow-up mapping:

- All verified parity gaps are owned by this SOW.
- Snapshot signing remains tracked by SOW-0017 and must remain blocked until
  this SOW completes.
- No untracked deferred implementation item is accepted at SOW creation.

## Outcome

Pending.

## Lessons Extracted

Pending implementation and final review.

## Followup

- SOW-0017: snapshot signing remains dependent on accepted completion of this
  unsigned SDK parity SOW.

## Regression Log

None yet. This SOW records an incorrect prior acceptance, not a later regression
of behavior that was previously implemented and proven.
