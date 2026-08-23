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

// PageParse counts one page-header decode. The reader page owner decodes
// exactly one header per visited page, so on the reader hot paths PageParse
// moves with PageVisit and the pinned reader tests treat them as one
// invariant. Writer edit paths decode headers through their own parse owner
// and can re-inspect a page (COW first-touch, cell apply), so there
// PageParse is not tied to PageVisit.
func PageParse(uint64) {}

// KeyProbe counts one key-only comparison during a fixed-tree search.
func KeyProbe(uint64) {}

// LeafValidation counts one decode of a selected leaf record.
func LeafValidation(uint64) {}

// WordRead counts one 8-byte membership bitmap word read.
func WordRead(uint64) {}

// MembershipDecode counts one point-match membership decode (one whole
// matching-feeds scan; Rust work::membership_decode parity).
func MembershipDecode(uint64) {}

// StructureDecode counts one structured payload decode.
func StructureDecode(uint64) {}

// StructureIntern counts one structured payload intern (Rust
// work::structure_intern parity).
func StructureIntern(uint64) {}

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

// EdgePathCheck counts one cached-edge position verification during a
// monotonic edge insertion (Rust edge_path_check).
func EdgePathCheck(uint64) {}

// RangeConsumed counts one range record read by a reader cursor (Rust
// range_consumed; written records are counted separately by
// RangeEmitted).
func RangeConsumed(uint64) {}

// RangeEmitted counts one range record written during a range edit.
func RangeEmitted(uint64) {}

// CatalogLookup counts one feed-name or feed-index catalog lookup (Rust
// work::catalog_lookup).
func CatalogLookup(uint64) {}

// RangeSplit counts one range record split into two during a rewrite.
func RangeSplit(uint64) {}

// RangeCoalesced counts one adjacency merge of two same-value ranges.
func RangeCoalesced(uint64) {}

// InputSourcePass counts one logical pass over one input source (Rust
// work::input_source_pass: one for a membership aggregation scan, two for
// a two-source join).
func InputSourcePass(uint64) {}

// MembershipDecodeCacheHit counts one selected-membership sequence served
// from the bounded decode cache (Rust work::membership_decode_cache_hit).
func MembershipDecodeCacheHit(uint64) {}

// MembershipWordRead counts one membership bitmap word read by the
// selected-membership decoder (Rust work::membership_word_read).
func MembershipWordRead(uint64) {}

// AggregationContribution counts one exact contribution folded into a
// feed total, pair total, cross cell, or uncovered total (Rust
// work::aggregation_contribution).
func AggregationContribution(uint64) {}

// AggregationResult counts one emitted aggregation or join result record
// (Rust work::aggregation_result).
func AggregationResult(uint64) {}

// JoinAdvance counts one sweep step of an ordered join (Rust
// work::join_advance).
func JoinAdvance(uint64) {}

// CatalogIntern counts one feed catalog internment (Rust
// work::catalog_intern).
func CatalogIntern(uint64) {}

// MembershipLookup counts one membership dictionary lookup (Rust
// work::membership_lookup).
func MembershipLookup(uint64) {}

// MembershipIntern counts one membership dictionary intern attempt (Rust
// work::membership_intern).
func MembershipIntern(uint64) {}

// MembershipRefcountBatch counts one applied dictionary refcount batch
// (Rust work::membership_refcount_batch).
func MembershipRefcountBatch(uint64) {}

// OutputPass counts one whole output-build pass over an input source
// (Rust work::output_pass).
func OutputPass(uint64) {}

// MembershipInternCacheHit counts one membership intern served from the
// bounded sequence cache (Rust work::membership_intern_cache_hit).
func MembershipInternCacheHit(uint64) {}

// SourcePass counts one logical pass over one history source (Rust
// work::source_pass).
func SourcePass(uint64) {}

// HistoryWindowTest counts one per-window before/after test during a
// history projection (Rust work::history_window_test).
func HistoryWindowTest(uint64) {}

// MembershipCombination counts one membership bitmap combination
// (Rust work::membership_combination).
func MembershipCombination(uint64) {}

// MembershipDeltaSpill counts one refcount delta spilled from the
// pending slots into the delta tree (Rust work::membership_delta_spill).
func MembershipDeltaSpill(uint64) {}

// LeafLocatorHit counts one private input served directly by the leaf
// locator cache (Rust work::leaf_locator_hit).
func LeafLocatorHit(uint64) {}

// LeafLocatorMiss counts one private input with no cached leaf (Rust
// work::leaf_locator_miss).
func LeafLocatorMiss(uint64) {}

// LeafLocatorFallback counts one private input whose cached leaf did not
// fit the gap and fell back to a full descent (Rust
// work::leaf_locator_fallback).
func LeafLocatorFallback(uint64) {}
