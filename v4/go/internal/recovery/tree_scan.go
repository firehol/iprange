package recovery

// CRC-checked salvage traversal for reachable slotted trees (Rust
// recovery/tree_scan.rs): one generic tree walk streams page and cell
// events instead of findings. A refused page (claims, bounds, checksum,
// type, header, or layout) streams its envelope and the walk continues
// under the current cell; an undecodable leaf or branch cell streams
// its codec-invalid envelope and the subtree walk stops exactly like
// the Rust arms.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// treeCodec abstracts the tree-specific operations of one recovery
// tree scan (Rust tree_scan::Codec), parameterized by the key type K
// (Rust Codec::Key: the numeric catalog index and membership ID keys,
// and the owned feed-name keys). Only the codec compares keys; the
// walk never interprets one. Parameterizing the walk over K keeps the
// scan state and the returned first keys concrete, so no per-record
// interface boxing exists (the Rust Option<K> peer).
type treeCodec[K any] interface {
	object() validation.ValidationObject
	branchType() format.PageType
	leafType() format.PageType
	aux() uint32
	branchLayout() format.CellLayout
	leafLayout() format.CellLayout
	branchInvalid() validation.ValidationReason
	leafInvalid() validation.ValidationReason
	decodeBranch(cell []byte) (key K, child uint32, ok bool)
	decodeLeafKey(cell []byte) (key K, ok bool)
	less(a, b K) bool
	equal(a, b K) bool
}

// treeKeyOption is one optional scan key returned by value (the Rust
// Option<K> scan result): the ok flag carries the None separation, so
// the walk never boxes a key.
type treeKeyOption[K any] struct {
	value K
	ok    bool
}

// treeEvents consumes one recovery tree scan (Rust
// tree_scan::TreeEvents).
type treeEvents interface {
	pageAccepted() error
	pageRejected(ioUnreadable bool) error
	unknown(reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error
	leaf(page uint32, index int, cell []byte, ok bool) error
}

// scanTree walks one reachable slotted tree (Rust tree_scan::scan: an
// absent root is the empty tree; the previous-key state resets after
// every refused page or cell, exactly like the Rust arms).
func scanTree[K any](codec treeCodec[K], m *mapping.Mapping, meta format.Meta, root uint32, pages *pageSet, check func() error, events treeEvents) error {
	if root == 0 {
		return nil
	}
	state := treeScanState[K]{}
	var path [format.MaxTreeLevel + 1]uint32
	_, err := scanTreeNode(codec, m, meta, root, nil, true, &path, 0, &state, pages, check, events)
	return err
}

// treeScanState is the previous-key state of one tree walk (Rust
// tree_scan::State).
type treeScanState[K any] struct {
	previous K
	has      bool
}

// scanTreeNode walks one node of the tree (Rust scan_node: a refused
// node resets the order state and returns no key; a collapsed root
// streams the level envelope).
func scanTreeNode[K any](codec treeCodec[K], m *mapping.Mapping, meta format.Meta, pageNumber uint32, expectedLevel *uint16, root bool, path *[format.MaxTreeLevel + 1]uint32, depth int, state *treeScanState[K], pages *pageSet, check func() error, events treeEvents) (treeKeyOption[K], error) {
	if err := live.Checkpoint(check); err != nil {
		return treeKeyOption[K]{}, err
	}
	claimed, reason, err := claimTreePage(meta, pageNumber, path, depth, pages)
	if err != nil {
		return treeKeyOption[K]{}, err
	}
	if !claimed {
		state.has = false
		page := pageNumber
		if err := emitTreeUnknown(events, reason, codec.object(), &page); err != nil {
			return treeKeyOption[K]{}, err
		}
		return treeKeyOption[K]{}, nil
	}
	inspection, ok, header, err := readTreePage(codec, m, meta, pageNumber, expectedLevel, events)
	if err != nil {
		return treeKeyOption[K]{}, err
	}
	if !ok {
		state.has = false
		return treeKeyOption[K]{}, nil
	}
	if root && header.Level > 0 && header.ItemCount == 1 {
		page := pageNumber
		if err := emitTreeUnknown(events, validation.ReasonTreeLevelInvalid, codec.object(), &page); err != nil {
			return treeKeyOption[K]{}, err
		}
	}
	if header.Level == 0 {
		return scanTreeLeaf(codec, &inspection, header, pageNumber, state, events)
	}
	return scanTreeBranch(codec, m, meta, pageNumber, &inspection, header, path, depth, state, pages, check, events)
}

// claimTreePage claims one node through the page set (Rust
// tree_scan::claim_page).
func claimTreePage(meta format.Meta, pageNumber uint32, path *[format.MaxTreeLevel + 1]uint32, depth int, pages *pageSet) (bool, validation.ValidationReason, error) {
	return pages.claim(pageNumber, meta.PageCount, path[:], depth)
}

// readTreePage reads and parses one tree node and proves its layout
// exactly once (Rust read_page: the checked page access, the parse
// refusal classes, and the single inspect_layout; an accepted page
// counts the page event). The proved inspection is returned so the
// leaf and branch arms reuse it instead of re-proving the page, like
// the Rust cell walks that follow the single proof.
func readTreePage[K any](codec treeCodec[K], m *mapping.Mapping, meta format.Meta, pageNumber uint32, expectedLevel *uint16, events treeEvents) (format.LayoutInspection, bool, format.PageHeader, error) {
	page, problem := checkedPage(m, pageNumber, meta.PageCount)
	if problem != nil {
		if err := rejectTreePage(events, codec.object(), pageNumber, problem.reason, problem.ioUnreadable); err != nil {
			return format.LayoutInspection{}, false, format.PageHeader{}, err
		}
		return format.LayoutInspection{}, false, format.PageHeader{}, nil
	}
	header, ok, err := parseTreePage(codec, page, meta, pageNumber, expectedLevel, events)
	if err != nil || !ok {
		return format.LayoutInspection{}, false, format.PageHeader{}, err
	}
	layout := codec.branchLayout()
	if header.Level == 0 {
		layout = codec.leafLayout()
	}
	inspection, valid := format.InspectLayout(page, &header, layout)
	if !valid || inspection.ReservedNonzero {
		if err := rejectTreePage(events, codec.object(), pageNumber, validation.ReasonPageHeaderInvalid, false); err != nil {
			return format.LayoutInspection{}, false, format.PageHeader{}, err
		}
		return format.LayoutInspection{}, false, format.PageHeader{}, nil
	}
	if err := events.pageAccepted(); err != nil {
		return format.LayoutInspection{}, false, format.PageHeader{}, err
	}
	return inspection, true, header, nil
}

// parseTreePage runs the tree page header inspection (Rust
// parse_tree: the type class maps to PageTypeMismatch, every other
// header refusal to PageHeaderInvalid).
func parseTreePage[K any](codec treeCodec[K], page []byte, meta format.Meta, pageNumber uint32, expectedLevel *uint16, events treeEvents) (format.PageHeader, bool, error) {
	header, problem := format.InspectTreeHeader(page, meta.TxnID, byte(codec.branchType()), byte(codec.leafType()), codec.aux(), expectedLevel)
	if problem != format.TreeHeaderProblemNone {
		reason := validation.ReasonPageHeaderInvalid
		if problem == format.TreeHeaderProblemType {
			reason = validation.ReasonPageTypeMismatch
		}
		if err := rejectTreePage(events, codec.object(), pageNumber, reason, false); err != nil {
			return format.PageHeader{}, false, err
		}
		return format.PageHeader{}, false, nil
	}
	return header, true, nil
}

// rejectTreePage streams one refused tree page (Rust
// tree_scan::reject_page: the rejected page class then the envelope).
func rejectTreePage(events treeEvents, object validation.ValidationObject, pageNumber uint32, reason validation.ValidationReason, ioUnreadable bool) error {
	if err := events.pageRejected(ioUnreadable); err != nil {
		return err
	}
	return emitTreeUnknown(events, reason, object, &pageNumber)
}

// emitTreeUnknown streams one tree envelope (Rust tree_scan reject
// arms).
func emitTreeUnknown(events treeEvents, reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error {
	return events.unknown(reason, object, page)
}

// scanTreeLeaf streams the cells of one leaf page (Rust scan_leaf: an
// undecodable cell streams the leaf-invalid envelope and the no-cell
// event; the order regression streams its envelope; readable cells
// stream the leaf event).
func scanTreeLeaf[K any](codec treeCodec[K], inspection *format.LayoutInspection, header format.PageHeader, pageNumber uint32, state *treeScanState[K], events treeEvents) (treeKeyOption[K], error) {
	var first treeKeyOption[K]
	cells := inspection.Cells()
	for index := 0; index < int(header.ItemCount); index++ {
		cell, ok := cells.Next()
		if !ok {
			return first, pageDecodeError()
		}
		key, ok := codec.decodeLeafKey(cell)
		if !ok {
			page := pageNumber
			if err := emitTreeUnknown(events, codec.leafInvalid(), codec.object(), &page); err != nil {
				return first, err
			}
			if err := events.leaf(pageNumber, index, nil, false); err != nil {
				return first, err
			}
			state.has = false
			continue
		}
		if !first.ok {
			first = treeKeyOption[K]{value: key, ok: true}
		}
		if state.has && !codec.less(state.previous, key) {
			page := pageNumber
			if err := emitTreeUnknown(events, validation.ReasonTreeOrderInvalid, codec.object(), &page); err != nil {
				return first, err
			}
		}
		state.previous = key
		state.has = true
		if err := events.leaf(pageNumber, index, cell, true); err != nil {
			return first, err
		}
	}
	return first, nil
}

// scanTreeBranch walks one branch page (Rust scan_branch: an
// undecodable branch cell streams the branch-invalid envelope and
// resets the order state; the order and fence defects stream their
// envelopes, and the walk descends into every decoded child).
func scanTreeBranch[K any](codec treeCodec[K], m *mapping.Mapping, meta format.Meta, pageNumber uint32, inspection *format.LayoutInspection, header format.PageHeader, path *[format.MaxTreeLevel + 1]uint32, depth int, state *treeScanState[K], pages *pageSet, check func() error, events treeEvents) (treeKeyOption[K], error) {
	var first treeKeyOption[K]
	var previous K
	var hasPrevious bool
	cells := inspection.Cells()
	for index := 0; index < int(header.ItemCount); index++ {
		if err := live.Checkpoint(check); err != nil {
			return first, err
		}
		cell, ok := cells.Next()
		if !ok {
			return first, pageDecodeError()
		}
		key, child, ok := codec.decodeBranch(cell)
		if !ok {
			page := pageNumber
			if err := emitTreeUnknown(events, codec.branchInvalid(), codec.object(), &page); err != nil {
				return first, err
			}
			state.has = false
			continue
		}
		if !first.ok {
			first = treeKeyOption[K]{value: key, ok: true}
		}
		if hasPrevious && !codec.less(previous, key) {
			page := pageNumber
			if err := emitTreeUnknown(events, validation.ReasonTreeOrderInvalid, codec.object(), &page); err != nil {
				return first, err
			}
		}
		previous = key
		hasPrevious = true
		expected := header.Level - 1
		actual, err := scanTreeNode(codec, m, meta, child, &expected, false, path, depth+1, state, pages, check, events)
		if err != nil {
			return first, err
		}
		if actual.ok && !codec.equal(actual.value, key) {
			page := pageNumber
			if err := emitTreeUnknown(events, validation.ReasonTreeFenceInvalid, codec.object(), &page); err != nil {
				return first, err
			}
		}
	}
	return first, nil
}

// leafCounter counts the accepted cells of one tree scan (Rust
// tree_scan::LeafCounter): the count policy decides every cell, the
// overflow folds the ArithmeticOverflow class.
type leafCounter struct {
	meta           format.Meta
	count          uint64
	overflowDetail string
	accept         func(meta format.Meta, cell []byte) bool
}

func (c *leafCounter) pageAccepted() error {
	return nil
}

func (c *leafCounter) pageRejected(ioUnreadable bool) error {
	return nil
}

func (c *leafCounter) unknown(reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error {
	return nil
}

func (c *leafCounter) leaf(page uint32, index int, cell []byte, ok bool) error {
	if ok && c.accept(c.meta, cell) {
		next := c.count + 1
		if next == 0 {
			return overflowError(c.overflowDetail)
		}
		c.count = next
	}
	return nil
}
