package exactv4

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const PageCRCOffset = 28

var pageCRCZero [4]byte

// IsBranch reports whether this exact page type requires a positive level.
func (p PageType) IsBranch() bool {
	switch p {
	case PageTypeRangeBranch,
		PageTypeCatalogNameBranch,
		PageTypeCatalogIndexBranch,
		PageTypeMembershipIDBranch,
		PageTypeMembershipHashBranch,
		PageTypeBlobBranch,
		PageTypeBitmapBranch,
		PageTypeRetirementBranch:
		return true
	default:
		return false
	}
}

func validPageType(value PageType) bool {
	return value >= PageTypeRangeBranch && value <= PageTypeRetirementLeaf
}

// PageHeader is the exact common 32-byte non-meta page header.
type PageHeader struct {
	PageType   PageType
	BornTxn    uint64
	ItemCount  uint16
	Level      uint16
	Lower      uint16
	Upper      uint16
	Aux        uint32
	PageCRC32C uint32
}

// PageHeaderErrorCode classifies ordinary-path structural failures.
type PageHeaderErrorCode uint8

const (
	PageHeaderErrPageSize PageHeaderErrorCode = iota + 1
	PageHeaderErrMagic
	PageHeaderErrPageType
	PageHeaderErrFlags
	PageHeaderErrHeaderSize
	PageHeaderErrBornTransactionZero
	PageHeaderErrBornTransactionFuture
	PageHeaderErrLevelTooHigh
	PageHeaderErrBranchLevelZero
	PageHeaderErrNonBranchLevelNonzero
	PageHeaderErrBounds
)

// PageHeaderError retains the exact offending field values.
type PageHeaderError struct {
	Code        PageHeaderErrorCode
	Length      int
	WireType    uint8
	Flags       uint8
	HeaderSize  uint16
	BornTxn     uint64
	SelectedTxn uint64
	PageType    PageType
	Level       uint16
	Lower       uint16
	Upper       uint16
}

func (e *PageHeaderError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 page header: error %d", e.Code)
}

// DecodePageHeader checks the ordinary-path common structural invariants.
// It deliberately does not verify CRC-32C; validation and recovery call
// VerifyPageCRC32C explicitly.
func DecodePageHeader(page []byte, selectedTxn uint64) (PageHeader, error) {
	if len(page) != PageSize {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrPageSize, Length: len(page)}
	}
	if page[0] != PageMagic[0] || page[1] != PageMagic[1] || page[2] != PageMagic[2] || page[3] != PageMagic[3] {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrMagic}
	}
	pageType := PageType(page[4])
	if !validPageType(pageType) {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrPageType, WireType: page[4]}
	}
	if page[5] != 0 {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrFlags, Flags: page[5]}
	}
	headerSize := binary.LittleEndian.Uint16(page[6:8])
	if headerSize != PageHeaderSize {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrHeaderSize, HeaderSize: headerSize}
	}

	bornTxn := binary.LittleEndian.Uint64(page[8:16])
	if bornTxn == 0 {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrBornTransactionZero}
	}
	if bornTxn > selectedTxn {
		return PageHeader{}, &PageHeaderError{
			Code:        PageHeaderErrBornTransactionFuture,
			BornTxn:     bornTxn,
			SelectedTxn: selectedTxn,
		}
	}

	level := binary.LittleEndian.Uint16(page[18:20])
	if level > MaxTreeLevel {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrLevelTooHigh, Level: level}
	}
	if pageType.IsBranch() {
		if level == 0 {
			return PageHeader{}, &PageHeaderError{Code: PageHeaderErrBranchLevelZero, PageType: pageType}
		}
	} else if level != 0 {
		return PageHeader{}, &PageHeaderError{
			Code:     PageHeaderErrNonBranchLevelNonzero,
			PageType: pageType,
			Level:    level,
		}
	}

	lower := binary.LittleEndian.Uint16(page[20:22])
	upper := binary.LittleEndian.Uint16(page[22:24])
	if lower < PageHeaderSize || lower > upper || uint64(upper) > PageSize {
		return PageHeader{}, &PageHeaderError{Code: PageHeaderErrBounds, Lower: lower, Upper: upper}
	}

	return PageHeader{
		PageType:   pageType,
		BornTxn:    bornTxn,
		ItemCount:  binary.LittleEndian.Uint16(page[16:18]),
		Level:      level,
		Lower:      lower,
		Upper:      upper,
		Aux:        binary.LittleEndian.Uint32(page[24:28]),
		PageCRC32C: binary.LittleEndian.Uint32(page[PageCRCOffset : PageCRCOffset+4]),
	}, nil
}

// EncodeInto writes only the exact common header. Type-specific body bytes are
// owned by the caller and the completed page is sealed with WritePageCRC32C.
func (h PageHeader) EncodeInto(page []byte) error {
	if len(page) != PageSize {
		return &PageHeaderError{Code: PageHeaderErrPageSize, Length: len(page)}
	}
	copy(page[0:4], PageMagic)
	page[4] = byte(h.PageType)
	page[5] = 0
	binary.LittleEndian.PutUint16(page[6:8], PageHeaderSize)
	binary.LittleEndian.PutUint64(page[8:16], h.BornTxn)
	binary.LittleEndian.PutUint16(page[16:18], h.ItemCount)
	binary.LittleEndian.PutUint16(page[18:20], h.Level)
	binary.LittleEndian.PutUint16(page[20:22], h.Lower)
	binary.LittleEndian.PutUint16(page[22:24], h.Upper)
	binary.LittleEndian.PutUint32(page[24:28], h.Aux)
	binary.LittleEndian.PutUint32(page[PageCRCOffset:PageCRCOffset+4], h.PageCRC32C)
	return nil
}

// VerifyPageCRC32C explicitly verifies a complete page with its CRC field zeroed.
func VerifyPageCRC32C(page []byte) bool {
	if len(page) != PageSize {
		return false
	}
	stored := binary.LittleEndian.Uint32(page[PageCRCOffset : PageCRCOffset+4])
	return pageCRC32C(page) == stored
}

// WritePageCRC32C seals a complete page and returns the stored checksum.
func WritePageCRC32C(page []byte) (uint32, error) {
	if len(page) != PageSize {
		return 0, &PageHeaderError{Code: PageHeaderErrPageSize, Length: len(page)}
	}
	checksum := pageCRC32C(page)
	binary.LittleEndian.PutUint32(page[PageCRCOffset:PageCRCOffset+4], checksum)
	return checksum, nil
}

func pageCRC32C(page []byte) uint32 {
	crc := crc32.Update(0, castagnoliTable, page[:PageCRCOffset])
	crc = crc32.Update(crc, castagnoliTable, pageCRCZero[:])
	return crc32.Update(crc, castagnoliTable, page[PageCRCOffset+4:])
}
