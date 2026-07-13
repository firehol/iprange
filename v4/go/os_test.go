//go:build unix

package iprangedb

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func tempPath(t *testing.T, tag string) string {
	return filepath.Join(t.TempDir(), "iprange-v4-"+tag+".iprdb")
}

func TestCreateCommitMmapRead(t *testing.T) {
	path := tempPath(t, "ccr")
	{
		fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); i < 1000; i++ {
			must(t, fw.Set(wk(i*10), wk(i*10+3), i&0xff))
		}
		must(t, fw.Commit(0))
		must(t, fw.Close())
	}

	mr, err := OpenMmap(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	r, err := mr.Reader()
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 1000 {
		t.Fatalf("count = %d", r.RecordCount())
	}
	if s, ok := r.LookupV4(wk(5001)); !ok || s != 244 {
		t.Fatalf("lookup 5001 = %d ok=%v", s, ok)
	}
	if _, ok := r.LookupV4(wk(5005)); ok {
		t.Fatal("gap should miss")
	}
}

func TestReopenMutateRecommit(t *testing.T) {
	path := tempPath(t, "rmr")
	{
		fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); i < 400; i++ {
			must(t, fw.Set(wk(i*10), wk(i*10+3), 1))
		}
		must(t, fw.Commit(1))
		must(t, fw.Close())
	}
	{
		fw, err := OpenFile[Ipv4Key](path)
		if err != nil {
			t.Fatal(err)
		}
		if rc := fw.RecordCount(); rc != 400 {
			t.Fatalf("reopen count = %d", rc)
		}
		_, derr := fw.Delete(wk(0), wk(1999))
		must(t, derr)
		must(t, fw.Set(wk(100000), wk(100000), 9))
		must(t, fw.Commit(2))
		must(t, fw.Close())
	}
	mr, err := OpenMmap(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	r, err := mr.Reader()
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 201 {
		t.Fatalf("count = %d, want 201", r.RecordCount())
	}
	if _, ok := r.LookupV4(wk(0)); ok {
		t.Fatal("i=0 should be deleted")
	}
	if s, ok := r.LookupV4(wk(2000)); !ok || s != 1 {
		t.Fatal("i=200 should survive")
	}
	if s, ok := r.LookupV4(wk(100000)); !ok || s != 9 {
		t.Fatal("new record")
	}
}

func TestExclusiveLockIsMutuallyExclusive(t *testing.T) {
	path := tempPath(t, "lock")
	fw1, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A second writer cannot acquire LOCK_EX while the first holds it
	// (active API fails immediately with LOCK_EX|LOCK_NB).
	if _, err := OpenFile[Ipv4Key](path); err == nil {
		t.Fatal("second writer must not acquire the exclusive lock")
	}
	must(t, fw1.Close())
	fw2, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal("second writer should succeed after release:", err)
	}
	must(t, fw2.Close())
}

func TestMmapRejectsSymlinkFinalComponent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sym-target.iprdb")
	fw, err := CreateFile[Ipv4Key](target, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	must(t, fw.Close())
	link := filepath.Join(dir, "sym-link.iprdb")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// O_NOFOLLOW refuses the symlink at the final component.
	if _, err := OpenMmap(link); err == nil {
		t.Fatal("expected rejection of a symlink final component")
	}
}

func TestMmapRejectsTooShort(t *testing.T) {
	path := tempPath(t, "short")
	if err := os.WriteFile(path, []byte("not a v4 file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenMmap(path); err == nil {
		t.Fatal("expected rejection of too-short file")
	}
}

// TestMmapRejectsNonRegularFile points OpenMmap at a directory. O_RDONLY opens
// the directory, but the mmap on a non-regular file must fail before any bogus
// read or SIGBUS.
func TestMmapRejectsNonRegularFile(t *testing.T) {
	if _, err := OpenMmap(t.TempDir()); err == nil {
		t.Fatal("expected rejection of a non-regular file (directory)")
	}
}

func TestMmapWriterNoFullFileHeapCopy(t *testing.T) {
	path := tempPath(t, "noheap")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), uint32(i&0xff)); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	fw2, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	// The store must NOT be a vecPageStore (which would hold the full file in heap).
	if _, ok := fw2.w.store.(*vecPageStore); ok {
		t.Fatal("mmap-backed writer must not use vecPageStore")
	}
	if rc2 := fw2.RecordCount(); rc2 != 5000 {
		t.Fatalf("expected 5000 records, got %d", rc2)
	}
	_ = fw2.Close()
}

func TestMmapWriterGrowthAndRemap(t *testing.T) {
	path := tempPath(t, "growth")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Verify the store is mmap-backed even after create.
	if _, ok := fw.w.store.(*vecPageStore); ok {
		t.Fatal("must be mmap-backed after create")
	}
	// Initial mapping is (committedPages + growthChunk) pages = (2 + 64) = 66.
	initialMapped := len(fw.store.data) / PageSize
	if initialMapped != 66 {
		t.Fatalf("initial mapping = %d pages, want 66", initialMapped)
	}

	// First txn: insert enough to grow past the initial mapping and force a remap.
	// LeafMax for IPv4 = (4096-16)/12 = 340 records/page. We need > 64 leaf pages
	// to exceed the growth chunk: 340 * 66 > 22400 records.
	const n1 = 25000
	for i := 0; i < n1; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), uint32(i&0xff)); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	pagesAfterFirst := fw.w.store.totalPages()
	mappedAfterFirst := len(fw.store.data)
	if pagesAfterFirst <= 2 {
		t.Fatal("file must have grown past meta pages")
	}

	// Second txn: insert more, forcing further growth and remap.
	const n2 = 25000
	for i := n1; i < n1+n2; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), uint32(i&0xff)); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(1); err != nil {
		t.Fatal(err)
	}
	pagesAfterSecond := fw.w.store.totalPages()
	mappedAfterSecond := len(fw.store.data)
	if pagesAfterSecond <= pagesAfterFirst {
		t.Fatal("file must have grown further")
	}
	if mappedAfterSecond <= mappedAfterFirst {
		t.Fatal("mmap must have grown after second commit (remap happened)")
	}
	if rc3 := fw.RecordCount(); rc3 != uint64(n1+n2) {
		t.Fatalf("expected %d records, got %d", n1+n2, rc3)
	}
	_ = fw.Close()

	// Reopen and verify through MmapReader.
	mr, err := OpenMmap(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := mr.Reader()
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != uint64(n1+n2) {
		t.Fatalf("expected %d records after reopen, got %d", n1+n2, r.RecordCount())
	}
	_ = mr.Close()
}

func TestMmapWriterReuseFreedPages(t *testing.T) {
	path := tempPath(t, "reuse")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), uint32(i&0xff)); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}

	// Delete half (frees pages into freedThisTxn, reclaimed at commit 2).
	for i := 0; i < 1000; i++ {
		if _, err := fw.Delete(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3))); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(1); err != nil {
		t.Fatal(err)
	}
	pagesAfterDelete := fw.w.store.totalPages()

	// Reinsert — should reuse pages freed at commit 1.
	for i := 0; i < 1000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10+50000)), Ipv4Key(uint32(i*10+3+50000)), 5); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(2); err != nil {
		t.Fatal(err)
	}
	pagesAfterReinsert := fw.w.store.totalPages()
	if pagesAfterReinsert > pagesAfterDelete+10 {
		t.Fatalf("freed pages must be reused: %d -> %d", pagesAfterDelete, pagesAfterReinsert)
	}
	_ = fw.Close()
}

func TestMmapWriterCloseReleasesLock(t *testing.T) {
	path := tempPath(t, "close-lock")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	// Now should be able to open (lock was released by Close).
	fw2, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	_ = fw2.Close()
}

func TestMmapWriterOpsFailAfterClose(t *testing.T) {
	path := tempPath(t, "ops-after-close")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), 1); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	// Operations after Close must fail (panic due to nilled mmap or readerTable).
	panicked := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s after Close must fail", name)
			}
		}()
		f()
	}
	panicked("Set", func() { _ = fw.Set(Ipv4Key(20), Ipv4Key(30), 2) })
	panicked("Commit", func() { _ = fw.Commit(1) })
}

// TestMmapWriterRepairsTrailingSparsePages verifies that a file extended with
// sparse pages (simulating a crashed growth) is handled correctly: the writer
// opens, does a small commit, and a reader can open the result.
func TestMmapWriterRepairsTrailingSparsePages(t *testing.T) {
	path := tempPath(t, "trailing-sparse")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), uint32(i&0xff)); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	// Extend the file with sparse pages (simulate a crashed growth).
	committedPages := fw.w.store.totalPages()
	sparseLen := int64(committedPages) * int64(PageSize) * 2
	if err := syscall.Ftruncate(int(fw.file.Fd()), sparseLen); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	// Open with writer, do a small commit.
	fw2, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw2.Set(Ipv4Key(9999), Ipv4Key(9999), 99); err != nil {
		t.Fatal(err)
	}
	if err := fw2.Commit(1); err != nil {
		t.Fatal(err)
	}
	_ = fw2.Close()

	// After commit, the reader can open.
	mr, err := OpenMmap(path)
	if err != nil {
		t.Fatal("reader must be able to open after commit repair: " + err.Error())
	}
	r, err := mr.Reader()
	if err != nil {
		t.Fatal(err)
	}
	if r.RecordCount() != 101 {
		t.Fatalf("expected 101 records, got %d", r.RecordCount())
	}
	_ = mr.Close()
}
