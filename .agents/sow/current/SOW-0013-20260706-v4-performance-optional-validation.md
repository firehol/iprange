# SOW-0013 - v4 performance: optional validation, hot-path optimization, and v5 reasoning

## Status

Status: in-progress

Sub-state: Phase 0 (static fixes) starting 2026-07-06. All user decisions recorded.
Builds on the shipped mmap writer (SOW-0011, paused for paperwork).

## Requirements

### Purpose

Achieve optimal performance for the v4 live-DB SDK by (1) making full validation
optional and trusted-by-default, (2) fixing every statically-identified hot-path
issue so a code review can say "optimal" before any benchmarking, (3) building a
benchmark harness to verify the fixes with data, and (4) producing a v5 format
reasoning document as the OUTPUT (not input) of this work.

### User Request

Costa's exact direction (2026-07-06):

> "Important: validation MUST be optional. If we can have an automated trigger is
> fine, but by default it should be an optional API call and the files should be
> trusted to be ok.
>
> Fix all issues you recognize, BEFORE we bench this. Static code review must say
> 'optimal' before we start benchmarking.
>
> [v5] this should be the result of this work. I don't want anything related to v5
> before we understand fully v4."

Decisions recorded:
- D1: New SOW-0013 (sibling to SOW-0005 which covers v3).
- D2: Parallel-track — build the harness AND land safe wins (hardware CRC32C,
  trusted-open) so the baseline is honest.
- D3: Implement hardware CRC32C now (runtime CPU detection, identical results).
- D4: v5 reasoning is the OUTPUT of this SOW, produced from measured data. No
  v5 work until v4 is fully understood.
- D5: Validation API shape = Option C:
  `Reader::open(bytes)` — TRUSTED (default): meta select + geometry only. Fast.
  `reader.validate()` — explicit full §9 validation, callable any time.
  No `open_checked` convenience method; callers compose `open()` + `validate()`.

### Assistant Understanding

Facts (measured from code on 2026-07-06):

- **Full validation runs on every `Reader::open`**: `reader.rs:78` calls
  `validate_tree()` which walks every reachable page; `reader.rs:440` runs
  `crc32c::verify_page` per page. `validate_scope_table()` (`reader.rs:79,87-115`)
  walks the scope table + every per-scope KV tree. For a 100k-page file, this is
  100k CRC computations + 100k page touches on every open.
- **Writer open walks the tree a second time**: `writer.rs:183` calls
  `derive_free_set()` which re-walks the reachable tree that `Reader::open` (called
  internally at `writer.rs:132,194`) just validated.
- **CRC32C is software-only, deliberately**: `crc32c.rs:7-9` states hardware
  acceleration (SSE4.2/ARM CRC) is "deliberately absent." The software path is
  1 byte → 1 table lookup + XOR per byte (`crc32c.rs:44-47`). Hardware does ~64
  bytes per instruction (~64× faster).
- **`set()` does 3-4 tree descents per record**: `writer.rs:251-271` —
  `delete_range` descent + 2× `lookup_covering` coalesce descents + `insert`
  descent. For ordered appends where predecessors are never adjacent, 3 of 4
  descents are wasted.
- **`read_leaf`/`read_branch` allocate Vecs per COW op**: `writer.rs:1094-1108`
  (`Vec<OwnedRecord>`), `writer.rs:1110-1117` (two Vecs for seps+children).
  Allocator pressure on every mutation.
- **`lookup_ge` allocates a `Vec<(u32,usize)>` stack**: `writer.rs:1473`.
  TREE_HEIGHT_MAX=32, so a `[u32; 32]` array would eliminate the allocation.
- **`take_dirty` allocates `FxHashSet` on every commit with freed pages**:
  `writer.rs:442`. For small freed sets, linear scan is faster.
- **Meta pages ARE CRC-verified during selection**: `reader.rs:603`
  (`classify` calls `crc32c::verify_page`). This is 2 pages (cheap, crash-safety).
- **`page_bytes` panics on out-of-range pgno**: `reader.rs:341` does unchecked
  slice indexing. Acceptable for trusted files; `validate()` covers untrusted.

Inferences:

- The dominant cost for open-close workloads (scenarios 7, 8) is validation +
  software CRC. Making validation optional + hardware CRC should cut open cost by
  ~90%+ for trusted daemon files.
- The dominant cost for write workloads (scenarios 2, 3, 4) is per-set descent
  overhead + Vec allocations. An `append()` fast-path + scratch buffers should
  help significantly.
- Read scenarios (1, 5, 6) are already lean post-open (mmap-slice reads, binary
  search within leaf). The win is making open free.

Unknowns (to be resolved by Phase 1 measurement):

- Whether `partition_idx` (branchy binary search) is a cache/branch-miss hot spot
  at scale, or negligible vs page-descent cost.
- Whether the `append()` fast-path is actually used by real consumers (update-ipsets
  appends; Netdata netflow may not).
- Rust vs Go vs C standing (the 5-10% band target).

### Acceptance Criteria

- **Validation is optional**: `Reader::open` is trusted by default (meta CRC +
  geometry only). `Reader::validate()` provides full §9 validation on demand.
  Both Rust and Go.
- **Hardware CRC32C**: SSE4.2 (x86_64) + ARM CRC (aarch64), runtime-detected,
  identical results to the software path. Fallback to software on unsupported CPUs.
- **All 13 static fixes landed** (Rust + Go), tests green, clippy/vet clean.
- **Static code review says "optimal"**: no known hot-path waste remains.
- **Benchmark harness**: criterion (Rust) + `testing.B` (Go), 9 scenarios, 3
  scales, committed and reproducible.
- **v5 reasoning document**: produced from measured Phase 1 data, committed as a
  spec under `.agents/sow/specs/`.
- **No on-disk format change**: v4 files remain byte-compatible.

## Analysis

Sources checked:

- `v4/rust/iprange-livedb/src/{reader,writer,crc32c,os,page_store}.rs`
- `v4/go/{reader,writer,crc32c,os,page_store}.go` (Go counterparts)
- `.agents/sow/specs/design-iprange-v4-livedb.md` (§9 validation, §6.3 commit)
- `.agents/sow/pending/SOW-0005-*` (v3 perf SOW — methodology reference)
- `.agents/sow/current/SOW-0011-*` (mmap writer — shipped, paused)

Current state:

- v4 writer/reader are correct and fully validated on every open. Performance is
  unmeasured. CRC32C is software-only. Every open pays O(N_pages) validation.
  Every `set` pays 3-4 tree descents + Vec allocations.

Risks:

- Making validation optional could let corrupt files through silently if callers
  forget to validate untrusted input. Mitigation: document the contract clearly;
  the daemon's own files are trusted (just committed under LOCK_EX).
- Hardware CRC32C via inline asm / intrinsics is platform-specific. Mitigation:
  runtime CPU detection + software fallback; gated by `target_arch`.
- `append()` fast-path must not bypass COW invariants. Mitigation: reuse the same
  `insert()` COW machinery, just skip the overlap/coalesce descents.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The v4 SDK was built correctness-first with always-on validation and a
  deliberately simple CRC. Performance was never measured. The result is a correct
  but unoptimized baseline whose dominant costs (per-open validation, software CRC,
  per-set multi-descent) are structurally identifiable from the code.

Evidence reviewed:

- `reader.rs:34-80` (open → validate_tree → validate_scope_table)
- `reader.rs:384-421` (validate_tree walks all reachable pages)
- `reader.rs:426-470` (validate_node: CRC per page + sortedness + tail-zero)
- `reader.rs:599-650` (classify: CRC on meta pages)
- `reader.rs:337-345` (page_bytes: unchecked slice, trusts validated tree)
- `writer.rs:130-184` (open_image: calls Reader::open + derive_free_set)
- `writer.rs:240-272` (set: 3-4 descents)
- `writer.rs:1094-1117` (read_leaf/read_branch: Vec allocations)
- `writer.rs:1469-1518` (lookup_ge: Vec stack)
- `writer.rs:435-448` (take_dirty: FxHashSet allocation)
- `crc32c.rs:7-9,42-48` (software CRC, 1 byte/iteration)

Affected contracts and surfaces:

- `Reader::open` — behavior changes (trusted, no tree walk). Breaking for callers
  that relied on open-time corruption rejection. Add `Reader::validate()`.
- `Writer::open_image` / `open_with_store` — calls `Reader::open`; decide default
  validation behavior for the writer (writer reads untrusted committed bytes).
- `MmapReader::reader()` — calls `Reader::open`.
- `crc32c.rs` / `crc32c.go` — new hardware path, same public API.
- Tests — many tests rely on `Reader::open` rejecting corrupt files; update to
  call `validate()` where they test rejection.
- Spec `design-iprange-v4-livedb.md` §9 — update: validation is optional.
- End-user docs — the trust contract must be documented.

Existing patterns to reuse:

- The §9 validation logic stays exactly as-is — it becomes `Reader::validate()`.
  No rewrite, just relocation.
- `select_active_meta` + `classify` already CRC-verify meta pages — keep this in
  `open` (crash-safety, 2 pages only).
- Rust `std::arch::x86_64` intrinsics + `is_x86_feature_detected!` for runtime
  CRC detection; `std::arch::aarch64` for ARM. Standard, well-documented.
- Go: `crypto/internal/edwards25519/field` and similar use `cpu.X86.HasSSE42`
  from `internal/cpu`; mirror that pattern with `golang.org/x/sys/cpu`.

Risk and blast radius:

- **Regression risk**: tests that assert corruption rejection via `open` must
  switch to `validate()`. Medium effort, zero functional change.
- **Performance risk**: low — all fixes are additive or relocate existing logic.
- **Security risk**: callers who don't validate untrusted files could get wrong
  answers or panics. Mitigation: clear docstrings; daemon files are trusted.
- **Compatibility**: no on-disk format change. v4 files read identically.

Sensitive data handling plan:

- Synthetic workloads only for benchmarks. No customer data. Standard guardrails.

Implementation plan (Phase 0 — ordered, before any benchmark):

1. **CRC32C hardware acceleration** (Rust + Go). Standalone, no API change.
   Files: `crc32c.rs`, `crc32c.go`. Runtime detect, identical results.
2. **Optional validation** (Rust + Go). `Reader::open` → trusted; extract full
   walk to `Reader::validate()`. Files: `reader.rs`, `reader.go`. Update all tests.
3. **Writer open: skip double walk**. If writer uses trusted `open`, the
   `derive_free_set` walk is the only walk (it's structurally necessary).
   If the caller validated, avoid re-walking.
4. **Writer scratch buffers**. Reuse `Vec<OwnedRecord>`, `Vec<K>`, `Vec<u32>`
   across COW ops. Files: `writer.rs`, `writer.go`.
5. **`lookup_ge` zero-alloc stack**. `[u32; TREE_HEIGHT_MAX]` array.
6. **`take_dirty` small-set threshold**. Linear scan for small freed sets.
7. **`append()` fast-path**. New method: ordered disjoint insert, 1 descent.
8. **Minor fixes**: Go pool trim nil-out, zeroPage doc, u32 overflow guard.
9. **Static review pass**: all tests green, clippy/vet clean, code review "optimal".

Phase 1 (benchmark harness): criterion + testing.B, 9 scenarios, 3 scales.

Phase 3 (v5 reasoning): from Phase 1 measured data.

Validation plan:

- All existing tests pass (111 Rust + Go suite) after updating corruption-rejection
  tests to call `validate()`.
- New tests: `validate()` rejects what `open` used to reject; `open` is fast.
- CRC32C: hardware and software paths produce identical results (property test).
- `cargo clippy --all-targets --all-features -- -D warnings` clean.
- `go vet ./...` clean.
- External reviewer pass before SOW completion.

Artifact impact plan:

- AGENTS.md: add benchmark commands to "Project-specific commands".
- Runtime project skills: candidate — the benchmark+profiling workflow.
- Specs: update `design-iprange-v4-livedb.md` §9 (validation optional). Add v5
  reasoning spec at completion.
- End-user/operator docs: document the trust contract (open vs validate).
- End-user/operator skills: unaffected (internal perf work).
- SOW lifecycle: SOW-0013 in current/; complete to done/ when v5 doc ships.

Open-source reference evidence:

- Rust CRC32C hardware: `rust-lang/std` `std::arch::x86_64::_mm_crc32_u8` /
  `_mm_crc32_u64`. Standard library, stable since Rust 1.27.
- Go CRC32C hardware: `golang.org/x/sys/cpu` (`cpu.X86.HasSSE42`,
  `cpu.ARM64.HasCRC32`); pattern used by Go stdlib `hash/crc32`.

Open decisions:

- All resolved (D1-D5 above). No blocking decisions remain.

## Implications And Decisions

1. **D1 — SOW scope**: New SOW-0013 (sibling to SOW-0005). **Selected: A.**
   Reasoning: v3 (flat array) and v4 (B+tree) have different hot paths and
   workloads. Separate SOWs give cleaner data.

2. **D2 — Sequencing**: Parallel-track harness + safe wins. **Selected: B.**
   Reasoning: hardware CRC32C and trusted-open are low-risk, additive, and
   explicitly deferred in the code. Building the harness takes time; landing these
   alongside makes the baseline honest.

3. **D3 — Hardware CRC32C**: Implement now. **Selected: A.**
   Reasoning: the code says it's deliberately deferred; the polynomial is
   identical; it's a well-understood 1-file change per language.

4. **D4 — v5 reasoning**: Output, not input. **Selected: defer to Phase 3.**
   Reasoning: no v5 design until Phase 1 data confirms which hot paths matter.

5. **D5 — Validation API shape**: Option C. **Selected: C.**
   - `Reader::open(bytes)` — trusted (meta CRC + geometry only). Fast.
   - `Reader::validate(&self)` — full §9 validation, callable any time.
   - No `open_checked`; callers compose `open()` + `validate()`.
   Reasoning: most flexible — validate any time, not just at open. Mirrors the
   file-layer/format-layer separation already in `MmapReader`.

## Plan

### Phase 0 — Static fixes (before any benchmark)

1. CRC32C hardware acceleration (Rust + Go) — standalone.
2. Optional validation: `open` trusted, `validate()` extracted (Rust + Go).
3. Writer open: avoid double walk where possible.
4. Writer scratch buffers (read_leaf/read_branch reuse).
5. `lookup_ge` zero-alloc stack.
6. `take_dirty` small-set linear scan.
7. `append()` fast-path.
8. Minor fixes (Go pool trim, zeroPage, u32 guard).
9. Static review pass.

### Phase 1 — Benchmark harness

- criterion (Rust) + testing.B (Go), 9 scenarios × 3 scales × IPv4/IPv6.
- perf stat + cachegrind on hottest scenarios.
- C ceiling (legacy `iprange`) for the 5-10% band.

### Phase 3 — v5 reasoning (output)

- From Phase 1 data: which format changes would yield the most.
- Committed as `.agents/sow/specs/design-iprange-v5-reasoning.md`.

## Execution Log

### 2026-07-06

- SOW created. All decisions recorded. Phase 0 starting.

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- SOW-0011 (mmap writer): paused for paperwork; its implementation is shipped and
  this SOW builds on it. Should be closed independently.
- If Phase 1 reveals an accelerator is warranted (e.g. branchless Eytzinger,
  DIR-24-8), spin out as a separate SOW.

## Regression Log

None yet.

## Execution Log (updated)

### 2026-07-06 — Phase 1 initial benchmark results

Hardware: Costa's workstation (RTX 5090, x86_64, SSE4.2 available). Criterion
(Rust, default config adapted: sample-size=10-30, measurement-time=2-3s) and
`testing.B` (Go, benchtime=1s). All numbers are wall-clock per iteration.

#### Rust vs Go — 1M records, IPv4, scope_width=1

| Scenario | Rust | Go | Go/Rust | Notes |
|----------|------|-----|---------|-------|
| 1_scan | 668 µs | 7.17 ms | 10.7× | Rust mmap traversal much faster |
| 2_append (100k) | 169 ms | 41.6 ms | 0.24× | **Go 4× faster** — Rust Vec alloc bottleneck |
| 3_set_random (100k) | 628 ms | 1.43 s | 2.3× | Rust faster on overlapping writes |
| 5_lookup_hit (1M keys) | 54.0 ms | 118.4 ms | 2.2× | 54ns vs 118ns per lookup |
| 7_open_read (trusted) | 3.42 µs | 2.08 µs | 0.61× | **Go faster** — stdlib CRC dispatch |
| 7b_open_validate | 6.91 ms | 14.85 ms | 2.2× | Rust hardware CRC faster |
| 7_open_read_file | — | 11.5 µs | — | Go MmapReader (flock + mmap + §10) |

#### The validation optimization impact (Rust, 1M records)

| Mode | Time | Ratio |
|------|------|-------|
| Trusted open | 3.42 µs | **baseline** |
| Full validate | 6.91 ms | **2000× slower** |

Trusted open is O(1) — 3.4µs at 10k, 100k, and 1M records. Validate scales
linearly: ~7ns/page (dominated by CRC + tree walk).

#### append() vs set() impact (Rust, 100k records)

| Method | Time | Per-record |
|--------|------|------------|
| append (ordered) | 169 ms | 1.69 µs |
| set (random) | 628 ms | 6.28 µs |
| **Speedup** | | **3.7×** |

#### Key findings for v5 reasoning

1. **Open is solved**: trusted open is O(1) at 3µs. Not a v5 concern.
2. **Scan is fast**: 0.67ns/record (Rust). Not a v5 concern.
3. **Lookup is OK**: 54ns/lookup (Rust, 1M records). Tree descent + binary search. Could be improved with a first-level DIR-24-8 table for IPv4.
4. **Write is the bottleneck**: 1.69µs/append, dominated by COW page allocation + per-record Vec clones. The deferred Fix 5-6 (scratch buffers) should help significantly.
5. **Rust write is SLOWER than Go** (4× on append): likely allocator pressure from `Vec<OwnedRecord>` per COW op, each cloning 1-byte scope Vecs. Needs profiling.
6. **Validate is expensive but optional**: 7ms for 1M records. The automated trigger (periodic validate on the daemon's own files) is affordable.

#### What the data says about v5

The hot paths that a v5 format change should target:
- **Write amplification**: COW copies entire 4KB pages per mutation. A log-structured append or an in-place (non-COW) design would help.
- **Per-write allocation**: Vec clones per record during COW. A format with fixed-size records (no per-record scope clone) would eliminate this.
- **IPv4 lookup**: 54ns is OK but a DIR-24-8 first level would cut tree depth by 1-2 levels.

What v5 should NOT change:
- The double-meta atomic commit (crash safety is correct).
- The page format (checksums work, hardware CRC is fast).
- The B+tree structure for IPv6 (good enough).

### 2026-07-06 — Deferred CRC: the single biggest write-path win

Profile finding (perf record on append/100k):
- 95.36% of append time was `crc32c::update_sse42` — CRC32C computed on EVERY
  write_leaf/write_branch call during mutation.
- Root cause: finalize_checksum(page) was called inside every write_* function,
  computing CRC over 4096 bytes per page write. For 100k COW appends, that's
  ~100k CRC computations on intermediate pages — most of which are freed as
  orphans within the same txn.

Fix (Costa's insight: "crc should be computed on commit, not on every update"):
- Removed finalize_checksum from ALL write_* functions (writer.rs, scope.rs,
  kv.rs — 18 call sites in Rust; same in Go).
- Added finalize_dirty_checksums(): at commit time, iterates self.dirty and
  finalizes CRC ONLY for pages not in freed_this_txn (orphan COW copies are
  skipped — their CRC is never validated by the reader).
- The in-memory commit() calls it between rebuild_commit_state and
  finish_commit_meta. The OS commit_durable() calls it before take_dirty,
  then finalizes the full take_dirty set (including grown-region freed pages
  needed for sparse-hole avoidance).
- create() finalizes CRC for both initial metas.
- Forge test helpers (forgeHeight3KVFile, forgeHeight3ScopeFile) call
  finalizeAllChecksums() after writing pages directly.

Before/after (100k records, Rust):
| Scenario   | Before    | After     | Speedup |
|------------|-----------|-----------|---------|
| append     | 169 ms    | 5.15 ms   | 33×     |
| set_random | 628 ms    | 244 ms    | 2.6×    |

Before/after (100k records, Go):
| Scenario   | Before    | After     | Speedup |
|------------|-----------|-----------|---------|
| append     | 41.6 ms   | 14.6 ms   | 2.8×    |
| set_random | 1.43 s    | 1.34 s    | 1.1×    |

Rust append at scale:
| Records | Time     | Per-record |
|---------|----------|------------|
| 10k     | 486 µs   | 49 ns      |
| 100k    | 5.15 ms  | 52 ns      |
| 1M      | 68.0 ms  | 68 ns      |

Rust is now FASTER than Go on append (5.15ms vs 14.6ms at 100k) — the
allocator pressure hypothesis was wrong; CRC was the real bottleneck.

Tests: 221 Rust (107 lib + 1 conformance + 2 metadata + 111 robustness) + all
Go tests pass. Clippy + go vet clean.
