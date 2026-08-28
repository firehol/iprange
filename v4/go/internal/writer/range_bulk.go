// Direct mapped-page construction of an ordered canonical range tree
// (Rust range_bulk.rs): a 6-level bottom-up builder with only-child
// collapse. Range records append in canonical order; full leaves roll
// upward as branch pages; finish yields the root and the record count.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

const rangeBulkBranchLevels = 6

// rangeBulkNode is one finished page plus its first key (Rust Node).
type rangeBulkNode[K any] struct {
	first      K
	pageNumber uint32
}

// rangeBulkPackedPage is one page under construction (Rust PackedPage).
type rangeBulkPackedPage[K any] struct {
	appender   format.SlottedAppender
	started    bool
	first      K
	hasFirst   bool
	pageNumber uint32
}

func (p *rangeBulkPackedPage[K]) active() bool { return p.started }

func (p *rangeBulkPackedPage[K]) start(store tree.Store, pageType format.PageType, bornTxn uint64, level uint16, aux uint32) error {
	pageNumber, err := store.Allocate()
	if err != nil {
		return err
	}
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return err
	}
	appender := format.NewSlottedAppender(page, pageType, bornTxn, level, aux)
	if err := store.RestoreDirty(pageNumber, tag); err != nil {
		return err
	}
	p.appender = appender
	p.started = true
	var zero K
	p.first = zero
	p.hasFirst = false
	p.pageNumber = pageNumber
	return nil
}

func (p *rangeBulkPackedPage[K]) push(store tree.Store, first K, cell []byte) (bool, error) {
	if p.pageNumber == 0 {
		return false, corrupt("ordered range page has no output page")
	}
	if !p.started {
		return false, corrupt("ordered range page is not active")
	}
	page, tag, err := store.Update(p.pageNumber)
	if err != nil {
		return false, err
	}
	appended, err := p.appender.TryPush(page, cell)
	if err != nil {
		return false, err
	}
	if err := store.RestoreDirty(p.pageNumber, tag); err != nil {
		return false, err
	}
	if appended && !p.hasFirst {
		p.first = first
		p.hasFirst = true
	}
	return appended, nil
}

func (p *rangeBulkPackedPage[K]) finish(store tree.Store) (rangeBulkNode[K], error) {
	appender := &p.appender
	if !p.started {
		return rangeBulkNode[K]{}, corrupt("ordered range page is not active")
	}
	pageNumber := p.pageNumber
	if pageNumber == 0 {
		return rangeBulkNode[K]{}, corrupt("ordered range page has no output page")
	}
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return rangeBulkNode[K]{}, err
	}
	if err := appender.Finish(page); err != nil {
		return rangeBulkNode[K]{}, err
	}
	if err := store.RestoreDirty(pageNumber, tag); err != nil {
		return rangeBulkNode[K]{}, err
	}
	if !p.hasFirst {
		return rangeBulkNode[K]{}, corrupt("ordered range page has no first key")
	}
	first := p.first
	p.started = false
	var zero K
	p.first = zero
	p.hasFirst = false
	p.pageNumber = 0
	return rangeBulkNode[K]{first: first, pageNumber: pageNumber}, nil
}

type rangeBulkBranchLevel[K any] struct {
	page      rangeBulkPackedPage[K]
	onlyChild rangeBulkNode[K]
	hasOnly   bool
	emitted   bool
}

type rangeBulkBuilder[K any] struct {
	bornTxn     uint64
	valueKind   uint8
	leaf        rangeBulkPackedPage[K]
	branches    [rangeBulkBranchLevels]rangeBulkBranchLevel[K]
	previous    rangeRecord[K]
	hasPrevious bool
	recordCount uint64
	family      rangeFamily[K]
	// recordScratch and branchScratch own the encoded record and branch
	// cells of one push (Rust Builder local cells). The codec interface
	// methods retain their slice argument, so a stack cell would escape
	// per record; the builder-owned buffers make the record push
	// allocation-free exactly like the Rust locals.
	recordScratch [rangeRecordMaxSize]byte
	branchScratch [rangeBranchMaxSize]byte
}

// newRangeBulkBuilder starts one ordered range tree builder (Rust
// Builder::new). family selects the address family of the records.
func newRangeBulkBuilder[K any](bornTxn uint64, valueKind uint8, family rangeFamily[K]) *rangeBulkBuilder[K] {
	return &rangeBulkBuilder[K]{bornTxn: bornTxn, valueKind: valueKind, family: family}
}

// init starts one ordered range tree builder in place (Rust
// Builder::new): the coverage ordered prefix embeds its builder so the
// per-workflow ordered path never allocates.
func (b *rangeBulkBuilder[K]) init(bornTxn uint64, valueKind uint8, family rangeFamily[K]) {
	*b = rangeBulkBuilder[K]{bornTxn: bornTxn, valueKind: valueKind, family: family}
}

var errRangeNotCanonical = invalid("ordered output ranges are not canonical")

// push appends one canonical range record; out-of-order or adjacent
// same-value records are rejected (Rust Builder::push).
func (b *rangeBulkBuilder[K]) push(store tree.Store, record rangeRecord[K]) error {
	ok, err := b.tryPush(store, record)
	if err != nil {
		return err
	}
	if !ok {
		return errRangeNotCanonical
	}
	return nil
}

// tryPush appends the record when it is canonical and reports whether it
// was appended (Rust Builder::try_push).
func (b *rangeBulkBuilder[K]) tryPush(store tree.Store, record rangeRecord[K]) (bool, error) {
	ok, err := b.canAppend(record)
	if err != nil || !ok {
		return ok, err
	}
	b.recordCount++
	cell := b.recordScratch[:]
	cellLen, err := b.family.EncodeRecord(record, cell)
	if err != nil {
		return false, err
	}
	if err := b.pushLeafCell(store, record.from, cell[:cellLen]); err != nil {
		return false, err
	}
	b.previous = record
	b.hasPrevious = true
	work.RangeEmitted(1)
	return true, nil
}

func (b *rangeBulkBuilder[K]) pushLeafCell(store tree.Store, first K, cell []byte) error {
	if !b.leaf.active() {
		if err := b.leaf.start(store, format.PageTypeRangeLeaf, b.bornTxn, 0, rangeFamilyAux(b.family)); err != nil {
			return err
		}
	}
	if pushed, err := b.leaf.push(store, first, cell); err != nil {
		return err
	} else if pushed {
		return nil
	}
	node, err := b.leaf.finish(store)
	if err != nil {
		return err
	}
	if err := b.pushNode(store, 0, node); err != nil {
		return err
	}
	if err := b.leaf.start(store, format.PageTypeRangeLeaf, b.bornTxn, 0, rangeFamilyAux(b.family)); err != nil {
		return err
	}
	if pushed, err := b.leaf.push(store, first, cell); err != nil {
		return err
	} else if !pushed {
		return corrupt("range record does not fit an empty leaf")
	}
	return nil
}

func (b *rangeBulkBuilder[K]) canAppend(record rangeRecord[K]) (bool, error) {
	if b.family.Less(record.to, record.from) {
		return false, invalid("range start is after its end")
	}
	if b.valueKind != format.ValueKindDirect && record.value == 0 {
		return false, corrupt("indirect range value is zero")
	}
	if !b.hasPrevious {
		return true, nil
	}
	previous := b.previous
	if b.family.Equal(previous.to, record.from) || !b.family.Less(previous.to, record.from) {
		return false, nil
	}
	// The comparator above covers from<=to; the canonical rules reject
	// overlap and adjacency with the same value (Rust can_append): from
	// must be strictly after the previous to, and adjacent same-value
	// ranges must be merged by the caller.
	next, ok := b.family.Next(previous.to)
	if ok && previous.value == record.value && b.family.Equal(next, record.from) {
		return false, nil
	}
	return true, nil
}

func (b *rangeBulkBuilder[K]) pushNode(store tree.Store, levelIndex int, node rangeBulkNode[K]) error {
	if levelIndex == rangeBulkBranchLevels {
		return pageSpaceExhausted()
	}
	if !b.branches[levelIndex].page.active() {
		if b.branches[levelIndex].hasOnly {
			first := b.branches[levelIndex].onlyChild
			b.branches[levelIndex].hasOnly = false
			if err := b.startBranch(store, levelIndex); err != nil {
				return err
			}
			if pushed, err := b.pushBranchCell(store, levelIndex, first); err != nil {
				return err
			} else if !pushed {
				return corrupt("range branch cell does not fit")
			}
		} else {
			b.branches[levelIndex].onlyChild = node
			b.branches[levelIndex].hasOnly = true
			return nil
		}
	}
	if pushed, err := b.pushBranchCell(store, levelIndex, node); err != nil {
		return err
	} else if pushed {
		return nil
	}
	parent, err := b.branches[levelIndex].page.finish(store)
	if err != nil {
		return err
	}
	b.branches[levelIndex].emitted = true
	if err := b.pushNode(store, levelIndex+1, parent); err != nil {
		return err
	}
	b.branches[levelIndex].onlyChild = node
	b.branches[levelIndex].hasOnly = true
	return nil
}

func (b *rangeBulkBuilder[K]) startBranch(store tree.Store, levelIndex int) error {
	aux := rangeFamilyAux(b.family)
	return b.branches[levelIndex].page.start(store, format.PageTypeRangeBranch, b.bornTxn, uint16(levelIndex)+1, aux)
}

func (b *rangeBulkBuilder[K]) pushBranchCell(store tree.Store, levelIndex int, node rangeBulkNode[K]) (bool, error) {
	cell := b.branchScratch[:]
	cellLen, err := encodeRangeBranch(b.family, node.first, node.pageNumber, cell)
	if err != nil {
		return false, err
	}
	return b.branches[levelIndex].page.push(store, node.first, cell[:cellLen])
}

// finish seals the tree and returns the root and the record count (Rust
// Builder::finish; the output-pass charge separates whole-tree builds
// from the coverage ordered-prefix builds).
func (b *rangeBulkBuilder[K]) finish(store tree.Store) (uint32, uint64, error) {
	work.OutputPass(1)
	return b.finishInline(store)
}

// finishInline seals the tree without charging a whole output pass (Rust
// Builder::finish_inline; the coverage ordered prefix seals inline inside
// one input workflow, so the pass is charged by the merge that consumes
// it, never here).
func (b *rangeBulkBuilder[K]) finishInline(store tree.Store) (uint32, uint64, error) {
	if b.recordCount == 0 {
		return 0, 0, nil
	}
	leaf, err := b.leaf.finish(store)
	if err != nil {
		return 0, 0, err
	}
	if err := b.pushNode(store, 0, leaf); err != nil {
		return 0, 0, err
	}
	for levelIndex := 0; levelIndex < rangeBulkBranchLevels; levelIndex++ {
		finished, err := b.finishLevel(store, levelIndex)
		if err != nil {
			return 0, 0, err
		}
		switch f := finished.(type) {
		case nil:
		case rangeBulkFinishedRoot:
			return uint32(f), b.recordCount, nil
		case rangeBulkFinishedParent[K]:
			if err := b.pushNode(store, levelIndex+1, rangeBulkNode[K](f)); err != nil {
				return 0, 0, err
			}
		}
	}
	return 0, 0, pageSpaceExhausted()
}

type rangeBulkFinishedRoot uint32
type rangeBulkFinishedParent[K any] rangeBulkNode[K]

func (b *rangeBulkBuilder[K]) finishLevel(store tree.Store, levelIndex int) (any, error) {
	if b.branches[levelIndex].page.active() {
		node, err := b.branches[levelIndex].page.finish(store)
		if err != nil {
			return nil, err
		}
		if b.branches[levelIndex].emitted {
			return rangeBulkFinishedParent[K](node), nil
		}
		return rangeBulkFinishedRoot(node.pageNumber), nil
	}
	if !b.branches[levelIndex].hasOnly {
		return nil, nil
	}
	child := b.branches[levelIndex].onlyChild
	b.branches[levelIndex].hasOnly = false
	if !b.branches[levelIndex].emitted {
		return rangeBulkFinishedRoot(child.pageNumber), nil
	}
	if err := b.startBranch(store, levelIndex); err != nil {
		return nil, err
	}
	if pushed, err := b.pushBranchCell(store, levelIndex, child); err != nil {
		return nil, err
	} else if !pushed {
		return nil, corrupt("range branch cell does not fit")
	}
	node, err := b.branches[levelIndex].page.finish(store)
	if err != nil {
		return nil, err
	}
	return rangeBulkFinishedParent[K](node), nil
}

func pageSpaceExhausted() error {
	return &format.Error{Code: format.CodePageSpaceExhausted, Detail: "v4 page-number space is exhausted"}
}

// rangeFamilyAux reports the mapped page AUX value of one range family
// codec (the format address family byte).
func rangeFamilyAux[K any](codec rangeFamily[K]) uint32 {
	if _, ok := any(codec).(rangeCodec4); ok {
		return uint32(format.AddressFamilyIPv4)
	}
	return uint32(format.AddressFamilyIPv6)
}

const (
	rangeRecordMaxSize = 36
	rangeBranchMaxSize = 20
)

// encodeRangeBranch writes one family branch cell (key prefix plus the
// child page number; Rust RangeCodec::write_branch).
func encodeRangeBranch[K any](codec rangeFamily[K], first K, child uint32, output []byte) (int, error) {
	size := codec.KeySize()
	codec.WriteKey(codec.KeyOf(first), output[0:size])
	format.PutU32(output[size:], child)
	return size + 4, nil
}
