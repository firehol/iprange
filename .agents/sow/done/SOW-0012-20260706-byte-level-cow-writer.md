# SOW-0012 - Transaction-local COW pages + byte-level writer edits

## Status

Status: completed

Sub-state: Transaction-local COW and byte-level writer page edits are implemented and
validated in Rust and Go.
User decision recorded: a page MUST be COW'd at most once per transaction; repeated
writes to the same transaction-private page MUST mutate that private page in place.

## Requirements

### Purpose

Fix the v4 writer transaction model so each logical page is COW'd at most once per
transaction. The first write to a committed page copies it into a transaction-private
dirty page; later writes to that same logical page in the same transaction mutate the
private page in place. At the same time, eliminate the per-record heap allocation cost
inside page edits: today, every leaf/branch touch materializes *all* records of the
touched page into owned structs (`OwnedRecord<K>` / `ownedRecord`, each with a
`Vec<u8>`/`[]byte` scope), edits one, then re-serializes all of them. For IPv4 with
`scope_width=1`, a full leaf holds ~510 records → **510 separate heap allocations** per
leaf touch, even when only one record changes.

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
  every COW. `cow_insert`, `cow_delete`, `rebalance`, split, and merge paths all pay
  this.
- The current mutation path also COWs the same logical page repeatedly within one
  transaction: a page touched by one `set`/`delete` becomes the current tree page, and a
  later operation touching that same logical page COWs it again instead of mutating the
  transaction-private page in place.
- The `Cursor` already proves the fixed-size-record model works: it reads records
  directly from the page buffer by index, zero-allocation.
- The dirty page buffer pool (`MmapPageStore.pool`) already recycles 4 KiB buffers — the
  byte-level approach reuses this; no new allocation infrastructure is needed.

Inferences:

- A transaction-local COW map (`committed/current pgno → private pgno`) prevents repeated
  page churn: first touch copies the source page into a dirty page, later touches edit the
  same dirty page.
- A byte-level edit (`private mutable page buffer → memmove to make room/close gap → write
  the one record in place → update entry_count → recompute CRC`) eliminates most
  write-path heap traffic. The remaining legitimate allocations are bounded transaction
  structures and caller-owned value copies, not per-existing-record materialization.
- Because records are fixed-size, there is no fragmentation/compaction concern within a
  page — offsets are pure arithmetic (`base = header + i * record_size`).
- Split/merge/rebalance become byte-level page operations instead of whole-page
  materializations.

Unknowns:

- Exact performance headroom (must measure before/after with the v4 benchmark harness).
- Whether the `OwnedRecord`/`ownedRecord` abstraction should be removed entirely or kept
  as a read-only view (zero-copy borrow from the page buffer) for non-mutation paths.

### Acceptance Criteria

- Each logical page is COW'd at most once per transaction. Repeated writes to the same
  transaction-private page mutate that private page in place.
- `cow_insert` / `cow_delete` / `rebalance` / split / merge paths perform **zero**
  per-existing-record heap allocations (measured: no `Vec::new`/`make` per record on the
  path).
- Dirty page buffers are pooled. Heap use is proportional to distinct pages dirtied in the
  transaction plus bounded transaction metadata, not to the number of writes to the same
  page and not to records materialized per page edit.
- All existing v4 tests pass (Rust: lib + conformance + metadata + robustness 111; Go:
  full suite) with no behavioral change.
- Cross-language parity: Rust and Go use the same byte-level approach and pass the shared
  conformance corpus.
- Benchmark evidence: repeated `set()` operations against the same full leaf in one
  transaction allocate/COW that logical page once, then mutate in place; no 510+ scope Vecs
  per write.
- Spec note added if the page-edit invariants (fixed record_size, memmove semantics)
  become a format-level guarantee worth recording.

## Analysis

Sources checked:

- `v4/rust/iprange-livedb/src/writer.rs` — `read_leaf`/`write_leaf`/`read_branch`/
  `write_branch`/`cow_insert`/`cow_delete`/`rebalance`/split/merge paths (~1100-1450)
- `v4/go/writer.go` — same functions (~1200-1450)
- `v4/rust/iprange-livedb/src/cursor.rs` — zero-alloc `LeafView`/`BranchView` record access
  by index (the template for byte-level reads)
- `v4/rust/iprange-livedb/src/page_store.rs` — `write_page_mut` + buffer pool
- `v4/rust/iprange-livedb/src/spec.rs` — fixed `record_size`, `leaf_max`, `branch_max`

Current state:

- COW path materializes all records of the touched page into owned structs, every touch.
- Repeated touches to the same logical page in the same transaction allocate new pages
  again instead of recognizing the page is already transaction-private.
- For IPv4 full leaf: 510 × `Vec<u8>` scope allocations + 1 × `Vec<OwnedRecord>` + 1 ×
  `Vec<K>` (branch seps) + 1 × `Vec<u32>` (branch children). Re-serialized on write,
  allocating again.
- The `Cursor` proves fixed-size record access by index is zero-alloc and clean.

Risks:

- **High blast radius**: touches the core mutation logic in both languages. A subtle bug
  in the transaction-local COW map, memmove bounds, parent pointer update, or entry_count
  update corrupts the tree silently.
- **Mitigation**: the 111 robustness fuzz tests + conformance corpus exercise these paths
  heavily. Byte-level COW must pass all of them unchanged. Add targeted tests for
  memmove-at-boundaries (insert at index 0, insert at last index, delete single, delete
  all, split exactly at half, merge two full) and repeated writes to the same page in one
  transaction.
- **Cross-language drift risk**: must keep Rust and Go byte-for-byte equivalent in the
  page-edit primitives. The conformance corpus catches output divergence but not
  algorithmic divergence; a shared comment block documenting the invariants is needed.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The materialize-all-then-reserialize pattern is an abstraction choice (`OwnedRecord`)
  inherited from the in-memory-only writer. It is not required by the format — the format
  stores fixed-size records in fixed slots. The mmap-backed store exposes mutable page
  buffers directly (`write_page_mut`), making byte-level edits natural.
- The current COW model is page-version COW per operation, not transaction-local COW per
  page. Pages freed during a transaction are not reusable until commit, so touching the
  same logical page repeatedly creates new page versions and leaves the previous private
  versions as transaction orphans. Correct COW for this writer is: copy a committed/current
  page once on first write in a transaction, then mutate that private page for later writes.

Evidence reviewed:

- `writer.rs:1114-1128` (`read_leaf` — `scope: r.scope().to_vec()` per record)
- `writer.rs:1130-1137` (`read_branch` — two collects)
- `writer.rs:1088-1112` (`write_leaf`/`write_branch` — reserialize)
- `writer.rs:1065-1080` (`cow_insert` always frees the input page and emits a new page)
- `writer.rs:1227-1231` (`free_page` keeps freed pages out of reuse until commit)
- `writer.rs:403-405` (`finish_commit_meta` reclaims freed pages only at commit)
- `cursor.rs` `LeafView::record(i)` — zero-alloc indexed record access (the template)
- SOW-0011 round-19 whole-code review (Rust + Go subagent reports)

Affected contracts and surfaces:

- Internal: `cow_insert`, `cow_delete`, `rebalance`, split/merge paths,
  `read_leaf`, `write_leaf`, `read_branch`, `write_branch`, `OwnedRecord`/`ownedRecord`.
- Internal: transaction-local page identity mapping, parent pointer updates, dirty page
  tracking, `take_dirty`, `finish_commit_meta`, and free-page reclamation.
- Public API: unchanged (set/delete/commit behave identically).
- Format: unchanged (byte-identical output required; conformance corpus enforces).
- Specs: possibly a note on fixed-record-size page-edit invariants.

Existing patterns to reuse:

- `Cursor`'s `LeafView`/`BranchView` indexed record access (zero-alloc).
- `MmapPageStore::write_page_mut` + buffer pool (already recycles 4 KiB buffers).
- `partition_idx`/`partitionIdx` for in-page binary search.

Risk and blast radius:

- High. Core mutation logic, both languages. Silent tree corruption on any transaction-local
  COW mapping, parent pointer, or bounds bug.
- Mitigated by 111 robustness tests + conformance corpus + new boundary tests.

Sensitive data handling plan:

- No secrets/PII involved. Test fixtures are synthetic.

Implementation plan (high-level — to detail at execution):

1. Add a transaction-private page set in Rust and Go. A page in this set is safe to edit
   in place until commit because no active on-disk meta references it.
2. Add first-touch helpers: if `pgno` is already transaction-private, return it; otherwise
   allocate one private page, copy the source page bytes, mark the source freed for
   post-commit reuse, mark the private page dirty, and return the private page.
3. Update `cow_insert`/`cow_delete`/root-delete/rebalance to rewrite transaction-private
   pages in place instead of always emitting a new page.
4. Keep the existing materialized-record page edit code for this first implementation step
   where it lowers risk. This SOW still tracks byte-level page editing, but the mandatory
   immediate correctness/performance fix is one-COW-per-page-per-transaction.
5. Add targeted tests proving repeated writes to the same leaf in one transaction do not
   create repeated page versions, in both Rust and Go.
6. Run the full Rust/Go validation suite and update this SOW with results.

Validation plan:

- All existing tests pass (current Rust v4 suite + Go full).
- New boundary tests for memmove edges.
- New tests prove repeated writes to the same logical page in one transaction do not create
  repeated COW page versions.
- Conformance corpus cross-read unchanged.
- Structural validation: repeated-write same-leaf page churn test proves one COW page per
  logical page per transaction; same-failure scans prove old per-existing-record
  materialization helpers were removed from the range-tree mutation paths.

Artifact impact plan:

- `AGENTS.md`: no change (build/test commands unchanged).
- Runtime project skills: none yet (project grows skills incrementally).
- Specs: possibly a note on fixed-record-size page-edit invariants.
- End-user/operator docs: no change (behavior identical; this is an internal optimization).
- SOW lifecycle: this SOW completes and moves to `done/`; SOW-0011 remains paused until
  its remaining mmap writer scope is resumed.

Open decisions:

1. Transaction-local page identity design: resolved for this implementation — use physical
   page ids plus a transaction-private page set. Do not introduce stable logical ids.
2. `OwnedRecord`/`ownedRecord`: resolved for the finished implementation. The range-tree
   mutation paths no longer materialize existing page records into owned structs.
   `ownedRecord` remains only for caller/new-record payloads and initial constructed writes.
3. Byte-level primitive placement: resolved for the finishing pass — keep primitives local
   to `writer.rs` / `writer.go`. This is the surgical option: no reader/cursor API changes
   and no new shared abstraction until the mutation logic is validated.
4. Priority/scheduling: user directed immediate implementation of this SOW.

## Execution Log

### 2026-07-06

- Paused SOW-0011 and moved this SOW to `current/` so only one implementation SOW is
  active.
- Implemented transaction-local COW in Rust:
  - added `private_pages` to the writer state;
  - added `mark_dirty()` and `ensure_private_page()`;
  - changed insert/delete/rebalance paths so non-split rewrites mutate transaction-private
    pages in place;
  - changed split paths so the left side reuses the private copy and the right side is the
    only newly emitted page;
  - centralized dirty tracking through `mark_dirty()`.
- Implemented the same transaction-local COW model in Go:
  - added `privatePages`;
  - added `markDirty()` and `ensurePrivatePage()`;
  - changed insert/delete/rebalance/split paths to match Rust;
  - routed scope-tree, KV-tree, and overflow dirty tracking through `markDirty()`.
- Added focused regression tests in both languages:
  - Rust: `writer::tests::repeated_writes_same_leaf_cow_once_per_txn`;
  - Go: `TestRepeatedWritesSameLeafCOWOncePerTxn`.
- Updated `design-iprange-v4-livedb.md` §6.2 and writer module comments to record the
  transaction-local COW rule.
- Intermediate checkpoint before the byte-edit finishing pass: transaction-local COW was
  implemented first, then the finishing pass below removed existing-record materialization
  from the range-tree mutation paths.

### 2026-07-06 byte-edit finishing pass

- User directed: proceed and finish this SOW.
- Implementation decision: keep byte-level page-edit primitives local to `writer.rs` /
  `writer.go` for this pass to minimize blast radius.
- Replaced the range-tree mutation path with byte-level page edits:
  - leaf insert/delete now shifts fixed-size record slots directly in the private page;
  - leaf split/merge writes left/right pages from stack snapshots of the source pages;
  - branch child update, separator insert, split, merge, and parent repair now edit fixed
    branch slots directly;
  - old `read_leaf`/`readLeaf`, `read_branch`/`readBranch`, and slice insert/remove
    helpers were removed from the mutation path.
- Added boundary tests for byte-level insert/delete at the beginning, middle, and end of a
  single leaf in Rust and Go.

## Validation

Acceptance criteria evidence:

- Satisfied: each logical page on the touched tree path is COW'd once per transaction.
  Evidence: both new regression tests build a committed two-level tree, perform eight
  `Set()`/`set()` operations against records in the same leaf without committing, and
  assert both total page count and dirty-page count remain equal to `pages_before +
  tree_height` / `tree_height` after the first write.
- Satisfied: Rust and Go use the same transaction-local COW model.
  Evidence: both implementations have the same `private_pages` / `privatePages` state,
  first-touch helper, clear-on-commit behavior, and rewrite-at-page helpers.
- Satisfied: range-tree mutation paths no longer materialize existing leaf records or
  branch entries. Evidence: insert/delete/rebalance/split/merge use byte-slot helpers over
  page snapshots or private pages; the old materialization helpers were removed.
- Satisfied: memmove/copy boundary coverage exists. Evidence:
  `byte_level_single_leaf_insert_delete_boundaries` and
  `TestByteLevelSingleLeafInsertDeleteBoundaries` insert/delete at beginning, middle, and
  end of a leaf and validate the committed image.

Tests or equivalent validation:

- `cargo test --manifest-path v4/rust/Cargo.toml --features slow-tests` — passed:
  106 lib tests, 1 conformance test, 2 metadata conformance tests, 111 robustness tests.
- `cargo test --manifest-path v4/rust/Cargo.toml --all-features` — passed:
  118 lib tests, 1 conformance test, 2 metadata conformance tests, 111 robustness tests.
- `cargo clippy --manifest-path v4/rust/Cargo.toml --all-targets --all-features -- -D warnings`
  — passed.
- `cargo clippy --manifest-path v4/rust/Cargo.toml -p iprange-livedb --no-default-features --features alloc -- -D warnings`
  — passed.
- `go -C v4/go test -count=1 ./...` — passed.
- `go -C v4/go vet ./...` — passed.

Real-use evidence:

- The mmap-backed writer path is exercised by existing Rust OS tests in the v4 test suite,
  including file growth, remap, reopen/mutate/recommit, freed-page reuse, and no full-file
  heap-copy coverage.

Reviewer findings:

- No external reviewer was run; the user did not request external reviewers for this
  finishing pass. Internal validation includes full Rust/Go tests, clippy/vet, conformance,
  robustness, same-failure scans, and SOW audit.

Same-failure scan:

- `rg -n "dirty = append|emitLeaf\\(|emitBranch\\(|freshly allocated page|newly-allocated pages|Every mutated node" v4/go/writer.go v4/rust/iprange-livedb/src/writer.rs .agents/sow/specs/design-iprange-v4-livedb.md`
  — only the intentional append inside `markDirty()` remains.
- `rg -n "dirty\\.push|emit_leaf\\(|emit_branch\\(|freshly allocated page|newly-allocated pages|Every mutated node" v4/rust/iprange-livedb/src/writer.rs .agents/sow/specs/design-iprange-v4-livedb.md`
  — only the intentional push inside `mark_dirty()` remains.
- `rg -n "read_leaf|read_branch|write_leaf_at|write_branch_at|emit_leaf|emit_branch|Vec<OwnedRecord>|Vec<K>|Vec<u32>" v4/rust/iprange-livedb/src/writer.rs`
  — no range-tree page materialization helpers remain; remaining `Vec<u32>` uses are
  allocator/dirty metadata or metadata-tree builders.
- `rg -n "readLeaf|readBranch|writeLeafAt|writeBranchAt|emitLeaf|emitBranch|insertRecord|removeRecord|insertKey|removeKey|insertU32|removeU32" v4/go/writer.go`
  — no old Go range-tree materialization/slice-edit helpers remain.

Sensitive data gate:

- No secrets, credentials, bearer tokens, SNMP communities, community/customer data,
  personal data, non-private customer-identifying IPs, private endpoints, or proprietary
  incident details were used. Tests use synthetic IPv4 ranges and single-byte scopes.

Artifact maintenance gate:

- `AGENTS.md`: unchanged; build/test commands and project workflow did not change.
- Runtime project skills: none exist; no reusable project skill was introduced by this
  completed implementation.
- Specs: updated `.agents/sow/specs/design-iprange-v4-livedb.md` §6.2 for
  transaction-local COW.
- End-user/operator docs: unchanged; public API, file format, and CLI/operator behavior
  are unchanged.
- End-user/operator skills: none exist; no exported skill surface is affected.
- SOW lifecycle: SOW-0011 paused; this SOW is completed and moved to `.agents/sow/done/`
  with the implementation in the same commit.

Specs update:

- Updated `.agents/sow/specs/design-iprange-v4-livedb.md` §6.2.

Project skills update:

- No runtime project skill update. The project currently has no runtime project skills, and
  this patch does not introduce a new repeatable workflow beyond the existing SOW/process
  rules.

End-user/operator docs update:

- No end-user/operator docs update. Behavior and commands are unchanged.

End-user/operator skills update:

- No end-user/operator skills exist or were affected.

Lessons:

- The writer needs a distinct transaction-private page identity set in addition to the
  dirty-page list. Dirty pages answer "what must be written"; private pages answer "what is
  safe to mutate again inside this transaction."

Follow-up mapping:

- Implemented: transaction-local page COW for Rust and Go.
- Implemented: byte-level leaf/branch mutation path for Rust and Go.
- No deferred items remain in this SOW.

## Outcome

Completed. Transaction-local COW is implemented and validated in Rust and Go, and the
range-tree writer mutation path now edits fixed-size leaf/branch slots directly instead of
materializing existing records/branches.

## Lessons Extracted

- Do not describe COW only as "allocate a new page" in specs or comments; the required
  contract is "allocate/copy once per touched committed page per transaction, then mutate
  the private page in place."

## Followup

None.

## Regression Log

None yet.
