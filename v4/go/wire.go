package iprangedb

import "encoding/binary"

// --- little-endian primitives ---

func u16le(b []byte, at int) uint16 {
	return binary.LittleEndian.Uint16(b[at:])
}

func u32le(b []byte, at int) uint32 {
	return binary.LittleEndian.Uint32(b[at:])
}

func u64le(b []byte, at int) uint64 {
	return binary.LittleEndian.Uint64(b[at:])
}

func putU16(b []byte, at int, v uint16) {
	binary.LittleEndian.PutUint16(b[at:], v)
}

func putU32(b []byte, at int, v uint32) {
	binary.LittleEndian.PutUint32(b[at:], v)
}

func putU64(b []byte, at int, v uint64) {
	binary.LittleEndian.PutUint64(b[at:], v)
}

// --- page header ---

type pageHeader struct {
	pageType   uint8
	reserved   uint8
	entryCount uint16
	pgno       uint32
	checksum   uint64
}

func decodeHeader(page []byte) pageHeader {
	return pageHeader{
		pageType:   page[PHPageType],
		reserved:   page[PHReserved],
		entryCount: u16le(page, PHEntryCount),
		pgno:       u32le(page, PHPgno),
		checksum:   u64le(page, PHChecksum),
	}
}

func writeHeader(page []byte, pageType uint8, entryCount uint16, pgno uint32) {
	page[PHPageType] = pageType
	page[PHReserved] = 0
	putU16(page, PHEntryCount, entryCount)
	putU32(page, PHPgno, pgno)
}

func finalizeChecksum(page []byte) {
	sum := pageChecksum(page)
	putU64(page, PHChecksum, sum)
}

// --- meta page ---

type meta struct {
	pgno            uint32
	versionMinor    uint16
	metaSize        uint16
	pageSize        uint32
	checksumAlgo    uint8
	flags           uint8
	keyWidth        uint8
	scopeMode       uint8 // was scopeWidth
	recordSize      uint32
	createdUnix     uint64
	rootPgno        uint32
	treeHeight      uint32
	totalPages      uint64
	recordCount     uint64
	txnID           uint64
	updatedUnix     uint64
	scopeTableRoot  uint32
	freeListHead    uint32
}

func (m *meta) encodeInto(page []byte) {
	for i := range page {
		page[i] = 0
	}
	writeHeader(page, PageTypeMeta, 0, m.pgno)
	copy(page[MetaMagic:MetaMagic+8], []byte(Magic))
	putU16(page, MetaVersionMajor, VersionMajor)
	putU16(page, MetaVersionMinor, m.versionMinor)
	putU16(page, MetaMetaSize, m.metaSize)
	putU32(page, MetaPageSize, m.pageSize)
	page[MetaChecksumAlgo] = m.checksumAlgo
	page[MetaFlags] = m.flags
	page[MetaKeyWidth] = m.keyWidth
	page[MetaScopeMode] = m.scopeMode
	putU32(page, MetaRecordSize, m.recordSize)
	putU64(page, MetaCreatedUnix, m.createdUnix)
	putU32(page, MetaRootPgno, m.rootPgno)
	putU32(page, MetaTreeHeight, m.treeHeight)
	putU64(page, MetaTotalPages, m.totalPages)
	putU64(page, MetaRecordCount, m.recordCount)
	putU64(page, MetaTxnID, m.txnID)
	putU64(page, MetaUpdatedUnix, m.updatedUnix)
	putU32(page, MetaScopeTableRoot, m.scopeTableRoot)
	putU32(page, MetaFreeListHead, m.freeListHead)
	finalizeChecksum(page)
}

func decodeMeta(page []byte) meta {
	return meta{
		pgno:           u32le(page, PHPgno),
		versionMinor:   u16le(page, MetaVersionMinor),
		metaSize:       u16le(page, MetaMetaSize),
		pageSize:       u32le(page, MetaPageSize),
		checksumAlgo:   page[MetaChecksumAlgo],
		flags:          page[MetaFlags],
		keyWidth:       page[MetaKeyWidth],
		scopeMode:      page[MetaScopeMode],
		recordSize:     u32le(page, MetaRecordSize),
		createdUnix:    u64le(page, MetaCreatedUnix),
		rootPgno:       u32le(page, MetaRootPgno),
		treeHeight:     u32le(page, MetaTreeHeight),
		totalPages:     u64le(page, MetaTotalPages),
		recordCount:    u64le(page, MetaRecordCount),
		txnID:          u64le(page, MetaTxnID),
		updatedUnix:    u64le(page, MetaUpdatedUnix),
		scopeTableRoot: u32le(page, MetaScopeTableRoot),
		freeListHead:   u32le(page, MetaFreeListHead),
	}
}

func readMagic(page []byte) []byte {
	return page[MetaMagic : MetaMagic+8]
}

func readVersionMajor(page []byte) uint16 {
	return u16le(page, MetaVersionMajor)
}
