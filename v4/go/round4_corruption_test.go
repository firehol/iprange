package iprangedb

import (
	"math"
	"testing"
)

func TestScopeValidationRejectsCrossLeafOrdering(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := w.ScopeIntern([]byte{byte(i), 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	meta, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	root := image[int(meta.scopeTableRoot)*PageSize : int(meta.scopeTableRoot+1)*PageSize]
	header := decodeHeader(root)
	if header.pageType != PageTypeScopeBranch {
		t.Fatalf("fixture needs a multi-leaf scope tree, root type=%d", header.pageType)
	}
	branch := newBranchView(root, int(header.entryCount), ScopeKeyWidth)
	secondLeafPgno := branch.child(1)
	secondLeaf := image[int(secondLeafPgno)*PageSize : int(secondLeafPgno+1)*PageSize]
	// Preserve local ordering while making the second leaf overlap the first.
	putU32(secondLeaf, PageHeaderSize, 1)
	finalizeChecksum(secondLeaf)

	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err == nil {
		t.Fatal("Reader.Validate accepted checksum-valid cross-leaf scope disorder")
	}
	if writer, err := openWriter[Ipv4Key](newVecPageStore(image)); err == nil {
		_ = writer
		t.Fatal("writable open accepted checksum-valid cross-leaf scope disorder")
	}
}

func TestScopeValidationRejectsOversizedBitmapLength(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeIntern([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	meta, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	leaf := image[int(meta.scopeTableRoot)*PageSize : int(meta.scopeTableRoot+1)*PageSize]
	if decodeHeader(leaf).pageType != PageTypeScopeLeaf {
		t.Fatal("fixture needs a scope leaf root")
	}
	putU16(leaf, PageHeaderSize+4, MaxBitmapWidth+1)
	finalizeChecksum(leaf)

	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err == nil {
		t.Fatal("Reader.Validate accepted bitmap_len beyond the on-disk payload")
	}
	if writer, err := openWriter[Ipv4Key](newVecPageStore(image)); err == nil {
		_ = writer
		t.Fatal("writable open accepted bitmap_len beyond the on-disk payload")
	}
}

func TestIndirectValidationRejectsDanglingRecordScope(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := w.ScopeIntern([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(1, 10, scope); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	meta, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	root := image[int(meta.rootPgno)*PageSize : int(meta.rootPgno+1)*PageSize]
	if decodeHeader(root).pageType != PageTypeLeaf {
		t.Fatal("fixture needs a data leaf root")
	}
	putU32(root, PageHeaderSize+2*int(meta.keyWidth), scope+1)
	finalizeChecksum(root)
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err == nil {
		t.Fatal("Reader.Validate accepted a record referencing an undefined scope")
	}
	if writer, err := openWriter[Ipv4Key](newVecPageStore(image)); err == nil {
		_ = writer
		t.Fatal("writable open accepted a record referencing an undefined scope")
	}
}

func TestFreeListRejectsReservedAndLivePages(t *testing.T) {
	base, meta := round4FreeListFixture(t)
	tests := []struct {
		name   string
		target uint32
	}{
		{name: "meta-page-zero", target: 0},
		{name: "active-data-root", target: meta.rootPgno},
		{name: "free-list-head", target: meta.freeListHead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			image := append([]byte(nil), base...)
			page := image[int(meta.freeListHead)*PageSize : int(meta.freeListHead+1)*PageSize]
			if u32le(page, TxnFreeCount) == 0 {
				t.Fatal("fixture has an empty free-list page")
			}
			putU32(page, TxnFreeArray, tt.target)
			finalizeChecksum(page)
			if writer, err := openWriter[Ipv4Key](newVecPageStore(image)); err == nil {
				_ = writer
				t.Fatalf("writable open accepted free-list entry for non-free page %d", tt.target)
			}
		})
	}
}

func round4FreeListFixture(t *testing.T) ([]byte, meta) {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 500; i++ {
		if err := w.Set(Ipv4Key(i*2), Ipv4Key(i*2), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 250; i++ {
		if _, err := w.Delete(Ipv4Key(i*2), Ipv4Key(i*2)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	meta, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	if meta.freeListHead == 0 || meta.rootPgno == 0 {
		t.Fatalf("fixture lacks roots: free-list=%d data=%d", meta.freeListHead, meta.rootPgno)
	}
	return image, meta
}

type round4BoundedPageReader struct {
	image []byte
	reads uint32
}

func (r *round4BoundedPageReader) page(pgno uint32) []byte {
	r.reads++
	if r.reads > r.totalPages() {
		panic("free-list traversal exceeded total_pages")
	}
	base := int(pgno) * PageSize
	return r.image[base : base+PageSize]
}

func (r *round4BoundedPageReader) totalPages() uint32 {
	return uint32(len(r.image) / PageSize)
}

func TestFreeListCycleIsRejectedWithinFileBounds(t *testing.T) {
	reader := &round4BoundedPageReader{image: make([]byte, 3*PageSize)}
	page := reader.image[2*PageSize : 3*PageSize]
	writeHeader(page, PageTypeTxnFree, 0, 2)
	putU32(page, TxnFreeNext, 2)
	finalizeChecksum(page)
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("free-list cycle was not detected before rereading pages: %v", recovered)
		}
	}()
	if err := ValidateChainCRC(reader, 2); err == nil {
		t.Fatal("ValidateChainCRC accepted a self-referential chain")
	}
}

func TestExportV3ValidatesStructureBeforeExport(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(10, 20, 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	meta, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	root := image[int(meta.rootPgno)*PageSize : int(meta.rootPgno+1)*PageSize]
	root[PHReserved] = 1
	finalizeChecksum(root)

	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err == nil {
		t.Fatal("fixture is not structurally invalid")
	}
	if _, err := ExportV3(image, 7, exportMeta()); err == nil {
		t.Fatal("ExportV3 exported a checksum-valid but structurally invalid v4 image")
	}
}

func TestReaderOperationsNeverPanicOnMalformedLeafCount(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(10, 20, 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	base, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	meta, err := selectActiveMeta(base)
	if err != nil {
		t.Fatal(err)
	}
	root := base[int(meta.rootPgno)*PageSize : int(meta.rootPgno+1)*PageSize]
	putU16(root, PHEntryCount, math.MaxUint16)
	finalizeChecksum(root)

	tests := []struct {
		name string
		run  func(*Reader)
	}{
		{name: "lookup", run: func(r *Reader) { _, _ = r.LookupV4(15) }},
		{name: "scan", run: func(r *Reader) { _ = r.ScanV4(func(_, _ Ipv4Key, _ uint32) {}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Open(base)
			if err != nil {
				return
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("%s panicked on malformed entry_count: %v", tt.name, recovered)
				}
			}()
			tt.run(r)
		})
	}
}
