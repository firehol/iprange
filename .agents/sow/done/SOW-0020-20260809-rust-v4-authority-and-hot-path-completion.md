# SOW-0020 - Rust v4 Authority and Hot-Path Completion

## Status

Status: completed

Sub-state: the superseded SOW-0016 remains closed without a successful-completion
claim. This focused SOW completed its iterative repair after the exact final
audit found no issue and the repaired source passed the full authorized native
matrix. Rust acceptance and any Go work remain user decisions.

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
- Extended the public scale benchmark to one million nested writes, exact feed
  replacement with 421 feeds, and membership import with 421 feeds. The new
  import case constructs both source and destination outside its timer.
- Added test-only necessary-work counters for page parses, key/cell/slot probes,
  structural slot scans, edit-fit probes, mapped bytes moved/zeroed, and
  membership-leaf reads. An exact release-symbol/string scan proved that this
  state compiles out of the benchmark binary.
- Replaced fixed-tree whole-page fit calculations with constant-time free-space
  arithmetic and removed the ordinary-edit scan of every slot offset. Full
  structural slot validation remains in operations that actually consume the
  complete slot structure and in explicit validation.
- Removed the direct-replacement coverage pass already computed by its old/new
  comparison. The permanent test now proves two source passes rather than
  accepting the old three-pass behavior.
- Stored the checked inline membership byte offset in the short-lived lookup
  token, so reading inline words no longer reparses and redecodes the selected
  dictionary leaf. No mapped reference or page cache is retained.
- Pinned release measurements after the fixed-tree repair were approximately
  2.23 seconds for one million nested overwrites and 2.00 seconds for one
  million exact-feed inputs with 421 feeds, down from 4.04 and 2.50 seconds.
  Membership import measured 1.49 seconds for one million source ranges with
  421 feeds. These are intermediate results, not acceptance claims.
- A frame-pointer profile after the repair moved the dominant nested/feed cost
  from whole-page fit scans to repeated fixed-tree lower-bound searches. This
  confirms that exact workflows still need the planned ordered merge/build and
  that arbitrary nested mutation still needs another hot-path audit.
- Added one persistent page-type registry and made range leaf/branch byte
  encoding, checked healthy decoding, and tolerant raw decoding authoritative
  in `range_tree`. Mutation, ordered construction, validation, and recovery now
  consume those definitions instead of carrying independent range layouts.
- Split fixed-tree read capability from mutation capability. A committed mapped
  generation can now drive the canonical tree readers without implementing
  allocation, COW, or retirement.
- Replaced exact named-feed clear-plus-per-range mutation and the later compare
  pass with destination-mapped input normalization followed by one ordered
  old/input merge. The merge builds the final range tree directly, updates
  membership refcounts, and calculates exact workflow statistics in that same
  sweep. Permanent counters prove that ingestion performs no membership work
  and finish performs exactly the two input streams and one output pass.
- The new randomized feed property test exposed a transaction allocator defect:
  a current-transaction scratch page freed and selected again through the free
  bitmap could be registered twice in the dirty-page chain. Reuse now preserves
  the page's existing chain link and budget charge. A direct regression test
  proves that the page is sealed once and the transaction remains committable.
- Exact feed replacement now measures approximately 0.98 to 1.13 seconds for
  one million unordered ranges with 421 feeds on the pinned reference P-core,
  down from the 2.50-second baseline. The complete all-feature/all-target Rust
  workspace graph and the four-target source graph pass after this slice.
- Replaced the separate feed and retention sweeps plus membership import's
  per-range general mutation with one authoritative ordered old/input merge.
  Policies now contain only the feed, retention, or import value rule and exact
  report accounting; mapped output construction, range alignment, coalescing,
  membership refcounts, old-tree retirement, and empty-input preservation have
  one implementation.
- Membership import now consumes its already ordered source incrementally,
  translates names through mapped operation-private state owned by the draft
  layer, and calculates the complete old/new comparison during the same sweep.
  The high-level importer no longer handles destination membership IDs, bitmap
  lengths, destination feed indexes, physical cache pages, or a final
  `compare_maps` pass.
- Permanent necessary-work evidence now requires exactly the source feed scan,
  source range scan, destination range scan, and one output pass for a
  representative import. The measured import path remains heap-allocation-free.
- Pinned release measurements after this consolidation are 0.804 seconds for
  one million exact-feed inputs with 421 feeds, 0.567 seconds for one million
  imported membership ranges with 421 feeds, and 0.440 seconds for one million
  retention inputs. The respective prior measurements were about 2.50, 1.49,
  and 0.42 seconds.
- The supported all-feature/all-target workspace graph passes with 354 Rust
  engine unit tests plus all integration, C ABI, conformance, crash, recovery,
  snapshot, and workflow tests. The four-target source graph, architecture
  boundary gate, mmap-only source gate, and warnings-denied Clippy also pass.
- Replaced the raw membership ID/bitmap-width pair exposed through
  `WriterEdit` and retained by high-level membership/feed workflows with one
  opaque transaction-bound handle. Only the draft-store membership authority
  can decode that handle; high-level code supplies feeds and logical algebra
  operations. The architecture gate now rejects restoration of the raw
  representation in those workflows, and the Rust/C membership test surfaces
  pass unchanged.
- Split selected-generation ownership from logical mapped reads. `ReaderCore`
  is now a 70-line owner/lifecycle object and its `read()` capability is the
  single implementation of healthy lookup, cursor, feed, membership, and
  metadata reads. Rust public readers, membership import, the C ownership
  bridge, and snapshot all use that same capability; the old forwarding layer
  was removed rather than retained as a second API implementation.
- Snapshot no longer reads `Mapping`, `MetaV4`, roots, raw range membership
  values, or dictionary IDs. Its high-level path passes the protected source
  into one bounded logical copier, which consumes reader-core streams and
  writes through the canonical mapped immutable-output builder. Source
  coordination retains the cached selected generation and performs the final
  check/release without returning physical metadata to the snapshot adapter.
- Permanent architecture checks reject direct physical access from snapshot
  orchestration/copying and reject construction of a logical generation reader
  outside the mapped owner and the one source-ownership bridge. The complete
  all-feature/all-target test graph, warnings-denied Clippy, architecture gate,
  and four-target source graph pass after this slice.
- Pinned release snapshot construction streams one million direct ranges in
  approximately 63-78 ms with no private residue. Same-session before/after
  reader measurements show no wrapper regression; lookups remain
  allocation-free and scans remain within run noise of the preceding commit.
- Centralized the remaining main-database page codecs. Common page headers,
  page checksums, bitmap geometry, slotted-tree layout inspection, blob leaf
  geometry, retirement records, feed-catalog branch records, and membership
  ID/hash records now each have one lower-level authority. Healthy access,
  mutation, validation, and recovery consume those authorities while validation
  and recovery retain their distinct strict and best-effort traversal policies.
- Removed the parallel physical-layout implementations from validation and
  recovery. Permanent architecture checks now reject restored common-header
  offsets, bitmap geometry, numeric tree-cell layouts, or checksum offsets in
  those untrusted paths; operation-private recovery scratch formats remain
  separate because they are not v4 database pages.
- This consolidation exposed a latent membership lookup defect: a hash branch
  cell was decoded as though it were a hash leaf record, so a sufficiently
  large reverse-lookup tree could reject its valid branch. The canonical branch
  decoder fixes the mismatch, and a permanent 160-membership test proves lookup
  across split hash-branch pages.
- The post-consolidation workspace passes 355 engine tests plus every
  integration, C ABI, conformance, crash, recovery, snapshot, and workflow
  suite. Warnings-denied Clippy, the architecture gate, four-target source
  graph, and mmap-only source gate also pass.
- Moved mapped page source/sink/edit capability into `page_io`, leaving slotted
  pages responsible only for their format and edits. Common page-header
  geometry now has one owner used by bootstrap, codecs, checksums, validation,
  recovery, and builders.
- Consolidated duplicated POSIX/Windows publication namespace policy above the
  syscall-specific implementations. Both platforms now share name validation,
  absence checks, creator checks, and stable directory-entry representation.
- Replaced the separate direct and membership recovery tree builders with one
  policy-driven range builder. Best-effort source selection remains separate;
  final mapped range-page construction and normalization now have one owner.
- The million-range nested-overwrite audit found repeated predecessor searches,
  separate mutation searches, generic leaf copies, variable-size split scans,
  and slot sorting on fixed-size range pages. The private-ingestion path now
  carries its already selected mapped path and typed predecessor into one
  replacement, and fixed codecs use arithmetic split selection plus linear
  packed-page truncation.
- The permanent nested-work test requires exactly 1,023 tree lookups for 1,024
  nested inputs: none for the first insertion and one for each later input.
  Arrival-order property tests continue to prove that each smaller later range
  overwrites only its covered portion.
- Five pinned release runs place one million nested overwrites at 0.307-0.328
  seconds, down from the 4.041-second SOW baseline. The same pinned build
  measured direct replacement at 0.427 seconds, retention at 0.568 seconds,
  421-feed replacement at 0.934 seconds, and 421-feed import at 0.449 seconds.
  Each timed writer path performs only 20-22 fixed setup allocations.
- Pinned one-million-operation readers measured 0.142 seconds for live and
  immutable 421-feed membership lookup, 0.073 seconds for direct lookup,
  0.007-0.013 seconds for direct scans, and 0.007-0.008 seconds for named-feed
  scans. All timed reader paths allocate zero bytes. A one-million-range
  snapshot measured 0.057 seconds.
- The complete supported workspace test graph, warnings-denied Clippy,
  architecture gate, 266-file mmap source scan, syscall-traced mmap runtime
  gate, and 361-source Linux/Windows/macOS/FreeBSD compile graph pass after this
  slice. The final clean-audit loop and native target runs remain pending.
- The cold-path authority audit found that `database.rs` mixes the public
  immutable-reader adapter with the mapped-file bootstrap/open primitives used
  by writers, lifecycle resolution, publication maintenance, validation, and
  recovery. It also found two independent empty-metadata constructors and live
  creation exporting mapped-file initialization through the public-writer
  module. The repair is to extract one private database-file authority for
  mapping, bootstrap, exact-empty construction/inspection, and geometry while
  leaving `database.rs` as a logical reader adapter. Architecture checks will
  cover `live_writer/create.rs` instead of exempting it.
- The same audit found an exact POSIX `openat`/identity clone between the normal
  retained-file opener and FreeBSD interrupted-transition opener. The repair is
  one Unix platform helper parameterized only by the required link-count policy;
  name-mutation code will no longer own a second retained-file opener.
- Healthy fixed-tree descent and leaf search are independently implemented in
  range lookup, feed lookup, membership lookup, and their cursors despite the
  existing generic fixed-tree codecs. After the file/lifecycle boundary repair,
  point lookup will move to one policy-driven fixed-tree query that keeps mapped
  bytes borrowed only for the call. Cursor consolidation will follow only where
  it deletes physical traversal authority without adding allocation or a
  generic abstraction to untrusted recovery traversal.
- Extracted one private database-file authority for mapped open/bootstrap,
  exact-empty construction and inspection, faultable open, sidecar absence,
  and database geometry. The public immutable-reader adapter no longer owns an
  empty metadata constructor, live creation delegates exact-empty construction,
  and the architecture gate no longer exempts that creation path. The duplicate
  Unix retained-file opener is now one helper parameterized by link-count
  policy.
- Replaced three independent healthy point-query descents with one generic
  fixed-tree query and one ordered lower-bound implementation. Range, feed-name,
  and membership lookup retain their typed codecs and result policies but no
  longer own page descent or cell search.
- Replaced the independent range, feed-catalog, and transaction-range traversal
  implementations with one allocation-free fixed-tree cursor. The cursor pins
  the selected generation, supports forward/backward iteration and seek, and
  has both shared and consuming sources so transaction output may grow without
  weakening the original generation bounds. The full engine library passes 355
  tests with four intentional ignores after this consolidation.
- The next boundary audit found a source-gate defect: `database.rs` and
  `live_reader.rs` still import mapped bootstrap/storage types and perform
  immutable open or live reader registration directly, while the gate checks
  those adapters only for two obsolete accessor names. The repair is to move
  immutable open and registered-reader lifecycle into `reader_core`, leave both
  public reader types as logical adapters, and make the gate reject physical
  dependencies in the complete adapter sources.
- Moved immutable open and the complete registered-live-reader state machine
  into `reader_core`. `database.rs` and `live_reader.rs` now contain only public
  logical calls and close-result translation; a new architecture detector
  rejects mapped-file, bootstrap, sidecar, and namespace ownership in either
  adapter. The complete workspace passes 355 engine tests with four intentional
  ignores plus every integration and C ABI suite after this repair.
- Split mapping lifecycle from mapped byte/page access and replaced the repeated
  page/range raw reads and writes with one checked mapped-access implementation.
  `PageView` remains one pointer wide, no page is copied out of the map, and the
  four-target warnings-denied source graph passes. POSIX SIGBUS and Windows
  in-page-error handlers now share one volatile mapped fault-record accessor
  instead of maintaining identical unsafe implementations.
- Removed the remaining physical types from public writer orchestration. Public
  transaction budgets are translated inside `WriterCore`, reclamation returns
  logical transaction/page counts instead of a retirement record, and the
  public create function delegates to the live-lifecycle owner. The writer
  architecture detector now rejects all mapped/database/page/tree/allocator/
  retirement module imports across every public writer source.
- Split the recovery/validation worker client and C-ABI error/support modules by
  responsibility without changing their wire protocol or public symbols. The
  target source graph now compiles the actual Unix, Windows, macOS, and FreeBSD
  configurations instead of hiding invalid conditional imports behind the
  Linux build.
- Removed duplicate blob, metadata, feed-catalog, membership-dictionary, and
  retirement record traversal/encoding. Healthy access, mutation, validation,
  and recovery now consume their canonical physical codecs; tolerant recovery
  behavior remains a separate policy over those codecs.
- Replaced fixed-tree first-touch reconstruction with one mapped-to-mapped COW
  page copy that changes only transaction ownership and checksum state. A
  permanent work assertion proves that copying a valid committed leaf does not
  revalidate or rebuild every cell during an ordinary write.
- Preserved the required committed free-bitmap validation before destructive
  reuse while removing repeated scans of transaction-private bitmap pages.
  Leaf and branch counts and summaries now update from the changed word/child,
  and a permanent counter proves a same-transaction update performs two scalar
  bitmap probes rather than rescanning the page.
- Removed the page-sized membership encoder image. Small memberships use a
  bounded 512-byte staging buffer; larger legal values use the existing mapped
  blob representation. Readers continue to accept either exact format
  representation, and the mmap source gate rejects restoration of a
  `MAX_ID_RECORD` stack or heap page image.
- Consolidated direct and membership recovery's identical ordered overlap-
  component state machine. One engine now owns cancellation, order checking,
  overlap grouping, overflow handling, and component completion; the two small
  policies retain only their genuinely different value resolution, reporting,
  and coalescing rules. All 43 focused recovery tests pass with the matched
  versioned worker.
- Removed avoidable private-page cleanup and reader ownership work. Discarding
  an unpublished transaction page no longer zeros bytes that can never become
  committed, and supported Linux live-handle checks now read one fork-wiped
  mapped marker rather than repeatedly querying process identity. The real fork
  subprocess tests still reject inherited handles.
- Replaced membership-delta's repeated lowest-key lookup/delete loop with one
  consuming fixed-tree cursor. Consecutive changes to one membership aggregate
  in one fixed pending record, spill only when the membership changes, and the
  temporary mapped tree is released once. Permanent work assertions require no
  per-range delta-tree lookup and exactly one final source pass.
- Kept arbitrary membership-import translation in bounded mapped state while
  adding one fixed last-translation entry for the common consecutive-ID case.
  The high-level importer now avoids entering the writer engine for that hit;
  the mapped cache remains authoritative for nonconsecutive recurrence.
- Replaced eager construction and destruction of the large SDK error enum on
  successful hot paths with the existing cold constructors. The final feed
  profile contains no error-drop cost above one percent, and warnings-denied
  Clippy passes without a broad lint exemption.
- Five pinned release runs at one million records place the medians at 0.511 s
  direct replacement, 0.487 s retention refresh, 0.376 s nested overwrite,
  0.501 s 421-feed replacement, 0.063 s 421-feed membership import, and 0.060 s
  snapshot construction. Every timed writer performs only 20-31 fixed setup
  allocations and leaves no private artifact.
- Five pinned one-million-operation reader runs place the medians at 0.074/0.064
  s live/immutable direct lookup, 0.098/0.090 s live/immutable 421-feed
  membership lookup, 0.0071/0.0076 s direct scan, and 0.0110/0.0086 s named-feed
  scan. Every timed reader reports zero allocations.
- Final-candidate frame-pointer profiles retain only required work: unordered
  feed normalization is dominated by B+tree lower-bound and selected-page
  access; nested overwrite by selected-path mutation and fixed-page edits;
  ordered import by source scanning, merge, and direct output construction;
  reader lookup by mapped page access and the required typed tree descents; and
  feed scan by the range cursor and membership projection. No profile contains
  implicit validation, per-record allocation, persistent-content I/O, page
  checksum work before commit, temporary sorting, cache churn, or delta-tree
  churn as a material cost.
- The repeated production-source audit counts 74,380 physical production lines
  in 282 inventoried Rust files before parser-empty exclusions. At a meaningful
  15-line/100-token threshold, clone detection finds eight small clones totaling
  154 lines (0.21%): frozen C entry-point shapes, public typestate wrappers, and
  direct/membership recovery policy dispatch. None duplicates a persistent
  format operation. The largest function remains the 191-line recovery worker
  state machine; the next is the 122-line live-reset resolution state machine.
  Both are cohesive terminal-outcome coordinators, not duplicate format logic.
- The Rust 1.74.1 matrix exposed one unnecessary `const` on the consuming
  fixed-tree cursor constructor. Removing it preserves runtime behavior and
  restores the declared MSRV. Big-endian Miri then exposed one write to a
  cursor that was immediately dropped; deleting that dead assignment left all
  ten portable codec vectors clean without warnings.
- The exact current production inventory is 74,375 physical lines and 67,949
  code lines in the same 282 files. Exact-clone results remain eight shapes and
  154 lines (0.21%). Lizard reports 4,117 functions averaging 13.6 NLOC and
  cyclomatic complexity 3.39; the largest function is the 191-line recovery
  attempt state machine. The higher reported complexities are flat exact-field
  codecs or cohesive tree/publication/recovery state machines; splitting them
  would hide branches or add state transfer without removing a responsibility.
- Local Codacy Analysis CLI execution of Lizard and Semgrep over the
  production-only inventory found no additional actionable defect. The Rust
  security rules classify every required unsafe mapped/C/syscall boundary and
  flag the worker's argument, adjacent-executable, and temporary-directory
  bootstrap APIs generically. Manual inspection confirmed the worker treats
  arguments as untrusted protocol input, creates the random control file
  exclusively with creator-only access, verifies parent/child/control state,
  and performs the version handshake before scanning or destination mutation.
- Five current-source pinned runs place the million-range medians at 0.346 s
  direct replacement, 0.477 s retention, 0.303 s nested overwrite, 0.431 s
  421-feed replacement, 0.058 s membership import, and 0.059 s snapshot. The
  slowest observed writer sample was 0.721 s. One million live/immutable direct
  lookups measured 0.083/0.063 s, membership lookups 0.117/0.114 s, direct
  scans 0.0084/0.0068 s, and named-feed scans 0.0117/0.0089 s. Reader
  allocations remain zero; writer allocations remain the 20-31 fixed setup
  calls; descriptors remain stable and private residue zero.
- After those last repairs, both exact workspace feature matrices, all
  integration and C ABI tests, warnings-denied Clippy and rustdoc, formatting,
  architecture and static mmap gates, the 379-source/four-target compiler
  graph, and the syscall-traced runtime mmap gate pass. The Rust 1.74.1 full
  matrix and s390x Miri codec gate also pass.
- Nightly AddressSanitizer with leak detection passes all 361 active livedb
  library tests and all 15 C-boundary unit tests. Valgrind, using the preserved
  matching glibc runtime and debug symbols because the host loader is stripped,
  passes the dedicated raw-fork ownership test with zero memory errors and zero
  definite/indirect leaks. Three worker-free native C programs also pass under
  Valgrind. The two C programs that intentionally observe a concurrent
  validation worker are not Valgrind evidence: instrumentation changes their
  live-reader timing/identity condition and makes the fixtures abort before
  cleanup; their complete code paths pass under AddressSanitizer and the normal
  native-C suite.
- Native execution of pushed candidate `c4662dc` passed both feature matrices
  on FreeBSD, including the exact immutable/publication-supported and
  live-unsupported boundary. macOS exposed concurrent feed-catalog test files
  colliding because their names used only process ID and wall-clock nanoseconds;
  a per-process atomic suffix makes those fixtures distinct. Windows GNU passed
  the native C header and caller boundary but found a committed range page with
  a stale CRC after abort-retain-reuse.
- The Windows root cause was page ownership inferred from bytes alone. A tail
  retained from an aborted draft carries the same next transaction ID as its
  retry, so generic allocation mistook it for a page already linked into the
  retry's dirty chain. Monotonic tail allocation now always charges and links
  the page as new; only pages recycled inside the active draft may preserve an
  existing dirty link. The repair adds no lookup, scan, bulk clear, allocation,
  or I/O. A platform-independent regression constructs the retained stale tail
  and proves the retry seals it at prepare time.
- After the retained-tail repair, both local workspace feature matrices pass,
  including the new regression and the complete native-C suite. Formatting,
  warnings-denied Clippy and rustdoc, architecture, static and runtime mmap,
  and the 379-source/four-target compiler-graph gates also pass. Native proof
  remains pending on the exact repaired commit.
- Pushed repaired source commit `45aa57999dab284228a592cd20eb6b82a3793372`
  passed both complete workspace feature matrices natively on Windows GNU,
  macOS ARM64, and FreeBSD 14. Windows also passes the generated header and
  native C caller; FreeBSD proves immutable read/publication support and the
  intentional live-operation rejection boundary. The macOS feed-catalog
  collision reproducer passed 20 consecutive parallel runs.
- Repeated the complete production-only inventory, clone, complexity,
  necessary-work, release-counter, pinned performance, and frame-pointer
  profile audit after the native repairs. The exact inventory is 74,384
  physical lines, 67,956 code lines, 282 files, and 4,119 functions. Exact
  clone detection remains eight manually classified shapes totaling 154 lines
  (0.21%). The repeated audit found no duplicate persistent operation and no
  unnecessary material hot-path work.
- Repeated AddressSanitizer correctly with the version-matched worker built
  under the same instrumentation. All 362 active engine tests and all 15 C
  boundary tests pass with leak detection. The complete Rust 1.74.1
  workspace/all-features/all-targets matrix also passes with warnings denied.

## Pre-Native Candidate Audit - 2026-08-09

### TL;DR

The repeated local audit finds no remaining avoidable hot-path work, duplicate
persistent-format operation, or physical-format access above the internal
engine. The implementation candidate is ready for exact-commit native testing;
this is not the final audit or completion claim because Windows, macOS, and
FreeBSD execution is still pending.

### Verdict

The local implementation audit was clean, but native execution disproved the
candidate. SOW-0020 remains in progress and the audit/repair loop has resumed.

### Current performance

Five CPU-pinned one-million-operation runs measured these medians:

| Operation | Median |
| --- | ---: |
| direct replacement | 0.346 s |
| retention refresh | 0.477 s |
| nested arrival-order overwrite | 0.303 s |
| exact feed replacement, 421 feeds | 0.431 s |
| membership import, 421 feeds | 0.058 s |
| compact snapshot | 0.059 s |
| live / immutable direct lookup | 0.083 / 0.063 s |
| live / immutable membership lookup | 0.117 / 0.114 s |
| live / immutable direct scan | 0.0084 / 0.0068 s |
| live / immutable named-feed scan | 0.0117 / 0.0089 s |

The slowest observed writer sample was 0.721 seconds. Readers allocate zero
bytes in the timed path; writers use 20-31 fixed setup allocations, not
per-record allocation. Descriptors remain stable and private residue is zero.

### Ranked findings

1. Windows correctness: after an aborted transaction retains mapped tail
   capacity for an active reader and a later transaction reuses that capacity,
   explicit live validation reports the committed generation invalid. The
   native C header/caller boundary passes; the failure is in live mapped-tail
   lifecycle or validation and is under investigation.
2. macOS test isolation: concurrent feed-catalog fixtures use only process ID
   and wall-clock nanoseconds for the same filename. macOS produced collisions,
   causing one test to overwrite another test's mapped database. The focused
   suite passes serially and fails immediately in parallel, proving a test-path
   collision rather than a feed-catalog format failure.

### The two-level architecture

- Public semantic adapters are `database.rs`, `live_reader.rs`, `live_writer`
  and its workflow modules, public Rust handles, and `iprange-capi`. They own
  typed requests, sequencing, cancellation, handles, and result translation.
- The private engine is `ReaderCore` for selected-generation reads and
  `WriterCore` over `DraftStore` for COW mutation. `database_file` owns mapped
  file bootstrap/creation; mapped-access and typed codec modules own bytes;
  `immutable_output` owns canonical mapped output construction.
- The compiled architecture gate rejects mapped files, metadata pages, roots,
  page numbers, allocators, retirement records, raw membership representation,
  and persistent codecs in public adapters. The complete adapter sources are
  checked, not a hand-picked import list.

### Physical-format authority

Every retained database page header, checksum field, page type, bitmap
geometry, tree record, membership record, feed record, metadata record, and
retirement record has one canonical codec/definition. Healthy query, cursor,
mutation, allocation, retirement, sealing, and mapped output each have one
implementation. Explicit validation and recovery consume those definitions but
retain separate strict/best-effort traversal policy because damaged pages
cannot satisfy healthy-reader assumptions.

### Where the production lines are

The production-only inventory is 74,375 physical lines and 67,949 code lines in
282 files. The largest areas are 13,870 lines of cross-platform namespace and
publication, 10,012 frozen C-ABI lines, 9,762 recovery lines, 5,303 isolated
fault-worker lines, and 5,089 validation lines. Public Rust writer/workflow
adapters are 3,691 lines and public reader adapters are 262 lines; the remainder
is the internal mapped engine, logical types, and output construction.

Exact-clone detection finds eight small shapes totaling 154 lines (0.21%): C
entry-point forms, public typestate wrappers, and distinct recovery policy
forms. None implements a persistent-format operation twice. The 4,117
production functions average 13.6 code lines and cyclomatic complexity 3.39;
the largest is a cohesive 191-line recovery-attempt state machine. These
measurements expose and bound the remaining size; they do not claim that a line
count can prove necessity.

### Retention

The public `RetentionRefresh` workflow is a typed semantic wrapper. The 106-line
retention policy supplies only the value rule to the shared ordered range merge:
coverage still present keeps its original timestamp, newly present coverage
gets the refresh timestamp, and missing coverage is removed. Normalization,
COW output, coalescing, statistics, and retirement are the same authoritative
engine operations used by the other ordered workflows.

### Recovery

Recovery remains large because it must safely interpret corrupted mappings in
the version-matched fault worker, select candidates, contain mapped faults, use
bounded mapped scratch/external sorting, preserve partial evidence, report exact
loss, and construct either direct or membership output. It reuses canonical v4
codecs, the shared overlap-component engine, the shared range builder, and
`immutable_output`. Its remaining direct/membership differences are value and
reporting policies, not second page/tree implementations.

### Implementation result

The refactor removed repeated fit/slot scans, repeated page reconstruction,
extra direct and workflow passes, repeated membership-leaf decode, per-range
membership-delta lookup/delete, raw membership state above the engine, and
duplicate codecs/traversals/builders. The baseline 4.041-second nested and
2.499-second feed workflows now measure 0.303 and 0.431 seconds. Final profiles
contain mapped page access, required tree search/edit, ordered merge, output
construction, and commit-time sealing/durability—not implicit validation,
pre-commit checksums, per-record allocation, persistent-content I/O, temporary
sorting, or cache/delta churn.

### Acceptance gates

- Local all-feature and no-default-feature workspace matrices: pass.
- Formatting, warnings-denied Clippy/rustdoc, architecture, mmap source/runtime,
  source graph, C header/manifest/export/native behavior, crash, conformance,
  Rust 1.74.1, and s390x Miri gates: pass.
- AddressSanitizer: 361 active engine tests and 15 C-boundary tests pass.
- Valid raw-fork Valgrind gate: zero errors and zero definite/indirect leaks.
- Release necessary-work proof and absence of counter symbols/state: pass.
- Native Windows, macOS, and FreeBSD on the exact pushed commit: pending and
  therefore completion remains blocked.

## Final Audit - 2026-08-09

### TL;DR

The exact repeated audit is clean. The Rust v4 implementation now has one
mapped-format authority beneath thin public semantic APIs, no measured
unnecessary hot-path work, and no remaining ranked finding. Correctness,
resource, ABI, sanitizer, performance, and native-platform gates pass on the
repaired source.

### Verdict

Pass. No evidence remains of duplicate persistent-format authority, physical
format access above the internal engine, implicit hot-path validation,
pre-commit checksum work, per-record allocation, persistent-content I/O,
temporary sorting, repeated workflow passes, or avoidable material reader or
writer work.

This is a measured engineering conclusion, not a claim that future evidence
cannot expose a defect. Any later regression reopens this SOW under the project
regression rules.

### Current performance

Five CPU-pinned one-million-operation runs measured these medians and observed
ranges on the reference Linux system:

| Operation | Median | Observed range | Median rate |
| --- | ---: | ---: | ---: |
| direct replacement | 0.4951 s | 0.3443-0.6565 s | 2.02 million/s |
| retention refresh | 0.3606 s | 0.3433-0.4737 s | 2.77 million/s |
| nested arrival-order overwrite | 0.3010 s | 0.2749-0.3666 s | 3.32 million/s |
| exact feed replacement, 421 feeds | 0.3486 s | 0.3295-0.4398 s | 2.87 million/s |
| membership import, 421 feeds | 0.0668 s | 0.0456-0.0934 s | 14.97 million/s |
| compact snapshot | 0.0816 s | 0.0663-0.0946 s | 12.26 million/s |
| live / immutable direct lookup | 0.0968 / 0.0815 s | 0.0866-0.1330 / 0.0740-0.1100 s | 10.33 / 12.28 million/s |
| live / immutable membership lookup | 0.1248 / 0.1094 s | 0.0885-0.1834 / 0.0808-0.1526 s | 8.01 / 9.14 million/s |
| live / immutable direct scan | 0.00835 / 0.00777 s | 0.00712-0.01052 / 0.00677-0.00949 s | 119.75 / 128.73 million/s |
| live / immutable named-feed scan | 0.01199 / 0.01146 s | 0.00874-0.01620 / 0.00814-0.01351 s | 83.40 / 87.25 million/s |

Every writer sample stayed below one second. Timed readers allocate zero;
writers make only 20-31 fixed setup allocations. Descriptors remain stable and
private residue is zero.

### Ranked findings

None.

### The two-level architecture

- Public semantic adapters own Rust/C handles, typed requests, workflow
  sequencing, cancellation, and result translation. They do not inspect
  mappings, metadata pages, roots, pages, allocators, raw membership IDs,
  bitmap widths, dictionary state, or persistent records.
- `ReaderCore::read()` is the single healthy selected-generation read
  capability. `WriterCore` over `DraftStore` is the single healthy COW
  mutation capability. `database_file` owns main-file bootstrap/creation;
  mapped byte/page and typed codec modules own physical access;
  `immutable_output` owns canonical mapped snapshot/recovery construction.
- Explicit validation and recovery are a separate untrusted-input boundary.
  They reuse the canonical codecs and mapped builders but retain strict and
  best-effort traversal policies required for damaged mappings.
- Compiled dependency and source gates cover every production source and
  reject restoration of a high-level physical bypass.

### Physical-format authority

Each persistent header, checksum field, page type, bitmap geometry, tree
record, range record, feed record, membership record, metadata record, and
retirement record has one canonical definition. Healthy query, cursor,
mutation, allocation, retirement, sealing, and committed-generation
publication each have one implementation. Snapshot and recovery construct
pages only through the canonical mapped output builder; no complete database
page exists outside a file-backed mapping.

### Where the production lines are

The production-only inventory contains 74,384 physical lines and 67,956 code
lines across 282 Rust files. The largest areas are 13,870 lines of
cross-platform namespace/publication, 10,012 frozen C ABI, 9,762 explicit
recovery, 5,303 isolated fault worker, and 5,089 explicit validation. Public
Rust writer/workflow adapters are 3,691 lines and public reader adapters are
262 lines; the rest is the internal engine, output construction, coordination,
and shared logical types.

Exact clone detection at 15 lines/100 tokens finds eight shapes totaling 154
lines (0.21%). Manual inspection classifies them as C entry-point forms,
typestate wrappers, and distinct damaged-input policy forms, not duplicate
persistent operations. The 4,119 production functions average 13.6 code lines
and cyclomatic complexity 3.39. The largest is a cohesive 191-line recovery
attempt state machine. These metrics expose the remaining size; they do not by
themselves prove every line necessary.

### Retention

Retention is a thin semantic policy over the shared ordered range merge.
Existing coverage keeps its original timestamp, newly present coverage gets
the refresh timestamp, and missing coverage is removed. Normalization,
coalescing, mapped COW output, statistics, and retirement are authoritative
engine operations shared with the other ordered workflows.

### Recovery

Recovery is intentionally separate from healthy reads because it handles
corrupt mappings: isolated mapped-fault containment, candidate selection,
bounded mapped scratch/external sorting, partial evidence, exact loss
reporting, cancellation, cleanup, and factual publication outcomes. It reuses
canonical v4 codecs, overlap processing, range construction, and mapped output.
Its direct/membership branches express value and reporting policy; they do not
reimplement pages or trees.

### Implementation result

The work removed whole-page fit/slot scans, repeated page reconstruction,
extra direct/feed/import passes, repeated membership decode, per-range
membership-delta lookup/delete, raw membership state above the engine, and
duplicate codecs/traversals/builders. The original 4.041-second nested and
2.499-second feed workflows now have 0.301 and 0.349-second medians.

Exact timed-region profiles now show only required mapped page access, tree
descent, selected-path COW edit, ordered merge, mapped output construction, and
commit-time sealing/durability. No material profile cost is implicit
validation, pre-commit checksum, per-record allocation, persistent-content I/O,
temporary sorting, page-cache churn, or repeated comparison/source work.

### Acceptance gates

- Both complete local workspace feature matrices: pass.
- Formatting, warnings-denied Clippy and rustdoc, architecture, mmap source and
  runtime syscall trace, 379-source/four-target source graph, C header,
  manifest, exports, native C behavior, crash, conformance, and workflow
  properties: pass.
- Rust 1.74.1 workspace/all-features/all-targets matrix: pass.
- s390x Miri authoritative portable-codec vectors: pass.
- AddressSanitizer with leak detection: 362 active engine tests and 15 C
  boundary tests pass; four subprocess entry points are intentionally ignored.
- Valid raw-fork Valgrind gate: zero errors and zero definite/indirect leaks;
  three worker-free native C programs also pass.
- Necessary-work assertions and release absence of counter symbols, strings,
  state, and calls: pass.
- Five-run million-operation performance and exact timed-region profiles: pass.
- Exact clone, complexity, dead-code, same-failure, and physical-authority
  inventories: pass.
- Both complete feature matrices on pushed repaired source commit
  `45aa57999dab284228a592cd20eb6b82a3793372`: pass natively on Windows GNU,
  macOS ARM64, and FreeBSD 14, including their exact supported boundaries.

## Validation

Acceptance criteria evidence:

- The exact 11-section final audit above has no ranked finding.
- All one-million writer cases have medians and observed maxima below one
  second on the pinned reference CPU. Timed readers allocate zero and all
  dominant profiled costs map to required format work.
- Compiled dependency gates and manual source/call-path review prove the two
  implementation levels and one physical-format authority.

Tests or equivalent validation:

- Both all-target workspace feature matrices pass locally and natively on the
  three authorized platforms. Every integration, C ABI, conformance, crash,
  recovery, snapshot, and workflow suite passes.
- Formatting, warnings-denied Clippy/rustdoc, Rust 1.74.1, s390x Miri codec,
  architecture, mmap source/runtime, source-graph, AddressSanitizer, and valid
  Valgrind gates pass as detailed above.
- The sanitizer gate builds the exact adjacent worker under the same
  instrumentation before executing worker-dependent library tests; all 362
  active engine tests pass.
- Focused permanent tests prove constant-time fixed-page capacity checks, local
  slotted edits, exact workflow pass counts, zero-allocation readers, one
  membership-leaf read, canonical codecs, retained-tail retry sealing, and
  native fixture isolation.

Real-use evidence:

- The update-ipsets-shaped release benchmark covers one million direct,
  retention, nested, exact-feed, import, lookup, scan, and snapshot operations.
  Five-run pinned local measurements and exact timed-window profiles are
  recorded above. The complete supported native matrix passes on the pushed
  repaired source commit.

Reviewer findings:

- No external reviewer was used, as required. The direct first-principles audit
  was repeated after the Windows and macOS candidate findings were repaired;
  the repeated audit has no finding.

Same-failure scan:

- The architecture gate rejects physical high-level imports, duplicate database
  creation, copied fixed-tree leaves, and repeated membership-delta search/delete.
  The mmap source gate rejects persistent-content I/O and complete page images.
  Clone, source-graph, eager-hot-error, duplicate-layout, tail-allocation, and
  native test-name searches cover the repaired failure classes. The tail
  allocator has one production call path; current-draft private reuse retains
  its separate proven dirty-chain link.

Sensitive data gate:

- Durable artifacts contain only repository paths, synthetic workload descriptions,
  generic platforms, source evidence, and commit IDs. It contains no raw
  sensitive or operational data.

Artifact maintenance gate:

- `AGENTS.md`: reviewed; its v4 engineering philosophy already states the
  exact lean, mmap-only, two-level, Rust-first, measured-performance rules, so
  no closure change is needed.
- Runtime project skill: updated with the canonical ownership boundary,
  complete gate matrix, worker requirement, profiling method, and source-size
  audit workflow.
- Specs: reviewed; the implementation changes authority and removes waste
  without changing exact bytes, public semantics, C ABI, durability,
  validation/recovery policy, or platform support. Existing specs already state
  the resulting contract, so no spec edit is needed.
- End-user/operator docs: Rust README records architecture, production-size and
  clone/complexity evidence, resource behavior, platform proof, and final
  workload measurements.
- End-user/operator skills: none exist in this repository, so none can be
  affected.
- SOW lifecycle: SOW-0016 remains honestly closed as superseded; SOW-0020 is
  completed and moves to `done/` with this closure commit.

Specs update:

- No update required. Exact format and product contracts did not change; the
  implementation now conforms to their existing ownership/performance rules.

Project skills update:

- `.agents/skills/project-v4-rust/SKILL.md` contains the final canonical proof
  workflow and boundary.

End-user/operator docs update:

- `v4/rust/README.md` contains the final evidence.

End-user/operator skills update:

- No output/reference skill exists; no change is possible or required.

Lessons:

- Persistent bytes alone cannot prove that an aborted retained page belongs to
  a retry with the same transaction ID; draft ownership must be explicit.
- Native parallel tests need deterministic per-process uniqueness in addition
  to process ID and wall-clock time.
- Performance profiles must be restricted to the benchmark's timed stack;
  whole-process profiles include intentionally explicit post-timing validation.

Follow-up mapping:

- SOW-0017: Phase-2 signing, unchanged.
- SOW-0018: Phase-2 multi-file algebra, unchanged.
- Go port: intentionally blocked until explicit user acceptance of this Rust
  result.

## Outcome

The Rust v4 authority and hot-path repair is complete. The repeated exact audit
has no finding; local, sanitizer, compatibility, performance, and authorized
native-platform evidence passes. No Go or Phase-2 work was started.

## Lessons Extracted

- Keep ownership in runtime draft state when persistent transaction bytes can
  legitimately survive an abort.
- Treat every full native platform matrix as part of correctness, not a final
  packaging check.
- Profile only the timed operation before classifying work as hot-path cost.

## Followup

Existing Phase-2 SOW-0017 and SOW-0018 remain unchanged and blocked. The Go
port remains blocked until explicit user acceptance of the Rust result.

The pre-close `defer|later|follow-up|future|TODO|pending` search is mapped:
`pending` above records dated execution and the superseded pre-native audit;
those items are resolved by the final audit. Other `later`/`pending` uses
describe arrival-order or in-memory record state, not unfinished work. Phase-2
work is represented by SOW-0017 and SOW-0018.

## Regression Log

None yet.
