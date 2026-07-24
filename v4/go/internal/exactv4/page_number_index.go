package exactv4

import (
	"encoding/binary"
	"fmt"
)

// pageNumberIndex is private transaction scratch for collecting arbitrary
// physical page numbers and replaying them in increasing order. Its logical
// pages never have a v4 physical page number or publication path.
const (
	pageNumberIndexLeafEntryBytes   = 4
	pageNumberIndexBranchEntryBytes = 8
	pageNumberIndexLeafCapacity     = PageSize / pageNumberIndexLeafEntryBytes
	pageNumberIndexBranchCapacity   = PageSize / pageNumberIndexBranchEntryBytes
	pageNumberIndexMaxBranchDepth   = 3
)

const pageNumberIndexNoPage uint32 = ^uint32(0)

type pageNumberIndexPageKind uint8

const (
	pageNumberIndexPageEmpty pageNumberIndexPageKind = iota
	pageNumberIndexPageLeaf
	pageNumberIndexPageBranch
)

// pageNumberIndexPage is one caller-owned logical 4 KiB node. Its bytes are
// deliberately dense so the index scales with listed pages, not input batches.
type pageNumberIndexPage struct {
	bytes [PageSize]byte
	kind  pageNumberIndexPageKind
	count uint16
}

type pageNumberIndexWorkspace struct {
	pages []pageNumberIndexPage
}

func newPageNumberIndexWorkspace(pages []pageNumberIndexPage) pageNumberIndexWorkspace {
	return pageNumberIndexWorkspace{pages: pages}
}

func (w *pageNumberIndexWorkspace) clean() bool {
	if w == nil {
		return false
	}
	for index := range w.pages {
		if w.pages[index].kind != pageNumberIndexPageEmpty || w.pages[index].count != 0 {
			return false
		}
	}
	return true
}

func (w *pageNumberIndexWorkspace) reset() {
	if w != nil {
		clear(w.pages)
	}
}

func (w *pageNumberIndexWorkspace) discardAfterAbort() { w.reset() }

type pageNumberIndexErrorCode uint8

const (
	pageNumberIndexErrWorkspaceBusy pageNumberIndexErrorCode = iota + 1
	pageNumberIndexErrWorkspacePageLimit
	pageNumberIndexErrPageBudget
	pageNumberIndexErrInvalidPageReference
	pageNumberIndexErrInvalidPageEncoding
	pageNumberIndexErrTreeTooDeep
	pageNumberIndexErrFailed
)

type pageNumberIndexError struct {
	code     pageNumberIndexErrorCode
	required uint64
	actual   uint64
}

func (e *pageNumberIndexError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 page-number index: error %d", e.code)
}

type pageNumberIndexPathFrame struct {
	page       uint32
	childIndex int
}

type pageNumberIndexBranchEntry struct {
	maximum uint32
	child   uint32
}

// pageNumberIndex is a fixed-page B+ tree with sorted u32 leaves. It uses
// only caller-supplied storage and all splits are preflighted before mutation.
type pageNumberIndex struct {
	workspace *pageNumberIndexWorkspace
	root      uint32
	pages     int
	values    uint64
	failed    bool

	leafScratch   [pageNumberIndexLeafCapacity + 1]uint32
	branchScratch [pageNumberIndexBranchCapacity + 1]pageNumberIndexBranchEntry
}

func newPageNumberIndex(workspace *pageNumberIndexWorkspace) (pageNumberIndex, error) {
	if workspace == nil || !workspace.clean() {
		return pageNumberIndex{}, &pageNumberIndexError{code: pageNumberIndexErrWorkspaceBusy}
	}
	if uint64(len(workspace.pages)) > uint64(^uint32(0)) {
		return pageNumberIndex{}, &pageNumberIndexError{code: pageNumberIndexErrWorkspacePageLimit}
	}
	return pageNumberIndex{workspace: workspace, root: pageNumberIndexNoPage}, nil
}

func (e *pageNumberIndex) len() uint64 {
	if e == nil {
		return 0
	}
	return e.values
}

func (e *pageNumberIndex) logicalPageCount() int {
	if e == nil {
		return 0
	}
	return e.pages
}

func (e *pageNumberIndex) discardAfterAbort() {
	if e == nil {
		return
	}
	if e.workspace != nil {
		e.workspace.discardAfterAbort()
	}
	e.root = pageNumberIndexNoPage
	e.pages = 0
	e.values = 0
	e.failed = false
}

func (e *pageNumberIndex) fail(err error) error {
	e.failed = true
	return err
}

func (e *pageNumberIndex) page(reference uint32) (*pageNumberIndexPage, error) {
	if e == nil || e.workspace == nil || uint64(reference) >= uint64(e.pages) {
		return nil, &pageNumberIndexError{code: pageNumberIndexErrInvalidPageReference}
	}
	return &e.workspace.pages[int(reference)], nil
}

func pageNumberIndexLeafValue(page *pageNumberIndexPage, index int) uint32 {
	return binary.LittleEndian.Uint32(page.bytes[index*pageNumberIndexLeafEntryBytes:])
}

func pageNumberIndexSetLeafValue(page *pageNumberIndexPage, index int, value uint32) {
	binary.LittleEndian.PutUint32(page.bytes[index*pageNumberIndexLeafEntryBytes:], value)
}

func pageNumberIndexBranchEntryAt(page *pageNumberIndexPage, index int) pageNumberIndexBranchEntry {
	offset := index * pageNumberIndexBranchEntryBytes
	return pageNumberIndexBranchEntry{
		maximum: binary.LittleEndian.Uint32(page.bytes[offset:]),
		child:   binary.LittleEndian.Uint32(page.bytes[offset+4:]),
	}
}

func pageNumberIndexSetBranchEntry(page *pageNumberIndexPage, index int, entry pageNumberIndexBranchEntry) {
	offset := index * pageNumberIndexBranchEntryBytes
	binary.LittleEndian.PutUint32(page.bytes[offset:], entry.maximum)
	binary.LittleEndian.PutUint32(page.bytes[offset+4:], entry.child)
}

func (e *pageNumberIndex) validateLeaf(page *pageNumberIndexPage) error {
	if page.kind != pageNumberIndexPageLeaf || int(page.count) == 0 || int(page.count) > pageNumberIndexLeafCapacity {
		return &pageNumberIndexError{code: pageNumberIndexErrInvalidPageEncoding}
	}
	return nil
}

func (e *pageNumberIndex) validateBranch(page *pageNumberIndexPage) error {
	if page.kind != pageNumberIndexPageBranch || int(page.count) == 0 || int(page.count) > pageNumberIndexBranchCapacity {
		return &pageNumberIndexError{code: pageNumberIndexErrInvalidPageEncoding}
	}
	return nil
}

func pageNumberIndexLeafSearch(page *pageNumberIndexPage, value uint32) int {
	low, high := 0, int(page.count)
	for low < high {
		middle := low + (high-low)/2
		if pageNumberIndexLeafValue(page, middle) < value {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low
}

func pageNumberIndexBranchSearch(page *pageNumberIndexPage, value uint32) int {
	low, high := 0, int(page.count)
	for low < high {
		middle := low + (high-low)/2
		if pageNumberIndexBranchEntryAt(page, middle).maximum < value {
			low = middle + 1
		} else {
			high = middle
		}
	}
	if low == int(page.count) {
		return low - 1
	}
	return low
}

func (e *pageNumberIndex) requiredInsertPages(path []pageNumberIndexPathFrame, leaf *pageNumberIndexPage) (int, error) {
	if int(leaf.count) < pageNumberIndexLeafCapacity {
		return 0, nil
	}
	required := 1 // split leaf
	carry := true
	for index := len(path) - 1; index >= 0 && carry; index-- {
		parent, err := e.page(path[index].page)
		if err != nil {
			return 0, err
		}
		if int(parent.count) == pageNumberIndexBranchCapacity {
			required++ // split this branch
		} else {
			carry = false
		}
	}
	if carry {
		required++ // new root after a root split
	}
	return required, nil
}

func (e *pageNumberIndex) reserveNewPages(required int) error {
	if required < 0 || e.workspace == nil || required > len(e.workspace.pages)-e.pages {
		actual := 0
		if e.workspace != nil {
			actual = len(e.workspace.pages) - e.pages
		}
		return &pageNumberIndexError{code: pageNumberIndexErrPageBudget, required: uint64(required), actual: uint64(actual)}
	}
	return nil
}

func (e *pageNumberIndex) allocatePage(kind pageNumberIndexPageKind) uint32 {
	index := e.pages
	e.pages++
	page := &e.workspace.pages[index]
	*page = pageNumberIndexPage{kind: kind}
	return uint32(index)
}

func pageNumberIndexWriteLeaf(page *pageNumberIndexPage, values []uint32) {
	*page = pageNumberIndexPage{kind: pageNumberIndexPageLeaf, count: uint16(len(values))}
	for index := range values {
		pageNumberIndexSetLeafValue(page, index, values[index])
	}
}

func pageNumberIndexWriteBranch(page *pageNumberIndexPage, entries []pageNumberIndexBranchEntry) {
	*page = pageNumberIndexPage{kind: pageNumberIndexPageBranch, count: uint16(len(entries))}
	for index := range entries {
		pageNumberIndexSetBranchEntry(page, index, entries[index])
	}
}

func pageNumberIndexBranchMaximum(page *pageNumberIndexPage) uint32 {
	return pageNumberIndexBranchEntryAt(page, int(page.count)-1).maximum
}

func pageNumberIndexLeafMaximum(page *pageNumberIndexPage) uint32 {
	return pageNumberIndexLeafValue(page, int(page.count)-1)
}

func pageNumberIndexNodeMaximum(page *pageNumberIndexPage) uint32 {
	if page.kind == pageNumberIndexPageLeaf {
		return pageNumberIndexLeafMaximum(page)
	}
	return pageNumberIndexBranchMaximum(page)
}

func (e *pageNumberIndex) updateAncestorMaximums(path []pageNumberIndexPathFrame, lastParent int) {
	childRef := path[lastParent].page
	child := &e.workspace.pages[int(childRef)]
	maximum := pageNumberIndexNodeMaximum(child)
	for index := lastParent - 1; index >= 0; index-- {
		parent := &e.workspace.pages[int(path[index].page)]
		childIndex := path[index].childIndex
		entry := pageNumberIndexBranchEntryAt(parent, childIndex)
		entry.maximum = maximum
		pageNumberIndexSetBranchEntry(parent, childIndex, entry)
		if childIndex != int(parent.count)-1 {
			return
		}
		maximum = pageNumberIndexBranchMaximum(parent)
	}
}

func (e *pageNumberIndex) updateLeafMaximum(path []pageNumberIndexPathFrame, leafRef uint32) {
	childRef := leafRef
	child := &e.workspace.pages[int(childRef)]
	maximum := pageNumberIndexNodeMaximum(child)
	for index := len(path) - 1; index >= 0; index-- {
		parent := &e.workspace.pages[int(path[index].page)]
		childIndex := path[index].childIndex
		entry := pageNumberIndexBranchEntryAt(parent, childIndex)
		entry.maximum = maximum
		pageNumberIndexSetBranchEntry(parent, childIndex, entry)
		if childIndex != int(parent.count)-1 {
			return
		}
		childRef = path[index].page
		child = &e.workspace.pages[int(childRef)]
		maximum = pageNumberIndexNodeMaximum(child)
	}
}

func (e *pageNumberIndex) insert(value uint32) (bool, error) {
	if e == nil || e.workspace == nil {
		return false, &pageNumberIndexError{code: pageNumberIndexErrWorkspaceBusy}
	}
	if e.failed {
		return false, &pageNumberIndexError{code: pageNumberIndexErrFailed}
	}
	if e.root == pageNumberIndexNoPage {
		if err := e.reserveNewPages(1); err != nil {
			return false, err
		}
		root := e.allocatePage(pageNumberIndexPageLeaf)
		pageNumberIndexWriteLeaf(&e.workspace.pages[int(root)], e.leafScratch[:1])
		pageNumberIndexSetLeafValue(&e.workspace.pages[int(root)], 0, value)
		e.root = root
		e.values = 1
		return true, nil
	}

	var path [pageNumberIndexMaxBranchDepth]pageNumberIndexPathFrame
	pathLen := 0
	current := e.root
	var leaf *pageNumberIndexPage
	for {
		page, err := e.page(current)
		if err != nil {
			return false, e.fail(err)
		}
		switch page.kind {
		case pageNumberIndexPageLeaf:
			if err = e.validateLeaf(page); err != nil {
				return false, e.fail(err)
			}
			leaf = page
		case pageNumberIndexPageBranch:
			if err = e.validateBranch(page); err != nil {
				return false, e.fail(err)
			}
			if pathLen == len(path) {
				return false, e.fail(&pageNumberIndexError{code: pageNumberIndexErrTreeTooDeep})
			}
			childIndex := pageNumberIndexBranchSearch(page, value)
			path[pathLen] = pageNumberIndexPathFrame{page: current, childIndex: childIndex}
			pathLen++
			current = pageNumberIndexBranchEntryAt(page, childIndex).child
		default:
			return false, e.fail(&pageNumberIndexError{code: pageNumberIndexErrInvalidPageEncoding})
		}
		if leaf != nil {
			break
		}
	}
	insertAt := pageNumberIndexLeafSearch(leaf, value)
	if insertAt != int(leaf.count) && pageNumberIndexLeafValue(leaf, insertAt) == value {
		return false, nil
	}
	if e.values == ^uint64(0) {
		return false, e.fail(&pageNumberIndexError{code: pageNumberIndexErrInvalidPageEncoding})
	}
	if required, err := e.requiredInsertPages(path[:pathLen], leaf); err != nil {
		return false, e.fail(err)
	} else if err = e.reserveNewPages(required); err != nil {
		return false, err
	}

	if int(leaf.count) < pageNumberIndexLeafCapacity {
		oldCount := int(leaf.count)
		copy(leaf.bytes[(insertAt+1)*pageNumberIndexLeafEntryBytes:(oldCount+1)*pageNumberIndexLeafEntryBytes], leaf.bytes[insertAt*pageNumberIndexLeafEntryBytes:oldCount*pageNumberIndexLeafEntryBytes])
		pageNumberIndexSetLeafValue(leaf, insertAt, value)
		leaf.count++
		if pathLen != 0 && insertAt == oldCount {
			e.updateLeafMaximum(path[:pathLen], current)
		}
		e.values++
		return true, nil
	}

	for index := 0; index < int(leaf.count); index++ {
		e.leafScratch[index] = pageNumberIndexLeafValue(leaf, index)
	}
	copy(e.leafScratch[insertAt+1:], e.leafScratch[insertAt:int(leaf.count)])
	e.leafScratch[insertAt] = value
	leftCount := (pageNumberIndexLeafCapacity + 1) / 2
	rightCount := pageNumberIndexLeafCapacity + 1 - leftCount
	pageNumberIndexWriteLeaf(leaf, e.leafScratch[:leftCount])
	rightRef := e.allocatePage(pageNumberIndexPageLeaf)
	rightLeaf := &e.workspace.pages[int(rightRef)]
	pageNumberIndexWriteLeaf(rightLeaf, e.leafScratch[leftCount:leftCount+rightCount])
	leftRef := current
	leftMaximum := pageNumberIndexLeafMaximum(leaf)
	rightMaximum := pageNumberIndexLeafMaximum(rightLeaf)

	for pathIndex := pathLen - 1; pathIndex >= 0; pathIndex-- {
		parentRef := path[pathIndex].page
		parent := &e.workspace.pages[int(parentRef)]
		childIndex := path[pathIndex].childIndex
		if pageNumberIndexBranchEntryAt(parent, childIndex).child != leftRef {
			return false, e.fail(&pageNumberIndexError{code: pageNumberIndexErrInvalidPageEncoding})
		}
		if int(parent.count) < pageNumberIndexBranchCapacity {
			oldCount := int(parent.count)
			copy(parent.bytes[(childIndex+2)*pageNumberIndexBranchEntryBytes:(oldCount+1)*pageNumberIndexBranchEntryBytes], parent.bytes[(childIndex+1)*pageNumberIndexBranchEntryBytes:oldCount*pageNumberIndexBranchEntryBytes])
			pageNumberIndexSetBranchEntry(parent, childIndex, pageNumberIndexBranchEntry{maximum: leftMaximum, child: leftRef})
			pageNumberIndexSetBranchEntry(parent, childIndex+1, pageNumberIndexBranchEntry{maximum: rightMaximum, child: rightRef})
			parent.count++
			e.updateAncestorMaximums(path[:pathLen], pathIndex)
			e.values++
			return true, nil
		}

		oldCount := int(parent.count)
		for index := 0; index < oldCount; index++ {
			e.branchScratch[index] = pageNumberIndexBranchEntryAt(parent, index)
		}
		copy(e.branchScratch[childIndex+2:], e.branchScratch[childIndex+1:oldCount])
		e.branchScratch[childIndex] = pageNumberIndexBranchEntry{maximum: leftMaximum, child: leftRef}
		e.branchScratch[childIndex+1] = pageNumberIndexBranchEntry{maximum: rightMaximum, child: rightRef}
		leftCount := (pageNumberIndexBranchCapacity + 1) / 2
		rightCount := pageNumberIndexBranchCapacity + 1 - leftCount
		pageNumberIndexWriteBranch(parent, e.branchScratch[:leftCount])
		rightRef = e.allocatePage(pageNumberIndexPageBranch)
		rightBranch := &e.workspace.pages[int(rightRef)]
		pageNumberIndexWriteBranch(rightBranch, e.branchScratch[leftCount:leftCount+rightCount])
		leftRef = parentRef
		leftMaximum = pageNumberIndexBranchMaximum(parent)
		rightMaximum = pageNumberIndexBranchMaximum(rightBranch)
	}

	root := e.allocatePage(pageNumberIndexPageBranch)
	pageNumberIndexWriteBranch(&e.workspace.pages[int(root)], []pageNumberIndexBranchEntry{
		{maximum: leftMaximum, child: leftRef},
		{maximum: rightMaximum, child: rightRef},
	})
	e.root = root
	e.values++
	return true, nil
}

func (e *pageNumberIndex) visitAscending(visit func(uint32) error) error {
	if e == nil || e.workspace == nil || visit == nil {
		return &pageNumberIndexError{code: pageNumberIndexErrWorkspaceBusy}
	}
	if e.failed {
		return &pageNumberIndexError{code: pageNumberIndexErrFailed}
	}
	if e.root == pageNumberIndexNoPage {
		return nil
	}
	return e.visitNode(e.root, 0, visit)
}

func (e *pageNumberIndex) visitNode(reference uint32, depth int, visit func(uint32) error) error {
	page, err := e.page(reference)
	if err != nil {
		return e.fail(err)
	}
	switch page.kind {
	case pageNumberIndexPageLeaf:
		if err = e.validateLeaf(page); err != nil {
			return e.fail(err)
		}
		for index := 0; index < int(page.count); index++ {
			if err = visit(pageNumberIndexLeafValue(page, index)); err != nil {
				return err
			}
		}
		return nil
	case pageNumberIndexPageBranch:
		if depth == pageNumberIndexMaxBranchDepth {
			return e.fail(&pageNumberIndexError{code: pageNumberIndexErrTreeTooDeep})
		}
		if err = e.validateBranch(page); err != nil {
			return e.fail(err)
		}
		for index := 0; index < int(page.count); index++ {
			if err = e.visitNode(pageNumberIndexBranchEntryAt(page, index).child, depth+1, visit); err != nil {
				return err
			}
		}
		return nil
	default:
		return e.fail(&pageNumberIndexError{code: pageNumberIndexErrInvalidPageEncoding})
	}
}
