# SOW-0025 - Pure-Go Exact v4 Semantic Port

## Status

Status: in-progress

Sub-state: milestone 1 implemented (portable mmap-only immutable reader with
corpus cross-open); six-agent review rounds 1-3 complete with all P0-P2
findings fixed and regression-pinned (see execution log and milestone report
sections 11c-11d); borrow-count view lifetime implemented; the closed-state
error-class choice (HandleClosed vs WrongState), the deletion set, and the
worker boundary remain pending user decisions; no tracked deletion applied
yet.

## Requirements

### Purpose

Deliver the pure-Go peer of the accepted Rust v4 SDK so `update-ipsets` and Go
consumers can use the exact current unsigned Phase-1 database without cgo or a
Rust runtime dependency. The result must preserve the format's mmap-only,
bounded, durable, two-level architecture and its measured performance
discipline rather than mechanically translating Rust source.

Make the work safe to hand to an independently run implementer. Evidence at
each milestone must let the user judge whether the implementation is accurate,
focused, maintainable, and worth continuing before a large rewrite is accepted.

### User Request

- Commit and push the existing work so the repository starts clean.
- Create a self-contained SOW for porting the accepted Rust implementation to
  pure Go.
- Keep the implementation minimal-complete, thin, clear, and performance-first.
- Use the implementation and its evidence as a controlled evaluation of an
  independently run implementer; the user will decide whether and how to
  continue from the milestone results.

### Assistant Understanding

Facts:

- The Rust engine is complete for the current approved Phase-1 contract and has
  passed correctness, durability, resource, performance, conformance, C-boundary,
  and native supported-platform gates. Its final behavior and measurements are
  summarized in `v4/rust/README.md` and completed SOWs 0020-0024.
- The normative authorities are
  `.agents/sow/specs/binary-format-v4.md` and
  `.agents/sow/specs/design-iprange-engine.md`. Rust is implementation evidence;
  its physical page placement and internal types are not a second specification.
- The current Go module is an older unreleased experiment. It has 50 production
  Go files and 44,088 newline-counted production lines, but its public package
  exposes only basic types, cardinality, and error declarations.
- The old Go wire model permits only direct and membership values, keeps the old
  `retention` tag and 1 MiB metadata limit, lacks all structured-value meta state,
  and does not open the current shared corpus
  (`v4/go/internal/exactv4/contract.go:11-18,63-73,98-101,135-162` and
  `v4/go/types.go:13-34`).
- The old Go storage layer performs positional content reads and writes and owns
  complete 4 KiB page arrays outside file mappings
  (`v4/go/internal/exactv4/page_source_linux.go:13-49`,
  `v4/go/internal/exactv4/page_source_other.go:12-54`,
  `v4/go/internal/exactv4/os_linux.go:495-577`, and
  `v4/go/internal/exactv4/private_page_pool.go:92-103`). These are prohibited by
  the current format contract.
- The old Go tests, vet, and formatting pass. This proves those old internal
  fragments are internally consistent; it does not prove current format or SDK
  parity. No Go test references `v4/conformance/cases.json` or opens a current
  Rust-produced fixture.
- The Rust-provided C ABI remains the only C SDK. Porting or regenerating that ABI
  in Go is outside this SOW.
- Snapshot signing remains Phase 2 under pending SOW-0017 and is outside this
  SOW.

Inferences:

- Treating the current Go tree as a nearly finished implementation would preserve
  the architecture that Rust deliberately replaced. The safe approach is a
  semantic port from the current spec and accepted Rust behavior, reusing an old
  Go component only after current tests prove it conforms.
- A literal Rust-to-Go line translation would copy language-specific ownership
  machinery and the Rust implementation's size. The Go design should reproduce
  each invariant and observable operation once using idiomatic Go and a smaller
  module graph.
- The isolated physical-fault worker, Windows namespace/security behavior, and
  mixed-language live coordination are the highest feasibility and portability
  risks. They must be proved early, not left to final integration.

Unknowns:

- The exact subset of old Go tests or codecs worth retaining. Milestone 0 must
  classify each current production file as retain, rewrite, or delete before any
  destructive edit.
- The final Go/Rust timing delta. The accepted 5-10% band is a target where the
  runtimes permit it; only matched measurement can establish justified
  exceptions.
- Whether the pure-Go fault worker can satisfy every POSIX signal-chaining rule
  without project-owned assembly or another new native boundary. The implementer
  must prove the existing no-cgo contract. If a new boundary appears necessary,
  implementation stops for a user design decision before adding it.

### Acceptance Criteria

- The final module is a pure-Go implementation with no cgo, Rust library,
  external database engine, or alternate content path.
- It supports only the exact current v4 identity. No v3 code, old-v4 parser,
  compatibility mode, importer, exporter, or obsolete fixture remains.
- Public Go behavior covers the complete Phase-1 semantic surface listed in
  `v4/rust/iprange-livedb/src/lib.rs` and the design spec, except the explicitly
  Rust-provided C ABI. Idiomatic Go names are allowed; missing semantics are not.
- The public SDK exposes logical advanced direct, membership, and structured
  transactions plus the typed high-level workflows. It never exposes page
  numbers, roots, raw feed indexes as mutation authority, membership IDs,
  structure IDs, bitmap combinations, or allocator state.
- One internal reader core owns healthy selected-generation access. One internal
  writer core owns healthy mapped COW mutation, allocation, retirement, page
  sealing, and committed-generation publication. Higher-level APIs only sequence
  logical operations over those owners.
- Validation/recovery, external reader coordination, and filesystem
  namespace/publication retain their separate narrow owners while reusing the
  canonical codecs and mapped output builders. No second physical tree or
  publication implementation exists in a workflow.
- Every persistent artifact is content-accessed only through file-backed
  mappings. Static and runtime gates reject `Read`, `Write`, `ReadAt`, `WriteAt`,
  `Pread`, `Pwrite`, `ReadFile`, `WriteFile`, buffered/stream content transfer,
  and equivalent Windows calls against SDK artifacts.
- No complete database page exists in a Go heap/stack array, cache, anonymous
  mapping, or copied byte slice. Page creation and COW edits occur at final
  offsets in file-backed mappings; mapped-to-mapped cell copies are allowed.
- Open, lookup, scan, mutation, commit, query, and snapshot do not implicitly
  run full validation. Explicit validation and recovery alone perform the full
  graph and checksum work.
- Direct, membership, and structured files implement the exact current meta,
  page, dictionary, metadata, retirement, sidecar, reservation, and publication
  contracts. `NetworkEnrichmentV1` uses one common structured manager plus its
  independent codec, including lazy threat membership.
- The opaque compressed metadata payload preserves exact caller bytes, absent
  versus empty state, read-your-writes, and the 20 MiB uncompressed limit.
- Generic direct assignments and structured range assignments apply every input
  in arrival order. A later range overwrites only its covered addresses.
  Unordered ingestion normalizes directly in the destination and creates no
  sorting file or complete-feed heap image.
- Named feeds, feed-index reuse, membership interning, structured interning,
  typed references, stale/foreign reference rejection, and deletion/rename
  behavior match the current contract. Callers never construct internal ID
  combinations.
- Exact direct replacement, first-seen refresh, last-seen refresh, named-feed
  create/replace/rename/delete, membership import, one-inode immutable feed
  construction, history projection, queries, provider joins, global named-feed
  algebra, compact snapshot, commit resolution, and lifecycle/publication
  resolvers match Rust semantics and reports.
- Operation failure aborts the private draft. No later commit can publish partial
  work after a failed source or sink. Commit, abort, close, cleanup, and
  outcome-unknown results retain exact evidence and retry obligations.
- Live reader registration, writer exclusion, reader-safe transaction-grouped
  reclamation, lowest-free-page allocation, sidecar identity/replacement, and
  forked-handle rejection match the exact contract.
- Linux, macOS, and Windows implement the supported live contract. FreeBSD 14
  implements immutable reading, offline validation/recovery, and durable
  immutable publication while every live entry rejects before path access or
  mutation.
- The Go distribution supplies its own exact version-matched
  `iprange-v4-worker`. It claims only SDK-owned in-region physical mapping faults,
  chains every unrelated POSIX `SIGBUS` disposition exactly, uses the equivalent
  Windows exception rule, and never mislabels an unrelated worker crash as source
  unreadability.
- Go independently produces all required conformance fixtures. Both readers
  actually open and semantically verify every Go- and Rust-produced fixture,
  including structured values, full IPv6 cardinality, and exact metadata bytes.
- Mixed Rust/Go subprocess tests pass in both directions for reader slots, writer
  exclusion, reclamation, stale-slot cleanup, sidecar replacement, transition
  states, reservations, publication inspection, and resolution.
- Warm successful point lookups and cursor steps allocate zero Go heap bytes.
  Writer allocations and heap use are fixed or bounded by declared budgets, not
  proportional to input records or sparse page numbers.
- Test-only necessary-work counters pin tree descents, pages visited/copied,
  range passes/splits/merges, dictionary work, checksum sealing, synchronization,
  and artifact creation. They compile out of production binaries.
- Matched release benchmarks cover the accepted Rust matrix and representative
  update-ipsets data. Each retained dominant cost maps to required format work;
  unexplained wasted work is fixed and the audit repeated.
- Operation-by-operation Go performance targets the accepted Rust result within
  5-10% where runtime behavior permits. Every material exception has matched
  profiles, hardware/runtime evidence, and a documented cause. CI uses a loose
  disaster threshold only after local performance acceptance.
- Current Go tests, race/checkptr/fuzz/property tests, malformed corpus tests,
  crash/fault tests, resource tests, cross-compilation, and authorized native
  platform tests pass. Skips cannot hide a required platform or producer.
- The final production graph has no unreachable source, broad dead-code
  suppression, duplicate physical authority, or test-only production mechanism.
  Production LOC, file sizes, function sizes/complexity, and exact-clone evidence
  are reported honestly against the directional engineering philosophy.
- Every valid finding in the final first-principles audit is repaired and the
  identical audit is repeated. This SOW cannot complete while an actionable
  correctness, durability, coordination, bounded-resource, performance,
  layering, duplication, portability, documentation, or conformance issue
  remains.

## Analysis

Sources checked:

- `AGENTS.md`, especially the v4 engineering philosophy and Rust-first gate.
- `.agents/skills/project-v4-rust/SKILL.md` for the proven Rust verification and
  portability workflow that the Go peer must reproduce semantically.
- `.agents/sow/specs/design-iprange-engine.md:15-50,57-129,131-169,366-449,451-519`.
- `.agents/sow/specs/binary-format-v4.md`, complete current contract, especially
  sections 3-5, 8-15, 16, 18-21.
- `v4/rust/README.md` and `v4/rust/iprange-livedb/src/lib.rs` for the accepted
  public semantic inventory and measured baseline.
- Completed SOWs 0019-0024 for the mmap-only correction, final authority/hot-path
  audit, update-ipsets workflows, performance proof, structured values, and
  randomized structured correctness.
- `v4/conformance/README.md`, `v4/conformance/cases.json`, and all five current
  Rust-produced fixtures.
- Complete current Go production/test inventory, `v4/go/go.mod`, public files,
  current format constants, storage/page sources, OS code, and test references.
- Baseline commands: `go test ./...`, `go vet ./...`, and `gofmt -l .`; all pass
  before porting.
- Official Go `runtime`, `unsafe`, `golang.org/x/sys/unix`, and
  `golang.org/x/sys/windows` documentation; Microsoft file-mapping documentation;
  and the platform syscall references already normative in the format spec.
- `etcd-io/bbolt @ 01f7d9658a8a` as a limited Go platform-wrapper reference:
  `bolt_unix.go:55-83`, `bolt_windows.go:65-119`, and `db.go:454-570`.

Current state:

- Git started this planning slice clean and synchronized after commit `900b345`.
- The Rust peer and Rust-produced corpus exist. The Go peer, Go-produced corpus,
  cross-open proof, and mixed-language coordination proof do not.
- The current Go tree is large but not product-shaped: 44,088 production lines,
  1,609 production function declarations, several files above 1,000 lines, and a
  4,794-line largest file. Twenty-seven production files contain positional
  content access and/or complete page arrays.
- The current Go public package has no reader, writer, workflow, query,
  validation, recovery, snapshot, publication, or live lifecycle constructor.
- `v4/go/go.mod` currently declares Go 1.23 and `golang.org/x/sys v0.35.0`.
  Preserve that support floor unless a required API proves it impossible; a
  toolchain/dependency-floor change requires evidence and user approval.

Risks:

- Preserving the old Go storage architecture would reproduce the already-fixed
  positional-I/O/page-buffer failure and invalidate performance/resource claims.
- Translating Rust module-for-module would likely reproduce its language-specific
  size and obscure Go's simpler ownership model.
- Porting only the happy-path reader/writer would miss lifecycle, outcome,
  cleanup, recovery, and cross-process contracts that prevent data loss.
- A Go mmap slice remains valid only while its retained mapping/handle lifetime is
  valid. Escaping page slices or racing `Close` can create stale mapped access.
- Go runtime signal ownership makes the exact isolated-worker contract a
  high-risk area. No runtime-internal linkname, swallowed signal, cgo fallback,
  or Rust-worker dependency is acceptable.
- OS-specific namespace, security, locking, flush, shrink, and crash behavior
  cannot be established by cross-compilation alone.
- Tight timing gates before local optimization would create noise. Loose CI gates
  before local proof would hide waste. Local matched profiles come first.
- Deleting the old tracked Go tree without an exact inventory could remove useful
  tests or independently correct codecs. No tracked file is deleted until the
  user approves the exact proposed deletion set.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The repository needs a pure-Go peer, but its current Go tree implements an
  obsolete, incomplete, positional-I/O architecture. The accepted Rust reset
  changed both the storage architecture and the public semantic surface. A port
  must therefore follow the current specs and observable Rust behavior, not
  continue the old Go experiment or transliterate Rust internals.

Evidence reviewed:

- The full normative architecture and binary-format specifications.
- The complete accepted Rust public export inventory, README evidence, current
  corpus, and SOW 0019-0024 outcomes.
- Every current Go production file name, production/test counts, LOC, public API,
  wire constants, platform build tags, content-transfer calls, complete-page
  arrays, conformance references, and baseline tests/vet/formatting.
- Official current Go/OS mapping and unsafe-memory references plus the limited
  bbolt platform-wrapper evidence listed above.

Affected contracts and surfaces:

- `v4/go/` public SDK, internal physical format implementation, OS platform
  layers, fault worker binary, tests, benchmarks, source/runtime architecture
  gates, module metadata, README, and package documentation.
- `v4/conformance/` manifest, Go-produced fixtures, cross-read tests, malformed
  transformations, and mixed Rust/Go subprocess harnesses.
- Rust test/harness code only where minimal cross-language orchestration is
  needed. Rust production behavior, bytes, public API, and C ABI are frozen.
- Project instructions and a new Go runtime project skill after the workflow is
  proven.

Existing patterns to reuse:

- Rust's authority boundaries, semantic tests/models, public workflow reports,
  conformance generator method, necessary-work accounting, update-ipsets
  benchmark scenarios, source/mmap gates, native platform matrix, and final
  audit structure.
- Shared language-neutral `cases.json` and the current Rust-produced files as the
  first immutable-reader target.
- Current Go scalar key/cardinality code and tests only if literal vectors prove
  exact current behavior.
- Small OS-specific mapping wrappers as a pattern. Do not copy bbolt's format,
  freelist, remapping, global locking, page objects, or durability model.

Risk and blast radius:

- This is a replacement of an unreleased Go experiment and eventually affects
  nearly all `v4/go/` production code and tests.
- Data-loss risk is concentrated in COW ownership, dirty-page sealing, durability
  ordering, abort, cleanup, and outcome resolution.
- Memory-safety/process-crash risk is concentrated in mapped lifetime, checked
  offsets, concurrent reuse, physical faults, and signal/exception chaining.
- Interoperability risk is concentrated in little-endian codecs, canonical
  validation, structure/membership dictionaries, metadata compression, sidecar
  locks, and publication/resolver records.
- Performance risk is hidden copying, validation, allocation, synchronization,
  hashing, lookup, and maintenance in hot paths.

Sensitive data handling plan:

- Use only synthetic addresses, feeds, metadata, paths, fault cases, and public
  aggregate benchmark facts in durable artifacts.
- Authorized update-ipsets replay may use operational files locally, but durable
  evidence records only sanitized aggregate sizes, counts, timings, and shapes.
- Do not record credentials, source filenames, literal operational ranges,
  personal/customer/community data, private endpoints, or host aliases.

Implementation plan:

1. Produce the Milestone 0 parity/gap map before production edits: every public
   Rust semantic export and every normative operation maps to its Go API/module,
   test, and status; every old Go production file is classified retain/rewrite/
   delete; the exact proposed deletion set is presented for user approval.
2. Prove the platform foundation in the smallest permanent form: retained
   file-backed mappings, checked offset/view lifetime, flush/sync, identity,
   locks, growth/shrink behavior, and isolated fault-worker feasibility. Delete
   any exploratory code not converted into the final owner or a permanent test.
3. Implement the current portable codecs and one mmap-only immutable reader that
   cross-opens and semantically verifies all Rust-produced fixtures, including
   structured data. This is the first implementation-quality checkpoint for the
   user.
4. Implement one mmap-only physical writer core: final-offset page construction,
   COW range/tree edits, free/used bitmaps, retirement, checksum sealing at
   commit, meta publication, abort, and reclaim. Generate independently produced
   Go fixtures through public APIs.
5. Build the public advanced logical transactions over that core: direct,
   membership, feed catalog/references, common structured manager, and modular
   `NetworkEnrichmentV1` codec. Add randomized scalar-model tests before high-level
   workflows.
6. Build the typed high-level workflows as thin sequencing layers: immutable
   creation, feed lifecycle, direct replacement, first-seen, last-seen,
   membership import, history, metadata, snapshots, queries, joins, and algebra.
7. Complete live coordination, lifecycle transitions, publication/reservation,
   cleanup/housekeeping, commit resolution, and supported-platform behavior over
   the same cores.
8. Complete explicit validation, candidate inspection, best-effort recovery, and
   the version-matched Go worker. Reuse canonical codecs and final mapped-output
   builders; do not add healthy-path validation or a second writer.
9. Add bidirectional producer/cross-open and mixed Rust/Go subprocess gates. Run
   complete correctness, crash, resource, race/checkptr, fuzz/property,
   architecture, mmap syscall, cross-target, and authorized native matrices.
10. Port the Rust benchmark scenarios, measure locally operation by operation,
    profile every material difference, remove wasted work, and repeat until the
    final audit finds no actionable issue. Add loose CI disaster limits only
    after local acceptance.
11. Update package docs, Go README, conformance docs, project instructions, and a
    proven `project-v4-go` runtime skill. Complete the SOW only with implementation,
    artifacts, final audit, SOW move, and one closure commit.

Validation plan:

- Baseline and every milestone: `go test ./...`, `go vet ./...`, `gofmt`, race
  where supported, checkptr, leak/descriptor/residue checks, and exact changed-
  surface tests.
- Literal portable vectors for every integer, meta field, page header, record,
  hash, checksum, structured payload, sidecar/control block, and publication
  record. Cross-architecture codec execution must include a big-endian target or
  emulator.
- Public black-box semantic/property models for arrival-order normalization,
  direct/membership/structured transactions, workflows, commit/abort, and
  reference invalidation.
- Explicit malformed/corrupt corpus, fault injection, truncation, crash-point,
  cleanup retry, outcome resolution, worker chaining, and worker-build mismatch
  tests.
- Static source gates plus Linux runtime tracing prove mmap-only persistent
  content and the absence of complete out-of-map page images.
- Go-produced fixtures are built only through public Go operations, verified
  against `cases.json`, opened by Rust, and then committed. Rust fixtures are
  actually opened and checked by Go.
- Mixed subprocesses cover every section-21 direction and state; language-local
  unit tests are insufficient.
- Local benchmarks use the same fixture identities, work counts, timing
  boundaries, allocation/RSS/descriptor/file measurements, warm-up, isolated
  samples, and semantic-result checks as Rust. Profiles and necessary-work
  counters identify retained costs.
- Local cross-compilation covers supported target graphs. Runtime platform proof
  occurs only on user-authorized native Windows GNU, Apple ARM64, and FreeBSD 14
  environments; operational host details are never written to the repository.
- Before closure, run the exact final audit format below. If it reports a valid
  issue, fix it and repeat the entire audit rather than completing the SOW.

Artifact impact plan:

- AGENTS.md: add the proven Go workflow/guards and index the Go project skill at
  closure; do not weaken the current engineering philosophy.
- Runtime project skills: create `.agents/skills/project-v4-go/SKILL.md` only
  after commands, platform boundaries, and evidence are proven.
- Specs: change only if implementation exposes a genuine contradiction. Stop for
  a user design decision before changing behavior or bytes; otherwise record
  that the Go peer implements the existing specs unchanged.
- End-user/operator docs: add `v4/go/README.md` and package documentation for the
  completed SDK, worker installation, platform support, resource behavior, and
  reproducible verification/performance commands.
- End-user/operator skills: none currently exist; reassess at closure if a
  downstream/public workflow is introduced.
- SOW lifecycle: this remains the sole Go-port work item. Move it to `current/`
  only when implementation begins; close it only with all code/artifacts and the
  closure commit. SOW-0017 remains separate Phase 2.

Open-source reference evidence:

- `etcd-io/bbolt @ 01f7d9658a8a`
  - `bolt_unix.go:55-83`
  - `bolt_windows.go:65-119`
  - `db.go:454-570`
- This reference is evidence that Go can isolate basic Unix/Windows mapping
  mechanics behind small platform files. Its database architecture is not reused
  because it does not implement this format's mmap-only writer, external reader
  table, exact durability, recovery, or public semantics.

Open decisions:

- No product or format decision is open. The current specifications and accepted
  Rust semantics are frozen for this port.
- The exact obsolete Go deletion set is intentionally resolved by Milestone 0
  evidence and still requires explicit user approval before deletion.
- If pure Go cannot meet the exact fault-worker contract without a new assembly
  or native boundary, stop and present evidence and options. Do not silently add
  cgo, use the Rust worker, reach into Go runtime internals, or weaken fault
  ownership/chaining.

Resolved decisions (2026-08-11, Milestone 0 closure):

- Deletion set (Decision 1): user chose C - decide after Milestone 1 evidence.
  The exact proposed set (100 tracked files + 2 untracked leftovers) is
  recorded in `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  section 7. No tracked file is deleted and no untracked leftover is removed
  before that decision; deletion, when approved, executes atomically with the
  compiling, tested Milestone 1 replacement.
- Fault-worker native boundary (Decision 2): user chose A - wait for the
  Milestone 1 feasibility evidence, then decide. No new native boundary
  (assembly shim, cgo, runtime internals, Rust worker) may be added before
  that decision.

## Implications And Decisions

1. **Long-term-best: semantic peer, not incremental compatibility.**
   The current Go experiment is not a supported predecessor. Current specs and
   observable Rust semantics win every conflict. Reuse requires proof.
2. **Long-term-best: pure Go.**
   No cgo or Rust runtime dependency. The Rust-provided C ABI remains untouched.
3. **Long-term-best: one physical authority.**
   Internal reader/writer owners implement persistent operations once; public
   advanced and high-level APIs are logical wrappers.
4. **Long-term-best: mmap-only persistent content.**
   Lifecycle and durability syscalls remain necessary, but content-transfer I/O
   and complete out-of-map pages are prohibited.
5. **Long-term-best: semantic conformance.**
   Go and Rust files need not be byte-identical, but every observable value,
   error class, outcome, and cross-process coordination state must agree.
6. **Long-term-best: correctness before optimization, optimality before CI
   standardization.**
   Implement one authoritative path, measure its necessary work, remove waste,
   approve local performance, then add loose CI disaster gates.
7. **Minimal-complete scope.**
   All unsigned Phase-1 Go semantics and their consequences are in scope. C ABI,
   signing, v3, old-v4 compatibility, update-ipsets downloader/parser changes,
   and speculative structures are not.
8. **Controlled handoff.**
   Milestone 0 is an evidence checkpoint; the immutable cross-reader is the first
   code-quality checkpoint. No remote push, native-host access, tracked deletion,
   format change, or new native boundary is authorized merely by this SOW.

## Plan

1. **Milestone 0 - exact gap and replacement map.** Read-only inventory, parity
   matrix, proposed Go API/module graph, current-file classification, deletion
   approval list, risk register, and projected size. No production edit.
2. **Milestone 1 - portable mapped immutable reader.** Exact current codecs,
   mapping/lifetime owner, public immutable reader, all Rust fixture cross-reads,
   malformed bootstrap rejection, zero-allocation lookup/scan evidence, and the
   first platform/worker feasibility report.
3. **Milestone 2 - mapped COW writer and Go producer.** One physical writer,
   current allocation/retirement/commit rules, direct/membership/structured
   construction, public generation of the Go corpus, Rust cross-open.
4. **Milestone 3 - complete logical SDK.** Advanced transactions, typed
   workflows, metadata, queries, joins, algebra, snapshots, reports, cancellation,
   cleanup, and randomized public models over the internal core.
5. **Milestone 4 - live/platform/recovery completion.** Sidecar coordination,
   lifecycle and publication resolvers, supported-platform boundaries, explicit
   validation/recovery, worker, crash/fault/resource proof.
6. **Milestone 5 - cross-language and performance acceptance.** Mixed-process
   matrix, native proof, representative update-ipsets replay, matched Rust/Go
   benchmarks, profiles, necessary-work audit, code-size/duplication audit, docs,
   skill, and repeated clean final audit.

Every milestone report records:

- exact files changed and commits;
- behavior completed and behavior still missing;
- commands and factual results;
- production LOC, largest files/functions, allocations/resources, and benchmark
  deltas relevant to that milestone;
- same-failure and duplicate-authority searches;
- deviations or new decisions requiring the user; and
- whether the next milestone is safe to start.

## Required Final Audit Format

Use these sections in this order:

1. **Executive conclusion** - accepted or not accepted, with no hedging.
2. **Scope and source graph** - every production source classified and compiled.
3. **Single authority and two-level API** - physical owner map and bypass search.
4. **Hot-path necessary work** - counters, profiles, allocations, copies, syscalls,
   checksums, synchronization, and rejected optimizations.
5. **Correctness and durability** - public models, failures, commit/abort/crash,
   cleanup, reclamation, and outcome resolution.
6. **mmap-only and bounded resources** - static/runtime proof, heap/RSS/VM,
   descriptors, scratch, sparse scaling, and no complete page copies.
7. **Cross-language conformance** - both producer sets and both subprocess
   directions actually executed.
8. **Portability** - cross-target compilation versus separately identified native
   runtime evidence and exact unsupported boundaries.
9. **Maintainability** - production LOC, largest files/functions, complexity,
   clone/similarity results, dead code, layering, and justified exceptions.
10. **Findings and iteration record** - every finding, repair, repeat result, and
    remaining issue. Any remaining actionable issue means not accepted.

## Execution Log

### 2026-08-11 - planning and clean handoff

- Committed and pushed ignore rules for local generated build trees, stamp files,
  and Go test executables as commit `900b345`.
- Verified local `master` and `origin/master` at that commit with no visible
  uncommitted or untracked generated state.
- Updated the Rust and conformance README status text to record that the proven
  Rust result now authorizes this pending Go port; no runtime behavior changed.
- Read the complete current architecture/binary specifications, current/pending
  SOWs, Rust project skill, accepted Rust public/benchmark evidence, conformance
  corpus, and current Go production/test tree.
- Ran the current Go test, vet, and formatting baselines successfully.
- Confirmed no Go implementation edit was made. This file defines the future
  port; it does not start it.

### 2026-08-11 - Milestone 0 completed (read-only)

- Files changed: created
  `.agents/sow/milestones/SOW-0025-milestone-0-report.md` (full inventory,
  parity matrix, proposed module/API graph, per-file classification, exact
  deletion set, risk register, size projection); appended this execution-log
  entry. No production or test file in `v4/go/` was touched.
- Commits: `972960a` is the pre-milestone HEAD; the report and this entry are
  committed together as the Milestone 0 closure commit (local `master`, no
  remote push — per the controlled-handoff rule).
- Commands and results: `go test ./...` exit 0, `go vet ./...` exit 0,
  `gofmt -l .` empty, at HEAD `972960add710` with a clean tree. Measured:
  50 production files / 44,088 newline-counted lines, 59 test files / 37,403
  lines, 4 files with content-transfer calls, 24 files with complete page
  arrays, zero conformance references, 64 matched error codes (65-69 missing),
  sidecar stale magics/layouts, Rust production base 82,516 lines (livedb).
- Milestone 0 report outcome:
  - transfer files (6 production + 5 tests): `errors.go`, `key.go`,
    `cardinality.go`, `page.go`, `name_binding.go`, `process_identity.go`
    and their tests — each re-verified against literal vectors in Milestone 1
    before reuse;
  - proposed tracked deletions: 98 files (44 production + 54 test) — exact
    list in the milestone report, awaiting user approval (Decision 1);
  - fault-worker native-boundary policy: pure-Go feasibility proof is a
    Milestone 1 exit criterion; minimal project-owned assembly shim requires
    user decision (Decision 2);
  - projected Go production size: ~32-44k lines (midpoint ~37k), ~40-53% of
    the Rust base;
  - next milestone safe to start once Decisions 1 and 2 are answered.
- Same-failure and authority searches: content-transfer (4 files), complete
  page arrays (24 files), stale constants (retention 2 files, 1 MiB limit 1
  file, v3 zero), structured/threat/conformance references (zero) — each class
  is re-searched as a gate in later milestones; the old tree has one read and
  one write boundary internally, but that authority design is the rejected
  one, so the new tree mechanically enforces owner boundaries with
  `v4/go/check-import-graph.sh` (import boundary gates, run in the same gate
  set as the Rust `check-source-graph.sh`).
- No deviation from the approved SOW plan. Decisions requested from the user:
  (1) approve the 100-tracked-file + 2-untracked-leftover deletion set;
  (2) worker boundary: reopened by the milestone-1 evidence — SetPanicOnFault
  proves only a panic-based subset, not the spec-exact SA_SIGINFO contract
  (milestone-1 report §11); the user chooses between a minimal project-owned
  assembly sigaction shim and a spec change. Both are recorded in the
  milestone report with evidence and recommendations.

### 2026-08-11 - Milestone 0 corrected after external review (read-only)

- An external review of the milestone report found material errors. Every
  finding was verified against the sources before any change; all verified
  findings were corrected, one claim was not reproducible, and the two
  original recommendations were withdrawn as unsafe:
  - `process_identity.go` moved from transfer to delete: it implements a
    PID/process-start stale-slot recovery model (`process_identity.go:15`,
    `sidecar.go:394-396`) that the current spec explicitly excludes
    (`binary-format-v4.md:2130-2138`). Its test was moved to the deletion set
    too. Deletion set is now 100 tracked files (45 production + 55 test).
  - Error-code mapping corrected: names match Rust 1-64 in every position
    except code 46 (Go `ErrorLiveCoordinationDomainMismatchRequiresReset` vs
    Rust `LiveCoordinationMalformedRequiresReset`, `sdk_error.rs:58`); the
    report no longer claims an exact match and transfer requires resolving
    code 46 plus adding 65-69.
  - Prohibited-I/O classification corrected: content-transfer calls are
    `ReadAt`/`WriteAt`/`unix.Pread` in 3 files (`os_linux.go:503,565`,
    `page_source_linux.go:27`, `page_source_other.go:26`); `Truncate`/`Sync`
    are required lifecycle/durability syscalls and are not prohibited.
  - Decision recommendations changed to evidence-first for both decisions
    (deletion executes atomically with the compiling, tested Milestone 1
    replacement; fault-worker boundary is decided after the Milestone 1
    feasibility evidence, per the SOW's own stop-for-decision wording).
  - Design-spec line count corrected (519, not 530); exported-symbol report
    corrected to measured values (199 top-level exported declarations; 43 of
    50 files export nothing) and no longer labeled a "public SDK surface";
    size projection explicitly labeled estimate-only, not a target.
  - The report moved from an undocumented `.agents/sow/milestones/` directory
    to `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`, the best
    interpretation of the review's "wrong SOW path" finding. The reviewer
    later retracted that finding ("the original path was correct; moving the
    report was unnecessary, although harmless"). The report stays at the new
    location; both the SOW and this log reference the new path.
  - `.reasonix/` is harness session state, untracked by design; it is not repo
    content and was not committed.
- Files changed: `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  (moved and corrected, 496 lines), this execution-log entry. Still zero
  production/test edits in `v4/go/`.
- Commits: `579c1a3` (initial Milestone 0 report + log), `04032d6` (verified
  corrections, report moved to `pending/`, SOW correction log entry),
  `b91301d` (removal of the superseded old report path). All local, no remote
  push.
- Milestone 1 begins with moving SOW-0025 to `.agents/sow/current/` with
  `Status: in-progress`; it remains `open` in `pending/` while implementation
  has not started.

### 2026-08-11 - Milestone 1 completed (implementation checkpoint)

- Moved SOW-0025 to `.agents/sow/current/` with `Status: in-progress`
  (commit `0de8793`) and implemented the milestone:
  - New production packages: `internal/format` (wire codecs, meta identity
    and kind invariants, page/slotted, range, catalog, membership,
    structured, blob, metadata chunks, 129-bit cardinality, single 1-69
    error-code table with code 46 per the current Rust contract), `internal/
    mapping` (the only mapping owner; POSIX + honest Windows stub),
    `internal/reader` (the only healthy-generation reader core), and the
    public facade `reader_public.go` at the module root. ~3,720 new
    production lines, ~1,700 test lines.
  - Conformance: all five committed Rust fixtures open and verify with exact
    `cases.json` semantics (metadata states, full-IPv6 cardinality strings,
    boundary probes, 70 feed names, word-level bitmaps incl. blob-backed and
    1 MiB metadata, structured values + threat memberships); all three
    invalid mutations rejected with code 32.
  - Zero-allocation evidence: direct v4/v6, membership (inline + blob),
    structured, feed (internal), scans, cardinality = 0 allocs; public feed
    lookup = exactly the returned string copy. Recorded in both root and
    reader zero-allocation tests.
  - Portability: cross-compiles for darwin/freebsd/windows/linux-arm; Windows
    open refuses with os-unsupported stub; runtime proof on Linux amd64.
  - Worker feasibility: pure Go disproven for POSIX with an empirical probe
    (runtime-fatal `unexpected fault address`, os/signal never delivers
    mapping SIGBUS, no si_addr/sigaction surface in x/sys); Windows vectored
    exception path is pure-Go feasible. Fallback = minimal project-owned
    assembly sigaction shim; per recorded Decision 2 the user decides with
    this evidence.
  - Commits: `0de8793` (SOW move), `913f4e6` (reader + tests),
    `9441f85` (independent-review repairs: blob-branch txn threading,
    slotted-page bounds, metadata allocation bound, catalog reserved bytes,
    synthetic two-leaf blob regression database), `1df90fa` (milestone
    report), `4eec44e` (third-pass repairs), `03a910f` (fourth-pass
  repairs: borrow-count lifetime, view API, absence-vs-corruption,
  concurrency race tests, worker and report fact corrections,
  check-import-graph.sh gate, view-API ID removal, callback-error
  passthrough, conformance enumeration, multi-level range-tree test,
  corrected worker conclusion), `1e1ac4b` (fifth-pass repairs: meta tail
  zero invariant, mandatory aux checks, exact zlib stream verification,
  namespace safety under the lifetime lock, structure payload decode at
  lookup, public error typing, adversarial regression suite). No tracked
  file deleted (Decision 1 = C).
  - Full report: `.agents/sow/pending/pure-go-v4-port-milestone-1-report.md`.
    Baseline gates re-run: `go test ./...` (incl. race), `go vet`, `gofmt`,
    cross-compilation matrix — all green; SOW audit clean.
- Both pending decisions now have their evidence: deletion set (100 tracked
  files + 2 untracked leftovers, re-approvable at any time) and the worker
  boundary — the third-pass evidence demonstrates only a panic-based fault
  subset via `runtime/debug.SetPanicOnFault` (empirical recover with exact
  fault address); the spec-exact worker contract needs either a minimal
  project-owned assembly sigaction shim (spec-exact) or an explicit
  spec change — user decision pending (see milestone-1 report section 11).
  Milestone 2 is safe to start after the user's answers to the deletion and
  worker decisions.
- Commands and results: `wc -l` design spec 519; `grep` evidence for error
  code 46 in `errors.go:54` and `sdk_error.rs:58`; sidecar/spec magic and slot
  layout at `sidecar.go:11,15,394-396` vs `binary-format-v4.md:2104,2130-2138`;
  I/O call sites listed above; baseline gates unchanged (already green).

### 2026-08-11 - gap analysis and repair pass (six-agent)

- `94723aa` records the fifth-pass repairs in this log and the M1 report.
- `9a835e4` adds `.agents/sow/pending/pure-go-m1-gap-analysis.md`: six
  concurrent read-only subagents, each with a disjoint brief (codecs, reader
  semantics, architecture, public API/errors, test evidence,
  worker/mapping/report facts) at HEAD `94723aa`. Result: one BLOCKER (B1,
  structure radix divisor one level too deep) and ten MAJOR findings,
  plus three MINOR and one refuted claim.
- `58c4d8f` repairs every verified finding with a regression test: B1 span
  fix, blob-walk record/geometry validation, slotted exactness, OFD lifetime
  lock, API binding guards, absence/word-exact conformance evidence,
  honest LOC, import-graph gate strengthening (every import checked +
  sync/sync-atomic/unsafe ban), and bootstrap minima. No tracked file
  deleted.
- `bb7f485` corrects the SOW worker-feasibility sentences and the report's
  stale claims to match the corrected section 11.
- Gates re-run green at `bb7f485`: `go test ./...` (5 packages, incl.
  -race), `go vet`, `gofmt -l`, `check-import-graph.sh`, 9-target
  cross-compile matrix.

### 2026-08-12 - mandatory six-agent review round 2 and fix pass

- Per the mandatory iterative review gate (one review round per milestone
  before it can close), six new concurrent reviewers with disjoint briefs
  (codecs / bootstrap+ranges / membership+structured+metadata / mapping+
  lifetimes+platform / public API+errors+zero-alloc / conformance+reports)
  reviewed HEAD `bb7f485`. Verdicts: 4 FAIL (codecs 1 P1 + 4 P2; bootstrap
  1 P1 + 1 P2; membership 1 P1 + 3 P2; mapping 1 P1 + 3 P2), 1 FAIL on
  paperwork (reports 5 P1 + 5 P2), 1 PASS-with-2-P1 that are the already
  recorded closed-state error-class user decision. No P0.
- Fixed in this pass (all regression-pinned):
  - catalog `feed_index >= feed_index_limit` now corruption
    (`internal/reader/catalog.go`, `guard_regression_test.go`).
  - kind-classification: registered-but-invalid combinations (structured
    kind 0, direct/membership kind 1) report FormatInvalid (32); only
    unknown kinds report UnsupportedStructure (67)
    (`internal/format/meta.go`).
  - dangling structure reference now the typed corrupt error (structure
    range names an absent structure ID), matching the membership twin
    (`internal/reader/structure.go`).
  - FreeBSD immutable opens now use the canonical whole-file shared flock
    lifetime lock instead of refusing every open (`mapping_lifetime_freebsd.go`).
  - `Mapping.View`/`Page` after Close report the typed wrong-state error;
    `Mapping.Close` is idempotent (`internal/mapping/mapping.go`).
  - metadata zlib FCHECK validated; `deflateStreamLen` probes bounded at
    the declared length (CPU-amplification fix) (`internal/reader/metadata.go`).
  - blob leaf `%8` alignment explicit; checked blob extent arithmetic;
    blob branch validates every probed entry's child page
    (`internal/reader/membership.go`).
  - `publicError` preserves the typed code through wraps; error-code names
    59/62/69 aligned to the Rust `Id` spelling.
  - `check-import-graph.sh` now bans content-transfer I/O in production
    sources and the stdlib `syscall` package.
  - test hygiene: public zero-alloc suite releases its v6 view; structured
    conformance absence probes added; Info()-after-close pinned; literal
    byte vectors added for the v6 range, membership leaf/branch, structure
    record, enrichment payload, and blob-branch codecs.
  - report facts corrected (honest production LOC 4,492 raw / tests 3,794
    raw, zero-alloc table labels, OFD lock wording, metadata allocation
    overhead 0-88 bytes) and this SOW log repaired.
- Commits: see the round-2 fix commit. Gates green after the pass; the six
  reviewers re-review the repaired tree before Milestone 1 closes.

### 2026-08-12 - six-agent review round 3 (final verification)

- All six reviewers re-reviewed the round-2 tree at HEAD with their same
  disjoint briefs. Verdicts: codecs PASS, bootstrap/ranges PASS,
  membership/structured/metadata PASS, public API/errors/zero-alloc PASS
  (fixable issues outside the recorded closed-state decision: none),
  mapping/lifetimes PASS after the gate fix, conformance/reports PASS after
  this record.
- Commits and fixes between the round-2 record and HEAD `3b4f3d5`:
  - `a5d7cf8` + `78cebc4` — import-graph gate comment stripping (line and
    stateful multi-line block), verified both directions.
  - `9203c28` — regression pins: `TestSoleMetaGeometry`,
    membership-kind1/kind2 subtests (both fail on the pre-fix code).
  - `e0a1687` — blob path zero-allocation: value-pair expected-level state;
    internal zero-alloc suite now measures blob word/scan at 0.
  - `a348c42` — real family-min and from-1 conformance absence probes;
    explicit `94723aa` log line.
  - `3b4f3d5` — P0 fix: blob coverage check underflowed when the request
    fell past the selected leaf's end (`end-off` wrapped), allowing a
    silent out-of-leaf word read or a slice panic on crafted files; the
    explicit `off > end` guard restores corruption semantics and
    `TestBlobGapRejectedCorruption` pins it (fails pre-fix in both modes).
- Report counts corrected at HEAD: production 4,500 raw lines (tests
  excluded), tests 3,922 raw, zero-allocation checks 18 (8 internal + 10
  public). Milestone report sections 11c-11d record both passes; the
  reviewers' final verdicts are all PASS with no P0-P2 remaining.
- Gates at HEAD: `go test ./...` (5 packages, incl. -race),
  `go vet`, `gofmt -l`, `check-import-graph.sh`, 9-target cross-compile
  matrix — all green; SOW audit clean.
- Pending user decisions unchanged: (1) deletion set — 100 tracked files +
  2 untracked leftovers, atomic with the Milestone 2 writer commit;
  (2) worker boundary — assembly sigaction shim vs spec change vs drop;
  (3) closed-state error class — HandleClosed (9) vs WrongState (11),
  Release idempotency, WordCount released-view silent-0; plus the recorded
  authority conflict (unknown nonzero structure_kind on direct/membership:
  spec text 67 vs Rust 32).

## Validation

Acceptance criteria evidence:

- Milestone 1 evidence: milestone report
  `.agents/sow/pending/pure-go-v4-port-milestone-1-report.md` (fixture
  cross-open with exact cases.json semantics, malformed rejection, literal
  codec vectors, zero-allocation measurements, five review passes plus the
  round-2 six-agent review, all repairs regression-pinned). Milestone 2 not
  started.

Tests or equivalent validation:

- `go test ./...` (5 packages) — green at the last commit.
- `go test -race ./internal/format ./internal/reader ./internal/mapping .` — green.
- `go vet ./...` — clean; `gofmt -l .` — empty.
- `./check-import-graph.sh` — passes; it now also bans content-transfer I/O
  in production sources and the stdlib `syscall` package.
- Cross-compilation: darwin/amd64+arm64, freebsd/amd64+arm64,
  windows/amd64+arm64+386, linux/386+arm64 — all build.
- Conformance: 5/5 Rust fixtures cross-open with exact semantics; 3/3 invalid
  mutations rejected with code 32; structured absence probes added.
- `.agents/sow/audit.sh` — clean.

Real-use evidence:

- The Rust peer has accepted representative update-ipsets replay evidence.
  Equivalent Go evidence is an implementation acceptance requirement, not a
  planning claim.

Reviewer findings:

- Six-agent adversarial review (round 2, 2026-08-12): 4 FAIL verdicts and 1
  PASS-with-pending-decision; every actionable P1/P2 finding was repaired in
  the round-2 fix pass with a regression test; the two P1s on the closed-state
  error class are the pending user decision. The reviewers re-review the
  repaired tree before Milestone 1 closes.

Same-failure scan:

- Round-2 searches re-ran the full classes: content-transfer I/O (now also a
  mechanical gate), complete page arrays, stale wire constants, missing
  record-limit validation (catalog feed index), kind-classification error
  classes, blob/probe validation gaps, metadata stream validation, report and
  SOW factual drift. Each class is fixed completely, not only the cited
  examples.

Sensitive data gate:

- This SOW contains repository-relative paths, public upstream identity,
  generic platform names, code metrics, and synthetic/public benchmark
  descriptions only. It contains no raw secret, credential, operational host
  alias, personal or customer/community data, private endpoint, or proprietary
  incident detail.

Artifact maintenance gate:

- AGENTS.md: reviewed; no change needed at this milestone.
- Runtime project skills: Rust skill reviewed; a Go skill is deliberately not
  invented before implementation evidence exists.
- Specs: reviewed completely; no format or behavior change was made.
- End-user/operator docs: Go README/package docs remain required during
  implementation.
- End-user/operator skills: none exist and no public workflow changed.
- SOW lifecycle: this file is `in-progress` under `current/`; SOW-0017 remains
  the separate Phase-2 item.

Specs update:

- None for Milestone 1: the Go reader implements the current normative
  contract unchanged.

Project skills update:

- None for Milestone 1: create the Go runtime skill only from proven commands
  and hazards later in this SOW.

End-user/operator docs update:

- The round-2 repairs are recorded in the milestone report and this SOW log;
  the Go SDK documentation itself is an implementation deliverable.

End-user/operator skills update:

- None exist; reassess at closure.

Lessons:

- Passing tests around private fragments do not establish a usable SDK or
  current wire compatibility.
- A semantic port needs an explicit parity matrix and cross-language
  execution; source similarity is neither required nor sufficient.
- The most dangerous way to port this engine is to preserve the obsolete Go
  storage architecture because it already looks substantial.
- Single-reviewer passes converge; only adversarial multi-agent passes with
  disjoint briefs found the catalog-limit, kind-class, dangling-reference,
  FreeBSD, and paperwork defects.

Follow-up mapping:

- Snapshot signing remains tracked by pending SOW-0017.
- No other deferred item is created by this milestone.

## Outcome

Pending implementation and user acceptance.

## Lessons Extracted

Pending implementation.

## Followup

- SOW-0017 remains the separate Phase-2 signing item.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated
`## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend
regression content above the original SOW narrative.
