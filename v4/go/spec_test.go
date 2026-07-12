package iprangedb

import "testing"

// meta byte offsets are contiguous and end at meta_size (mirrors the Rust spec test).
func TestMetaOffsetsContiguous(t *testing.T) {
	checks := []struct {
		got, want int
		name      string
	}{
		{MetaMagic, PageHeaderSize, "magic"},
		{MetaVersionMajor, MetaMagic + 8, "version_major"},
		{MetaVersionMinor, MetaVersionMajor + 2, "version_minor"},
		{MetaMetaSize, MetaVersionMinor + 2, "meta_size"},
		{MetaPageSize, MetaMetaSize + 2, "page_size"},
		{MetaChecksumAlgo, MetaPageSize + 4, "checksum_algo"},
		{MetaFlags, MetaChecksumAlgo + 1, "flags"},
		{MetaKeyWidth, MetaFlags + 1, "key_width"},
		{MetaScopeMode, MetaKeyWidth + 1, "scope_mode"},
		{MetaRecordSize, MetaScopeMode + 1, "record_size"},
		{MetaCreatedUnix, MetaRecordSize + 4, "created_unix"},
		{MetaRootPgno, MetaCreatedUnix + 8, "root_pgno"},
		{MetaTreeHeight, MetaRootPgno + 4, "tree_height"},
		{MetaTotalPages, MetaTreeHeight + 4, "total_pages"},
		{MetaRecordCount, MetaTotalPages + 8, "record_count"},
		{MetaTxnID, MetaRecordCount + 8, "txn_id"},
		{MetaUpdatedUnix, MetaTxnID + 8, "updated_unix"},
		{MetaScopeTableRoot, MetaUpdatedUnix + 8, "scope_table_root"},
		{MetaFreeListHead, MetaScopeTableRoot + 4, "free_list_head"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s offset = %d, want %d", c.name, c.got, c.want)
		}
	}
	if MetaFreeListHead+4 != int(MetaSize) {
		t.Errorf("last field ends at %d, want meta_size %d", MetaFreeListHead+4, MetaSize)
	}
	if MetaStaticStart != MetaMagic || MetaStaticEnd != MetaRootPgno {
		t.Errorf("static region [%d,%d) != [%d,%d)", MetaStaticStart, MetaStaticEnd, MetaMagic, MetaRootPgno)
	}
}

func TestGeometryFormulas(t *testing.T) {
	// v4.3: scope_id is always 4 bytes (u32), so record_size = 2*key_width + 4.
	if recordSize(4) != 12 {
		t.Errorf("record_size(4)=%d want 12", recordSize(4))
	}
	if leafMax(4) != (4096-16)/12 {
		t.Errorf("leaf_max(4)=%d want %d", leafMax(4), (4096-16)/12) // 340
	}
	if recordSize(16) != 36 {
		t.Errorf("record_size(16)=%d want 36", recordSize(16))
	}
	if leafMax(16) != (4096-16)/36 {
		t.Errorf("leaf_max(16)=%d want %d", leafMax(16), (4096-16)/36) // 113
	}
	if branchMax(4) != (4096-16-4)/(4+4) {
		t.Errorf("branch_max(4)=%d want %d", branchMax(4), (4096-16-4)/(4+4)) // 509
	}
	if branchMax(16) != (4096-16-4)/(16+4) {
		t.Errorf("branch_max(16)=%d want %d", branchMax(16), (4096-16-4)/(16+4)) // 203
	}
}

func TestIPVersionMapping(t *testing.T) {
	if V4.KeyWidth() != 4 || V6.KeyWidth() != 16 {
		t.Fatal("key_width mapping")
	}
	if V4.Flag() != 0 || V6.Flag() != FlagIPVersion {
		t.Fatal("flag mapping")
	}
}
