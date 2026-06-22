package iprangedb

import (
	"bytes"
	"testing"
)

func sampleMeta() meta {
	return meta{
		pgno:            1,
		versionMinor:    0,
		metaSize:        metaSize,
		pageSize:        pageSize,
		checksumAlgo:    checksumAlgoCRC32C,
		flags:           flagIPVersion, // IPv6
		keyWidth:        16,
		scopeWidth:      4,
		recordSize:      recordSize(16, 4),
		createdUnixtime: 0x1122334455667788,
		rootPgno:        0x0A0B0C0D,
		treeHeight:      0x11121314,
		totalPages:      0x2122232425262728,
		recordCount:     0x3132333435363738,
		txnID:           0x4142434445464748,
		updatedUnixtime: 0x5152535455565758,
	}
}

func TestMetaRoundTripAndChecksum(t *testing.T) {
	m := sampleMeta()
	var page [pageSize]byte
	m.encodeInto(page[:])
	if !verifyPage(page[:]) {
		t.Fatal("encoded meta is not self-consistent")
	}
	if decodeMeta(page[:]) != m {
		t.Fatalf("round-trip: got %+v want %+v", decodeMeta(page[:]), m)
	}
	h := decodePageHeader(page[:])
	if h.pageType != pageTypeMeta || h.reserved != 0 || h.entryCount != 0 || h.pgno != 1 {
		t.Fatalf("header = %+v", h)
	}
	if readMagic(page[:]) != magic {
		t.Fatal("magic")
	}
	if readVersionMajor(page[:]) != versionMajor {
		t.Fatal("version_major")
	}
}

// The correctness anchor: every field appears at its exact §5.1 byte offset, LE.
func TestMetaFieldByteOffsetsMatchSpec(t *testing.T) {
	m := sampleMeta()
	var p [pageSize]byte
	m.encodeInto(p[:])

	if p[phPageType] != pageTypeMeta || p[phReserved] != 0 {
		t.Fatal("page header type/reserved")
	}
	if le.Uint16(p[phEntryCount:]) != 0 || le.Uint32(p[phPgno:]) != 1 {
		t.Fatal("page header entry_count/pgno")
	}
	if !bytes.Equal(p[metaMagic:metaMagic+8], []byte("IPRANGE4")) {
		t.Fatal("magic bytes")
	}
	if le.Uint16(p[metaVersionMajor:]) != 4 || le.Uint16(p[metaVersionMinor:]) != 0 {
		t.Fatal("version")
	}
	if le.Uint16(p[metaMetaSize:]) != 90 || le.Uint32(p[metaPageSize:]) != 4096 {
		t.Fatal("meta_size/page_size")
	}
	if p[metaChecksumAlgo] != 1 || p[metaFlags] != flagIPVersion {
		t.Fatal("checksum_algo/flags")
	}
	if p[metaKeyWidth] != 16 || p[metaScopeWidth] != 4 {
		t.Fatal("key_width/scope_width")
	}
	if le.Uint32(p[metaRecordSize:]) != 36 {
		t.Fatal("record_size")
	}
	if le.Uint64(p[metaCreatedUnixtime:]) != 0x1122334455667788 {
		t.Fatal("created_unixtime")
	}
	if le.Uint32(p[metaRootPgno:]) != 0x0A0B0C0D || le.Uint32(p[metaTreeHeight:]) != 0x11121314 {
		t.Fatal("root_pgno/tree_height")
	}
	if le.Uint64(p[metaTotalPages:]) != 0x2122232425262728 {
		t.Fatal("total_pages")
	}
	if le.Uint64(p[metaRecordCount:]) != 0x3132333435363738 {
		t.Fatal("record_count")
	}
	if le.Uint64(p[metaTxnID:]) != 0x4142434445464748 {
		t.Fatal("txn_id")
	}
	if le.Uint64(p[metaUpdatedUnixtime:]) != 0x5152535455565758 {
		t.Fatal("updated_unixtime")
	}
	// exact little-endian byte check for one multi-byte field (created_unixtime).
	if !bytes.Equal(p[metaCreatedUnixtime:metaCreatedUnixtime+8],
		[]byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}) {
		t.Fatal("created_unixtime LE bytes")
	}
	// the region [meta_size, page_size) is reserved zero.
	for _, x := range p[metaSize:] {
		if x != 0 {
			t.Fatal("region [meta_size, page_size) must be zero")
		}
	}
}

func TestPageHeaderWriteRoundTrip(t *testing.T) {
	var p [pageSize]byte
	writePageHeader(p[:], pageTypeLeaf, 7, 42)
	finalizeChecksum(p[:])
	h := decodePageHeader(p[:])
	if h.pageType != pageTypeLeaf || h.reserved != 0 || h.entryCount != 7 || h.pgno != 42 {
		t.Fatalf("header = %+v", h)
	}
	if !verifyPage(p[:]) {
		t.Fatal("verify")
	}
}
