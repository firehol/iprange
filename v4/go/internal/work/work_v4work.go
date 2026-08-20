//go:build v4work

package work

import "sync/atomic"

// Enabled reports whether the counters are compiled in.
const Enabled = true

type counters struct {
	treeLookups       atomic.Uint64
	treeDescents      atomic.Uint64
	pagesVisited      atomic.Uint64
	pagesParsed       atomic.Uint64
	keyProbes         atomic.Uint64
	leafValidations   atomic.Uint64
	wordReads         atomic.Uint64
	membershipDecodes atomic.Uint64
	structureDecodes  atomic.Uint64
	mappingRemaps     atomic.Uint64
	mappingGrowths    atomic.Uint64
	mappingFlushes    atomic.Uint64
	fileSyncs         atomic.Uint64
	cellProbes        atomic.Uint64
	slotReads         atomic.Uint64
	slotScanSteps     atomic.Uint64
	editFitProbes     atomic.Uint64
	bitmapProbes      atomic.Uint64
	pagesCreated      atomic.Uint64
	pagesCopied       atomic.Uint64
	pagesSplit        atomic.Uint64
	pagesRetired      atomic.Uint64
	pagesReclaimed    atomic.Uint64
	pagesSealed       atomic.Uint64
	bytesMoved        atomic.Uint64
	bytesZeroed       atomic.Uint64
	firstFenceUpdates atomic.Uint64
	edgePathChecks    atomic.Uint64
	rangesConsumed    atomic.Uint64
	rangesEmitted     atomic.Uint64
	rangesSplit       atomic.Uint64
	rangesCoalesced   atomic.Uint64
}

var current counters

func TreeLookup(n uint64)       { current.treeLookups.Add(n) }
func TreeDescent(n uint64)      { current.treeDescents.Add(n) }
func PageVisit(n uint64)        { current.pagesVisited.Add(n) }
func PageParse(n uint64)        { current.pagesParsed.Add(n) }
func KeyProbe(n uint64)         { current.keyProbes.Add(n) }
func LeafValidation(n uint64)   { current.leafValidations.Add(n) }
func WordRead(n uint64)         { current.wordReads.Add(n) }
func MembershipDecode(n uint64) { current.membershipDecodes.Add(n) }
func StructureDecode(n uint64)  { current.structureDecodes.Add(n) }
func MappingRemap(n uint64)     { current.mappingRemaps.Add(n) }
func MappingGrowth(n uint64)    { current.mappingGrowths.Add(n) }
func MappingFlush(n uint64)     { current.mappingFlushes.Add(n) }
func FileSync(n uint64)         { current.fileSyncs.Add(n) }
func CellProbe(n uint64)        { current.cellProbes.Add(n) }
func SlotRead(n uint64)         { current.slotReads.Add(n) }
func SlotScanStep(n uint64)     { current.slotScanSteps.Add(n) }
func EditFitProbe(n uint64)     { current.editFitProbes.Add(n) }
func BitmapProbe(n uint64)      { current.bitmapProbes.Add(n) }
func PageCreated(n uint64)      { current.pagesCreated.Add(n) }
func PageCopied(n uint64)       { current.pagesCopied.Add(n) }
func PageSplit(n uint64)        { current.pagesSplit.Add(n) }
func PageRetired(n uint64)      { current.pagesRetired.Add(n) }
func PageReclaimed(n uint64)    { current.pagesReclaimed.Add(n) }
func PageSealed(n uint64)       { current.pagesSealed.Add(n) }
func BytesMoved(n uint64)       { current.bytesMoved.Add(n) }
func BytesZeroed(n uint64)      { current.bytesZeroed.Add(n) }
func FirstFenceUpdate(n uint64) { current.firstFenceUpdates.Add(n) }

// Snapshot is a consistent point-in-time copy of every counter.
type Snapshot struct {
	TreeLookups       uint64
	TreeDescents      uint64
	PagesVisited      uint64
	PagesParsed       uint64
	KeyProbes         uint64
	LeafValidations   uint64
	WordReads         uint64
	MembershipDecodes uint64
	StructureDecodes  uint64
	MappingRemaps     uint64
	MappingGrowths    uint64
	MappingFlushes    uint64
	FileSyncs         uint64
	CellProbes        uint64
	SlotReads         uint64
	SlotScanSteps     uint64
	EditFitProbes     uint64
	BitmapProbes      uint64
	PagesCreated      uint64
	PagesCopied       uint64
	PagesSplit        uint64
	PagesRetired      uint64
	PagesReclaimed    uint64
	PagesSealed       uint64
	BytesMoved        uint64
	BytesZeroed       uint64
	FirstFenceUpdates uint64
	EdgePathChecks    uint64
	RangesConsumed    uint64
	RangesEmitted     uint64
	RangesSplit       uint64
	RangesCoalesced   uint64
}

// Read returns a consistent snapshot of the counters.
func Read() Snapshot {
	return Snapshot{
		TreeLookups:       current.treeLookups.Load(),
		TreeDescents:      current.treeDescents.Load(),
		PagesVisited:      current.pagesVisited.Load(),
		PagesParsed:       current.pagesParsed.Load(),
		KeyProbes:         current.keyProbes.Load(),
		LeafValidations:   current.leafValidations.Load(),
		WordReads:         current.wordReads.Load(),
		MembershipDecodes: current.membershipDecodes.Load(),
		StructureDecodes:  current.structureDecodes.Load(),
		MappingRemaps:     current.mappingRemaps.Load(),
		MappingGrowths:    current.mappingGrowths.Load(),
		MappingFlushes:    current.mappingFlushes.Load(),
		FileSyncs:         current.fileSyncs.Load(),
		CellProbes:        current.cellProbes.Load(),
		SlotReads:         current.slotReads.Load(),
		SlotScanSteps:     current.slotScanSteps.Load(),
		EditFitProbes:     current.editFitProbes.Load(),
		BitmapProbes:      current.bitmapProbes.Load(),
		PagesCreated:      current.pagesCreated.Load(),
		PagesCopied:       current.pagesCopied.Load(),
		PagesSplit:        current.pagesSplit.Load(),
		PagesRetired:      current.pagesRetired.Load(),
		PagesReclaimed:    current.pagesReclaimed.Load(),
		PagesSealed:       current.pagesSealed.Load(),
		BytesMoved:        current.bytesMoved.Load(),
		BytesZeroed:       current.bytesZeroed.Load(),
		FirstFenceUpdates: current.firstFenceUpdates.Load(),
		EdgePathChecks:    current.edgePathChecks.Load(),
		RangesConsumed:    current.rangesConsumed.Load(),
		RangesEmitted:     current.rangesEmitted.Load(),
		RangesSplit:       current.rangesSplit.Load(),
		RangesCoalesced:   current.rangesCoalesced.Load(),
	}
}

// Reset zeroes every counter (test setup only).
func Reset() {
	for _, atomic := range []*atomic.Uint64{
		&current.treeLookups, &current.treeDescents, &current.pagesVisited,
		&current.pagesParsed, &current.keyProbes, &current.leafValidations,
		&current.wordReads, &current.membershipDecodes, &current.structureDecodes, &current.mappingRemaps,
		&current.mappingGrowths, &current.mappingFlushes, &current.fileSyncs,
		&current.cellProbes, &current.slotReads, &current.slotScanSteps,
		&current.editFitProbes, &current.bitmapProbes, &current.pagesCreated,
		&current.pagesCopied, &current.pagesSplit, &current.pagesRetired,
		&current.pagesReclaimed, &current.pagesSealed, &current.bytesMoved,
		&current.bytesZeroed, &current.firstFenceUpdates,
		&current.edgePathChecks, &current.rangesConsumed, &current.rangesEmitted, &current.rangesSplit,
		&current.rangesCoalesced,
	} {
		atomic.Store(0)
	}
}

// EdgePathCheck counts one cached-edge position verification (Rust
// edge_path_check).
func EdgePathCheck(n uint64) { current.edgePathChecks.Add(n) }

// RangeConsumed counts one range record read by a reader cursor (Rust
// range_consumed; written records are counted separately by
// RangeEmitted).
func RangeConsumed(n uint64) { current.rangesConsumed.Add(n) }

// RangeEmitted counts one range record written during a range edit.
func RangeEmitted(n uint64) { current.rangesEmitted.Add(n) }

// RangeSplit counts one range record split into two during a rewrite.
func RangeSplit(n uint64) { current.rangesSplit.Add(n) }

// RangeCoalesced counts one adjacency merge of two same-value ranges.
func RangeCoalesced(n uint64) { current.rangesCoalesced.Add(n) }
