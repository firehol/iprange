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
type rangeBulkNode struct {
	first      tree.Key
	pageNumber uint32
}

// rangeBulkPackedPage is one page under construction (Rust PackedPage).
type rangeBulkPackedPage struct {
	appender   *format.SlottedAppender
	first      tree.Key
	hasFirst   bool
	pageNumber uint32
}

func (p *rangeBulkPackedPage) active() bool { return p.appender != nil }

func (p *rangeBulkPackedPage) start(store tree.Store, pageType format.PageType, bornTxn uint64, level uint16, aux uint32) error {
	pageNumber, err := store.Allocate()
	if err != nil {
		return err
	}
	var appender *format.SlottedAppender
	if err := store.Update(pageNumber, func(page []byte) error {
		appender = format.NewSlottedAppender(page, pageType, bornTxn, level, aux)
		return nil
	}); err != nil {
		return err
	}
	p.appender = appender
	p.first = tree.Key{}
	p.hasFirst = false
	p.pageNumber = pageNumber
	return nil
}

func (p *rangeBulkPackedPage) push(store tree.Store, first tree.Key, cell []byte) (bool, error) {
	if p.pageNumber == 0 {
		return false, corrupt("ordered range page has no output page")
	}
	if p.appender == nil {
		return false, corrupt("ordered range page is not active")
	}
	var appended bool
	if err := store.Update(p.pageNumber, func(page []byte) error {
		var err error
		appended, err = p.appender.TryPush(page, cell)
		return err
	}); err != nil {
		return false, err
	}
	if appended && !p.hasFirst {
		p.first = first
		p.hasFirst = true
	}
	return appended, nil
}

func (p *rangeBulkPackedPage) finish(store tree.Store) (rangeBulkNode, error) {
	appender := p.appender
	if appender == nil {
		return rangeBulkNode{}, corrupt("ordered range page is not active")
	}
	pageNumber := p.pageNumber
	if pageNumber == 0 {
		return rangeBulkNode{}, corrupt("ordered range page has no output page")
	}
	if err := store.Update(pageNumber, func(page []byte) error {
		return appender.Finish(page)
	}); err != nil {
		return rangeBulkNode{}, err
	}
	if !p.hasFirst {
		return rangeBulkNode{}, corrupt("ordered range page has no first key")
	}
	first := p.first
	p.appender = nil
	p.first = tree.Key{}
	p.hasFirst = false
	p.pageNumber = 0
	return rangeBulkNode{first: first, pageNumber: pageNumber}, nil
}

type rangeBulkBranchLevel struct {
	page      rangeBulkPackedPage
	onlyChild rangeBulkNode
	hasOnly   bool
	emitted   bool
}

type rangeBulkBuilder struct {
	bornTxn     uint64
	valueKind   uint8
	leaf        rangeBulkPackedPage
	branches    [rangeBulkBranchLevels]rangeBulkBranchLevel
	previous    rangeRecord
	hasPrevious bool
	recordCount uint64
	family      uint8
}

// newRangeBulkBuilder starts one ordered range tree builder (Rust
// Builder::new). family selects the v4 address family of the records.
func newRangeBulkBuilder(bornTxn uint64, valueKind, family uint8) *rangeBulkBuilder {
	return &rangeBulkBuilder{bornTxn: bornTxn, valueKind: valueKind, family: family}
}

var errRangeNotCanonical = invalid("ordered output ranges are not canonical")

// push appends one canonical range record; out-of-order or adjacent
// same-value records are rejected (Rust Builder::push).
func (b *rangeBulkBuilder) push(store tree.Store, record rangeRecord) error {
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
func (b *rangeBulkBuilder) tryPush(store tree.Store, record rangeRecord) (bool, error) {
	ok, err := b.canAppend(record)
	if err != nil || !ok {
		return ok, err
	}
	b.recordCount++
	var cell [rangeRecordMaxSize]byte
	cellLen, err := encodeRangeRecord(b.family, record, cell[:])
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

func (b *rangeBulkBuilder) pushLeafCell(store tree.Store, first tree.Key, cell []byte) error {
	if !b.leaf.active() {
		if err := b.leaf.start(store, rangeLeafType(b.family), b.bornTxn, 0, uint32(b.family)); err != nil {
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
	if err := b.leaf.start(store, rangeLeafType(b.family), b.bornTxn, 0, uint32(b.family)); err != nil {
		return err
	}
	if pushed, err := b.leaf.push(store, first, cell); err != nil {
		return err
	} else if !pushed {
		return corrupt("range record does not fit an empty leaf")
	}
	return nil
}

func (b *rangeBulkBuilder) canAppend(record rangeRecord) (bool, error) {
	if record.from.Hi > record.to.Hi || (record.from.Hi == record.to.Hi && record.from.Lo > record.to.Lo) {
		return false, invalid("range start is after its end")
	}
	if b.valueKind != format.ValueKindDirect && record.value == 0 {
		return false, corrupt("indirect range value is zero")
	}
	if !b.hasPrevious {
		return true, nil
	}
	previous := b.previous
	if previous.to.Equal(record.from) || !previous.to.Less(record.from) {
		return false, nil
	}
	// The comparator above covers from<=to; the canonical rules reject
	// overlap and adjacency with the same value (Rust can_append): from
	// must be strictly after the previous to, and adjacent same-value
	// ranges must be merged by the caller.
	next, ok := nextKey(b.family, previous.to)
	if ok && previous.value == record.value && next.Equal(record.from) {
		return false, nil
	}
	return true, nil
}

func (b *rangeBulkBuilder) pushNode(store tree.Store, levelIndex int, node rangeBulkNode) error {
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

func (b *rangeBulkBuilder) startBranch(store tree.Store, levelIndex int) error {
	return b.branches[levelIndex].page.start(store, rangeBranchType(b.family), b.bornTxn, uint16(levelIndex)+1, uint32(b.family))
}

func (b *rangeBulkBuilder) pushBranchCell(store tree.Store, levelIndex int, node rangeBulkNode) (bool, error) {
	var cell [rangeBranchMaxSize]byte
	cellLen, err := encodeRangeBranch(b.family, node.first, node.pageNumber, cell[:])
	if err != nil {
		return false, err
	}
	return b.branches[levelIndex].page.push(store, node.first, cell[:cellLen])
}

// finish seals the tree and returns the root and the record count (Rust
// Builder::finish).
func (b *rangeBulkBuilder) finish(store tree.Store) (uint32, uint64, error) {
	work.OutputPass(1)
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
		case rangeBulkFinishedParent:
			if err := b.pushNode(store, levelIndex+1, rangeBulkNode(f)); err != nil {
				return 0, 0, err
			}
		}
	}
	return 0, 0, pageSpaceExhausted()
}

type rangeBulkFinishedRoot uint32
type rangeBulkFinishedParent rangeBulkNode

func (b *rangeBulkBuilder) finishLevel(store tree.Store, levelIndex int) (any, error) {
	if b.branches[levelIndex].page.active() {
		node, err := b.branches[levelIndex].page.finish(store)
		if err != nil {
			return nil, err
		}
		if b.branches[levelIndex].emitted {
			return rangeBulkFinishedParent(node), nil
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
	return rangeBulkFinishedParent(node), nil
}

func pageSpaceExhausted() error {
	return &format.Error{Code: format.CodePageSpaceExhausted, Detail: "range tree page space exhausted"}
}

func rangeLeafType(family uint8) format.PageType   { return format.PageTypeRangeLeaf }
func rangeBranchType(family uint8) format.PageType { return format.PageTypeRangeBranch }
func nextKey(family uint8, key tree.Key) (tree.Key, bool) {
	if family == format.AddressFamilyIPv4 {
		if key.Hi == 0xFFFFFFFF {
			return tree.Key{}, false
		}
		return tree.Key{Hi: key.Hi + 1}, true
	}
	hi, lo := key.Hi, key.Lo
	lo++
	if lo == 0 {
		hi++
		if hi == 0 {
			return tree.Key{}, false
		}
	}
	return tree.Key{Hi: hi, Lo: lo}, true
}

const (
	rangeRecordMaxSize = 36
	rangeBranchMaxSize = 20
)

func encodeRangeRecord(family uint8, record rangeRecord, output []byte) (int, error) {
	if family == format.AddressFamilyIPv4 {
		if err := format.EncodeRangeRecordV4(format.RangeRecordV4{
			From: uint32(record.from.Hi), To: uint32(record.to.Hi), Value: record.value,
		}, output); err != nil {
			return 0, err
		}
		return format.RangeRecordV4Size, nil
	}
	if err := format.EncodeRangeRecordV6(format.RangeRecordV6{
		FromHi: record.from.Hi, FromLo: record.from.Lo,
		ToHi: record.to.Hi, ToLo: record.to.Lo,
		Value: record.value,
	}, output); err != nil {
		return 0, err
	}
	return format.RangeRecordV6Size, nil
}

func encodeRangeBranch(family uint8, first tree.Key, child uint32, output []byte) (int, error) {
	if family == format.AddressFamilyIPv4 {
		format.PutU32(output, uint32(first.Hi))
		format.PutU32(output[4:], child)
		return 8, nil
	}
	format.PutU128(output, first.Hi, first.Lo)
	format.PutU32(output[16:], child)
	return 20, nil
}
