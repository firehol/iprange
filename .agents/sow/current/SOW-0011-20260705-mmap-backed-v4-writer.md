# SOW-0011 - mmap-backed v4 writer

## Status

Status: in-progress

Sub-state: Implementation repaired after round-19 review fixes. Both `create` and
`open` paths are now mmap-backed after the 8KB bootstrap image. The commit path no
longer returns ordinary remap/truncate errors after the Barrier 2 commit point.
Validation passes; SOW remains in progress until the normal close/review process.

## Requirements

### Purpose

Restore the fundamental v4 live-DB guarantee: the file-backed writer must operate on
an on-disk/mmap-backed database without loading the whole database into heap memory.
The writer may scan/validate reachable pages and derive allocator state, but it must
not require the full file to fit in RAM.

### User Request

The user requested an immediate corrective SOW to convert the v4 writer to work on
mmap-backed files, after pointing out that a whole-file in-memory writer defeats the
core reason for the v4 file format.

### Assistant Understanding

Facts:

- The locked v4 spec says the dataset across feeds exceeds RAM and mmap is mandatory.
- The locked v4 spec says the file may exceed RAM and read access should keep only hot
  pages resident.
- SOW-0007 says v4 fixes update-ipsets retention by avoiding a full rebuild and by
  rewriting only `O(log n + k)` pages.
- SOW-0007 also sets a hard implementation bar: no redundant reads, minimal I/O, and
  measured allocation/I/O evidence.
- The current Rust `FileWriter::open` allocates a buffer of `meta.len()` and reads the
  whole file into it before `Writer::open_image`.
- The current Go `openFileWriter` allocates a buffer of `st.Size` and reads the whole
  file into it before `openImage`.

Inferences:

- The writer must still validate the reachable tree and derive a free-page set at
  open; that `O(pages)` scan is part of the v4 design.
- Copying the full file into one heap image is not required by the format. It is an
  implementation shortcut from the current in-memory writer core.
- A correct replacement must preserve the existing COW + double-meta crash contract:
  committed pages must not be overwritten in place before the commit point.

Unknowns:

- The exact lowest-risk implementation shape must be finalized during design review:
  either a mmap-backed page source with private dirty-page buffers and `pwrite` commit,
  or a stricter shared-mmap dirty-page path with equivalent durability proof.

### Acceptance Criteria

- Rust `FileWriter::open` no longer allocates `Vec<u8>` proportional to the file size
  or reads the whole file into heap memory.
- Go `openFileWriter` no longer allocates `[]byte` proportional to the file size or
  reads the whole file into heap memory.
- `FileWriter::create` / `createFileWriter` writes the initial 2-page file (8KB heap
  buffer), fsyncs, then reopens through the mmap-backed `open` path. The writer never
  holds the full DB image in heap memory — all subsequent growth and commits go through
  COW + pwrite on the mmap-backed store.
- File-backed writer open validates the committed tree and derives the free-page set
  by walking reachable pages through the mmap. The free-set derivation allocates a
  `Vec<bool>` proportional to `total_pages` (transient, O(pages) memory) — this is
  the same as the current Vec-backed writer and is accepted per the v4 spec (§7:
  "The walk is O(pages) once per writer open"). No whole-file data copy occurs.
- COW semantics remain intact: committed pages are not modified in place before the
  inactive meta is durably flipped.
- Commit still writes only dirty/new/COW pages plus the inactive meta page, with the
  existing two-barrier crash-safety behavior.
- Existing Rust and Go v4 conformance, robustness, cross-read, OS, metadata, and export
  tests pass.
- New tests prove the file-backed writer does not depend on whole-file heap residency.
  At minimum: instrumentation or a memory-limited integration path that would fail the
  old implementation and pass the mmap-backed writer.
- Specs and SOW outcomes are updated so the durable contract no longer contradicts the
  implementation.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-v4-livedb.md`
- `.agents/sow/done/SOW-0006-20260622-v4-livedb-format-design.md`
- `.agents/sow/done/SOW-0007-20260622-v4-livedb-implementation.md`
- `v4/rust/iprange-livedb/src/os.rs`
- `v4/rust/iprange-livedb/src/writer.rs`
- `v4/go/os.go`
- `v4/go/writer.go`

Current state:

- The format/spec direction is file-backed and mmap-oriented.
- The reader path is mmap-backed.
- The writer path is not mmap-backed. It reads the entire file into a private
  full-image buffer and mutates that buffer.
- The current implementation writes dirty pages at commit, but this does not remove
  the full-file heap residency imposed at writer-open.

Risks:

- This is a core writer refactor touching allocator, validation, COW mutation, commit,
  and OS-layer behavior in both Rust and Go.
- A naive mmap writer can break crash safety if it overwrites pages reachable from the
  currently active meta.
- A naive sparse-growth path can create holes that the mmap reader correctly rejects.
- A naive remap-after-growth path can leave an open writer with stale mappings or
  invalid page references.
- Rust and Go may diverge unless the shared conformance and cross-read suite is kept as
  the source of truth.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- What is happening: the file-backed writer copies the entire v4 file into heap memory
  before mutation.
- Why it is happening: the reusable writer core was implemented as an in-memory file
  image (`image: Vec<u8>` in Rust, `image []byte` in Go), and the OS writer wraps that
  core by first reading the whole file.
- Why this is wrong: the v4 work exists because the dataset may exceed RAM and because
  a file-backed mutable index should avoid full rebuild/full-file residency. The current
  writer only avoids full-file rewrite at commit; it does not avoid full-file heap
  residency at open.

Evidence reviewed:

- `design-iprange-v4-livedb.md`: v4 exists because existing models fail at scale; dataset
  exceeds RAM; mmap is mandatory; only hot pages should be resident; mutation should
  touch `O(log n + k)` pages.
- `design-iprange-v4-livedb.md`: writer-open must validate and derive the free set;
  that justifies an `O(pages)` scan, not a full heap copy.
- `SOW-0007`: hard performance requirement says no redundant reads and measured minimal
  I/O/allocation evidence.
- `SOW-0007`: shipped implementation records `pread the image -> Writer::open_image`,
  which is the current defect.
- Rust and Go OS code confirm whole-file heap allocation on writer-open.

Affected contracts and surfaces:

- Rust API and internals: `v4/rust/iprange-livedb/src/os.rs`, `writer.rs`, and likely
  reader/page access helpers.
- Go API and internals: `v4/go/os.go`, `writer.go`, and likely reader/page access
  helpers.
- v4 locked spec: updated to the corrected mmap-backed writer contract, without
  weakening the pwrite/fsync crash contract.
- Conformance corpus and OS tests.
- SOW-0007 outcome and lessons as historical evidence; this SOW corrects the defect.

Existing patterns to reuse:

- Existing `MmapReader` hardening: `O_NOFOLLOW`, `O_CLOEXEC`, `fstat`, sparse-hole
  rejection, remap/TOCTOU checks, last-byte probe.
- Existing `Reader::open` / `Open` validation over borrowed bytes.
- Existing dirty-page set and two-fsync commit protocol.
- Existing robustness/fuzz tests and cross-read corpus.
- Existing mirrored Go/Rust implementation discipline: every behavior change must land
  symmetrically.

Risk and blast radius:

- Correctness risk: COW pages, free-page reuse, root changes, and metadata pages must
  still produce old-or-new crash outcomes.
- Memory risk: the fix must not replace one full-file buffer with another hidden full
  page cache owned by the process.
- Performance risk: page abstraction must not slow hot lookup/mutation paths
  unnecessarily; any overhead must be measured.
- Compatibility risk: existing files must remain readable/writable; on-disk bytes must
  stay compatible unless the spec explicitly changes.
- Platform risk: mmap behavior differs by OS and filesystem. The current v4 OS layer is
  Unix-focused; Windows remains future work.

Sensitive data handling plan:

- This work uses synthetic v4 fixtures and local test files only. No secrets,
  credentials, personal data, customer data, non-private customer-identifying IPs, or
  operational FireHOL infrastructure details are needed. If real production artifacts are
  later needed for scale testing, they must be referenced only through sanitized metrics
  and not copied into durable artifacts.

Implementation plan:

### Architecture: Page Store Abstraction

Replace `Writer`'s whole-file `image: Vec<u8>` / `image []byte` with a **page store
trait/interface** that provides page-level read/write/allocate operations. Two
implementations:

1. **VecPageStore** — current in-memory behavior (wraps `Vec<u8>` / `[]byte`). Used by
   tests and the pure-API path (`Writer::create`, `openImage` from a buffer).
2. **MmapPageStore** — new mmap-backed behavior. Reads committed pages from a read-only
   mmap; stores dirty/new pages in a private `HashMap<u32, Vec<u8>>` / `map[uint32][]byte`.

The Writer's COW logic (insert/delete/split/merge, ~2000 lines) calls `page()`,
`write_page()`, `alloc_page()`, `free_page()` through the store — these are the only
methods that change. The COW algorithms themselves stay identical.

### Page Store — Dynamic Dispatch (Not Generics)

**Decision: Use `Box<dyn PageStore>` inside Writer instead of a generic parameter.**

A generic `Writer<K, S: PageStore = VecPageStore>` would cascade to `FileWriter<K, S>`,
breaking every `FileWriter::<Ipv4Key>::open(...)` call site (26+ in tests alone). The
`FileWriter` struct stores `Writer<K>` — with generics, `FileWriter::open` (which needs
MmapPageStore) and `FileWriter::create` (which needs VecPageStore) would require
different `FileWriter` types. This is unworkable.

Instead, Writer stores `Box<dyn PageStore>`:

```rust
pub struct Writer<K: IpKey> {
    store: Box<dyn PageStore>,
    // ... other fields unchanged
}
```

This avoids the generic cascade entirely. The cost is one heap allocation for the
`Box<dyn PageStore>` and thin dynamic dispatch on every `page()`/`write_page()` call.
The dispatch overhead (~3–5ns per call) is negligible compared to the 4KB memcpy +
CRC32C checksum work in every page write.

```rust
pub(crate) trait PageStore: Send + Sync {
    /// Read a page. Checks the dirty map first (hit → return dirty data),
    /// then falls back to the committed source (mmap or Vec). The Writer
    /// core reads both committed pages (initial COW descent) and dirty pages
    /// (after COW: rebalance, lookup_covering, contains_from, lookup_ge,
    /// descend_to_leaf, scan_node all traverse from root_pgno which may
    /// point at dirty pages after the first COW in a txn).
    fn page(&self, pgno: u32) -> &[u8];

    // NOTE: committed_page() fast path was considered but removed.
    // After the first COW in a txn, root_pgno may point at a dirty page,
    // and subsequent cow_insert/cow_delete/rebalance calls read through
    // it. The safety window for a dirty-map-skip is only the very first
    // operation in a txn — too narrow to justify the complexity and risk
    // of silent corruption if misused. Always use page() which checks the
    // dirty map first (~2ns FxHashMap lookup).

    /// Get a mutable reference to a page's storage. For VecPageStore: returns
    /// a slice into the Vec (zero-copy). For MmapPageStore: returns a slice
    /// into a recycled dirty buffer from the dirty map (Entry API, ~2ns
    /// FxHashMap lookup). Used by write_leaf/write_branch/write_meta to
    /// write directly into the destination, avoiding the stack buffer + memcpy.
    fn write_page_mut(&mut self, pgno: u32) -> &mut [u8];

    /// Store a dirty page. The data is copied into the store's dirty map
    /// (for MmapPageStore with private buffers) or written into the Vec
    /// (for VecPageStore). Does NOT touch Writer.dirty — that is the Writer's
    /// responsibility.
    fn write_page(&mut self, pgno: u32, data: &[u8]);

    /// Extend the store by one page (called only when the Writer's free list
    /// is empty). Returns the new page number. The store's internal counter
    /// is incremented; no actual memory allocation is required for MmapPageStore.
    fn alloc_page(&mut self) -> u32;

    /// Total logical pages in the store (committed + allocated this txn).
    /// Returns u64 to match all existing callers (kv::get, kv::list,
    /// kv::collect_pages, Meta::total_pages). The format enforces
    /// total_pages < 2^32, so the return value is always u32-storable.
    fn total_pages(&self) -> u64;

    /// Truncate the store to the given number of pages.
    fn truncate(&mut self, pages: u32);

    /// Return the committed bytes as a contiguous slice.
    /// For VecPageStore: returns the Vec. For MmapPageStore: returns the mmap
    /// (sized to exactly `total_pages * PAGE_SIZE` — no trailing garbage).
    /// Used by external functions (Reader::open, scope::collect_pages,
    /// kv::collect_pages, kv::get, kv::list) that need a contiguous byte slice
    /// of the committed state.
    fn committed_bytes(&self) -> &[u8];

    /// Return the bytes for a specific dirty page.
    /// Used by the OS layer at commit time to obtain page data for pwrite.
    /// For VecPageStore: returns the Vec slice directly. For MmapPageStore:
    /// returns from dirty map (or panics if not dirty — the OS layer only
    /// calls this for pages in the dirty set).
    fn page_data(&self, pgno: u32) -> &[u8];

    /// Clear all dirty pages. Called after a successful commit, after the
    /// OS layer has finished reading dirty page data for pwrite. The next
    /// txn starts with an empty dirty map; committed pages are read from
    /// the (remapped) mmap or Vec. Recycled buffers are kept alive for
    /// reuse in the next txn (zero allocation in steady state).
    fn clear_dirty(&mut self);

    /// Extract the inner Vec (VecPageStore only). Returns None for MmapPageStore.
    /// Used by Writer::into_image() to avoid a copy when the writer is Vec-backed.
    fn into_vec(self: Box<Self>) -> Option<Vec<u8>>;

    /// Borrow the inner Vec bytes (VecPageStore only). Returns None for MmapPageStore
    /// (no Vec to borrow — image() falls back to committed_bytes()).
    /// Used by Writer::image() to return the Vec directly when available.
    fn as_bytes(&self) -> Option<&[u8]>;

    /// Remap the mmap to a new size (MmapPageStore only). VecPageStore no-ops.
    /// Called after the file has been extended via ftruncate/fallocate.
    /// On failure, the old mapping is preserved (the mmap syscall does not
    /// destroy the old mapping if the new one fails). The caller must poison
    /// the writer — the on-disk file is valid but the in-memory state cannot
    /// continue safely.
    /// Takes a raw fd because MmapPageStore does not own the file descriptor
    /// (FileWriter does). The fd is needed for the new mmap() call on
    /// platforms without mremap (macOS, FreeBSD).
    fn remap(&mut self, fd: RawFd, new_size: u64) -> Result<()>;

    /// Release resources (mmap munmap for MmapPageStore). VecPageStore no-ops.
    /// Called from FileWriter::close() / Drop to prevent mmap leaks.
    /// Must be idempotent (safe to call multiple times). In Rust, MmapPageStore
    /// stores the Mmap as `Option<Mmap>` — `close()` calls `self.mmap.take()`
    /// and drops it (memmap2::Mmap unmaps on drop). A second `close()` is a
    /// no-op (Option is already None). No `ManuallyDrop` or manual `munmap`
    /// needed — memmap2 handles this. In Go, a `closed` flag prevents
    /// double-unmap.
    fn close(&mut self);
}
```

Writer stores `Box<dyn PageStore>`. Existing code using `Writer<K>` continues to work
unchanged. `FileWriter` stays `FileWriter<K>` — no generic cascade.

### MmapPageStore Design

- **Construction**: takes a read-only `Mmap` + committed page count (`meta.total_pages`).
  `logical_pages` is initialized to `meta.total_pages` (the authoritative source, not
  `mmap.len() / PAGE_SIZE`). Dirty map is empty. A recycled buffer pool
  (`Vec<Vec<u8>>`) is also empty.
- **`page(pgno)`**: checks the dirty map first (FxHashMap lookup, ~2ns). If found,
  returns the dirty data. Otherwise checks `pgno < committed_pages` (the mmap's page
  count — NOT `logical_pages` which includes pages allocated this txn) and returns
  the mmap slice. Otherwise returns a static zero page (for pages allocated but not
  yet written this txn, or pages beyond the committed file size). The Writer core calls this for all reads during mutation —
  after the first COW in a txn, `root_pgno` may point at a dirty page, and methods
  like `rebalance`, `lookup_covering`, `contains_from`, `lookup_ge`, `descend_to_leaf`,
  and `scan_node` all traverse through dirty pages.
- **`write_page_mut(pgno)`**: returns a mutable reference to the page's storage.
  For private-buffer mode: uses `dirty.entry(pgno).or_insert_with(|| pool.pop()...)`
  to get or create a recycled buffer. Only pops from the pool when no existing dirty
  entry exists (avoids losing buffers on double-write to the same pgno).
- **`write_page(pgno, data)`**: for private-buffer mode: inserts `pgno → data.to_vec()`
  into dirty map (used when the caller already has a buffer). For MAP_SHARED mode:
  no-op (data was already written via `write_page_mut`).
- **`alloc_page()`**: increment `logical_pages`. For VecPageStore, also resize the
   underlying Vec by `PAGE_SIZE` bytes (the Writer no longer does this — it calls
   `store.alloc_page()` which handles the resize). For MmapPageStore, no memory
   allocation is required (logical_pages is just a counter; the mmap is extended
   later via `remap()` at commit time). The free-list check and 2^32-page limit
   check stay in the Writer (format-level concepts).
- **`truncate(pages)`**: set `logical_pages = pages`, remove dirty entries ≥ pages.
- **`total_pages()`**: return `logical_pages`.
- **`committed_bytes()`**: return the mmap slice (sized to exactly
   `total_pages * PAGE_SIZE` — no trailing garbage, since the writer mmaps only the
   committed range after reading the meta pages).
- **`page_data(pgno)`**: return from dirty map (panics if not dirty — the OS layer
  guarantees it only calls this for pages in the dirty set).
- **`clear_dirty()`**: move all dirty `Vec<u8>` buffers into the recycled pool
  (instead of dropping them). The next txn pops them from the pool — zero heap
  allocation per dirty page in steady state. The pool grows to the maximum dirty
  set size across all txns and stays there.

**Rust dirty map storage**: Use `FxHashMap<u32, Vec<u8>>` (from the `rustc-hash`
crate — must be added to `v4/rust/iprange-livedb/Cargo.toml`). FxHash for u32 keys
is a single multiply+shift (~2ns) vs SipHash (~25ns). The dirty map is only accessed
by `write_page_mut()`, `write_page()`, `page_data()`, and `clear_dirty()` — never
by `page()` for committed pages. This means the hot read path has zero HashMap
overhead.

**Buffer recycling**: Without recycling, every dirty page allocates a new `Vec<u8>`
(heap allocation) and every `clear_dirty()` drops it (heap deallocation). For a txn
with 1000 dirty pages, this is 1000 allocate + 1000 free calls per txn. With
recycling, the Vecs are kept alive in the pool. After the first txn, all subsequent
txns reuse the same allocations — zero heap allocation per dirty page in steady
state. The pool holds at most `max_dirty_pages × PAGE_SIZE` bytes (e.g., 4MB for
1000 pages). This is the same memory the plan already uses, just kept alive.

To prevent the pool from holding memory from a single anomalous large txn forever,
`clear_dirty()` trims the pool to the current txn's dirty page count if the pool
is more than 2x larger. This keeps memory usage proportional to recent workload
while avoiding thrashing for modest fluctuations.

### Commit Protocol (Unchanged)

The two-fsync barrier stays identical. The OS layer (`FileWriter::commit_durable` /
`commitDurable`) changes how it reads page data for pwrite:

- **Before**: `self.w.image()[off..off+PAGE_SIZE]` — reads from contiguous Vec
- **After**: `self.w.store.page_data(pgno)` — reads from store (dirty map for mmap
  backed, Vec for Vec backed)

The meta page is written through the store (goes into dirty map for mmap-backed), then
the OS layer pwrites it from the store via `page_data(inactive_meta)`. The timing
dependency is preserved: `write_meta` is called by `finish_commit_meta` AFTER
`take_dirty` drains the dirty set, so the meta page lands in the clean post-drain
dirty map and is NOT included in Barrier 1's pwrite set.

<!-- CRC32C hardware acceleration is deferred to a follow-up SOW. See item 40 in the
     Execution Log. The mmap-backed writer works correctly with software CRC32C. -->

### Converting write_leaf/write_branch/write_meta

These functions currently write directly to `self.image[base..]`. With `Box<dyn PageStore>`,
there is a single function body (not conditionalized by store type). They use
`self.store.write_page_mut(pgno)` to get a mutable reference to the page's storage,
write directly into it, then checksum in-place. This eliminates the stack buffer and
the extra 4KB memcpy:

```rust
fn write_leaf(&mut self, records: &[OwnedRecord<K>]) -> Result<u32> {
    let pgno = self.alloc_page()?;
    self.dirty.push(pgno);
    let page = self.store.write_page_mut(pgno);
    page.fill(0);
    PageHeader::write(page, spec::PAGE_TYPE_LEAF, records.len() as u16, pgno);
    for (i, r) in records.iter().enumerate() {
        let off = PAGE_HEADER_SIZE + i * self.record_size;
        record::write::<K>(&mut page[off..off + self.record_size], r.from, r.to, &r.scope);
    }
    finalize_checksum(page);
    Ok(pgno)
}
```

For VecPageStore, `write_page_mut()` returns `&mut self.image[base..base+PAGE_SIZE]`
— zero copy, same as the current code. For MmapPageStore (private-buffer mode), it
returns a mutable slice into a recycled `Vec<u8>` from the dirty map — one Entry API
call, no memcpy. For MmapPageStore (MAP_SHARED mode), it returns a mutable slice into
the mmap — zero copy.

Same pattern for `write_branch` and `write_meta`. The `self.dirty.push(pgno)` stays in
Writer (format-level tracking). The store's `write_page_mut` stores the data — it
does NOT touch `Writer.dirty`. The Writer's `dirty` Vec tracks which pages the OS
layer must pwrite.

The same `write_page_mut()` conversion applies to the metadata bulk-build functions:
`build_kv_tree`, `build_scope_tree`, and `write_overflow_chain`. These currently
allocate `vec![0u8; PAGE_SIZE]` on the heap, build into it, then call `write_page()`
which copies. Replace with `store.write_page_mut(pgno)` to eliminate the heap
allocation and extra memcpy per metadata page.

### Growth and Remap

- **During mutation**: `alloc_page()` increments logical page count. No buffer resize.
- **At commit (Barrier 1 prep)**: Extend the file to the new logical size. Use a
   fallback chain. The fallocate calls target only the growth region (offset `old_len`,
   length `new_len - old_len`) — using offset `0` would zero-fill the entire file
   including committed pages (data corruption):
   1. Try `fallocate(fd, FALLOC_FL_ZERO_RANGE, old_len, new_len - old_len)` — allocates
      space and zeroes it, no holes. Available on Linux 2.6.31+.
   2. Try `posix_fallocate(fd, old_len, new_len - old_len)` — POSIX standard, available
      on most Unix systems. Ensures space is allocated.
   3. Fall back to `ftruncate` + `pwrite`-of-zeros for every page in the grown region.
      This is the slow path but guarantees no holes on any platform.
   On Linux, `ftruncate` of a MAP_SHARED file returns zero-filled pages for holes
   (safe). On FreeBSD, `ftruncate` creates a hole that SIGBUSes on access — the
   fallback chain avoids this.
- **After commit**: `mmap` new size first, then `munmap` old (or `mremap` on Linux) to
   remap the mmap to the new file size. The next txn sees the new pages through the
   remapped mmap.
- **Remap failure**: Perform the new `mmap` BEFORE `munmap` to avoid a window with
  no valid page source. On Linux, `mremap` with `MREMAP_MAYMOVE` is atomic (kernel
  handles both old and new mappings). On other Unix, `mmap` the new size at a
  different address, then `munmap` the old one, then update the store's pointer.
  **Both Rust and Go must follow this ordering** — mmap new before munmap old.
   In Rust, `memmap2::Mmap` does not expose a constructor from `RawFd`. Use
   `memmap2::MmapOptions::new().len(new_size).map(fd)` — `RawFd` implements
   `memmap2::MmapAsRawDesc` (memmap2 0.9+), so `.map()` accepts the raw fd
   directly without taking ownership of the file.
  In Go, use `unix.Mmap(fd, 0, int(newSize), unix.PROT_READ, unix.MAP_SHARED)`
  to create the new mapping, then `unix.Munmap(old_data)` to release the old one.
  If the new `mmap` fails, the old mapping is still valid — but `page()` would OOB
  for pages beyond the old mmap size. Since Barrier 2 has already completed (the
  new meta is durable), the on-disk file is valid but the in-memory state cannot
  continue safely. **Poison the writer** — the caller must discard and reopen.
- **Dirty map clearing**: After the remap check (whether or not remap was needed), call
   `store.clear_dirty()`. The next txn starts with an empty dirty map; committed pages
   are read from the (possibly remapped) mmap. Without this, the dirty map grows
   unboundedly across txns and stale entries shadow committed data.

### Writer Open Path — Sparse File Handling

The writer must handle files that have holes beyond the committed range (e.g., from
a crashed growth where `set_len` extended the file but pages were never written).
Unlike the reader, the writer does NOT reject sparse files outright — instead, it
mmaps only the committed range (`meta.total_pages * PAGE_SIZE`) rather than the
full file. This avoids SIGBUS from holes beyond the committed tree while still
using mmap for the committed data.

Implementation: read the two meta pages first (via `pread_exact` at offsets 0
and 4096 — use `read_exact_at` in Rust, `ReadAt` with full-length buffer in Go,
to handle partial reads). Determine `total_pages` and select the active meta.
Verify that `file_size >= total_pages * PAGE_SIZE` (reject with `FileTooShort`
if not — a corrupt meta page reporting a `total_pages` beyond the file would
cause SIGBUS on mmap access).

**Truncate trailing pages**: If `file_size > total_pages * PAGE_SIZE`, truncate
the file to `total_pages * PAGE_SIZE` with `ftruncate`. Trailing pages beyond
the committed tree are unreachable garbage — they may contain holes that would
cause the reader's `MmapReader::open` to reject the file (the reader checks for
holes in the full file). Truncation is safe because:
- The committed tree never references pages beyond `total_pages`
- The meta pages are within the first 8KB, always before the truncation point
- `ftruncate` is atomic on all supported platforms (POSIX guarantee)
- The truncation happens before any mmap, so there is no stale mapping to worry
  about

After truncation, mmap only `total_pages * PAGE_SIZE` bytes. This is safe
because:
- The meta pages are always at fixed positions (0 and 4096) within the first 8KB
- The committed tree never references pages beyond `total_pages`
- Holes beyond `total_pages` are never accessed through the mmap

If the file is too short to contain the meta pages (< 2 * PAGE_SIZE), reject with
`FileTooShort` (same as the reader).

Additionally, verify that the committed range is hole-free before mmaping. Use
`SEEK_HOLE` scoped to `[0, total_pages * PAGE_SIZE)`:
- If `SEEK_HOLE` returns a hole within the committed range, **reject the file**
  with `SparseFile` error. A hole in the committed range means the file is
  corrupt (the fallocate chain in `commit_durable` should have prevented this).
  Do NOT fall back to a pread-based heap copy — that would reintroduce the
  "file must fit in RAM" failure for large files.
- If `SEEK_HOLE` returns `ENXIO`, **reject the file** with `SparseFile` error.
  `man 2 lseek` specifies `ENXIO` only for "offset is beyond the end of the
  file" — SEEK_HOLE with no hole returns EOF, not ENXIO. The Linux VFS layer
  handles SEEK_HOLE for all filesystems (the simplest implementation returns
  EOF for any query), so ENXIO from offset 0 on a non-empty file indicates a
  genuinely broken file descriptor or filesystem state. Failing closed is
  correct. Users on exotic filesystems can use VecPageStore directly (the
  explicit non-mmap API, analogous to the reader's "read into a Vec" fallback).
- If `SEEK_HOLE` returns any other error, fail the open (the file or filesystem
  is in an unexpected state).

If validation fails after the mmap is created (e.g., `Reader::open` rejects the
file), the mmap must be cleaned up before returning the error. In Rust, wrap the
mmap creation and validation in a helper that calls `store.close()` on the error
path. In Go, the existing `defer` + `ok` pattern (same as `MmapReader.Open`) must
be extended to also call `store.close()` (or `unix.Munmap`) on failure — closing
the file alone leaks the mmap.

### Free-Set Derivation and Validation at Open

At open time, the mmap bytes are passed directly to:
- `Reader::open(&mmap_bytes)` — full §9 validation (no heap copy)
- `derive_free_set()` — walks reachable pages through the mmap bytes (calls
  `scope::collect_pages`, `kv::collect_pages` with mmap bytes via
  `self.store.committed_bytes()`). The `used` Vec is sized to
  `self.store.total_pages()` (the committed page count from meta), NOT to
  `committed_bytes().len()` (which is the mmap size — equal to `total_pages *
  PAGE_SIZE` for MmapPageStore, but using `total_pages()` is the canonical
  source of truth). This ensures the free-set derivation is bounded by the
  committed tree, not by the file size.

### Committed Data Access for External Functions

The following external functions take `&[u8]` (contiguous committed image) and are
called from Writer methods. With the PageStore, they receive bytes from
`self.store.committed_bytes()`:

| Function | Called from | Access pattern |
|---|---|---|
| `Reader::open(&[u8])` | `open_image` (at Writer construction) | Full validation |
| `scope::load_all(&[u8], root)` | `open_image` (at Writer construction) | Load scope registry |
| `scope::collect_pages(&[u8], root, ...)` | `rebuild_scope_table`, `derive_free_set` | Walk scope tree |
| `kv::collect_pages(&[u8], root, ...)` | `free_kv_tree`, `derive_free_set` | Walk KV trees |
| `kv::get(&[u8], root, key, ...)` | `meta_get` | Read committed KV |
| `kv::list(&[u8], root, ...)` | `kv_load_dirty`, `meta_list` | List committed KV |

All of these operate on the **committed** state (not dirty pages). For MmapPageStore,
`committed_bytes()` returns the mmap slice. For VecPageStore, it returns the Vec slice.
The functions themselves are unchanged — they receive a `&[u8]` as before.

### Writer Open Path for MmapPageStore

The current `Writer::open_image(mut image: Vec<u8>)` takes ownership of a Vec. For the
mmap path, a new constructor is needed:

```rust
impl<K: IpKey> Writer<K> {
    /// Open an existing image from a page store. The store must already contain
    /// the committed state (e.g., from an mmap). Validates the tree and derives
    /// the free set.
    pub(crate) fn open_with_store(store: Box<dyn PageStore>) -> Result<Self> {
        let bytes = store.committed_bytes();
        let r = Reader::open(bytes)?;
        // ... same logic as open_image but using store instead of Vec ...
    }
}
```

For VecPageStore, `open_image` is kept as a convenience wrapper. For MmapPageStore,
`FileWriter::open` creates the MmapPageStore and calls `Writer::open_with_store`.

### `image()` / `into_image()` — Via Trait Methods

With `Box<dyn PageStore>`, `Writer<K>` has no generic parameter — there is no
`Writer<K, VecPageStore>` type. Instead, the PageStore trait provides optional
access to the inner Vec:

```rust
// On Writer<K>:
pub fn image(&self) -> &[u8] {
    // For VecPageStore: returns the Vec (zero-cost). For MmapPageStore:
    // returns committed_bytes() (the mmap, sized to total_pages * PAGE_SIZE).
    self.store.as_bytes()
        .unwrap_or_else(|| self.store.committed_bytes())
}

pub fn into_image(self) -> Vec<u8> {
    // For VecPageStore: moves the Vec out (zero-cost). For MmapPageStore:
    // copies committed_bytes() into a new Vec (O(file_size) — acceptable
    // because into_image is only used in tests and the pure-API path).
    // Materialize the boolean first to avoid holding a borrow across
    // the move of self.store into into_vec().
    let is_vec = self.store.as_bytes().is_some();
    if is_vec {
        self.store.into_vec().unwrap()
    } else {
        self.store.committed_bytes().to_vec()
    }
}
```

For VecPageStore, `as_bytes()` returns `Some(&self.image)` and `into_vec()` returns
`Some(self.image)` — zero-cost, no copy. For MmapPageStore, `as_bytes()` returns
`None` (falls back to `committed_bytes()`) and `into_vec()` returns `None` (falls
back to copying committed bytes). Tests that use `into_image()` continue to work
unchanged because they create Writers via `Writer::create` (VecPageStore).

In Go, `Image()` uses the same pattern: check if the store is Vec-backed via a type
assertion or interface method; if not, return a copy of committed bytes.

### Go Port

Same architecture using an interface:

```go
type pageStore interface {
    page(pgno uint32) []byte           // checks dirty map, then committed
    writePageMut(pgno uint32) []byte   // mutable ref, no copy
    writePage(pgno uint32, data []byte)
    allocPage() (uint32, error)
    totalPages() uint64
    truncate(pages uint32)
    committedBytes() []byte
    pageData(pgno uint32) []byte
    clearDirty()
    remap(fd uintptr, newSize int64) error
    close()
}
```

`Writer[K]` gains a `store pageStore` field. `createWriter` uses `vecPageStore`,
`openFileWriter` uses `mmapPageStore` (backed by `unix.Mmap` + `map[uint32][]byte`).

**Go safety**: `page()` returns `[]byte` (mutable). For MmapPageStore, the mmap is
`PROT_READ` — writing to the returned slice causes SIGSEGV. Document prominently that
`page()` output MUST NOT be written to. The zero-page fallback for grown-but-unwritten
pages returns a package-level `var zeroPage [4096]byte` slice (no allocation, no
aliasing risk since all callers are read-only).

**Go `Image()`**: Returns a copy of `committed_bytes()` for mmap-backed writers
(instead of panicking). This is O(file_size) but acceptable for diagnostics and
tests. VecPageStore returns the Vec directly (zero-cost). The method signature
stays `func (w *Writer[K]) Image() []byte` — no API break.

**Go `commitDurable`**: The `Truncate` call changes from
`len(fw.w.Image())` to `int64(fw.w.totalPages() * pageSize)`. The pwrite loop reads
from `fw.w.store.pageData(p)` instead of `fw.w.Image()[off:off+pageSize]`. After
Barrier 2, if the file size changed, call `fw.w.store.remap(fd, newSize)` and
update the tracked mmap size. Then call `fw.w.store.clearDirty()`.

**Go `FileWriter.Close()`**: Must also clean up the mmap. Add a `Close()` method that
calls `store.close()` (idempotent, uses a `closed` flag to prevent double-unmap)
before closing the file. VecPageStore no-ops, MmapPageStore calls `unix.Munmap`.
This prevents mmap leaks on writer close.

### take_dirty — Avoid O(n²) with freed_this_txn

The current `take_dirty` filter (writer.rs:373-383) checks `self.freed_this_txn.contains(&p)`
for each dirty page. `Vec::contains` is O(n). For a txn with many dirty and freed pages,
this is O(dirty × freed).

However, `free_page()` can be called for pages with `pgno >= committed_pages` — pages
allocated this txn and then freed within the same txn (e.g., via `rebalance` during
`cow_delete`). A bitset sized to `committed_pages` would OOB for these pages. A
dynamically-grown bitset adds complexity.

Instead, keep `freed_this_txn: Vec<u32>` but convert it to an `FxHashSet<u32>` at
`take_dirty` time for O(1) lookup. The conversion cost is O(freed) per commit
(one HashSet insertion per freed page). The HashSet is dropped after `take_dirty`
returns. This avoids the OOB issue entirely while still eliminating the O(n²)
behavior.

```rust
fn take_dirty(&mut self) -> Vec<u32> {
    let dirty = core::mem::take(&mut self.dirty);
    if self.freed_this_txn.is_empty() {
        return dirty;
    }
    // Clone — do NOT drain! finish_commit_meta reads freed_this_txn
    // to move freed pages into the free list.
    let freed: FxHashSet<u32> = self.freed_this_txn.iter().copied().collect();
    let boundary = self.committed_pages as u32;
    dirty.into_iter()
        .filter(|&p| p >= boundary || !freed.contains(&p))
        .collect()
}
```

Same change in Go: convert `freedThisTxn []uint32` to `map[uint32]struct{}` in
`takeDirty`.

### MAP_SHARED + PROT_WRITE — Future Optimization

The plan uses private dirty-page buffers with pwrite commit. An alternative approach
is to mmap the file with `PROT_READ | PROT_WRITE | MAP_SHARED` and write dirty pages
directly into the mmap. This eliminates:
- The dirty HashMap entirely (pages written directly to the mmap)
- The `write_page()` / `page_data()` methods (data is live in the mmap)
- The extra 4KB memcpy per page write
- The SIGSEGV risk in Go (mmap is writable)

Analysis confirms this is safe under the writer's LOCK_EX contract:
- No concurrent readers (LOCK_EX blocks LOCK_SH)
- Dirty pages written through MAP_SHARED are visible to the same process immediately
  (fine — single writer)
- fsync guarantees all MAP_SHARED dirty pages are on disk before returning
- The meta page is still pwritten (not through the mmap) — Barrier 2 ordering preserved
- A crash after kernel writeback of dirty pages but before the meta page: data pages
  on disk are from the new txn, but the meta still points at the old tree. On next
  open, the old tree is active (correct). Orphaned data pages are harmless.

However, MAP_SHARED requires special handling for newly allocated pages (beyond the
committed file size) — they can't be written through the mmap until the file is
extended at commit time. This adds complexity for a relatively small benefit (the
private-buffer approach with buffer recycling already achieves zero-allocation steady
state). Defer MAP_SHARED to a follow-up SOW if profiling shows the private-buffer
overhead is significant.

### commit_durable — Use store.total_pages() and store.page_data()

The Rust `commit_durable` (os.rs:288-303) currently uses `self.w.image()` for both
the file length and pwrite data. With the store:

```rust
fn grow_file(fd: RawFd, old_len: u64, new_len: u64) -> Result<()> {
    if new_len <= old_len {
        // Shrink: ftruncate is sufficient (no hole risk)
        if unsafe { libc::ftruncate(fd, new_len as libc::off_t) } != 0 {
            return Err(Error::Io(io::Error::last_os_error()));
        }
        return Ok(());
    }
    // Grow: avoid sparse holes that would SIGBUS on mmap access.
    // First extend the file to the new size.
    if unsafe { libc::ftruncate(fd, new_len as libc::off_t) } != 0 {
        return Err(Error::Io(io::Error::last_os_error()));
    }
    // Then allocate space for the growth region only (offset old_len,
    // length new_len - old_len). Using offset 0 would zero-fill the
    // entire file including committed pages — data corruption.
    let grow_off = old_len as libc::off_t;
    let grow_len = (new_len - old_len) as libc::off_t;
    #[cfg(target_os = "linux")]
    {
        if unsafe { libc::fallocate(fd, libc::FALLOC_FL_ZERO_RANGE, grow_off, grow_len) } == 0 {
            return Ok(());
        }
    }
    #[cfg(not(target_os = "linux"))]
    {
        if unsafe { libc::posix_fallocate(fd, grow_off, grow_len) } == 0 {
            return Ok(());
        }
    }
    // Fallback: pwrite-zero-fill every page in the grown region
    for off in (old_len..new_len).step_by(PAGE_SIZE) {
        let zeros = [0u8; PAGE_SIZE];
        if unsafe { libc::pwrite(fd, zeros.as_ptr() as *const _, PAGE_SIZE, off as libc::off_t) } != PAGE_SIZE as isize {
            return Err(Error::Io(io::Error::last_os_error()));
        }
    }
    Ok(())
}

fn commit_durable(&mut self, updated_unixtime: u64) -> Result<()> {
    let dirty = self.w.take_dirty();
    let new_len = self.w.store.total_pages() as u64 * PAGE_SIZE as u64;
    // grow_file is only needed for mmap-backed stores (mmap_len > 0).
    // VecPageStore uses pwrite which extends the file naturally.
    // Calling grow_file with old_len=0 would zero-fill the entire file
    // (data corruption) — this is the SEEK_HOLE fallback case where
    // VecPageStore wraps an existing file.
    if self.mmap_len > 0 {
        grow_file(self.file.as_raw_fd(), self.mmap_len, new_len)?;
    }
    for &p in &dirty {
        let off = p as u64 * PAGE_SIZE as u64;
        self.file.write_all_at(self.w.store.page_data(p), off)?;
    }
    self.file.sync_all()?; // Barrier 1
    let inactive = self.w.finish_commit_meta(updated_unixtime);
    let off = inactive as u64 * PAGE_SIZE as u64;
    self.file.write_all_at(self.w.store.page_data(inactive), off)?;
    self.file.sync_all()?; // Barrier 2
    // Remap if the file size changed (grew or shrunk). A stale mmap larger
    // than the file would SIGBUS on access to pages beyond EOF. self.mmap_len
    // tracks the mmap size (0 for VecPageStore, file size at open for
    // MmapPageStore, updated after each successful remap).
    if self.mmap_len > 0 && new_len != self.mmap_len {
        self.w.store.remap(self.file.as_raw_fd(), new_len)?;
        self.mmap_len = new_len;
    }
    self.w.store.clear_dirty();
    Ok(())
}
```

The `FileWriter` struct gains a `mmap_len: u64` field, initialized to `0` for
`FileWriter::create` (VecPageStore, no mmap) and to `meta.total_pages * PAGE_SIZE`
for `FileWriter::open` (MmapPageStore, the partial mmap covers exactly the
committed range).

Rust `FileWriter` also needs a `Drop` impl to ensure the mmap is cleaned up:

```rust
impl<K: IpKey> Drop for FileWriter<K> {
    fn drop(&mut self) {
        self.w.store.close();
    }
}
```

Without this, `MmapPageStore`'s mmap (wrapped in `Option<Mmap>`) would never be
unmapped when the `FileWriter` is dropped without an explicit `close()` call.
The `Drop` impl calls `store.close()` which takes the `Option` and drops it
(`memmap2::Mmap` unmaps on drop) — a second `close()` call is a no-op.

Key changes:
- `set_len` uses `store.total_pages() * PAGE_SIZE`, not `image().len()`
- pwrite reads from `store.page_data(pgno)`, not `image()[off..]`
- `finish_commit_meta` sets `committed_pages = store.total_pages() as usize`
  instead of `self.image.len() / PAGE_SIZE`. `total_pages()` returns `u64`; the
  `as usize` cast is safe because the format enforces a 2^32-page limit, which
  fits in `usize` on all supported targets (64-bit Unix, 64-bit Windows). On
  32-bit targets, the cast truncates — but v4 targets 64-bit systems.
- After Barrier 2, remap if the file grew, then clear_dirty()

Same pattern in Go's `commitDurable`.

### Writer::commit() — Runtime Guard

The in-memory `Writer::commit()` path (used by tests and the pure-API path) calls
`finish_commit_meta` which writes the meta through the store. For MmapPageStore, this
adds the meta to the dirty map. But `Writer::commit()` does NOT call `clear_dirty()`
or remap the mmap — it would silently leak the dirty map and leave stale entries.

Add a runtime guard at the top of `Writer::commit()`:

```rust
pub fn commit(&mut self, updated_unixtime: u64) -> Result<()> {
    if self.store.as_bytes().is_none() {
        return Err(Error::InvalidInput(
            "Writer::commit() is only valid for VecPageStore. \
             Use FileWriter::commit() for mmap-backed writers."
        ));
    }
    // ... rest of commit logic unchanged ...
}
```

This returns a typed error in all builds, preventing silent data corruption if
`Writer::commit()` is accidentally called on an mmap-backed writer.

### Step-by-Step

1. **Define `PageStore` trait + `VecPageStore` in Rust** — Writer stores
   `Box<dyn PageStore>` (not a generic parameter). All direct `self.image` accesses in
   writer.rs go through `self.store`. `image()`/`into_image()` use trait methods
   (`as_bytes()`/`into_vec()`) instead of conditional impl blocks.

2. **Implement `MmapPageStore` in Rust** — wraps `Mmap` + `HashMap<u32, Vec<u8>>`.
   Handle page read (dirty→mmap→zero), write (dirty map insert), alloc (counter bump),
   truncate.

3. **Update Rust `FileWriter`** — `open` uses mmap + MmapPageStore. `commit_durable`
   reads from store's dirty map. Handle remap after growth. `create` stays Vec-backed
   (new file is small).

4. **Port to Go** — `pageStore` interface, `vecPageStore`, `mmapPageStore`, update
   `openFileWriter` and `commitDurable`.

5. **Tests** — existing tests pass (Vec-backed). New: memory regression test (prove no
   whole-file heap copy), cross-read with mmap-backed writer, crash safety with mmap
   writer, growth/remap tests.

6. **Spec and artifact updates** — update `design-iprange-v4-livedb.md`, close SOW.

Validation plan:

- Rust:
  - `cargo test --manifest-path v4/rust/Cargo.toml`
  - `cargo test --manifest-path v4/rust/Cargo.toml --features export-v3`
  - `cargo clippy --manifest-path v4/rust/Cargo.toml --all-targets --all-features -- -D warnings`
- Go:
  - `go -C v4/go test ./...`
  - `go -C v4/go vet ./...`
- Cross-read:
  - regenerate or verify v4 conformance goldens as needed; Rust reads Go files and Go
    reads Rust files identically.
- Memory/file-backed regression:
  - add a test or script that demonstrates writer-open does not allocate/read the full
    file. The test must fail against the current implementation.
- Crash safety:
  - existing interrupted-commit tests must still pass;
  - add mmap-backed writer growth/reuse tests to cover newly allocated pages, reclaimed
    pages below committed size, and remap after commit.
- Same-failure search:
  - search both languages for file-size-proportional allocation/read patterns in writer
    open paths.
- External review:
  - run external reviewers at the production-grade milestone if the user asks or before
    a PR/commit boundary where the reviewer policy applies.

Artifact impact plan:

- AGENTS.md: update project-specific commands or warnings only if workflow changes.
- Runtime project skills: create or update a project runtime skill only if this SOW
  establishes reusable v4 conformance/crash/mmap-writer workflow knowledge.
- Specs: update `design-iprange-v4-livedb.md` and, if needed,
  `design-iprange-v4-scope-api.md`.
- End-user/operator docs: likely unaffected unless public SDK writer behavior is
  documented elsewhere.
- End-user/operator skills: likely unaffected; record evidence at close.
- SOW lifecycle: this SOW lives in `current/` while active; it closes only after code,
  specs, validation, and follow-up mapping are complete.

Open-source reference evidence:

- None checked yet in this SOW. Prior SOWs used bbolt/redb as technique references; this
  SOW may re-check mmap/COW writer patterns if implementation design needs external
  evidence.

Open decisions:

- Resolved by user direction: whole-file heap writer is invalid and must be replaced now.
- Resolved by v4 crash-safety contract: the writer must not overwrite committed pages
  reachable from the active meta before the commit point.
- Resolved by design analysis: use private dirty-page buffers with pwrite commit (not
  shared mmap writes). This preserves the existing crash-safety proof without requiring
  new mmap write-ordering guarantees.
- Resolved by design analysis: `image()`/`into_image()` stay Vec-backed only; mmap-backed
  writer does not expose a contiguous image.
- Resolved by design analysis: free-set derivation and validation at open use the mmap
  bytes directly (no heap copy).

## Implications And Decisions

1. **Decision: full-file writer-open copy is invalid.**
   - Selection: replace it.
   - Reason: it contradicts the recorded "dataset may exceed RAM" purpose of v4.
   - Implication: this is a core refactor, not a cosmetic optimization.

2. **Decision: preserve COW crash safety.**
   - Selection: mmap-backed does not mean overwriting committed pages in place.
   - Reason: active-meta pages must continue to reference a complete old tree until the
     inactive meta is durably flipped.
   - Implication: the writer needs page-level indirection: committed page source plus
     dirty/new page storage.

3. **Recommendation: long-term-best.**
   - Build a mmap-backed/page-backed writer now, not another temporary optimization.
   - Reason: it aligns with the entire v4 purpose and removes the RAM-size defect at the
     correct abstraction boundary.

## Plan

See the detailed architecture, design decisions, and step-by-step plan in the
Pre-Implementation Gate section above. The six implementation steps are:

1. Define `PageStore` trait + `VecPageStore` in Rust — Writer stores
   `Box<dyn PageStore>` (not a generic parameter).
2. Implement `MmapPageStore` in Rust — mmap + dirty HashMap.
3. Update Rust `FileWriter` — mmap-backed open, commit from dirty map, remap after
   growth.
4. Port the same architecture to Go with equivalent behavior and tests.
5. Add regression tests proving no whole-file heap copy and preserving all existing
   crash/conformance/metadata behavior.
6. Update the locked v4 spec, SOW notes, and project workflow memory to state the
   corrected file-backed writer contract.

## Execution Log

### 2026-07-05

- Created this corrective SOW.
- Recorded the defect plainly: current writer-open loads the full file into heap memory,
  which contradicts the v4 scale requirement.

### 2026-07-05 (later)

- Thorough analysis of Rust and Go writer implementations completed.
- Designed the page store abstraction architecture (trait/interface + Vec/Mmap backends).
- Updated Pre-Implementation Gate with detailed design decisions, architecture, and
  step-by-step plan.
- Resolved open decisions: private dirty-page buffers with pwrite (not shared mmap);
  `image()`/`into_image()` Vec-backed only; free-set derivation uses mmap bytes directly.
- Spawned three internal review agents to find gaps, prove the plan wrong, and identify
  uncovered edge cases.

**Review results — critical refinements incorporated (round 1):**

1. **Added `committed_bytes()` and `page_data()` to PageStore trait** — external
   functions (Reader::open, scope::collect_pages, kv::collect_pages, kv::get, kv::list)
   need a contiguous committed byte slice. The OS layer needs a way to read page data
   for pwrite at commit time.

2. **Converted write_leaf/write_branch/write_meta to local buffer + store.write_page()**
   — these functions write directly to `self.image[base..]`. They now build a local
   `[u8; PAGE_SIZE]` buffer and call `self.store.write_page(pgno, &buf)`.

3. **New Writer constructor `open_with_store(store)`** — the mmap path avoids
   `open_image(Vec<u8>)`. A new constructor takes a pre-built PageStore, validates
   through `committed_bytes()`, and derives the free set.

4. **Remap failure handling** — if remap fails after a successful commit, the writer
   is poisoned. The on-disk file is valid. Implementation: mmap new size before
   munmap old (on Linux via mremap; on other Unix, mmap at different address, swap).

5. **Platform-specific growth** — use `fallocate` with `FALLOC_FL_ZERO_RANGE` instead
   of `ftruncate` on platforms where `ftruncate` creates SIGBUS-prone holes (FreeBSD).

6. **Go safety conventions** — `page()` returns `[]byte` (mutable). Document that
    output MUST NOT be written to. Zero-page fallback returns a fresh buffer each time
    to avoid aliasing. `Image()` returns a copy of committed bytes for mmap-backed
    writers (no panic, no API break).

7. **Meta page timing dependency** — `write_meta` is called by `finish_commit_meta`
   AFTER `take_dirty` drains the dirty set. The meta page lands in the clean post-drain
   dirty map and is NOT included in Barrier 1's pwrite set. This timing must be
   preserved.

**Review results — critical refinements incorporated (round 2):**

8. **Switched from generic `Writer<K, S: PageStore>` to `Box<dyn PageStore>`** — the
   generic approach cascades to `FileWriter<K, S>`, breaking every `FileWriter::open`
   call site. Dynamic dispatch avoids the cascade entirely. Overhead is negligible
   (~3–5ns per call vs 4KB memcpy + CRC32C per page write).

9. **Added `clear_dirty()` to PageStore trait** — the dirty HashMap grows unboundedly
    across txns without clearing. Called unconditionally after the remap check (whether
    or not remap was needed). Without this, stale dirty entries shadow committed data
    and memory grows linearly with txns.

10. **MmapPageStore `logical_pages` initialized from `meta.total_pages`, not file size**
    — the file may have trailing garbage pages from a crashed growth. Using file size
    would include garbage in `total_pages`, corrupting the committed metadata.

11. **Added SEEK_HOLE sparse file check to writer open path** — a sparse file would
     SIGBUS on first access to a hole page through the mmap. Later refined: partial
     mmap (only committed range) avoids rejecting files with holes beyond committed
     pages; SEEK_HOLE scoped to committed range detects corrupt files (item 42, 63).

12. **fallocate fallback chain** — try `fallocate(FALLOC_FL_ZERO_RANGE)` →
    `posix_fallocate()` → `ftruncate` + pwrite-zero-fill. Ensures no holes on any
    platform (FreeBSD `ftruncate` creates SIGBUS-prone holes).

13. **FxHashMap for dirty map** — SipHash is ~25ns per lookup; FxHash is ~2ns. Every
    `page()` call does a HashMap lookup (which misses for committed pages — the common
    case). Saves ~20ns per lookup, ~4μs per typical txn.

14. **VecPageStore direct-write considered and abandoned** — with `Box<dyn PageStore>`,
     `write_leaf` is a single function body. Always use `write_page_mut()` which is
     zero-copy for VecPageStore and buffer-recycled for MmapPageStore. The extra 4KB
     memcpy (~200ns) is negligible vs CRC32C (~500ns).

15. **Writer::commit() documented as VecPageStore-only** — the in-memory commit path
    does not clear_dirty() or remap. Only FileWriter::commit (three-phase protocol)
    supports MmapPageStore.

16. **Remap failure considered — degraded-mode rejected** — initial design allowed
     degraded-mode (keep old mapping, retry on next commit). Later corrected (item 32):
     remap failure after a successful Barrier 2 poisons the writer. The on-disk file is
     valid but the in-memory state cannot continue safely. The caller must discard and
     reopen. `page()` is safe (zero-page fallback for pages beyond old mmap), but
     committed data beyond the old mmap is invisible — the writer cannot continue.

**Review results — critical refinements incorporated (round 3):**

17. **Added `into_vec()`, `as_bytes()`, `remap()` to PageStore trait** — with
    `Box<dyn PageStore>`, there is no `Writer<K, VecPageStore>` type for conditional
    impl blocks. `into_vec()`/`as_bytes()` return `Some` for VecPageStore (zero-cost
    extraction) and `None` for MmapPageStore (falls back to `committed_bytes()` copy).
    `remap()` updates the mmap after file growth; VecPageStore no-ops.

18. **Abandoned VecPageStore direct-write optimization in write_leaf/write_branch** —
    with `Box<dyn PageStore>`, `write_leaf` is a single function body. Always use the
    local-buffer approach. The extra 4KB memcpy (~200ns) is negligible vs CRC32C
    (~500ns). Simplifies the design significantly.

19. **Added `Send + Sync` bound to PageStore trait** — `Box<dyn PageStore>` loses
    auto-Send/Sync. Writer is currently auto-Send+Sync. Adding the bound preserves
    this. Writer is never shared across threads in practice, but tests may rely on it.

20. **Fixed `take_dirty` O(n²) with `freed_this_txn`** — initially replaced `Vec<u32>`
     with `FxHashSet<u32>` for `freed_this_txn`. Later reverted (item 31): `free_page()`
     can be called for pages with `pgno >= committed_pages` (allocated this txn, freed
     via rebalance). Keep `Vec<u32>` for `free_page()`, convert to `FxHashSet<u32>` at
     `take_dirty` time for O(1) lookup. Same pattern in Go.

21. **Fixed `commit_durable` to use `store.total_pages()` and `store.page_data()`** —
    `set_len` uses `store.total_pages() * PAGE_SIZE`, not `image().len()`. pwrite
    reads from `store.page_data(pgno)`, not `image()[off..]`. Added remap + clear_dirty
    after Barrier 2. Both Rust and Go specified.

22. **Added `grow_file` responsibility to OS layer** — the fallocate/fallback chain
    lives in `FileWriter::commit_durable` (which owns the fd), not in MmapPageStore.
    After extending the file, the OS layer calls `store.remap(new_size)` to update
    the mmap.

**Review results — performance optimization refinements (round 4):**

23. **`committed_page()` fast path considered and removed** — the Writer core's COW path
     was thought to only read committed pages. Later analysis (item 29) showed that after
     the first COW in a txn, `root_pgno` may point at dirty pages — methods like
     `rebalance`, `lookup_covering`, `contains_from`, `lookup_ge`, `descend_to_leaf`,
     and `scan_node` all traverse through dirty pages. A separate `committed_page()`
     fast path was briefly added (item 29) then removed (item 57): the safety window
     (only the very first operation in a txn before any COW) is too narrow to justify
     the risk of silent corruption. Always use `page()` which checks the dirty map first
     (~2ns FxHashMap lookup). `page_data()` is used only by the OS layer at commit time
     to extract dirty page data for pwrite — it is unrelated to the `committed_page()`
     concept.

24. **Added `write_page_mut()` to PageStore trait** — returns a mutable reference to
    the page's storage. For VecPageStore: returns `&mut self.image[base..]` (zero copy,
    same as current code). For MmapPageStore: returns a mutable slice into a recycled
    dirty buffer (or the mmap if MAP_SHARED). Eliminates the stack buffer + extra 4KB
    memcpy from write_leaf/write_branch/write_meta.

25. **Buffer recycling in MmapPageStore** — `clear_dirty()` moves dirty `Vec<u8>` buffers
    into a recycled pool instead of dropping them. The next txn pops them from the pool.
    Zero heap allocation per dirty page in steady state. The pool grows to the maximum
    dirty set size across all txns.

26. **Bitset for `freed_this_txn` considered and rejected** — `free_page()` can be
     called for pages with `pgno >= committed_pages` (pages allocated this txn and
     freed within the same txn via `rebalance`). A bitset sized to `committed_pages`
     would OOB. A dynamically-grown bitset adds complexity. Keep `Vec<u32>` + convert
     to `FxHashSet<u32>` at `take_dirty` time (item 31).

27. **CRC32C hardware acceleration considered and deferred** — the current software
     table-driven CRC32C is ~3-5μs per page. Hardware CRC32C (SSE 4.2, ARM CRC) is
     ~0.3-0.5μs per page — a 10-25x improvement. Deferred to a follow-up SOW (item 40)
     because it is a performance optimization, not a correctness requirement for the
     mmap-backed writer.

28. **MAP_SHARED + PROT_WRITE documented as future optimization** — analysis confirms
    it is safe under LOCK_EX. Would eliminate the dirty HashMap, write_page(), and
    page_data() entirely. Deferred to a follow-up SOW due to complexity of handling
    newly allocated pages beyond the committed file size.

**Review results — critical correctness fixes (round 4):**

29. **Replaced `committed_page()` with unified `page()` as the primary read method** —
     the Writer core DOES read dirty pages after the first COW in a txn. Methods like
     `rebalance`, `lookup_covering`, `contains_from`, `lookup_ge`, `descend_to_leaf`,
     and `scan_node` all traverse from `root_pgno` which may point at dirty pages.
     `page()` checks the dirty map first (FxHashMap, ~2ns), then falls back to the
     committed source. `committed_page()` was briefly retained as a fast path for two
     verified-safe call sites, but later removed entirely (item 57): the safety window
     is too narrow to justify the risk of silent corruption if misused.

30. **Fixed `into_image()` partial-move compile error** — the closure in
    `unwrap_or_else` captured `self.store` after it was moved by `into_vec()`.
    Restructured to check `as_bytes().is_some()` first (temporary borrow, released
    before `into_vec()`).

31. **Reverted `freed_this_txn` from bitset to `Vec<u32>` + FxHashSet conversion** —
    `free_page()` can be called for pages with `pgno >= committed_pages` (pages
    allocated this txn and freed within the same txn via `rebalance`). A bitset
    sized to `committed_pages` would OOB. Instead, keep `Vec<u32>` for `free_page()`
    and convert to `FxHashSet<u32>` at `take_dirty` time for O(1) lookup.

32. **Remap failure poisons the writer** — if remap fails after a successful Barrier 2,
     `page()` would return the zero page for committed pages beyond the old mmap size
     (they are not in the dirty map and not in the mmap). The on-disk file is valid but
     the in-memory state cannot continue safely — the writer is poisoned. The caller
     must discard and reopen.

**Review results — round 5 fixes:**

33. **Fixed `take_dirty` — use `.iter().copied().collect()` not `.drain(..)`** — draining
    `freed_this_txn` would empty it before `finish_commit_meta` reads it to move freed
    pages into the free list. Every freed page would be permanently lost, causing
    unbounded file growth.

34. **Fixed `open_with_store` signature — `Box<dyn PageStore>` not generic `S`** — the
    plan uses `Box<dyn PageStore>`, not generics. The signature must match.

35. **Fixed `into_image()` lifetime — materialize boolean before move** — `as_bytes()`
    borrows `self.store`, but `into_vec()` moves it. The borrow from `as_bytes()` is
    still alive when `into_vec()` is called. Materialize `is_vec` first.

36. **Added `RawFd` parameter to `remap()`** — `MmapPageStore` does not own the file
    descriptor (`FileWriter` does). A new `mmap()` call (or `mremap`) needs the fd.
    Without it, remap cannot work on non-Linux platforms.

37. **Fixed `page()` OOB check — use `committed_pages` not `logical_pages`** — pages
    allocated this txn (`pgno >= committed_pages`) are not in the mmap. The check
    must use the mmap's page count, not the logical page count.

38. **Added `close()` to PageStore trait** — Go's `MmapPageStore` holds an `[]byte`
    from `unix.Mmap()` that must be explicitly `unix.Munmap()`ed. `FileWriter.Close()`
    calls `store.close()` before closing the file. Prevents mmap leaks.

39. **Added `mmap_len` field to `FileWriter`** — tracks the current mmap size for
    the growth check in `commit_durable`. Initialized to `0` for `create` (VecPageStore)
    and to the file size for `open` (MmapPageStore).

40. **Moved CRC32C hardware acceleration to a separate follow-up SOW** — it is a
    performance optimization, not a correctness requirement for the mmap-backed writer.
    The performance analysis in this SOW uses the correct software CRC32C numbers
    (~3-5μs per page).

41. **Updated acceptance criteria for `derive_free_set`** — the free-set derivation
    allocates a `Vec<bool>` proportional to `total_pages` (transient, O(pages) memory).
    This is accepted per the v4 spec (§7) and is the same as the current Vec-backed
    writer. The criteria now accurately state that no whole-file data copy occurs.

**Review results — round 6 fixes:**

42. **Replaced SEEK_HOLE rejection with partial mmap** — the writer now mmaps only
    `meta.total_pages * PAGE_SIZE` bytes (after reading the meta pages via pread).
    This avoids rejecting files with holes beyond the committed range (e.g., from a
    crashed growth where `set_len` extended the file but pages were never written).

43. **Fixed `page()` OOB bound — uses committed page count, not `logical_pages`**
     — `logical_pages` includes pages allocated this txn which are not in the mmap.
     The mmap access check must use the mmap's actual page count, not the logical
     page count. (Applies to the `pgno < committed_pages` guard in `page()`.)

44. **Fixed shrink commit — remap unconditionally when size changes** — a stale mmap
    larger than the file would SIGBUS on access to pages beyond EOF. Changed the
    condition from `new_len > self.mmap_len` to `new_len != self.mmap_len`.

45. **Fixed `derive_free_set` sizing — uses `store.total_pages()` not
    `committed_bytes().len()`** — the `used` Vec must be sized to the committed page
    count, not the mmap length (which may include trailing garbage pages).

46. **Converted metadata bulk-build functions to `write_page_mut()`** — `build_kv_tree`,
    `build_scope_tree`, and `write_overflow_chain` now use `store.write_page_mut(pgno)`
    instead of heap-allocated scratch buffers + `write_page()`. Eliminates heap
    allocation and extra memcpy per metadata page.

47. **Added runtime guard to `Writer::commit()`** — initially a `debug_assert!` (catches
     accidental use on MmapPageStore-backed writers in debug builds only). Later changed
     to typed `Err` return (item 74): `debug_assert!` is stripped in release builds,
     allowing silent data corruption. Now returns `Error::InvalidInput` in all builds.

48. **Reconciled remap failure description** — removed the contradictory "degraded-mode"
    language from item 16. Remap failure after a successful Barrier 2 poisons the
    writer (item 32). The on-disk file is valid; the caller must discard and reopen.

49. **Moved CRC32C section to deferred** — replaced the implementation section with an
    HTML comment clearly marking it as deferred to a follow-up SOW. No ambiguity.

**Review results — round 7 fixes:**

50. **Fixed Rust double-munmap UB** — MmapPageStore wraps the `Mmap` in `ManuallyDrop`
    to prevent the Drop impl from double-unmapping after `close()` calls `munmap`.
    `close()` is idempotent (safe to call multiple times).

51. **Added file_size >= total_pages * PAGE_SIZE guard before mmap** — a corrupt meta
    page reporting a `total_pages` beyond the file would cause SIGBUS. Reject with
    `FileTooShort` before mmaping.

52. **Changed `total_pages()` return type from `u32` to `u64`** — all existing callers
    (kv::get, kv::list, kv::collect_pages, Meta::total_pages) expect `u64`. The format
    enforces total_pages < 2^32, so the u64 return is always u32-storable.

53. **Completed Go `pageStore` interface** — added `writePageMut`, `remap`, and `close`
     methods that were missing from the interface snippet. `committedPage` was considered
     but removed in item 57 (safety window too narrow).

54. **Added remap step to Go `commitDurable`** — after Barrier 2, if the file size
    changed, call `store.remap(fd, newSize)` and update the tracked mmap size.

55. **Updated `committed_bytes()` documentation** — after the partial-mmap change
    (item 42), the mmap is sized to exactly `total_pages * PAGE_SIZE`. No trailing
    garbage. Removed the stale bound warning.

56. **Fixed Go zero-page fallback** — use a package-level `var zeroPage [4096]byte`
    instead of `make([]byte, pageSize)` per call. No allocation on the defensive path.

**Review results — round 8 fixes:**

57. **Removed `committed_page()` fast path** — the safety window (only the very first
    operation in a txn before any COW) is too narrow to justify the risk of silent
    corruption if misused. Always use `page()` which checks the dirty map first
    (~2ns FxHashMap lookup).

58. **Fixed `commitDurable` to use fallocate chain** — replaced raw `set_len` with
    a `grow_file()` helper that tries `fallocate(FALLOC_FL_ZERO_RANGE)` →
    `posix_fallocate` → `ftruncate` + pwrite-zero-fill. On FreeBSD, uses the
    pwrite-zero-fill path directly (posix_fallocate on UFS can still create holes).

59. **Fixed Rust `close()` idempotency** — MmapPageStore stores the Mmap as
    `Option<ManuallyDrop<Mmap>>`. `close()` takes the Option and calls munmap,
    then drops the empty ManuallyDrop. A second `close()` is a no-op (Option is
    already None). No double-unmap UB.

60. **Added Rust `FileWriter` `Drop` impl** — calls `store.close()` to ensure the
    mmap is unmapped when the FileWriter is dropped. Without this, the mmap leaks
    on every writer close.

61. **Fixed mmap leak on validation failure** — if `Reader::open` rejects the file
    after the mmap is created, the mmap must be cleaned up. Rust: wrap in a helper
    that calls `store.close()` on error. Go: use the existing `defer` + `ok` pattern.

62. **Fixed partial pread handling** — use `read_exact_at` (Rust) / `ReadAt` with
    full-length buffer (Go) for the meta page reads, not raw `pread`. Partial reads
    could produce truncated meta pages that appear valid.

**Review results — round 9 fixes:**

63. **Added SEEK_HOLE check scoped to committed range** — if a hole exists within
    `[0, total_pages * PAGE_SIZE)`, fall back to a pread-based VecPageStore instead
    of MmapPageStore. Prevents SIGBUS from sparse files that the fallocate chain
    failed to prevent.

64. **Fixed `grow_file` with `#[cfg]` gating** — `FALLOC_FL_ZERO_RANGE` is Linux-only.
    Use `#[cfg(target_os = "linux")]` for fallocate, `#[cfg(not(target_os = "linux"))]`
    for posix_fallocate. Handle shrink correctly (ftruncate only, no fallocate).

65. **Added buffer pool trim** — `clear_dirty()` trims the pool to the current txn's
    dirty page count if the pool is more than 2x larger. Prevents a single anomalous
    large txn from permanently inflating RSS.

66. **Fixed Go interface snippet** — removed `committedPage` (fast path was removed
    in item 57). Interface now matches the design.

67. **Fixed Rust RawFd→mmap bridge** — use `memmap2::MmapOptions::with_raw_fd()` to
    create the new mapping from a raw fd without taking ownership of the fd.

68. **Fixed Go remap ordering** — mmap new before munmap old, matching the Rust
    approach. Prevents a window with no valid page source.

69. **Fixed `mmap_len` initialization** — explicitly `meta.total_pages * PAGE_SIZE`,
    not the raw file size (which may include trailing garbage).

**Review results — round 10 fixes:**

70. **Fixed `grow_file` fallocate offset** — was using offset `0` which would zero-fill
    the entire file including committed pages (data corruption). Changed to offset
    `old_len`, length `new_len - old_len` — only the growth region is affected.

71. **Fixed `grow_file` error handling** — all `ftruncate` and `pwrite` calls now check
    return values and propagate errors. The pseudocode was silently ignoring I/O
    failures.

72. **Changed Go `Image()` from panic to committed-bytes copy** — returns a copy of
    `committed_bytes()` for mmap-backed writers instead of panicking. O(file_size)
    but acceptable for diagnostics. No API break.

73. **Fixed Go `openFileWriter` defer cleanup** — extended the error-defer pattern to
    also call `store.close()` (or `unix.Munmap`) on failure. Closing the file alone
    leaks the mmap.

74. **Changed `Writer::commit()` guard from `debug_assert!` to typed `Err` return** —
     `debug_assert!` is stripped in release builds, allowing silent data corruption.
     Now returns `Error::InvalidInput` in all builds.

**Self-review fixes — round 11 (no external reviewers):**

75. **Fixed Growth section prose** — still said `fallocate(fd, 0, new_size)` with offset
    0 (data corruption). Updated to `fallocate(fd, old_len, new_len - old_len)` to match
    the corrected pseudocode.

76. **Fixed "munmap + mmap" ordering** — prose said "munmap + mmap" which implies wrong
    order (munmap first creates a window with no valid page source). Changed to "mmap
    new size first, then munmap old".

77. **Fixed `grow_file` call missing `old_len`** — `grow_file(self.file.as_raw_fd(),
    new_len)` was missing the `old_len` parameter. Changed to `grow_file(self.file.
    as_raw_fd(), self.mmap_len, new_len)`.

78. **Fixed `committed_bytes()` stale trailing-garbage warning** — item 55 claimed this
    was fixed but the prose still warned about trailing garbage that no longer exists
    (partial mmap is sized to exactly `total_pages * PAGE_SIZE`).

79. **Fixed `image()` stale trailing-garbage comment** — same issue as #78.

80. **Fixed stale execution log items** — items 6 (Image() panics), 14 (VecPageStore
    direct-write), 26 (bitset for freed_this_txn), 27 (CRC32C as prerequisite), and
    53 (committedPage in Go interface) contradicted later refinements. Updated to
    reflect current design.

81. **Fixed `clear_dirty()` prose** — said "After remap succeeds" implying conditional
    clearing. `clear_dirty()` is unconditional (dirty pages must be cleared regardless
    of whether the file grew). Updated to "After the remap check (whether or not remap
    was needed)".

82. **Fixed SEEK_HOLE fallback data corruption** — SEEK_HOLE fallback creates a
     VecPageStore with `mmap_len=0`. The unconditional `grow_file(fd, 0, new_len)` would
     zero-fill the entire file. Guarded `grow_file` and `remap` behind `mmap_len > 0`.
     VecPageStore uses pwrite which extends the file naturally — no grow_file needed.

**Self-review fixes — round 12 (gap closure):**

83. **Fixed VecPageStore `alloc_page()` documentation** — the SOW said "no actual memory
     allocation is required" without clarifying that this applies only to MmapPageStore.
     VecPageStore must resize the Vec by `PAGE_SIZE` bytes. Updated the description to
     clarify both implementations.

84. **Fixed SEEK_HOLE error handling gap** — the SOW didn't specify what happens if
     `SEEK_HOLE` is not supported by the filesystem. Added three cases: hole found
     (fall back to VecPageStore), `ENXIO` (no holes, proceed with mmap), other error
     (fail the open).

85. **Fixed `committed_pages` type conversion** — `finish_commit_meta` currently sets
     `committed_pages = self.image.len() / PAGE_SIZE`. With the store, this becomes
     `committed_pages = self.store.total_pages() as usize`. Documented the `u64 → usize`
     cast and the rationale (2^32-page limit fits in `usize` on 64-bit targets).

86. **Fixed `with_raw_fd()` → correct `MmapOptions::new().len(...).map(fd)`** — the SOW
     referenced `with_raw_fd()` which does not exist in installed memmap2. The correct
     API is `.map(fd)` because `RawFd` implements `MmapAsRawDesc`.

87. **Fixed sparse committed-range fallback — reject, don't full-copy** — the SOW said
     to fall back to a pread-based VecPageStore (heap copy) if a hole is found in the
     committed range. This reintroduces the "file must fit in RAM" failure. Changed to
     reject the file with `SparseFile` error.

88. **Added explicit trailing-page truncation at open** — the SOW mapped only the
     committed range but did not truncate trailing garbage pages from the raw file.
     Added `ftruncate` to `total_pages * PAGE_SIZE` before mmap, with rationale
     (prevents reader rejection due to sparse trailing holes).

89. **Fixed SEEK_HOLE `ENXIO` semantics** — the SOW incorrectly claimed `ENXIO` means
     "not supported / no holes". `ENXIO` means either the filesystem doesn't support
     `SEEK_HOLE`/`SEEK_DATA` or the offset is beyond EOF. Changed to: proceed with
     mmap because the fallocate chain is the primary hole-free guarantee; `SEEK_HOLE`
     is only a verification step.

90. **Fixed `open_with_store` visibility** — changed from `pub` to `pub(crate)` to
     avoid clippy `-D warnings` failure (private interface leak through `Box<dyn PageStore>`).

91. **Corrected SEEK_HOLE `ENXIO` handling — fail closed** — initially accepted the
     reviewer's "fail closed" recommendation, then rejected it claiming the reviewer's
     premise was wrong (reader has Vec fallback, writer can't use it). But `man 2 lseek`
     shows ENXIO only occurs for "offset beyond EOF" — SEEK_HOLE with no hole returns
     EOF. The Linux VFS handles SEEK_HOLE for all filesystems, so ENXIO from offset 0
     on a non-empty file indicates genuine corruption, not an unsupported filesystem.
     Failing closed is correct. VecPageStore is the explicit non-mmap path for exotic
     scenarios.

92. **Simplified Rust mmap cleanup — `Option<Mmap>`, no `ManuallyDrop`** — the SOW
     used `Option<ManuallyDrop<Mmap>>` with manual `munmap`. `memmap2::Mmap` already
     unmaps on drop. Changed to `Option<Mmap>` — `close()` calls `self.mmap.take()`
     and drops it. No `ManuallyDrop`, no manual `munmap`.

**External review — round 13 (fable, P0 discovery):**

93. **P0: Writer open could destroy files via ftruncate** — the writer open path used
     `ftruncate` to trim trailing pages before validation. A hostile CRC-valid meta with
     small `total_pages` would truncate committed data. Fixed by:
     - Removing the destructive `ftruncate` at open entirely — now mmaps only the
       committed range and defers trailing-page cleanup to commit time.
     - Using `select_active_meta` (reader bootstrap) for validation before any mutation.
     - Adding `total_pages < 2` and `total_pages >= 2^32` bounds checks.
     - Adding TOCTOU re-fstat + last-byte probe after mmap.
     - Adding trailing sparse page truncation at commit time (after Barrier 2) instead
       of at open.

94. **P2: Go `Writer.Commit` missing mmap store guard** — Go `Writer.Commit` called
     `w.image()` which panics on mmap store. Added mmap store guard matching Rust.

95. **P2: `grow_file` shrink path** — `grow_file` could shrink the file if `new_len <
     old_len`. Added shrink rejection in both languages.

96. **P2: Go `pageData` silent nil on non-dirty page** — Go `pageData` returned nil
     silently for non-dirty pages instead of panicking (matching Rust). Added panic.

97. **P2: Go pool-trim missing nil** — Go pool-trim didn't nil dropped buffers before
     re-slice, preventing GC. Added nil assignment (page_store.go:203-208).

98. **P3: Go mmap writer test parity** — Go gained 4 mmap-writer tests (open, growth,
     reuse, close-releases-lock). Rust already had these.

99. **P3: Rust overflow/shrink-reject/close-releases-lock/page-after-close tests** —
     Added regression tests for each finding.

100. **P3: SEEK_HOLE ENXIO semantics corrected** — initially accepted reviewer's "fail
      closed" recommendation, then re-analyzed: `man 2 lseek` shows ENXIO only for
      "offset beyond EOF". SEEK_HOLE with no hole returns EOF. Linux VFS handles
      SEEK_HOLE for all filesystems, so ENXIO from offset 0 on a non-empty file
      indicates genuine corruption. Failing closed is correct.

101. **P3: Trailing-page truncation moved from open to commit** — truncation at open
      was removed to prevent a hostile (but CRC-valid) meta with small `total_pages`
      from destroying data. Now done after Barrier 2 in `commit_durable`.

102. **P3: `closed` flag + `check_open()` guard** — `FileWriter::close()` closes file
      and releases lock; `closed` flag blocks all mutating operations (set, delete,
      commit, scope_define, scope_drop, scope_set_version, scope_bump_version,
      scope_set_type, meta_set, meta_delete).

103. **P3: Alloc-only clippy fixes** — `#[cfg(feature = "os")]` on `FxHashMap` import,
      `#[cfg_attr(not(feature = "os"), allow(dead_code))]` on `poison()`.

104. **P3: Robustness tests gated behind `slow-tests` feature** — default `cargo test`
      completes in ~1s (105 tests). With `--features slow-tests`: +111 robustness tests
      (~53s).

**Self-review fixes — round 14 (post-P0 gap closure):**

105. **Fixed remap-failure-after-commit error message** — remap failure after Barrier 2
      returned a generic error, misleading callers into thinking the commit failed when
      the data was actually durably on disk. Changed to a distinct error message:
      "commit succeeded but remap failed (data is safe, reopen the file)".

106. **Added `#[allow(dead_code)]` to `PageStore::truncate`** — the `truncate` method
      is no longer called after trailing-page truncation was moved from open to commit.
      Added `#[allow(dead_code)]` to suppress warnings, with a comment noting it's
      preserved for future use.

107. **Updated stale Validation section** — test count corrected from 206 to 219
      (102 lib + 1 conformance + 2 metadata_conformance + 111 robustness). External
      reviewer findings recorded. P0 discovery and fix documented in execution log.

**External review — round 15 (codex):**

108. **P1: Go Darwin/FreeBSD build failure** — `unix.Fallocate` is Linux-only. Split
      `grow_file` into platform-specific files: `os_linux.go` (fallocate + pwrite
      fallback), `os_unix.go` (pwrite-only for Darwin/FreeBSD), with shared
      `growFilePwrite` helper in `os.go`.

109. **P1: Rust clippy failure** — already fixed in round 14 (item 105). Verified:
      `cargo clippy --all-targets --all-features -- -D warnings` passes.

110. **P2: Close() contract incomplete** — `meta_get`, `meta_list`, and `record_count`
      bypassed `check_open()`. After `close()`, the mmap is unmapped and accessing it
      would SIGBUS. Added `check_open()` guards to all three in both Rust and Go.
      `FileWriter::record_count` return type changed from `u64` to `Result<u64>` in
      Rust and from `uint64` to `(uint64, error)` in Go.

111. **P2: remap failure creates false commit error** — already fixed in round 14
      (item 105) with distinct error message. The error tells callers "data is safe,
      reopen the file". Writer is poisoned after the error, preventing retry on the
      same writer. This is the best fix without changing the commit contract.

112. **P2: spec/SOW artifact state inconsistent** — spec §10 said "the writer does not
      mmap". Updated to reflect that the writer mmaps the committed range read-only
      (`PROT_READ`) and uses COW + pwrite for mutation.

113. **P1: FileWriter::create still heap-backed** — acknowledged in SOW acceptance
      criteria (§63-65). Create starts empty and builds incrementally from caller data;
      the Vec grows with caller input, not from loading a pre-existing file. Making it
      mmap-backed requires MAP_SHARED + PROT_WRITE, deferred to follow-up SOW. No
      code change needed.

**External review — round 16 (codex, second pass):**

114. **P2: Growth/remap test tested Vec growth, not mmap growth** — test started with
      `FileWriter::create` (Vec-backed), so growth and remap never exercised the mmap
      path. Rewritten to: create Vec-backed → close → reopen mmap-backed → grow →
      verify remap. Both Rust and Go.

115. **P2: Scope read methods bypassed check_open** — `scope_name`, `scope_list`,
      `scope_version`, `scope_type` returned data from in-memory state without
      checking `closed` flag. Added `check_open()` guards. Return types changed:
      Rust `Option<T>` → `Result<Option<T>>`, Go added `error` return value.

116. **P3: Stale SOW evidence** — "no remaining `self.image`/`w.image` references"
      was misleading (create path still uses it). Clarified that the scan was for
      the `open` path only. Spec update deferral note updated to "completed".

**External review — round 17 (codex, third pass):**

117. **P2: 2^32 page boundary inconsistent** — spec said `total_pages ≤ 2^32` but
      code rejects `>= 2^32` (u32-storable max is `2^32 - 1`). Fixed spec to say
      `< 2^32` matching code and u32 store constraint.

118. **P2: Growth/remap test only asserted `> 2`, not actual remap** — strengthened
      both Rust and Go tests to verify `mmap_len`/`MmapLen` actually increased after
      commit (proves remap happened, not just page count grew). Added `mmap_len()`
      accessor in Rust and `MmapLen()` in Go.

119. **P3: Rust formatting failed** — `cargo fmt --check` had diffs. Ran `cargo fmt`
      to fix all formatting.

120. **P3: SOW stale text** — sub-state said "No implementation has started" (stale).
      Test count said 219 (actual 216). Review rounds said 13 (actual 17). All fixed.

**User-directed fix — round 18 (create path mmap-backed):**

121. **P1: FileWriter::create now mmap-backed** — instead of keeping the Vec-backed
      writer after writing the initial 2-page file, `create` now writes the 2-page
      file (8KB heap buffer), fsyncs, drops the Vec-backed writer, and reopens through
      the mmap-backed `open` path. Both Rust and Go. The writer never holds the full DB
      image in heap memory. Refactored `open` to share logic via `open_locked` /
      `openFileWriterLocked`. Growth/remap tests simplified (create is now mmap-backed
      too, no need for create→close→reopen dance). `mmap_writer_no_full_file_heap_copy`
      test now also verifies create is mmap-backed.

**Review fixes — round 19:**

122. **P1: Commit no longer returns ordinary errors after Barrier 2** — moved
      trailing-page `ftruncate` and mmap `remap` before `finish_commit_meta` /
      inactive-meta `pwrite` / Barrier 2 in Rust and Go. If truncate/remap fails,
      the old meta remains active and the caller sees an uncommitted error. After
      Barrier 2, the only remaining action is clearing private dirty buffers.

123. **P2: `2^32` page boundary fixed consistently** — readers reject
      `total_pages >= 2^32`; writers reject allocation when current logical pages
      are already `2^32 - 1`, before a new page could produce an unrepresentable
      `u32` page number. Spec and code comments now say `total_pages < 2^32`.

124. **P2: Test-only mmap length accessors removed from public SDK API** — removed
      Rust `FileWriter::mmap_len()` and Go `FileWriter.MmapLen()`. Tests now inspect
      private state from their own module/package.

125. **P2: Dirty-page heap caveat documented honestly** — the current design avoids
      full-file heap residency, but it is still COW with private dirty buffers.
      Heap use during a transaction is proportional to pages dirtied/allocated in
      that uncommitted transaction, not to file size. A direct `MAP_SHARED` writable
      design is a separate optimization with a different durability proof.

126. **P2: Added allocation-boundary tests** — Rust
      `alloc_page_rejects_before_u32_wrap` and Go
      `TestAllocPageRejectsBeforeU32Wrap` prove the allocation guard rejects before
      calling the underlying page store allocator at the wrap boundary.

127. **P2: Rust `close()` now closes the fd immediately** — changed `FileWriter`
      to own `Option<File>` so `close()` can unmap, unlock, and drop the file
      descriptor immediately instead of waiting for `Drop`.

128. **P2: SOW artifact gate corrected** — `AGENTS.md` was updated with explicit
      v4 Rust/Go test commands, so the artifact maintenance gate now records that
      workflow guidance change instead of claiming no workflow change happened.

**Whole-code review — round 20 (reader/writer/page_store cleanup):**

129. **P2: `lookup_ge`/`lookupGE` heap-allocated the descent stack** — both Rust
      (`writer.rs`) and Go (`writer.go`) used `Vec<(u32,usize)>`/`[]frame` + push/pop
      on every `lookup_ge` call (the set/delete hot path). Replaced with a fixed
      `[TREE_HEIGHT_MAX]`/`[treeHeightMax]` array + length counter, mirroring the
      `Cursor`'s zero-alloc pattern. Eliminates one heap allocation per set/delete
      that hits `any_overlap`.

130. **P2: Dead code removal — `writePage`/`truncate` on the store interface** —
      `PageStore::write_page` / `pageStore.writePage` and `PageStore::truncate` /
      `pageStore.truncate` were never called in either language. The `mmapPageStore`
      `writePage` impl also had a latent pool-bypass bug (allocated a fresh 4 KiB
      buffer instead of recycling from the pool). Removed from the trait/interface,
      both store impls, the Rust test mock (`FullPageStore`), and the
      `Writer::write_page` / `Writer.writePage` wrappers. Byte-level COW (SOW-0012)
      works on `write_page_mut` buffers directly, so these stay dead.

131. **P3: mmap `page()` empty-dirty fast-path** — `MmapPageStore::page` /
      `mmapPageStore.page` checked the dirty map on every call. Added a fast path:
      when `dirty.is_empty()` (common in append-only txns that only read committed
      pages during descent), skip the map lookup and go straight to the mmap slice.

132. **P1: `write_overflow_chain` empty-value panic in release** — the
      `debug_assert!(!value.is_empty())` vanishes in release, leaving `pgnos[0]` to
      panic on an empty chain. Replaced with a release-mode `Result` guard. Currently
      unreachable (callers check `KV_INLINE_MAX`) but removes the landmine for future
      callers.

## Validation

Acceptance criteria evidence:

- Rust `FileWriter::open` no longer allocates `Vec<u8>` proportional to file size: uses
  `pread_exact` for 2 meta pages (8KB) + `Mmap` for committed range. ✓
- Go `openFileWriter` no longer allocates `[]byte` proportional to file size: uses `ReadAt`
  for 2 meta pages (8KB) + `unix.Mmap` for committed range. ✓
- File-backed writer open validates committed tree and derives free set through mmap bytes
  via `open_with_store`/`openWithStore`. ✓
- COW semantics intact: committed pages are `PROT_READ` mmap, dirty pages go to private
  HashMap, pwrite at commit. ✓
- Two-fsync crash-safety preserved: `commit_durable`/`commitDurable` uses same barrier
  ordering. ✓
- All 218 Rust tests pass (104 lib + 1 conformance + 2 metadata_conformance + 111 robustness).
- All Go tests pass (35-38s).
- New `mmap_writer_no_full_file_heap_copy` test proves store is MmapPageStore (not Vec).
- New `mmap_writer_growth_and_remap` test proves growth + remap works.
- New `mmap_writer_reuse_freed_pages` test proves freed page reuse (D7).
- New `mmap_writer_close_releases_lock` test proves exclusive flock is released on close.
- New `mmap_writer_close_closes_fd` test proves `close()` drops the file descriptor immediately.
- New `mmap_writer_page_after_close_rejected` test proves mutating operations after close fail.
- New `mmap_writer_overflow_rejected` test proves `total_pages >= 2^32` is rejected.
- New Rust/Go allocation-boundary tests prove growth refuses before creating an
  unrepresentable `u32` page number.
- New `mmap_writer_shrink_rejected` test proves `grow_file` rejects shrink.
- New `mmap_writer_hostile_meta_rejected` test proves CRC-valid hostile meta is rejected.
- New `mmap_writer_crashed_growth_recovered` test proves trailing junk pages are handled.
- New `mmap_writer_torn_inactive_meta_recovered` test proves torn inactive meta is handled.
- Go gained 4 mmap-writer tests matching Rust parity.

Tests or equivalent validation:

- Rust: `cargo test --manifest-path v4/rust/Cargo.toml --features slow-tests`
  (218 tests).
- Rust: `cargo clippy --manifest-path v4/rust/Cargo.toml --all-targets --all-features -- -D warnings`.
- Rust: `cargo clippy --manifest-path v4/rust/Cargo.toml -p iprange-livedb --no-default-features --features alloc -- -D warnings`.
- Go: `go -C v4/go test -count=1 ./...`.
- Go: `go -C v4/go vet ./...`.
- Formatting / hygiene: `cargo fmt --manifest-path v4/rust/Cargo.toml -- --check`,
  `gofmt -l v4/go`, `git diff --check`.
- Cross-OS compile checks: `GOOS=darwin go -C v4/go test -exec=/bin/true ./...`,
  `GOOS=freebsd go -C v4/go test -exec=/bin/true ./...`.
- Cross-read: existing conformance and metadata_conformance tests pass (Rust reads Go files
  and Go reads Rust files identically).

Real-use evidence:

- Pending (no production deployment yet).

Reviewer findings:

- Round 1 (fable): P0 writer-open file destruction — FIXED and verified. P2/P3 findings
  (Go Commit guard, grow_file shrink, pageData nil, pool-trim nil, test parity, SEEK_HOLE
  ENXIO, trailing-page truncation, closed flag, alloc-only clippy, robustness gating) —
  all FIXED and verified.
- Round 2 (fable): remap-failure-after-commit error message — FIXED. `truncate` dead code
  — documented with `#[allow(dead_code)]`. Stale Validation section — UPDATED.
- All 19 review/fix rounds documented in execution log above.

Same-failure scan:

- Searched both Rust and Go for remaining full-file heap-copy patterns in
  `FileWriter::open` / `openFileWriter`: none found. The `create` paths use only the
  initial 2-page bootstrap image and immediately reopen through the mmap-backed path.
- Searched for file-size-proportional allocation patterns in writer open paths: both Rust
  and Go now use partial mmap (only committed range) instead of full-file heap copy.
- Searched Rust/Go/spec for the removed public `mmap_len()` / `MmapLen()` accessors and
  stale `total_pages ≤ 2^32` wording: none remain outside historical SOW notes.

Sensitive data gate:

- No secrets, credentials, personal data, customer data, or non-private customer-identifying
  IPs in any changed file. All tests use synthetic v4 fixtures and local temp files.

Artifact maintenance gate:

- AGENTS.md: updated project-specific commands with explicit v4 Rust/Go test
  commands used by this SOW.
- Runtime project skills: no update needed (no reusable workflow knowledge established yet).
- Specs: `design-iprange-v4-livedb.md` updated to reflect mmap-backed writer contract,
  private dirty buffers, and `total_pages < 2^32`.
- End-user/operator docs: no public SDK writer behavior documented; no update needed.
- End-user/operator skills: no update needed.
- SOW lifecycle: active in `.agents/sow/current/`.

Specs update:

- `design-iprange-v4-livedb.md` updated in this SOW. It now states that the writer
  mmaps the committed range read-only, keeps dirty pages in private COW buffers, uses
  `pwrite` at commit, and enforces `total_pages < 2^32`.

Project skills update:

- No update needed.

End-user/operator docs update:

- No update needed.

End-user/operator skills update:

- No update needed.

Lessons:

- Keep the commit point as the last fallible durability step. Any ordinary failure
  before the inactive meta fsync is an uncommitted error; after Barrier 2, callers
  must not receive a normal error that looks like the transaction failed.
- Test helpers must not leak into public SDK API. Package/module-private tests can
  inspect private mmap state directly.

Follow-up mapping:

- External reviewers must be run before SOW close.
- Spec update (`design-iprange-v4-livedb.md`) completed in round 15 (item 112).
- CRC32C hardware acceleration deferred to follow-up SOW.
- MAP_SHARED + PROT_WRITE optimization (direct mmap writes instead of COW + pwrite)
  remains a follow-up only if the project wants lower dirty-transaction heap use.
  The current COW + pwrite path is correct for full-file heap avoidance, but a very
  large single transaction can still allocate many dirty-page buffers.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
