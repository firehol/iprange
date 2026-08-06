# SOW-0019 - Rust v4 mmap-only storage rewrite

## Status

Status: in-progress

Sub-state: Full gap analysis and product decisions complete. SOW-0016 is paused;
this is the sole in-progress SOW. Implementation begins with the frozen
mmap-only, isolated-fault-worker, and unchanged reader/reclamation contracts.

## Requirements

### Purpose

Restore the intended v4 architecture: a thin, mmap-native database whose pages
exist and are mutated only in mapped persistent files. Eliminate the positional
file-I/O and page-buffer architecture that currently makes reads slow, copies
pages through application memory, and invalidates the claimed zero-copy and
performance properties.

This is a blocking correction to the Rust-first Phase-1 engine. The Go port
remains blocked until the corrected Rust implementation is complete, proven,
benchmarked, and accepted as suitable for `update-ipsets`.

### User Request

- The SDK must not transfer persistent file contents with file-I/O calls.
- Persistent database and SDK-artifact bytes are accessible only through mapped
  file views.
- Database pages are allocated directly at their final offsets in mapped files.
- Complete page images must not be created or copied into stack buffers, heap
  buffers, caches, or anonymous mappings.
- Copy-on-write construction copies required bytes only between mapped source
  and mapped destination pages.
- Remove the current positional-I/O and external-page-buffer implementation.
- Perform a full architectural gap analysis before implementation because the
  current engine implements the wrong storage model.
- Preserve the established v4 semantics, explicit validation model, durability
  guarantees, bounded resources, Rust-first order, and lean engineering
  philosophy.
- Explicit validation and recovery must contain physical mapped-page failures in
  an SDK-provided isolated worker. On POSIX, its `SIGBUS` handler may claim only
  a kernel fault inside its currently armed SDK mapping; every other `SIGBUS`
  must chain to the exact previous disposition.
- Preserve the already-approved external reader table, transaction-grouped
  retirement, explicit bounded `Reclaim`, and lowest-free-page allocation. Do
  not add an append-only fallback merely because readers exist.

### Exact Meaning Of mmap-only

The phrase "no file I/O" means no content-transfer API against any persistent
artifact owned or consumed by the SDK. The following are forbidden in
production code for the main database, reader sidecar, immutable output,
publication artifacts, recovery sources/outputs, and authorized scratch files:

- `read`, `readv`, `pread`, `preadv`, `write`, `writev`, `pwrite`, `pwritev`;
- Rust `Read`, `Write`, `Seek`, and positional `FileExt` content methods;
- Windows `ReadFile` and `WriteFile` content transfer;
- buffered file I/O, `copy_file_range`, `sendfile`, or equivalent content-copy
  APIs;
- a complete database page represented in an owned `[u8; PAGE_SIZE]`, `Vec`,
  `Box`, heap cache, stack buffer, or anonymous mapping.

Persistent files remain impossible without OS lifecycle and durability calls.
The following are required control operations, not alternative content paths,
and remain allowed:

- open/create/close and retained file or directory handles;
- identity and geometry inspection such as `fstat` or handle metadata;
- checked file extension, sparse preallocation, truncation, and allocation-size
  queries;
- `mmap`/`munmap`/remap and Windows file-mapping equivalents;
- `msync`, `FlushViewOfFile`, `fsync`, `F_FULLFSYNC`, and `FlushFileBuffers`;
- locks, rename/link/unlink, directory enumeration, and directory sync.
- process creation/exec/wait and a small mapped worker-control region used only
  by explicit validation/recovery fault containment.

Small scalar records may be decoded into local values. Bounded metadata
compression workspace is not a page image and remains allowed. Copying live
records or cells from one mapped page directly into another mapped page is
allowed and required for copy-on-write. A complete page must never leave a
file-backed mapping.

### Assistant Understanding

Facts:

- The normative design still describes mapped virtual address space, mapped COW
  capacity, mapped dirty-page synchronization, and mapping-aware growth
  (`.agents/sow/specs/design-iprange-engine.md:242-270`,
  `.agents/sow/specs/binary-format-v4.md:1132-1141`,
  `.agents/sow/specs/binary-format-v4.md:1272-1294`).
- A later assistant-derived SOW decision replaced mapped readers with positional
  reads. It was not recorded as a user decision
  (`.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md:2093-2112`).
- The positional-reader decision entered the active SOW in commit `93ea0ff`, and
  positional-reader language entered the binary spec in commit `8681e7a`.
- The production Rust database code contains no `mmap`, `msync`,
  `MapViewOfFile`, `CreateFileMapping`, or mmap-library use.
- `v4/rust/iprange-livedb/src/file_io.rs:1-103` implements all content access with
  positional reads and writes into caller buffers.
- There are 65 direct calls to that content-I/O layer across 38 non-test-named
  production modules. The calls reach ordinary reads, writers, metadata,
  sidecar coordination, immutable output, publication, validation, recovery,
  and scratch handling.
- There are 441 textual page-array matches across 58 non-test-named production
  modules. Some are borrowed signatures, but many are owned stack/heap pages,
  builders, cursors, return values, and caches.
- The union of modules that directly use positional I/O or page-array APIs is 75
  files and 24,007 physical lines. This is a lower-bound blast radius, not a
  rewrite-size estimate.
- The current zero-allocation benchmark counts only calls through Rust's global
  heap allocator (`v4/rust/iprange-livedb/benches/update_ipsets/allocation.rs:1-62`).
  It cannot see stack page buffers, kernel-to-user copies, positional syscalls,
  or page-cache behavior. The benchmark also authorizes 64 MiB of heap
  (`v4/rust/iprange-livedb/benches/update_ipsets/model.rs:59-69`).
- The active SOW measured 504,138 positional page reads for 100,000 membership
  checks and 302,680 for 100,000 direct checks
  (`.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md:13767-13777`).
- Existing semantic tests remain valuable. Existing I/O, zero-copy, resource,
  and performance acceptance claims do not prove the requested architecture.
- The reader/reclamation behavior is already a resolved user design:
  `.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md:1142-1175`
  defines the free bitmap and external reader table, and
  `.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md:1669-1683`
  defines bounded explicit reclamation.
- The normative form is exact: ordinary commits retire replaced committed
  pages; the external reader table protects generations; `Reclaim` moves only
  complete reader-safe transaction groups into the free bitmap; allocation
  chooses the lowest eligible free page before tail growth
  (`.agents/sow/specs/binary-format-v4.md:890-929`,
  `.agents/sow/specs/binary-format-v4.md:1896-1936`).

Inferences:

- Replacing `file_io.rs` alone cannot satisfy the requirement. The tree/store,
  cursor, builder, validation, recovery, output, and coordination abstractions
  themselves own and pass copied pages.
- The correction should preserve on-disk v4.3 bytes and public semantic APIs
  unless implementation uncovers a real contradiction. This is an internal
  storage rewrite, not permission to redesign v4 behavior.
- Pre-sizing each writable artifact from its existing operation budget and
  mapping that capacity once is the simplest hot-path design. It avoids per-page
  growth syscalls, remap churn, pointer invalidation, and Windows resize
  complexity.
- A narrow private raw-mapping wrapper is required. Exposing whole-file slices
  or raw pointers publicly would spread unsafe lifetime and concurrency rules
  throughout the SDK.
- Live mapped page access must not create an ordinary Rust slice/reference whose
  validity assumes that a corrupt pointer cannot name a concurrently reused
  page. A checked raw `PageView`/`PageMut` access layer decodes bounded scalars
  and copies cells mapped-to-mapped without materializing a page.
- The physical-fault worker must be a separate, version-matched executable
  shipped as part of the SDK. It is not an in-process signal wrapper and it does
  not `longjmp` through Rust frames.

Unknowns:

- None. The user selected the SDK-provided isolated worker and reaffirmed the
  existing reader-table/retirement/reclamation design on 2026-08-06.

### Acceptance Criteria

- Production source contains no persistent-content read/write API and no
  alternate buffered or positional content path.
- Every database page used by an engine algorithm is accessed through a checked
  view of an active file-backed mapping at exactly
  `page_number * PAGE_SIZE`.
- Live mappings do not expose ordinary Rust page slices/references. Checked raw
  accessors decode bounded values or copy directly between mapped regions so a
  corrupt alias cannot create Rust reference aliasing undefined behavior.
- No complete page image exists in production stack/heap buffers, caches, or
  anonymous mappings.
- Readers map exactly the selected committed extent, never the unpublished tail.
- Writable artifacts are extended within their declared growth budgets, mapped,
  and mutated directly; page allocation returns mapped page locations, not page
  arrays.
- COW builders write directly to mapped destination pages and copy only required
  bytes mapped-to-mapped.
- Page checksums are computed and sealed in mapped pages during commit
  preparation, not on each hot-path edit.
- Commit flushes dirty mapped data, synchronizes the retained file, publishes the
  alternate mapped meta page, flushes it, and synchronizes the retained file in
  the specified order on every supported OS.
- Default open remains O(1) plus the bounded reader-table scan and does not run
  full validation.
- Explicit validation and best-effort recovery remain bounded and mmap-only,
  with physical page faults contained by the SDK-provided worker.
- The validation/recovery worker is version matched, single-threaded while a
  mapping probe is armed, and never installed in the caller process.
- Its POSIX handler claims only kernel-generated `SIGBUS` whose `si_addr` lies
  inside the exact currently armed mapping interval. It records the mapping role
  and relative fault offset in a mapped control block and terminates the worker
  without unwinding Rust frames.
- Every unrelated, user-generated, stale-region, out-of-region, nested, or
  otherwise unowned `SIGBUS` is chained to the exact prior disposition with the
  correct one-argument or `SA_SIGINFO` ABI. Default/ignored dispositions are
  restored and redispatched rather than swallowed.
- On Windows, the worker claims only `EXCEPTION_IN_PAGE_ERROR` whose documented
  accessed address lies inside the armed mapping; all other exceptions return
  `EXCEPTION_CONTINUE_SEARCH`.
- The parent restarts from the last sealed recovery checkpoint, reports the
  affected source page/window as physically unreadable, and never publishes a
  partial output. An unrelated worker crash is never mislabeled as source
  unreadability.
- Reader registration, retirement, `Reclaim`, and lowest-free-page allocation
  retain their existing semantics. Allocation does not switch to tail growth
  merely because readers are present.
- The heap page cache and positional-I/O module are deleted after all callers are
  migrated.
- Static and runtime syscall gates prove the prohibition across ordinary reads,
  writes, commit, snapshots, publication, validation, recovery, sidecar, and
  scratch paths.
- Existing semantic, crash, C ABI, conformance, and resource tests pass after
  adaptation to mapped page borrows.
- Warm cache-resident point reads and scans are materially faster than writes.
  The one-million-range direct replacement, retention refresh, and unsigned
  snapshot workflows are re-measured against the user's approximate one-second
  target; results are reported honestly rather than hidden behind relaxed gates.
- Rust is proven locally and cross-compiled for the four supported OS families.
  Runtime cross-platform validation occurs only after explicit authorization to
  access those systems.
- No Go implementation work starts until the corrected Rust engine is accepted.

## Analysis

### Executive Conclusion

The current Rust v4 engine implements the wrong storage architecture.

The on-disk format is still a page-oriented COW format, but the SDK treats it as
a positional-I/O database. Nearly every tree access copies a 4 KiB page into
application memory; mutations build or cache complete pages outside the mapped
file and then write them back. There is no mapping layer.

This explains the read/write inversion observed in the benchmarks. The kernel
page cache already contains the data, but every lookup still enters the kernel
several times and copies cached bytes into temporary page arrays. The 100,000
membership-check run performs approximately five positional page reads per
check. mmap removes those syscalls and copies; a separate application cache is
not the correct architecture.

The error is not isolated. The core storage interfaces encode copied pages, so
the repair touches all storage-facing subsystems. The semantic range algorithms
and on-disk v4.3 layout do not need to be discarded merely because their storage
plumbing is wrong.

### How The Drift Happened

The active SOW identified a real safety concern: an ordinary reader does not run
`Validate`, while corrupt tree pointers might name a page that a writer regards
as free. The assistant selected positional reads as the answer
(`SOW-0016:2093-2112`) without a recorded user decision.

That choice contradicted the established mapped-memory contract rather than
resolving the concurrency invariant. Later performance work then optimized the
consequence by adding a 4 MiB heap page cache
(`SOW-0016:12719-12739`). The cache reduced repeated reads during writes, but it
made the wrong architecture faster instead of restoring the required one.

The current spec is internally contradictory:

- it requires mapped dirty-page flushes at commit
  (`binary-format-v4.md:1132-1141`);
- it budgets mutation against the existing mapping
  (`binary-format-v4.md:1272-1294`);
- it says live readers continue through positional descriptors
  (`binary-format-v4.md:1954-1958`);
- it requires recovery through checked windowed I/O
  (`binary-format-v4.md:2669-2675`, `2690-2697`);
- it says readers must not map the unpublished physical tail
  (`binary-format-v4.md:148-153`).

The first, second, and fifth statements describe the intended mmap design. The
positional-reader and windowed-I/O statements are the drift and must be removed
or rewritten.

### Current Data Path

Ordinary read today:

```text
page number
  -> checked offset
  -> pread / seek_read syscall
  -> kernel page cache copies 4 KiB into a stack/cursor/cache array
  -> parser reads the copied array
```

Required ordinary read:

```text
page number
  -> checked offset inside selected committed mapping
  -> parser reads the mapped page directly
```

Mutation today:

```text
pread source page -> stack/heap page -> rebuild another stack/heap page
  -> optional heap page cache -> pwrite full page -> sync_all at commit
```

Required mutation:

```text
borrow mapped source page -> allocate mapped destination page
  -> build destination in place -> seal mapped dirty pages at prepare
  -> flush mapped dirty pages and publish mapped meta at commit
```

### Quantified Blast Radius

The inventory deliberately excludes files whose names identify them as tests.
Inline `#[cfg(test)]` blocks may still contribute textual matches, so the counts
are evidence of architectural spread, not exact runtime LOC.

| Measure | Result | Meaning |
|---|---:|---|
| Direct positional-I/O calls | 65 | Calls to `read_page`, `read_exact_at`, or `write_exact_at` |
| Direct caller modules | 38 | Storage I/O reaches every major subsystem |
| Page-array textual matches | 441 | Includes borrowed APIs and owned pages |
| Modules with page-array matches | 58 | Page-buffer design is pervasive |
| Union of both module sets | 75 files / 24,007 lines | Lower bound of storage-facing repair surface |
| `iprange-livedb/src` non-test-named source | 202 files / 56,420 lines | Physical source inventory, not pure runtime LOC |
| Rust C API non-test-named source | 29 files / 10,136 lines | Mostly adapts public behavior; storage remains in livedb |

The 24,007-line blast radius does not mean 24,007 lines should be rewritten.
Many parsers already accept borrowed page references and can remain after their
source changes from an owned array to a mapped borrow. The target is to delete
plumbing and caches, not recreate every module.

### Gap Matrix

| Surface | Required architecture | Current evidence | Impact | Required correction |
|---|---|---|---|---|
| Mapping owner | One private checked file-mapping abstraction | No mapping symbols or dependency exist | No mmap at all | Add one narrow raw mapping owner; keep unsafe code inside it |
| Content access | Persistent bytes only through mappings | `file_io.rs:13-103` uses positional reads/writes | Every page crosses a syscall and application buffer | Delete after migrating all callers |
| Meta bootstrap | Read both mapped meta pages, copy only decoded scalars | `bootstrap.rs:96-97` expects page arrays populated elsewhere | Open uses copied pages | Bootstrap directly from the first two mapped pages |
| Tree store | Borrow mapped pages; allocate mapped destination pages | `fixed_tree.rs:68-90` defines read/write through full arrays | Wrong abstraction infects all trees | Replace with checked page borrows and in-place destination access |
| Tree edit | Source and destination remain mapped | `fixed_tree.rs:442-487` creates full source/output arrays | Two page copies per edit path | Build edits directly into allocated mapped pages |
| Slotted pages | Initialize destination mapped page once; append cells in place | `slotted_page.rs:174-225` clears destination and copies cells through builder APIs | Extra page initialization/copies | Retain layout logic but target mapped destination bytes |
| Cell scratch | Scratch bounded to maximum encoded cell | `fixed_tree/page.rs:9-29` reserves `PAGE_SIZE` for one cell | Waste and obscured copy cost | Size scratch to maximum cell, or encode directly when safe |
| Point lookup | Direct mapped traversal | `range_tree.rs:34`, `membership_tree.rs:30-67` allocate stack pages | Several syscalls and copies per point | Traverse mapped page borrows by page number |
| Cursors | Store page/path position only | `range_cursor.rs:43-80`, `range_store_cursor.rs:35-59` own page arrays | Refill/copy on page changes | Cursor retains mapping handle plus page numbers/indices |
| Draft storage | Dirty pages already exist in mapped file | `draft_store/storage.rs:275-308` copies and pwrite-writes pages | Hot path performs content I/O | Allocate and mutate mapped COW pages directly |
| Page cache | Kernel page cache through mmap is the cache | `draft_store/page_cache.rs:11-147` owns page arrays in a `Vec` | Duplicate cache, heap use, page copies | Delete entirely |
| Growth | Preflight checked budget, extend and map capacity before mutation | Current code extends/writes incrementally | Remap and hot-path syscall risk | Pre-size sparse capacity once per transaction/output budget |
| Free-page allocation | Return checked mapped page location | Current `Store::allocate` returns number then callers build arrays | Page exists outside final storage while built | Return page number plus exclusive mapped destination access |
| Checksums | Seal dirty mapped pages once at prepare/commit | Checksum hot path was partly repaired, but pages are still external before persistence | Correct timing, wrong location | Traverse mapped dirty chain and seal in place |
| Commit data | Flush mapped dirty ranges, then retained handle | `live_writer/commit.rs:166-190` calls `sync_all`, writes stack meta positionally, syncs again | Does not implement specified mapped durability protocol | `msync`/view flush data, file sync, mapped meta write, meta flush, file sync |
| Abort | Unmap and truncate unpublished extent; committed mapping unchanged | Current abort reasons over positional writes and file length | Wrong storage state model | Discard mapped draft and restore checked base length |
| Immutable output | Pre-size/map private final inode and construct in place | `immutable_output.rs:334-390` builds stack meta and rereads/rewrites pages | Full extra scan and I/O | Construct/seal entirely in output mapping, then flush/publish |
| Reader sidecar | Fixed-size mapped coordination table | `live_sidecar/header.rs:50-104` builds and transfers page buffers | Coordination violates same rule | Map header/table once; atomic/locked fields operate in mapped bytes |
| Publication artifacts | Fixed or budgeted mapped control files | publication modules directly call `file_io` | Hidden alternate content path | Map reservation, digest, residue, and private output artifacts |
| Validation | Explicit scan over mapped source pages | validation source/context/query/cache modules copy pages | Slow and contradicts mmap-only | Checked mapped traversal; bounded non-page bookkeeping only |
| Recovery | mmap-only source and mapped scratch/output | recovery inspection/scans/scratch directly call `file_io` | Explicit exception to user's rule | Map checked windows or fixed mappings without content I/O; resolve physical-fault contract |
| External sort/scratch | Mapped caller-authorized files | recovery scratch uses read/write helpers | Alternate I/O engine | Pre-size/map scratch files; bounded mapped runs/tables |
| Allocation proof | Distinguish heap, stack, map, faults, syscalls, RSS | allocator hook sees heap calls only | “Zero allocation” was overstated | Add address provenance, syscall, RSS, fault, and mapping metrics |
| C ABI | Preserve semantics; mapped ownership hidden behind Rust handles | C API delegates to current livedb | ABI tests do not prove storage model | Keep ABI unless lifetime contract requires a documented correction |
| Go port | Port only accepted Rust architecture | Go work is intentionally deferred | Porting now would duplicate error | Keep blocked |

### Production Modules With Direct Content I/O

The 38 non-test-named modules fall into these groups:

- Core/bootstrap/tree access: `blob_tree.rs`, `commit_resolution.rs`,
  `database.rs`, `draft_store/storage.rs`, `feed_catalog.rs`, `metadata.rs`,
  `membership_tree.rs`, `range_cursor.rs`, and `range_tree.rs`.
- Live mutation/coordination: `live_writer/create.rs`,
  `live_writer/commit.rs`, `live_sidecar.rs`, and
  `live_sidecar/header.rs`.
- Immutable/publication: `immutable_output.rs`, publication file inspection,
  output, digest, reservation, replacement inspection, residue, GC, and
  maintenance modules.
- Validation: source, context, bitmap query, and bitmap word cache.
- Recovery: inspection, tree/range/membership/metadata scans, scratch, fixed
  scratch, and scratch maintenance.

This grouping is why a helper substitution would be false progress. Each group
also owns page buffers or assumes read/write semantics.

### Corrected Minimal Architecture

#### 1. Mapping owner

Introduce one private mapping module, preferably backed by a pinned version of
`memmap2::MmapRaw` or an equally small platform wrapper. It owns:

- the retained file/handle;
- raw base address and mapped length;
- readable committed length;
- writable capacity when applicable;
- checked page-number-to-offset conversion;
- checked read-only and exclusive mutable page closures/borrows;
- flush-range, flush-file, unmap, and remap lifecycle.

It must not implement `Deref<[u8]>` for the whole file or expose a raw pointer to
public code. No page reference may outlive the mapping, cross a remap, or escape
an operation whose lock protects it.

#### 2. Readers

- Select the two meta pages from a minimal mapping, decode scalar state, and map
  exactly `committed_bytes` for the chosen generation.
- Never map or read an unpublished physical tail as committed data.
- Range, catalog, dictionary, membership, metadata, validation, and cursor code
  traverses checked mapped pages directly.
- Cursors store page numbers, tree paths, slots, and scalar decoded state, not
  copied pages.
- Opening does not validate the page graph by default.

#### 3. Writers

- Preflight `max_file_growth_pages` and arithmetic before mutation.
- Extend/preallocate the sparse file to the maximum authorized transaction
  capacity and map that capacity once before the hot path.
- Allocation chooses an eligible free page or tail page and returns access to
  that mapped destination.
- COW algorithms read mapped source cells and encode directly into mapped
  destination cells. A full-page copy may occur only mapped-to-mapped when the
  algorithm genuinely preserves the full page.
- Maintain dirty-page links in the mapped pages themselves. Do not retain a
  parallel page cache or file-sized dirty structure.
- Prepare validates/seals the mapped dirty chain and completes all fallible work
  before publication.

#### 4. Commit and abort

Commit order remains the specified four-phase publication protocol:

1. Seal dirty mapped non-meta pages and finish fallible preparation.
2. Flush dirty mapped ranges and synchronize the retained file/handle.
3. Encode the complete alternate meta directly into its mapped page.
4. Flush that mapped meta page and synchronize the retained file/handle.

On macOS, the retained file sync is `F_FULLFSYNC`; on Linux/FreeBSD it is
`fsync`; on Windows it is `FlushViewOfFile` followed by `FlushFileBuffers`.
Namespace durability remains separate.

Abort unmaps the writable draft and truncates the unpublished aligned tail back
to the proven base where possible. Truncation never occurs while an affected
mapping remains live.

#### 5. Outputs, sidecar, publication, validation, and recovery

- Immutable/private outputs use their output budget to pre-size/map one final
  private inode and build directly in it.
- Fixed-size sidecar and publication control artifacts are mapped once.
- Validation scans mapped pages and retains only its explicitly bounded graph
  bookkeeping.
- Recovery source windows, output, sort runs, and scratch tables are all
  file-backed mappings. “Windowed” means mapped windows, not read buffers.
- Each remap/window transition invalidates all prior page borrows by construction.

### Performance Consequences

Established measurement, not speculation:

- Current 100,000 membership checks issue 504,138 positional page reads.
- Current 100,000 direct checks issue 302,680 positional page reads.
- The active SOW records 337.5 ms and 187.5 ms respectively for those runs.

Working theory to be proven after the rewrite:

- Warm mapped lookup should eliminate essentially all per-query content syscalls
  and 4 KiB kernel-to-user copies.
- Five cached tree-page touches per query are not intrinsically expensive. The
  current syscall/copy path is the dominant architectural waste.
- Ordered scans should become pointer/offset traversal plus decode and therefore
  be substantially faster than mutation.
- Writes should improve by eliminating full-page reconstruction outside the map,
  pwrite per page, and the heap page cache. Checksumming and durability flushes
  remain real commit costs.

No exact post-rewrite throughput is asserted before measurement. The user's
one-second target for each one-million-item publisher workflow is the design
target, not evidence yet.

### What Existing Evidence Remains Valid

Still useful:

- semantic range normalization and ordered-overwrite tests;
- on-disk encoding/decoding and cross-language corpus tests;
- corruption classifiers and explicit validation expectations;
- crash-state expectations and publication outcome model;
- public Rust and C API behavior tests;
- deterministic workload generators and result verification.

Must be rerun or replaced before acceptance:

- all read/write throughput numbers;
- syscall and descriptor claims tied to positional I/O;
- “zero allocation” claims that did not distinguish page buffers from heap calls;
- RSS and cache claims influenced by the application page cache;
- commit durability evidence that did not flush mapped views as specified;
- recovery failure behavior that depended on `read` returning an ordinary error.

### Resolved Conflict 1: Physical Read Failure

Logical corruption is inspectable through successfully mapped bytes and can
still return detailed best-effort recovery diagnostics. Physical storage failure
is different:

- POSIX permits mapped access to raise `SIGBUS` when the mapped object cannot
  provide the page or was truncated.
- Windows mapped access can raise `EXCEPTION_IN_PAGE_ERROR`.
- Rust cannot portably turn either event into an ordinary `Result` while
  continuing safely in the same process.

The selected contract is an SDK-provided isolated validation/recovery worker.
The caller process receives typed results, but the process that actually touches
the damaged mapping may terminate and restart. This keeps the data path
mmap-only without pretending Rust can safely recover by unwinding across an OS
fault.

The worker uses a staged mapped-scratch protocol:

1. The parent creates a version-matched worker and mapped control block.
2. The worker registers one exact active mapping interval and role before each
   source/scratch/output access that may fault.
3. An owned physical fault records its relative offset in the mapped control
   block and terminates the worker using an OS-safe immediate-exit primitive.
4. The parent waits, verifies the fault record and worker identity, marks the
   affected source page/window unreadable, and restarts from the last sealed
   scratch checkpoint.
5. Recovery builds the final private output only from verified mapped scratch
   state and publishes nothing until the caller's diagnostic sink succeeds.

The POSIX handler is installed only inside the single-purpose worker with
`SA_SIGINFO | SA_ONSTACK`. It requires a kernel-generated `SIGBUS`, an armed
probe, and `si_addr` inside that exact probe interval. It performs no allocation,
locking, formatting, callback, Rust unwinding, or `longjmp`. If the fault is not
ours, it chains the saved prior `sigaction`, preserving whether that action uses
`sa_handler`, `sa_sigaction`, default, or ignore semantics. The handler never
turns an unrelated program fault into database corruption.

Windows uses a worker-local vectored exception handler. It checks
`EXCEPTION_IN_PAGE_ERROR` and the documented accessed-address parameter against
the armed mapping. An unowned exception returns `EXCEPTION_CONTINUE_SEARCH`.

### Resolved Conflict 2: Unvalidated Readers And Free-Page Reuse

The normal open contract intentionally does not run full validation. A damaged
but selectable root can therefore point to a page which the allocation metadata
regards as free. If a writer mutates that mapped page while a Rust reader holds a
shared reference to it, the program violates Rust's aliasing/data-race rules.

The earlier analysis asked the wrong question. This project already has the
required page-lifetime protocol:

- every ordinary commit retires replaced committed pages;
- an external reader-table slot pins each reader's selected transaction;
- the gate makes generation selection plus slot publication atomic against
  commit/reclaim;
- `Reclaim` moves only complete transaction groups which no registered reader
  can observe into the free bitmap; and
- later allocation uses the lowest eligible free bit before tail growth.

No zero-reader requirement, reader-dependent append policy, or
append-now/reclaim-later fallback is added. The normal one-maintenance-operation
retirement delay already defined by the format remains unchanged.

The remaining corruption-safety issue belongs in the mapping abstraction, not
the allocator. Live page parsers use checked raw mapped views rather than
ordinary Rust page slices. They validate local bounds/type/level/born-transaction
fields before following data and never let a page reference escape. A corrupt
pointer may therefore produce a typed local corruption result or an inconsistent
logical read, as already permitted without full validation, but it cannot create
an aliased Rust reference or an out-of-bounds memory access.

### External Reference Evidence

The reference implementations support the corrected design and expose its real
constraints:

- LMDB's `MDB_WRITEMAP` allocator returns page pointers inside the file mapping;
  its non-WRITEMAP mode allocates separate memory. It flushes mapped data with
  `msync` and prevents remap while transactions hold pointers.
- libmdbx documents the same direct-mapped write benefit and warns that small
  growth steps/remaps are costly, especially on Windows. Its implementation uses
  substantial synchronization around remap.
- `memmap2::MmapRaw` provides raw read-only/read-write mappings and explicit
  flushes without exposing a whole-file Rust slice. Its safety documentation
  warns that files modified beneath a mapping require application coordination.
- redb's optimized backend uses positional reads/writes. That design is a useful
  counterexample and is explicitly rejected for this requirement.
- V8's POSIX trap handler first proves the executing context, kernel signal,
  accessed mapping region, and faulting-code region before claiming a signal;
  otherwise it restores the prior handler and redispatches. The v4 worker uses
  the same ownership principle but terminates/restarts instead of modifying a
  Rust execution context.

Official platform contracts:

- POSIX/Linux `mmap`: https://man7.org/linux/man-pages/man2/mmap.2.html
- POSIX `mmap` fault behavior:
  https://www.man7.org/linux/man-pages/man3/mmap.3p.html
- Linux `msync`: https://man7.org/linux/man-pages/man2/msync.2.html
- Windows mapped view flush:
  https://learn.microsoft.com/en-us/windows/win32/api/memoryapi/nf-memoryapi-flushviewoffile
- Windows mapped access errors:
  https://learn.microsoft.com/en-us/windows/win32/memory/reading-and-writing-from-a-file-view
- Windows mapping construction:
  https://learn.microsoft.com/en-us/windows/win32/memory/creating-a-file-view
- macOS `F_FULLFSYNC`:
  https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fcntl.2.html
- POSIX `sigaction` and previous-disposition contract:
  https://pubs.opengroup.org/onlinepubs/9799919799/functions/sigaction.html
- Rust prohibition against `longjmp` skipping Rust frames:
  https://doc.rust-lang.org/reference/behavior-considered-undefined.html
- Windows vectored exception registration:
  https://learn.microsoft.com/en-us/windows/win32/api/errhandlingapi/nf-errhandlingapi-addvectoredexceptionhandler
- Windows `EXCEPTION_IN_PAGE_ERROR` accessed-address parameters:
  https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-exception_record

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The engine's storage interfaces were changed from mapped page access to
  positional I/O as an assistant-derived response to a real concurrency concern.
- That change contradicted the mapped format/resource contract and was not a
  recorded user decision.
- Tree, cursor, writer, validation, recovery, sidecar, and publication code were
  then built around copied page arrays.
- A heap page cache optimized the resulting syscall storm and made the mismatch
  harder to see.
- The implementation must be corrected at the storage-abstraction boundary and
  across every caller. A local optimization cannot meet the requirement.
- Physical-fault containment is resolved as an SDK-provided isolated worker with
  strict mapping-region ownership and signal/exception chaining.
- Free-page reuse is resolved by preserving the existing external reader table,
  retirement tree, explicit bounded reclaim, and lowest-free-page allocator.
  No new append fallback is permitted.

Evidence reviewed:

- `.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md:2093-2112`
- `.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md:12702-12752`
- `.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md:13758-13779`
- `.agents/sow/specs/design-iprange-engine.md:242-279`
- `.agents/sow/specs/binary-format-v4.md:136-165`
- `.agents/sow/specs/binary-format-v4.md:1132-1147`
- `.agents/sow/specs/binary-format-v4.md:1272-1297`
- `.agents/sow/specs/binary-format-v4.md:1940-1958`
- `.agents/sow/specs/binary-format-v4.md:2669-2710`
- `.agents/sow/specs/binary-format-v4.md:890-999`
- `.agents/sow/specs/binary-format-v4.md:1783-1960`
- `v4/rust/iprange-livedb/src/file_io.rs:1-103`
- `v4/rust/iprange-livedb/src/fixed_tree.rs:68-90`
- `v4/rust/iprange-livedb/src/fixed_tree.rs:221-487`
- `v4/rust/iprange-livedb/src/slotted_page.rs:174-225`
- `v4/rust/iprange-livedb/src/draft_store/page_cache.rs:11-147`
- `v4/rust/iprange-livedb/src/draft_store/storage.rs:250-308`
- `v4/rust/iprange-livedb/src/live_writer/commit.rs:155-197`
- `v4/rust/iprange-livedb/src/immutable_output.rs:334-411`
- `v4/rust/iprange-livedb/benches/update_ipsets/allocation.rs:1-62`
- `v4/rust/iprange-livedb/benches/update_ipsets/model.rs:59-76`
- Source-wide `rg` inventories for positional content I/O, mapping symbols, and
  owned/borrowed page arrays.
- Git history searches that locate the positional-I/O drift.
- Official platform documentation linked above.
- Open-source implementation evidence listed below.

Affected contracts and surfaces:

- Binary v4 open, mapping, growth, COW, commit, abort, live-reader, validation,
  recovery, snapshot, and resource contracts.
- Rust database internal storage, page/tree, cursor, writer, sidecar,
  publication, immutable output, validation, recovery, and scratch modules.
- Rust public handles where mapped lifetimes or recovery errors are observable.
- Rust-provided C ABI only if an existing public lifetime/error statement is
  incompatible with the selected physical-fault contract.
- Tests, crash harnesses, conformance corpus openers, benchmarks, syscall gates,
  README/resource claims, project skill, and engineering instructions.
- Go remains out of implementation scope but must later port this accepted
  architecture rather than the current one.

Existing patterns to reuse:

- Existing checked page-number arithmetic and v4.3 codec definitions.
- Existing alternate-meta COW publication and outcome classification.
- Existing dirty-page chain, transaction budgets, cancellation checkpoints,
  reader-table operation gate, retirement metadata, and retained handle/identity
  checks where they remain valid.
- Existing exact reader lifetime protocol: slot lock, selected transaction,
  transaction-grouped retirement, explicit reclaim under the operation gate,
  persistent free bitmap, and lowest-free-page allocation.
- Existing deterministic semantic, corruption, crash, conformance, and
  update-ipsets benchmark workloads.
- Existing explicit validation/recovery APIs and no-default-validation rule.
- LMDB/libmdbx direct mapped-page allocation and pre-sized-map patterns, adapted
  to this much smaller fixed-format engine rather than copied wholesale.

Risk and blast radius:

- Correctness: stale mapping lengths, offset overflow, aliasing, pointer escape,
  and remap lifetime errors can cause memory unsafety or silent corruption.
- Durability: mapped flush and retained-handle sync ordering must match each OS;
  `FlushViewOfFile` alone is insufficient on Windows.
- Concurrency: the existing reader/retirement protocol must not drift. Live
  mapped access must avoid ordinary Rust page references so a corrupt pointer
  cannot manufacture aliasing undefined behavior.
- Recovery: the SDK-provided helper is a shipped executable and protocol. A
  version mismatch, missing helper, failed handler installation, overwritten
  handler, unclassified helper crash, or unsealed checkpoint must fail closed.
- Signals: claiming an unrelated `SIGBUS` would hide a real process defect;
  invoking a saved handler with the wrong ABI or jumping through Rust frames
  would itself be undefined behavior.
- Resource use: pre-sized mappings consume virtual address space proportional to
  authorized capacity, but must not consume equivalent heap or resident memory.
- Compatibility: on-disk v4.3 bytes should remain unchanged; any discovered
  required format change is a stop-and-return design decision.
- Public API: ordinary lookup/scan APIs should remain stable; recovery error and
  transaction-mode behavior may need documentation depending on decisions.
- Performance: removing syscalls/copies should improve hot paths, but incorrect
  flush granularity or remap strategy can regress commits.
- Scope: 75 storage-facing files are implicated. The implementation must reduce
  code and delete obsolete layers rather than add a second engine beside them.
- Rollout: no production format has shipped, so no compatibility layer or
  migration for the current experimental implementation is allowed.

Sensitive data handling plan:

- Work uses repository source, synthetic database fixtures, temporary local
  files, public OS documentation, and public open-source repositories.
- No credentials, customer data, personal data, operational endpoints, or
  production threat-intelligence data are required.
- Durable evidence records upstream repository identities/commits and relative
  paths, never workstation mirror/clone paths or sensitive runtime details.

Implementation plan:

1. Pause SOW-0016 and activate this SOW so only one SOW is in progress. The two
   product decisions are resolved and recorded below.
2. Correct the normative design/binary specs first: exact mmap-only content rule,
   mapping extents, page ownership, growth, commit flush order, recovery fault
   contract, and free-page reuse coordination.
3. Add the narrow private mapping owner and platform durability backend. Add
   compile-time/source bans before migrating callers.
4. Convert bootstrap, ordinary readers, trees, membership/catalog/metadata, and
   cursors to checked mapped page borrows. Prove no page arrays or content
   syscalls remain on read paths.
5. Convert transaction growth/allocation, COW builders, dirty chain,
   checksumming, commit, and abort to direct mapped destinations. Delete the heap
   page cache.
6. Convert immutable output, sidecar, publication, reservation, digest, and GC
   artifacts to mappings.
7. Add the version-matched isolated validation/recovery worker, mapped control
   and sealed checkpoint protocol, POSIX region-owning/chained `SIGBUS` handler,
   and Windows region-owning vectored exception handler. Convert recovery
   source/output, mapped windows, and authorized scratch/sort files.
8. Adapt Rust/C tests and public documentation only where behavior is genuinely
   affected. Do not expand the API spec opportunistically.
9. Run semantic, corruption, crash, conformance, syscall, allocation, benchmark,
   C ABI, sanitizer, and cross-compilation gates. Obtain permission before remote
   runtime checks.
10. Remove `file_io.rs`, page cache, owned production page images, obsolete tests,
    and contradicted performance claims. Audit source graph and line growth.

Validation plan:

- Static source gate rejects forbidden content-I/O symbols and traits in all
  production Rust modules.
- Static source gate rejects owned `PAGE_SIZE` byte arrays/containers outside
  mapping-independent test fixtures; any exception requires explicit evidence
  that it is not a database page image.
- Test-only page-provenance hooks assert that every page supplied to parsers,
  builders, allocators, and checksum code lies inside the active file mapping at
  its exact page offset.
- Linux `strace -yy` or equivalent proves zero `read`, `pread`, `readv`, `write`,
  `pwrite`, or `writev` calls against SDK artifact descriptors during create,
  open, lookup, cursor scan, mutation, commit, abort, snapshot, validation,
  recovery, sidecar, and publication workflows.
- Fault injection covers data sealing, mapped data flush, retained file sync,
  mapped meta write, mapped meta flush, final file sync, unmap, truncation, and
  cleanup.
- Subprocess fault tests truncate or invalidate a mapped source page and prove:
  exact in-region classification; mapped relative offset; no partial output;
  sealed-checkpoint restart; bounded retry; `IO_ERROR` recovery reporting; and
  successful continuation over independently verifiable content.
- Signal-chain tests install one-argument, `SA_SIGINFO`, default, and ignored
  prior dispositions, then prove out-of-region, user-generated, stale-region,
  and unarmed `SIGBUS` are not claimed. The meaningful supported dispositions
  must receive the original signal/context or their exact restored redispatch.
- Handler tests prove no `longjmp`, panic/unwind, allocation, lock, callback,
  formatting, content-I/O call, or non-signal-safe cleanup executes in the
  POSIX handler.
- Windows tests prove only an in-region `EXCEPTION_IN_PAGE_ERROR` is claimed and
  every other code/address returns `EXCEPTION_CONTINUE_SEARCH`.
- Reader/reclamation tests pin old and current generations concurrently, reclaim
  only safe complete transaction groups, reuse the resulting lowest free pages,
  and prove there is no reader-triggered append fallback.
- Tests prove ordinary open does not invoke full validation.
- Existing semantic/conformance/corruption/crash suites pass after adaptation.
- Measure heap calls/bytes, stack use where practical, mapped virtual size, RSS,
  minor/major faults, descriptors, syscalls, residue, and timings separately.
- Warm direct/membership point lookup and ordered scan benchmarks demonstrate
  that reads are materially faster than writes.
- Re-run one-million dispersed direct replacement, retention refresh, and
  unsigned snapshot workloads against the approximate one-second design target.
- Run Rust 1.74, all-feature/no-default-feature, Clippy, rustdoc, source graph,
  ASan/Valgrind where applicable, C ABI suites, and all supported cross-target
  compiles.
- Search the full repository for forbidden APIs and equivalent wrappers before
  closure; do not rely only on known call sites.

Artifact impact plan:

- AGENTS.md: add the exact mmap-only storage invariant and prohibition against
  owned production page images after the design decisions are resolved.
- Runtime project skills: update
  `.agents/skills/project-v4-rust/SKILL.md` with source/syscall/page-provenance
  verification commands and mapped-fault limitations.
- Specs: update `.agents/sow/specs/design-iprange-engine.md` and
  `.agents/sow/specs/binary-format-v4.md`; update `.agents/sow/specs/c-abi-v4.md`
  only if the selected observable error/lifetime contract changes it.
- End-user/operator docs: correct Rust resource/performance/recovery statements
  in the v4 README and any other affected user-facing material.
- End-user/operator skills: none currently exist; verify again at closure.
- SOW lifecycle: SOW-0016 is paused and preserved in `current/`; this SOW is the
  sole in-progress work. SOW-0017 and SOW-0018 remain separate Phase-2 work.

Open-source reference evidence:

- `LMDB/lmdb @ 567292b5d489`
  - `libraries/liblmdb/mdb.c:2680-2909` - WRITEMAP allocation returns mapped
    page pointers; non-WRITEMAP allocates page buffers.
  - `libraries/liblmdb/mdb.c:4893-4916` - mapped meta publication and sync.
  - `libraries/liblmdb/mdb.c:5113-5203` - shared mapping and remap constraints.
- `erthink/libmdbx @ 8a38f3056d9b`
  - `mdbx.h:1176-1227` - direct mapped writes, avoided page copying, and remap
    constraints.
  - `mdbx.h:3470-3503` - growth-step and Windows remap implications.
  - `mdbx.c:20141-20325`, `mdbx.c:31785-32140` - remap synchronization and
    platform complexity.
- `RazrFalcon/memmap2-rs @ d26817827d78`
  - `src/lib.rs:141-147`, `src/lib.rs:1220-1226` - file mutation safety warning.
  - `src/lib.rs:264-282`, `src/lib.rs:634-676` - explicit lengths and raw
    read-only/read-write mappings.
  - `src/lib.rs:1010-1069`, `src/unix.rs:365-379`,
    `src/windows.rs:406-416` - mapped flush APIs and platform behavior.
- `cberner/redb @ f551b96f43d3`
  - `src/tree_store/page_store/file_backend/optimized.rs:63-107`
  - `src/tree_store/page_store/file_backend/optimized.rs:133-199`
  - This positional-I/O backend is a counterexample and is not suitable for the
    mandated v4 architecture.
- `ClickHouse/rust_vendor @ 0b26464adee9`
  - `v8-139.0.0/v8/src/trap-handler/handler-inside-posix.cc:119-241` - proves
    thread state, kernel generation, accessed region, and faulting-code region
    before claiming a signal; restores/redispatches unowned faults.
  - `v8-139.0.0/v8/src/trap-handler/handler-outside-posix.cc:34-95` - saves the
    prior `sigaction`, uses `SA_SIGINFO | SA_ONSTACK`, verifies installation, and
    restores the prior action.

Open decisions:

- None. Decisions 1B and corrected 2A were selected on 2026-08-06.

## Implications And Decisions

### 1. Physical mapped-page failures

Context: logical corruption can return a detailed recovery result. A physical
failure while the OS faults in a mapped page may instead raise `SIGBUS` on
POSIX or `EXCEPTION_IN_PAGE_ERROR` on Windows. There is no portable safe
in-process conversion to a Rust `Result`.

**Decision 1B selected by the user on 2026-08-06: SDK-provided isolated
validation/recovery worker.** This is required to do physical fault recovery
correctly, despite its additional implementation and packaging cost.

Exact decision:

- The SDK, not the caller, owns the versioned worker protocol and executable.
- The caller never installs a process-global `SIGBUS` handler.
- The worker remains mmap-only and transfers no persistent content through
  read/write APIs.
- Worker state, progress, and fault classification use mapped control/scratch
  regions, not pipes carrying database content.
- On POSIX, the handler checks that the signal is kernel generated, a probe is
  armed, and `si_addr` lies inside that exact SDK-owned mapping interval.
- If the address is not in its own active region—or any ownership check
  fails—the handler chains to the saved previous disposition. It never swallows,
  relabels, or exits for an unrelated signal.
- Chaining preserves the previous handler ABI and flags. A saved `SA_SIGINFO`
  handler receives the original three arguments; a one-argument handler receives
  the signal; default/ignored dispositions are restored and redispatched.
- For an owned fault, the handler records only fixed fault facts in the mapped
  control block and immediately terminates the worker. It does not return to the
  faulting instruction, `longjmp`, unwind Rust, run destructors, call user code,
  allocate, lock, format, or perform content I/O.
- The parent accepts a physical-unreadability result only when worker identity,
  control-block generation, active region, exit status, and recorded offset all
  agree. Any other crash remains an unclassified worker failure.
- The parent resumes only from a sealed recovery checkpoint. Partial tree/output
  mutation is discarded and never published.
- On Windows, a worker-local vectored handler claims only
  `EXCEPTION_IN_PAGE_ERROR` with its documented accessed address inside the
  active region; every other exception returns
  `EXCEPTION_CONTINUE_SEARCH`.

Implications:

- The SDK distribution must ship a matching helper executable; absence or
  version mismatch is a typed preflight failure.
- Explicit validation/recovery can survive physical mapped-source faults and
  report their extent. Ordinary lookup/mutation does not acquire a global fault
  handler or pay worker overhead.
- Recovery restart/checkpoint behavior is outside ordinary hot paths and remains
  bounded by the explicit recovery budget.

Risks:

- Signal chaining and checkpoint restart are security/correctness critical.
- Sanitizers, crash reporters, or another handler may replace the worker's
  handler; installation and continued ownership must be verified and failure
  must be closed.
- Platform behavior must be tested natively before support is claimed.

### 2. Free-page reuse with unvalidated live readers

Context: normal readers intentionally do not validate the complete graph. A
corrupt root could therefore reference a page marked free. Concurrent mapped
mutation of that page would violate Rust's read/write aliasing rules.

**Decision 2A selected and corrected by the user on 2026-08-06: preserve the
existing external reader-table/retirement/reclaim design exactly.** The earlier
zero-reader plus append-fallback wording was wrong and is rejected.

Exact decision:

- Every reader pins one selected transaction in the external reader table for
  its complete lifetime.
- Commit retires every replaced committed page under the transaction which made
  it unreachable. It does not place that page directly in the free bitmap.
- `Reclaim` holds the operation gate from the stable reader-table scan through
  publication and selects only complete retirement transaction groups that no
  registered reader can still observe.
- Reclaimed pages enter the persistent free bitmap.
- Later allocation takes the lowest eligible free-page bit first and grows the
  aligned tail only when there is no eligible free page or when the established
  allocator-reserve rule specifically requires tail growth.
- Existing readers do not force an otherwise eligible allocation to append.
- There is no new zero-reader requirement, no waiting for all readers, and no
  append-now/reclaim-later fallback.
- Normal open remains non-validating. The mapping layer prevents Rust aliasing
  by exposing checked raw page views rather than ordinary references into a
  live-mutable file.

Implications:

- The complex reader/retirement work remains useful and authoritative.
- Valid older generations remain readable while a writer progresses.
- Reclamation remains the already-approved explicit bounded maintenance
  operation; this SOW changes storage access, not reclamation policy.

Risk control:

- Tests must prove free pages are reused in lowest-page order while unrelated
  readers remain active whenever the retirement transaction is safe for those
  readers.
- Source and spec searches must reject any new branch that selects tail growth
  solely because active readers exist.

## Plan

1. Pause SOW-0016 and make this the sole in-progress SOW.
2. Freeze the mmap-only, worker-fault, and unchanged reader/reclamation contracts in specs and
   static source gates.
3. Implement and prove the mapping/durability owner in isolation.
4. Migrate read-only bootstrap, tree, lookup, catalog, membership, metadata, and
   cursor paths; remove read buffers and prove zero content syscalls.
5. Migrate direct mapped COW allocation, builders, checksums, commit, and abort;
   remove positional writes and heap page cache.
6. Migrate immutable output, sidecar, publication, validation, recovery, and
   mapped scratch/output paths.
7. Adapt tests/docs/skill, delete obsolete storage code, and run complete
   semantic/durability/resource/performance/portability validation.
8. Present measured Rust suitability evidence for acceptance before any Go port.

## Execution Log

### 2026-08-06

- Performed read-only source, spec, SOW-history, benchmark, and git-history
  analysis.
- Inventoried positional-I/O calls, page-array usage, affected modules, and
  physical source size.
- Reviewed official Linux/POSIX, Windows, and macOS mapping/durability contracts.
- Reviewed pinned LMDB, libmdbx, memmap2, and redb implementations as evidence.
- Created this pending SOW. No production code, tests, specs, or active SOW state
  changed.
- Recorded the user's decisions: SDK-provided isolated physical-fault worker with
  strict owned-region SIGBUS chaining; preserve the existing external reader
  table, retirement, reclaim, and free-page reuse without an append fallback.
- Re-read the existing reader/reclamation contract and corrected the earlier
  false framing. Added official POSIX/Rust/Windows fault-handling evidence and a
  V8 signal-ownership reference.
- Updated the normative format, engine, C-ABI, repository instructions, and
  runtime Rust skill with the frozen mmap-only and isolated-fault-worker
  contracts.
- Added `v4/rust/check-mmap-storage.sh`. Its initial run fails on the old source
  by design and reports the forbidden content-I/O and complete-page ownership
  that this SOW must remove.
- Verified the SOW audit, shell syntax, and diff hygiene after the contract
  transition.
- Replaced the positional content-I/O layer and heap page cache with one private
  file-backed mapping owner plus checked raw mapped byte/page views. Migrated
  bootstrap, readers, writers, trees, sidecar coordination, immutable output,
  publication, validation, recovery, and mapped scratch callers to that owner.
- Writers now reserve and map their authorized transaction extent before the hot
  path, construct COW pages at their final mapped offsets, seal checksums during
  commit preparation, flush mapped dirty state, and retain the existing
  transaction-grouped retirement/reclaim and lowest-free-page behavior.
- Deleted `file_io.rs` and `draft_store/page_cache.rs`. The static mmap-only gate
  now passes across 233 production Rust files.
- Corrected two mapped-edit regressions found by the existing suite: duplicate
  leaf inspection during fixed-tree mutation and stale bytes left by shrinking
  slotted-page edits. Added permanent coverage for both.
- Replaced the membership-import test's obsolete physical-truncation injection
  with mapped logical page corruption. Physical truncation belongs to the
  isolated validation/recovery worker; ordinary import remains worker-free by
  the frozen architecture.
- The complete all-feature Rust workspace now passes, including Rust library,
  integration, conformance, Rust-provided C ABI, and native C tests. Warnings-
  denied all-target Clippy passes, and the source graph cross-compiles 315
  sources for Linux, Windows, macOS, and FreeBSD. The worker, syscall evidence,
  and performance measurements remain pending.

## Validation

Acceptance criteria evidence:

- Core mmap-only storage migration is implemented and source-gated. The isolated
  fault worker and final resource/performance/portability evidence remain
  pending.

Tests or equivalent validation:

- `cargo test --manifest-path v4/rust/Cargo.toml` passes the complete default
  workspace: 321 Rust library tests pass with two intentional subprocess entry
  points ignored, and every integration, conformance, Rust-provided C ABI, and
  native C test passes.
- `cargo test --manifest-path v4/rust/Cargo.toml --all-features` passes the same
  complete workspace.
- Warnings-denied workspace/all-feature/all-target Clippy passes.
- `./v4/rust/check-source-graph.sh` passes: 315 sources compile across the four
  supported target families, including the runtime-built native fixture.
- `./v4/rust/check-mmap-storage.sh` passes across 233 production Rust files.
- Rust formatting, `git diff --check`, and the SOW audit pass.

Real-use evidence:

- Existing benchmark traces prove the former positional-read storm. Corrected
  mmap performance has not yet been remeasured, so no speed claim is made here.

Reviewer findings:

- External review is intentionally not used. The user requested autonomous work
  without subagents or external reviewers.

Same-failure scan:

- Completed across the Rust v4 database source. The issue affects ordinary I/O,
  copied page ownership, sidecar/publication artifacts, validation, recovery,
  and scratch—not only point lookup.

Sensitive data gate:

- This SOW contains only source references, synthetic benchmark facts, public
  platform documentation, and public upstream identities. It contains no raw
  secrets, credentials, customer/personal data, private endpoints, or
  proprietary incident details.

Artifact maintenance gate:

- AGENTS.md: updated with the exact mmap-only and page-ownership invariant.
- Runtime project skills: updated with the mmap-only verification gate and
  physical-fault boundary.
- Specs: format, engine, and C-ABI contracts now record the approved mmap-only,
  worker-fault, and unchanged reader/reclamation architecture.
- End-user/operator docs: affected claims identified; correction awaits
  implementation evidence.
- End-user/operator skills: none currently exist; recheck at closure.
- SOW lifecycle: in progress under `current/`; SOW-0016 is paused and this SOW
  is the sole executing work.

Specs update:

- Updated before implementation with the approved architecture. Final evidence
  corrections remain part of SOW closure.

Project skills update:

- Updated with the current source gate; final runtime verification commands will
  be added after worker and syscall gates are complete.

End-user/operator docs update:

- Required for current resource, performance, and recovery claims after measured
  corrected behavior exists.

End-user/operator skills update:

- No such skills currently exist.

Lessons:

- A performance workaround must not replace an explicit storage architecture.
- Heap-allocation counting does not prove zero-copy page access.
- A safety conflict must be resolved as a coordination invariant, not by silently
  changing the user's format architecture.

Follow-up mapping:

- No work is deferred from this SOW. Signing remains tracked by SOW-0017 and
  high-level feed algebra by SOW-0018; both remain Phase 2.

## Outcome

Pending.

## Lessons Extracted

Pending implementation completion.

## Followup

None yet.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated
`## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend
regression content above the original SOW narrative.
