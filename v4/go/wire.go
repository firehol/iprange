package iprangedb

import "encoding/binary"

// Unaligned little-endian field access and the fixed page structures.
//
// Per D8 every multi-byte field is little-endian, read/written by explicit byte access —
// never a packed-struct pointer cast over the mmap'd bytes (fields are not guaranteed
// naturally aligned: meta u64s sit at offsets 58/66/74, record_size at 38, branch keys
// follow a u32 child pgno). encoding/binary.LittleEndian does exactly this. This module
// is pure (de)serialization plus the page checksum finalize; the reader does validation.

var le = binary.LittleEndian

// pageHeader is the common 16-byte page header (§5), present on every page.
type pageHeader struct {
	pageType   uint8  // 1 = meta, 2 = branch, 3 = leaf
	reserved   uint8  // MUST be 0 (reader rejects non-zero)
	entryCount uint16 // records in a leaf / separators in a branch; 0 for a meta
	pgno       uint32 // this page's own number (reader verifies it matches)
	checksum   uint64 // D9 page checksum (whole page, this field zeroed)
}

// decodePageHeader parses the header from the first 16 bytes of a page. page MUST be
// >= 16 bytes.
func decodePageHeader(page []byte) pageHeader {
	return pageHeader{
		pageType:   page[phPageType],
		reserved:   page[phReserved],
		entryCount: le.Uint16(page[phEntryCount:]),
		pgno:       le.Uint32(page[phPgno:]),
		checksum:   le.Uint64(page[phChecksum:]),
	}
}

// writePageHeader writes page_type / reserved=0 / entry_count / pgno into the header. The
// checksum is written separately by finalizeChecksum after the body is filled.
func writePageHeader(page []byte, pageType uint8, entryCount uint16, pgno uint32) {
	page[phPageType] = pageType
	page[phReserved] = 0
	le.PutUint16(page[phEntryCount:], entryCount)
	le.PutUint32(page[phPgno:], pgno)
	// checksum field [8,16) is left zero until finalizeChecksum.
}

// finalizeChecksum computes the D9 checksum over the whole (already fully populated) page
// and writes it into the header checksum field. Call last, after every other byte is set.
func finalizeChecksum(page []byte) {
	le.PutUint64(page[phChecksum:], pageChecksum(page))
}

// meta is the meta page (pgno 0 / 1) — static identity + committed dynamic state (§5.1).
// magic and version_major are implied constants (encodeInto writes them; decodeMeta does
// not check them — the reader's bootstrap reads magic/version first to classify, §5.1).
type meta struct {
	pgno uint32
	// --- static identity (identical in both metas) ---
	versionMinor    uint16
	metaSize        uint16
	pageSize        uint32
	checksumAlgo    uint8
	flags           uint8
	keyWidth        uint8
	scopeWidth      uint8
	recordSize      uint32
	createdUnixtime uint64
	// --- dynamic state (per commit) ---
	rootPgno        uint32
	treeHeight      uint32
	totalPages      uint64
	recordCount     uint64
	txnID           uint64
	updatedUnixtime uint64
	// scopeTableRoot (u32, v4.1 only — §C.1). 0 = no metadata (or v4.0). At v4.0
	// (meta_size == 90) decodeMeta reports 0 and encodeInto writes 0 (keeping the reserved
	// tail zero); at v4.1 (meta_size == 94) it holds the scope-table root pgno.
	scopeTableRoot uint32
}

// encodeInto serializes into a full pageSize page buffer: zero-fill, write the page
// header (page_type=1, entry_count=0, pgno), magic, version_major, every field at its
// §5.1 offset, then finalize the checksum. page MUST be exactly pageSize bytes.
func (m *meta) encodeInto(page []byte) {
	clear(page)
	writePageHeader(page, pageTypeMeta, 0, m.pgno)
	copy(page[metaMagic:metaMagic+8], magic[:])
	le.PutUint16(page[metaVersionMajor:], versionMajor)
	le.PutUint16(page[metaVersionMinor:], m.versionMinor)
	le.PutUint16(page[metaMetaSize:], m.metaSize)
	le.PutUint32(page[metaPageSize:], m.pageSize)
	page[metaChecksumAlgo] = m.checksumAlgo
	page[metaFlags] = m.flags
	page[metaKeyWidth] = m.keyWidth
	page[metaScopeWidth] = m.scopeWidth
	le.PutUint32(page[metaRecordSize:], m.recordSize)
	le.PutUint64(page[metaCreatedUnixtime:], m.createdUnixtime)
	le.PutUint32(page[metaRootPgno:], m.rootPgno)
	le.PutUint32(page[metaTreeHeight:], m.treeHeight)
	le.PutUint64(page[metaTotalPages:], m.totalPages)
	le.PutUint64(page[metaRecordCount:], m.recordCount)
	le.PutUint64(page[metaTxnID:], m.txnID)
	le.PutUint64(page[metaUpdatedUnixtime:], m.updatedUnixtime)
	// v4.1: scope_table_root at offset 90. 0 for v4.0 (keeps the reserved tail zero).
	le.PutUint32(page[metaScopeTableRoot:], m.scopeTableRoot)
	finalizeChecksum(page)
}

// decodeMeta parses the variable meta fields from a page (no validation of
// magic/version/geometry — the reader's bootstrap does that, §5.1). page MUST be >= 90
// bytes.
func decodeMeta(page []byte) meta {
	return meta{
		pgno:            le.Uint32(page[phPgno:]),
		versionMinor:    le.Uint16(page[metaVersionMinor:]),
		metaSize:        le.Uint16(page[metaMetaSize:]),
		pageSize:        le.Uint32(page[metaPageSize:]),
		checksumAlgo:    page[metaChecksumAlgo],
		flags:           page[metaFlags],
		keyWidth:        page[metaKeyWidth],
		scopeWidth:      page[metaScopeWidth],
		recordSize:      le.Uint32(page[metaRecordSize:]),
		createdUnixtime: le.Uint64(page[metaCreatedUnixtime:]),
		rootPgno:        le.Uint32(page[metaRootPgno:]),
		treeHeight:      le.Uint32(page[metaTreeHeight:]),
		totalPages:      le.Uint64(page[metaTotalPages:]),
		recordCount:     le.Uint64(page[metaRecordCount:]),
		txnID:           le.Uint64(page[metaTxnID:]),
		updatedUnixtime: le.Uint64(page[metaUpdatedUnixtime:]),
		// v4.1 trailing field; absent (reported 0) at v4.0 (meta_size == 90).
		scopeTableRoot: decodeScopeTableRoot(page),
	}
}

// decodeScopeTableRoot reads scope_table_root only when the file declares a v4.1+ meta_size
// (>= 94); a v4.0 file (meta_size 90) reports 0 (the field lies in its reserved tail, §C.1).
func decodeScopeTableRoot(page []byte) uint32 {
	if le.Uint16(page[metaMetaSize:]) >= metaSizeV41 {
		return le.Uint32(page[metaScopeTableRoot:])
	}
	return 0
}

// readMagic reads the file magic from a page ([16, 24)). The bootstrap uses this before
// trusting any other field (§5.1).
func readMagic(page []byte) [8]byte {
	var m [8]byte
	copy(m[:], page[metaMagic:metaMagic+8])
	return m
}

// readVersionMajor reads version_major from a page ([24, 26)), used by bootstrap
// classification.
func readVersionMajor(page []byte) uint16 {
	return le.Uint16(page[metaVersionMajor:])
}
