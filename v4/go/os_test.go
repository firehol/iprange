//go:build unix

package iprangedb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func tempPath(t *testing.T, tag string) string {
	return filepath.Join(t.TempDir(), "iprange-v4-"+tag+".iprdb")
}

func TestCreateCommitMmapRead(t *testing.T) {
	path := tempPath(t, "ccr")
	{
		fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); i < 1000; i++ {
			must(t, fw.Set(wk(i*10), wk(i*10+3), []byte{byte(i & 0xff)}))
		}
		must(t, fw.Commit(0))
		must(t, fw.Close()) // release LOCK_EX
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
	if s, ok, _ := r.LookupV4(wk(5001)); !ok || s[0] != 244 { // i=500 -> 5000..5003
		t.Fatalf("lookup 5001 = %v ok=%v", s, ok)
	}
	if _, ok, _ := r.LookupV4(wk(5005)); ok {
		t.Fatal("gap should miss")
	}
}

func TestReopenMutateRecommit(t *testing.T) {
	path := tempPath(t, "rmr")
	{
		fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
		if err != nil {
			t.Fatal(err)
		}
		// Non-adjacent ranges (gaps) so same-scope records don't coalesce.
		for i := uint32(0); i < 400; i++ {
			must(t, fw.Set(wk(i*10), wk(i*10+3), []byte{1}))
		}
		must(t, fw.Commit(1))
		must(t, fw.Close())
	}
	{
		fw, err := OpenFileWriterV4(path, DefaultLockWait)
		if err != nil {
			t.Fatal(err)
		}
		rc, err := fw.RecordCount()
		if err != nil {
			t.Fatal(err)
		}
		if rc != 400 {
			t.Fatalf("reopen count = %d", rc)
		}
		must(t, fw.Delete(wk(0), wk(1999))) // removes i = 0..=199 (200 records)
		must(t, fw.Set(wk(100000), wk(100000), []byte{9}))
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
	if r.RecordCount() != 201 { // 200 survivors + 1 new
		t.Fatalf("count = %d, want 201", r.RecordCount())
	}
	if _, ok, _ := r.LookupV4(wk(0)); ok {
		t.Fatal("i=0 should be deleted")
	}
	if s, ok, _ := r.LookupV4(wk(2000)); !ok || s[0] != 1 {
		t.Fatal("i=200 should survive")
	}
	if s, ok, _ := r.LookupV4(wk(100000)); !ok || s[0] != 9 {
		t.Fatal("new record")
	}
}

func TestCreateCommitMetadataReopen(t *testing.T) {
	// The file-backed commit must persist the scope-table + KV pages that are BUILT at commit
	// time. Regression (codex Finding 1): the metadata rebuild ran AFTER the dirty set was
	// drained, so those pages were never pwritten and the reopened file referenced unwritten
	// pages → corruption. Covers a defined-scope KV (text + overflow-spanning binary) and a
	// FILE-target KV, alongside real IP data.
	path := tempPath(t, "meta")
	big := bytes.Repeat([]byte{0xAB}, 9000) // multi-page overflow value
	{
		fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); i < 50; i++ {
			must(t, fw.Set(wk(i*10), wk(i*10+3), []byte{7}))
		}
		a, err := fw.ScopeDefine([]byte("scope-a"))
		must(t, err)
		b, err := fw.ScopeDefine([]byte("scope-b"))
		must(t, err)
		if a != 1 || b != 2 {
			t.Fatalf("scope ids = %d,%d, want 1,2", a, b)
		}
		if _, err := fw.ScopeSetType(a, 5); err != nil {
			t.Fatal(err)
		}
		if _, err := fw.ScopeBumpVersion(b); err != nil {
			t.Fatal(err)
		}
		must(t, fw.MetaSet(a, []byte("license"), 0, []byte("MIT")))
		must(t, fw.MetaSet(a, []byte("blob"), 9, big)) // overflow-spanning binary value
		must(t, fw.MetaSet(fileScopeID, []byte("dataset"), 0, []byte("firehol")))
		must(t, fw.Commit(1))
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
	if r.RecordCount() != 50 {
		t.Fatalf("count = %d", r.RecordCount())
	}
	if len(r.ScopeList()) != 2 {
		t.Fatalf("scope list len = %d", len(r.ScopeList()))
	}
	if name, ok := r.ScopeName(1); !ok || string(name) != "scope-a" {
		t.Fatalf("scope 1 name = %q ok=%v", name, ok)
	}
	if ty, ok := r.ScopeType(1); !ok || ty != 5 {
		t.Fatalf("scope 1 type = %d ok=%v", ty, ok)
	}
	if v, ok := r.ScopeVersion(2); !ok || v != 1 {
		t.Fatalf("scope 2 version = %d ok=%v", v, ok)
	}
	if ty, v, ok, _ := r.MetaGet(1, []byte("license")); !ok || ty != 0 || string(v) != "MIT" {
		t.Fatalf("meta(1,license) = (%d,%q,%v)", ty, v, ok)
	}
	if ty, v, ok, _ := r.MetaGet(1, []byte("blob")); !ok || ty != 9 || !bytes.Equal(v, big) {
		t.Fatalf("meta(1,blob) ok=%v ty=%d len=%d", ok, ty, len(v))
	}
	if ty, v, ok, _ := r.MetaGet(fileScopeID, []byte("dataset")); !ok || ty != 0 || string(v) != "firehol" {
		t.Fatalf("meta(FILE,dataset) = (%d,%q,%v)", ty, v, ok)
	}
}

func TestReopenMutateMetadataRecommit(t *testing.T) {
	// Incremental metadata mutation on an EXISTING file (open → MetaSet → Commit) must rebuild
	// and persist the changed pages across the two-fsync commit.
	path := tempPath(t, "meta2")
	{
		fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
		must(t, err)
		a, err := fw.ScopeDefine([]byte("a"))
		must(t, err)
		if a != 1 {
			t.Fatalf("scope id = %d, want 1", a)
		}
		must(t, fw.MetaSet(a, []byte("k"), 0, []byte("v1")))
		must(t, fw.Commit(1))
		must(t, fw.Close())
	}
	{
		fw, err := OpenFileWriterV4(path, DefaultLockWait)
		must(t, err)
		if ty, v, ok, _ := fw.MetaGet(1, []byte("k")); !ok || ty != 0 || string(v) != "v1" {
			t.Fatalf("meta(1,k) = (%d,%q,%v)", ty, v, ok)
		}
		must(t, fw.MetaSet(1, []byte("k"), 0, []byte("v2")))
		must(t, fw.MetaSet(1, []byte("k2"), 0, []byte("x")))
		must(t, fw.Commit(2))
		must(t, fw.Close())
	}
	mr, err := OpenMmap(path)
	must(t, err)
	defer mr.Close()
	r, err := mr.Reader()
	must(t, err)
	if _, v, ok, _ := r.MetaGet(1, []byte("k")); !ok || string(v) != "v2" {
		t.Fatalf("meta(1,k) after update = %q ok=%v", v, ok)
	}
	if _, v, ok, _ := r.MetaGet(1, []byte("k2")); !ok || string(v) != "x" {
		t.Fatalf("meta(1,k2) = %q ok=%v", v, ok)
	}
}

func TestExclusiveLockIsMutuallyExclusive(t *testing.T) {
	path := tempPath(t, "lock")
	fw1, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	// A second writer cannot acquire LOCK_EX while the first holds it.
	if _, err := OpenFileWriterV4(path, 150*time.Millisecond); err == nil {
		t.Fatal("second writer must not acquire the exclusive lock")
	}
	must(t, fw1.Close()) // release
	fw2, err := OpenFileWriterV4(path, DefaultLockWait)
	if err != nil {
		t.Fatal("second writer should succeed after release:", err)
	}
	must(t, fw2.Close())
}

func TestMmapRejectsSymlinkFinalComponent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sym-target.iprdb")
	fw, err := CreateFileWriterV4(target, 1, 0, DefaultLockWait)
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
	if _, err := OpenMmap(path); errorClass(err) != "FileTooShort" {
		t.Fatalf("expected FileTooShort, got %v", err)
	}
}

// TestMmapRejectsNonRegularFile points OpenMmap at a directory: it opens (O_RDONLY) and locks,
// but the fstat S_IFMT check rejects a non-regular file before mapping (§10), never SIGBUS or a
// bogus read. (The writer's openFileWriter has the same guard, but O_RDWR on a directory fails
// with EISDIR first, so a directory cannot exercise that path; this MmapReader test covers the
// "not a regular file" Structural reject.)
func TestMmapRejectsNonRegularFile(t *testing.T) {
	if _, err := OpenMmap(t.TempDir()); errorClass(err) != "Structural" {
		t.Fatalf("expected Structural for a directory, got %v", err)
	}
}

func TestMmapWriterNoFullFileHeapCopy(t *testing.T) {
	// Prove that openFileWriter uses mmapPageStore (not a full-file []byte copy).
	path := tempPath(t, "noheap")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), []byte{byte(i & 0xff)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	fw2, err := OpenFileWriterV4(path, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	// Verify the store is NOT a vecPageStore (which would hold the full file in heap).
	if _, ok := fw2.w.store.(*vecPageStore); ok {
		t.Fatal("mmap-backed writer must not use vecPageStore")
	}
	rc2, err := fw2.RecordCount()
	if err != nil {
		t.Fatal(err)
	}
	if rc2 != 5000 {
		t.Fatalf("expected 5000 records, got %d", rc2)
	}
	_ = fw2.Close()
}

func TestMmapWriterGrowthAndRemap(t *testing.T) {
	// Verify that the mmap-backed writer correctly grows the file and remaps.
	// Both create and open are now mmap-backed, so we can start with create
	// and grow directly.
	path := tempPath(t, "growth")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	// Verify the store is mmap-backed even after create.
	if _, ok := fw.w.store.(*vecPageStore); ok {
		t.Fatal("must be mmap-backed after create")
	}
	mmapLenBefore := fw.mmapLen
	if mmapLenBefore != int64(2*pageSize) {
		t.Fatalf("initial file must be 2 meta pages, got mmapLen=%d", mmapLenBefore)
	}

	// First txn: insert enough to grow past the initial 2 pages.
	for i := 0; i < 2000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), []byte{byte(i & 0xff)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	pagesAfterFirst := fw.w.store.totalPages()
	mmapLenAfterFirst := fw.mmapLen
	if pagesAfterFirst <= 2 {
		t.Fatal("file must have grown past meta pages")
	}
	if mmapLenAfterFirst <= mmapLenBefore {
		t.Fatal("mmapLen must have increased after first commit (remap happened)")
	}

	// Second txn: insert more, forcing further growth and remap.
	for i := 2000; i < 4000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), []byte{byte(i & 0xff)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(1); err != nil {
		t.Fatal(err)
	}
	pagesAfterSecond := fw.w.store.totalPages()
	mmapLenAfterSecond := fw.mmapLen
	if pagesAfterSecond <= pagesAfterFirst {
		t.Fatal("file must have grown further")
	}
	if mmapLenAfterSecond <= mmapLenAfterFirst {
		t.Fatal("mmapLen must have increased after second commit (remap happened)")
	}
	rc3, err := fw.RecordCount()
	if err != nil {
		t.Fatal(err)
	}
	if rc3 != 4000 {
		t.Fatalf("expected 4000 records, got %d", rc3)
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
	if r.RecordCount() != 4000 {
		t.Fatalf("expected 4000 records after reopen, got %d", r.RecordCount())
	}
	_ = mr.Close()
}

func TestMmapWriterReuseFreedPages(t *testing.T) {
	// Verify that the mmap-backed writer reuses freed pages (D7).
	path := tempPath(t, "reuse")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), []byte{byte(i & 0xff)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}

	// Delete half (frees pages into freedThisTxn, reclaimed at commit 2).
	for i := 0; i < 1000; i++ {
		if err := fw.Delete(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3))); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(1); err != nil {
		t.Fatal(err)
	}
	pagesAfterDelete := fw.w.store.totalPages()

	// Reinsert — should reuse pages freed at commit 1.
	for i := 0; i < 1000; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10+50000)), Ipv4Key(uint32(i*10+3+50000)), []byte{5}); err != nil {
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
	// Regression: Close() must release LOCK_EX so another writer can open.
	path := tempPath(t, "close-lock")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	// Now should be able to open (lock was released by Close).
	fw2, err := OpenFileWriterV4(path, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	_ = fw2.Close()
}

func TestMmapWriterRejectsTotalPagesOverflow(t *testing.T) {
	// Regression: a file with total_pages > 2^32 must be rejected.
	path := tempPath(t, "overflow")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	// Patch the active meta's total_pages to exceed 2^32.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m0 := decodeMeta(data[:pageSize])
	m1 := decodeMeta(data[pageSize:])
	active := 0
	if m1.txnID > m0.txnID {
		active = 1
	}
	base := active * pageSize
	// total_pages is at offset 48 in the meta (u64 little-endian).
	le.PutUint64(data[base+48:base+56], 1<<33)
	// Re-checksum the page so selectActiveMeta accepts it.
	finalizeChecksum(data[base : base+pageSize])
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenFileWriterV4(path, DefaultLockWait); err == nil {
		t.Fatal("writer must reject total_pages > 2^32")
	}
}

func TestMmapWriterRejectsCorruptMeta(t *testing.T) {
	// Verify that openFileWriter rejects a file with both metas corrupt.
	path := tempPath(t, "corrupt-meta")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	// Corrupt both meta pages.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[64] ^= 0xFF
	data[pageSize+64] ^= 0xFF
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenFileWriterV4(path, DefaultLockWait); err == nil {
		t.Fatal("writer must reject file with both metas corrupt")
	}
}

func TestMmapWriterRejectsTotalPagesZero(t *testing.T) {
	// Regression: writer-open must reject total_pages < 2 before any mutation.
	path := tempPath(t, "tp-zero")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	// Patch the active meta's total_pages to 0, re-checksum.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m0 := decodeMeta(data[:pageSize])
	m1 := decodeMeta(data[pageSize:])
	active := 0
	if m1.txnID > m0.txnID {
		active = 1
	}
	base := active * pageSize
	le.PutUint64(data[base+48:base+56], 0)
	finalizeChecksum(data[base : base+pageSize])
	fileLen := len(data)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenFileWriterV4(path, DefaultLockWait); err == nil {
		t.Fatal("writer must reject total_pages == 0")
	}
	// Verify the file was NOT truncated.
	if fi, _ := os.Stat(path); int(fi.Size()) != fileLen {
		t.Fatalf("writer must not truncate file on reject: %d -> %d", fileLen, fi.Size())
	}
}

func TestMmapWriterRejectsTotalPagesEq2Pow32(t *testing.T) {
	// Regression: writer-open must reject total_pages >= 2^32 (wraps u32).
	path := tempPath(t, "tp-2pow32")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m0 := decodeMeta(data[:pageSize])
	m1 := decodeMeta(data[pageSize:])
	active := 0
	if m1.txnID > m0.txnID {
		active = 1
	}
	base := active * pageSize
	le.PutUint64(data[base+48:base+56], 1<<32)
	finalizeChecksum(data[base : base+pageSize])
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenFileWriterV4(path, DefaultLockWait); err == nil {
		t.Fatal("writer must reject total_pages == 2^32")
	}
}

func TestMmapWriterOpsFailAfterClose(t *testing.T) {
	// Regression: Set/Commit after Close() must fail (no LOCK_EX).
	path := tempPath(t, "ops-after-close")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	// Operations after Close must fail.
	if err := fw.Set(Ipv4Key(20), Ipv4Key(30), []byte{2}); err == nil {
		t.Fatal("Set after Close must fail")
	}
	if err := fw.Commit(1); err == nil {
		t.Fatal("Commit after Close must fail")
	}
}

func TestMmapWriterPageAfterCloseReturnsZero(t *testing.T) {
	// Regression: page() after Close() must return zero page, not panic.
	path := tempPath(t, "page-after-close")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	// Reopen with mmap-backed writer, close, then check page() returns zero.
	fw2, err := OpenFileWriterV4(path, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw2.Close(); err != nil {
		t.Fatal(err)
	}
	page := fw2.w.store.page(0)
	if len(page) != pageSize {
		t.Fatalf("page length %d != %d", len(page), pageSize)
	}
	for i, b := range page {
		if b != 0 {
			t.Fatalf("page[%d] = %d, expected 0 after close", i, b)
		}
	}
}

func TestMmapWriterRepairsTrailingSparsePages(t *testing.T) {
	// Regression: a file with trailing sparse pages (from a crashed growth) must
	// be repaired after commit so readers can open it.
	path := tempPath(t, "trailing-sparse")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := fw.Set(Ipv4Key(uint32(i*10)), Ipv4Key(uint32(i*10+3)), []byte{byte(i & 0xff)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	// Extend the file with sparse pages (simulate a crashed growth).
	sparseLen := int64(fw.w.store.totalPages()*uint64(pageSize)) * 2
	if err := unix.Ftruncate(int(fw.file.Fd()), sparseLen); err != nil {
		t.Fatal(err)
	}
	_ = fw.Close()

	// A reader must reject the sparse file.
	if _, err := OpenMmap(path); err == nil {
		t.Fatal("reader must reject sparse file")
	}

	// Open with writer (mmap-backed), do a small commit.
	fw2, err := OpenFileWriterV4(path, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw2.Set(Ipv4Key(9999), Ipv4Key(9999), []byte{99}); err != nil {
		t.Fatal(err)
	}
	if err := fw2.Commit(1); err != nil {
		t.Fatal(err)
	}
	_ = fw2.Close()

	// After commit, the trailing sparse pages must be gone — reader can open.
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

func TestMmapWriterScopeMetaFailAfterClose(t *testing.T) {
	// Regression: scope/meta mutators must fail after Close().
	path := tempPath(t, "scope-after-close")
	fw, err := CreateFileWriterV4(path, 1, 0, DefaultLockWait)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(10), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(0); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := fw.ScopeDefine([]byte("x")); err == nil {
		t.Fatal("ScopeDefine after Close must fail")
	}
	if _, err := fw.ScopeSetVersion(1, 1); err == nil {
		t.Fatal("ScopeSetVersion after Close must fail")
	}
	if _, err := fw.ScopeBumpVersion(1); err == nil {
		t.Fatal("ScopeBumpVersion after Close must fail")
	}
	if _, err := fw.ScopeSetType(1, 1); err == nil {
		t.Fatal("ScopeSetType after Close must fail")
	}
	if err := fw.MetaSet(0, []byte("k"), 0, []byte("v")); err == nil {
		t.Fatal("MetaSet after Close must fail")
	}
	if _, err := fw.MetaDelete(0, []byte("k")); err == nil {
		t.Fatal("MetaDelete after Close must fail")
	}
	// Read methods must also fail after close (mmap is unmapped).
	if _, _, _, err := fw.MetaGet(0, []byte("k")); err == nil {
		t.Fatal("MetaGet after Close must fail")
	}
	if _, err := fw.MetaList(0); err == nil {
		t.Fatal("MetaList after Close must fail")
	}
	if _, err := fw.RecordCount(); err == nil {
		t.Fatal("RecordCount after Close must fail")
	}
	if _, err := fw.ScopeDrop(999); err == nil {
		t.Fatal("ScopeDrop after Close must fail")
	}
}
