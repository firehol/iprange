// Package iprangedb reads and writes the iprange v4 live mutable on-disk DB format —
// a portable, mmap'd, copy-on-write B+tree of fixed-size [from, to, scope] records,
// mutated in place (set / delete) without a full rewrite. It is the live working store
// that complements the sealed v3 snapshot format (which v4 exports to, §13).
//
// This pure-Go implementation cross-reads files written by the Rust reference
// (v4/rust/iprange-livedb); both pass the shared conformance corpus in ../conformance.
// The on-disk contract is specified in .agents/sow/specs/design-iprange-v4-livedb.md.
package iprangedb

// Format constants (normative). Each value matches design-iprange-v4-livedb.md and the
// Rust spec module field-for-field; changing one is a format change.

// magic is the 8-byte file magic, compared bytewise (endianness-independent, §5.1).
var magic = [8]byte{'I', 'P', 'R', 'A', 'N', 'G', 'E', '4'}

const (
	// versionMajor — a reader MUST reject any other major (§5.1 forward-compat).
	versionMajor uint16 = 4
	// versionMinor for the v4.0 contract.
	versionMinor uint16 = 0

	// metaSize for v4.0: the offset just past the last defined meta field (§5.1). A
	// reader requires meta_size >= 90, and exactly 90 at version_minor == 0.
	metaSize uint16 = 90

	// pageSize is the fixed page size for all v4.x (D10). A reader MUST reject any other
	// value at version_major == 4. Pinning it to 4096 fixes meta-B at byte offset 4096
	// and completes bootstrap (§5.1). It is the page-aligned I/O / allocation unit (§2).
	pageSize = 4096

	// versionMinorMetadata is the v4.1 contract (the metadata system: scope table +
	// per-scope KV). Additive (§C.6): a v4.0 reader reads the IP tree and skips the
	// metadata; a v4.0 writer refuses to mutate a v4.1 file.
	versionMinorMetadata uint16 = 1

	// metaSizeV41 is meta_size for v4.1: v4.0's 90 plus the trailing scope_table_root (u32)
	// at offset 90.
	metaSizeV41 uint16 = 94

	// checksumAlgoCRC32C selects CRC32C/Castagnoli (D9); a field, so future algorithms
	// are possible. v4.0 readers require this value.
	checksumAlgoCRC32C uint8 = 1

	// treeHeightMax is the hard cap on tree_height (§5.3): even at the degenerate minimum
	// branch fanout of 2, a u32-pgno file (< 2^32 pages) cannot exceed ~32 levels. A
	// reader MUST reject tree_height > 32 and treat descending deeper as a hard error.
	treeHeightMax uint32 = 32
)

// Common 16-byte page header field offsets within a page (§5).
const (
	pageHeaderSize = 16 // size of the header present on every page
	phPageType     = 0  // page_type (u8): 1 meta, 2 branch, 3 leaf
	phReserved     = 1  // reserved (u8): MUST be 0
	phEntryCount   = 2  // entry_count (u16): records in a leaf / separators in a branch; 0 for meta
	phPgno         = 4  // pgno (u32): this page's own number; a reader MUST verify it matches
	phChecksum     = 8  // checksum (u64): D9, over the whole page with this field zeroed
)

// Page type values (§5, §5.2, §5.3).
const (
	pageTypeMeta   uint8 = 1
	pageTypeBranch uint8 = 2
	pageTypeLeaf   uint8 = 3
	// v4.1 metadata page types (§D). A v4.0 reader never reaches these (they hang off
	// scope_table_root); a v4.1 reader rejects an unknown page_type.
	pageTypeScopeBranch uint8 = 4 // scope-table branch (internal) page
	pageTypeScopeLeaf   uint8 = 5 // scope-table leaf (fixed scopeRecordSize-byte headers)
	pageTypeKVBranch    uint8 = 6 // per-scope KV branch page (variable-length separators, slot directory)
	pageTypeKVLeaf      uint8 = 7 // per-scope KV leaf page (variable-length entries, slot directory)
	pageTypeOverflow    uint8 = 8 // KV overflow page (chained payload for a large value)
)

// Meta page field offsets within the page, after the 16-byte header (§5.1).
const (
	metaMagic           = 16 // magic (u8[8])
	metaVersionMajor    = 24 // version_major (u16)
	metaVersionMinor    = 26 // version_minor (u16)
	metaMetaSize        = 28 // meta_size (u16)
	metaPageSize        = 30 // page_size (u32)
	metaChecksumAlgo    = 34 // checksum_algo (u8)
	metaFlags           = 35 // flags (u8): bit0 ip_version; bits 1-7 reserved = 0
	metaKeyWidth        = 36 // key_width (u8): 4 or 16
	metaScopeWidth      = 37 // scope_width (u8)
	metaRecordSize      = 38 // record_size (u32): MUST equal 2*key_width + scope_width
	metaCreatedUnixtime = 42 // created_unixtime (u64): static; identical in both metas
	metaRootPgno        = 50 // root_pgno (u32): 0 = empty tree. First dynamic field.
	metaTreeHeight      = 54 // tree_height (u32): 0 = empty; leaf level = 1
	metaTotalPages      = 58 // total_pages (u64): 2 <= total_pages < 2^32
	metaRecordCount     = 66 // record_count (u64): UNVERIFIED hint; never size an allocation from it
	metaTxnID           = 74 // txn_id (u64): monotonic; the checksum-valid meta with the higher value is active
	metaUpdatedUnixtime = 82 // updated_unixtime (u64): caller-supplied per commit

	// metaScopeTableRoot is scope_table_root (u32), v4.1 only (version_minor >= 1,
	// meta_size >= 94). 0 = no metadata; else the scope table's root pgno (§C.1). At v4.0
	// (meta_size == 90) this offset lies in the reserved-zero tail.
	metaScopeTableRoot = 90

	// metaStaticStart/metaStaticEnd bound the static identity region [16, 50) (§5.1):
	// magic..=created_unixtime. Two valid metas MUST agree byte-for-byte here; the
	// dynamic region [50, metaSize) differs per commit.
	metaStaticStart = 16
	metaStaticEnd   = 50
)

// flagIPVersion is meta flags bit 0: IP version (0 = IPv4, 1 = IPv6). Bits 1-7 reserved.
const flagIPVersion uint8 = 0b1

// IPVersion is the IP family of a file (meta flags bit 0).
type IPVersion uint8

// IP families.
const (
	V4 IPVersion = iota // IPv4: 4-byte keys
	V6                  // IPv6: 16-byte keys
)

// keyWidth returns the key width in bytes (4 or 16).
func (v IPVersion) keyWidth() uint8 {
	if v == V6 {
		return 16
	}
	return 4
}

// flag returns the meta flags value for this family (bit 0).
func (v IPVersion) flag() uint8 {
	if v == V6 {
		return flagIPVersion
	}
	return 0
}

// ipVersionFromFlagBit returns the family of a flags byte (only bit 0 is meaningful; the
// caller rejects other bits, §5.1).
func ipVersionFromFlagBit(flags uint8) IPVersion {
	if flags&flagIPVersion != 0 {
		return V6
	}
	return V4
}

// recordSize returns record_size = 2*key_width + scope_width (§4, D1). The inputs are
// bytes so it never overflows a u32.
func recordSize(keyWidth, scopeWidth uint8) uint32 {
	return 2*uint32(keyWidth) + uint32(scopeWidth)
}

// leafMax returns the maximum records in a leaf: (page_size - 16) / record_size (§5.3).
// record_size MUST be > 0 (it is >= 2*key_width >= 8).
func leafMax(recSize uint32) int {
	return (pageSize - pageHeaderSize) / int(recSize)
}

// branchMax returns the maximum separators in a branch:
// (page_size - 16 - 4) / (key_width + 4) (§5.2). The leading -4 is child_pgno[0]; each
// separator adds key_width + 4. Children = separators + 1.
func branchMax(keyWidth uint8) int {
	return (pageSize - pageHeaderSize - 4) / (int(keyWidth) + 4)
}

// --- v4.1 scope table (§C.2, §D): a fixed-record B+tree keyed by scope_id (u32) ---

const (
	// fileScopeID — scope_id 0 is reserved for the file/dataset-level metadata (the FILE
	// target). ScopeDefine never returns it.
	fileScopeID uint32 = 0

	// scopeKeyWidth — the scope-table B+tree key is the 4-byte scope_id.
	scopeKeyWidth = 4

	// scopeNameMax is the max bytes of a per-scope name (UTF-8). The fixed slot keeps the
	// seek a fixed offset.
	scopeNameMax = 256

	// Per-scope header record layout (§C.2), little-endian, within a scope-table leaf.
	scopeRecID      = 0   // scope_id (u32) — the B+tree key, first field of the record
	scopeRecVersion = 4   // version (u64)
	scopeRecType    = 12  // type (u8) — opaque caller value (engine does not reject unknown)
	scopeRecNameLen = 13  // name_len (u16, 0..=256)
	scopeRecName    = 15  // name (scopeNameMax bytes; name_len used, the rest MUST be zero)
	scopeRecKVRoot  = 271 // kv_root (u32) — 0 = no KV; else this scope's KV tree root (§C.4)
	// scopeRecordSize is the fixed per-scope record size: 4 + 8 + 1 + 2 + 256 + 4 = 275.
	scopeRecordSize = 275
)

// scopeLeafMax returns the max per-scope records in a scope-table leaf:
// (page_size - 16) / 275.
func scopeLeafMax() int {
	return (pageSize - pageHeaderSize) / scopeRecordSize
}

// scopeBranchMax returns the max separators in a scope-table branch: same geometry as
// branchMax(4).
func scopeBranchMax() int {
	return branchMax(scopeKeyWidth)
}

// --- v4.1 per-scope KV (§C.4, §D): a slot-directory B+tree behind each kv_root ---

const (
	// kvKeyMin is the minimum KV key length in bytes. An empty key is rejected (§C.4).
	kvKeyMin = 1
	// kvKeyMax is the maximum KV key length in bytes, UTF-8, no NUL (§C.4).
	kvKeyMax = 1024

	// kvTypeText: type == 0 ⇒ the value is text the engine validates as UTF-8 + no NUL
	// (§C.4); any non-zero type is caller-defined binary the engine never interprets.
	kvTypeText uint32 = 0

	// kvValueInline / kvValueOverflow: the value_kind byte in a KV leaf entry — the value
	// bytes live inside the entry, or in an overflow chain (§D).
	kvValueInline   uint8 = 0
	kvValueOverflow uint8 = 1

	// kvSlotSize is one slot in a KV page's slot directory: a u16 byte offset (from the
	// page start) to the entry heap (§D). The directory grows from the front, the heap from
	// the back.
	kvSlotSize = 2

	// kvPageBody is the bytes available for KV slots + entry heap on a page: everything
	// after the 16-byte header. The slot directory and the entry heap share this region from
	// opposite ends.
	kvPageBody = pageSize - pageHeaderSize

	// overflowNextPgno is the next_pgno (u32) offset within an overflow page, right after
	// the common header (§D).
	overflowNextPgno = pageHeaderSize
	// overflowPayload is the payload bytes per overflow page: page_size - 16 - 4 (header +
	// next_pgno), §D.
	overflowPayload = pageSize - pageHeaderSize - 4

	// kvInlineMax is the inline/overflow threshold (writer choice, §C.4/§D): a value of at
	// most this many bytes is stored inline; larger values go to an overflow chain.
	// value_kind makes each entry self-describing, so a reader parses either regardless of
	// this threshold. Chosen so a single entry (max-key + descriptor + inline value) always
	// fits a fresh leaf's body.
	kvInlineMax = 512

	// kvBranchDirStart is where a KV branch's slot directory starts: after the header AND
	// the leftmost-child u32 (§D).
	kvBranchDirStart = pageHeaderSize + 4
)

// kvEntryFixed is the fixed bytes of a KV leaf entry header before the inline value:
// key_len(2) · key · type(4) · value_kind(1). key is the variable part; this returns the
// constant surround for a given keyLen.
func kvEntryFixed(keyLen int) int {
	return 2 + keyLen + 4 + 1
}

// kvInlineEntrySize is the encoded size of an inline KV leaf entry (entry bytes, excluding
// its slot): the fixed header + value_len(4) + the value bytes (§D).
func kvInlineEntrySize(keyLen, valueLen int) int {
	return kvEntryFixed(keyLen) + 4 + valueLen
}

// kvOverflowEntrySize is the encoded size of an overflow KV leaf entry (entry bytes,
// excluding its slot): the fixed header + first_pgno(4) + value_total_len(8) (§D).
func kvOverflowEntrySize(keyLen int) int {
	return kvEntryFixed(keyLen) + 4 + 8
}

// kvBranchSepSize is the encoded size of a KV branch separator (entry bytes, excluding its
// slot): sep_len(2) · sep_key · child_pgno(4) (§D).
func kvBranchSepSize(sepLen int) int {
	return 2 + sepLen + 4
}
