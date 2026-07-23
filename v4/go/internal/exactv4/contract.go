// Package exactv4 implements the exact unsigned Phase-1 v4 wire foundation.
// It deliberately contains no compatibility path for earlier experimental files.
package exactv4

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

const (
	PageSize                       = 4096
	PageShift               uint8  = 12
	MetaSize                uint16 = 256
	MaxPageCount            uint64 = 1 << 32
	MaxTreeLevel            uint16 = 31
	MaxMetadataUncompressed uint64 = 1_048_576
	BitmapLeafWords                = 500
	BitmapLeafBits          uint64 = 32_000
	BitmapFanout            uint64 = 256
	PageHeaderSize          uint16 = 32
	MetaCRCOffset                  = 252

	MetaMagic = "IPRANGE4"
	PageMagic = "IP4P"
)

// PageType is the exact non-meta page discriminator.
type PageType uint8

const (
	PageTypeRangeBranch          PageType = 1
	PageTypeRangeLeaf            PageType = 2
	PageTypeCatalogNameBranch    PageType = 3
	PageTypeCatalogNameLeaf      PageType = 4
	PageTypeCatalogIndexBranch   PageType = 5
	PageTypeCatalogIndexLeaf     PageType = 6
	PageTypeMembershipIDBranch   PageType = 7
	PageTypeMembershipIDLeaf     PageType = 8
	PageTypeMembershipHashBranch PageType = 9
	PageTypeMembershipHashLeaf   PageType = 10
	PageTypeBlobBranch           PageType = 11
	PageTypeBlobLeaf             PageType = 12
	PageTypeMetadataChunk        PageType = 13
	PageTypeBitmapBranch         PageType = 14
	PageTypeBitmapLeaf           PageType = 15
	PageTypeRetirementBranch     PageType = 16
	PageTypeRetirementLeaf       PageType = 17
)

// AddressFamily is the numeric address-family field stored in each meta.
type AddressFamily uint8

const (
	AddressFamilyIPv4 AddressFamily = 4
	AddressFamilyIPv6 AddressFamily = 6
)

func validAddressFamily(value AddressFamily) bool {
	return value == AddressFamilyIPv4 || value == AddressFamilyIPv6
}

// ValueKind selects the meaning of the fixed u32 range value.
type ValueKind uint8

const (
	ValueKindDirect     ValueKind = 1
	ValueKindMembership ValueKind = 2
)

func validValueKind(value ValueKind) bool {
	return value == ValueKindDirect || value == ValueKindMembership
}

var ErrInvalidValueTag = errors.New("exact v4 value tag is not canonical")

// ValueTag is a canonical 15-byte maximum value followed by a mandatory NUL.
// The wire bytes are private so invalid tags cannot be constructed directly.
type ValueTag struct {
	wire [16]byte
}

// NewValueTag constructs a canonical tag from non-NUL caller bytes.
func NewValueTag(value []byte) (ValueTag, error) {
	if len(value) > 15 {
		return ValueTag{}, ErrInvalidValueTag
	}
	for _, b := range value {
		if b == 0 {
			return ValueTag{}, ErrInvalidValueTag
		}
	}
	var tag ValueTag
	copy(tag.wire[:], value)
	return tag, nil
}

// RetentionTag returns the exact predefined retention tag.
func RetentionTag() ValueTag {
	return ValueTag{wire: [16]byte{'r', 'e', 't', 'e', 'n', 't', 'i', 'o', 'n'}}
}

func valueTagFromWire(wire [16]byte) (ValueTag, bool) {
	nul := -1
	for i, b := range wire {
		if b == 0 {
			nul = i
			break
		}
	}
	if nul < 0 {
		return ValueTag{}, false
	}
	for _, b := range wire[nul:] {
		if b != 0 {
			return ValueTag{}, false
		}
	}
	return ValueTag{wire: wire}, true
}

// Wire returns the exact 16-byte on-disk representation.
func (t ValueTag) Wire() [16]byte { return t.wire }

// Bytes returns the tag content before its mandatory NUL.
func (t ValueTag) Bytes() []byte {
	for i, b := range t.wire {
		if b == 0 {
			return t.wire[:i]
		}
	}
	panic("invalid ValueTag invariant")
}

// Meta is the exact logical content of bytes [0,256) in a v4 meta page.
type Meta struct {
	AddressFamily           AddressFamily
	ValueKind               ValueKind
	ValueTag                ValueTag
	DatabaseID              [16]byte
	TxnID                   uint64
	CommitNonce             [16]byte
	PageCount               uint64
	RangeRecordCount        uint64
	ActiveFeedCount         uint64
	FeedIndexLimit          uint64
	MembershipEntryCount    uint64
	MembershipIDLimit       uint64
	MetadataUncompressedLen uint64
	MetadataCompressedLen   uint64
	RetirementBatchCount    uint64
	RangeRoot               uint32
	CatalogNameRoot         uint32
	CatalogIndexRoot        uint32
	FeedUsedRoot            uint32
	MembershipIDRoot        uint32
	MembershipHashRoot      uint32
	MembershipUsedRoot      uint32
	MetadataRoot            uint32
	FreeBitmapRoot          uint32
	RetirementRoot          uint32
}

// EncodePage returns one exact 4,096-byte meta page, including its CRC-32C.
// Validation is intentionally separate so tests and recovery tooling can encode
// deliberately invalid dynamic fields without inventing a second codec.
func (m Meta) EncodePage() [PageSize]byte {
	var page [PageSize]byte
	copy(page[0:8], MetaMagic)
	binary.LittleEndian.PutUint16(page[8:10], MetaSize)
	page[10] = PageShift
	page[11] = byte(m.AddressFamily)
	page[12] = byte(m.ValueKind)
	copy(page[16:32], m.ValueTag.wire[:])
	copy(page[32:48], m.DatabaseID[:])
	binary.LittleEndian.PutUint64(page[48:56], m.TxnID)
	copy(page[56:72], m.CommitNonce[:])
	binary.LittleEndian.PutUint64(page[72:80], m.PageCount)
	binary.LittleEndian.PutUint64(page[80:88], m.RangeRecordCount)
	binary.LittleEndian.PutUint64(page[88:96], m.ActiveFeedCount)
	binary.LittleEndian.PutUint64(page[96:104], m.FeedIndexLimit)
	binary.LittleEndian.PutUint64(page[104:112], m.MembershipEntryCount)
	binary.LittleEndian.PutUint64(page[112:120], m.MembershipIDLimit)
	binary.LittleEndian.PutUint64(page[120:128], m.MetadataUncompressedLen)
	binary.LittleEndian.PutUint64(page[128:136], m.MetadataCompressedLen)
	binary.LittleEndian.PutUint64(page[136:144], m.RetirementBatchCount)
	binary.LittleEndian.PutUint32(page[144:148], m.RangeRoot)
	binary.LittleEndian.PutUint32(page[148:152], m.CatalogNameRoot)
	binary.LittleEndian.PutUint32(page[152:156], m.CatalogIndexRoot)
	binary.LittleEndian.PutUint32(page[156:160], m.FeedUsedRoot)
	binary.LittleEndian.PutUint32(page[160:164], m.MembershipIDRoot)
	binary.LittleEndian.PutUint32(page[164:168], m.MembershipHashRoot)
	binary.LittleEndian.PutUint32(page[168:172], m.MembershipUsedRoot)
	binary.LittleEndian.PutUint32(page[172:176], m.MetadataRoot)
	binary.LittleEndian.PutUint32(page[176:180], m.FreeBitmapRoot)
	binary.LittleEndian.PutUint32(page[180:184], m.RetirementRoot)
	binary.LittleEndian.PutUint32(page[MetaCRCOffset:MetaCRCOffset+4], metaCRC(page[:]))
	return page
}

func decodeMetaUnchecked(page []byte) (Meta, bool) {
	var tagWire [16]byte
	copy(tagWire[:], page[16:32])
	tag, ok := valueTagFromWire(tagWire)
	if !ok {
		return Meta{}, false
	}

	meta := Meta{
		AddressFamily:           AddressFamily(page[11]),
		ValueKind:               ValueKind(page[12]),
		ValueTag:                tag,
		TxnID:                   binary.LittleEndian.Uint64(page[48:56]),
		PageCount:               binary.LittleEndian.Uint64(page[72:80]),
		RangeRecordCount:        binary.LittleEndian.Uint64(page[80:88]),
		ActiveFeedCount:         binary.LittleEndian.Uint64(page[88:96]),
		FeedIndexLimit:          binary.LittleEndian.Uint64(page[96:104]),
		MembershipEntryCount:    binary.LittleEndian.Uint64(page[104:112]),
		MembershipIDLimit:       binary.LittleEndian.Uint64(page[112:120]),
		MetadataUncompressedLen: binary.LittleEndian.Uint64(page[120:128]),
		MetadataCompressedLen:   binary.LittleEndian.Uint64(page[128:136]),
		RetirementBatchCount:    binary.LittleEndian.Uint64(page[136:144]),
		RangeRoot:               binary.LittleEndian.Uint32(page[144:148]),
		CatalogNameRoot:         binary.LittleEndian.Uint32(page[148:152]),
		CatalogIndexRoot:        binary.LittleEndian.Uint32(page[152:156]),
		FeedUsedRoot:            binary.LittleEndian.Uint32(page[156:160]),
		MembershipIDRoot:        binary.LittleEndian.Uint32(page[160:164]),
		MembershipHashRoot:      binary.LittleEndian.Uint32(page[164:168]),
		MembershipUsedRoot:      binary.LittleEndian.Uint32(page[168:172]),
		MetadataRoot:            binary.LittleEndian.Uint32(page[172:176]),
		FreeBitmapRoot:          binary.LittleEndian.Uint32(page[176:180]),
		RetirementRoot:          binary.LittleEndian.Uint32(page[180:184]),
	}
	copy(meta.DatabaseID[:], page[32:48])
	copy(meta.CommitNonce[:], page[56:72])
	return meta, true
}

func (m Meta) staticIdentityEqual(other Meta) bool {
	return m.AddressFamily == other.AddressFamily &&
		m.ValueKind == other.ValueKind &&
		m.ValueTag == other.ValueTag &&
		m.DatabaseID == other.DatabaseID
}

var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

func metaCRC(page []byte) uint32 {
	crc := crc32.Update(0, castagnoliTable, page[:MetaCRCOffset])
	var zero [4]byte
	crc = crc32.Update(crc, castagnoliTable, zero[:])
	return crc32.Update(crc, castagnoliTable, page[MetaCRCOffset+4:])
}
