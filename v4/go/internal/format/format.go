// Package format implements the exact v4 wire codecs: fixed constants and
// literal little-endian encodings only. It owns no I/O, no mappings, and no
// language runtime state. Every function is a pure decode/encode of bounded
// scalar records, so this package can be shared unchanged by the reader core,
// the writer core, validation, recovery, and the worker.
package format

import "errors"

// Fixed constants from binary-format-v4.md section 2.
const (
	PageSize                = 4096
	PageShift               = 12
	MetaSize                = 256
	MaxPageCount            = uint64(1 << 32)
	MaxTreeLevel            = 31
	MaxMetadataUncompressed = 20_971_520 // 20 MiB
	BitmapLeafWords         = 500
	BitmapLeafBits          = 32_000
	BitmapFanout            = 256
)

// Page types (binary-format-v4.md section 5.1).
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
	PageTypeStructureIDDirectory PageType = 18
	PageTypeStructureIDRecord    PageType = 19
	PageTypeStructureHashBranch  PageType = 20
	PageTypeStructureHashLeaf    PageType = 21
)

// Address families (binary-format-v4.md section 2).
const (
	AddressFamilyIPv4 uint8 = 4
	AddressFamilyIPv6 uint8 = 6
)

// Value kinds (meta field, binary-format-v4.md section 4).
const (
	ValueKindDirect     uint8 = 1
	ValueKindMembership uint8 = 2
	ValueKindStructured uint8 = 3
)

// Structure kinds (meta field, sections 4 and 9A).
const (
	StructureKindNone                uint8 = 0
	StructureKindNetworkEnrichmentV1 uint8 = 1
)

// Blob kinds (binary-format-v4.md section 10).
const (
	BlobKindMembership uint32 = 1
	BlobKindRetirement uint32 = 2
)

// MaxMembershipWordCount is the largest word_count a membership bitmap may
// declare (section 9.1).
const MaxMembershipWordCount = 67_108_864

// Raw wire magics.
var (
	MainMagic    = [8]byte{'I', 'P', 'R', 'A', 'N', 'G', 'E', '4'}
	PageMagic    = [4]byte{'I', 'P', '4', 'P'}
	SidecarMagic = [8]byte{'I', 'P', 'R', 'D', 'R', 'S', '4', 0}
)

var (
	// ErrNotV4 reports bytes that are not the exact v4 identity.
	ErrNotV4 = errors.New("not v4")
	// ErrFormat reports a structurally invalid v4 artifact.
	ErrFormat = errors.New("invalid v4 format")
	// ErrUnsupportedStructure reports a valid v4 static identity whose
	// structure kind this SDK does not implement.
	ErrUnsupportedStructure = errors.New("unsupported structure kind")
	// ErrIO reports an underlying platform operation failure.
	ErrIO = errors.New("v4 platform operation failed")
)
