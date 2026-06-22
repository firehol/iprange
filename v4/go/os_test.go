//go:build unix

package iprangedb

import (
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
