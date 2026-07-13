// Package iprangedb: format constants for the v4.3 streaming mmap COW engine.
//
// Every record is a fixed [from: K, to: K, scope_id: u32] — 12 bytes (IPv4)
// or 36 bytes (IPv6). scope_mode (0/1/2) selects how the 4-byte scope_id is
// interpreted. There is no scope_width field.
package iprangedb

// File magic (8 ASCII bytes, compared bytewise).
const Magic = "IPRANGE4"

// Version constants.
const VersionMajor uint16 = 4
const VersionMinor uint16 = 3 // v4.3: streaming mmap COW engine (breaking change)

// meta_size: offset past the last defined meta field.
const MetaSize uint16 = 98

// Fixed page size for all v4.x.
const PageSize = 4096

// Size of the header present on every page.
const PageHeaderSize = 16

// Maximum B+tree height.
const TreeHeightMax = 32

const ChecksumAlgoCRC32C = 1

// Page header field offsets.
const (
	PHPageType   = 0
	PHReserved   = 1
	PHEntryCount = 2
	PHPgno       = 4
	PHChecksum   = 8
)

// page_type values.
const (
	PageTypeMeta        = 1
	PageTypeBranch      = 2
	PageTypeLeaf        = 3
	PageTypeScopeBranch = 4
	PageTypeScopeLeaf   = 5
	PageTypeKVBranch    = 6
	PageTypeKVLeaf      = 7
	PageTypeOverflow    = 8
	PageTypeTxnFree     = 9
)

// Meta page field offsets (within the page, after the 16-byte header).
const (
	MetaMagic          = 16
	MetaVersionMajor   = 24
	MetaVersionMinor   = 26
	MetaMetaSize       = 28
	MetaPageSize       = 30
	MetaChecksumAlgo   = 34
	MetaFlags          = 35
	MetaKeyWidth       = 36
	MetaScopeMode      = 37 // was MetaScopeWidth
	MetaRecordSize     = 38
	MetaCreatedUnix    = 42
	MetaRootPgno       = 50
	MetaTreeHeight     = 54
	MetaTotalPages     = 58
	MetaRecordCount    = 66
	MetaTxnID          = 74
	MetaUpdatedUnix    = 82
	MetaScopeTableRoot = 90
	MetaFreeListHead   = 94

	MetaStaticStart = 16
	MetaStaticEnd   = 50
)

// scope_mode values.
const (
	ScopeModeScalar   = 0 // u32 is a value (e.g. timestamp); compare with =
	ScopeModeBitmap   = 1 // u32 IS a 32-bit bitmap; compare with &
	ScopeModeIndirect = 2 // u32 → scope table interned bitmap; compare with &
)

// flags
const FlagIPVersion = 1

// IP family.
type IPVersion int

const (
	V4 IPVersion = 0
	V6 IPVersion = 1
)

func (v IPVersion) KeyWidth() uint8 {
	if v == V6 {
		return 16
	}
	return 4
}

func (v IPVersion) Flag() uint8 {
	if v == V6 {
		return FlagIPVersion
	}
	return 0
}

// Scope ID is always 4 bytes (u32).
const ScopeIDSize = 4

// recordSize = 2*keyWidth + 4 (scope_id is always u32).
func recordSize(keyWidth uint8) uint32 {
	return 2*uint32(keyWidth) + ScopeIDSize
}

// leafMax: maximum records in a leaf.
func leafMax(keyWidth uint8) int {
	return (PageSize - PageHeaderSize) / int(recordSize(keyWidth))
}

// branchMax: maximum separators in a branch.
func branchMax(keyWidth uint8) int {
	return (PageSize - PageHeaderSize - 4) / (int(keyWidth) + 4)
}

// Transaction free-list page layout.
const (
	TxnFreeNext     = PageHeaderSize
	TxnFreeCount    = PageHeaderSize + 4
	TxnFreeFreedIn  = PageHeaderSize + 8
	TxnFreeArray    = PageHeaderSize + 16
	TxnFreeCapacity = (PageSize - TxnFreeArray) / 4
)

// Committed free-list page layout.
const (
	FreeNext       = PageHeaderSize
	FreeFreedInTxn = PageHeaderSize + 4
)

// Scope table constants (for mode 2 = indirect).
const (
	FileScopeID     = 0
	ScopeKeyWidth   = 4
	ScopeNameMax    = 256
	ScopeRecID      = 0
	ScopeRecVersion = 4
	ScopeRecType    = 12
	ScopeRecNameLen = 13
	ScopeRecName    = 15
	ScopeRecKVRoot  = 271
	ScopeRecordSize = 275
)

func scopeLeafMax() int {
	return (PageSize - PageHeaderSize) / ScopeRecordSize
}

func scopeBranchMax() int {
	return branchMax(ScopeKeyWidth)
}

// KV constants.
const (
	KVKeyMin         = 1
	KVKeyMax         = 1024
	KVTypeText       = 0
	KVValueInline    = 0
	KVValueOverflow  = 1
	KVSlotSize       = 2
	KVPageBody       = PageSize - PageHeaderSize
	OverflowNextPgno = PageHeaderSize
	OverflowPayload  = PageSize - PageHeaderSize - 4
	KVInlineMax      = 512
)
