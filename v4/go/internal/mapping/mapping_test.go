//go:build linux

package mapping

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestOpenImmutable acquires the shared OFD lifetime lock and holds it for
// the mapping lifetime: an exclusive contender on a second descriptor of
// the same file must fail with EWOULDBLOCK while the mapping is open, and
// succeed after Close releases the lock.
func TestOpenImmutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.iprdb")
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := OpenImmutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}

	tryExclusive := func() (bool, error) {
		// A write lock requires an fd opened for writing.
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return false, err
		}
		defer f.Close()
		fl := unix.Flock_t{Type: unix.F_WRLCK, Whence: unix.SEEK_SET, Start: lifetimeLockOffset, Len: lifetimeLockLen}
		err = unix.FcntlFlock(f.Fd(), unix.F_OFD_SETLK, &fl)
		if err == nil {
			return true, nil
		}
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return false, nil
		}
		return false, err
	}

	// The shared lock must exclude an exclusive contender at the lifetime
	// offset, but the rest of the file stays free (the lock is a byte range).
	if held, err := tryExclusive(); err != nil || held {
		t.Fatalf("exclusive contender while open: held=%v err=%v", held, err)
	}
	if _, err := m.View(0, format.PageSize); err != nil {
		t.Fatal("view:", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	held, err := tryExclusive()
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("exclusive lock not acquired after close")
	}
}

// TestOpenRefusesForeignFile exercises the geometry refusals: a short file,
// an unaligned file, and a symlink must all be refused without mapping.
func TestOpenRefusesForeignFile(t *testing.T) {
	dir := t.TempDir()

	short := filepath.Join(dir, "short.iprdb")
	if err := os.WriteFile(short, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenImmutable(short, nil); err == nil {
		t.Fatal("short file accepted")
	}
	unaligned := filepath.Join(dir, "unaligned.iprdb")
	if err := os.WriteFile(unaligned, make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenImmutable(unaligned, nil); err == nil {
		t.Fatal("unaligned file accepted")
	}
	target := filepath.Join(dir, "target.iprdb")
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.iprdb")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenImmutable(link, nil); err == nil {
		t.Fatal("symlink accepted")
	}
}
