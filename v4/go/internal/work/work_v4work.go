//go:build v4work

package work

import "sync/atomic"

// Enabled reports whether the counters are compiled in.
const Enabled = true

type counters struct {
	treeLookups      atomic.Uint64
	treeDescents     atomic.Uint64
	pagesVisited     atomic.Uint64
	pagesParsed      atomic.Uint64
	keyProbes        atomic.Uint64
	leafValidations  atomic.Uint64
	wordReads        atomic.Uint64
	structureDecodes atomic.Uint64
	mappingRemaps    atomic.Uint64
	mappingGrowths   atomic.Uint64
	mappingFlushes   atomic.Uint64
	fileSyncs        atomic.Uint64
}

var current counters

func TreeLookup(n uint64)      { current.treeLookups.Add(n) }
func TreeDescent(n uint64)     { current.treeDescents.Add(n) }
func PageVisit(n uint64)       { current.pagesVisited.Add(n) }
func PageParse(n uint64)       { current.pagesParsed.Add(n) }
func KeyProbe(n uint64)        { current.keyProbes.Add(n) }
func LeafValidation(n uint64)  { current.leafValidations.Add(n) }
func WordRead(n uint64)        { current.wordReads.Add(n) }
func StructureDecode(n uint64) { current.structureDecodes.Add(n) }
func MappingRemap(n uint64)    { current.mappingRemaps.Add(n) }
func MappingGrowth(n uint64)   { current.mappingGrowths.Add(n) }
func MappingFlush(n uint64)    { current.mappingFlushes.Add(n) }
func FileSync(n uint64)        { current.fileSyncs.Add(n) }

// Snapshot is a consistent point-in-time copy of every counter.
type Snapshot struct {
	TreeLookups      uint64
	TreeDescents     uint64
	PagesVisited     uint64
	PagesParsed      uint64
	KeyProbes        uint64
	LeafValidations  uint64
	WordReads        uint64
	StructureDecodes uint64
	MappingRemaps    uint64
	MappingGrowths   uint64
	MappingFlushes   uint64
	FileSyncs        uint64
}

// Read returns a consistent snapshot of the counters.
func Read() Snapshot {
	return Snapshot{
		TreeLookups:      current.treeLookups.Load(),
		TreeDescents:     current.treeDescents.Load(),
		PagesVisited:     current.pagesVisited.Load(),
		PagesParsed:      current.pagesParsed.Load(),
		KeyProbes:        current.keyProbes.Load(),
		LeafValidations:  current.leafValidations.Load(),
		WordReads:        current.wordReads.Load(),
		StructureDecodes: current.structureDecodes.Load(),
		MappingRemaps:    current.mappingRemaps.Load(),
		MappingGrowths:   current.mappingGrowths.Load(),
		MappingFlushes:   current.mappingFlushes.Load(),
		FileSyncs:        current.fileSyncs.Load(),
	}
}

// Reset zeroes every counter (test setup only).
func Reset() {
	current.treeLookups.Store(0)
	current.treeDescents.Store(0)
	current.pagesVisited.Store(0)
	current.pagesParsed.Store(0)
	current.keyProbes.Store(0)
	current.leafValidations.Store(0)
	current.wordReads.Store(0)
	current.structureDecodes.Store(0)
	current.mappingRemaps.Store(0)
	current.mappingGrowths.Store(0)
	current.mappingFlushes.Store(0)
	current.fileSyncs.Store(0)
}
