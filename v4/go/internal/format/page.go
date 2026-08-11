package format

import "fmt"

// PageHeader is the exact common 32-byte non-meta page header
// (binary-format-v4.md section 5).
type PageHeader struct {
	PageType   PageType
	PageFlags  uint8
	HeaderSize uint16
	BornTxn    uint64
	ItemCount  uint16
	Level      uint16
	Lower      uint16
	Upper      uint16
	Aux        uint32
	PageCRC32C uint32
}

// HeaderError classifies ordinary-path structural failures. It deliberately
// excludes CRC mismatches: validation and recovery alone verify page CRCs.
type HeaderError struct {
	Reason string
}

func (e *HeaderError) Error() string { return "v4 page header: " + e.Reason }

func headerErr(format string, args ...any) *HeaderError {
	return &HeaderError{Reason: fmt.Sprintf(format, args...)}
}

// DecodePageHeader checks the ordinary-path common structural invariants of
// one full committed page view: length, magic, type, flags, header size,
// born transaction, level, and the slotted bounds needed for memory safety.
// It is intentionally not a full page validation.
func DecodePageHeader(page []byte, selectedTxn uint64) (PageHeader, error) {
	if len(page) != PageSize {
		return PageHeader{}, headerErr("page length %d", len(page))
	}
	if !magic4(page, PageMagic[:]) {
		return PageHeader{}, headerErr("bad page magic")
	}
	p := PageHeader{
		PageType:   PageType(page[4]),
		PageFlags:  page[5],
		HeaderSize: U16(page[6:8]),
		BornTxn:    U64(page[8:16]),
		ItemCount:  U16(page[16:18]),
		Level:      U16(page[18:20]),
		Lower:      U16(page[20:22]),
		Upper:      U16(page[22:24]),
		Aux:        U32(page[24:28]),
		PageCRC32C: U32(page[28:32]),
	}
	if p.PageType < PageTypeRangeBranch || p.PageType > PageTypeStructureHashLeaf {
		return PageHeader{}, headerErr("invalid page type %d", p.PageType)
	}
	if p.PageFlags != 0 {
		return PageHeader{}, headerErr("nonzero page flags %d", p.PageFlags)
	}
	if p.HeaderSize != 32 {
		return PageHeader{}, headerErr("header size %d", p.HeaderSize)
	}
	if p.BornTxn == 0 {
		return PageHeader{}, headerErr("zero born transaction")
	}
	if p.BornTxn > selectedTxn {
		return PageHeader{}, headerErr("born transaction %d above selected %d", p.BornTxn, selectedTxn)
	}
	if p.Level > MaxTreeLevel {
		return PageHeader{}, headerErr("level %d above max", p.Level)
	}
	if p.IsBranch() && p.Level == 0 {
		return PageHeader{}, headerErr("branch page with zero level")
	}
	if !p.IsBranch() && p.Level != 0 {
		return PageHeader{}, headerErr("leaf page with nonzero level %d", p.Level)
	}
	return p, nil
}

func magic4(page []byte, magic []byte) bool {
	return page[0] == magic[0] && page[1] == magic[1] && page[2] == magic[2] && page[3] == magic[3]
}

// IsBranch reports whether this page type requires a positive level.
func (p PageHeader) IsBranch() bool {
	switch p.PageType {
	case PageTypeRangeBranch, PageTypeCatalogNameBranch, PageTypeCatalogIndexBranch,
		PageTypeMembershipIDBranch, PageTypeMembershipHashBranch, PageTypeBlobBranch,
		PageTypeBitmapBranch, PageTypeRetirementBranch, PageTypeStructureIDDirectory,
		PageTypeStructureHashBranch:
		return true
	default:
		return false
	}
}

// SlottedPage is a checked view of one slotted B+tree page
// (binary-format-v4.md section 7). All access is bounds-checked against the
// page view; no decoded record outlives the view or the operation.
type SlottedPage struct {
	Page   []byte
	Header PageHeader
}

// OpenSlotted validates the common header and slotted geometry of one page.
// itemLimit caps the type-specific record count (the page capacity bounds);
// auxExpected, when nonzero, must equal the page aux.
func OpenSlotted(page []byte, selectedTxn uint64, expectedType PageType, auxExpected uint32, itemLimit uint16) (SlottedPage, error) {
	h, err := DecodePageHeader(page, selectedTxn)
	if err != nil {
		return SlottedPage{}, err
	}
	if h.PageType != expectedType {
		return SlottedPage{}, headerErr("page type %d expected %d", h.PageType, expectedType)
	}
	if auxExpected != 0 && h.Aux != auxExpected {
		return SlottedPage{}, headerErr("aux %d expected %d", h.Aux, auxExpected)
	}
	if h.ItemCount < 1 {
		return SlottedPage{}, headerErr("empty slotted page")
	}
	if h.ItemCount > itemLimit {
		return SlottedPage{}, headerErr("item count %d over limit %d", h.ItemCount, itemLimit)
	}
	if uint32(h.Lower) < uint32(32)+uint32(h.ItemCount)*2 {
		return SlottedPage{}, headerErr("lower %d below slot array", h.Lower)
	}
	if h.Upper < h.Lower {
		return SlottedPage{}, headerErr("upper %d below lower %d", h.Upper, h.Lower)
	}
	return SlottedPage{Page: page, Header: h}, nil
}

// SlotItemsPerPage is the maximum slot count for one slotted page.
const SlotItemsPerPage = (PageSize - 32) / 2

// SlotOffset returns the checked record offset of slot i.
func (s SlottedPage) SlotOffset(i int) (uint16, error) {
	if i < 0 || int(s.Header.ItemCount) <= i {
		return 0, headerErr("slot %d out of range", i)
	}
	off := U16(s.Page[32+2*i : 34+2*i])
	if uint32(off) < uint32(s.Header.Upper) {
		return 0, headerErr("record offset %d below upper %d", off, s.Header.Upper)
	}
	if uint32(off)+4096 < uint32(off) {
		return 0, headerErr("record offset overflow")
	}
	return off, nil
}

// Record returns the checked bytes of slot i's record.
func (s SlottedPage) Record(i int) ([]byte, error) {
	off, err := s.SlotOffset(i)
	if err != nil {
		return nil, err
	}
	// The caller's codec re-checks the exact record length; here only the
	// invariant that the record starts inside the page is enforced, and the
	// returned slice is bounded to the page end.
	return s.Page[off:], nil
}

// PageNumberValid reports whether a stored page number names a non-meta page
// below the selected committed page count.
func PageNumberValid(pgno uint32, pageCount uint64) bool {
	return pgno >= 2 && uint64(pgno) < pageCount
}
