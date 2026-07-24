package exactv4

import (
	"encoding/binary"
	"fmt"
)

type rangePageErrorCode uint8

const (
	rangePageErrWrongKeyFamily rangePageErrorCode = iota + 1
	rangePageErrWrongPageType
	rangePageErrWrongAux
	rangePageErrFixedGeometry
	rangePageErrEmptyBranch
	rangePageErrIndexOutOfBounds
	rangePageErrChildOutOfBounds
	rangePageErrReservedNonzero
	rangePageErrEmptySummaryNonzero
	rangePageErrSummaryOrder
	rangePageErrRangeReversed
	rangePageErrMembershipValueZero
)

type rangePageError struct {
	code rangePageErrorCode
}

func (e *rangePageError) Error() string {
	return fmt.Sprintf("exact v4 range page: error %d", e.code)
}

// rangePageWriteError describes input rejected before an encoder changes the
// destination COW page. It is distinct from rangePageError, which describes
// bytes that were already read from a file.
type rangePageWriteErrorCode uint8

const (
	rangePageWriteErrBornTransactionZero rangePageWriteErrorCode = iota + 1
	rangePageWriteErrTooManyRecords
	rangePageWriteErrRangeReversed
	rangePageWriteErrMembershipValueZero
	rangePageWriteErrRangeOverlap
	rangePageWriteErrAdjacentEqualValue
	rangePageWriteErrBranchLevel
	rangePageWriteErrPageCount
	rangePageWriteErrEmptyBranch
	rangePageWriteErrTooManyChildren
	rangePageWriteErrFirstFence
	rangePageWriteErrFenceBounds
	rangePageWriteErrFenceOrder
	rangePageWriteErrChildOutOfBounds
	rangePageWriteErrDuplicateChild
	rangePageWriteErrEmptySummaryNonzero
	rangePageWriteErrSummaryOrder
	rangePageWriteErrSummaryBeforeFence
	rangePageWriteErrSummaryOutsideFence
	rangePageWriteErrSummaryOverlap
)

type rangePageWriteError struct {
	code     rangePageWriteErrorCode
	required int
	actual   int
	level    uint16
	pages    uint64
	child    uint32
}

func (e *rangePageWriteError) Error() string {
	return fmt.Sprintf("exact v4 range page write: error %d", e.code)
}

type rangeRecord[K rangeKey[K]] struct {
	from  K
	to    K
	value uint32
}

type rangeBranchEntry[K rangeKey[K]] struct {
	lowerFence         K
	childPage          uint32
	subtreeRecordCount uint64
	firstFrom          K
	lastFrom           K
	lastTo             K
}

func (e rangeBranchEntry[K]) empty() bool { return e.subtreeRecordCount == 0 }

func rangeLeafCapacity[K rangeKey[K]]() int {
	return (PageSize - int(PageHeaderSize)) / rangeRecordSize[K]()
}

func rangeBranchCapacity[K rangeKey[K]]() int {
	return (PageSize - int(PageHeaderSize)) / rangeBranchEntrySize[K]()
}

// encodeRangeLeaf validates the complete canonical record slice before it
// changes page, then writes one exact CRC-sealed leaf.
func encodeRangeLeaf[K rangeKey[K]](
	page []byte,
	bornTxn uint64,
	valueKind ValueKind,
	records []rangeRecord[K],
) error {
	if len(page) != PageSize {
		return &PageHeaderError{Code: PageHeaderErrPageSize, Length: len(page)}
	}
	if bornTxn == 0 {
		return &rangePageWriteError{code: rangePageWriteErrBornTransactionZero}
	}
	if len(records) > rangeLeafCapacity[K]() {
		return &rangePageWriteError{
			code:     rangePageWriteErrTooManyRecords,
			required: len(records),
			actual:   rangeLeafCapacity[K](),
		}
	}
	for index, record := range records {
		if record.from.compare(record.to) > 0 {
			return &rangePageWriteError{code: rangePageWriteErrRangeReversed}
		}
		if valueKind == ValueKindMembership && record.value == 0 {
			return &rangePageWriteError{code: rangePageWriteErrMembershipValueZero}
		}
		if index == 0 {
			continue
		}
		previous := records[index-1]
		if previous.to.compare(record.from) >= 0 {
			return &rangePageWriteError{code: rangePageWriteErrRangeOverlap}
		}
		if next, ok := previous.to.next(); ok && next.compare(record.from) == 0 && previous.value == record.value {
			return &rangePageWriteError{code: rangePageWriteErrAdjacentEqualValue}
		}
	}

	clear(page)
	var key K
	lower := int(PageHeaderSize) + len(records)*rangeRecordSize[K]()
	if err := (PageHeader{
		PageType:  PageTypeRangeLeaf,
		BornTxn:   bornTxn,
		ItemCount: uint16(len(records)),
		Level:     0,
		Lower:     uint16(lower),
		Upper:     PageSize,
		Aux:       uint32(key.family()),
	}).EncodeInto(page); err != nil {
		return err
	}
	width := key.width()
	for index, record := range records {
		at := int(PageHeaderSize) + index*rangeRecordSize[K]()
		record.from.writeLE(page[at : at+width])
		record.to.writeLE(page[at+width : at+2*width])
		binary.LittleEndian.PutUint32(page[at+2*width:at+2*width+4], record.value)
	}
	_, err := WritePageCRC32C(page)
	return err
}

// encodeRangeBranch validates the complete child-summary sequence before it
// changes page, then writes one exact CRC-sealed branch. lowerFence is the
// exact lower bound inherited from the parent. upperFence is the exclusive
// upper bound inherited from the parent; hasUpperFence false represents the
// unrepresentable endpoint just past the family maximum.
func encodeRangeBranch[K rangeKey[K]](
	page []byte,
	bornTxn uint64,
	level uint16,
	pageCount uint64,
	lowerFence K,
	upperFence K,
	hasUpperFence bool,
	entries []rangeBranchEntry[K],
) error {
	if len(page) != PageSize {
		return &PageHeaderError{Code: PageHeaderErrPageSize, Length: len(page)}
	}
	if bornTxn == 0 {
		return &rangePageWriteError{code: rangePageWriteErrBornTransactionZero}
	}
	if level == 0 || level > MaxTreeLevel {
		return &rangePageWriteError{code: rangePageWriteErrBranchLevel, level: level}
	}
	if pageCount < 3 || pageCount > MaxPageCount {
		return &rangePageWriteError{code: rangePageWriteErrPageCount, pages: pageCount}
	}
	if len(entries) == 0 {
		return &rangePageWriteError{code: rangePageWriteErrEmptyBranch}
	}
	if len(entries) > rangeBranchCapacity[K]() {
		return &rangePageWriteError{
			code:     rangePageWriteErrTooManyChildren,
			required: len(entries),
			actual:   rangeBranchCapacity[K](),
		}
	}
	if entries[0].lowerFence.compare(lowerFence) != 0 {
		return &rangePageWriteError{code: rangePageWriteErrFirstFence}
	}
	if hasUpperFence && lowerFence.compare(upperFence) >= 0 {
		return &rangePageWriteError{code: rangePageWriteErrFenceBounds}
	}

	var previous rangeBranchEntry[K]
	havePrevious := false
	for index, entry := range entries {
		if hasUpperFence && entry.lowerFence.compare(upperFence) >= 0 {
			return &rangePageWriteError{code: rangePageWriteErrFenceBounds}
		}
		if entry.childPage < 2 || uint64(entry.childPage) >= pageCount {
			return &rangePageWriteError{code: rangePageWriteErrChildOutOfBounds, child: entry.childPage}
		}
		for _, prior := range entries[:index] {
			if prior.childPage == entry.childPage {
				return &rangePageWriteError{code: rangePageWriteErrDuplicateChild, child: entry.childPage}
			}
		}
		if index != 0 && entries[index-1].lowerFence.compare(entry.lowerFence) >= 0 {
			return &rangePageWriteError{code: rangePageWriteErrFenceOrder}
		}
		if entry.empty() {
			if entry.firstFrom.compare(entry.firstFrom.minimum()) != 0 ||
				entry.lastFrom.compare(entry.lastFrom.minimum()) != 0 ||
				entry.lastTo.compare(entry.lastTo.minimum()) != 0 {
				return &rangePageWriteError{code: rangePageWriteErrEmptySummaryNonzero}
			}
			continue
		}
		if entry.firstFrom.compare(entry.lowerFence) < 0 {
			return &rangePageWriteError{code: rangePageWriteErrSummaryBeforeFence}
		}
		if entry.firstFrom.compare(entry.lastFrom) > 0 || entry.lastFrom.compare(entry.lastTo) > 0 {
			return &rangePageWriteError{code: rangePageWriteErrSummaryOrder}
		}
		nextFence, hasNextFence := upperFence, hasUpperFence
		if index+1 < len(entries) {
			nextFence, hasNextFence = entries[index+1].lowerFence, true
		}
		if hasNextFence && entry.lastFrom.compare(nextFence) >= 0 {
			return &rangePageWriteError{code: rangePageWriteErrSummaryOutsideFence}
		}
		if havePrevious && previous.lastTo.compare(entry.firstFrom) >= 0 {
			return &rangePageWriteError{code: rangePageWriteErrSummaryOverlap}
		}
		previous = entry
		havePrevious = true
	}

	clear(page)
	var key K
	lower := int(PageHeaderSize) + len(entries)*rangeBranchEntrySize[K]()
	if err := (PageHeader{
		PageType:  PageTypeRangeBranch,
		BornTxn:   bornTxn,
		ItemCount: uint16(len(entries)),
		Level:     level,
		Lower:     uint16(lower),
		Upper:     PageSize,
		Aux:       uint32(key.family()),
	}).EncodeInto(page); err != nil {
		return err
	}
	width := key.width()
	for index, entry := range entries {
		at := int(PageHeaderSize) + index*rangeBranchEntrySize[K]()
		entry.lowerFence.writeLE(page[at : at+width])
		if width == 4 {
			binary.LittleEndian.PutUint32(page[at+4:at+8], entry.childPage)
			binary.LittleEndian.PutUint64(page[at+8:at+16], entry.subtreeRecordCount)
			entry.firstFrom.writeLE(page[at+16 : at+20])
			entry.lastFrom.writeLE(page[at+20 : at+24])
			entry.lastTo.writeLE(page[at+24 : at+28])
		} else {
			binary.LittleEndian.PutUint32(page[at+16:at+20], entry.childPage)
			binary.LittleEndian.PutUint64(page[at+24:at+32], entry.subtreeRecordCount)
			entry.firstFrom.writeLE(page[at+32 : at+48])
			entry.lastFrom.writeLE(page[at+48 : at+64])
			entry.lastTo.writeLE(page[at+64 : at+80])
		}
	}
	_, err := WritePageCRC32C(page)
	return err
}

type rangeLeaf[K rangeKey[K]] struct {
	page      []byte
	count     int
	valueKind ValueKind
}

type rangeLeafInfo struct {
	count     int
	valueKind ValueKind
}

func openRangeLeaf[K rangeKey[K]](
	page []byte,
	selectedTxn uint64,
	family AddressFamily,
	valueKind ValueKind,
) (rangeLeaf[K], error) {
	info, err := inspectRangeLeaf[K](page, selectedTxn, family, valueKind)
	if err != nil {
		return rangeLeaf[K]{}, err
	}
	return rangeLeaf[K]{page: page, count: info.count, valueKind: info.valueKind}, nil
}

func inspectRangeLeaf[K rangeKey[K]](
	page []byte,
	selectedTxn uint64,
	family AddressFamily,
	valueKind ValueKind,
) (rangeLeafInfo, error) {
	var key K
	if key.family() != family {
		return rangeLeafInfo{}, &rangePageError{code: rangePageErrWrongKeyFamily}
	}
	header, err := DecodePageHeader(page, selectedTxn)
	if err != nil {
		return rangeLeafInfo{}, err
	}
	if header.PageType != PageTypeRangeLeaf {
		return rangeLeafInfo{}, &rangePageError{code: rangePageErrWrongPageType}
	}
	if header.Aux != uint32(family) {
		return rangeLeafInfo{}, &rangePageError{code: rangePageErrWrongAux}
	}
	count := int(header.ItemCount)
	lower := int(PageHeaderSize) + count*rangeRecordSize[K]()
	if lower != int(header.Lower) || header.Upper != PageSize {
		return rangeLeafInfo{}, &rangePageError{code: rangePageErrFixedGeometry}
	}
	return rangeLeafInfo{count: count, valueKind: valueKind}, nil
}

func (l rangeLeaf[K]) record(index int) (rangeRecord[K], error) {
	return decodeRangeRecord[K](l.page, rangeLeafInfo{count: l.count, valueKind: l.valueKind}, index)
}

func decodeRangeRecord[K rangeKey[K]](
	page []byte,
	info rangeLeafInfo,
	index int,
) (rangeRecord[K], error) {
	if index < 0 || index >= info.count {
		return rangeRecord[K]{}, &rangePageError{code: rangePageErrIndexOutOfBounds}
	}
	var key K
	width := key.width()
	at := int(PageHeaderSize) + index*rangeRecordSize[K]()
	from := readImmediateRangeKey(key, page, at)
	to := readImmediateRangeKey(key, page, at+width)
	value := binary.LittleEndian.Uint32(page[at+2*width : at+2*width+4])
	if from.compare(to) > 0 {
		return rangeRecord[K]{}, &rangePageError{code: rangePageErrRangeReversed}
	}
	if info.valueKind == ValueKindMembership && value == 0 {
		return rangeRecord[K]{}, &rangePageError{code: rangePageErrMembershipValueZero}
	}
	return rangeRecord[K]{from: from, to: to, value: value}, nil
}

type rangeBranch[K rangeKey[K]] struct {
	page      []byte
	count     int
	level     uint16
	pageCount uint64
}

type rangeBranchInfo struct {
	count     int
	level     uint16
	pageCount uint64
}

func openRangeBranch[K rangeKey[K]](
	page []byte,
	selectedTxn uint64,
	family AddressFamily,
	pageCount uint64,
) (rangeBranch[K], error) {
	info, err := inspectRangeBranch[K](page, selectedTxn, family, pageCount)
	if err != nil {
		return rangeBranch[K]{}, err
	}
	return rangeBranch[K]{page: page, count: info.count, level: info.level, pageCount: info.pageCount}, nil
}

func inspectRangeBranch[K rangeKey[K]](
	page []byte,
	selectedTxn uint64,
	family AddressFamily,
	pageCount uint64,
) (rangeBranchInfo, error) {
	var key K
	if key.family() != family {
		return rangeBranchInfo{}, &rangePageError{code: rangePageErrWrongKeyFamily}
	}
	header, err := DecodePageHeader(page, selectedTxn)
	if err != nil {
		return rangeBranchInfo{}, err
	}
	if header.PageType != PageTypeRangeBranch {
		return rangeBranchInfo{}, &rangePageError{code: rangePageErrWrongPageType}
	}
	if header.Aux != uint32(family) {
		return rangeBranchInfo{}, &rangePageError{code: rangePageErrWrongAux}
	}
	count := int(header.ItemCount)
	if count == 0 {
		return rangeBranchInfo{}, &rangePageError{code: rangePageErrEmptyBranch}
	}
	lower := int(PageHeaderSize) + count*rangeBranchEntrySize[K]()
	if lower != int(header.Lower) || header.Upper != PageSize {
		return rangeBranchInfo{}, &rangePageError{code: rangePageErrFixedGeometry}
	}
	return rangeBranchInfo{count: count, level: header.Level, pageCount: pageCount}, nil
}

func (b rangeBranch[K]) entry(index int) (rangeBranchEntry[K], error) {
	return decodeRangeBranchEntry[K](b.page, rangeBranchInfo{count: b.count, level: b.level, pageCount: b.pageCount}, index)
}

func decodeRangeBranchEntry[K rangeKey[K]](
	page []byte,
	info rangeBranchInfo,
	index int,
) (rangeBranchEntry[K], error) {
	if index < 0 || index >= info.count {
		return rangeBranchEntry[K]{}, &rangePageError{code: rangePageErrIndexOutOfBounds}
	}
	var key K
	width := key.width()
	at := int(PageHeaderSize) + index*rangeBranchEntrySize[K]()
	var entry rangeBranchEntry[K]
	if width == 4 {
		if binary.LittleEndian.Uint32(page[at+28:at+32]) != 0 {
			return entry, &rangePageError{code: rangePageErrReservedNonzero}
		}
		entry = rangeBranchEntry[K]{
			lowerFence:         readImmediateRangeKey(key, page, at),
			childPage:          binary.LittleEndian.Uint32(page[at+4 : at+8]),
			subtreeRecordCount: binary.LittleEndian.Uint64(page[at+8 : at+16]),
			firstFrom:          readImmediateRangeKey(key, page, at+16),
			lastFrom:           readImmediateRangeKey(key, page, at+20),
			lastTo:             readImmediateRangeKey(key, page, at+24),
		}
	} else {
		if binary.LittleEndian.Uint32(page[at+20:at+24]) != 0 {
			return entry, &rangePageError{code: rangePageErrReservedNonzero}
		}
		entry = rangeBranchEntry[K]{
			lowerFence:         readImmediateRangeKey(key, page, at),
			childPage:          binary.LittleEndian.Uint32(page[at+16 : at+20]),
			subtreeRecordCount: binary.LittleEndian.Uint64(page[at+24 : at+32]),
			firstFrom:          readImmediateRangeKey(key, page, at+32),
			lastFrom:           readImmediateRangeKey(key, page, at+48),
			lastTo:             readImmediateRangeKey(key, page, at+64),
		}
	}
	if entry.childPage < 2 || uint64(entry.childPage) >= info.pageCount {
		return entry, &rangePageError{code: rangePageErrChildOutOfBounds}
	}
	minimum := key.minimum()
	if entry.empty() {
		if entry.firstFrom != minimum || entry.lastFrom != minimum || entry.lastTo != minimum {
			return entry, &rangePageError{code: rangePageErrEmptySummaryNonzero}
		}
	} else if entry.firstFrom.compare(entry.lastFrom) > 0 || entry.lastFrom.compare(entry.lastTo) > 0 {
		return entry, &rangePageError{code: rangePageErrSummaryOrder}
	}
	return entry, nil
}

func (b rangeBranch[K]) nextNonempty(from int) (int, bool, error) {
	if from < 0 {
		from = 0
	}
	for index := from; index < b.count; index++ {
		entry, err := b.entry(index)
		if err != nil {
			return 0, false, err
		}
		if !entry.empty() {
			return index, true, nil
		}
	}
	return 0, false, nil
}

func (b rangeBranch[K]) previousNonempty(before int) (int, bool, error) {
	if before > b.count {
		before = b.count
	}
	for index := before - 1; index >= 0; index-- {
		entry, err := b.entry(index)
		if err != nil {
			return 0, false, err
		}
		if !entry.empty() {
			return index, true, nil
		}
	}
	return 0, false, nil
}

func (b rangeBranch[K]) predecessorFor(target K) (int, bool, error) {
	found := false
	result := 0
	for index := 0; index < b.count; index++ {
		entry, err := b.entry(index)
		if err != nil {
			return 0, false, err
		}
		if !entry.empty() && entry.firstFrom.compare(target) <= 0 {
			result, found = index, true
		}
	}
	return result, found, nil
}

func rangeRecordSize[K rangeKey[K]]() int {
	var key K
	return 2*key.width() + 4
}

func readImmediateRangeKey[K rangeKey[K]](key K, page []byte, at int) K {
	if key.width() == 4 {
		return key.fromHalves(0, uint64(binary.LittleEndian.Uint32(page[at:at+4])))
	}
	return key.fromHalves(
		binary.LittleEndian.Uint64(page[at+8:at+16]),
		binary.LittleEndian.Uint64(page[at:at+8]),
	)
}

func rangeBranchEntrySize[K rangeKey[K]]() int {
	var key K
	if key.width() == 4 {
		return 32
	}
	return 80
}
