//go:build linux

package mapping

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestViewAfterClose pins the owner-level refusal: any view access after
// Close reports the typed wrong-state error instead of panicking on the
// released mapping (the Windows stub already refuses; the POSIX owner must
// match).
func TestViewAfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "closed.iprdb")
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
	if _, err := m.View(0, format.PageSize); err != nil {
		t.Fatalf("view before close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal("close:", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal("double close:", err)
	}
	if _, err := m.View(0, format.PageSize); err == nil {
		t.Fatal("view after close accepted")
	} else if fe, ok := err.(*format.Error); !ok || fe.Code != format.CodeWrongState {
		t.Fatalf("view after close code %v want WrongState", err)
	}
	if _, err := m.Page(2); err == nil {
		t.Fatal("page after close accepted")
	}
}

// TestOpenImmutableWaitsForExclusiveLifetimeLock pins the blocking
// F_OFD_SETLKW wait semantics of the shared lifetime lock: an immutable
// open must block behind an exclusive contender at the lifetime offset,
// not fail immediately with EWOULDBLOCK (regression: non-blocking F_OFD_SETLK).
func TestOpenImmutableWaitsForExclusiveLifetimeLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "waits.iprdb")
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Hold the exclusive OFD write lock at the lifetime offset on a
	// separate descriptor (write locks require an fd opened for writing).
	holder, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	excl := unix.Flock_t{Type: unix.F_WRLCK, Whence: unix.SEEK_SET, Start: lifetimeLockOffset, Len: lifetimeLockLen}
	if err := unix.FcntlFlock(holder.Fd(), unix.F_OFD_SETLK, &excl); err != nil {
		t.Fatal("hold exclusive:", err)
	}

	opened := make(chan *Mapping, 1)
	failed := make(chan error, 1)
	go func() {
		m, err := OpenImmutable(path, nil)
		if err != nil {
			failed <- err
			return
		}
		opened <- m
	}()

	// The open must not fail and must not complete while the exclusive
	// lock is held; 250ms is far beyond the non-blocking failure path.
	select {
	case err := <-failed:
		t.Fatalf("open failed while exclusive lock held (non-blocking lock regression): %v", err)
	case m := <-opened:
		m.Close()
		t.Fatal("open completed while exclusive lock held")
	case <-time.After(250 * time.Millisecond):
	}

	// Releasing the exclusive lock lets the blocked open proceed.
	unl := unix.Flock_t{Type: unix.F_UNLCK, Whence: unix.SEEK_SET, Start: lifetimeLockOffset, Len: lifetimeLockLen}
	if err := unix.FcntlFlock(holder.Fd(), unix.F_OFD_SETLK, &unl); err != nil {
		t.Fatal("release exclusive:", err)
	}
	select {
	case m := <-opened:
		m.Close()
	case err := <-failed:
		t.Fatalf("open failed after exclusive lock release: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("open did not complete after exclusive lock release")
	}
}

// TestOpenImmutableRefusesPathReplacedDuringOpen pins the post-lock and
// post-mmap path identity recheck: if the directory entry no longer names
// the opened inode when the open publishes, the open must refuse (Rust
// verify_path_any_link -> WrongMode -> code 11), never map the old unlinked
// inode while the path names a new database.
func TestOpenImmutableRefusesPathReplacedDuringOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replaced.iprdb")
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// A second file with a distinct inode, placed over path while the
	// open is in flight.
	other := filepath.Join(dir, "other.iprdb")
	if err := os.WriteFile(other, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	check := func(clean string) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return nil
	}

	result := make(chan error, 1)
	go func() {
		m, err := OpenImmutable(path, check)
		if m != nil {
			m.Close()
		}
		result <- err
	}()

	// The open has passed the post-lock identity check and is paused
	// inside the check callback with the fd still on the original inode.
	<-entered
	if err := os.Rename(other, path); err != nil {
		t.Fatal("replace path:", err)
	}
	close(release)

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("open accepted a path replaced while the open was in flight")
		}
		fe, ok := err.(*format.Error)
		if !ok || fe.Code != format.CodeWrongState {
			t.Fatalf("replaced-during-open error %v, want WrongState (11)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open did not refuse the replaced path")
	}
}

// TestOpenImmutableRefusesPathUnlinkedDuringOpen pins the deleted-mid-open
// error class: when the directory entry disappears while the open is in
// flight, the open must refuse with NameNotFound (18), mirroring Rust
// verify_path_inner's .ok_or(Error::NameNotFound).
func TestOpenImmutableRefusesPathUnlinkedDuringOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unlinked.iprdb")
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	check := func(clean string) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return nil
	}

	result := make(chan error, 1)
	go func() {
		m, err := OpenImmutable(path, check)
		if m != nil {
			m.Close()
		}
		result <- err
	}()

	<-entered
	if err := os.Remove(path); err != nil {
		t.Fatal("remove path:", err)
	}
	close(release)

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("open accepted a path removed while the open was in flight")
		}
		fe, ok := err.(*format.Error)
		if !ok || fe.Code != format.CodeNameNotFound {
			t.Fatalf("unlinked-during-open error %v, want NameNotFound (18)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open did not refuse the unlinked path")
	}
}
