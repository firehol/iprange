# SOW-0020 - Rust v4 Authority and Hot-Path Completion

## Status

Status: in-progress

Sub-state: the superseded SOW-0016 is closed without a successful-completion
claim. This focused SOW starts from the 2026-08-09 repeated architecture and
performance audit. Implementation is authorized and proceeds iteratively. Rust
cannot be accepted, this SOW cannot be completed, and the goal cannot be marked
complete until the exact final audit below finds no issue.

## Requirements

### Purpose

Deliver the simplest clear Rust implementation of the exact unsigned Phase-1
v4 format: mmap-only, durable, bounded, portable, maintainable, and as fast as
the format architecture permits. The result must materially improve
`update-ipsets`: unordered input normalizes directly in the destination,
exact feed/direct/retention workflows avoid unnecessary passes, readers remain
allocation-free, and all persistent-format behavior has one authoritative
internal implementation.

Rust is completed, proven, benchmarked, and accepted before any Go port begins.
Snapshot signing and general multi-file algebra remain Phase 2.

### User Request

Close the previous oversized SOW and create a focused SOW containing the complete
repeated audit and its repairs. The goal is strict:

- no wasted or unnecessary work in reader or writer hot paths;
- maximum practical performance from the approved format architecture;
- strong separation of concerns;
- two defined implementation levels: public semantic APIs and one internal
  low-level mapped-format engine used by them;
- persistent layout, page types, codecs, traversal, mutation, allocation,
  retirement, sealing, and publication isolated below the public layer;
- repeat the same first-principles audit and the same report format after the
  implementation;
- if that audit finds any issue, keep the SOW and goal open and iterate until
  the audit is clean.

### Assistant Understanding

Facts:

- The normative product architecture already requires two public semantic API
  families over one authoritative internal owner. Public APIs expose logical
  operations; the SDK alone owns feed indexes, membership IDs, bitmaps,
  dictionary state, roots, pages, allocation, and publication.
- The current implementation has `ReaderCore` and `WriterCore`, but the
  repeated audit proved that they are not yet authoritative boundaries.
- The exact v4 bytes, public Rust semantics, frozen 136-symbol C ABI, durability,
  mmap-only storage, resource bounds, explicit validation/recovery, and platform
  boundaries remain unchanged.
- Validation and recovery must retain separate damaged-input traversal and error
  policy. They must reuse canonical byte codecs and mapped-output builders and
  must not add checks to healthy hot paths.
- The current release benchmark proves direct replacement and retention near
  0.42 seconds for one million ranges on the reference P-core, but nested
  overwrite takes about 4.04 seconds and 421-feed replacement about 2.50
  seconds.
- Existing architecture gates detect named imports and selected bypasses. They
  do not prove that layouts, codecs, or algorithms have one implementation.

Inferences:

- The largest immediate performance gain comes from removing the measured
  whole-page fit scan and unnecessary slotted-page extent scans before changing
  higher-level algorithms.
- Exact feed replacement and membership import need one internal ordered
  map-transform/build operation, analogous to the existing retention merge,
  rather than repeated generic arbitrary-range mutations.
- Physical-codec consolidation must proceed one persistent structure at a time;
  a repository-wide rewrite would create excessive integration risk.
- Code-size reduction is an expected consequence of deleting duplicate
  authorities, not a mechanical line-count exercise.

Unknowns:

- The exact final production line count cannot be known before duplicate
  physical definitions and adapters are removed.
- The final throughput after each repair must be measured; no performance gain
  is assumed from source structure alone.
- Publication, recovery, worker, and C ABI source may contain further semantic
  duplication. The audit has not yet proved a safe removable line count for
  those cold paths.

### Acceptance Criteria

- The exact final audit in this SOW is rerun from first principles and reported
  using the required report format below.
- The final audit has no finding of avoidable or unnecessary reader/writer
  hot-path work.
- Every retained dominant profile cost maps to an approved semantic,
  memory-safety, COW, mmap, durability, coordination, or resource-bound
  requirement.
- One million direct replacement, retention refresh, nested overwrite, exact
  feed replacement with 421 feeds, and representative membership import are
  benchmark cases. The working target is at or near one second per million
  ranges on the same pinned reference CPU. A miss is not silently accepted; it
  requires a proven irreducible cost and explicit user decision.
- Live and immutable direct lookup, membership lookup, direct scan, and
  named-feed scan each time at least one million real operations with setup,
  open, close, snapshot construction, and validation outside the timer.
- Release reader operations allocate zero bytes per operation. Writer ingestion
  has no per-record allocation, external sorting file, page-sized heap/stack
  image, or persistent-content I/O syscall.
- Test-only necessary-work accounting covers page parses, tree descents, key and
  cell probes, slot scans, edit-fit probes, mapped bytes moved/zeroed, range
  input/output, dictionary revisits, page creation/copy/split/retirement/sealing,
  mapping changes, flushes, syncs, and source/output passes where applicable.
- Necessary-work assertions are derived from operation and tree shape; tests do
  not freeze known waste as expected behavior.
- Release binaries contain no work-counter state, symbol, field string, or call.
- The implementation has two levels:
  1. public semantic adapters: advanced logical transactions, exact workflows,
     Rust handles, and the C ABI;
  2. one internal mapped-format engine: canonical layout/codecs, healthy reads,
     COW mutations, ordered rebuilds, allocation, retirement, sealing, and
     main-file generation publication.
- Public/high-level modules do not inspect `Mapping`, `MetaV4`, roots, page
  numbers, allocator state, raw membership IDs, bitmap word counts, dictionary
  hashes/refcounts, or physical page records.
- Every persistent page type, field offset, record codec, and healthy traversal
  has one authoritative definition. Validation and recovery reuse those byte
  definitions while keeping their independent tolerant policies.
- Snapshot uses logical reader-core streams and the canonical mapped-output
  builder. File creation is owned by the internal format/lifecycle layer.
- Source boundaries are enforced by module visibility plus compiled dependency
  tests that cover all production files; regular-expression import scans are
  supplementary evidence only.
- The production source is materially easier to explain and maintain. Duplicate,
  dead, unreachable, compatibility, test-only production machinery, and
  variations of the same persistent operation are removed. The 5,000-line goal
  remains directional, not mechanical.
- Exact format bytes, public behavior, frozen C ABI, mmap-only storage,
  durability, bounded resources, explicit validation/recovery, and supported
  platform boundaries do not regress.
- The complete local and authorized native platform validation matrix passes on
  the exact final commit.
- SOW-0020 remains `in-progress` after any failed final audit. It is moved to
  `done/` with `Status: completed` only together with the clean
  implementation and final evidence.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/specs/c-abi-v4.md`
- `.agents/skills/project-v4-rust/SKILL.md`
- superseded SOW-0016, including its final ownership/performance claims
- all production Rust source under `v4/rust/`
- current public benchmark, work counters, architecture/storage/source gates,
  release profiles, source-line classification, and compiled call paths

Current state:

- HEAD and `origin/master` are
  `edb144d880461a6421e9834a10e3ec111f559663` at SOW start.
- The classified production implementation contains 75,418 lines in 255 files;
  the prior SOW's slightly broader production accounting reports about 76,000
  lines.
- The prior final audit profiled direct replacement and retention but its scale
  matrix stopped nested overwrite at 100,000 ranges, feed replacement at 10,000
  ranges, and omitted membership import.
- A fresh million-range profile attributes 34.18% of nested overwrite and
  22.87% of feed replacement to `fixed_tree::page::edit_fits`.
- `edit_fits` computes a virtual encoded size by visiting every cell before
  the actual edit. Fixed-size records can determine capacity arithmetically.
- Slotted-page replacement/removal derives one record's end by scanning every
  slot, adding implicit structural validation to normal mutation.
- Direct replacement calculates current coverage, then performs an old/new
  comparison that already computes the same after-coverage. Its permanent test
  asserts three source passes.
- Exact feed replacement clears the target feed over the whole address space
  and then applies each input range through the general membership transform.
- Membership import applies every source range through the general transform
  and subsequently compares complete old/new maps for statistics, contradicting
  the required direct merge/build algorithm.
- Membership point lookup finds a dictionary leaf, then maps/parses/decodes the
  same leaf again to read inline words.
- Range, membership, blob, bitmap, metadata, and retirement physical knowledge
  is not gathered in one registry/codec layer. Range page types and record
  decoding alone are independently represented in healthy reads, mutation,
  ordered construction, and validation.
- `fixed_tree::Store` combines reads and mutations, structurally encouraging
  separate mapped-reader traversal implementations.
- `WriterEdit` exposes raw membership IDs and word counts; exact feed/import
  code manipulates them.
- Snapshot consumes `Mapping` and `MetaV4` directly rather than logical
  reader-core streams.

Baseline release measurements, pinned reference P-core:

| Operation | Work | Elapsed | Rate |
| --- | ---: | ---: | ---: |
| direct replacement | 1,000,000 ranges | 0.423 s | 2.36 M/s |
| retention refresh | 1,000,000 ranges | 0.421 s | 2.37 M/s |
| nested overwrite | 1,000,000 ranges | 4.041 s | 0.25 M/s |
| exact feed replacement | 1,000,000 ranges / 421 feeds | 2.499 s | 0.40 M/s |
| live direct lookup | 1,000,000 lookups | 82 ms | 12.1 M/s |
| immutable direct lookup | 1,000,000 lookups | 68 ms | 14.7 M/s |
| live membership lookup | 1,000,000 lookups / 421 feeds | 163 ms | 6.1 M/s |
| immutable membership lookup | 1,000,000 lookups / 421 feeds | 128 ms | 7.8 M/s |
| live/immutable direct scan | 1,000,000 records | 5.3/4.4 ms | 189/228 M/s |
| live/immutable feed scan | 1,000,000 records / 421 feeds | 6.6/5.6 ms | 152/180 M/s |

Production-source ownership classification:

| Area | Lines |
| --- | ---: |
| namespace/publication | 14,211 |
| format codecs and logical primitives | 11,176 |
| recovery | 10,323 |
| C ABI implementation | 10,149 |
| validation | 5,643 |
| fault worker and wire | 5,628 |
| live coordination/lifecycle | 4,950 |
| high-level and advanced writer API | 4,113 |
| healthy storage cores | 3,008 |
| mapping/bootstrap | 1,691 |
| snapshot/output builder | 1,566 |
| public reader API | 1,565 |
| C ABI Rust bridge | 753 |
| errors/control | 642 |

Risks:

- A page-edit optimization can weaken memory safety if it removes mapped bounds
  checks together with unnecessary whole-page validation.
- Consolidating codecs can accidentally change exact bytes, endianness,
  alignment, or tolerated normal-read corruption behavior.
- Ordered bulk rebuild can mis-handle arrival-order normalization, partial
  overlap, full-space IPv6, membership refcounts, absent membership, or no-op
  detection.
- Moving creation/snapshot responsibilities can change namespace, durability,
  cleanup, or factual outcome reporting.
- Recovery cannot be forced through healthy-reader assumptions; doing so would
  make damaged input unsafe and lose best-effort reporting.
- Mechanical abstraction can increase source size and indirection without
  deleting authority. Every slice must show the deleted duplicate and compiled
  caller.
- Performance can improve one workload while regressing another. All benchmark
  classes and scaling shapes must be rerun after each hot-path slice.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The implementation added reader/writer core facades without fully relocating
  physical-format ownership behind them. Physical codecs and traversals remain
  repeated across healthy reads, mutations, builders, validation, recovery, and
  snapshot.
- General arbitrary-range mutation is reused for exact whole-feed operations
  where an ordered merge/build is both simpler and asymptotically better.
- Defensive whole-page checking is performed inside hot mutations even though
  the contract requires only local memory-safety checks during ordinary access
  and reserves full structural validation for explicit `Validate`.
- Existing proof counted broad operations and profiled incomplete workload
  coverage. It therefore certified current behavior rather than minimum
  necessary work.

Evidence reviewed:

- Architecture contract:
  `.agents/sow/specs/design-iprange-engine.md:75-120`.
- Hot-path and validation contract:
  `.agents/sow/specs/design-iprange-engine.md:191-204,274-290,328-378`.
- Exact workflow algorithms:
  `.agents/sow/specs/binary-format-v4.md:2362-2494`.
- No implicit validation:
  `.agents/sow/specs/binary-format-v4.md:2687-2695`.
- Fixed-tree fit scan:
  `v4/rust/iprange-livedb/src/fixed_tree/page.rs:211-299` and
  `fixed_tree/insert.rs:213-232`.
- Slotted-page extent scan:
  `v4/rust/iprange-livedb/src/slotted_page.rs:301-377`.
- Exact feed replacement:
  `v4/rust/iprange-livedb/src/live_writer/feed_workflow.rs:186-220,277-330`.
- Membership import:
  `v4/rust/iprange-livedb/src/live_writer/membership_import.rs:252-300` and
  `membership_import/report.rs:11-47`.
- Direct extra pass and frozen counter:
  `v4/rust/iprange-livedb/src/live_writer/direct_workflow.rs:307-331`,
  `workflow/compare.rs:31-41`, and
  `direct_workflow_tests.rs:95-101`.
- Duplicated range/membership/blob physical definitions:
  `range_tree.rs`, `range_mutation.rs`, `range_bulk.rs`,
  `validation/range.rs`, `membership_dictionary/codec.rs`,
  `membership_tree.rs`, and `blob_tree.rs`.
- High-level physical state:
  `writer_core/edit.rs:78-126`, `snapshot/build.rs:19-62`, and
  `live_writer/create.rs:288-370`.
- Architecture gate limitations:
  `v4/rust/check-architecture.sh:67-75,124-156`.
- Existing work counters:
  `v4/rust/iprange-livedb/src/work.rs:7-79`.
- Release profiles:
  `fixed_tree::page::edit_fits` at 34.18% nested and 22.87% feed
  replacement, with no lost samples.

Affected contracts and surfaces:

- Internal Rust mapped-page views, slotted pages, fixed B+tree, range tree,
  feed catalog, membership dictionary/blob, bitmaps, metadata, retirement,
  allocation, page sealing, and publication.
- Reader core, writer core, advanced transactions, exact workflows, snapshot,
  validation, recovery, public Rust handles, and Rust-provided C adapter.
- Test-only necessary-work counters, public benchmark, architecture/mmap/source
  gates, shared conformance corpus, generated ABI artifacts, project skill,
  engine specification, Rust README, and SOW lifecycle.
- No released C v1/v2 CLI behavior or file is affected.

Existing patterns to reuse:

- `draft_store/retention.rs`: ordered two-input merge, direct final mapped
  output, integrated exact statistics.
- `range_bulk.rs` and `immutable_output`: direct mapped construction at final
  offsets.
- `feed_catalog::decode_entry`: canonical codec shared across callers.
- `mapping::ByteSource`, `PageView`, and mapped edit/sink traits: bounded raw
  mapped access without ordinary long-lived Rust references.
- `work.rs`: test-only zero-cost-in-release accounting mechanism.
- Existing exact byte vectors, conformance corpus, crash injection, mmap
  syscall tracing, allocation counter, and platform compiler graphs.

Risk and blast radius:

- Main risks are wrong bytes, memory unsafety, tree corruption, data loss,
  incorrect membership refcounts, incorrect no-op classification, durability
  regression, hidden allocation/I/O, and platform-specific namespace failure.
- Blast radius includes every Rust v4 reader/writer and the C ABI because all
  delegate into the internal engine.
- There is no compatibility blast radius for older v4 experiments: only the
  latest unreleased exact v4 is supported, and its current bytes must remain
  unchanged.

Sensitive data handling plan:

- Work uses synthetic benchmark databases, feed names, metadata, temporary
  paths, and injected failures only.
- SOWs, specs, skills, docs, tests, code comments, profiles, and reports must not
  contain credentials, operational infrastructure, customer/member names,
  personal data, identifying public IPs, private endpoints, or proprietary
  incidents.
- Native-platform evidence records generic platform and commit identity only.

Implementation plan:

1. Preserve baseline measurements and extend the benchmark/counter proof to the
   missing million-range workloads.
2. Remove fixed-tree/slotted-page redundant work with local safety proofs and
   rerun all hot-path benchmarks.
3. Introduce one canonical physical-format registry and split read-only page
   access from mutation/retirement capability.
4. Consolidate healthy traversal and codecs one persistent structure at a time,
   deleting old definitions in the same slice.
5. Add one internal ordered range-map transform/output primitive and move feed
   replacement and membership import to it with integrated statistics and
   refcount accounting.
6. Remove the direct extra pass and membership dictionary reread.
7. Move creation, snapshot, and exact workflows behind logical core operations;
   remove physical values from adapters.
8. Make validation/recovery consume canonical codecs and builders while keeping
   their independent fault-tolerant traversal/report policy.
9. Audit publication, lifecycle, worker wire, and C ABI for repeated state,
   schema, and translation only after the main engine boundary is stable.
10. Run full local and native proof, then repeat the exact audit. Any finding
    returns to the earliest relevant implementation step.

Validation plan:

- Focused unit/property tests after every slice.
- Both full workspace feature matrices, warnings-denied Clippy/rustdoc,
  formatting, Rust 1.74.1, source graph, architecture, static/runtime mmap,
  conformance, C ABI generation/export/header/native behavior, crash,
  sanitizer, fork, and big-endian codec gates.
- Public benchmark smoke and scale matrices plus explicit million-range direct,
  retention, nested, 421-feed replacement, membership import, snapshot, live
  and immutable lookup/scan cases.
- Repeated pinned measurements, scaling checks, allocation/RSS/FD/file-size/
  page-fault/artifact evidence, and frame-pointer profiles.
- Same-failure searches for every removed duplicate or hot-path check.
- Native Windows, macOS, and FreeBSD verification only on the exact pushed
  final commit under the standing user authorization.
- Final source-size, file-size, complexity, exact-clone, semantic-duplication,
  dependency, and physical-authority inventories.
- Final audit report uses exactly these sections:
  1. `TL;DR`
  2. `Verdict`
  3. `Current performance`
  4. `Ranked findings`
  5. `The two-level architecture`
  6. `Physical-format authority`
  7. `Where the production lines are`
  8. `Retention`
  9. `Recovery`
  10. `Implementation result`
  11. `Acceptance gates`
- A nonempty `Ranked findings` section blocks completion and triggers another
  repair/audit iteration.

Artifact impact plan:

- AGENTS.md: already contains the governing philosophy; update only if this work
  discovers a durable project-wide rule not already recorded.
- Runtime project skills: update `.agents/skills/project-v4-rust/SKILL.md`
  with the final canonical architecture/performance proof workflow.
- Specs: update the engine-design spec if internal ownership wording needs more
  exact enforcement; change binary-format/C-ABI specs only for clarified current
  behavior, never to excuse implementation drift.
- End-user/operator docs: update `v4/rust/README.md` with the final proven
  architecture, resource boundary, and benchmark evidence. Released C CLI/wiki
  docs remain unaffected because v4 is unreleased.
- End-user/operator skills: none currently exist; record this explicitly at
  closure.
- SOW lifecycle: SOW-0016 is closed as superseded and moved to `done/`.
  SOW-0020 is the sole active SOW and owns all findings until a clean audit.

Open-source reference evidence:

- No new external repository is needed for this implementation gate. The exact
  format, mmap-only constraint, public semantics, profiles, and duplication are
  project-specific, and the current code is the authoritative evidence.
- SOW-0016 already recorded historical COW/reader-table references to
  `LMDB/lmdb @ 389e1009a86c` and `cberner/redb @ fe0141159c73`. They do not
  override this project's simpler exact contract.

Open decisions:

- None. The user explicitly authorized implementation-detail decisions within
  the unchanged approved format/API/platform contract and required autonomous
  iteration until the final audit is clean.
- Any newly discovered caller-visible semantic, compatibility, durability,
  recovery-policy, platform-support, or destructive choice is not an
  implementation detail and must stop for a numbered user decision.

## Implications And Decisions

1. SOW-0016 is closed as `closed`, not `completed`, because its final
   optimality and ownership claims were disproved.
2. SOW-0020 is the sole implementation authority for this repair.
3. No exact v4 format byte, public semantic, C ABI symbol, durability guarantee,
   mmap-only rule, resource bound, explicit-validation policy, recovery policy,
   or platform boundary changes.
4. The implementation has two levels. Advanced and workflow APIs are two public
   semantic families within the public level; they do not become separate
   physical implementations.
5. Validation/recovery remain separate untrusted policies but share canonical
   physical byte definitions and final mapped builders.
6. Performance counters remain test-only or diagnostic-build-only and compile
   out of ordinary release binaries.
7. Mechanical file splitting, helper proliferation, clone-percentage reduction,
   and line-count reduction are not goals by themselves. Deleting duplicate
   authority and unnecessary work is.
8. The user-approved approximately-one-second million-range target remains the
   working performance gate.
9. No external reviewers or subagents are used for this work.
10. No Go implementation begins before final Rust acceptance.

## Plan

1. SOW lifecycle and immutable baseline.
2. Complete necessary-work/performance proof.
3. Page-edit hot-path repair.
4. Canonical mapped-format authority.
5. Exact workflow and snapshot migration.
6. Validation/recovery codec consolidation.
7. Cold-path authority and size audit.
8. Complete proof matrix.
9. Exact repeated audit and iterative repair.
10. Clean closure and user acceptance report.

## Execution Log

### 2026-08-09

- Repeated the architecture, source-size, call-graph, and release-profile audit
  from first principles.
- Disproved the prior no-removable-work and single-authority conclusions.
- Closed SOW-0016 as superseded without claiming successful completion.
- Created SOW-0020 with the unchanged product boundary, concrete root-cause
  model, ordered implementation, and mandatory clean-audit loop.
- No Rust implementation file changed in this setup slice.

## Validation

Acceptance criteria evidence:

- Pending implementation and clean final audit.

Tests or equivalent validation:

- Baseline benchmark and profile evidence is recorded above.
- Full post-change validation pending.

Real-use evidence:

- Pending update-ipsets-shaped final benchmark.

Reviewer findings:

- No external reviewers are authorized. The required final first-principles
  audit is performed directly and repeated after every candidate-complete state.

Same-failure scan:

- Pending per implementation slice.

Sensitive data gate:

- Current SOW contains only repository paths, synthetic workload descriptions,
  generic platforms, source evidence, and commit IDs. It contains no raw
  sensitive or operational data.

Artifact maintenance gate:

- AGENTS.md: pending final review.
- Runtime project skills: pending final architecture/proof update.
- Specs: pending final consistency review.
- End-user/operator docs: pending Rust README update.
- End-user/operator skills: none exist; confirm again at closure.
- SOW lifecycle: SOW-0016 superseded closure and SOW-0020 activation are part of
  the first implementation milestone.

Specs update:

- Pending final consistency review; no product behavior change is planned.

Project skills update:

- Pending final canonical proof workflow.

End-user/operator docs update:

- Pending final Rust architecture/performance evidence.

End-user/operator skills update:

- No output/reference skill currently exists.

Lessons:

- Pending final retrospective; initial inherited lessons are recorded in closed
  SOW-0016 and the root-cause model above.

Follow-up mapping:

- SOW-0017: Phase-2 signing, unchanged.
- SOW-0018: Phase-2 multi-file algebra, unchanged.
- Go port: blocked until explicit user acceptance of Rust after this SOW.

## Outcome

Pending. A clean repeated audit is mandatory.

## Lessons Extracted

Pending.

## Followup

Pending clean Rust acceptance. Existing Phase-2 SOW-0017 and SOW-0018 remain
unchanged and blocked.

## Regression Log

None yet.
