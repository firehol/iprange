package mapping

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Publication-namespace primitive tests (Rust publication namespace
// tests): no-replace refusal, atomic exchange, directory sync, and the
// device+inode identity probe.

func TestRenameNoReplaceRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	dst := filepath.Join(dir, "b")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RenameNoReplace(src, dst)
	if err == nil {
		t.Fatal("rename_noreplace over an existing destination succeeded")
	}
	got, readErr := os.ReadFile(dst)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "old" {
		t.Fatalf("destination changed to %q, want untouched %q", got, "old")
	}
	if _, statErr := os.Stat(src); statErr != nil {
		t.Fatalf("source disappeared after refused rename: %v", statErr)
	}
}

func TestRenameExchangeSwaps(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RenameExchange(a, b); err != nil {
		t.Fatal(err)
	}
	gotB, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	gotA, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotB) != "A" || string(gotA) != "B" {
		t.Fatalf("exchange produced a=%q b=%q, want a=B b=A", gotA, gotB)
	}
}

func TestSyncDirectoryAndStatIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := SyncDirectory(dir); err != nil {
		t.Fatalf("sync directory: %v", err)
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d1, i1, err := StatIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	d2, i2, err := StatIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 || i1 != i2 {
		t.Fatalf("directory identity changed: (%d,%d) vs (%d,%d)", d1, i1, d2, i2)
	}
	if _, _, err := StatIdentity(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("stat of a missing path succeeded")
	} else if e, ok := err.(*format.Error); !ok || e.Code != format.CodeIO {
		t.Fatalf("missing-path error = %v, want CodeIO", err)
	}
}
