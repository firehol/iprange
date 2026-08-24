package validation

// Tree walk validation (Rust validation/tree.rs): one codec-driven walk
// over a committed tree root with root-shape, per-page order, and fence
// checks, streaming findings through the context. The walk is
// allocation-free: the first-key results and the ordered-key cursor are
// values, and the cell iterator slices the inspected page.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// treeCodec carries the per-tree wire decoding of one validated B+tree
// (Rust validation/tree.rs Codec). The decode functions return ok=false
// for an undecodable cell; the walk reports the codec's invalid class.
type treeCodec struct {
	branchType    byte
	leafType      byte
	aux           uint32
	branchLayout  format.CellLayout
	leafLayout    format.CellLayout
	branchInvalid ValidationReason
	leafInvalid   ValidationReason
	branchKey     func(cell []byte) (tree.Key, bool)
	branchChild   func(cell []byte) (uint32, bool)
	leafKey       func(cell []byte) (tree.Key, bool)
}

// treeWalkResult mirrors Rust WalkResult.
type treeWalkResult struct {
	records uint64
}

// treeWalkState mirrors the Rust State: the ordered previous key and the
// record count of the whole walk.
type treeWalkState struct {
	records     uint64
	previous    tree.Key
	hasPrevious bool
}

// walkTree mirrors Rust tree::walk over one root: a zero root is an empty
// tree, every leaf record runs leaf, and the result carries the decoded
// record count.
func walkTree(ctx *context, root uint32, object ValidationObject, codec treeCodec, leaf func(*context, uint32, []byte) error) (treeWalkResult, error) {
	if root == 0 {
		return treeWalkResult{}, nil
	}
	state := treeWalkState{}
	var path [format.MaxTreeLevel + 1]uint32
	if _, _, err := walkTreeNode(ctx, root, object, codec, nil, true, &path, 0, &state, leaf); err != nil {
		return treeWalkResult{}, err
	}
	return treeWalkResult{records: state.records}, nil
}

// walkTreeNode visits one tree node and returns the first key of its
// subtree for the parent fence check (Rust walk_node).
func walkTreeNode(ctx *context, pageNumber uint32, object ValidationObject, codec treeCodec, expectedLevel *uint16, root bool, path *[format.MaxTreeLevel + 1]uint32, depth int, state *treeWalkState, leaf func(*context, uint32, []byte) error) (tree.Key, bool, error) {
	page, err := readTreeNodePage(ctx, pageNumber, object, path, depth)
	if err != nil || page == nil {
		state.hasPrevious = false
		return tree.Key{}, false, err
	}
	header, err := treePageHeader(ctx, pageNumber, page, object, treePageSpec{
		branchType:    codec.branchType,
		leafType:      codec.leafType,
		aux:           codec.aux,
		expectedLevel: expectedLevel,
	})
	if err != nil || header == nil {
		state.hasPrevious = false
		return tree.Key{}, false, err
	}
	if err := validateRootShape(ctx, pageNumber, object, root, header); err != nil {
		return tree.Key{}, false, err
	}
	inspection, err := validateTreeLayout(ctx, pageNumber, page, object, header, nodeLayout(codec, header.Level))
	if err != nil || inspection == nil {
		state.hasPrevious = false
		return tree.Key{}, false, err
	}
	if header.Level == 0 {
		return walkTreeLeaf(ctx, pageNumber, inspection, object, codec, state, leaf)
	}
	return walkTreeBranch(ctx, pageNumber, inspection, header, object, codec, path, depth, state, leaf)
}

// readTreeNodePage records the page in the walk path and reads it through
// the graph claims (Rust read_node_page: a path deeper than the maximum
// tree level is the TreeLevelInvalid class).
func readTreeNodePage(ctx *context, pageNumber uint32, object ValidationObject, path *[format.MaxTreeLevel + 1]uint32, depth int) ([]byte, error) {
	if depth >= len(path) {
		if err := ctx.emit(ReasonTreeLevelInvalid, object, &pageNumber, nil, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}
	path[depth] = pageNumber
	return ctx.readGraphPage(pageNumber, object, path[:depth])
}

// validateRootShape mirrors tree.rs validate_root_shape: a root branch
// with exactly one record is the TreeLevelInvalid class.
func validateRootShape(ctx *context, pageNumber uint32, object ValidationObject, root bool, header *format.PageHeader) error {
	if root && header.Level > 0 && header.ItemCount == 1 {
		if err := ctx.emit(ReasonTreeLevelInvalid, object, &pageNumber, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// nodeLayout selects the cell layout of one tree level (Rust node_layout:
// level zero uses the leaf layout, every higher level the branch layout).
func nodeLayout(codec treeCodec, level uint16) format.CellLayout {
	if level == 0 {
		return codec.leafLayout
	}
	return codec.branchLayout
}

// walkTreeBranch visits every branch cell in order: the per-page key
// order, the child subtree with its expected level, and the child-first
// fence (Rust walk_branch).
func walkTreeBranch(ctx *context, pageNumber uint32, inspection *format.LayoutInspection, header *format.PageHeader, object ValidationObject, codec treeCodec, path *[format.MaxTreeLevel + 1]uint32, depth int, state *treeWalkState, leaf func(*context, uint32, []byte) error) (tree.Key, bool, error) {
	var keys branchKeys
	expected := header.Level - 1
	cells := inspection.Cells()
	for {
		cell, ok := cells.Next()
		if !ok {
			break
		}
		key, child, ok, err := branchTreeEntry(ctx, pageNumber, object, codec, cell, state)
		if err != nil || !ok {
			if err != nil {
				return tree.Key{}, false, err
			}
			continue
		}
		if err := recordBranchKey(ctx, pageNumber, object, key, &keys); err != nil {
			return tree.Key{}, false, err
		}
		actual, hasActual, err := walkTreeNode(ctx, child, object, codec, &expected, false, path, depth+1, state, leaf)
		if err != nil {
			return tree.Key{}, false, err
		}
		if err := validateFence(ctx, pageNumber, object, key, actual, hasActual); err != nil {
			return tree.Key{}, false, err
		}
	}
	return keys.first, keys.hasFirst, nil
}

// branchKeys is the ordered branch-key cursor of one branch page (Rust
// first/previous locals).
type branchKeys struct {
	first       tree.Key
	hasFirst    bool
	previous    tree.Key
	hasPrevious bool
}

// branchTreeEntry decodes one branch cell (Rust branch_entry: an
// undecodable key or child reports the codec branch-invalid class and
// resets the walk order state).
func branchTreeEntry(ctx *context, pageNumber uint32, object ValidationObject, codec treeCodec, cell []byte, state *treeWalkState) (tree.Key, uint32, bool, error) {
	key, keyOK := codec.branchKey(cell)
	child, childOK := codec.branchChild(cell)
	if !keyOK || !childOK {
		if err := ctx.emit(codec.branchInvalid, object, &pageNumber, nil, nil); err != nil {
			return tree.Key{}, 0, false, err
		}
		state.hasPrevious = false
		return tree.Key{}, 0, false, nil
	}
	return key, child, true, nil
}

// recordBranchKey records one branch key and reports a non-increasing
// key as the TreeOrderInvalid class (Rust record_branch_key).
func recordBranchKey(ctx *context, pageNumber uint32, object ValidationObject, key tree.Key, keys *branchKeys) error {
	if !keys.hasFirst {
		keys.first = key
		keys.hasFirst = true
	}
	if keys.hasPrevious && !keys.previous.Less(key) {
		if err := ctx.emit(ReasonTreeOrderInvalid, object, &pageNumber, nil, nil); err != nil {
			return err
		}
	}
	keys.previous = key
	keys.hasPrevious = true
	return nil
}

// validateFence checks one branch key against the first key of its child
// subtree (Rust validate_fence: a mismatch is the TreeFenceInvalid
// class).
func validateFence(ctx *context, pageNumber uint32, object ValidationObject, expected tree.Key, actual tree.Key, hasActual bool) error {
	if hasActual && !actual.Equal(expected) {
		if err := ctx.emit(ReasonTreeFenceInvalid, object, &pageNumber, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

// walkTreeLeaf visits every leaf cell in order (Rust walk_leaf: the
// per-page key order, the checked record count, and the leaf callback).
func walkTreeLeaf(ctx *context, pageNumber uint32, inspection *format.LayoutInspection, object ValidationObject, codec treeCodec, state *treeWalkState, leaf func(*context, uint32, []byte) error) (tree.Key, bool, error) {
	first := tree.Key{}
	hasFirst := false
	cells := inspection.Cells()
	for {
		cell, ok := cells.Next()
		if !ok {
			break
		}
		key, ok := codec.leafKey(cell)
		if !ok {
			if err := ctx.emit(codec.leafInvalid, object, &pageNumber, nil, nil); err != nil {
				return tree.Key{}, false, err
			}
			state.hasPrevious = false
			continue
		}
		if !hasFirst {
			first = key
			hasFirst = true
		}
		if state.hasPrevious && !state.previous.Less(key) {
			if err := ctx.emit(ReasonTreeOrderInvalid, object, &pageNumber, nil, nil); err != nil {
				return tree.Key{}, false, err
			}
		}
		state.previous = key
		state.hasPrevious = true
		if state.records == ^uint64(0) {
			return tree.Key{}, false, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation tree record count"}
		}
		state.records++
		if err := leaf(ctx, pageNumber, cell); err != nil {
			return tree.Key{}, false, err
		}
	}
	return first, hasFirst, nil
}
