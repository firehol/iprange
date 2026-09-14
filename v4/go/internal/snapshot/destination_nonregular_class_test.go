//go:build !windows

package snapshot

// Refusal-class pins for the destination probe of the live snapshot
// self-replacement check (Rust publication::namespace::unix
// Directory::open_regular through Destination::bind). The destination
// is classified by the open of the name, never by a path stat, and the
// order is what decides the class: a node the open itself refuses
// (AF_UNIX socket, ENXIO) is a filesystem failure, while a node the open
// returns and the descriptor then fails (directory, FIFO, symlink under
// O_NOFOLLOW) is the namespace non-regular class. A pre-open stat would
// report every one of them as the Conflict class and lose the io answer
// the reference implementation gives for the socket.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// nonRegularDestination runs the destination probe against one name the
// test materializes, returning the class it reported.
func nonRegularDestination(t *testing.T, materialize func(t *testing.T, path string)) *format.Error {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "archive.iprange")
	materialize(t, target)
	err := rejectLiveSelf(probeSource{device: 1, inode: 1}, SourceLive, target,
		publication.PolicyReplaceExisting)
	if err == nil {
		t.Fatalf("%s destination was accepted", filepath.Base(target))
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	return fe
}

// An AF_UNIX socket destination is refused by the open itself, so the
// answer is the io class (Rust NamespaceError::IoAt of
// open_regular_with_links, folded by Problem::namespace).
func TestRejectLiveSelfSocketDestinationIsIo(t *testing.T) {
	fe := nonRegularDestination(t, bindUnixSocket)
	if fe.Code != format.CodeIO {
		t.Fatalf("code = %v (%v), want %v", fe.Code, fe.Detail, format.CodeIO)
	}
}

// The genuine name-collision refusals must stay the Conflict class: a
// directory or FIFO opens and the descriptor is then refused as not a
// regular file, and a symlink is refused for the link under O_NOFOLLOW
// (Rust is_nofollow_symlink folds it into the same NotRegular arm).
func TestRejectLiveSelfCollisionDestinationsStayConflict(t *testing.T) {
	materializeDirectory := func(t *testing.T, path string) {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	materializeFifo := func(t *testing.T, path string) {
		if err := syscallMkfifo(path); err != nil {
			t.Skipf("FIFOs are unavailable: %v", err)
		}
	}
	materializeSymlink := func(t *testing.T, path string) {
		real := filepath.Join(filepath.Dir(path), "elsewhere.iprange")
		if err := os.WriteFile(real, make([]byte, 512), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, path); err != nil {
			t.Skipf("symlinks are unavailable: %v", err)
		}
	}
	for name, materialize := range map[string]func(t *testing.T, path string){
		"directory": materializeDirectory,
		"fifo":      materializeFifo,
		"symlink":   materializeSymlink,
	} {
		fe := nonRegularDestination(t, materialize)
		if fe.Code != format.CodeConflict {
			t.Errorf("%s destination: code = %v (%v), want %v", name, fe.Code, fe.Detail, format.CodeConflict)
		}
	}
}

// A missing destination is not a rejection of this probe at all: the
// attempt creation reports it with the exact publication class (Rust
// Directory::open_regular reports Ok(None)).
func TestRejectLiveSelfMissingDestinationIsNotARejection(t *testing.T) {
	dir := t.TempDir()
	if err := rejectLiveSelf(probeSource{device: 1, inode: 1}, SourceLive,
		filepath.Join(dir, "absent.iprange"), publication.PolicyReplaceExisting); err != nil {
		t.Fatalf("absent destination reported %v, want acceptance", err)
	}
}
