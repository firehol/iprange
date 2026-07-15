package iprangedb

import (
	"math"
	"testing"
)

func round5IndirectFreeListFixture(t *testing.T) ([]byte, meta, uint32, uint32, uint32) {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern(round5Bitmap(MaxBitmapWidth + 1))
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(1); i < 20; i++ {
		if _, err := w.ScopeIntern([]byte{i, 1}); err != nil {
			t.Fatal(err)
		}
	}
	for i := uint32(0); i < 800; i++ {
		if err := w.Set(Ipv4Key(i*2), Ipv4Key(i*2), id); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	for i := uint32(100); i < 300; i++ {
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
	m, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	if m.freeListHead == 0 || m.rootPgno == 0 || m.scopeTableRoot == 0 {
		t.Fatalf("fixture lacks roots: free=%d data=%d scope=%d", m.freeListHead, m.rootPgno, m.scopeTableRoot)
	}
	root := image[int(m.rootPgno)*PageSize : int(m.rootPgno+1)*PageSize]
	h := decodeHeader(root)
	if h.pageType != PageTypeBranch {
		t.Fatalf("fixture data root type=%d, want branch", h.pageType)
	}
	branch := newBranchView(root, int(h.entryCount), int(m.keyWidth))
	dataChild := branch.child(0)
	scopeRoot := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
	scopeHeader := decodeHeader(scopeRoot)
	if scopeHeader.pageType != PageTypeScopeBranch {
		t.Fatalf("fixture scope root type=%d, want branch", scopeHeader.pageType)
	}
	scopeBranch := newBranchView(scopeRoot, int(scopeHeader.entryCount), ScopeKeyWidth)
	scopeChild := scopeBranch.child(0)
	scopeLeaf := image[int(scopeChild)*PageSize : int(scopeChild+1)*PageSize]
	if decodeHeader(scopeLeaf).pageType != PageTypeScopeLeaf {
		t.Fatalf("fixture scope child type=%d, want leaf", decodeHeader(scopeLeaf).pageType)
	}
	overflow := u32le(scopeLeaf, PageHeaderSize+10)
	if overflow == 0 {
		t.Fatal("fixture has no scope overflow page")
	}
	return image, m, dataChild, scopeChild, overflow
}

func TestRound5FreeListRejectsEveryReachablePageClass(t *testing.T) {
	base, m, dataChild, scopeChild, overflow := round5IndirectFreeListFixture(t)
	tests := []struct {
		name string
		pgno uint32
	}{
		{name: "data-root", pgno: m.rootPgno},
		{name: "data-child", pgno: dataChild},
		{name: "scope-root", pgno: m.scopeTableRoot},
		{name: "scope-child", pgno: scopeChild},
		{name: "scope-overflow", pgno: overflow},
		{name: "free-list-chain", pgno: m.freeListHead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			image := append([]byte(nil), base...)
			freePage := image[int(m.freeListHead)*PageSize : int(m.freeListHead+1)*PageSize]
			if u32le(freePage, TxnFreeCount) == 0 {
				t.Fatal("fixture free-list page has no entries")
			}
			putU32(freePage, TxnFreeArray, tt.pgno)
			finalizeChecksum(freePage)
			if writer, err := openWriter[Ipv4Key](newVecPageStore(image)); err == nil {
				_ = writer
				t.Fatalf("writable open accepted reachable %s page %d as authoritative free state", tt.name, tt.pgno)
			}
		})
	}
}
