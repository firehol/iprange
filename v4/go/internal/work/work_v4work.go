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
}
