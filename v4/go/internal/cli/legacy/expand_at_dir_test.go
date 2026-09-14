//go:build unix

package legacy

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A directory input is classified with stat(), which follows symlinks,
// exactly like the C reference (src/iprange.h FILE_LIST: stat +
// S_ISREG) and Rust legacy/parse.rs (fs::metadata). A symlink whose
// target is a regular file is therefore a file to load; only a
// non-regular target stays skipped.
func TestExpandAtDirectoryFollowsSymlinksToRegularFiles(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "b.regular.txt")
	if err := os.WriteFile(regular, []byte("192.0.2.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := filepath.Join(dir, "a.direct.txt")
	if err := os.WriteFile(direct, []byte("192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(regular, filepath.Join(dir, "c.link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	o := &Options{Family: V4}
	resolver := NewResolver(1, true, false, V4, false)
	var lastSource string
	var dnsUsed bool
	sets, err := expandAt(o, resolver, dir, &lastSource, &dnsUsed)
	if err != nil {
		t.Fatalf("expandAt(directory) = %v, want the symlinked regular file loaded too", err)
	}
	if len(sets) != 3 {
		t.Fatalf("expandAt loaded %d files, want 3 (two regular plus one symlink to a regular file)", len(sets))
	}
}

// A directory whose only entries are symlinks to regular files is a
// valid input: the C reference exits 0 for it, so the expansion must not
// report "no valid files found".
func TestExpandAtDirectoryOfSymlinksSucceeds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(target, []byte("198.51.100.0/24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.link", "two.link"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	o := &Options{Family: V4}
	resolver := NewResolver(1, true, false, V4, false)
	var lastSource string
	var dnsUsed bool
	sets, err := expandAt(o, resolver, dir, &lastSource, &dnsUsed)
	if err != nil {
		t.Fatalf("expandAt(all-symlink directory) = %v, want success like the C reference", err)
	}
	if len(sets) != 2 {
		t.Fatalf("expandAt loaded %d files, want 2", len(sets))
	}
}

// Non-regular targets keep being skipped: a fifo entry contributes no
// file, and a directory that holds only non-regular entries still
// reports the empty-directory failure rather than blocking on the fifo.
func TestExpandAtDirectorySkipsNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "entry.fifo"), 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	o := &Options{Family: V4}
	resolver := NewResolver(1, true, false, V4, false)
	var lastSource string
	var dnsUsed bool
	if _, err := expandAt(o, resolver, dir, &lastSource, &dnsUsed); err == nil {
		t.Fatal("expandAt accepted a directory with no regular file")
	}
}
