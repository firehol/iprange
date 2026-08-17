//go:build !v4work

// Package work records test-only necessary-work counters for the reader and
// writer cores. Every counter call compiles to an inlineable no-op in
// production builds (Enabled is const false); build the module with -tags
// v4work to enable the counters and the snapshot API. Tests pin the exact
// counts so a change that adds or removes required work on the hot path is
// visible (mirroring v4/rust/iprange-livedb/src/work.rs for the Go peer).
package work

// Enabled reports whether the counters are compiled in.
const Enabled = false

// TreeLookup counts one root-to-leaf query (range, catalog, membership
// dictionary, blob leaf, structure table).
func TreeLookup(uint64) {}

// TreeDescent counts one branch-follow during a tree walk.
func TreeDescent(uint64) {}

// PageVisit counts one visited page view on the reader hot paths.
func PageVisit(uint64) {}

// PageParse counts one page-header decode. The reader decodes exactly one
// header per visited page on every path, so PageParse moves with PageVisit
// and the pinned tests treat them as one invariant.
func PageParse(uint64) {}

// KeyProbe counts one key-only comparison during a fixed-tree search.
func KeyProbe(uint64) {}

// LeafValidation counts one decode of a selected leaf record.
func LeafValidation(uint64) {}

// WordRead counts one 8-byte membership bitmap word read.
func WordRead(uint64) {}

// StructureDecode counts one structured payload decode.
func StructureDecode(uint64) {}

// MappingRemap counts one actual mapping resize (remap or shrink that
// re-establishes the mapping); same-size no-ops do not count.
func MappingRemap(uint64) {}

// MappingGrowth counts one file-and-mapping extension.
func MappingGrowth(uint64) {}

// MappingFlush counts one whole-mapping msync (MS_SYNC).
func MappingFlush(uint64) {}

// FileSync counts one stable-storage file sync (fsync / F_FULLFSYNC).
func FileSync(uint64) {}

// CellProbe counts one per-record decode attempt during slotted-page
// inspection (fixed cells and variable records).
func CellProbe(uint64) {}

// SlotRead counts one persistent slot-array value read.
func SlotRead(uint64) {}

// SlotScanStep counts one slot visited during a layout scan or adjustment.
func SlotScanStep(uint64) {}

// EditFitProbe counts one fit pre-check before a structural edit.
func EditFitProbe(uint64) {}

// BitmapProbe counts one bitmap word or summary read during a bitmap walk.
func BitmapProbe(uint64) {}

// PageCreated counts one page allocated (private or tail) for a draft.
func PageCreated(uint64) {}

// PageCopied counts one complete page copied for COW (copy_for_cow).
func PageCopied(uint64) {}

// PageSplit counts one page split during a structural edit.
func PageSplit(uint64) {}

// PageRetired counts one page retired to the retirement tree.
func PageRetired(uint64) {}

// PageReclaimed counts one retired page extent reclaimed into the free
// bitmap.
func PageReclaimed(uint64) {}

// PageSealed counts one page whose checksum was stamped.
func PageSealed(uint64) {}

// BytesMoved counts bytes copied within or across mapped pages.
func BytesMoved(uint64) {}

// BytesZeroed counts bytes zeroed inside mapped pages.
func BytesZeroed(uint64) {}

// FirstFenceUpdate counts one branch first-key (fence) update during a
// split or delete propagation.
func FirstFenceUpdate(uint64) {}
