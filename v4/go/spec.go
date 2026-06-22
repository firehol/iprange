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

	// checksumAlgoCRC32C selects CRC32C/Castagnoli (D9); a field, so future algorithms
	// are possible. v4.0 readers require this value.
	checksumAlgoCRC32C uint8 = 1

	// treeHeightMax is the hard cap on tree_height (§5.3): even at the degenerate minimum
	// branch fanout of 2, a u32-pgno file (<= 2^32 pages) cannot exceed ~32 levels. A
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
	metaTotalPages      = 58 // total_pages (u64): 2 <= total_pages <= 2^32
	metaRecordCount     = 66 // record_count (u64): UNVERIFIED hint; never size an allocation from it
	metaTxnID           = 74 // txn_id (u64): monotonic; the checksum-valid meta with the higher value is active
	metaUpdatedUnixtime = 82 // updated_unixtime (u64): caller-supplied per commit

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
