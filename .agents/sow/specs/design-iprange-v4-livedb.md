# Design: iprange v4.3 Streaming Mmap COW Engine

## Status

**Current reality (v4.3).** Supersedes v4.0–v4.2 (scope_width, flock, heap dirty
pages). The record format and storage model are breaking changes — old files
cannot be read by the new engine.

## Purpose

A streaming SDK for huge IP-range datasets. The engine serves:
- **Retention files**: scope_id = timestamp; IPs expire by age.
- **Multi-feed files**: scope_id = membership bitmap or indirected bitmap.
- **Single-feed files**: one feed per file (external-sorted from unsorted input).
- **Delta-driven migration**: keep huge multi-feed files current without
  loading the whole dataset into memory.

## The Four Rules

**Rule 1 — Zero heap in the writer hot path.** `Set`/`Delete`/`Append`/`Commit`
do ZERO heap allocations. All page storage lives in the mmap'd file
(`MAP_SHARED` + `PROT_READ|PROT_WRITE`). No dirty-page map, no buffer pool,
no heap-grown collections. Fixed open-time allocations only.

**Rule 2 — Concurrent N readers + 1 writer.** The writer reads committed
(pre-pending) data via a reader/cursor. Readers do not block the writer.
Cross-process via a reader-registration companion file (Phase 3, pending).

**Rule 3 — Migration.** External sort (bounded memory) + streaming merge
(old cursor + desired stream → diff + apply). Emits change events.

**Rule 4 — Fixed scope_id record.** Every IP record is `[from: K, to: K,
scope_id: u32]` — 12 bytes (IPv4) / 36 bytes (IPv6). Three scope_modes:
- `0 = scalar` (retention: u32 is a timestamp; compare with `=`)
- `1 = bitmap` (32-scopes: u32 IS the bitmap; compare with `&`)
- `2 = indirect` (unlimited: u32 → scope table interned bitmap; compare with `&`)

## Record Format

```
IPv4:  [from: u32 LE] [to: u32 LE] [scope_id: u32 LE]   — 12 bytes
IPv6:  [from: u128 LE] [to: u128 LE] [scope_id: u32 LE] — 36 bytes
```

- Keyed by `from` in a sorted B+tree.
- `scope_id` is opaque to the engine — interpretation depends on `scope_mode`.
- Coalescing: adjacent ranges merge iff `scope_id` is equal (one u32 compare).
- Leaf capacity: `(4096 - 16) / record_size` → 340 (IPv4) / 113 (IPv6).

## Meta Page

Two meta pages at fixed locations (pgno 0 and pgno 1), written alternately
(double-meta atomic commit). Each is PAGE_SIZE bytes:

```
@0   page_header (16 bytes): page_type=1, reserved=0, entry_count=0, pgno, checksum
@16  magic "IPRANGE4" (8 bytes)
@24  version_major u16 (= 4)
@26  version_minor u16 (= 3)
@28  meta_size u16 (= 98)
@30  page_size u32 (= 4096)
@34  checksum_algo u8 (= 1 = CRC32C)
@35  flags u8 (bit0: 0=IPv4, 1=IPv6)
@36  key_width u8 (4 or 16)
@37  scope_mode u8 (0=scalar, 1=bitmap, 2=indirect)
@38  record_size u32 (MUST == 2*key_width + 4)
@42  created_unixtime u64 (static)
@50  root_pgno u32 (0 = empty tree)
@54  tree_height u32 (0 = empty; leaf level = 1)
@58  total_pages u64
@66  record_count u64 (unverified hint)
@74  txn_id u64 (monotonic generation; higher valid = active)
@82  updated_unixtime u64 (caller-supplied per commit)
@90  scope_table_root u32 (0 unless scope_mode=2)
@94  free_list_head u32 (0 = empty)
```

The active meta is the one with the higher valid `txn_id` (CRC must verify).

## Storage Model

**Writable MAP_SHARED mmap.** The writer maps the file with
`PROT_READ|PROT_WRITE, MAP_SHARED`. All page storage is in the mmap — zero heap.

**COW in the growth region.** Committed pages `[0, committed_pages)` are never
modified in-place (except meta pages 0/1). COW copies go into the growth region
`[committed_pages, total_pages)`. Within a transaction, a page COW'd once stays
at the same growth-region pgno for subsequent mutations (in-place after first
touch).

**Commit.** Finalize CRCs on all growth-region pages, write the new meta page
(the inactive one, alternating 0/1) in-place, `msync(MS_SYNC)`. A crash leaves
old-or-new, never torn.

**File growth.** `ftruncate` + remap in chunks (default 64 pages = 256KB,
doubling on each growth).

## Concurrency Model

**Readers do not block the writer.** The reader maps the committed range
read-only. The writer never modifies committed pages (COW to growth region).

**Writer-open serialization.** A brief `flock(LOCK_EX|LOCK_NB)` at open time
serializes multiple writers; the lock is released immediately. Readers take no
lock.

**Cross-process reader coordination (Phase 3, pending).** A reader-registration
companion file tracks each reader's `txn_id`. The writer reclaims freed pages
only when no active reader needs the old generation (LMDB model).

## Migration API

See `migrate.rs` / `migrate.go`.

```rust
pub fn migrate<K: IpKey>(
    writer: &mut Writer<K>,
    desired: &mut dyn DesiredStream<K>,
    opts: &MigrateOptions<K>,
) -> Result<MigrateCounters>
```

- Old state: scanned from the writer's committed reader.
- Desired state: sorted, disjoint stream (from external sort or direct source).
- Merge: parallel advance over interval boundaries.
- Output: change events (Added/Removed/Changed/Unchanged) + counters.
- For mostly-unchanged feeds: scans and does very few writes.

## External Sort

See `extsort.rs` / `extsort.go`.

```rust
pub fn ext_sort<K: IpKey>(
    records: Vec<DesiredRecord<K>>,
    config: &ExtSortConfig,
) -> Result<SortedStream<K>>
```

In-memory sort + coalesce for inputs that fit in `chunk_size`. Spill path
(file-backed runs + k-way merge) is pending for huge inputs.

## Cross-Language Compatibility

Rust and Go produce byte-identical files:
- Same record layout (`[from LE, to LE, scope_id LE]`).
- Same meta layout (offsets in spec.rs/spec.go).
- Same CRC32C (Castagnoli, hardware-accelerated in both).
- Same double-meta commit protocol.

## Page Types

```
1 = meta         (pgno 0/1)
2 = branch       (IP-tree internal node)
3 = leaf         (IP-tree data node)
4 = scope-table branch  (scope_mode=2 only)
5 = scope-table leaf    (scope_mode=2 only)
6 = KV branch           (per-scope metadata, pending)
7 = KV leaf             (per-scope metadata, pending)
8 = overflow            (large KV values, pending)
9 = txn-free            (transaction free-list, pending)
```

## What Was Removed (vs v4.0–v4.2)

- `scope_width` field → replaced by `scope_mode`
- Variable-width records → fixed `[from, to, scope_id:u32]`
- `flock LOCK_EX` for writer's lifetime → brief open-time lock only
- Heap dirty-page map + buffer pool → writable mmap, zero heap
- `pwrite`-at-commit → pages live in the mmap, commit is meta-flip + msync
- Derived in-memory allocator → persisted free-list (pending Phase 3)
- Per-scope KV metadata writes → pending re-implementation (Phase 4c)

## Open Items

- Phase 3: reader-registration companion file (cross-process MVCC)
- Phase 4c: scope/KV metadata APIs (mode 2 = indirect)
- External sort spill path (file-backed runs for huge inputs)
- Branch split (trees > 2 levels deep with > branch_max separators)
- Cursor-based streaming old-state scan in migrate (current: Vec-based)
