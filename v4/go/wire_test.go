package iprangedb

import (
	"bytes"
	"testing"
)

func sampleMeta() meta {
	return meta{
		pgno:           1,
		versionMinor:   0,
		metaSize:       MetaSize,
		pageSize:       PageSize,
		checksumAlgo:   ChecksumAlgoCRC32C,
		flags:          FlagIPVersion, // IPv6
		keyWidth:       16,
		scopeMode:      ScopeModeIndirect,
		recordSize:     recordSize(16),
		createdUnix:    0x1122334455667788,
		rootPgno:       0x0A0B0C0D,
		treeHeight:     0x11121314,
		totalPages:     0x2122232425262728,
		recordCount:    0x3132333435363738,
		txnID:          0x4142434445464748,
		updatedUnix:    0x5152535455565758,
		scopeTableRoot: 0,
		freeListHead:   0,
	}
}

func TestMetaRoundTripAndChecksum(t *testing.T) {
	m := sampleMeta()
	var page [PageSize]byte
	m.encodeInto(page[:])
	if !verifyPage(page[:]) {
		t.Fatal("encoded meta is not self-consistent")
	}
	if decodeMeta(page[:]) != m {
		t.Fatalf("round-trip: got %+v want %+v", decodeMeta(page[:]), m)
	}
	h := decodeHeader(page[:])
	if h.pageType != PageTypeMeta || h.reserved != 0 || h.entryCount != 0 || h.pgno != 1 {
		t.Fatalf("header = %+v", h)
	}
	if string(readMagic(page[:])) != Magic {
		t.Fatal("magic")
	}
	if readVersionMajor(page[:]) != VersionMajor {
		t.Fatal("version_major")
	}
}

// The correctness anchor: every field appears at its exact §5.1 byte offset, LE.
func TestMetaFieldByteOffsetsMatchSpec(t *testing.T) {
	m := sampleMeta()
	var p [PageSize]byte
	m.encodeInto(p[:])

	if p[PHPageType] != PageTypeMeta || p[PHReserved] != 0 {
		t.Fatal("page header type/reserved")
	}
	if u16le(p[:], PHEntryCount) != 0 || u32le(p[:], PHPgno) != 1 {
		t.Fatal("page header entry_count/pgno")
	}
	if !bytes.Equal(p[MetaMagic:MetaMagic+8], []byte("IPRANGE4")) {
		t.Fatal("magic bytes")
	}
	if u16le(p[:], MetaVersionMajor) != 4 || u16le(p[:], MetaVersionMinor) != 0 {
		t.Fatal("version")
	}
	if u16le(p[:], MetaMetaSize) != MetaSize || u32le(p[:], MetaPageSize) != 4096 {
		t.Fatal("meta_size/page_size")
	}
	if p[MetaChecksumAlgo] != ChecksumAlgoCRC32C || p[MetaFlags] != FlagIPVersion {
		t.Fatal("checksum_algo/flags")
	}
	if p[MetaKeyWidth] != 16 || p[MetaScopeMode] != ScopeModeIndirect {
		t.Fatal("key_width/scope_mode")
	}
	if u32le(p[:], MetaRecordSize) != 36 {
		t.Fatal("record_size")
	}
	if u64le(p[:], MetaCreatedUnix) != 0x1122334455667788 {
		t.Fatal("created_unix")
	}
	if u32le(p[:], MetaRootPgno) != 0x0A0B0C0D || u32le(p[:], MetaTreeHeight) != 0x11121314 {
		t.Fatal("root_pgno/tree_height")
	}
	if u64le(p[:], MetaTotalPages) != 0x2122232425262728 {
		t.Fatal("total_pages")
	}
	if u64le(p[:], MetaRecordCount) != 0x3132333435363738 {
		t.Fatal("record_count")
	}
	if u64le(p[:], MetaTxnID) != 0x4142434445464748 {
		t.Fatal("txn_id")
	}
	if u64le(p[:], MetaUpdatedUnix) != 0x5152535455565758 {
		t.Fatal("updated_unix")
	}
	// exact little-endian byte check for one multi-byte field (created_unix).
	if !bytes.Equal(p[MetaCreatedUnix:MetaCreatedUnix+8],
		[]byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}) {
		t.Fatal("created_unix LE bytes")
	}
	// the region [meta_size, page_size) is reserved zero.
	for _, x := range p[MetaSize:] {
		if x != 0 {
			t.Fatal("region [meta_size, page_size) must be zero")
		}
	}
}

func TestPageHeaderWriteRoundTrip(t *testing.T) {
	var p [PageSize]byte
	writeHeader(p[:], PageTypeLeaf, 7, 42)
	finalizeChecksum(p[:])
	h := decodeHeader(p[:])
	if h.pageType != PageTypeLeaf || h.reserved != 0 || h.entryCount != 7 || h.pgno != 42 {
		t.Fatalf("header = %+v", h)
	}
	if !verifyPage(p[:]) {
		t.Fatal("verify")
	}
}
