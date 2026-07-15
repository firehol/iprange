# SOW-0016 - v4.3 Audit Round 5: Adversarial Test Expansion

## Status

Status: in-progress

Sub-state: test-only adversarial audit and permanent regression coverage in progress

## Requirements

### Purpose

Make the v4 Go and Rust implementations trustworthy enough to become the
high-performance storage and comparison engine for update-ipsets. This round
must reveal correctness, corruption, durability, lifecycle, resource, and
performance failures before further implementation work. More permanent tests
are preferred over a narrowly minimal suite, including tests that already pass
when they document important boundaries and contracts.

### User Request

Create a new SOW and add failing tests directly to the normal Go and Rust test
suites. Think broadly about what can go wrong, identify testing gaps and corner
cases, and add all useful tests. Do not fix implementation code in this round.

### Assistant Understanding

Facts:

- Commit `20a15fc169766f80f0a3f304a093b9a95e4166f8` passes 553 normal tests
  (301 Rust and 252 Go), Go race validation, Rust formatting, and Rust Clippy.
- Independent control-flow review found untested wrong-result, corruption,
  crash-consistency, resource-exhaustion, cleanup, and performance paths.
- Go statement coverage is 79.6%, but important public and format paths remain
  untested or lightly tested. Examples include file-backed overlap wrappers,
  merged CIDR queries, overflow scope overlap iteration, multi-pass merge
  failure, and overflow-chain validation.
- The new scope-bitmap overflow representation is persisted under the existing
  v4.3 format identity and is read by allocation-bearing code before complete
  chain validation.
- The repository already contains older SOWs under `current/`, and the untracked
  SOW-0015 remains there. This SOW does not delete, rewrite, or combine those
  records.

Inferences:

- Green happy-path tests are insufficient for a mutable mmap COW database. The
  required confidence comes from hostile-file validation, injected storage
  failures, crash-point modeling, exact boundary cases, and mirrored language
  behavior.
- Tests that fail on the current commit should remain red until a separate
  implementation pass fixes their contracts. Tests that pass are still useful
  when they lock down format boundaries or cross-language semantics.

Unknowns:

- Whether v4.3 has been distributed as a stable persistent format. This affects
  the eventual versioning remedy for overflow pages, but does not block tests
  proving that incompatible encodings must be distinguishable or rejected.

### Acceptance Criteria

- Every confirmed defect has a deterministic permanent regression test.
- Shared behavior has equivalent Go and Rust coverage wherever both public
  surfaces exist.
- Overflow bitmap tests cover inline/overflow boundaries, multiple pages,
  corruption, ownership, resolution, overlap, rebuild, and reclamation.
- Transaction tests cover operation ordering, old-generation survival, failure
  poisoning, and retry rejection at every injectable commit stage.
- Free-list tests prove that no reachable page class can be authoritative free
  state.
- External-sort tests cover invalid configuration, spill and merge failures,
  terminal state, data preservation, file-descriptor bounds, and cleanup.
- Public API boundary tests cover previously untested wrappers and family
  endpoints without relying on private implementation details where avoidable.
- Test and benchmark code compiles and passes static checks. Red tests are
  catalogued as intentional contract failures, not flaky timing assertions.
- No Go or Rust implementation source is changed in this test-only round.

## Analysis

Sources checked:

- `v4/go/` implementation and existing tests.
- `v4/rust/iprange-livedb/src/`, tests, and benchmarks.
- `.agents/sow/specs/design-iprange-v4-livedb.md`.
- `.agents/sow/specs/design-iprange-v4-scope-api.md`.
- SOW-0015 Round 4 requirements and tests.
- Go statement coverage generated from the full baseline suite.

Current state:

- Overflow scopes resolve through an owned allocation, while the zero-copy
  resolver returns no value; overlap paths use only the zero-copy resolver.
- Overflow readers trust a persisted `u32` length before validating the chain
  and cap traversal using the unrelated B+tree height limit.
- Commit truncation happens before the first durability barrier and metadata
  publication.
- Free-list validation rejects only meta pages and roots, not every reachable
  data, scope, overflow, and chain page.
- Aggregate overlap overflow is not surfaced, and Rust pair aggregation performs
  a linear search for every pair contribution.
- External-sort failure paths can lose ownership of runs or buffered records.
- Scope mutations mark the registry dirty before validation and before knowing
  whether membership changed.

Risks:

- Some tests intentionally request behavior the current implementation cannot
  provide and will make the suite red until repaired.
- Corruption tests can accidentally allocate excessive memory if they call the
  unsafe resolution path. They must assert validation rejection first and use
  subprocess/resource bounds only when direct behavior must be exercised.
- Storage fault tests must use synthetic in-memory stores or temporary files and
  must never touch real v4 databases.
- Performance regressions should be represented by benchmarks or operation-count
  assertions, not flaky wall-clock test thresholds.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

1. The previous round tested newly identified examples but did not systematically
   test all consumers and corruption modes of the format extension it introduced.
2. Overflow pages are a new graph of owned pages, but validation still treats
   the scope leaf as the complete object. Length, page type, CRC, header, cycle,
   termination, exact byte count, and exclusive ownership are not proved.
3. The transaction tests record sync/meta order but omit truncation and several
   allocation/write failure points, allowing the old durable generation to be
   invalidated before the new generation is durable.
4. Free-list safety requires set disjointness against every reachable page, but
   tests currently exercise only meta pages, roots, and the chain head.
5. Overlap tests cover one-record cardinality overflow and bitmap mode, but not
   accumulated overflow or committed indirect overflow scopes.
6. External-sort lifecycle tests cover successful cleanup and bounded file
   descriptors, but not ownership and poisoning after spill/merge I/O failures.
7. Coverage shows additional public APIs and family/boundary paths with no direct
   behavioral tests.

Evidence reviewed:

- Scope overflow read/write and zero-copy resolution:
  `v4/go/scope_table.go:23-100,755-800` and
  `v4/rust/iprange-livedb/src/scope_table.rs:295-370,763-833`.
- Overlap resolution and aggregation:
  `v4/go/overlap.go:39-74,277-325` and
  `v4/rust/iprange-livedb/src/overlap.rs:45-173`.
- Commit/free-list ordering:
  `v4/go/writer.go:438-735`, `v4/go/free_list.go:127-199`,
  `v4/rust/iprange-livedb/src/writer.rs:411-668`, and
  `v4/rust/iprange-livedb/src/free_list.rs:110-192`.
- Scope and free-list validation:
  `v4/go/reader.go:438-531`, `v4/go/scope_table.go:930-1007`, and
  Rust mirrors in `reader.rs` and `scope_table.rs`.
- External-sort failure ownership:
  `v4/go/extsort.go:912-1055` and
  `v4/rust/iprange-livedb/src/extsort.rs:45-180,800-850`.
- Baseline Go function coverage: 79.6% statements overall; zero direct coverage
  for `QueryCIDRsMerged`, file-backed overlap/migration wrappers, multi-pass
  `mergeRunsToFile`, and committed indirect pair iteration.

Affected contracts and surfaces:

- v4.3 on-disk page graph, validation, double-meta durability, free-list
  ownership, and corruption rejection.
- Go and Rust Writer, Reader, scope, overlap, migration, feed migration,
  external-sort, cursor/query, and file-backed APIs.
- Normal unit/integration suites and deterministic benchmark suites.
- update-ipsets adoption readiness; no update-ipsets code is changed here.

Existing patterns to reuse:

- Synthetic images with checksum restamping from Round 4 corruption tests.
- Fault-injecting page stores from Round 4 transaction tests.
- Temporary-directory and subprocess isolation from external-sort/OS tests.
- Mirrored Go/Rust scenario naming and equivalent observable assertions.
- Benchmarks for scaling behavior instead of wall-clock correctness tests.

Risk and blast radius:

- Test files, benchmarks, and this SOW only. Production implementation remains
  unchanged, so there is no runtime or format migration blast radius.
- The expected red suite blocks accidental claims that v4 is ready; each red
  result must identify a specific contract and proposed implementation surface.
- Test fixtures use synthetic addresses and temporary storage only.

Sensitive data handling plan:

- No secrets, customer data, personal data, private endpoints, production paths,
  or real feed history are used. Durable artifacts contain only synthetic ranges,
  local relative paths, and sanitized technical evidence.

Implementation plan:

1. Add mirrored overflow-boundary, overlap, validation, ownership, rebuild, and
   reclamation tests.
2. Extend transaction fault stores and add crash-point, truncation-order, and
   writer-poisoning tests.
3. Add full reachable-page/free-list disjointness and scope-separator tests.
4. Add accumulated cardinality, deterministic ordering, indirect-scope, and
   Rust scaling benchmark coverage for overlap.
5. Add external-sort configuration, spill/merge failure, terminal-state, data
   preservation, and cleanup tests.
6. Add useful green public API/family/boundary tests identified by coverage,
   prioritizing behavior relevant to streaming update-ipsets adoption.
7. Compile and statically check both suites, run targeted tests, catalogue every
   expected failure, and verify no implementation files changed.

Validation plan:

- Run new tests individually first to distinguish expected contract failures
  from test defects.
- Run `go test ./... -count=1`, `go test -race ./... -count=1`, and `go vet ./...`.
- Run `cargo test --workspace --all-features -- --test-threads 1`, Rust formatting,
  and Clippy with warnings denied.
- Re-run coverage and inspect whether targeted zero-coverage public paths moved.
- Search all failures for the same pattern in both languages and nearby APIs.

Artifact impact plan:

- AGENTS.md: no workflow or project-wide guardrail change expected.
- Runtime project skills: none exist in this repository; no update expected.
- Specs: no contract is changed by tests; discovered spec contradictions are
  recorded for the later implementation repair rather than silently rewritten.
- End-user/operator docs: no released behavior changes in a test-only audit.
- End-user/operator skills: none affected.
- SOW lifecycle: SOW-0016 remains current while tests are intentionally red;
  implementation repair and final validation must be recorded before closure.
  Existing current SOW inconsistencies are reported but not modified here.

Open-source reference evidence:

- No external repository is needed for this round. Tests target this format's
  explicit local contracts and directly observed implementation control flow.

Open decisions:

- None blocking. The user explicitly selected a new SOW, broad permanent test
  coverage, mirrored Go/Rust tests, and test-first red-state delivery.

## Implications And Decisions

1. User decision: create a new SOW rather than extending the previous audit SOW.
2. User decision: add all useful corner-case tests, including tests that already
   pass, because permanent contract coverage is the goal.
3. User decision: expose failures first; implementation repair is a separate
   subsequent pass.

## Plan

1. Complete the adversarial test matrix and map each scenario to a public or
   durable-file contract.
2. Add Go tests and fault fixtures without touching implementation code.
3. Add equivalent Rust tests and deterministic benchmarks.
4. Run targeted and full validation, then record exact expected failures.

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

## Validation

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
- Rust Round 5 targeted matrix: public API 4/4 green; overflow 1 green/7 red;
  external sort 0/2; free list 0/1; limits 0/2; transactions 0/7; writable
  corruption 0/1.
- `cargo check --workspace --all-features --benches`: pass.
- Go and Rust all-to-all pair-scaling benchmarks compile and run.
- `./.agents/sow/audit.sh`: SOW-0016 passes its gate, status/directory,
  sensitive-data, and framework checks. The repository-wide audit remains
  partial because pre-existing SOW-0013 and SOW-0014 lack current-template
  gate text; this test-only SOW did not modify those unrelated records.

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

- `AGENTS.md`: no workflow or project-wide guardrail changed.
- Runtime project skills: this repository has no project runtime skills.
- Specs: tests expose implementation violations without changing the intended
  v4.3 contract; no spec was silently changed.
- End-user/operator docs and skills: no released behavior changed.
- SOW lifecycle: SOW-0016 remains current/in-progress while the permanent suite
  is intentionally red. SOW-0015 and unrelated untracked build artifacts were
  not modified.

Specs update:

- No contract change. The durability and overflow findings reinforce existing
  v4.3 rules. The eventual format-version remedy remains an implementation
  decision only if compatibility evidence proves the persisted extension was
  distributed.

Project skills update:

- No project runtime skills exist.

End-user/operator docs update:

- No released behavior changes.

End-user/operator skills update:

- None affected.

Lessons:

- Every new persisted page graph needs validation, ownership, reclamation,
  crash-point, and consumer tests, not only a successful round trip.
- A 32-level B+tree traversal guard cannot safely double as an overflow-chain
  length bound.
- Writable open needs full committed-state validation before any file-backed
  store can extend or map the input.
- Legal maximum identifiers and generations need checked exhaustion behavior;
  debug-language panics and release-language wrapping are both unacceptable.
- Pair-count scaling must be benchmarked by feed cardinality; record-count-only
  benchmarks hide the aggregation complexity.

Follow-up mapping:

- Implementation repair remains in this SOW because the intentional red tests
  are its acceptance gate. No defect is deferred or moved to an untracked
  future item.
- Implementation repair will follow against every reproducible failure.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

Implementation repair is required after the test-only audit is complete.

## Regression Log

None yet.
