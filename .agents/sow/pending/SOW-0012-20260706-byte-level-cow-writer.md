# SOW-0012 - Byte-level COW: eliminate per-record heap allocations on the writer hot path

## Status

Status: open

Sub-state: Pending design discussion. Created from the reader/writer whole-code review
(SOW-0011 round 19). Not started — SOW-0011 must close first (one SOW at a time).

## Requirements

### Purpose

Eliminate the dominant per-operation heap allocation cost in the v4 writer's COW
(copy-on-write) mutation path. Today, every leaf/branch touch materializes *all* records
of the touched page into owned structs (`OwnedRecord<K>` / `ownedRecord`, each with a
`Vec<u8>`/`[]byte` scope), edits one, then re-serializes all of them. For IPv4 with
`scope_width=1`, a full leaf holds ~510 records → **510 separate heap allocations** per
leaf touch, even when only one record changes. This is the single biggest performance
bottleneck in the library and the main obstacle to "absolutely optimal" write throughput.

### User Request

From the SOW-0011 whole-code review (user-directed): "review the reader and writer as a
whole ... identify performance issues, smelly code, bad practices, unnecessary
allocations, edge cases." The review identified the COW-path allocation storm as P1
(shared across Rust + Go). User selected option "C" = fix quick wins now + create a SOW
for byte-level COW.

### Assistant Understanding

Facts:

- Records are **fixed-size** within a file (`record_size = key_width + scope_width +
  value_width`, computed at create, immutable per file). This makes byte-level page
  manipulation straightforward: insert/delete is a `memmove` within the 4 KiB page buffer.
- Branch entries are also fixed-size: `key_width` separators + 4-byte child pointers.
- The current `read_leaf`/`readLeaf` → materialize-all → `write_leaf`/`writeLeaf` →
  reserialize-all pattern allocates `count` owned records (each with a scope clone) on
  every COW. `cow_insert`, `cow_delete`, `rebalance`, `split`, `borrow`, `merge` all pay
  this.
- The `Cursor` already proves the fixed-size-record model works: it reads records
  directly from the page buffer by index, zero-allocation.
- The dirty page buffer pool (`MmapPageStore.pool`) already recycles 4 KiB buffers — the
  byte-level approach reuses this; no new allocation infrastructure is needed.

Inferences:

- A byte-level COW (`get mutable page buffer → memmove to make room/close gap → write the
  one record in place → update entry_count → recompute CRC`) eliminates ~99% of write-path
  heap traffic. The only allocation is the dirty page buffer itself, which is pooled.
- Because records are fixed-size, there is no fragmentation/compaction concern within a
  page — offsets are pure arithmetic (`base = header + i * record_size`).
- Split/borrow/merge become two-buffer memmoves instead of two materializations.

Unknowns:

- Exact performance headroom (must measure before/after with the v4 benchmark harness).
- Whether the `OwnedRecord`/`ownedRecord` abstraction should be removed entirely or kept
  as a read-only view (zero-copy borrow from the page buffer) for non-mutation paths.

### Acceptance Criteria

- `cow_insert` / `cow_delete` / `rebalance` / `split` / `borrow` / `merge` perform **zero**
  per-record heap allocations (measured: no `Vec::new`/`make` per record on the path).
- The only allocation on the COW path is the dirty page buffer (pooled).
- All existing v4 tests pass (Rust: lib + conformance + metadata + robustness 111; Go:
  full suite) with no behavioral change.
- Cross-language parity: Rust and Go use the same byte-level approach and pass the shared
  conformance corpus.
- Benchmark evidence: a `set()` touching one record in a full leaf allocates ≤ the dirty
  page buffer (one pooled 4 KiB buffer), not 510+ scope Vecs.
- Spec note added if the page-edit invariants (fixed record_size, memmove semantics)
  become a format-level guarantee worth recording.

## Analysis

Sources checked:

- `v4/rust/iprange-livedb/src/writer.rs` — `read_leaf`/`write_leaf`/`read_branch`/
  `write_branch`/`cow_insert`/`cow_delete`/`rebalance`/`split`/`borrow`/`merge` (~1100-1450)
- `v4/go/writer.go` — same functions (~1200-1450)
- `v4/rust/iprange-livedb/src/cursor.rs` — zero-alloc `LeafView`/`BranchView` record access
  by index (the template for byte-level reads)
- `v4/rust/iprange-livedb/src/page_store.rs` — `write_page_mut` + buffer pool
- `v4/rust/iprange-livedb/src/spec.rs` — fixed `record_size`, `leaf_max`, `branch_max`

Current state:

- COW path materializes all records of the touched page into owned structs, every touch.
- For IPv4 full leaf: 510 × `Vec<u8>` scope allocations + 1 × `Vec<OwnedRecord>` + 1 ×
  `Vec<K>` (branch seps) + 1 × `Vec<u32>` (branch children). Re-serialized on write,
  allocating again.
- The `Cursor` proves fixed-size record access by index is zero-alloc and clean.

Risks:

- **High blast radius**: touches the core mutation logic in both languages. A subtle bug
  in memmove bounds or entry_count update corrupts the tree silently.
- **Mitigation**: the 111 robustness fuzz tests + conformance corpus exercise these paths
  heavily. Byte-level COW must pass all of them unchanged. Add targeted tests for
  memmove-at-boundaries (insert at index 0, insert at last index, delete single, delete
  all, split exactly at half, borrow exactly one, merge two full).
- **Cross-language drift risk**: must keep Rust and Go byte-for-byte equivalent in the
  page-edit primitives. The conformance corpus catches output divergence but not
  algorithmic divergence; a shared comment block documenting the invariants is needed.

## Pre-Implementation Gate

Status: blocked (SOW-0011 not yet closed; design needs user discussion)

Problem / root-cause model:

- The materialize-all-then-reserialize pattern is an abstraction choice (`OwnedRecord`)
  inherited from the in-memory-only writer. It is not required by the format — the format
  stores fixed-size records in fixed slots. The mmap-backed store exposes mutable page
  buffers directly (`write_page_mut`), making byte-level edits natural.

Evidence reviewed:

- `writer.rs:1114-1128` (`read_leaf` — `scope: r.scope().to_vec()` per record)
- `writer.rs:1130-1137` (`read_branch` — two collects)
- `writer.rs:1088-1112` (`write_leaf`/`write_branch` — reserialize)
- `cursor.rs` `LeafView::record(i)` — zero-alloc indexed record access (the template)
- SOW-0011 round-19 whole-code review (Rust + Go subagent reports)

Affected contracts and surfaces:

- Internal: `cow_insert`, `cow_delete`, `rebalance`, `split`, `borrow`, `merge`,
  `read_leaf`, `write_leaf`, `read_branch`, `write_branch`, `OwnedRecord`/`ownedRecord`.
- Public API: unchanged (set/delete/commit behave identically).
- Format: unchanged (byte-identical output required; conformance corpus enforces).
- Specs: possibly a note on fixed-record-size page-edit invariants.

Existing patterns to reuse:

- `Cursor`'s `LeafView`/`BranchView` indexed record access (zero-alloc).
- `MmapPageStore::write_page_mut` + buffer pool (already recycles 4 KiB buffers).
- `partition_idx`/`partitionIdx` for in-page binary search.

Risk and blast radius:

- High. Core mutation logic, both languages. Silent tree corruption on any bounds bug.
- Mitigated by 111 robustness tests + conformance corpus + new boundary tests.

Sensitive data handling plan:

- No secrets/PII involved. Test fixtures are synthetic.

Implementation plan (high-level — to detail at execution):

1. Add zero-alloc indexed record setters to `LeafView`/`BranchView` (or a mutable
   counterpart): `set_record(i, from, to, scope)`, `insert_record(i, ...)`,
   `remove_record(i)`, `set_sep(i, k)`, `set_child(i, pgno)` — each a memmove + write.
2. Rewrite `cow_insert`/`cow_delete` to operate on the mutable page buffer directly.
3. Rewrite `split`/`borrow`/`merge` as two-buffer memmoves.
4. Remove or repurpose `read_leaf`/`write_leaf`/`read_branch`/`write_branch` and
   `OwnedRecord`/`ownedRecord` (keep as read-only borrow view if useful for non-mutation
   paths like `scan`/`lookup`).
5. Add boundary tests (insert/delete at index 0/last; split/borrow/merge edge cases).
6. Cross-language parity check + conformance corpus.
7. Benchmark before/after.

Validation plan:

- All existing tests pass (Rust 216 + Go full).
- New boundary tests for memmove edges.
- Conformance corpus cross-read unchanged.
- Benchmark: per-`set` allocation count before/after (target: 0 per-record allocs).

Artifact impact plan:

- `AGENTS.md`: no change (build/test commands unchanged).
- Runtime project skills: none yet (project grows skills incrementally).
- Specs: possibly a note on fixed-record-size page-edit invariants.
- End-user/operator docs: no change (behavior identical; this is an internal optimization).
- SOW lifecycle: this pending SOW; SOW-0011 closes first.

Open decisions (need user input before execution):

1. Remove `OwnedRecord`/`ownedRecord` entirely, or keep as a read-only borrow view?
2. Should the byte-level primitives live in `cursor.rs`/`cursor.go` (next to the existing
   views) or a new `page_edit.rs`/`page_edit.go` module?
3. Priority/scheduling relative to other pending SOWs (0001 engine, 0004 parser, 0005 perf).
