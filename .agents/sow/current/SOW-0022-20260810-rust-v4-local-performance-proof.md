# SOW-0022 - Rust v4 Local Performance Proof

## Status

Status: in-progress

Sub-state: reopened after a fresh random-access audit found unmeasured reader
work and actionable writer navigation cost; repair and repeated audit are in
progress.

## Requirements

### Purpose

Prove and achieve the maximum defensible local performance of the Rust v4 SDK,
within the already approved v4 format and semantics, before any Go port starts.
The result must be fast because each operation performs only the work its
result requires: no hidden passes, repeated lookup or decoding, avoidable page
movement, premature checksumming, per-record allocation, or other housekeeping
in hot paths.

This is a performance-proof SOW, not permission to redesign the format or API.
It must preserve the mmap-only storage contract, correctness, durability,
bounded memory, one physical-format authority, and two-level API architecture.

### User Request

Create a SOW, set an autonomous goal, and proceed. First prove and optimize
local performance against what the fixed file format can provide. Only after
local performance is accepted should the benchmark suite be standardized and
loose CI regression limits be added. Locally use the authorized public feed
corpus to test realistic shapes. Do not start Go.

### Assistant Understanding

Facts:

- The current unsigned v4 format and public Rust semantics are fixed by the
  normative specifications and SOW-0021. This SOW does not authorize a second
  format, compatibility mode, sorting file, complete-feed heap materialization,
  ordinary file-content I/O, or page copies outside file-backed mappings.
- Ordinary readers and writers intentionally do safety checks needed for mapped
  access, but do not implicitly run whole-file validation. Explicit validation
  and recovery remain separate operations.
- Checksums belong to commit or immutable-output sealing, not per mutation.
- Existing test-only work counters compile out of release builds and already
  expose tree, page, slot, range, mapping, durability, membership, join, and
  aggregation work.
- The existing benchmark target measures public update-ipsets-shaped workflows,
  allocation, memory, descriptors, and file size, but executes one timed sample
  per process. It has no physical lower-bound kernels and previously used only
  synthetic workloads.
- A controlled 1,000,000-range direct-replace profile attributes about 51% of
  sampled CPU to fixed-tree page binary search, about 16% to page inspection,
  and about 8% to private-path selection. Commit-time page sealing is below 1%.
  The searched key path repeatedly traverses `fixed_tree/page.rs:144-203` and
  `slotted_page.rs:175-193,273-283`.
- A read-only inventory of the authorized public feed artifacts found 1,457
  `.ipset`/`.netset` files with about 48.2 million lines in aggregate. Median
  size is 198 lines, p99 is 318,404 lines, and the largest artifact
  is about 22.6 million lines. A 1-million-range diagnostic alone therefore
  does not cover the real long tail.
- Snapshot signing remains Phase 2 in pending SOW-0017. Rust acceptance remains
  an explicit user decision. No Go implementation may begin in this SOW.

Inferences:

- Prior sub-second or near-second timings prove that an operation is fast, not
  that it is optimal. A defensible optimality claim needs a format-derived
  minimum-work model, lower-bound measurements on the same mapping primitives,
  controlled profiles, necessary-work counts, and tested alternatives.
- A mathematical proof that no program can run faster on a general-purpose
  machine is impossible. The achievable proof is empirical and structural: all
  required work is named, production work is compared with physical lower
  bounds, every dominant cost is classified, and every stable maintainable
  improvement outside measurement noise is implemented and re-audited.
- The current direct-insertion profile shows where investigation must start; it
  does not yet prove the tree search is waste. Binary search may be required,
  while repeated generic cell decoding or redundant bounds/error construction
  inside each probe may not be.
- Real feed files should influence shape, density, ordering, overlap, and scale
  cases. Their literal addresses, filenames, and operational directory layout
  are not needed in committed fixtures or reports.

Unknowns:

- Which currently dominant instructions are semantically necessary and which
  are avoidable is not known until lower bounds and controlled A/B variants are
  measured.
- Final accepted local throughput and its stable variance are measurements, not
  design assumptions.
- CI limits cannot be selected honestly until the optimized local baselines
  exist. They will intentionally tolerate roughly twice the accepted local time
  plus an absolute noise allowance, rather than pretending shared CI is a
  benchmark laboratory.

### Acceptance Criteria

#### Minimum-work proof

- A per-operation work model covers new sorted, reverse, random disjoint, and
  random overlapping feed construction; adding the same inputs as a second
  feed; direct first-seen and last-seen refresh; point lookup; ordered scan;
  named membership lookup; cardinality and overlap aggregation; direct and
  membership joins; algebra; publication; validation; and the complete
  update-ipsets-shaped workflow.
- For every operation, the model identifies required source passes, tree
  descents, page visits, key/cell/slot probes, decodes, combinations, output
  records, page creation/copy/split/seal work, mapped growth/remap, flush, sync,
  bytes moved/zeroed, and allocations. Output-proportional work is separated
  from avoidable overhead.
- Permanent tests assert exact counts where deterministic and justified bounds
  where tree shape or requested output varies. Counts remain test-only and
  compile out of release builds.

#### Lower bounds and profiles

- Benchmark-only kernels measure the relevant physical floors without changing
  production semantics: sequential mapped record access, fixed-page key search,
  mapped page construction, sealing/digest work, and durability costs where the
  operating system permits meaningful isolation.
- Lower-bound results are explicitly labelled as component floors, not as
  promises that the full semantic operation can equal them.
- Timed-region profiles and hardware counters cover every material writer and
  reader family. Setup, fixture generation, explicit validation, and cleanup
  are either outside the timer or reported separately.
- Every dominant cost is classified as required output work, required format
  work, operating-system cost, measurement cost, or avoidable implementation
  work. Unclassified dominant cost keeps the SOW open.

#### Optimization

- Every avoidable cost with a stable effect outside measured noise is removed
  when the change remains simple, maintainable, and within the fixed contract.
  Alternatives are tested with interleaved repeated A/B runs; a single fastest
  observation is not evidence.
- Hot paths perform no implicit whole-file validation, premature checksum,
  persistent-content file I/O, complete-page off-mapping construction or copy,
  per-record allocation, hidden source rescan, temporary sorting database, or
  redundant decode/search/page reconstruction.
- Corruption containment and memory safety are not traded away for speed.
  Fast paths may reuse already established page invariants, but may not assume
  unchecked untrusted mapped offsets without a proven owning check.
- Reader work is materially cheaper than equivalent writer work except where
  the requested reader output itself dominates, such as emitting millions of
  feed names or aggregation cells. Any contrary result must be explained by a
  controlled profile or repaired.

#### Real workload evidence

- Synthetic cases cover exact controlled shapes from tiny inputs through at
  least 1 million ranges, including the four required order/overlap patterns
  both in a new file and as a second feed.
- Authorized public update-ipsets artifacts supply sanitized representative
  shape cases at median, upper quantiles, and the observed long tail, including
  at least one workload larger than 1 million input lines when its normalized
  form is supported by the SDK.
- Real-feed evidence records only aggregate size/order/overlap/density facts and
  timings. No operational configuration, credentials, private data, literal
  ranges, full filenames, or directory layout enters durable artifacts.
- The complete publisher-shaped benchmark demonstrates current-feed creation,
  first-seen and last-seen refresh, central named-feed replacement, overlap and
  provider work, algebra, publication, and final enumeration with truthful
  resource measurements.

#### Benchmark preservation and regression protection

- Only after the local audit finds no actionable wasted work, the benchmark
  runner gains reproducible warm-up, repeated samples, median/distribution
  reporting, machine/compiler/profile metadata, explicit fixture identity, and
  comparison with a preserved accepted baseline.
- CI runs a representative smoke subset and uses deliberately loose regression
  limits derived from accepted local measurements: approximately 2x local time
  plus a documented absolute noise allowance. CI thresholds detect disasters;
  they do not define optimality.
- The local full suite remains the acceptance authority for performance. The CI
  configuration does not turn unstable micro-timings into correctness gates.

#### Correctness, architecture, and delivery

- All Rust tests, all-features tests, source-graph checks, formatting, Clippy,
  generated ABI checks, documentation tests, mmap/source assertions, and
  relevant durability/recovery tests pass after optimization.
- The public high-level API continues to delegate file-format work exclusively
  to the internal low-level authorities. No benchmark-only production API or
  duplicate page/range algorithm is introduced.
- The final report repeats the same audit after the final change. Any remaining
  actionable waste, unexplained dominant cost, duplicate authority, correctness
  regression, or unbounded resource use triggers another repair/audit cycle.
- Rust is presented for explicit user acceptance with exact local and real-feed
  evidence. This SOW neither starts Go nor snapshot signing.

## Analysis

Sources checked:

- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/update-ipsets-v4-adoption-findings.md`
- `.agents/sow/done/SOW-0020-20260809-rust-v4-authority-and-hot-path-completion.md`
- `.agents/sow/done/SOW-0021-20260810-v4-update-ipsets-sdk-core.md`
- `v4/rust/iprange-livedb/src/work.rs`
- `v4/rust/iprange-livedb/src/fixed_tree/page.rs:144-203`
- `v4/rust/iprange-livedb/src/slotted_page.rs:175-193,273-283`
- `v4/rust/iprange-livedb/benches/update_ipsets.rs`
- `v4/rust/iprange-livedb/benches/update_ipsets/`
- Controlled `perf` timed-region profile and five-run pinned-core baseline on
  the local Linux workstation.
- Read-only aggregate inventory of authorized public update-ipsets feed files.

Current state:

- Existing tests and counters cover much of the required structural work, but
  there is no complete operation-by-operation minimum-work ledger.
- The benchmark is a useful scenario driver and allocation/resource checker,
  but one sample per process cannot quantify noise or establish a baseline.
- Current five-run medians on one pinned performance core are approximately
  0.40 s for 1M direct replace, 0.50 s first-seen, 0.43 s last-seen, 0.45 s
  random immutable construction, 0.56 s for membership matching that emits 4M
  names, 0.03 s for cardinalities, 0.03 s for selected-pair overlap, 0.07 s for
  direct join, 0.14 s for membership join, 0.15-0.16 s for analytical algebra,
  0.26 s for publishing algebra, and 1.83 s for the full workflow. Workstation
  noise is visible; these are investigation baselines, not accepted numbers.
- Controlled profiling isolates the timed SDK region and confirms checksum
  sealing is no longer a writer-hot-path problem. Repeated fixed-page search is
  now the largest observed direct-write cost.

Risks:

- Removing mapped-input checks without an ownership proof can convert a clean
  corruption error into memory unsafety or a fault. Optimizations must preserve
  the mapped safety boundary.
- Synthetic generators can reward a special case absent from production. Real
  feed shapes and full workflow evidence are required before acceptance.
- Filesystem cache state, CPU scheduling, thermal state, ASLR, allocator state,
  and background load can move timings. Interleaved samples and hardware/work
  counters must separate code effects from noise.
- A benchmark framework requiring a newer Rust compiler would violate the 1.74
  MSRV. The final harness must remain compatible or use a small local runner.
- Optimizing one path by adding a parallel algorithm can violate single
  authority and increase maintenance. Shared low-level primitives must remain
  authoritative.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- SOW-0021 established broad correctness and reported strong timings, but its
  proof method was target-based: one-million-range cases, a single timed sample,
  and profiles of known regressions. It did not compare each operation to the
  minimum work forced by the format or to measured mapped component floors.
- The first controlled profile demonstrates a concrete opportunity boundary:
  direct random insertion spends most sampled CPU in the tree-search stack,
  especially repeated generic key/cell/slot access. The root-cause question is
  whether each layer contributes necessary search work or repeats checks and
  abstraction work after the page invariant is already established.
- The real public corpus contains a much longer size tail than prior 1M cases.
  Local acceptance based only on 1M synthetic inputs can miss scaling defects.

Evidence reviewed:

- Normative specs, completed SOWs, production source, tests, work counters,
  benchmark driver, controlled profiles, pinned-core repeated timings, and a
  sanitized read-only feed inventory listed above.
- `LMDB/lmdb @ 567292b5d4896d558c7f4fffbf711b86432cc15a`
  - `libraries/liblmdb/lmdb.h:1560-1585` documents reserve and append fast paths.
  - `libraries/liblmdb/mdb.c:6707-6785` shows direct page binary search.
- `erthink/libmdbx @ f7a3a9323cacacfa9dc6137ae7a7252a67744ff0`
  - `mdbx.h:1660-1695` documents append-without-full-page-split and reserve modes.
  - `mdbx.c:15320-15410` implements a last-key append fast path before general
    seek.
- `cberner/redb @ beb7c8ec7af5c4c2a37867301b5289cc0b84f01b`
  - `src/tree_store/btree_base.rs:456-535` exposes zero-copy leaf access and
    binary position search over page-backed records.
- `RoaringBitmap/CRoaring @ 95e424b60f4e4d2cb2ae0176976d0d26aa6a3ebe`
  was inspected for compact bitmap search/cardinality patterns; its container
  model is not a direct replacement for v4 membership-combination records.

Affected contracts and surfaces:

- Internal mapped page views, fixed/slotted tree search and edits, range
  normalization and merge, immutable output, membership query/aggregation,
  joins, algebra, commit/sealing/durability, test-only work accounting, benchmark
  scenarios/measurement/reporting, project Rust workflow documentation, and
  SOW lifecycle.
- Public API behavior, on-disk bytes, format version, recovery behavior,
  durability ordering, and C ABI are compatibility constraints, not intended
  change surfaces.

Existing patterns to reuse:

- `work.rs` for zero-release-cost operation accounting.
- The public update-ipsets benchmark scenarios and timed-region perf control.
- `fixed_tree` page/cursor/gap authorities, mapped `PageEdit`/`PageSink`, ordered
  range merge, immutable mapped builder, and existing cancellation/reporting.
- Existing source-graph, mmap-only, allocation, descriptor, residue, validation,
  failure-injection, and model/property tests.

Risk and blast radius:

- Tree/page fast paths affect every reader and writer and therefore have the
  highest correctness blast radius. Changes require corruption, property,
  explicit-validation, mmap-fault, and cross-operation testing, not benchmarks
  alone.
- Merge/output specialization can silently change arrival-order overwrite,
  coverage union, timestamp, membership, or same-name semantics. Work models
  must be derived separately for each semantic family.
- Commit/durability changes can lose published data even while benchmarks pass.
  Durability ordering remains fixed; only proven redundant work may change.
- No format migration exists because the format is unchanged. Any finding that
  requires a format, public semantic, or API choice stops implementation for a
  user decision.

Sensitive data handling plan:

- Access only authorized public feed artifacts needed for local shape and
  performance measurements. Do not read environment files, credentials,
  operational configuration, logs, server data, or unrelated files under the
  update-ipsets installation.
- Durable artifacts contain sanitized aggregate counts, distributions, shape
  classifications, and timings only. They contain no literal feed ranges, full
  operational paths or filenames, secrets, customer/community data, personal
  data, private endpoints, or proprietary incident details.

Implementation plan:

1. Complete the format-derived minimum-work ledger and add missing test-only
   counters/assertions without changing release behavior.
2. Add benchmark-only mapped lower-bound kernels and controlled repeated local
   measurement; profile all material reader/writer families.
3. Rank avoidable work by measured cost. Optimize one shared low-level authority
   at a time, run focused correctness tests, and use interleaved A/B evidence.
4. Repeat the full synthetic and sanitized real-feed audit until all dominant
   costs are classified and no stable actionable waste remains.
5. Only then standardize result capture and add loose CI regression protection;
   run the complete correctness, architecture, resource, and documentation gate.
6. Update durable artifacts with exact accepted evidence, complete and move the
   SOW, and present Rust for explicit user acceptance. Do not start Go.

Validation plan:

- Focused unit/property/model tests after each low-level change.
- `./v4/rust/check-source-graph.sh`.
- `cargo test --manifest-path v4/rust/Cargo.toml` and `--all-features`.
- Formatting, Clippy with warnings denied, docs, generated header/ABI checks,
  mmap/source assertions, benchmark compilation, and project SOW audit.
- Pinned-core repeated local benchmarks with release and native/profile builds;
  controlled `perf record/stat`, allocation/RSS/descriptor/file-size evidence,
  cold/warm cache labelling, and interleaved before/after comparisons.
- Sanitized representative real-feed runs including the observed long tail and
  complete publisher-shaped workflow.
- Final same-failure searches for repeated parsing, duplicate page authority,
  per-record allocation, persistent-content file I/O, pre-commit checksum,
  extra passes, off-mapping page copies, and hidden scratch files.
- Repeat the complete audit after the last optimization; any finding reopens the
  repair loop.

Artifact impact plan:

- AGENTS.md: philosophy and locked order already cover the work; update only if
  a genuinely project-wide guardrail is learned.
- Runtime project skills: update `project-v4-rust` with the proven local
  performance workflow and accepted commands after the audit stabilizes.
- Specs: no format/API change is intended; update only for a discovered current
  contract discrepancy, which may require a user decision first.
- End-user/operator docs: public behavior is unchanged; update the Rust README
  only with reproducible benchmark/use evidence that belongs to SDK users.
- End-user/operator skills: none exist, so no output/reference skill is expected
  to change unless one is introduced by separately approved scope.
- SOW lifecycle: SOW-0022 is the sole current SOW. Signing stays pending in
  SOW-0017; Go has no execution SOW until explicit Rust acceptance.

Open-source reference evidence:

- `LMDB/lmdb @ 567292b5d4896d558c7f4fffbf711b86432cc15a`
  - `libraries/liblmdb/lmdb.h:1560-1585`
  - `libraries/liblmdb/mdb.c:6707-6785`
- `erthink/libmdbx @ f7a3a9323cacacfa9dc6137ae7a7252a67744ff0`
  - `mdbx.h:1660-1695`
  - `mdbx.c:15320-15410`
- `cberner/redb @ beb7c8ec7af5c4c2a37867301b5289cc0b84f01b`
  - `src/tree_store/btree_base.rs:456-535`
- `RoaringBitmap/CRoaring @ 95e424b60f4e4d2cb2ae0176976d0d26aa6a3ebe`
  - Bitmap container search/cardinality sources were checked; no v4 design was
    copied because the persistent models differ.

Open decisions:

- None. The user approved local proof and optimization first, benchmark
  standardization second, and loose CI protection last. The public feed corpus
  is authorized for local read-only performance work.
- A format, semantic, public API, durability, safety, or compatibility change is
  not an implementation detail. If one appears necessary, this gate becomes
  `needs-user-decision` and implementation stops before that change.

## Implications And Decisions

1. Local performance proof precedes benchmark standardization.
   - Decision: accepted.
   - Implication: the existing benchmark remains an investigation tool until
     production hot paths are clean; its eventual standardization preserves the
     optimized result rather than defining it prematurely.
2. CI performance limits are intentionally loose.
   - Decision: accepted.
   - Implication: local controlled evidence establishes optimality; CI catches
     large regressions with approximately 2x timing headroom plus an absolute
     noise allowance.
3. Current v4 format and semantics are fixed.
   - Decision: inherited and reaffirmed.
   - Implication: optimization may simplify or specialize shared internals but
     may not alter bytes, behavior, durability, or public API without a new user
     decision.
4. Authorized public update-ipsets feeds may be used locally.
   - Decision: accepted.
   - Implication: realistic shape/scale evidence is required, while operational
     state and literal data remain outside durable artifacts.

## Plan

1. Establish minimum necessary work and component lower bounds.
2. Profile and rank every material public workflow.
3. Remove proven wasted work through shared low-level authorities.
4. Repeat correctness and local performance audits on synthetic and real shapes.
5. Preserve the accepted result with reproducible benchmarks and loose CI gates.
6. Complete the Rust evidence package and request explicit acceptance.

## Execution Log

### 2026-08-10

- Read the normative format/engine/update-ipsets specs, SOW-0020 and SOW-0021,
  the Rust runtime skill, current counters, benchmark harness, and initial hot
  paths.
- Inventoried authorized public feed artifacts using aggregate read-only
  metadata; did not inspect environment or credential files.
- Recorded five-run pinned-core investigation baselines and a controlled timed-
  region direct-write profile. Confirmed commit-time sealing is not dominant and
  fixed-tree search is the first optimization boundary.
- Checked LMDB, libmdbx, redb, and CRoaring source for comparable zero-copy page
  search, append, reserve, and bitmap patterns.
- Created SOW-0022 with a ready gate before production optimization.
- Specialized fixed-record page search only after the page shape and fixed cell
  width are established. This removed repeated generic cell machinery while
  retaining a checked persistent slot extent at every probe. Controlled lookup
  measurements improved by about 6-8%; forged-shape and forged-slot tests pass.
- Replaced four byte-at-a-time page-magic comparisons with one native 32-bit
  comparison. This removes about 1.5% of instructions in both measured reader
  and writer paths without weakening the mapped extent check.
- Reused the authoritative constant-coverage union to construct the first feed
  directly in the final range tree. Seven-run interleaved medians improved by
  about 13% for ascending and descending disjoint input and 6% for random
  disjoint input. A separate direct-assignment implementation was 2.5-5x slower
  and was removed rather than retained as a second authority.
- Reused the same ordered constant-coverage path for timestamp refresh input,
  with the existing general assignment path after disorder. Exact work tests
  prove ordered input does not descend once per range; random-input hardware
  instructions improved by about 1%, while full-union and generic-wrapper
  alternatives regressed and were removed.
- Rejected tested fixed-page cache, branchless-search, forced-inline,
  unreadable-page special-case, combined-header-mask, and full-union variants:
  none produced a stable maintainable gain outside noise.
- Streamed sanitized real public feed samples through file creation, commit,
  close, and explicit validation. Warm local creation took 58-63 ms for about
  318 thousand records, 232-254 ms for about 1.36 million records, and
  4.15-4.34 s for the 22.64-million-record long tail; separate validation took
  about 18 ms, 73-75 ms, and 174-178 ms respectively.
- Profiled the long-tail feed. About 36% of sampled CPU is in the deliberately
  simple benchmark parser, but the SDK still persists every ordered adjacent
  input before coalescing it: 22.64 million input records become only 3.09
  million canonical intervals. The next controlled optimization is to retain
  one pending constant-coverage interval and coalesce touching input before the
  existing authoritative tree mutation. It uses constant memory, preserves
  unordered coverage-union semantics, and removes avoidable mapped page edits;
  it does not introduce sorting, a second range algorithm, or a public change.
- Implemented that one-interval buffer inside the authoritative constant-value
  union. The largest public-feed replay fell from about 4.3 seconds to about
  1.2 seconds while producing the same 3.09-million-range validated result.
  Permanent randomized/model tests cover disorder, overlap, bridging, IPv4,
  IPv6, and transition from the ordered edge path to the general path.
- Removed repeated generic `Result`/cell machinery from fixed-record page
  search after checking the page shape once. Every persistent slot extent is
  still checked at every probe. Added a proof-reusing inspected-cell iterator,
  word-at-a-time extent marking, and fixed-layout validation specializations.
- Added raw mapped CRC32C dispatch for x86 SSE4.2 and ARM CRC, with a portable
  table fallback. The code never creates an ordinary Rust reference into an
  untrusted mapping; independent vectors, both native architectures, s390x
  codec proof, and AddressSanitizer cover the boundary. Page sealing remains a
  commit/final-output operation and is absent from mutation hot paths.
- Added component-floor kernels for mapped scanning, fixed-page search, mapped
  page construction, CRC32C, SHA-512, and mapped flush/sync. All six allocate
  zero heap objects inside their timed regions.
- Profiled every material writer and reader family. Required fixed-page binary
  search dominates random insertion and point lookup; output enumeration
  dominates scans and name matching; contribution/decode work dominates
  aggregation and membership joins; CRC dominates commit/validation; SHA-512
  dominates snapshot publication. No dominant cost remained unclassified.
- Replayed sanitized median, p99, and largest public-feed shapes with a separate
  mapped source harness. The largest profile spends about 62% in parsing and
  batching outside the SDK, and no SDK function reaches 5%.
- Only after the production audit was clean, added one-warm-up/five-sample local
  reporting, immutable fixture/compiler/machine metadata, semantic-result
  identity checks, an accepted 70-case baseline, and a three-sample 12-case CI
  disaster gate. CI limits are twice the accepted local median plus 100 ms;
  hosted-job startup noise does not define local performance.
- The first repeated benchmark audit found that result identity checked the
  scenario and size but omitted the auxiliary parameter. Fixing that check
  exposed incorrect auxiliary labels in seven matrix entries. The entries were
  corrected, all focused cases were rerun, and the complete 70-case local and
  12-case CI matrices were repeated. This was benchmark bookkeeping, not an SDK
  defect; the preserved baseline already contained the actual result values.

## Final Audit - 2026-08-10

### TL;DR

The repeated final audit is clean. Seventy local performance cases, sanitized
public-feed replay through 22.6 million input records, component floors,
controlled profiles, permanent work-count tests, both complete feature
matrices on four native systems, MSRV, Miri, AddressSanitizer, mmap enforcement,
architecture enforcement, and static source review expose no remaining
actionable waste or duplicate physical-format authority.

This is the strongest defensible empirical and structural result for the
audited workloads. It is not a mathematically impossible promise that no future
workload or hardware can reveal another improvement.

### Verdict

Pass. No actionable correctness, performance, ownership, duplication,
resource, portability, documentation, or maintainability finding remains.

The CI limits preserve this result against disasters; they do not establish
optimality. The controlled local audit remains the performance authority.

### Current performance

The accepted local baseline is a generic optimized Rust 1.91.1 build on x86_64
Linux with an Intel i9-12900K. Every case has one untimed warm-up and five
isolated samples with identical semantic results. The full 70-row evidence is
committed in `accepted-baseline.csv`.

| One-million-range feed input | First feed median | Second feed median | Final ranges |
| --- | ---: | ---: | ---: |
| ascending disjoint | 0.135 s | 0.155 s | 1,000,000 |
| descending disjoint | 0.134 s | 0.186 s | 1,000,000 |
| deterministic random disjoint | 0.470 s | 0.479 s | 1,000,000 |
| deterministic random overlap chain | 0.301 s | 0.285 s | 1 |

| Material publisher operation | Work | Median |
| --- | ---: | ---: |
| direct replacement | 1,000,000 ranges | 0.395 s |
| first-seen refresh | 1,000,000 ranges | 0.405 s |
| last-seen refresh | 1,000,000 ranges | 0.437 s |
| exact feed replacement | 1,000,000 ranges / 421 feeds | 0.403 s |
| membership import | 1,000,000 ranges / 421 feeds | 0.0450 s |
| seven-window history projection | 1,000,000 ranges | 0.0864 s |
| cardinalities | 1,000,000 ranges / 64 feeds | 0.0336 s |
| direct provider join | 1,000,000 ranges / 421 feeds | 0.159 s |
| membership provider join | 1,000,000 ranges / 421 feeds | 0.115 s |
| global algebra count | 2,000,000 input ranges | 0.163 s |
| preserve-feed algebra publication | 2,000,000 input ranges | 0.228 s |
| complete publisher-shaped workflow | 13,600,000 work units | 1.439 s |

Mapped readers are materially faster than writers. One million direct point
lookups take 0.0653 seconds live and 0.0549 seconds immutable; membership point
lookups over 421 feeds take 0.0913 and 0.0829 seconds. One-million-range direct
scans take 0.00697 and 0.00646 seconds; named-feed scans take 0.00700 and 0.0106
seconds. Timed lookup and scan paths allocate nothing.

Explicit validation remains separate from normal access. One million direct
ranges validate in 0.0126 seconds, one million membership ranges with 421 feeds
in 0.0168 seconds, and the immutable direct snapshot in 0.0115 seconds. Sealing
one million already-built direct ranges at commit takes 0.00728 seconds.

The final independent 70-case repeat has 70 tracked results, stable semantic
outputs, and zero limit failures; its complete workflow median is 1.738 seconds
under normal workstation noise. The final 12-case CI-mode repeat also has zero
failures. Its largest baseline ratio is 1.39 and every case remains far below
the deliberately loose disaster limit.

The measured component floors are 0.472 ms for a one-million-record mapped
scan, 24.6 ms for one million fixed-page binary searches, 5.36 ms to construct
64 MiB of mapped pages, 6.76 ms to CRC32C 64 MiB, 81.7 ms to SHA-512 64 MiB,
and 25.1 ms to dirty, flush, and sync 64 MiB. These classify required costs;
they are not full-operation targets.

### Ranked findings

None.

### The two-level architecture

- Public APIs own typed sources, names, workflow choice, cancellation,
  commit/abort state, reports, Rust handles, and C translation. They do not own
  mappings, roots, pages, physical membership IDs, checksums, or allocation.
- `WriterCore` and `WriterEdit` are the sole healthy mutation boundary.
  `DraftStore` owns transaction roots, page identity, refcounts, retirement,
  and mapped allocation. Public direct/feed workflows only sequence these
  internal operations.
- `range_mutation` owns normalization and arrival-order range semantics once.
  `fixed_tree` owns mapped traversal, search, COW paths, split/delete, and
  fences once. `slotted_page` owns record layout and movement once. Checksum
  sealing and publication each have one owner.
- `ReaderCore`/`GenerationReader` own healthy selected-generation access.
  Explicit validation and recovery remain isolated untrusted-input policies
  over the same byte definitions and codecs; they are not healthy-path
  alternatives.
- `check-architecture.sh` and the four-target 431-source compiler graph pass.
  Release symbol/string inspection confirms test-only work counters are absent
  from optimized benchmark binaries.

### Physical-format authority

The work changes no on-disk byte, format version, public semantic, Rust API, C
ABI, durability point, recovery rule, or supported-platform boundary. It adds
faster implementations only after an existing page or mapped extent invariant
is established.

Each layout, codec, traversal, mutation, allocation, retirement, seal, and
publication operation still has one authoritative implementation. No complete
database page exists outside a file-backed mapping. Source and syscall-traced
runtime gates find no persistent-content read/write/seek transfer. Healthy
ingestion creates no sorting file, heap page, anonymous page image, or implicit
validation pass.

### Where the production lines are

The reproduced inventory counts tracked implementation files under
`iprange-livedb/src` and `iprange-capi/src`, excluding dedicated test modules:
313 files and 89,579 newline-counted lines. Lizard reports 81,259 code lines
across 4,838 functions, averaging 13.9 code lines and cyclomatic complexity
3.5. The largest function remains a cohesive 191-line recovery state machine.
Fifty files exceed the directional 500-line target; the largest has 950 lines
and no file reaches 1,000.

Strict exact-clone detection at 15 lines/100 tokens finds 18 shapes totaling
352 lines, or 0.39%. Manual inspection classifies them as frozen C report
forms, maintenance/typestate/typed-family wrappers, source adapters, output
error plumbing, lifecycle wrappers, publication setup, and distinct damaged-
input policies. None duplicates persistent layout, healthy traversal,
mutation, allocation, retirement, sealing, or page construction.

The local static-analysis pass reports 498 Lizard review signals, 733 Semgrep
matches, and zero Trivy findings. Of the Semgrep matches, 725 are the generic
unsafe-code pattern and eight are unchanged parser/security warnings. Every new
unsafe site is restricted to checked mapped extents in page, checksum, and
fixed-layout code. Manual classification found no actionable bug, dead code,
duplicate authority, or responsibility-reducing split. The implementation is
still far above the directional 5,000-line goal; this audit does not pretend
that a metric proves every line necessary. It states the narrower fact: no
concrete removable mechanism or duplicated physical operation was found.

### Retention

First-seen and last-seen remain special timestamp workflows over the same
range authority. The caller supplies the feed and refresh time through the
typed workflow, never arbitrary internal values or membership combinations.
The new ordered constant-value path coalesces input before mapped mutation and
falls back to the same arrival-order assignment authority on disorder. Full-
delta semantics and timestamp invariants are unchanged and model-tested.

### Recovery

Recovery is unchanged. It remains explicit, fault-contained, bounded, and
best-effort-capable for damaged mappings. It reuses canonical codecs and mapped
output construction but keeps its deliberately tolerant traversal policy
separate from healthy access. No recovery scratch mechanism enters ordinary
ingestion, and no I/O or validation fallback was added.

### Implementation result

The minimum-work ledger is now explicit:

| Operation family | Minimum required work and observed implementation |
| --- | --- |
| sorted/reverse feed construction | one source pass; one pending interval; one mapped output insertion per canonical interval; one retained edge path; dirty pages sealed once at commit |
| random/overlapping construction | one source pass; one fixed-tree search or affected-run operation where position is unknown; arrival-order normalization; output-proportional mapped edits; no sort or second source pass |
| first-seen/last-seen | one input normalization pass plus one ordered old/new merge; only changed output pages are built; timestamp policy is typed and internal |
| point lookup | one root-to-leaf descent and binary probes; membership adds one canonical membership decode; zero allocation |
| ordered scan | one bounded cursor visiting each required page/record once; requested output emission only; zero allocation |
| names/cardinality/overlap | one range scan, one membership resolution per distinct value, and requested name/cell contributions; four-million-name output remains output-dominated |
| provider joins/algebra | ordered cursor merge at input boundaries, selected membership decode/contribution, and output-proportional records/cells; no materialized temporary feed |
| snapshot/publication | one source scan, one mapped output build, one page seal, required whole-file digest, flush/sync, and namespace publication |
| explicit validation | each declared reachable page once, one CRC per page, format/semantic checks, and required membership-table probes; never called implicitly |
| complete publisher workflow | exact composition of the operations above; no hidden extra validation, source replay, sorting, or page cache |

Permanent work tests assert exact search reuse, ordered-edge behavior, bounded
page/COW/fence work, canonical interval persistence, allocation shape, and
commit-only sealing. Mapping grows only for required final pages; each private
path is copied at most once per transaction before reuse. Release builds contain
none of the test counters.

Controlled profiles close every material cost class:

- random direct/feed construction spends about 46% in required fixed-page
  lower-bound search and 12-13% in required page inspection;
- direct point lookup spends about 47% in fixed search, with the remainder in
  page view and range lookup; membership lookup adds required membership find
  and decode;
- ordered scans are cursor and requested-output work;
- cardinality, joins, algebra, and name matching are dominated by required
  contribution, decode, cursor, or output work;
- ten-million-range direct validation is dominated by CRC, reachability mark,
  node validation, and inspection; membership validation adds required
  cardinality and table work;
- ten-million-range commit is more than 94% hardware CRC32C; snapshot is led by
  required SHA-512, followed by cursor, builder, copy, and page update work;
- the full publisher workflow contains the expected composition and no
  unclassified dominant stack.

Rejected A/B variants include a fixed-page cache, branchless search, forced
inlining, alternate page-header masks, separate direct assignment, and full-
union wrappers. They either regressed, added authority, or produced no stable
gain outside noise and were removed rather than retained.

### Acceptance gates

- Complete local current-Rust workspace matrices, all targets, with all
  features and without default features: 551 passed, zero failed, six explicit
  ignored cases in each matrix.
- Rust 1.74.1 complete all-feature/all-target matrix: the same 551 passed, zero
  failed, six ignored. Ten authoritative portable-codec vectors pass under
  s390x Miri.
- Exact final-source native matrices, both feature configurations and all
  targets, pass on Windows GNU, macOS ARM64, and FreeBSD 14.
- AddressSanitizer with leak detection passes 389 engine tests and 19 C-ABI
  tests with no address or leak failure.
- Formatting, warnings-denied Clippy, warnings-denied rustdoc, workflow lint,
  diff hygiene, architecture, mmap-only source, syscall-traced mmap runtime,
  and the 431-source/four-target source graph pass.
- The accepted 70-case baseline, independent 70-case repeat, 12-case CI repeat,
  six component floors, reader/writer profiles, allocation/descriptor/residue
  checks, explicit validation, and sanitized public-feed replay all pass.
- Same-failure searches find no dead-code suppression, premature page seal,
  persistent-content I/O, complete off-mapping page image, healthy-path
  validation, ordinary-ingestion external sort, high-level physical bypass, or
  release work counter.
- Local Codacy-compatible static analysis, strict clone review, complexity
  review, and manual unsafe/authority review have no actionable finding. No
  subagent or external reviewer was used, as required by the user.

## Validation

Acceptance criteria evidence:

- The repeated 11-section audit above has an empty ranked-findings section.
- The minimum-work ledger, component floors, permanent work tests, controlled
  profiles, and final repeated matrices classify all dominant work and expose
  no remaining stable maintainable optimization.
- Architecture/source gates and manual call-path review prove two API levels
  over one physical-format authority.

Tests and real-use evidence:

- All current-Rust, MSRV, Miri, sanitizer, mmap, source, documentation,
  architecture, native-platform, benchmark, and static-analysis gates pass as
  recorded above.
- The authorized public corpus contains 1,457 feed artifacts and about 48.2
  million input lines. Median-ranked input normalizes 168 records to 148 in
  about 1.53 ms; p99 normalizes 318,373 to 261,610 in about 47.8 ms; the largest
  normalizes 22,637,111 to 3,094,652 in about 1.20 seconds and validates
  separately in about 34.8 ms. Durable artifacts record no names, paths, or
  literal ranges.

Reviewer findings:

- No subagent or external reviewer was used. Local static analysis and the
  repeated evidence-driven audit found no actionable final issue.
- The benchmark identity omission found during the first repeat was repaired;
  the same-failure search covered every result-identity field and the complete
  matrices were rerun.

Same-failure scan:

- Covered fixed/slotted page checks, validation loops, CRC callers, page-seal
  callers, source passes, sorting/scratch paths, allocations, physical imports,
  mmap-only storage, release counter symbols/strings, and unwired source files.
- No second instance of the repaired benchmark identity defect or actionable
  production analogue remains.

Sensitive data gate:

- Durable changes contain synthetic fixture identities, aggregate public-feed
  counts/timings, generic platform/compiler/CPU facts, and repository-relative
  paths only. They contain no raw secret, credential, operational configuration,
  source filename, literal range, private endpoint, customer/community data,
  personal data, or proprietary incident detail.

Artifact maintenance gate:

- `AGENTS.md`: reviewed; its mmap-only, two-level, lean, measured-performance,
  and Rust-first philosophy already governs the result. No update is needed.
- Runtime project skill: updated with exact worker preparation, local/CI
  repeated runners, component floors, result interpretation, and sanitized
  public-feed replay rules.
- Specs: reviewed; bytes, semantics, API, ABI, durability, recovery, and
  platform contracts are unchanged, so no normative spec update is needed.
- End-user/operator docs: `v4/rust/README.md` records the reproducible commands,
  accepted performance/resource evidence, reader/writer relationship,
  validation/commit costs, component floors, public-feed replay, and current
  source metrics.
- End-user/operator skills: none exist in this repository.
- SOW lifecycle: SOW-0022 is the sole current SOW and moves to `done/` with
  `Status: completed` in the closure commit.

Lessons:

- A time target is not an optimality proof. Minimum-work accounting, component
  floors, timed-stack profiles, and rejected A/B variants are all required.
- Real long-tail input exposed the largest waste: persisting adjacent source
  records before canonical coalescing. Synthetic one-million-range cases alone
  did not expose it.
- Benchmark identity must include every shape parameter. Repeated timing with
  mismatched labels can look stable while measuring a different case.
- Fast mapped validation requires one proof per page and proof reuse inside the
  page, not removal of corruption checks.

Follow-up mapping:

- Snapshot signing remains independently tracked by SOW-0017 and is outside
  this Phase-1 performance work.
- The Go port remains blocked until explicit user acceptance of the Rust result.
  No Go or Phase-2 implementation was started.
- The pre-close `defer|later|follow-up|future|TODO|pending` search maps all
  matches to dated investigation text, the real SOW-0017 item, or the explicit
  Rust-acceptance boundary; no untracked SOW-0022 work remains.

## Outcome

The Rust v4 SDK has a clean repeated performance and architecture audit. Its
hot paths contain no identified actionable wasted work, readers are materially
faster than writers, public-feed long-tail replay is parser/batching-bound, and
the result is preserved by reproducible local evidence plus a loose CI disaster
gate. Rust is ready for the user's explicit product-acceptance decision; this
SOW does not make that decision or start Go.

## Lessons Extracted

- Preserve performance through minimum-work tests and profiles, not timing
  thresholds alone.
- Coalesce canonical constant-value input before mapped mutation while keeping
  the general arrival-order path authoritative.
- Reuse validated page shape inside a page, but continue checking every
  persistent pointer/slot extent before access.
- Keep local controlled measurements authoritative and shared CI deliberately
  loose.

## Followup

- SOW-0017 remains the Phase-2 signing work item.
- A Go-port SOW may be created only after explicit Rust acceptance.

## Regression Log

## Regression - 2026-08-10

### What broke

- The completed audit classified one root-to-leaf descent per random input as
  required work and declared no actionable performance finding. A fresh exact
  timed-region profile shows that classification was too strong: fixed-page
  search, mapped page inspection, and private-path selection account for about
  74% of one-million-range random direct replacement. The B+tree remains the
  sole authority, but transaction-private navigation hints may safely avoid
  repeated upper-level descent when a candidate private leaf proves a local
  interior insertion.
- The committed point benchmarks enumerate addresses in ascending order at
  `v4/rust/iprange-livedb/benches/update_ipsets/scenarios.rs:144-156`. They prove
  sequential point throughput, not random point throughput. The previous
  reader conclusion therefore omitted a material access shape.
- Ordered empty-tree construction still inserts canonical records through the
  incremental edge editor even though the existing authoritative
  `range_bulk::Builder` can pack an already proven ascending prefix directly
  into final mapped pages.

### Evidence and why prior validation missed it

- Fresh one-million random direct profile: fixed-tree lower-bound search 49.38%,
  `DraftStore::inspect_page` 14.32%, and `private_path_select` 10.23%; leaf
  insertion is about 0.72% and whole-operation checksum sealing about 0.14%.
- Fresh hardware counters report about 1.50 billion cycles, 2.89 billion
  instructions, 508 million branches, and 11.1 million branch misses for one
  million random direct inputs.
- The clean pre-change seven-sample medians on the pinned local performance core
  are 311.873 ms for random direct replacement, 113.835 ms for first-feed
  ascending, 136.800 ms for first-feed descending, 352.205 ms for first-feed
  random disjoint, and 289.209 ms for first-feed random overlap.
- The prior audit profiled random mutation but treated repeated tree descent as
  inherently required, and its point generator happened to be ordered. It did
  not test a bounded untrusted leaf-navigation accelerator or a truly random
  point-query order.

### Approved minimal-complete repair

1. Reuse the existing mapped ordered range builder for a demonstrably ascending
   canonical prefix. If later input becomes unordered or overlapping, finalize
   that prefix in the same private destination and continue through the one
   authoritative mutation path. No input is reordered, buffered as a feed, or
   written to scratch.
2. Add a bounded, operation-private scalar leaf locator for private exact
   workflows. It stores no page bytes and is not persistent or authoritative.
   A hit may edit only a currently mapped private leaf after proving that the
   key is strictly inside that leaf, both adjacent records are local, the gap is
   valid, and the edit fits without a split or first-key change. Every stale,
   ambiguous, boundary, overlap, split, or full-leaf case falls back to the
   canonical root-to-leaf path. Correctness must be identical with the locator
   disabled.
3. Add permanent random-order direct and membership point benchmarks while
   retaining the existing sequential cases. Add test-only locator hit, miss,
   fallback, and necessary-work counters; they must compile out of release
   binaries.
4. Measure each change with interleaved repeated A/B runs. Retain only simple
   variants with stable material gains; delete unsuccessful prototypes. Repeat
   profiles, correctness/resource/architecture gates, synthetic feed shapes,
   and sanitized real-feed replay after the final change.

### Constraints and risk

- No on-disk byte, public API, C ABI, durability point, recovery behavior,
  format version, or Go code may change.
- The locator is a cache of scalar navigation facts, not a cache of mapped
  content. The mmap remains the only database-content cache, and no complete
  page may exist outside it.
- The locator's retained heap is capped by the existing transaction heap budget
  and by a small implementation ceiling; insufficient budget disables the
  optimization without changing behavior. It is released before later workflow
  stages can retain other heap state.
- The largest correctness risk is allowing a stale leaf hint to become storage
  authority. The restricted fast path, full local proof, and mandatory fallback
  prevent that. Corruption tests must prove malformed mapped content is not
  hidden by a cache miss.
- The ordered-builder risk is changing arrival-order overwrite semantics. It is
  allowed only while the consumed prefix is already canonical and strictly
  ascending; transition to general mutation applies every later input in its
  original order.

### Artifact and validation plan

- Production: shared low-level range/fixed-tree code only; public workflows may
  retain opaque operation state but may not inspect pages or hints.
- Benchmarks: add random point cases and refresh the accepted local baseline only
  after the repeated final audit is clean. CI limits remain deliberately loose.
- Tests: model/property tests for IPv4/IPv6, ordered-to-general transition,
  splits, first-key changes, overlaps, stale hints, tiny/zero heap budgets,
  cancellation/abort, and exact final ranges; necessary-work assertions for
  avoided descents and mandatory fallbacks.
- Gates: full project-v4-rust correctness, architecture, mmap, source graph,
  MSRV, sanitizer, Miri codec, docs, static/same-failure, and local performance
  workflow. Native cross-platform reruns are required only if shared source
  changes survive A/B and before re-completion.
- Specs and public docs: the contract is unchanged. Update performance evidence
  and the runtime project skill if the optimized workflow or benchmark commands
  change; otherwise record why each artifact is unaffected at closure.
- Sensitive data: real-feed evidence remains aggregate and sanitized; no names,
  paths, literal ranges, credentials, operational configuration, personal data,
  private endpoints, or proprietary data enter durable artifacts.

### Decisions

1. The user approved all recommended repairs after the locator explanation:
   ordered bulk construction, the bounded untrusted transaction-private leaf
   locator with mandatory fallback, and permanent random-reader coverage.
2. The B+tree remains the sole correctness authority. A locator miss or disabled
   locator must execute the exact canonical path and produce identical bytes and
   semantics permitted by the format.
3. This regression reopens SOW-0022. It is not a new feature SOW and does not
   authorize signing or the Go port.
4. A successful ordered empty-map build may carry its exact interval
   cardinality into the workflow report. This removes the otherwise redundant
   post-build full-tree scan; unordered or late-disordered input retains the
   authoritative comparison scan. The optimization must survive repeated A/B
   measurement and exact-report tests or be removed.

## Regression Final Audit - 2026-08-10

### TL;DR

The repeated local audit is clean after the regression repair. The final
implementation preserves one physical-format authority, adds no page cache or
second mutation algorithm, and removes substantial avoidable work from ordered
construction and random IPv4 mutation. The 79-case local matrix, 14-case CI
matrix, exact profiles, work counters, static analysis, clone review, and the
22.6-million-input public replay expose no remaining actionable local finding.

This is a local pass, not SOW closure. Exact final-source native Windows GNU,
macOS ARM64, and FreeBSD matrices remain required after the implementation
milestone is pushed.

### Verdict

Local pass. No actionable correctness, performance, ownership, duplication,
resource, or maintainability finding remains in the audited source and
workloads.

The claim is empirical and structural, not a mathematical claim that no future
hardware or workload can reveal another improvement. Native-platform evidence
is still pending and keeps this SOW in progress.

### Current performance

The preserved baseline and final repeat use a generic optimized Rust 1.91.1
build on x86_64 Linux with an Intel i9-12900K, one untimed warm-up, and five
isolated samples per case. All 79 final-repeat cases produced identical
semantic results and remained within the loose disaster limits.

| Operation | Accepted median | Final-repeat median |
| --- | ---: | ---: |
| direct replacement, 1M IPv4 ranges | 0.262 s | 0.260 s |
| direct replacement, 1M IPv6 ranges | 0.511 s | 0.501 s |
| first-seen refresh, 1M ranges | 0.337 s | 0.337 s |
| last-seen refresh, 1M ranges | 0.343 s | 0.350 s |
| exact feed replacement, 1M ranges / 421 feeds | 0.326 s | 0.308 s |
| membership import, 1M ranges / 421 feeds | 0.0414 s | 0.0407 s |
| compact snapshot, 1M ranges | 0.0427 s | 0.0456 s |
| complete publisher workflow | 1.217 s | 1.102 s |

| One-million-range feed input | First feed accepted median | Second feed accepted median | Final ranges |
| --- | ---: | ---: | ---: |
| ascending disjoint | 0.0368 s | 0.0683 s | 1,000,000 |
| descending disjoint | 0.129 s | 0.155 s | 1,000,000 |
| deterministic random disjoint | 0.361 s | 0.382 s | 1,000,000 |
| deterministic random overlap chain | 0.275 s | 0.278 s | 1 |

The true-random reader cases use a one-million-range tree and one million
deterministically shuffled point queries. Accepted medians are 0.223 s live and
0.202 s immutable for direct lookup, and 0.307 s live and 0.234 s immutable for
membership lookup with 421 feeds. Ordered scans emit one million records in
6-8 ms. Readers therefore remain materially cheaper than equivalent writers.

Final seven-run component-floor medians are 0.327 ms for a one-million-record
mapped scan, 24.5 ms for one million fixed-page searches, 4.41 ms for 64 MiB of
mapped page construction, 5.57 ms for CRC32C, 80.3 ms for SHA-512, and 23.5 ms
for mapped dirty/flush/sync.

The largest authorized public input was repeated three times on the final
source. It normalizes 22,637,111 inputs to 3,094,652 ranges in a 1.079-second
median including parsing, creation, commit, and close. Separate explicit full
validation takes 27.2 ms median.

### Ranked findings

None.

Exact final-source native platform execution remains an uncompleted acceptance
gate, not a discovered implementation defect.

### The two-level architecture

- Public Rust and C APIs still own typed inputs, feed names, workflow choice,
  cancellation, reports, and commit/abort sequencing only.
- `WriterCore` over `DraftStore` remains the sole healthy mapped mutation and
  transaction boundary. `ReaderCore` remains the sole healthy selected-
  generation reader boundary.
- `range_mutation` owns normalization and arrival-order semantics once;
  `fixed_tree` owns mapped search, COW paths, local edits, splits, deletion, and
  fences once; `slotted_page` owns physical record movement once.
- The new locator is private low-level state inside `range_mutation`. Higher-
  level workflows cannot inspect hints, roots, pages, or mappings.
- Validation and recovery remain separate untrusted policies over shared codecs
  and mapped builders. They do not enter healthy access paths.
- The architecture gate and release symbol/string inspection pass. Optimized
  binaries contain no test work-counter symbol, field string, storage, or call.

### Physical-format authority

No on-disk byte, format version, public Rust API, C ABI, durability point,
recovery rule, or supported-platform boundary changed.

The ordered fast path reuses the authoritative mapped `range_bulk::Builder` at
final offsets. The locator retains only bounded scalar `{first key, page
number}` hints. Every candidate is rechecked against a currently mapped private
leaf and may edit only a fully proved strict interior gap; committed, stale,
boundary, overlap, split, full-leaf, or ambiguous cases fall back to the
canonical root-to-leaf operation. No complete page, mapped byte, or ordinary
reference into a remappable region enters the locator.

Page checksums remain commit/final-output work. Ordinary ingestion performs no
implicit validation, external sort, scratch creation, persistent-content I/O,
or off-mapping page construction.

### Where the production lines are

The exact production inventory contains 315 files and 90,225 newline-counted
lines under the two Rust library `src/` trees, excluding dedicated tests.
Lizard reports 81,840 code lines across 4,871 functions, with averages of 13.9
code lines and cyclomatic complexity 3.5. The largest function remains a
cohesive 191-line recovery state machine. Forty-nine files exceed the
directional 500-line target; the largest has 950 lines and none reaches 1,000.

The repair's two initially oversized responsibilities were split by purpose:
`assign.rs` is 424 physical lines, `coverage.rs` is 492,
`coverage/input.rs` owns streamed input and ordered-prefix construction, and
`locator.rs` owns bounded navigation hints. The former 82-line/CCN-24 locator
insertion is now three single-purpose operations; the largest has NLOC 45 and
CCN 12. Removing forced inlining from `push_ordered` after the module split
regressed ascending construction from about 37 ms to about 150 ms. Restoring
the measured annotation returned it to 31-35 ms, so that one annotation is
retained as performance evidence rather than style.

Exact clone detection at 15 lines/100 tokens finds 11 groups totaling 237 lines
or 0.26%. Manual review classifies them as C maintenance wrappers, typed
workflow wrappers, reader facades, and separate direct/membership recovery
orchestration. None duplicates layout, traversal, mutation, allocation,
retirement, sealing, or page construction.

Codacy-compatible analysis reports 503 Lizard review signals, 733 Semgrep
matches, and zero Trivy findings. The Semgrep result is 725 generic unsafe-code
matches plus eight unchanged path/process warnings; its eight parser errors are
tool limitations on accepted Rust syntax. Manual review found no actionable
issue. The implementation remains far above the directional 5,000-line goal;
this audit makes the narrower defensible claim that it found no concrete
removable mechanism or duplicated physical operation.

### Retention

First-seen and last-seen remain typed special workflows over the same direct
range authority. The caller never owns arbitrary internal timestamp values or
membership combinations. Ordered input uses the shared mapped prefix builder;
late disorder continues through the authoritative arrival-order assignment
path. Full-delta and timestamp semantics are unchanged and model-tested.

### Recovery

Recovery is unchanged. It remains explicit, worker-contained, bounded, and
best-effort-capable for damaged mappings. Its external sort and scratch support
are recovery-only and never enter ordinary ingestion. It reuses canonical
codecs and mapped output construction without becoming a second healthy-file
authority.

### Implementation result

- A proven ascending canonical prefix is packed directly into final mapped
  pages. The second pending range establishes order; disorder finalizes that
  prefix in the same destination and applies every later input in arrival
  order through the canonical general path.
- Successful ordered empty-map feed creation carries exact `Cardinality129`
  into its report, removing one redundant post-build comparison scan. General
  input retains the authoritative comparison.
- The IPv4 locator is capped at 256 KiB. Direct assignment enables it once;
  feed union allocates it lazily only after general input. Eight consecutive
  local overlap conflicts release it, avoiding overhead on overwrite runs.
  IPv6 leaves did not benefit in controlled tests, so IPv6 retains the normal
  path without locator allocation.
- Random feed construction falls from about 3.04 billion instructions with the
  locator disabled to about 2.36 billion enabled, roughly 22% less. Overlap and
  nested overwrite instructions remain flat because the adaptive policy exits.
- Permanent tests compare enabled and disabled results across random disjoint
  inserts, splits, and later overlaps; reject committed-page hints; cover the
  ordered-to-general transition; and assert necessary feed work.
- Permanent benchmarks now separate repeated sequential point queries from
  true one-million-tree random queries for both direct and membership readers.

Rejected prototypes were deleted: following an incomplete hint was incorrect
and a property test caught record loss; teaching split results to the locator
was negligible; routing feed coverage through direct assignment regressed to
about 1.6 seconds; following-key variants added code without stable gain; and
the unannotated module split caused the measured ordered regression above.

The final random-feed profile attributes 41.7% to required fixed-page lower-
bound search. Locator learning and cached insertion together are below 5%; the
remaining dominant stacks are canonical fallback, mapped inspection, and
required edit work. No dominant cost is unclassified.

### Acceptance gates

Passed on exact local source:

- 79-case local and 14-case CI benchmark matrices, zero limit failures;
- seven-repeat component floors and three-repeat largest public-feed replay;
- allocation, descriptor, artifact-residue, result-shape, and explicit-
  validation checks;
- enabled/disabled differential, transition, corruption, and necessary-work
  tests;
- exact production static analysis, 11-clone manual review, file/function
  complexity review, same-failure searches, and release counter inspection;
- no dead-code suppression, premature page seal, persistent-content I/O,
  off-mapping complete page, healthy-path validation, ordinary-ingestion sort,
  high-level physical bypass, or release work counter.

The organized final source also passes both complete current-Rust feature
matrices, formatting, warnings-denied Clippy and rustdoc, architecture, mmap
source and runtime enforcement, the four-target source graph, the complete
Rust 1.74.1 matrix, all ten s390x Miri codec vectors, and AddressSanitizer with
leak detection for 397 active engine tests and 19 C-boundary tests. The exact
version-matched worker was built beside the tests with the same sanitizer
instrumentation.

Pending before completion:

- push the validated implementation milestone;
- run both exact feature matrices natively on Windows GNU, macOS ARM64, and
  FreeBSD from that pushed commit;
- update final validation evidence, complete and move this SOW, then commit and
  push the lifecycle closure.
