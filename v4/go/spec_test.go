package iprangedb

import "testing"

// meta byte offsets are contiguous and end at meta_size (mirrors the Rust spec test).
func TestMetaOffsetsContiguous(t *testing.T) {
	checks := []struct {
		got, want int
		name      string
	}{
		{metaMagic, pageHeaderSize, "magic"},
		{metaVersionMajor, metaMagic + 8, "version_major"},
		{metaVersionMinor, metaVersionMajor + 2, "version_minor"},
		{metaMetaSize, metaVersionMinor + 2, "meta_size"},
		{metaPageSize, metaMetaSize + 2, "page_size"},
		{metaChecksumAlgo, metaPageSize + 4, "checksum_algo"},
		{metaFlags, metaChecksumAlgo + 1, "flags"},
		{metaKeyWidth, metaFlags + 1, "key_width"},
		{metaScopeWidth, metaKeyWidth + 1, "scope_width"},
		{metaRecordSize, metaScopeWidth + 1, "record_size"},
		{metaCreatedUnixtime, metaRecordSize + 4, "created_unixtime"},
		{metaRootPgno, metaCreatedUnixtime + 8, "root_pgno"},
		{metaTreeHeight, metaRootPgno + 4, "tree_height"},
		{metaTotalPages, metaTreeHeight + 4, "total_pages"},
		{metaRecordCount, metaTotalPages + 8, "record_count"},
		{metaTxnID, metaRecordCount + 8, "txn_id"},
		{metaUpdatedUnixtime, metaTxnID + 8, "updated_unixtime"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s offset = %d, want %d", c.name, c.got, c.want)
		}
	}
	if metaUpdatedUnixtime+8 != int(metaSize) {
		t.Errorf("last field ends at %d, want meta_size %d", metaUpdatedUnixtime+8, metaSize)
	}
	if metaStaticStart != metaMagic || metaStaticEnd != metaRootPgno {
		t.Errorf("static region [%d,%d) != [%d,%d)", metaStaticStart, metaStaticEnd, metaMagic, metaRootPgno)
	}
}

func TestGeometryFormulas(t *testing.T) {
	if recordSize(4, 0) != 8 {
		t.Errorf("record_size(4,0)=%d want 8", recordSize(4, 0))
	}
	if leafMax(8) != (4096-16)/8 {
		t.Errorf("leaf_max(8)=%d want %d", leafMax(8), (4096-16)/8) // 510
	}
	if recordSize(16, 4) != 36 {
		t.Errorf("record_size(16,4)=%d want 36", recordSize(16, 4))
	}
	if leafMax(36) != (4096-16)/36 {
		t.Errorf("leaf_max(36)=%d want %d", leafMax(36), (4096-16)/36) // 113
	}
	if branchMax(4) != (4096-16-4)/(4+4) {
		t.Errorf("branch_max(4)=%d want %d", branchMax(4), (4096-16-4)/(4+4)) // 509
	}
	if branchMax(16) != (4096-16-4)/(16+4) {
		t.Errorf("branch_max(16)=%d want %d", branchMax(16), (4096-16-4)/(16+4)) // 203
	}
}

func TestIPVersionMapping(t *testing.T) {
	if V4.keyWidth() != 4 || V6.keyWidth() != 16 {
		t.Fatal("key_width mapping")
	}
	if V4.flag() != 0 || V6.flag() != flagIPVersion {
		t.Fatal("flag mapping")
	}
	if ipVersionFromFlagBit(0) != V4 || ipVersionFromFlagBit(1) != V6 {
		t.Fatal("from_flag_bit mapping")
	}
}
