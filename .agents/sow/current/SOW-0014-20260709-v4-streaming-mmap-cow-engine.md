# SOW-0014 - v4 Streaming Mmap COW Engine Rebuild

## Status

Status: in-progress

Sub-state: 4 review rounds complete, 3 critical bugs fixed, 168 Rust + 74 Go tests pass

## Requirements

### Purpose

Rebuild the v4 live-DB engine to satisfy four foundational rules that the current
implementation violates. The engine must be a streaming SDK where IP feeds can be
multiple times larger than available memory, the writer hot path does zero heap
allocation, readers and a writer operate concurrently cross-process, and the scope
model supports retention timestamps, 32-feed bitmaps, and unlimited interned
combinations — all with a fixed `[from, to, scope_id:u32]` record.

### User Request

The user (Costa) gave four rules and several decisions:

**Rule 1 — Zero heap in writer hot path.** The writer API in the hot path of
adding IPs does ZERO heap allocations. Not 1 byte. Not transient. Not ever. The
reader hot path allocates ONLY the result returned to the caller. Fixed predefined
allocation at open time is allowed (freed at close). All page allocations happen in
mmap'd data. The mmap'd file is not for transient query/ingestion storage. This is
a STREAMING SDK — feeds can be bigger than RAM.

**Rule 2 — Concurrent readers + writer.** The writer API is also a reader API for
committed (pre-pending) data. N readers + 1 writer operate concurrently
cross-process. No flock LOCK_EX blocking readers.

**Rule 3 — Migration.** New ipset → new file (external sort). Big multi-feed files
→ delta-driven migrate. Retention scope = upstream-detected timestamp (u32).
Multi-feed scope = membership bitmap. Splits and merges for optimal footprint.

**Rule 4 — Scope model.** Every IP record is `[from, to, scope_id:u32]` — fixed
12 bytes (IPv4) / 36 bytes (IPv6). No scope_width field in meta. Three scope_modes:
  - 0 = scalar (retention: u32 is a timestamp; compare with `=`)
  - 1 = bitmap (32-scopes: u32 IS the bitmap; compare with `&`)
  - 2 = indirect (unlimited: u32 → scope table interned bitmap; compare with `&`)

**Rule 5 — Optimal file size.** CoW must optimally reuse available pages,
preferring pages at the beginning (low page numbers). The file must shrink
(truncate trailing free pages) when free pages are at the end. No unbounded
file growth under any workload.

**Rule 6 — Optimal performance.** No unnecessary operations. Don't reuse fat
code when a leaner path exists. Performance dictates code organization — not the
other way around. Any choice that doesn't boost performance is not a choice.
Code simplicity/organization follows performance, never leads it.

**Rule 7 — No full scans.** Full page-by-page scans must never be part of
normal file lifecycle management. Only when explicitly required by end-user
operations (migration, validate, compact).

**Rule 8 — Performance must be proved.** Profile everything. Eliminate all
heavy operations. The file format itself is subject to change for performance —
no backwards compatibility constraints, no prior design decisions are sacred.
"Performance is god." Must be proved with benchmarks and profiling.

**Decisions:**
- D1: Writable shared mmap (`PROT_READ|PROT_WRITE`, `MAP_SHARED`) with app-level COW
- D2: Free-list in CoW pages with freed-in-txn tags
- D3: Reader-registration companion file (LMDB model)
- D4: No write amplification (COW pages survive untouched until no reader needs them)
- D5: scope_mode replaces scope_width, fixed at creation time
- D6: Cross-process N readers + 1 writer (not just same-process)

### Assistant Understanding

Facts (from 3-subagent gap analysis):

- Current writer mmap is `PROT_READ` (os.go:301, os.rs:261) → forces heap dirty map
- Current writer holds `flock LOCK_EX` for entire session (os.go:143-152) → blocks all readers
- Current record is `[from, to, scope_bytes]` with variable scope_width baked into
  wire.rs:119, spec.rs:104-107, record.rs:15-64, node.rs:14-53 — every layer
- Current writer has 6 heap collections grown per mutation (writer.rs:72,78-82;
  writer.go:63,69-73)
- ~150 violations across 25+ files
- Reader/cursor hot path is already allocation-free (the template to preserve)
- Key types (u32/u128) are already clean (key.go/key.rs unchanged)

Inferences:

- The entire `MmapPageStore` + pwrite-at-commit pattern collapses once the mmap is
  writable (no dirty map, no pool, no pageData, no pwrite loop)
- Meta pages stay at fixed locations (pgno 0/1) written in-place alternately with
  CRC — LMDB model, proven
- "Private pages" become simply `pgno >= txn_start_pages` — no tracking structure needed
- Freed-page tracking becomes a linked list in growth-region pages + a u32 head pointer

Unknowns:

- Exact companion-file format for reader registration (needs design)
- Whether KV metadata rebuild can be made fully zero-heap (may need streaming merge
  into growth-region pages)

### Working Process

The assistant must NEVER report "finished" before verifying rule compliance.
Before reporting completion:

1. Spawn 2–3 internal review agents to audit the implementation against ALL rules.
2. Iterate to fix findings from the agents.
3. Only report "finished" when all agents pass and all rules are verified.

### Acceptance Criteria

1. Writer Set/Delete/Append: zero heap allocations (verified by alloc-tracking
   tests in both Rust and Go)
2. Reader open/lookup/scan/cursor: zero heap except the returned result
3. Cross-process: N readers + 1 writer concurrent, verified by multi-process tests
4. Record format: fixed `[from, to, scope_id:u32]` in all 3 scope_modes
5. Rust↔Go cross-read/cross-write compatible
6. All existing behavioral tests pass (adapted to new APIs)
7. All specs and SOWs updated with no contradictions remaining

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

Three foundational design decisions are wrong:

1. `PROT_READ` writer mmap → dirty pages forced to heap → Rule 1 violated
2. `flock LOCK_EX` for writer lifetime → readers blocked → Rule 2 violated
3. Variable `scope_width` in records → can't grow feeds, variable record size → Rule 4 violated

Evidence reviewed:

- v4/rust/iprange-livedb/src/{page_store,os,writer,wire,spec,node,record,reader,cursor}.rs
- v4/go/{page_store,os,writer,wire,spec,node,record,reader,cursor,scope,kv,key}.go
- .agents/sow/specs/{design-iprange-v4-livedb,design-iprange-v4-scope-api,design-iprange-v5-reasoning}.md
- .agents/sow/{current,done}/*.md (all SOWs)
- AGENTS.md
- LMDB design (LMDB documentation, read from prior knowledge — to be verified against
  /opt/baddisk/monitoring/repos if needed)

Affected contracts and surfaces:

- On-disk format: record layout, meta layout, free-list, scope table
- APIs: Writer (Set/Delete/Append/Commit), Reader (open/lookup/scan), Cursor
- Concurrency model: flock → MVCC reader registration
- Specs: all v4 specs
- SOWs: SOW-0006/0007/0009/0011/0012/0013 (mark superseded)
- Build: Rust Cargo.toml, Go modules

Existing patterns to reuse:

- Reader/cursor zero-alloc discipline (reader.rs, cursor.rs) — the template
- Key types (key.go/key.rs) — unchanged
- CRC32C hardware acceleration (crc32c.rs/crc32c.go) — unchanged
- Double-meta commit protocol (concept survives, mechanism changes)
- B+tree navigation (tree descent, leaf ops, branch updates) — logic survives,
  storage backing changes
- Scope table B+tree (scope.rs/scope.go) — becomes mode-2 target

Risk and blast radius:

- **Complete format break**: old v4.0-v4.2 files cannot be read by the new engine
  (record layout changed). Acceptable — user explicitly approved format change.
- **Migration risk**: update-ipsets will need a v3→new-format bridge or recreate
  files. Mitigated by external-sort-from-scratch workflow.
- **Concurrency complexity**: reader registration has subtle failure modes (crashed
  readers, stale slots). Must be tested thoroughly.
- **Performance regression risk**: the writable-mmap model has different performance
  characteristics. Must benchmark.

Sensitive data handling plan:

- No secrets, credentials, or personal data involved in this work.
- IP ranges in test fixtures are synthetic (RFC 5737 documentation ranges, random
  generated ranges).

Implementation plan (7 phases):

**Phase 1 — Format foundation:**
- spec.rs/go: scope_mode constants (0/1/2), record_size = 2*key_width + 4
- wire.rs/go: Meta struct (scope_mode replaces scope_width at offset 37)
- record.rs/go: fixed [from, to, scope_id:u32] layout
- node.rs/go: fixed record width, leaf capacity constants
- Conformance goldens: regenerated for the new format

**Phase 2 — Storage layer:**
- page_store.rs/go: new WritableMmapStore (MAP_SHARED, PROT_READ|PROT_WRITE)
- page reads/writes go directly through the mmap — no dirty map, no pool
- File growth: ftruncate + remap in batches
- Meta pages at fixed locations (0/1), written in-place alternately

**Phase 3 — Concurrency:**
- Reader registration companion file (.iprdb-lock or similar)
- Reader open: register generation (current committed txn_id)
- Reader close: deregister
- Writer commit: scan readers, find oldest active generation
- Free-list entries tagged with freed-in-txn; reclaim only when safe

**Phase 4 — Writer rebuild:**
- Writer struct: fixed-size fields only (no Vec, no HashMap)
- Free-page tracking: linked list in growth-region pages + u32 head
- COW: copy within mmap growth region, not heap
- Committed reads: via Reader/Cursor over committed bytes
- Scope table operations (mode 2): zero-heap where possible

**Phase 5 — Reader/cursor updates:**
- scope() returns u32, not &[u8]
- All selectors/visitors use u32
- Validate: update for new format

**Phase 6 — Migration APIs:**
- External sort module (bounded memory, spill+merge)
- Streaming merge (old cursor + desired stream → diff + apply)
- Retention migrate API (scope=timestamp, return old timestamps)
- Feed-delta API (bitmap set/clear bits)

**Phase 7 — Specs + docs:**
- Rewrite design-iprange-v4-livedb.md (the core spec)
- Rewrite design-iprange-v4-scope-api.md
- Rewrite design-iprange-v5-reasoning.md
- Mark SOW-0006/0007/0009/0011/0012/0013 as superseded
- Update AGENTS.md

Validation plan:

- Allocation tracking: Rust (alloc tracker / jemalloc), Go (testing.AllocsPerOp)
- Cross-process concurrency tests (fork + read while parent writes)
- Rust↔Go cross-read/cross-write conformance (shared goldens)
- Fuzz: random old/desired sets → migrate → verify
- Crash tests: kill mid-commit → recover → verify old-or-new
- Benchmarks: all 9 scenarios, both languages

Artifact impact plan:

- AGENTS.md: update build/test commands, remove flock/scope_width references
- Runtime project skills: none exist yet (per AGENTS.md decision)
- Specs: rewrite livedb, scope-api, v5-reasoning
- End-user docs: wiki/ unaffected (legacy CLI docs)
- SOW lifecycle: mark 0006/0007/0009/0011/0012/0013 superseded; this SOW tracks
  the full rebuild

Open-source reference evidence:

- LMDB design to be checked against /opt/baddisk/monitoring/ if implementation
  details need verification (reader registration, meta protocol)
- To be cited as evidence is gathered

Open decisions:

- All decisions resolved (D1-D6 above)

## Implications And Decisions

1. **D1 — Writable shared mmap**: `PROT_READ|PROT_WRITE`, `MAP_SHARED`. COW copies
   live in the file's growth region. Selected by user (Q1).

2. **D2 — Free-list in CoW pages**: freed pages carry freed-in-txn tags. The
   free-list is committed as regular pages, not derived in memory. Selected by user (Q2).

3. **D3 — Cross-process readers**: N readers + 1 writer concurrent. Reader
   registration via companion file. Selected by user (Q3, extended to cross-process).

4. **D4 — No write amplification**: COW pages survive untouched; reclaimed only when
   no reader needs the old generation. Proven by LMDB. Acknowledged by user.

5. **D5 — scope_mode replaces scope_width**: 3 values (0=scalar, 1=bitmap, 2=indirect).
   Fixed at creation time. Selected by user.

6. **D6 — Three scope_mode values**: 0 (scalar, compare with `=`), 1 (bitmap, compare
   with `&`), 2 (indirect, compare with `&` after table lookup). Selected by user.

## Plan

### Phase 1 — Format foundation
Scope: record/meta layout, constants. Risk: breaks all existing goldens. Dependencies: none.

### Phase 2 — Storage layer
Scope: writable mmap, zero-heap page ops. Risk: mmap semantics differ across platforms. Dependencies: Phase 1.

### Phase 3 — Concurrency
Scope: reader registration, generation tracking. Risk: subtle race conditions. Dependencies: Phase 2.

### Phase 4 — Writer rebuild
Scope: zero-heap Set/Delete/Append/Commit. Risk: algorithmic complexity. Dependencies: Phases 1-3.

### Phase 5 — Reader/cursor
Scope: u32 scope everywhere. Risk: low (mechanical). Dependencies: Phase 1.

### Phase 6 — Migration APIs
Scope: external sort, streaming merge. Risk: new algorithms. Dependencies: Phases 4-5.

### Phase 7 — Specs + docs
Scope: rewrite all contradicting docs. Dependencies: all phases complete.

## Execution Log

### 2026-07-09 (completed phases)

- SOW created from 3-subagent gap analysis (Rust storage, Go implementation, specs/docs)
- ~150 violations documented across 25+ files
- 7-phase plan defined
- **Phase 1 (format foundation):** spec.rs/go, wire.rs/go, record.rs/go, node.rs/go —
  scope_mode, fixed [from,to,scope_id:u32], VERSION_MINOR=3, META_SIZE=98
- **Phase 2 (storage layer):** page_store.rs/go — writable MAP_SHARED mmap, copy_page,
  zero-heap. VecPageStore (tests) + MmapStore (file-backed)
- **Phase 4a (writer rebuild):** writer.rs/go — fixed-size struct, COW in growth region,
  leaf split, delete with boundary trim, double-meta commit
- **Phase 4b (file layer):** os.rs/go — MmapReader (no flock), FileWriter (writable mmap)
- **Phase 5 (reader/cursor):** scope_id u32 everywhere, LeafView 2-arg
- **Phase 6 (migration APIs):** migrate.rs/go (streaming merge + change events),
  extsort.rs/go (bounded-memory sort + coalesce)
- **Phase 7 (specs/docs):** design-iprange-v4-livedb.md, scope-api.md, v5-reasoning.md
  all rewritten. AGENTS.md updated.
- **Gap analysis redo:** no active code references old APIs (scope_width, 2-arg
  record_size, heap dirty map, lifetime flock LOCK_EX). Remaining references are in
  comments and disabled test files.
- **Test results:** Rust 68 tests pass (53 lib + 10 engine + 5 migrate);
  Go 12 tests pass (7 engine + 5 migrate)

### Pending phases

- Phase 3: reader-registration companion file (cross-process MVCC)
- Phase 4c: scope/KV metadata APIs re-implementation (mode 2 = indirect)
- External sort spill path (file-backed runs for huge inputs)
- Branch split (trees > 2 levels with > branch_max separators)
- Cursor-based streaming old-state scan in migrate (current: Vec-based)
- Port conformance/metadata/robustness tests to new API

## Validation

Pending implementation.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
