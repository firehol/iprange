// Private-tree gap insertion and bounded edge reuse (Rust
// fixed_tree/gap.rs).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Edge selects the first or last position of a private edge leaf (Rust
// fixed_tree::Edge).
type Edge uint8

const (
	// EdgeFirst is the leftmost boundary of a leaf.
	EdgeFirst Edge = iota
	// EdgeLast is the rightmost boundary of a leaf.
	EdgeLast
)

// LocalPrevious is the outcome of one predecessor gap probe (Rust
// LocalPrevious).
type LocalPrevious uint8

const (
	// LocalPreviousAccept keeps the gap open.
	LocalPreviousAccept LocalPrevious = iota
	// LocalPreviousReject closes the gap with a decoded cell value.
	LocalPreviousReject
)

// LocalNext is the outcome of one successor gap probe (Rust LocalNext).
type LocalNext uint8

const (
	// LocalNextAccept keeps the gap open.
	LocalNextAccept LocalNext = iota
	// LocalNextReject closes the gap with a decoded cell value.
	LocalNextReject
)

// LocalGap evaluates whether a candidate leaf cell fits into the physical
// gap between two adjacent existing records (Rust LocalGap). The cell
// argument is the raw leaf cell on the probed side, or nil when the tree
// has no cell on that side (the candidate is the tree's extreme edge). A
// Reject decision carries the decoded cell value that blocks the gap.
type LocalGap interface {
	Previous(exact bool, cell []byte) (LocalPrevious, any, error)
	Next(cell []byte) (LocalNext, any, error)
}

// LocalInsert is the outcome of a local gap insertion (Rust LocalInsert).
type LocalInsert struct {
	// Inserted reports whether the cell was inserted into the local leaf.
	Inserted bool
	// PageNumber names the leaf that received the cell when Inserted.
	PageNumber uint32
	// Reject carries the probing decision when the local gap is bridged.
	Reject *LocalReject
}

// CachedInsert is the outcome of a cached-leaf gap probe (Rust
// CachedInsert).
type CachedInsert uint8

const (
	// CachedInsertMiss reports the cached leaf is not a fitting gap.
	CachedInsertMiss CachedInsert = iota
	// CachedInsertInserted reports the cell was inserted into the cached
	// private leaf.
	CachedInsertInserted
)

// EdgeInsert is the outcome of an edge insertion (Rust EdgeInsert).
type EdgeInsert struct {
	// Inserted reports whether the cell was inserted at the edge.
	Inserted bool
	// Edge carries the refreshed private edge when Inserted.
	Edge *PrivateEdge
	// Reject carries the probing decision when the edge is bridged.
	Reject *LocalReject
}

// PrivatePosition identifies one private leaf position after a COW descent
// (Rust PrivatePosition).
type PrivatePosition struct {
	Path       Path
	PageNumber uint32
}

// PrivateEdge caches one private leaf at the first or last tree edge for
// monotonic insertions (Rust PrivateEdge).
type PrivateEdge struct {
	position     PrivatePosition
	direction    Edge
	directionSet bool
	pendingFirst *Key
}

// consistentEdge builds a direction-less edge over one private position
// (Rust PrivateEdge::consistent).
func consistentEdge(position PrivatePosition) PrivateEdge {
	return PrivateEdge{position: position}
}

// RootEdge builds the edge over a single-page tree root (Rust root_edge).
func RootEdge(pageNumber uint32) PrivateEdge {
	return consistentEdge(PrivatePosition{Path: Path{}, PageNumber: pageNumber})
}

// FlushEdge propagates a pending first-key fence update and forgets it
// (Rust flush_edge).
func FlushEdge(codec Codec, store Store, root *uint32, edge *PrivateEdge) error {
	if edge.pendingFirst != nil {
		if err := PropagateFirst(codec, store, root, &edge.position.Path, *edge.pendingFirst); err != nil {
			return err
		}
		edge.pendingFirst = nil
	}
	return nil
}

// rejectCell is one decoded neighbor cell and its physical index.
type rejectCell struct {
	index int
	value any
}

// LocalReject is the probing outcome of a blocked local gap: the exact
// target position plus the decoded neighbor cells that close the gap (Rust
// LocalReject).
type LocalReject struct {
	Target LeafTarget
	// predecessor is the decoded cell immediately before the gap, when the
	// leaf had one and it bridged the gap.
	predecessor *rejectCell
	// successor is the decoded cell immediately after the gap, when the
	// leaf had one and it bridged the gap.
	successor *rejectCell
	// predecessorComplete reports the predecessor side of the tree was
	// fully examined (no external predecessor exists that could bridge
	// the gap).
	predecessorComplete bool
	// successorComplete reports the successor side of the tree was fully
	// examined (no external successor exists that could bridge the gap).
	successorComplete bool
}

// Predecessor returns the decoded predecessor cell value, when present.
func (r *LocalReject) Predecessor() (any, bool) {
	if r.predecessor == nil {
		return nil, false
	}
	return r.predecessor.value, true
}

// Successor returns the decoded successor cell value, when present.
func (r *LocalReject) Successor() (any, bool) {
	if r.successor == nil {
		return nil, false
	}
	return r.successor.value, true
}

// PredecessorComplete reports the predecessor side was fully examined.
func (r *LocalReject) PredecessorComplete() bool { return r.predecessorComplete }

// SuccessorComplete reports the successor side was fully examined.
func (r *LocalReject) SuccessorComplete() bool { return r.successorComplete }

// IntoPosition converts the rejection into the private position it names
// (Rust LocalReject::into_position).
func (r *LocalReject) IntoPosition() PrivatePosition {
	return PrivatePosition{Path: r.Target.Path, PageNumber: r.Target.PageNumber}
}

// ExternalPredecessor reads the tree-predecessor leaf of the rejected gap,
// walking the sibling subtree (Rust LocalReject::external_predecessor).
func (r *LocalReject) ExternalPredecessor(codec Codec, store Store) (any, error) {
	leaf, err := adjacentLeaf(codec, store, &r.Target.Path, AdjacentBefore)
	if err != nil || leaf == nil {
		return nil, err
	}
	return leaf.leaf, nil
}

// ExternalSuccessor reads the tree-successor leaf of the rejected gap,
// walking the sibling subtree (Rust LocalReject::external_successor).
func (r *LocalReject) ExternalSuccessor(codec Codec, store Store) (any, error) {
	leaf, err := adjacentLeaf(codec, store, &r.Target.Path, AdjacentAfter)
	if err != nil || leaf == nil {
		return nil, err
	}
	return leaf.leaf, nil
}

// InsertIfLocalGap inserts one leaf cell into the physical gap around its
// key when the gap is fully local to the probed leaf (Rust
// insert_if_local_gap). The tree must already be private.
func InsertIfLocalGap(codec Codec, store Store, root *uint32, leafCell []byte, retired *RetiredPages, gap LocalGap) (LocalInsert, error) {
	if err := RequireLeaf(codec, leafCell); err != nil {
		return LocalInsert{}, err
	}
	if *root == 0 {
		pageNumber, err := NewLeaf(codec, store, leafCell)
		if err != nil {
			return LocalInsert{}, err
		}
		*root = pageNumber
		return LocalInsert{Inserted: true, PageNumber: pageNumber}, nil
	}
	key, err := codec.ReadKey(leafCell, 0)
	if err != nil {
		return LocalInsert{}, err
	}
	var selector gapSelector
	selector.init(codec, key, len(leafCell), gap)
	leaf, err := privatePathSelect(codec, store, root, key, retired, selector.decide)
	if err != nil {
		return LocalInsert{}, err
	}
	if retired.Len() != 0 {
		return LocalInsert{}, corrupt("private B+tree contains a committed page")
	}
	decision := leaf.Selection.(gapDecision)
	if !decision.insert {
		reject, err := rejection(leaf.Path, leaf.PageNumber, leaf.Header, decision)
		if err != nil {
			return LocalInsert{}, err
		}
		return LocalInsert{Reject: reject}, nil
	}
	target := LeafTarget{Path: leaf.Path, PageNumber: leaf.PageNumber, Header: leaf.Header, Index: decision.index, Exists: false}
	pageNumber := target.PageNumber
	positioned, err := insertGapTarget(codec, store, root, leafCell, target, key, decision.fits)
	if err != nil {
		return LocalInsert{}, err
	}
	if positioned != nil {
		pageNumber = positioned.PageNumber
	}
	return LocalInsert{Inserted: true, PageNumber: pageNumber}, nil
}

// InsertIfCachedInteriorGap probes one cached private leaf for a fitting
// interior gap and inserts the cell there (Rust
// insert_if_cached_interior_gap). The page must already be private.
func InsertIfCachedInteriorGap(codec Codec, store Store, pageNumber uint32, leafCell []byte, gap LocalGap) (CachedInsert, error) {
	if err := RequireLeaf(codec, leafCell); err != nil {
		return CachedInsertMiss, err
	}
	key, err := codec.ReadKey(leafCell, 0)
	if err != nil {
		return CachedInsertMiss, err
	}
	var selected *Header
	index := 0
	level := uint16(0)
	err = store.Inspect(pageNumber, func(page []byte) error {
		header, err := parse(codec, page, store.TargetTxn(), &level)
		if err != nil {
			return err
		}
		if format.U64(page[format.HeaderBorn:]) != store.TargetTxn() {
			return corrupt("leaf locator selected a committed page")
		}
		probeIndex, exists, err := lowerBound(codec, page, header, key, true)
		if err != nil {
			return err
		}
		if exists || probeIndex == 0 || probeIndex == int(header.ItemCount) {
			return nil
		}
		var selector gapSelector
		selector.init(codec, key, len(leafCell), gap)
		decision, err := selector.selectAt(page, header, &Path{}, probeIndex, false)
		if err != nil {
			return err
		}
		if !decision.insert || !decision.fits {
			return nil
		}
		index = decision.index
		selected = header
		return nil
	})
	if err != nil {
		return CachedInsertMiss, err
	}
	if selected == nil {
		return CachedInsertMiss, nil
	}
	return CachedInsertInserted, applyLeafEdit(codec, store, pageNumber, selected, Edit{index: index, replace: false, cell: leafCell})
}

// InsertRejectedGap completes a previously rejected local gap insertion
// after the caller proved the external sides (Rust insert_rejected_gap).
// It returns the final private position when the record fit locally.
func InsertRejectedGap(codec Codec, store Store, root *uint32, leafCell []byte, rejected *LocalReject) (*PrivatePosition, error) {
	if err := RequireLeaf(codec, leafCell); err != nil {
		return nil, err
	}
	key, err := codec.ReadKey(leafCell, 0)
	if err != nil {
		return nil, err
	}
	fits := format.SlottedInsertFits(&rejected.Target.Header, len(leafCell))
	return insertGapTarget(codec, store, root, leafCell, rejected.Target, key, fits)
}

// insertGapTarget applies one positioned gap insertion, propagating the
// first-key fence when the record lands at index zero and splitting the
// leaf when the record does not fit (Rust insert_gap_target).
func insertGapTarget(codec Codec, store Store, root *uint32, leafCell []byte, target LeafTarget, key Key, fits bool) (*PrivatePosition, error) {
	if fits {
		if err := applyLeafEdit(codec, store, target.PageNumber, &target.Header, Edit{index: target.Index, replace: false, cell: leafCell}); err != nil {
			return nil, err
		}
		if target.Index == 0 {
			if err := PropagateFirst(codec, store, root, &target.Path, key); err != nil {
				return nil, err
			}
		}
		return &PrivatePosition{Path: target.Path, PageNumber: target.PageNumber}, nil
	}
	if _, err := EditLeaf(codec, store, root, leafCell, &target); err != nil {
		return nil, err
	}
	return nil, nil
}

// InsertIfEdgeGap inserts one leaf cell at a cached private tree edge when
// the local gap is open (Rust insert_if_edge_gap).
func InsertIfEdgeGap(codec Codec, store Store, root *uint32, leafCell []byte, cached *PrivateEdge, edge Edge, knownGap bool, gap LocalGap) (EdgeInsert, error) {
	if err := RequireLeaf(codec, leafCell); err != nil {
		return EdgeInsert{}, err
	}
	if *root == 0 {
		return EdgeInsert{}, corrupt("cached B+tree edge has an empty root")
	}
	if err := verifyCachedEdge(cached, *root, edge); err != nil {
		return EdgeInsert{}, err
	}
	key, err := codec.ReadKey(leafCell, 0)
	if err != nil {
		return EdgeInsert{}, err
	}
	level := uint16(0)
	var header *Header
	decision := gapDecision{}
	err = store.Inspect(cached.position.PageNumber, func(page []byte) error {
		h, err := parse(codec, page, store.TargetTxn(), &level)
		if err != nil {
			return err
		}
		if format.U64(page[format.HeaderBorn:]) != store.TargetTxn() {
			return corrupt("cached B+tree edge is not private")
		}
		header = h
		boundary := 0
		if edge == EdgeLast {
			boundary = int(h.ItemCount) - 1
		}
		boundaryKey, err := keyAt(codec, page, h, boundary)
		if err != nil {
			return err
		}
		index := 0
		exists := false
		if edge == EdgeFirst {
			switch {
			case key.Less(boundaryKey):
				index, exists = 0, false
			case key.Equal(boundaryKey):
				index, exists = 0, true
			default:
				return corrupt("cached B+tree edge order changed")
			}
		} else {
			switch {
			case boundaryKey.Less(key):
				index, exists = int(h.ItemCount), false
			case key.Equal(boundaryKey):
				index, exists = int(h.ItemCount)-1, true
			default:
				return corrupt("cached B+tree edge order changed")
			}
		}
		if knownGap {
			if exists {
				return corrupt("known B+tree edge gap contains its key")
			}
			decision = gapDecision{insert: true, index: index, fits: format.SlottedInsertFits(h, len(leafCell))}
			return nil
		}
		var selector gapSelector
		selector.init(codec, key, len(leafCell), gap)
		decision, err = selector.selectAt(page, h, &cached.position.Path, index, exists)
		return err
	})
	if err != nil {
		return EdgeInsert{}, err
	}
	if !decision.insert {
		if err := FlushEdge(codec, store, root, cached); err != nil {
			return EdgeInsert{}, err
		}
		reject, err := rejection(cached.position.Path, cached.position.PageNumber, *header, decision)
		if err != nil {
			return EdgeInsert{}, err
		}
		return EdgeInsert{Reject: reject}, nil
	}
	target := LeafTarget{Path: cached.position.Path, PageNumber: cached.position.PageNumber, Header: *header, Index: decision.index, Exists: false}
	if decision.fits {
		var pendingFirst *Key
		if target.Index == 0 && target.Path.Depth() != 0 {
			k := key
			pendingFirst = &k
		}
		position, err := applyFittingEdgeInsert(codec, store, target, leafCell)
		if err != nil {
			return EdgeInsert{}, err
		}
		cached.position = position
		if pendingFirst != nil {
			cached.pendingFirst = pendingFirst
		}
	} else {
		if err := SplitLeafAtEdge(codec, store, root, &target, leafCell, edge); err != nil {
			return EdgeInsert{}, err
		}
		cached.pendingFirst = nil
		position, err := locatePrivatePosition(codec, store, root, key)
		if err != nil {
			return EdgeInsert{}, err
		}
		cached.position = *position
	}
	return EdgeInsert{Inserted: true, Edge: cached}, nil
}

func verifyCachedEdge(cached *PrivateEdge, root uint32, edge Edge) error {
	if cached.directionSet && cached.direction != edge {
		return corrupt("cached B+tree edge direction changed")
	}
	if cached.directionSet {
		return nil
	}
	work.EdgePathCheck(1)
	if !pathIsEdge(&cached.position.Path, edge) ||
		(cached.position.Path.Depth() == 0 && cached.position.PageNumber != root) {
		return corrupt("cached B+tree position is not its claimed edge")
	}
	cached.direction = edge
	cached.directionSet = true
	return nil
}

func applyFittingEdgeInsert(codec Codec, store Store, target LeafTarget, leafCell []byte) (PrivatePosition, error) {
	if err := applyLeafEdit(codec, store, target.PageNumber, &target.Header, Edit{index: target.Index, replace: false, cell: leafCell}); err != nil {
		return PrivatePosition{}, err
	}
	return PrivatePosition{Path: target.Path, PageNumber: target.PageNumber}, nil
}

// rejection converts a general gap decision into its rejection record
// (Rust rejection).
func rejection(path Path, pageNumber uint32, header Header, decision gapDecision) (*LocalReject, error) {
	if decision.insert {
		return nil, corrupt("accepted B+tree gap became a rejection")
	}
	return &LocalReject{
		Target:              LeafTarget{Path: path, PageNumber: pageNumber, Header: header, Index: decision.index, Exists: false},
		predecessor:         decision.predecessor,
		successor:           decision.successor,
		predecessorComplete: decision.predecessorComplete,
		successorComplete:   decision.successorComplete,
	}, nil
}

// gapDecision is the internal outcome of one gap probe (Rust
// GapDecision).
type gapDecision struct {
	insert              bool
	index               int
	fits                bool
	predecessor         *rejectCell
	successor           *rejectCell
	predecessorComplete bool
	successorComplete   bool
}

// gapSelector drives the gap probe over one leaf (Rust GapSelector).
type gapSelector struct {
	codec   Codec
	key     Key
	cellLen int
	gap     LocalGap
}

func (g *gapSelector) init(codec Codec, key Key, cellLen int, gap LocalGap) {
	g.codec = codec
	g.key = key
	g.cellLen = cellLen
	g.gap = gap
}

// select implements the leaf selection entry used by the COW descent
// (Rust LeafSelector::select).
func (g *gapSelector) decide(page []byte, header *Header, path *Path) (any, error) {
	index, exists, err := lowerBound(g.codec, page, header, g.key, true)
	if err != nil {
		return nil, err
	}
	return g.selectAt(page, header, path, index, exists)
}

// selectAt decides the gap at one already-located position (Rust
// GapSelector::select_at).
func (g *gapSelector) selectAt(page []byte, header *Header, path *Path, index int, exists bool) (gapDecision, error) {
	predecessor, predecessorComplete, err := g.probePredecessor(page, header, path, index, exists)
	if err != nil {
		return gapDecision{}, err
	}
	successor, successorComplete, err := g.probeSuccessor(page, header, path, index, exists)
	if err != nil {
		return gapDecision{}, err
	}
	if predecessor != nil || successor != nil || !predecessorComplete || !successorComplete {
		return gapDecision{
			insert:              false,
			index:               index,
			predecessor:         predecessor,
			successor:           successor,
			predecessorComplete: predecessorComplete,
			successorComplete:   successorComplete,
		}, nil
	}
	return gapDecision{insert: true, index: index, fits: format.SlottedInsertFits(header, g.cellLen)}, nil
}

func (g *gapSelector) probePredecessor(page []byte, header *Header, path *Path, index int, exists bool) (*rejectCell, bool, error) {
	if exists {
		cell, err := g.validLeaf(page, header, index)
		if err != nil {
			return nil, false, err
		}
		decision, value, err := g.gap.Previous(true, cell)
		if err != nil {
			return nil, false, err
		}
		if decision == LocalPreviousAccept {
			return nil, false, corrupt("exact B+tree key was accepted as a gap")
		}
		return &rejectCell{index: index, value: value}, true, nil
	}
	if index > 0 {
		cell, err := g.validLeaf(page, header, index-1)
		if err != nil {
			return nil, false, err
		}
		decision, value, err := g.gap.Previous(false, cell)
		if err != nil {
			return nil, false, err
		}
		if decision == LocalPreviousAccept {
			return nil, true, nil
		}
		return &rejectCell{index: index - 1, value: value}, true, nil
	}
	if allFirst(path) {
		decision, _, err := g.gap.Previous(false, nil)
		if err != nil {
			return nil, false, err
		}
		if decision == LocalPreviousReject {
			return nil, false, corrupt("absent B+tree predecessor was rejected")
		}
		return nil, true, nil
	}
	return nil, false, nil
}

func (g *gapSelector) probeSuccessor(page []byte, header *Header, path *Path, index int, exists bool) (*rejectCell, bool, error) {
	successorIndex := index
	if exists {
		successorIndex++
	}
	if successorIndex < int(header.ItemCount) {
		cell, err := g.validLeaf(page, header, successorIndex)
		if err != nil {
			return nil, false, err
		}
		decision, value, err := g.gap.Next(cell)
		if err != nil {
			return nil, false, err
		}
		if decision == LocalNextAccept {
			return nil, true, nil
		}
		return &rejectCell{index: successorIndex, value: value}, true, nil
	}
	if allLast(path) {
		decision, _, err := g.gap.Next(nil)
		if err != nil {
			return nil, false, err
		}
		if decision == LocalNextReject {
			return nil, false, corrupt("absent B+tree successor was rejected")
		}
		return nil, true, nil
	}
	return nil, false, nil
}

func allFirst(path *Path) bool {
	for _, frame := range path.Slice() {
		if frame.Index != 0 {
			return false
		}
	}
	return true
}

func allLast(path *Path) bool {
	for _, frame := range path.Slice() {
		if frame.Index+1 != frame.ItemCount {
			return false
		}
	}
	return true
}

func pathIsEdge(path *Path, edge Edge) bool {
	if edge == EdgeFirst {
		return allFirst(path)
	}
	return allLast(path)
}

// validLeaf re-verifies one leaf cell and returns it (Rust validated_leaf).
func (g *gapSelector) validLeaf(page []byte, header *Header, index int) ([]byte, error) {
	cell, err := codecCell(g.codec, page, header, index)
	if err != nil {
		return nil, err
	}
	if _, err := g.codec.ReadLeaf(cell); err != nil {
		return nil, err
	}
	return cell, nil
}

// LocalRun selects which neighbor cell a replacement overwrites (Rust
// LocalRun).
type LocalRun uint8

const (
	// LocalRunPredecessor replaces the predecessor cell.
	LocalRunPredecessor LocalRun = iota
	// LocalRunSuccessor replaces the successor cell.
	LocalRunSuccessor
	// LocalRunBoth replaces the contiguous predecessor+successor pair
	// with one cell.
	LocalRunBoth
)

// ReplaceLocalPredecessorWith replaces the rejected gap's local
// predecessor cell with a 2-3 cell replacement (Rust
// replace_local_predecessor_with).
func ReplaceLocalPredecessorWith(codec Codec, store Store, root *uint32, rejected *LocalReject, key Key, cells [][]byte) error {
	if err := RequireReplacement(codec, key, cells); err != nil {
		return err
	}
	if rejected.predecessor == nil {
		return corrupt("B+tree local predecessor is unavailable")
	}
	target := rejected.Target
	target.Index = rejected.predecessor.index
	target.Exists = true
	return replaceTarget(codec, store, root, target, cells)
}

// ReplaceLocalRun overwrites one local neighbor cell (or the contiguous
// pair) of a rejected gap with one replacement cell (Rust
// replace_local_run). The replacement must keep the same encoded size.
func ReplaceLocalRun(codec Codec, store Store, root *uint32, rejected *LocalReject, run LocalRun, replacement []byte) error {
	if err := RequireLeaf(codec, replacement); err != nil {
		return err
	}
	start := 0
	removeCount := 1
	switch run {
	case LocalRunPredecessor:
		if rejected.predecessor == nil {
			return corrupt("local predecessor is unavailable")
		}
		start = rejected.predecessor.index
	case LocalRunSuccessor:
		if rejected.successor == nil {
			return corrupt("local successor is unavailable")
		}
		start = rejected.successor.index
	case LocalRunBoth:
		if rejected.predecessor == nil {
			return corrupt("local predecessor is unavailable")
		}
		if rejected.successor == nil {
			return corrupt("local successor is unavailable")
		}
		if rejected.successor.index != rejected.predecessor.index+1 {
			return corrupt("local B+tree run is not contiguous")
		}
		start = rejected.predecessor.index
		removeCount = 2
	}
	target := &rejected.Target
	if start+removeCount > int(target.Header.ItemCount) {
		return corrupt("local B+tree run is outside its leaf")
	}
	replacementKey, err := codec.ReadKey(replacement, 0)
	if err != nil {
		return err
	}
	err = store.Update(target.PageNumber, func(page []byte) error {
		oldLen, err := codecCell(codec, page, &target.Header, start)
		if err != nil {
			return err
		}
		oldLenBytes := len(oldLen)
		if len(replacement) != oldLenBytes {
			return unsupported("local B+tree run replacement changed cell size")
		}
		if start > 0 {
			previous, err := keyAt(codec, page, &target.Header, start-1)
			if err != nil {
				return err
			}
			if !previous.Less(replacementKey) {
				return corrupt("local B+tree replacement is out of order")
			}
		}
		if start+removeCount < int(target.Header.ItemCount) {
			next, err := keyAt(codec, page, &target.Header, start+removeCount)
			if err != nil {
				return err
			}
			if !replacementKey.Less(next) {
				return corrupt("local B+tree replacement is out of order")
			}
		}
		changed, err := format.SlottedReplace(page, &target.Header, start, oldLenBytes, replacement)
		if err != nil {
			return err
		}
		if !changed {
			return corrupt("local B+tree replacement no longer fits")
		}
		header := target.Header
		for i := 1; i < removeCount; i++ {
			cell, err := codecCell(codec, page, &header, start+1)
			if err != nil {
				return err
			}
			if err := format.SlottedRemove(page, &header, start+1, len(cell)); err != nil {
				return err
			}
			header.ItemCount--
			header.Lower -= 2
			header.Upper += uint16(len(cell))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if start == 0 {
		if err := PropagateFirst(codec, store, root, &target.Path, replacementKey); err != nil {
			return err
		}
	}
	return nil
}
