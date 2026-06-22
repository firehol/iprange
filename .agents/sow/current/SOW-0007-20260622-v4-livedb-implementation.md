# SOW-0007 - iprange v4 live-DB format: implementation (Go + Rust)

## Status

Status: in-progress

Sub-state: Authored 2026-06-22 after the v4 format spec was **locked** (SOW-0006,
`.agents/sow/specs/design-iprange-v4-livedb.md`). Implementation decisions **O1–O4
resolved by the user 2026-06-22** (see "Decisions (resolved)"). Moved to `current/`
2026-06-22; gate filled. **Step 0 (repo reorganization into `v3/`) done and verified
green** (see Execution Log). Next: scaffold the Rust v4 engine under `v4/`.

## Requirements

### Purpose

Implement the **locked** v4 live-DB format — a portable, mmap'd, mutable copy-on-write
B+tree of fixed-size `[from, to, scope]` records — in **Go** (the reference writer; the
update-ipsets daemon is the sole writer) and **Rust** (reader + writer for cross-read
conformance; also the C-facing library, decision O3 of SOW-0006). Two independent
implementations that **interoperate** (a file written by one is read identically by
the other) and **safely reject hostile input**, validated by a shared behavioral
conformance corpus, crash-recovery tests, and fuzzing.

This is the engine that fixes the update-ipsets retention performance problem:
per-IP removal becomes `O(log n)` (no cohort scan) and a change rewrites only
`O(log n + k)` pages (no full rebuild), while the dataset may exceed RAM (mmap).

### Hard Performance Requirement (non-negotiable, user-set 2026-06-22)

Every statement in these libraries is crucial. The code MUST be:

- **Lean and absolutely necessary** — no statement that is not required; no
  speculative abstraction, no dead generality, no convenience layer that costs
  cycles. If a line is not earning its place, it is removed.
- **Highly optimized for speed** — the hot paths (lookup, set, delete, commit,
  validate, scan) are designed for the CPU: cache-friendly access, predictable
  branches, no per-operation hashing/boxing/virtual dispatch where avoidable.
- **Zero allocations where possible** — the steady-state read and point-mutation
  paths allocate **nothing** on the heap; buffers are reused; results are written
  into caller-provided storage. Any unavoidable allocation is justified in a
  comment and counted in tests.
- **Minimal I/O** — a change touches only the pages it must (`O(log n + k)`), with
  the smallest number of `fsync`/`msync` barriers the crash-safety contract allows
  (spec §6). No full-file rewrite, no redundant reads, no write amplification
  beyond the COW path the format requires.

**Acceptance bar:** the external review panel must vote **"ABSOLUTELY OPTIMAL,
ZERO WASTE"** on both implementations. Anything less means the design or the code
is wrong and must be fixed before completion. This bar applies to **both** the
format (if the byte layout forces waste, the format is wrong) and the code (if the
implementation wastes cycles/allocations/I/O the format does not require, the code
is wrong). Allocation and I/O claims must be **measured**, not asserted
(alloc counters / `perf` / syscall traces), and the evidence recorded in
Validation.

### User Request

Costa: build the v4 live DB, then implement it. The format was locked through 9
external review rounds (SOW-0006). Earlier direction: "get bbolt code, change it for
ranges, port to rust, same tests pass." As the spec converged it became **simpler and
different from bbolt** (fixed-size records, no MVCC, derived allocator, double-meta
commit, flock) — so the implementation follows the **locked spec**, using bbolt (Go)
and redb (Rust) as **technique references** for COW/mmap/crash, not as a literal fork
(see O2).

### Assistant Understanding

Facts:
- The contract is `design-iprange-v4-livedb.md` (LOCKED). It fully specifies the byte
  layout, the COW + double-meta-page commit, the derived allocator, flock concurrency,
  mmap safety, the recursive `validate_node`, and the v3-export delegation.
- Conformance is **behavioral + cross-read** (O1 resolved): byte-identity is **not**
  required; Go and Rust MAY differ in tree shape but MUST return identical query
  results and read each other's files.
- The v3 format libraries already exist in `go/` and `rust/iprange-format/` (from
  SOW-0002/0003); v4 is a **new** structure, not an extension of those.
- Reference implementations to study (technique only): **bbolt** (Go COW B+tree, page
  meta-flip, crash model) and **redb** (pure-Rust COW B+tree, safe mmap). Cite as
  `owner/repo @ commit` when used as evidence.

Inferences:
- Go should be implemented first: it is the daemon's writer + the immediate consumer,
  and shaking out the spec in the reference language de-risks the Rust port.
- Because byte-identity is dropped, the two implementations need only agree on the
  **on-disk format** (so cross-read works) and on **query results** — not on tree
  shape — which is exactly what the conformance corpus tests.

Unknowns → the implementation decisions O1–O4 below (for the user).

### Acceptance Criteria

- **Go v4 library**: writer (`set`/`delete`/commit, COW + double-meta, derived
  allocator, flock) + reader (mmap, recursive `validate_node`, lookup/scan) +
  `export_v3` delegation. Open-change-close API per spec §11.
- **Rust v4 library**: the same, byte-interoperable with the Go files; exposes the
  C-facing library (O3).
- **Shared behavioral conformance corpus** (language-neutral, like `conformance/` for
  v3): scripted op-sequences → expected query results; **cross-read** (Go-written file
  read by Rust and vice versa, identical results); **malformed-input** cases hitting
  every §9 reject path.
- **Crash-recovery tests**: a commit interrupted at each barrier (before Barrier 1,
  between barriers, before/after Barrier 2) leaves the defined surviving tree; torn
  meta / orphan pages handled per §6.3/§6.4.
- **Property/fuzz**: random op-sequences checked against an in-memory interval-map
  **oracle** (same `lookup` results); `cargo-fuzz` `Reader::open` + Go `FuzzOpen` over
  the on-disk format never panic/loop/OOB and reject hostile input.
- Both implementations green on the shared corpus; sanitizer/Miri clean where
  applicable; the round-9 cosmetic P3s (worked examples in the spec) folded in.
- **Performance bar met and measured** (see "Hard Performance Requirement"):
  steady-state read + point-mutation paths show **zero heap allocations**
  (alloc-counter test in both languages); a single mutation issues only the
  spec-mandated `fsync`/`msync` barriers and touches only `O(log n + k)` pages
  (syscall/page-write trace); hot loops are branch- and cache-conscious. External
  review panel vote recorded as **"ABSOLUTELY OPTIMAL, ZERO WASTE"** (or the
  findings are fixed and the panel re-run until it is).

## Analysis

Sources checked:
- `.agents/sow/specs/design-iprange-v4-livedb.md` (the locked contract).
- `.agents/sow/specs/binary-format-v3.md` (the v3 writer the export delegates to).
- Existing `go/`, `rust/iprange-format/`, `conformance/` (patterns to mirror).
- bbolt / redb (technique references — to be pinned by commit when used).

Risks:
- It is an embedded database: **crash-recovery and allocator/commit correctness** are
  the high-risk areas. Mitigation: mirror bbolt/redb's proven mechanics; exhaustive
  crash-injection tests; the behavioral oracle; fuzzing.
- A shared logic bug could make Go and Rust agree on a wrong answer. Mitigation: the
  in-memory oracle (independent of the B+tree) cross-checks query results.

## Decisions (resolved)

The user resolved O1–O4 on 2026-06-22. Recorded here as binding before implementation:

- **O1 — Language order → Rust is the REFERENCE, Go follows immediately.**
  Rust is written first as the canonical reference implementation; the Go port
  starts immediately after (not deferred). *(User overrode the Go-first
  recommendation: Rust is the C-facing library and the cross-impl reference; Go
  follows at once so both land together.)*
- **O2 — Implementation base → Fresh.** Implement the locked spec from scratch.
  bbolt (Go) and redb (Rust) are **technique references only** (COW/mmap/crash
  mechanics), cited `owner/repo @ commit` when used as evidence. No fork.
- **O3 — Code location → reorganize into top-level `v3/` and `v4/` directories;
  move the existing v3 code into `v3/`.** The repo is restructured so the two
  format generations are cleanly separated. The exact move plan and its blast
  radius (build files, CI, conformance harness) are mapped before any move — see
  the "v3/v4 reorganization" item in the Implementation plan. v4 code lives under
  `v4/`.
- **O4 — Phase 1 scope → engine first, then immediately the whole of it.** Build
  and fully validate the core engine first (writer + reader + conformance +
  crash-recovery + fuzz + the performance bar), then **immediately** complete the
  rest in the same SOW: `export_v3` delegation and the update-ipsets retention
  integration. No long deferral to a separate phase/SOW.

## Pre-Implementation Gate

Status: complete (O1–O4 resolved; gate filled 2026-06-22).

**Problem / root-cause model.** update-ipsets retention is slow because the
time-partitioned cohort files have no per-IP index: removing one IP scans thousands
of files (`update-ipsets/pkg/engine/retention_update.go:381-419`). The dataset can
exceed RAM, so an in-memory index is not an option. The fix is an on-disk, mmap'd,
mutable interval map keyed by IP — the v4 live DB — where a point removal is
`O(log n)` and a change rewrites only `O(log n + k)` pages instead of the whole file.
The format design is settled and **locked** (SOW-0006); this SOW builds it.

**Evidence reviewed.**
- `.agents/sow/specs/design-iprange-v4-livedb.md` — the LOCKED contract (byte layout,
  COW + double-meta commit §6, derived allocator §7, set/delete §8, recursive
  `validate_node` §9, mmap safety §10, flock concurrency §11, conformance §12,
  v3-export delegation §13, corner cases §14, complexity §15).
- `.agents/sow/specs/binary-format-v3.md` — the v3 writer that `export_v3` delegates to.
- Existing v3 libs (now under `v3/`): `v3/go/`, `v3/rust/iprange-format/`,
  `v3/conformance/` — patterns to mirror (wire (de)serialization, conformance corpus
  schema, robustness/fuzz harness, big-endian guard).
- Technique references (cited `owner/repo @ commit` when used): **etcd-io/bbolt**
  (Go COW B+tree, meta-page flip, crash model) and **cberner/redb** (pure-Rust COW
  B+tree, safe mmap). Reference only — not forked (O2).

**Affected contracts and surfaces.**
- New on-disk artifact: the v4 file format (the locked spec is the contract).
- New libraries: `v4/rust/iprange-livedb` (reference + C-facing) and `v4/go/...`.
- New shared corpus: `v4/conformance/` (behavioral op-sequences + cross-read +
  malformed-input + crash-injection).
- `export_v3` bridges v4 → the existing v3 writer (must not change v3 bytes).
- update-ipsets retention integration (Phase: "then immediately the whole of it").
- Repo structure changed: `v3/` now holds the v3 libs (Step 0, done); `v4/` holds v4.

**Existing patterns to reuse.**
- The v3 wire layer's explicit little-endian (de)serialization + the s390x big-endian
  CI guard (`.github/workflows/big-endian.yml`) — extend to v4.
- The conformance corpus schema (`v3/conformance/README.md`) and the
  language-neutral case → golden/expected pattern — adapt to behavioral cases.
- The Rust robustness tests (truncation/bit-flip never-panic) and Go fuzz harness —
  reuse the shape for `Reader::open` over the v4 on-disk format.
- The LCG workload generator (shared Rust/Go constants) for benchmarks.

**Risk and blast radius.**
- **Highest risk: crash-recovery + commit/allocator correctness** (it is an embedded
  DB). Mitigation: mirror bbolt/redb proven mechanics; exhaustive crash-injection at
  each barrier; the in-memory interval-map oracle; fuzzing; Miri/sanitizers.
- **Shared-logic risk**: Rust and Go could agree on a wrong answer. Mitigation: the
  oracle is independent of the B+tree.
- **Performance risk**: the hard bar (zero-alloc, minimal I/O, "ABSOLUTELY OPTIMAL,
  ZERO WASTE"). Mitigation: design hot paths zero-alloc from the start; measure with
  alloc counters + syscall traces, not assertions; review panel gate.
- **Reorg blast radius (Step 0, already executed + verified):** moving
  `go/`+`rust/`+`conformance/` under `v3/` touched only `go.mod` module path
  (no importers — safe), `big-endian.yml` (working-dirs + path filters), `.gitignore`,
  and 3 doc/comment path mentions. Test code unchanged (relative paths preserved). Go
  + Rust suites green from the new locations.

**Sensitive data handling plan.** None expected — synthetic IP ranges only, no real
feeds, no credentials. Standard FireHOL guardrail: operational details (servers,
keys) stay out of this repo's artifacts. Benchmarks use synthetic, production-shaped
data (counts/shapes, never customer-identifying content).

**Implementation plan (ordered).** See "Plan" below (Step 0 done; Steps 1–6 next).

**Validation plan.** See "Acceptance Criteria" + the performance bar. In short:
behavioral conformance (both langs), cross-read, malformed-input reject paths,
crash-injection recovery, property/fuzz vs oracle, sanitizer/Miri, **measured**
zero-alloc + minimal-I/O evidence, external panel vote "ABSOLUTELY OPTIMAL, ZERO
WASTE".

**Artifact impact plan.** AGENTS.md: add v4 build/test commands + the new `v3/`/`v4/`
layout once the engine lands. Specs: `design-iprange-v4-livedb.md` stays the contract;
add a current-reality v4 usage note when the API stabilizes. Project skills: capture
the conformance + crash-injection + benchmark harness as the first `project-*` skill
once stable (SOW-0001 deferred-skill candidate). End-user docs (`wiki/`): add v4 CLI/
library usage when shipped. SOW lifecycle: this SOW drives it to completion.

**Open decisions.** None — O1–O4 resolved (see "Decisions (resolved)").

## Implications And Decisions

Decisions O1–O4 resolved by the user 2026-06-22 (see "Decisions (resolved)"):
**O1 = Rust-reference-then-Go-immediately, O2 = fresh, O3 = `v3/`+`v4/` reorg
(move v3), O4 = engine-first-then-immediately-the-whole.** Plus the **hard
performance bar** (lean / speed / zero-alloc / minimal-I/O, reviewer vote
"ABSOLUTELY OPTIMAL, ZERO WASTE").

## Plan (proposed; finalized when SOW moves to current/)

0. **Repo reorganization (`v3/` + `v4/`)** — map every build/CI/conformance
   reference to the current v3 paths (blast-radius in progress), then move the
   existing Go + Rust v3 libraries under `v3/` and update every reference
   (autotools/CMake dist, `.github/workflows/` incl. the big-endian s390x job, the
   conformance harness, READMEs). Create `v4/` for the new code. Verify the full
   build + test + CI matrix is green **before** writing v4 code.
1. **Rust reference (v4)** — page/meta structs, CRC32C, fixed-record B+tree
   (search/insert/delete/split/merge), derived free-set, COW + double-meta commit,
   flock, mmap reader + recursive `validate_node`, `set`/`delete`/`lookup`/`scan`.
   Built to the performance bar: zero-alloc read/mutation paths, minimal I/O, hot
   loops cache/branch-conscious; alloc + syscall evidence captured as it is built.
   Exposes the C-facing library.
2. **Conformance harness + corpus** — language-neutral op-sequence cases → expected
   query results; crash-injection cases; malformed-input cases; the in-memory
   interval-map oracle.
3. **Go port (immediately after Rust)** — same on-disk format; cross-read against
   the Rust-produced corpus; same performance bar.
4. **Fuzz + crash-recovery** — `cargo-fuzz`/`go test -fuzz` on `open`; barrier-
   interruption recovery tests; sanitizer/Miri; the alloc/I/O measurements.
5. **Then immediately (still this SOW, per O4)** — `export_v3` delegation +
   update-ipsets retention integration.
6. **Review to bar** — run the external panel until it votes "ABSOLUTELY OPTIMAL,
   ZERO WASTE" on both implementations; fix every finding, re-run, repeat.

## Execution Log

**2026-06-22 — Step 0: repo reorganization into `v3/` (done, verified green).**
- Mapped the full blast radius first (every build/CI/conformance reference).
- `git mv go v3/go`, `git mv rust v3/rust`, `git mv conformance v3/conformance`
  (history preserved; relative test paths preserved by moving all three together).
- Path fixes: `v3/go/go.mod` module → `github.com/firehol/iprange/v3/go` (no importers,
  safe); `.github/workflows/big-endian.yml` working-dirs + `paths:` filters → `v3/...`;
  `.gitignore` → `v3/`+`v4/` target/bin; 3 doc/comment path mentions.
- Verified: Go (`v3/go`) build + tests pass; Rust (`v3/rust`) conformance/legacy/
  robustness/unit all pass after a clean rebuild (initial failure was a stale
  incremental-build cache with the old `CARGO_MANIFEST_DIR` baked in — gone after
  `cargo clean`). `git grep` confirms zero stale old-path references in tracked files.

**2026-06-22 — Step 1a: Rust v4 foundation layer (done, green).**
- `v4/rust/` workspace + `v4/rust/iprange-livedb/` crate, mirroring the v3 crate's
  idioms (no_std core, `std`/`alloc` features, `IpKey` trait, typed `Error`, the
  `spec`/`wire` LE split). Modules (~1163 src lines incl. tests):
  - `spec.rs` — every v4 constant + the §5.1 meta byte offsets + geometry
    (`record_size`/`leaf_max`/`branch_max`) with a contiguity self-check.
  - `crc32c.rs` — CRC32C/Castagnoli (D9), const table, `page_checksum`/`verify_page`
    (whole-page span with the checksum field zeroed; high-32-bits-zero rule). Spec
    vector `crc32c("123456789") == 0xE3069283` verified.
  - `key.rs` — `Ipv4Key`/`Ipv6Key` (numeric order, §4 `u128_inc`/`dec` boundaries).
  - `record.rs` — zero-copy `RecordRef` + `write`; `scope` always borrowed (D11).
  - `wire.rs` — unaligned LE primitives (D8), `PageHeader`, `Meta` (de)serializer +
    the **meta byte-offset anchor test** (every §5.1 field at its exact offset).
  - `error.rs` — typed reject errors.
- Verified: build clean; **19/19 unit tests pass**; `clippy --all-targets -D warnings`
  clean; no_std build (`--no-default-features --features alloc`) clean. (The fork I
  first delegated this to produced a narrative but made zero tool calls — created
  nothing; caught by a filesystem check, then built directly. Lesson recorded.)

**2026-06-22 — Step 1b-i: reader core (byte-slice; done, green).**
- `node.rs` — zero-copy `LeafView`/`BranchView` page views (offset arithmetic only).
- `reader.rs` — `Reader::open(&[u8])`:
  - §5.1 bootstrap: `select_active_meta` + `classify` (3-class — torn discard / intact-
    incompatible fail-closed / valid; higher `txn_id` wins, static-identity agreement).
  - §9 step 2 geometry: `total_pages` range, overflow-checked `total_pages·page_size`,
    file-size multiple/≥, `tree_height ≤ 32`, height↔root consistency, root in range.
  - §9 step 4 recursive `validate_node` (inherited `[lo,hi]`, depth/cycle defense,
    page_type↔depth, leaf/branch occupancy, separators strictly increasing, children
    distinct + in range, tail-zero, cross-leaf disjointness via threaded `prev_to`) +
    the exact `record_count` check (§9 step 5).
  - `lookup` (binary-search descent + leaf search), `scan` (in-order re-descend, D3);
    both return the **borrowed** scope (zero-copy). Family-checked public API.
- All zero-alloc: validation/scan recursion is bounded by `tree_height ≤ 32` (native
  stack, no heap); hostile input is rejected with a typed error, never panics/OOB.
- Verified: **29/29 tests pass** (10 new reader tests: single-leaf + two-level lookup/
  scan, empty tree, torn-inactive-meta recovery, both-metas-corrupt reject,
  incompatible-major fail-closed, unsorted-leaf reject, record_count mismatch, family
  mismatch, truncation); clippy `-D warnings` clean; no_std build clean.

Next: Step 1b-ii — the **OS layer** (open `O_NOFOLLOW|O_CLOEXEC`, `fstat`, `SEEK_HOLE`,
`mmap` `MAP_SHARED`, `flock(LOCK_SH)`, §10/§11 hardening) wrapping this core; then the
**writer** (COW B+tree, double-meta commit).

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- M1–M4 tunables (page-size already fixed at 4096; split/fill thresholds, fsync
  policy) measured by **SOW-0005** once the engine runs.
- Test-hardening patterns (oracle, fuzz, malformed-input) from **SOW-0004** apply.
- Capture the conformance + crash-injection + benchmark harness as the project's
  first `project-*` runtime skill once it stabilizes (SOW-0001 deferred-skill note).

## Regression Log

None yet.
