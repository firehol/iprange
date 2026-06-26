//go:build unix

package iprangedb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
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
		if fw.RecordCount() != 400 {
			t.Fatalf("reopen count = %d", fw.RecordCount())
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
