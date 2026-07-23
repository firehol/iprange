package exactv4

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

const (
	bitmapLeafLower      uint16 = 4032
	bitmapBranchLower    uint16 = 1088
	bitmapSummaryOffset         = 32
	bitmapChildrenOffset        = 64
)

type bitmapKind uint32

const (
	bitmapKindFreePages      bitmapKind = 1
	bitmapKindFeedUsed       bitmapKind = 2
	bitmapKindMembershipUsed bitmapKind = 3
)

func bitmapKindFromWire(value uint32) (bitmapKind, bool) {
	switch bitmapKind(value) {
	case bitmapKindFreePages, bitmapKindFeedUsed, bitmapKindMembershipUsed:
		return bitmapKind(value), true
	default:
		return 0, false
	}
}

type bitmapPageErrorCode uint8

const (
	bitmapPageErrHeader bitmapPageErrorCode = iota + 1
	bitmapPageErrWrongPageType
	bitmapPageErrWrongKind
	bitmapPageErrFixedGeometry
	bitmapPageErrEmptyPage
	bitmapPageErrTooManyItems
	bitmapPageErrReservedNonzero
	bitmapPageErrItemCountMismatch
	bitmapPageErrChecksum
	bitmapPageErrBitOutsideLimit
	bitmapPageErrChildPageOutOfBounds
	bitmapPageErrChildOutsideLimit
)

type bitmapPageError struct {
	code      bitmapPageErrorCode
	cause     error
	pageType  PageType
	wireKind  uint32
	itemCount uint16
	childPage uint32
}

func (e *bitmapPageError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 bitmap page: error %d", e.code)
}

func (e *bitmapPageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type bitmapLeaf struct {
	page   []byte
	header PageHeader
}

func openBitmapLeaf(page []byte, selectedTxn uint64, kind bitmapKind) (bitmapLeaf, error) {
	header, err := DecodePageHeader(page, selectedTxn)
	if err != nil {
		return bitmapLeaf{}, &bitmapPageError{code: bitmapPageErrHeader, cause: err}
	}
	if header.PageType != PageTypeBitmapLeaf {
		return bitmapLeaf{}, &bitmapPageError{
			code:     bitmapPageErrWrongPageType,
			pageType: header.PageType,
		}
	}
	wireKind, valid := bitmapKindFromWire(header.Aux)
	if !valid || wireKind != kind {
		return bitmapLeaf{}, &bitmapPageError{
			code:     bitmapPageErrWrongKind,
			wireKind: header.Aux,
		}
	}
	if header.Lower != bitmapLeafLower || header.Upper != PageSize {
		return bitmapLeaf{}, &bitmapPageError{code: bitmapPageErrFixedGeometry}
	}
	if int(header.ItemCount) > BitmapLeafWords {
		return bitmapLeaf{}, &bitmapPageError{
			code:      bitmapPageErrTooManyItems,
			itemCount: header.ItemCount,
		}
	}
	return bitmapLeaf{page: page, header: header}, nil
}

func (l bitmapLeaf) word(index int) uint64 {
	if index < 0 || index >= BitmapLeafWords {
		panic("exact v4 bitmap leaf word index out of bounds")
	}
	at := bitmapSummaryOffset + index*8
	return binary.LittleEndian.Uint64(l.page[at : at+8])
}

func (l bitmapLeaf) verifyLocal(kind bitmapKind, base, limit uint64) error {
	if !VerifyPageCRC32C(l.page) {
		return &bitmapPageError{code: bitmapPageErrChecksum}
	}
	if anyNonzero(l.page[int(bitmapLeafLower):]) {
		return &bitmapPageError{code: bitmapPageErrReservedNonzero}
	}

	actualNonzero := 0
	for index := 0; index < BitmapLeafWords; index++ {
		word := l.word(index)
		if word == 0 {
			continue
		}
		actualNonzero++
		wordBase, ok := checkedAdd(base, uint64(index)*64)
		if !ok {
			return &bitmapPageError{code: bitmapPageErrBitOutsideLimit}
		}
		for word != 0 {
			bit := uint64(bits.TrailingZeros64(word))
			absolute, ok := checkedAdd(wordBase, bit)
			if !ok || absolute >= limit ||
				(kind == bitmapKindFreePages && absolute < 2) ||
				(kind == bitmapKindMembershipUsed && absolute == 0) {
				return &bitmapPageError{code: bitmapPageErrBitOutsideLimit}
			}
			word &= word - 1
		}
	}
	if actualNonzero != int(l.header.ItemCount) {
		return &bitmapPageError{code: bitmapPageErrItemCountMismatch}
	}
	return nil
}

type bitmapBranch struct {
	page   []byte
	header PageHeader
}

func openBitmapBranch(page []byte, selectedTxn uint64, kind bitmapKind) (bitmapBranch, error) {
	header, err := DecodePageHeader(page, selectedTxn)
	if err != nil {
		return bitmapBranch{}, &bitmapPageError{code: bitmapPageErrHeader, cause: err}
	}
	if header.PageType != PageTypeBitmapBranch {
		return bitmapBranch{}, &bitmapPageError{
			code:     bitmapPageErrWrongPageType,
			pageType: header.PageType,
		}
	}
	wireKind, valid := bitmapKindFromWire(header.Aux)
	if !valid || wireKind != kind {
		return bitmapBranch{}, &bitmapPageError{
			code:     bitmapPageErrWrongKind,
			wireKind: header.Aux,
		}
	}
	if header.Lower != bitmapBranchLower || header.Upper != PageSize {
		return bitmapBranch{}, &bitmapPageError{code: bitmapPageErrFixedGeometry}
	}
	if header.ItemCount == 0 {
		return bitmapBranch{}, &bitmapPageError{code: bitmapPageErrEmptyPage}
	}
	if uint64(header.ItemCount) > BitmapFanout {
		return bitmapBranch{}, &bitmapPageError{
			code:      bitmapPageErrTooManyItems,
			itemCount: header.ItemCount,
		}
	}
	return bitmapBranch{page: page, header: header}, nil
}

func (b bitmapBranch) level() uint16 { return b.header.Level }

func (b bitmapBranch) summaryWord(index int) uint64 {
	if index < 0 || index >= 4 {
		panic("exact v4 bitmap summary word index out of bounds")
	}
	at := bitmapSummaryOffset + index*8
	return binary.LittleEndian.Uint64(b.page[at : at+8])
}

func (b bitmapBranch) summaryBit(index int) bool {
	if index < 0 || uint64(index) >= BitmapFanout {
		panic("exact v4 bitmap summary bit index out of bounds")
	}
	return b.summaryWord(index/64)&(uint64(1)<<uint(index%64)) != 0
}

func (b bitmapBranch) child(index int) uint32 {
	if index < 0 || uint64(index) >= BitmapFanout {
		panic("exact v4 bitmap child index out of bounds")
	}
	at := bitmapChildrenOffset + index*4
	return binary.LittleEndian.Uint32(b.page[at : at+4])
}

func (b bitmapBranch) nextSummary(start int) (int, bool) {
	if start < 0 || uint64(start) >= BitmapFanout {
		return 0, false
	}
	wordIndex := start / 64
	word := b.summaryWord(wordIndex) & (^uint64(0) << uint(start%64))
	for {
		if word != 0 {
			return wordIndex*64 + bits.TrailingZeros64(word), true
		}
		wordIndex++
		if wordIndex == 4 {
			return 0, false
		}
		word = b.summaryWord(wordIndex)
	}
}

func (b bitmapBranch) verifyLocal(base, childSpan, limit, pageCount uint64) error {
	if !VerifyPageCRC32C(b.page) {
		return &bitmapPageError{code: bitmapPageErrChecksum}
	}
	if anyNonzero(b.page[int(bitmapBranchLower):]) {
		return &bitmapPageError{code: bitmapPageErrReservedNonzero}
	}

	actualNonzero := 0
	for index := 0; uint64(index) < BitmapFanout; index++ {
		child := b.child(index)
		if child != 0 {
			actualNonzero++
			if child < 2 || uint64(child) >= pageCount {
				return &bitmapPageError{
					code:      bitmapPageErrChildPageOutOfBounds,
					childPage: child,
				}
			}
		}
		offset, ok := checkedMul(childSpan, uint64(index))
		if !ok {
			return &bitmapPageError{code: bitmapPageErrChildOutsideLimit}
		}
		childBase, ok := checkedAdd(base, offset)
		if !ok {
			return &bitmapPageError{code: bitmapPageErrChildOutsideLimit}
		}
		if childBase >= limit && (child != 0 || b.summaryBit(index)) {
			return &bitmapPageError{code: bitmapPageErrChildOutsideLimit}
		}
	}
	if actualNonzero != int(b.header.ItemCount) {
		return &bitmapPageError{code: bitmapPageErrItemCountMismatch}
	}
	return nil
}
