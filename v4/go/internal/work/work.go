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
