# SOW-0021 - Rust v4 update-ipsets SDK Core

## Status

Status: completed

Sub-state: delivered. Exact implementation candidate
`1fa819acab95d55eb31f577cf2c22c196d09ea55` is implemented, audited, pushed,
and clean on Linux, Windows GNU, macOS ARM64, and FreeBSD 14. This SOW move and
status are committed together as the closure record.

## Requirements

### Purpose

Make the Rust v4 SDK the long-term-best, minimal-complete, and performance-
optimal interval engine required at the heart of `update-ipsets`. The SDK must
let the application construct a downloaded feed once, maintain exact first-seen
and last-seen state, update the central named-feed database, compute overlaps
and provider attribution, and publish every set result as v4 without reparsing
text, materializing complete feeds in heap, or performing avoidable passes.

The storage implementation remains surgical. This SOW adds only the approved
semantic operations over the existing two-level architecture and one physical-
format authority. It does not authorize another format, engine, or product.

### User Request

Create and execute one SOW covering all SDK changes identified in the
update-ipsets suitability analysis. Always choose the long-term-best design and
deliver it as minimal-complete. Performance is paramount: every hot path must be
optimal, with no wasted or unnecessary work. Do not increase scope or drift
without explicit user authorization.

### Assistant Understanding

Facts:

- One exact unreleased v4 contract exists. There is no compatibility obligation
  for the experimental `retention` tag or API.
- A file has one address family and one physical value kind: `direct` or
  `membership`. The existing immutable 16-byte value tag can encode a
  well-known direct semantic without adding a field or page type.
- The format-level direct semantic enum must contain `first_seen` and
  `last_seen`; all other direct tags remain opaque generic values.
- `first_seen` means the start of an address's current uninterrupted presence,
  not its first appearance over all history.
- `last_seen` means the latest successful source observation. Its database
  contains current and recently absent addresses; entries outside the largest
  configured history window are removed.
- One `last_seen` database serves every configured history window. A window
  contains addresses whose value is strictly newer than its cutoff.
- Each feed therefore has one current downloaded v4 file, one `first_seen`
  direct v4 file, and one `last_seen` direct v4 file per address family in use.
- Callers identify membership feeds by name. They never assign feed indexes,
  membership IDs, bitmap words, or combinations.
- Unordered overlapping valued input is applied in arrival order. Value-free
  feed input is coverage union. Neither path uses a sorting file.
- A set-producing public operation returns a materialized v4 file plus exact
  statistics. An analytical operation returns exact statistics without creating
  a pointless result file.
- Same-named feeds across input files are one global logical feed and are
  aggregated during enumeration, without a temporary combined database.
- Multi-feed set results support preserved global provenance or one caller-named
  flat feed.
- Normal open/read/write performs no whole-file validation. Validation and
  recovery remain explicit and separate.
- Rust must be completed, proven, benchmarked, and accepted before any Go port.
  Snapshot signing remains separate pending SOW-0017.
- The current implementation already has one mapped healthy reader owner, one
  mapped healthy writer owner, an authoritative ordered old/input merge,
  append-only mapped immutable construction, named-feed lifecycle operations,
  publication, recovery, and test-only necessary-work counters.
- The current implementation has only the exact `retention` tag/workflow. It
  does not expose last-seen refresh, direct v4-to-v4 batched sources, one-pass
  history projection, named membership matches, overlap aggregation, provider
  joins, or the approved feed algebra.

Inferences:

- No new persistent page, root, index, journal, or alternate format is needed.
  The physical byte layout can remain unchanged while the tag's normative
  semantics and public enum change.
- `range_merge::Policy` is the single correct internal extension point for both
  timestamp refresh policies and their statistics. A second merge engine would
  duplicate physical authority.
- `immutable_output::Builder` is the single correct output owner. A public
  builder must be a semantic adapter over it, not another encoder.
- Ordered cursor joins are sufficient for provider attribution and multi-file
  algebra. Point lookups per input interval, complete-feed heap materialization,
  and temporary combined files are unnecessary work.
- One base-feed update and all of its history-window feeds are one natural
  related membership update. The SDK should scan last-seen once and use one
  membership transaction; the application retains cross-file crash coordination.
- Exact timestamp-removal evidence is visible during the required first-seen
  merge. An optional bounded batched sink can expose it without a second source
  pass or an unbounded returned collection.

Unknowns:

- Final measured throughput and source size cannot be known before the approved
  operations exist. Neither may be assumed; both are acceptance evidence.
- Workload-dependent membership-combination and pair-output density cannot be
  reduced below requested semantic output. Benchmarks must report scanned
  intervals and emitted aggregation cells separately.
- No product or format decision remains open. Rust type names and private helper
  placement may follow existing conventions as implementation details, but may
  not alter the semantics or scope recorded here.

### Acceptance Criteria

#### Semantic identity

- The public format contract exposes `Generic`, `FirstSeen`, and `LastSeen`
  direct semantics derived from the existing immutable value tag.
- Canonical tags are exactly `first_seen` and `last_seen`, with canonical NUL
  padding. Experimental `retention` semantics, APIs, tests, docs, and fixtures
  are removed rather than retained as compatibility.
- Creation, open information, explicit validation, snapshots, recovery, Rust
  API, and C ABI preserve and report the semantic consistently.
- Generic direct tags remain legal and opaque. ASN, Geo, and other caller-owned
  meanings do not become speculative format variants.

#### Timestamp workflows

- `FirstSeenRefresh(t)` consumes one complete unordered current snapshot. Old
  and current coverage keeps its old value, new-only coverage receives `t`, and
  old-only coverage is deleted, including partial-overlap splitting.
- `LastSeenRefresh(t, cutoff)` consumes one complete unordered current snapshot.
  Current coverage receives `t`; absent old coverage newer than `cutoff` keeps
  its old value; absent coverage at or below `cutoff` is deleted.
- Last-seen never moves backwards: replayed/out-of-order observations use the
  greater of the stored value and `t`.
- Both workflows enforce the exact semantic tag, accept empty snapshots, detect
  no-change, permit the existing one metadata stage, and obey whole-draft abort,
  cancellation, commit, cleanup, and outcome-unknown rules.
- Reports contain exact input, before, after, unchanged, value-changed, added,
  removed/expired, and normalized-range counts. Optional first-seen removal
  batches include old timestamp and exact removed interval/count; sink failure
  aborts the unpublished draft.
- Reports and sinks are computed during the required merge, with no second
  full-map comparison or scan.

#### Mapped v4 chaining and construction

- A pinned live or immutable named-feed reader can feed create/replace,
  first-seen, last-seen, and immutable-output workflows through a bounded
  batched source owned by the SDK.
- Chaining performs no text round trip, complete-feed heap materialization, page
  copy outside file-backed mappings, external sorting, or intermediate database.
- A public one-pass immutable single-feed workflow ingests unordered input into
  one private final database inode, stages optional JSON, seals and publishes it
  through existing authorities, and returns statistics and truthful outcome.
- It reuses the authoritative normalizer and mapped output builder; it does not
  create a live database and copy it into a second snapshot database.

#### History projection

- One operation consumes one pinned `last_seen` database and a nonempty unique
  list of `(feed_name, cutoff)` windows, then updates all destination feeds from
  one last-seen scan and one membership transaction.
- Each feed contains exactly addresses with `last_seen > cutoff`; empty feeds
  stay cataloged and adjacent qualifying intervals coalesce.
- The caller supplies names and cutoffs only. The SDK owns feed indexes,
  membership combinations, refcounts, and mapped changes.
- Per-window and aggregate before/after/add/remove statistics require no
  independent source rescan for each window.

#### Queries and overlap aggregation

- Point lookup enumerates all matching feed names from one pinned generation
  without caller-managed bitmaps or one lookup per catalog feed.
- One membership scan reports exact selected feed cardinalities,
  target-versus-all overlaps, selected pairs, or all pairs.
- Work is proportional to scanned canonical intervals plus requested emitted
  feed/pair contributions; sparse requests are not quadratic in catalog size.
- Results use bounded batched sinks and `Cardinality129` for exact full-space
  IPv6 counts.

#### Provider joins

- Ordered membership-by-direct joins report exact `(feed_name, direct_u32)`
  overlaps plus unmapped coverage without per-range tree lookup.
- Ordered membership-by-membership joins report exact selected/global feed-name
  cross-products plus uncovered coverage.
- Provider labels and JSON schemas remain caller-owned; the SDK never interprets
  a generic direct value as ASN, country, or another label.
- Provider-value joins return analytical statistics only because generic direct
  values have no universal output-conflict rule. Address coverage is published
  as v4 through the set algebra, without a second join-specific output engine.

#### Feed algebra

- Public operations cover union, intersection, exclusion, equality,
  comparison, overlap, and counting over pinned v4 inputs and selected scopes.
- Same-named feeds across inputs are one virtual global feed. K-way ordered
  merging never first materializes that feed.
- Preserved union keeps contributing global names; preserved intersection keeps
  selected contributing names on common coverage; preserved exclusion keeps
  included-side names on surviving coverage. Flat mode writes the same coverage
  to one caller-named feed.
- Set-producing operations publish one final v4 output plus exact statistics.
  Analytical operations create no output.
- Descriptor and memory budgets are explicit. Heap use is bounded by source
  count, selected feeds, requested aggregation output, and reusable batches,
  never by input range count or address cardinality.

#### Performance and architecture

- Each caller source is drained at most once. Each refresh, join, projection,
  aggregation, or algebra operation performs only the sequential traversals
  required by its declared output; hidden comparisons and rereads are defects.
- Release ingestion/scans have no per-record allocation; reader point/cursor
  operations retain zero allocation. Setup and output-proportional allocations
  are measured.
- No hot path performs persistent-content read/write/seek calls, builds a page
  outside file-backed mappings, checksums before commit/output sealing, or
  creates a sorting/materialization scratch database.
- Test-only work counters cover source passes, intervals, page work, membership
  decodes/combinations, join advances, window tests, aggregation cells, mapping
  changes, durability, and publication; they compile out of release.
- One-million-range benchmarks cover every new linear workflow on the pinned
  reference core. The working target is at or near one second. A miss requires
  profiles proving irreducible work and explicit user acceptance before closure.
- Output-dense pair operations report time normalized by scanned ranges and
  emitted cells. Requested quadratic output is visible; catalog-size quadratic
  overhead is rejected.
- Timed-stack profiles classify every dominant cost. Avoidable validation,
  repeated lookup/decode/pass, allocation, page reconstruction, dictionary
  churn, or output copy keeps the SOW open.
- Public adapters own typed scopes, workflows, reports, cancellation, and
  handles. Only internal reader/writer/output authorities inspect mappings,
  roots, pages, raw membership state, allocation, retirement, checksums, or
  records.
- Rust and C reuse one implementation of every operation. No C-only algorithm,
  second merge/output encoder, or high-level physical bypass is added.
- Source size, file/function size, complexity, dead code, dependencies, and
  clones are audited. Growth must map to an acceptance criterion; superseded
  code and redundant adapters are removed.

#### Proof and delivery

- Rust, the Rust-provided C ABI, generated header/manifest, normative specs,
  Rust-first corpus, README, project skill, and benchmarks agree.
- Permanent tests cover empty/unordered/overlap/full-space input, cutoff
  equality, replay, failure, cancellation, no-change, metadata, abort/commit,
  recovery, publication residue, and budget exhaustion.
- Property tests compare all new semantics with independent models.
- Existing architecture, mmap, source, workspace, MSRV, lint, docs, C, ABI,
  Miri, sanitizer, Valgrind, and authorized native gates pass.
- A complete update-ipsets-shaped benchmark covers current-feed construction,
  both timestamp files, central base/history update, overlaps, both provider
  joins, algebra, and final enumeration, with setup/validation outside timers.
- The final SOW-0020-style audit finds no actionable correctness, performance,
  ownership, duplication, resource, portability, documentation, or
  maintainability issue. Any finding triggers repair and full audit restart.
- No Go or signing work starts. Rust acceptance remains an explicit user
  decision after this evidence.

## Analysis

Sources checked:

- `AGENTS.md`
- `.agents/skills/project-v4-rust/SKILL.md`
- `.agents/sow/specs/{design-iprange-engine,binary-format-v4,c-abi-v4}.md`
- `.agents/sow/specs/update-ipsets-v4-adoption-findings.md`
- completed SOW-0020 and superseded pending SOW-0018
- current contract, reader, source, cursor, workflow, ordered merge, immutable
  output, publication, C ABI, benchmark, and work-counter production paths
- `firehol/update-ipsets @ f299ee780dc0ce09a15a46d0ee660611399c2d48`,
  especially `pkg/engine/feed_body_stage.go`, `retention_update.go`, and
  `output_comparison.go`
- official upstream repositories listed under the gate below

Current state:

- Baseline HEAD is `a8ea711019673c99cb3187f722fdb0401c65cdda`;
  only pre-existing untracked build outputs are present.
- `ValueKind` has `Direct` and `Membership`; `ValueTag` has one special
  `RETENTION` constant. `DatabaseInfo` reports kind and raw tag.
- `RetentionRefresh` checks only `RETENTION` and already uses the authoritative
  ordered merge, whose policy observes every exact old/input/output segment.
- `WorkflowReport` has aggregate replacement statistics but no timestamp-
  transition batches.
- Named-feed cursors are allocation-free one-record iterators but do not directly
  satisfy the batched `RangeSource` contract.
- `immutable_output::Builder` owns mapped ordered output, catalog construction,
  membership interning, metadata, and sealing, but is private and ordered-only.
- The C ABI exposes old retention classifiers/functions and no new analytics.
- Current update-ipsets unions retained full snapshots for history windows and
  enumerates feed pairs for overlaps. These are correct but repeat work the v4
  models can avoid.
- SOW-0020 proves isolated hot paths, but its 421-feed replacement baseline has
  only 128 existing ranges with one all-feeds combination. It does not prove a
  real catalog refresh, sparse combinations, history, overlap, or joins.

Risks:

- Timestamp or provenance errors silently corrupt published intelligence.
- Reports outside the ordered merge add passes or duplicate split semantics.
- High-level convenience can expose raw indexes or create file-sized heap state.
- Requested pair/provider output can be large; it must be bounded/streamed and
  distinguished from catalog overhead.
- Direct construction can duplicate encoding/durability if it bypasses existing
  output/publication owners.
- A complete `last_seen` map rewrites current timestamps each observation. This
  is the approved simple semantic. If measurements reject it, work stops for a
  user decision; it does not silently become an absent-only design.
- Cross-file commits are not atomic. The SDK returns identities/outcomes while
  update-ipsets owns its journal; this SOW does not add distributed transactions.
- The unreleased C ABI changes exactly; obsolete aliases are not retained.
- The implementation is already large. New capability must compose existing
  authorities; new physical machinery requires separate authorization.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- Phase 1 proves mapped storage primitives and isolated feed/direct paths, but
  update-ipsets lacks the semantic composition that makes them one publisher
  engine. Otherwise the application must reparse output, manage bitmap details,
  rescan snapshots, perform nested provider lookups, enumerate pair files, or
  reimplement ordered algebra.
- The root cause is missing high-level composition over existing low-level
  mapped authorities, not a missing page structure or storage engine.

Evidence reviewed:

- The analysis sources establish current APIs, consumer loops, normative
  constraints, and Phase-1 performance/architecture evidence.
- `range_merge::OrderedMerge` already owns canonical old/input splitting and
  mapped rebuild through policies; both timestamp variants belong there.
- `immutable_output::Builder` already owns canonical mapped direct/membership
  output; result construction must feed it rather than encode elsewhere.
- `ReaderCore` already exposes selected-generation logical ranges internally;
  higher operations can reuse it without exposing physical state.
- SOW-0020's work counters and timed profiles are proven mechanisms for finding
  extra passes, lookup/decode repetition, page churn, and release leakage.

Affected contracts and surfaces:

- Exact direct semantic identity; Rust reader/writer/workflow/report/source/sink
  APIs; mapped output/publication; errors; Rust-provided C ABI; fixtures;
  validation/recovery reporting; specs; README; skill; benchmarks; and future
  consumer/Go parity without changing update-ipsets or Go in this SOW.

Existing patterns to reuse:

- `ValueTag`, `DatabaseInfo`, typestate workflows, `FinishedWorkflow`,
  `range_mutation`, `range_merge::{OrderedMerge,Policy,MapComparison}`,
  `feed_merge`, `import_merge`, `ReaderCore`, named-feed cursors, `RangeSource`,
  C batch adapters, `immutable_output::Builder`, publication, `Cardinality129`,
  cancellation/budgets/results, explicit recovery, and necessary-work counters.

Risk and blast radius:

- Behavioral: time boundaries, overlap splitting, provenance, same-name
  aggregation, uncovered provider coverage, and balanced statistics.
- Durability: source/sink failure after mutation, publication residue,
  application ordering, and outcome unknown.
- Performance: extra scans, bitmap decode, per-cell hash work, output memory,
  descriptors, callback overhead, and timestamp-wide COW writes.
- Compatibility: intentional removal of unreleased retention surfaces.
- Portability: mappings, callbacks, cardinality, publication and native C.
- Maintainability: adapter growth and physical bypass risk.

Sensitive data handling plan:

- Use synthetic names/ranges/timestamps and public fixtures only. Store no raw
  secret, credential, key, customer/community identity, personal data,
  customer-identifying address, private endpoint, or proprietary evidence.
- Cite sibling/upstream repositories by public identity, commit, relative path,
  and summarized behavior.

Implementation plan:

1. Replace retention identity with the direct semantic enum and exact
   first/last specs, Rust/C types, errors, and identity fixtures.
2. Add two small policies over the existing ordered merge, transition reports,
   failure/property/necessary-work tests, and benchmarks.
3. Add internal batched logical sources and public one-inode immutable feed
   construction over existing output/publication owners.
4. Add one-pass multi-window projection using one direct scan and one advanced
   membership transaction.
5. Add membership-name enumeration, overlap aggregation, and ordered direct and
   membership provider joins.
6. Add scoped global-name k-way algebra, preserved/flat mapped result output,
   analytics, and exact reports.
7. Mirror Rust through the thin C adapter; update specs, corpus, README, skill,
   and consumer-shaped benchmarks.
8. Repeat all correctness, mmap, architecture, source, performance, profile,
   allocation, quality, documentation, and native audits until clean.

Validation plan:

- Focused tests/work counters after each slice; full Cargo/lint/doc/mmap/source/
  ABI/C/MSRV/Miri/sanitizer/Valgrind gates at milestones and final candidate.
- Independent randomized models for IPv4/IPv6 semantics and full-space counts.
- Reopen and explicitly validate each result outside timers.
- Benchmark sparse/dense synthetic and public catalog-shaped data across scales,
  repeated refreshes, growth and reclaim.
- Record five-run pinned medians, allocations/bytes, RSS, descriptors, file
  sizes, residue, work snapshots, and exact timed-stack profiles.
- Search all Rust/C paths for old retention, allocation, page buffers,
  persistent-content I/O, physical high-level access, duplicate merge/output,
  unbounded state, extra passes, and analogous failure errors.
- Use the already authorized native Windows GNU, macOS ARM64, and FreeBSD 14
  systems after pushing an exact candidate; state compilation/runtime boundaries.
- Repeat the final audit from first principles; do not close while a finding
  remains.

Artifact impact plan:

- `AGENTS.md`: update only for a genuinely new project-wide guardrail.
- Runtime project skill: add proven workflow benchmarks/profiles/gates.
- Specs: update all three v4 specs and adoption findings.
- End-user/operator docs: update `v4/rust/README.md`.
- End-user/operator skills: none exist; recheck at close.
- SOW lifecycle: close/move overlapping SOW-0018 as superseded; keep signing
  SOW-0017 pending; keep this the sole current SOW.

Open-source reference evidence:

- `RoaringBitmap/CRoaring @ 95e424b60f4e4d2cb2ae0176976d0d26aa6a3ebe`
  - `include/roaring/roaring.h:217-231`, `288-313`, `861-892`
  - Reuse direct cardinality and bounded-iteration lessons; do not adopt its
    heap result or bitmap format.
- `LMDB/lmdb @ 567292b5d4896d558c7f4fffbf711b86432cc15a`
  - `libraries/liblmdb/lmdb.h:20-29`, `696-708`
  - `libraries/liblmdb/mdb.c:793-824`
  - Reuse one-writer, pinned-reader and generation-reclaim lessons already owned
    below the SDK; adopt no alternate engine.
- `maxmind/MaxMind-DB @ 2f18851d11a77ca981975666b18680979a6759a5`
  - `MaxMind-DB-spec.md:10-31`, `52-87`
  - Reuse the separation of IP lookup from application record meaning; adopt no
    MaxMind layout.

Open decisions:

- None. Any discovery requiring semantic, compatibility, scope, physical-format,
  or performance-acceptance change stops for user authorization.

## Implications And Decisions

1. Implement long-term-best as minimal-complete; do not cut correctness, proof,
   or blast-radius handling to save work.
2. Performance is paramount; hot paths contain no nonessential maintenance,
   validation, checksum, allocation, copy, or pass.
3. Keep direct/membership physical kinds; the SDK owns membership state.
4. Encode well-known direct semantics as canonical tags. Replace unreleased
   `retention` with `first_seen` and `last_seen`; keep generic direct tags.
5. First-seen tracks uninterrupted presence; removal/reappearance starts anew.
6. Last-seen tracks latest observation; one file serves all history windows.
7. Result feeds are v4 files; same names aggregate virtually.
8. Results may preserve provenance or flatten and always include statistics.
9. Unordered input normalizes directly without sorting files.
10. Healthy operation has no implicit validation; best effort stays in recovery.
11. Preserve two API levels and one physical-format authority.
12. Finish and obtain acceptance for Rust before Go; signing stays separate.
13. Do not broaden or drift without explicit user authorization.

## Plan

1. Semantic identity and first/last timestamp workflows.
2. Mapped chaining, immutable construction, and history projection.
3. Membership queries, overlaps, and provider joins.
4. Scoped multi-file algebra and mapped result publication.
5. C parity, specs, corpus, docs, realistic benchmarks, and repeated clean audit.

## Execution Log

### 2026-08-10

- Created the active goal and this consolidated SOW from the approved SDK gap
  analysis.
- Reviewed current Rust APIs/owners/specs, completed SOW-0020 evidence, current
  update-ipsets history/comparison paths, and three upstream references.
- Closed SOW-0018 as superseded because its algebra scope is incorporated here.
  Signing SOW-0017 remains separate.
- No production code changed before this gate became ready.
- Replaced the experimental `retention` identity with the exact `Generic`,
  `FirstSeen`, and `LastSeen` direct semantic contract. The persistent layout is
  unchanged; canonical tags are the existing 16-byte tag field.
- Added first-seen and last-seen full-snapshot refreshes over the one existing
  ordered merge. First-seen optionally emits bounded exact removed intervals;
  last-seen keeps only absent values newer than the cutoff and never moves an
  observed timestamp backwards.
- Added pinned mapped v4 sources, one-inode unordered immutable feed creation,
  one-pass multiwindow history projection, named membership point and overlap
  queries, direct and membership provider joins, and global same-name feed
  algebra with analytical and preserved/flat v4 outputs.
- Kept physical operations behind `ReaderCore`, `WriterCore`/`DraftStore`, and
  `immutable_output::Builder`. Public semantic modules do not inspect mappings,
  roots, pages, allocators, raw membership IDs, bitmaps, records, or checksums.
- Extended the Rust-owned C adapter and regenerated its exact header/manifest.
  The current boundary has 158 functions, 14 opaque handles, and 18 callbacks;
  C calls the Rust operations rather than implementing a second algorithm.
- Regenerated the Rust-first conformance corpus with `first_seen`; removed the
  obsolete retention fixture. Updated all affected v4 specs, adoption findings,
  README, benchmark matrix, architecture checks, and project skill.
- A bounded-resource audit found two allocation-before-proof defects: Rust
  algebra source retention and C selected-pair decoding. Both now prove source,
  heap, and operation reservations before allocating; permanent failure tests
  cover the zero/insufficient-budget cases.
- The runtime mmap proof exposed a shell-only false failure: under `pipefail`, a
  successful early second-stage match could make the first `rg` exit on
  `SIGPIPE`. The proof now uses one `awk` process. Ten consecutive repetitions
  pass and the traced SDK paths contain no persistent-content transfer call.
- Committed and pushed the complete implementation as
  `788c137052c1cbb012ec2e7e017e97c3a2c49b09`.
- Native FreeBSD execution then found one test-portability defect: three new C
  SDK tests unconditionally constructed live databases even though the
  documented FreeBSD boundary deliberately rejects live coordination. The
  production SDK returned `LiveCoordinationUnsupported` before mutation, as
  required; the tests had the wrong platform assumption.
- Reviewed every new C SDK test for the same assumption. The repair excludes
  only those three live-dependent cases on FreeBSD, keeps the supported
  immutable C workflow active there, and keeps the permanent C test that proves
  live creation returns error code 44 without creating database or sidecar
  artifacts. Linux, Windows, and macOS continue to run all three live tests.
- Committed and pushed the repair as
  `1fa819acab95d55eb31f577cf2c22c196d09ea55`, then restarted the complete local,
  native, hosted, architecture, mmap, source, and final-audit gates on that exact
  implementation revision.

## Validation

Acceptance criteria evidence:

- Direct semantic identity is defined at
  `v4/rust/iprange-livedb/src/contract.rs:79-148` and exported in the generated
  C header at `v4/rust/iprange-capi/include/iprange_v4.h:397-412`.
- Public timestamp entry points are at
  `v4/rust/iprange-livedb/src/live_writer/direct_workflow.rs:54-103`; their one
  physical merge owner is
  `v4/rust/iprange-livedb/src/draft_store/timestamp_refresh.rs`.
- Public immutable construction is at
  `v4/rust/iprange-livedb/src/immutable_feed.rs:69-211`; unordered normalization
  and final-inode construction are owned by
  `v4/rust/iprange-livedb/src/immutable_output/unordered.rs`.
- History projection enters at
  `v4/rust/iprange-livedb/src/live_writer/history_projection.rs:55-118` and
  uses one source cursor and one membership draft.
- Named queries, joins, and algebra enter at
  `v4/rust/iprange-livedb/src/membership_query.rs:79-231`,
  `v4/rust/iprange-livedb/src/membership_query/join.rs:108-129`, and
  `v4/rust/iprange-livedb/src/membership_query/algebra.rs:156-267`.
- Architecture and source gates prove that those semantic adapters cannot
  bypass the private mapped-format owners.

Tests or equivalent validation:

- Both complete workspace matrices pass: all features and no default features,
  all targets. The engine library has 379 active passing tests and four explicit
  subprocess/probe entry points; the C library has 19 passing unit tests. Every
  integration, property, conformance, crash, recovery, publication, native C,
  and benchmark target passes.
- Formatting, warnings-denied Clippy and rustdoc, architecture, static mmap,
  runtime syscall mmap, and the 428-source/four-target compiler graph pass.
- The complete Rust 1.74.1 workspace matrix passes. Ten authoritative codec
  vectors pass under big-endian s390x Miri.
- AddressSanitizer with leak detection passes all 379 active engine tests and
  all 19 C-boundary tests with the exact instrumented adjacent worker.
- Valgrind 3.25.1 with its matching preserved glibc loader/debug symbols reports
  zero memory errors and zero definite/indirect leaks for the current raw-fork
  ownership test. All four worker-free native C SDK programs pass under the
  same error-exit gate.
- The release benchmark binary contains none of the 49 necessary-work field
  strings and no `iprange_livedb::work` symbol or call.
- Both complete native feature matrices pass on exact implementation commit
  `1fa819acab95d55eb31f577cf2c22c196d09ea55` on Windows GNU, macOS ARM64, and
  FreeBSD 14. The same matrices pass locally on Linux. FreeBSD runs 297 active
  engine tests with one explicit probe ignored and proves immutable Rust/C
  operation plus clean rejection of every unsupported live entry.
- All five hosted workflows for that exact pushed commit pass: push matrix,
  big-endian, security, Scorecard, and publication.

Real-use evidence:

- The complete million-range publisher-shaped workflow constructs the current
  feed, refreshes first-seen and last-seen, projects seven history windows,
  performs named overlaps, both provider joins and algebra publication, and
  enumerates the result. The final scale run processes 13.6 million declared
  work units and emits 3,201,805 units in 1.506 seconds, with 250 setup/output-
  proportional allocations, 2,446,063 allocated bytes, four descriptors before
  and after, and no private residue.
- Final one-million scale results are 0.355 seconds direct replacement, 0.382
  seconds first-seen, 0.463 seconds last-seen, 0.388 seconds unordered immutable
  creation, 0.075 seconds seven-window projection, and 0.674 seconds for one
  million point checks emitting four million matching names.
- Million-range analytical results are 0.030 seconds selected cardinalities,
  0.042 selected-pair overlap, 0.046 all-pair overlap, 0.055 direct-provider
  join, 0.060 membership-provider join, 0.109 algebra count, 0.120 comparison,
  and 0.182/0.183 seconds preserved/flat v4 publication.
- Five-run medians and frame-pointer profiles independently cover immutable
  construction, both timestamps, history, matching names, analytics, joins,
  algebra outputs, and the complete workflow. Dominant stacks are required
  tree/cursor/normalization/output/SHA-512 work; no profile contains implicit
  validation, pre-commit checksum, per-record allocation, page reconstruction,
  persistent-content I/O, temporary sorting, or an extra source/comparison pass.

Reviewer findings:

- No subagent or external reviewer will be used, per user instruction. The final
  audit is local and first-principles.
- The local audit found and repaired the two allocation-before-budget defects,
  the mmap proof-script defect, and the FreeBSD test-platform assumption
  recorded above. The audit was restarted after each repair; none remains in
  the final pass.

Same-failure scan:

- Every new Rust/C vector allocation was reviewed against its exact heap owner.
  Rust algebra and C pair decoding now fail before allocation when the declared
  reservation is insufficient. Fixed-capacity caches allocate through `Heap`.
- Searches find no obsolete retention format/API reference in Rust,
  conformance, normative specs, or the runtime skill; remaining prose uses the
  update-ipsets product concept or mapped-tail retention in its ordinary sense.
- Searches and the static/runtime mmap gates find no persistent read/write/seek
  API, complete owned page, hot-path checksum, or implicit whole-file validation.
- Exact-clone review inspected all 17 strict 15-line/100-token shapes. None owns
  persistent layout, traversal, mutation, allocation, retirement, sealing, or
  page construction.
- Every new C SDK test was checked for a hidden live-coordination assumption.
  The three live-only cases are now platform-qualified; the immutable workflow
  and explicit unsupported-boundary proof remain active on FreeBSD.

Local architecture and source-quality audit:

- Production-only inventory: 313 files and 88,712 newline-counted lines. Lizard
  parses 80,518 code lines in 4,795 functions; average function length is 13.9
  code lines and average cyclomatic complexity is 3.46.
- Forty-eight files exceed the directional 500-line target; the largest is 859
  lines and none reaches 1,000. Fifty-six functions exceed 60 code lines, five
  exceed 100, and the largest is the existing cohesive 191-line recovery
  attempt. There are 249 functions above complexity 9, 62 above 15, and 14 above
  20; manual review found state/codec decisions, not duplicate physical owners.
- Strict exact-clone detection finds 17 shapes, 328 lines, 2,676 tokens, and
  0.37% duplication. They are frozen C report forms, maintenance/typestate
  wrappers, source adapters, output error plumbing, lifecycle wrappers,
  publication setup, and distinct direct/membership recovery policies.
- Mutually exclusive responsibility accounting is: mapped format engine and
  utilities 24,432 lines; lifecycle/coordination/publication 18,895; explicit
  validation/recovery worker 20,643; public semantic workflows/adapters 11,749;
  and the Rust-owned C ABI 12,993. The public semantic portion further divides
  into 4,875 query/join/algebra lines, 3,797 live-writer adapters, 1,229 C
  semantic support lines, 674 snapshot lines, 492 reader/source lines, 425
  shared workflow/report lines, 213 immutable-feed lines, and 44 history types.
- This breakdown explains the size; it does not make the size desirable. The
  architecture gate plus manual import/owner review found no high-level mapping,
  codec, allocator, tree, retirement, checksum, or page-construction authority
  and no removable duplicate engine. Reducing materially further would require
  dropping an approved capability or weakening platform/fault/durability proof,
  neither of which this SOW authorizes.

Sensitive data gate:

- This SOW contains public repository identities/commits/paths, synthetic
  semantics, and generic platforms only; no sensitive data is present.

Artifact maintenance gate:

- `AGENTS.md`: unchanged; its Rust-first, two-level, mmap-only, explicit-
  validation, simplicity, and performance rules already cover this work.
- Runtime project skill: updated with the current two-level ownership model,
  158-symbol C boundary, workflow matrix, and profiling obligations.
- Specs: binary format, engine design, C ABI, and update-ipsets adoption findings
  updated to current implemented semantics.
- End-user/operator docs: Rust README updated with APIs, architecture, resource
  rules, current performance, and honest source-quality inventory.
- End-user/operator skills: none exist, so no consumer skill can be stale.
- SOW lifecycle: SOW-0021 is sole current; SOW-0018 is superseded; SOW-0017
  remains pending.

Specs update:

- Completed for all three normative specs and the adoption findings. No second
  physical format, page, root, journal, or signing contract was added.

Project skills update:

- Completed in `.agents/skills/project-v4-rust/SKILL.md`.

End-user/operator docs update:

- Completed in `v4/rust/README.md`; native platform wording will be rechecked
  against the exact pushed candidate before closure.

End-user/operator skills update:

- None exist; repository index and filesystem search agree.

Lessons:

- One physical merge and one mapped output builder were sufficient for all new
  semantics. Performance came from composing ordered cursors, not from adding a
  cache, page type, temporary database, or alternate engine.
- Budget correctness includes allocations performed by language adapters before
  entering the engine. An operation-level reservation must precede those
  allocations as well as the engine's own workspace.
- A proof script can be wrong while the implementation is correct. Pipeline
  exit semantics are part of the validation surface and must be made
  deterministic.
- The implementation remains much larger than the directional 5,000-line goal.
  The measured production inventory is 313 files and 88,712 physical lines.
  The local audit found no duplicate physical authority to remove, but size is
  reported as a continuing maintenance cost rather than described as ideal.

Follow-up mapping:

- Go remains blocked on explicit Rust acceptance and requires a separate SOW.
- Signing remains tracked by SOW-0017.
- Every former SOW-0018 item is implemented or rejected with evidence here.

## Final Audit After Native Repair - 2026-08-10

### TL;DR

The repeated audit of exact implementation commit
`1fa819acab95d55eb31f577cf2c22c196d09ea55` is clean. The required
update-ipsets workflows use the existing mapped engine and mapped output owner,
all individual million-range linear operations remain below one second on the
pinned reference core, and the complete 13.6-million-work-unit publisher
scenario completes in 1.506 seconds.

The first native pass found a real FreeBSD test-platform defect. Production
correctly rejected unsupported live coordination; three new C tests assumed it
was available. That test defect was repaired, the full audit restarted, and
both complete feature matrices now pass on Linux, Windows GNU, macOS ARM64, and
FreeBSD 14.

### Verdict

Pass. No actionable correctness, performance, ownership, duplication,
resource, portability, documentation, or maintainability finding remains in
the approved SOW scope.

This is a measured result for the implemented workloads and supported systems,
not proof that future workloads cannot expose another defect. Rust acceptance
and authorization to start Go remain user decisions.

### Current performance

Final release measurements pinned to one Intel i9-12900K performance core are:

| Million-range operation | Elapsed |
| --- | ---: |
| direct replacement | 0.355 s |
| first-seen refresh | 0.382 s |
| last-seen refresh | 0.463 s |
| nested arrival-order overwrite | 0.507 s |
| unordered immutable feed construction | 0.388 s |
| seven-window history projection | 0.075 s |
| one million named membership checks, four million names emitted | 0.674 s |
| selected cardinalities | 0.030 s |
| selected-pair / all-pair overlap | 0.042 / 0.046 s |
| direct / membership provider join | 0.055 / 0.060 s |
| algebra count / comparison | 0.109 / 0.120 s |
| preserved / flat v4 algebra publication | 0.182 / 0.183 s |
| complete update-ipsets-shaped workflow | 1.506 s |

One-million-range feed construction, including normal commit, measures:

| Input order | First feed | Second feed | Final ranges |
| --- | ---: | ---: | ---: |
| ascending disjoint | 0.151 s | 0.143 s | 1,000,000 |
| descending disjoint | 0.152 s | 0.158 s | 1,000,000 |
| deterministic random disjoint | 0.414 s | 0.455 s | 1,000,000 |
| deterministic random overlap chain | 0.299 s | 0.320 s | 1 |

The 66-case scale matrix passes with exact result enumeration and explicit
post-timing validation. The complete workflow makes 250 setup/output-
proportional allocations totaling 2,446,063 bytes, keeps four descriptors
before and after, and leaves no private residue. The FreeBSD repair changed
test selection only; production source and these timed paths are unchanged.

### Ranked findings

None.

### The two-level architecture

- Public semantic adapters own typed workflows, names/scopes, cancellation,
  reports, commit/abort sequencing, Rust handles, and frozen C translation.
  They do not inspect mappings, roots, pages, raw membership IDs, bitmaps,
  allocator state, retirement, records, or checksums.
- `ReaderCore::read()` is the one healthy selected-generation read authority.
  `WriterCore` over `DraftStore` is the one healthy mapped mutation authority.
  `immutable_output::Builder` is the one canonical mapped result-construction
  authority.
- Timestamp refresh, feed refresh, history, queries, joins, and algebra compose
  those private authorities through logical cursors and ordered sources. The C
  layer translates ownership and values; it implements no second algorithm.
- The architecture gate and four-target source graph cover every production
  source and reject high-level physical access or an unwired implementation.

### Physical-format authority

No persistent page, root, record, index, journal, or alternate format was
added. `first_seen` and `last_seen` use the existing canonical 16-byte value
tag. Existing byte codecs, mapped access, fixed-tree traversal/mutation, COW
allocation, transaction-grouped retirement, checksum sealing, and generation
publication each retain one owner.

No complete database page exists outside a file-backed mapping. Static review
of 321 production files and the syscall-traced runtime path find no persistent-
content read/write/seek operation. Validation and recovery reuse canonical
codecs and mapped output while remaining isolated from healthy open/read/write.

### Where the production lines are

The reproduced production-only inventory contains 313 files and 88,712
newline-counted lines. Lizard parses 80,518 code lines in 4,795 functions, with
average function length 13.9 code lines and average cyclomatic complexity 3.46.
Forty-eight files exceed the directional 500-line target; the largest is 859
lines and none reaches 1,000. Fifty-six functions exceed 60 code lines, five
exceed 100, and the largest is the existing cohesive 191-line recovery state
machine.

Strict exact-clone detection at 15 lines/100 tokens reports 17 shapes totaling
328 lines, or 0.37%. Manual inspection classifies them as frozen C report forms,
maintenance/typestate wrappers, source adapters, output error plumbing,
lifecycle/publication setup, and distinct direct/membership damaged-input
policies. None duplicates persistent layout, traversal, mutation, allocation,
retirement, sealing, or page construction.

Mutually exclusive responsibility accounting is 24,432 lines of mapped engine
and utilities, 18,895 lifecycle/coordination/publication, 20,643 explicit
validation/recovery, 11,749 public semantic workflows/adapters, and 12,993
Rust-owned C ABI. The implementation remains far above the directional
5,000-line goal. This audit does not claim that a metric proves every line is
necessary; it establishes that no concrete duplicate physical owner, dead
implementation, or safe removable mechanism was found and keeps size visible
as a maintenance cost.

### First-seen and last-seen

- `first_seen` preserves the timestamp of continuously present old coverage,
  assigns the refresh time only to new coverage, and removes absent coverage.
- `last_seen` assigns current coverage the greater of its old value and refresh
  time, retains absent coverage only while it is newer than the cutoff, and
  serves every configured history window from one file and one scan.
- Both are policies over the one ordered old/input merge. Exact transition
  reports and optional first-seen removal batches are produced during that
  required merge, without a second comparison pass.
- Generic direct tags remain opaque. The obsolete experimental `retention` tag
  and API are not retained as compatibility.

### Recovery

Recovery is unchanged: explicit, isolated, budgeted, and best-effort-capable.
Healthy open/read/write performs no implicit validation. Damaged mapped input is
handled by the version-matched worker and canonical codecs; recovered output is
built through the mapped output authority. This SOW adds no recovery format,
healthy-path fallback, sorting file, page buffer, or content-I/O path.

### Implementation result

- Added exact first-seen and last-seen full-snapshot refreshes and reports.
- Added direct mapped v4-to-v4 sources and one-inode unordered immutable feed
  construction without text, sorting files, or intermediate databases.
- Added one-scan multiwindow history projection, named membership lookup and
  aggregation, ordered provider joins, and global same-name feed algebra with
  analytical and preserved/flat v4 results.
- Added the same surface through the Rust-provided C ABI: 158 exact functions,
  14 opaque handles, and 18 callbacks, all backed by the Rust implementation.
- Test-only necessary-work counters prove bounded passes, tree/page work,
  decodes, combinations, joins, mapping changes, durability, and publication.
  Release inspection proves all counter state, symbols, calls, and 49 field
  strings compile out.
- Timed-stack profiles show required cursor/tree/normalization/mapped-output/
  SHA-512 work. They contain no implicit validation, pre-commit checksum,
  per-record allocation, page reconstruction, repeated source/comparison pass,
  content I/O, or temporary materialization.

### Acceptance gates

- Both complete local workspace matrices, all targets, with all features and
  with no default features: pass. Linux has 379 active engine tests plus every
  integration, property, conformance, crash, recovery, publication, native C,
  and benchmark target; four explicit probe entry points remain ignored.
- Formatting, warnings-denied Clippy and rustdoc, diff hygiene, architecture,
  static mmap, syscall-traced mmap runtime, and the 428-source/four-target
  compiler graph: pass after the final repair.
- Rust 1.74.1 complete workspace matrix, ten s390x Miri codec vectors,
  AddressSanitizer leak detection, and Valgrind/C caller gates: pass.
- The 66-case scale matrix, five-run medians, exact timed-stack profiles,
  allocations, descriptors, residue, result enumeration, and release-counter
  absence: pass.
- Both complete native feature matrices pass on exact implementation commit
  `1fa819acab95d55eb31f577cf2c22c196d09ea55` on Windows GNU, macOS ARM64, and
  FreeBSD 14. Every clone reports that revision; FreeBSD proves its documented
  immutable-supported/live-unsupported boundary.
- All five hosted workflows for that exact pushed commit pass: push matrix,
  big-endian, security, Scorecard, and publication.
- Same-failure searches, source/complexity/clone review, sensitive-data gate,
  artifact maintenance, follow-up mapping, and the repeated physical-authority
  audit: pass with no open finding.

## Outcome

Implementation, proof, and the repeated audit are complete. Rust acceptance and
any Go work remain separate user decisions.

## Lessons Extracted

Captured in the updated runtime project skill: operation-level budget preflight,
complete publisher-shaped benchmark/profile coverage, exact two-level ownership,
and deterministic mmap runtime proof.

## Followup

- Pending SOW-0017 remains the only signing tracker.
- Go requires separate authorization after Rust acceptance.

## Regression Log

None yet.
