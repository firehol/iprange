# SOW-0025 — Milestone 2 Gap Analysis: mapped COW writer and Go producer

Status: in progress (2026-08-17; chunk 1 gate CLOSED at 7a90fb4; chunk 2 - writer open - implemented at 56e8516, level-1 round-1 fix round committed at 35a096b, delta re-reviews in progress; edit core (chunk 3) pending)

## Scope

One physical writer core for v4/go mirroring the Rust writer semantics:
final-offset page construction, COW edits, free/used bitmap mutation,
retirement, checksum sealing at commit, meta publication, abort, reclaim;
then public Go generation of the corpus and Rust cross-open. Authority:
Rust writer implementation + binary-format-v4.md; mmap-only (no complete
page in owned memory); one authoritative low-level implementation per
operation; test-only work counters; clean-code (small files, single
purpose).

## Rust authority inventory -> Go mapping

| Rust module | Responsibility | Go target |
|---|---|---|
| mapping.rs (read_write, bytes_mut, page_mut, resize, flush_range, sync_file, remap) | mutable file-backed mapping owner | internal/mapping (chunk 1: OpenMutable/Grow/Flush/SyncFile DONE) |
| checksum.rs, page_checksum.rs | CRC-32C, page seal at offset 28 len 4 | internal/format le.go (chunk 1 DONE: CRC32CWithZeroed, PageChecksumValid, SealPageChecksum) |
| page_io.rs (PageSink put_u32 ...) | mapped page writes | internal/format PutU32 + mapping views (existing) |
| writer_core/open.rs | map_writer, select_committed, trim_committed_tail | internal/writer/open (chunk 2) |
| writer_core/edit.rs + draft_store.rs | COW range/membership edits, drafts, budgets | internal/writer/edit + draft (chunk 3) |
| writer_core/publication.rs | commit attempt, prepare, publish | internal/writer/publication (chunk 4) |
| writer_core/close.rs, reclaim.rs | close plan, reclaim | internal/writer/close (chunk 4) |
| used_bitmap/, free_bitmap/, retirement.rs, commit_resolution.rs, private_page_pool | page allocation/retirement | internal/alloc (chunk 3) |
| database_file.rs (grow/shrink) | file extent | internal/mapping Grow (chunk 1 partial; shrink later) |

Chunk plan: (1) mapping write mode + page checksum [DONE at HEAD
bf49779]; (2) writer open: map_writer, committed selection, tail trim;
(3) allocation + COW edit core: bitmaps, retirement, drafts, direct/
membership/structured page builders; (4) publication/commit/abort/close/
reclaim; (5) public Go generation API + Go-produced corpus + Rust
cross-open + mixed subprocess gates.

## Risk register

- COW ownership: a draft references base pages; edits must land at final
  offsets in the file-backed mapping, never in owned memory; page copies
  would violate mmap-only and the complete-page gate rule.
- Dirty-page sealing: seal only after the page is fully written; CRC-32C
  zeroed-field semantics must match Rust byte-for-byte (pinned by vector
  tests).
- Durability: msync (Flush) before fsync (SyncFile) ordering at commit;
  meta publication last with commit nonce/generation rules.
- Locking: exclusive lifetime lock vs readers (chunk 1 implemented and
  pinned by tests); writer exclusion is a hard reader-safety bound.
- Growth/remap failure: file may be extended while mapping remap fails;
  fail-closed state avoids unmapped views (pinned in chunk 1 tests).
- Wire parity: literal vectors from the Rust writer tests must pin every
  Go writer output; Go-produced fixtures open in Rust and vice versa.
- Gate currency: the typed gate must scan the new writer surface; new
  x/sys lifecycle calls (ftruncate/msync/fsync, and on darwin
  fcntl(F_FULLFSYNC) via mapping_sync_darwin.go) are allowed only in
  internal/mapping and are now name-banned everywhere else
  (lifecycleOwnerOnly rule family covering Ftruncate/Msync/Fsync/
  Fdatasync/Syncfs/Sync/Truncate/Fallocate/SyncFileRange, pinned by
  gate battery cases 299-313); the FcntlInt exemption is per-file and per-call
  (mapping_sync_darwin.go only), with the pre-existing FcntlInt dup pin
  (240) still rejecting every other location.
- Performance: no per-item allocation in hot COW paths, declared page
  budgets mirroring Rust PageBudget, necessary-work counters test-only.

## Validation (per chunk)

go test (both tag sets) + -race/checkptr + vet + gofmt + gate scan +
import-graph + 11-target cross-compile; cross-open and literal-vector
tests are the milestone gate; SOW audit before close.
