//go:build linux

package mapping

import (
	"errors"
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
	} else {
		var fe *format.Error
		if !errors.As(err, &fe) || fe.Code != format.CodeWrongState {
			t.Fatalf("view after close code %v want WrongState", err)
		}
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
		var fe *format.Error
		if !errors.As(err, &fe) || fe.Code != format.CodeWrongState {
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
		var fe *format.Error
		if !errors.As(err, &fe) || fe.Code != format.CodeNameNotFound {
			t.Fatalf("unlinked-during-open error %v, want NameNotFound (18)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open did not refuse the unlinked path")
	}
}

// TestOpenMutableWritesVisible pins the writer mapping contract: bytes
// written through a mutable View land in the file-backed mapping (visible to
// an independent mapping of the same file before any flush) and survive
// Flush + SyncFile + close for a fresh immutable open.
func TestOpenMutableWritesVisible(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 4)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	view, err := m.View(0, format.PageSize)
	if err != nil {
		t.Fatal("view:", err)
	}
	copy(view, []byte("writer marker"))

	// MAP_SHARED: an independent raw mapping of the same file sees the
	// write immediately, before any msync.
	raw, err := unix.Mmap(-1, 0, format.PageSize, unix.PROT_READ, unix.MAP_SHARED)
	if err == nil {
		unix.Munmap(raw) // placeholder; replaced below
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = unix.Mmap(int(f.Fd()), 0, format.PageSize, unix.PROT_READ, unix.MAP_SHARED)
	f.Close()
	if err != nil {
		t.Fatal("raw mmap:", err)
	}
	if string(raw[:13]) != "writer marker" {
		t.Fatalf("independent mapping sees %q, want writer marker", raw[:13])
	}
	unix.Munmap(raw)

	// OpenMutable bootstraps exactly the two meta pages like the Rust
	// writer; the committed extent is mapped by Remap.
	if m.Size() != 2*format.PageSize {
		t.Fatalf("size = %d, want 2-page bootstrap", m.Size())
	}
	if err := m.Remap(4 * format.PageSize); err != nil {
		t.Fatal("remap:", err)
	}
	if err := m.Flush(); err != nil {
		t.Fatal("flush:", err)
	}
	if err := m.SyncFile(); err != nil {
		t.Fatal("sync:", err)
	}
	if m.Size() != 4*format.PageSize {
		t.Fatalf("size = %d, want 4 pages", m.Size())
	}
	if m.PhysicalSize() != 4*format.PageSize {
		t.Fatalf("physical = %d, want 4 pages", m.PhysicalSize())
	}
	if err := m.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r, err := OpenImmutable(path, nil)
	if err != nil {
		t.Fatal("reopen:", err)
	}
	defer r.Close()
	got, err := r.Page(0)
	if err != nil {
		t.Fatal("page:", err)
	}
	if string(got[:13]) != "writer marker" {
		t.Fatalf("immutable reader sees %q, want writer marker", got[:13])
	}
}

// TestOpenMutableGrow pins Grow: ftruncate + remap extend the mapping and
// the file, writes beyond the old extent are visible to a fresh immutable
// open, and the remap keeps the mapping writable.
func TestOpenMutableGrow(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 4)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if m.Size() != 2*format.PageSize {
		t.Fatalf("size = %d, want 2-page bootstrap", m.Size())
	}
	if err := m.Grow(8 * format.PageSize); err != nil {
		t.Fatal("grow:", err)
	}
	if m.Size() != 8*format.PageSize {
		t.Fatalf("size after grow = %d, want 8 pages", m.Size())
	}
	view, err := m.View(4*format.PageSize, format.PageSize)
	if err != nil {
		t.Fatal("view beyond old extent:", err)
	}
	copy(view, []byte("grown marker"))
	// The mapping must still be writable after the mremap.
	view2, err := m.View(5*format.PageSize, format.PageSize)
	if err != nil {
		t.Fatal("view:", err)
	}
	copy(view2, []byte("still writable"))
	if err := m.Flush(); err != nil {
		t.Fatal("flush:", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r, err := OpenImmutable(path, nil)
	if err != nil {
		t.Fatal("reopen:", err)
	}
	defer r.Close()
	if r.PhysicalSize() != 8*format.PageSize {
		t.Fatalf("physical = %d, want 8 pages", r.PhysicalSize())
	}
	if err := r.Remap(8 * format.PageSize); err != nil {
		t.Fatal("remap:", err)
	}
	got, err := r.Page(4)
	if err != nil {
		t.Fatal("page 4:", err)
	}
	if string(got[:12]) != "grown marker" {
		t.Fatalf("page 4 = %q, want grown marker", got[:12])
	}
	got, err = r.Page(5)
	if err != nil {
		t.Fatal("page 5:", err)
	}
	if string(got[:14]) != "still writable" {
		t.Fatalf("page 5 = %q, want still writable", got[:14])
	}
}

// TestGrowRefusals pins the Grow error classes on a read-only mapping and
// on misaligned or shrinking requests.
func TestGrowRefusals(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 4)

	r, err := OpenImmutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Grow(8 * format.PageSize); err == nil {
		t.Fatal("grow of read-only mapping succeeded")
	}
	// The reader's shared lock must be released before the writer's
	// exclusive lock can be taken (locking order is part of the test).
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Grow(1000); err == nil {
		t.Fatal("unaligned grow succeeded")
	}
	if err := m.Grow(1 * format.PageSize); err == nil {
		t.Fatal("shrink succeeded")
	}
}

// TestOpenMutableExcludesReaders pins the exclusive lifetime lock: an
// immutable open blocks while the writer mapping is open, then completes
// after Close releases the lock.
func TestOpenMutableExcludesReaders(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 4)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan *Mapping, 1)
	errc := make(chan error, 1)
	go func() {
		r, err := OpenImmutable(path, nil)
		if err != nil {
			errc <- err
			return
		}
		done <- r
	}()

	select {
	case r := <-done:
		r.Close()
		t.Fatal("immutable open succeeded while writer held the exclusive lock")
	case err := <-errc:
		t.Fatal("immutable open failed:", err)
	case <-time.After(300 * time.Millisecond):
		// Blocked as expected.
	}

	if err := m.Close(); err != nil {
		t.Fatal("writer close:", err)
	}
	select {
	case r := <-done:
		r.Close()
	case err := <-errc:
		t.Fatal("immutable open after writer close:", err)
	case <-time.After(5 * time.Second):
		t.Fatal("immutable open still blocked after writer close")
	}
}

// TestGrowBelowPhysicalRefused pins the file-truncation guard: after Remap
// shrinks the mapping, Grow must refuse any request below the opened
// physical extent instead of ftruncating committed data away.
func TestGrowBelowPhysicalRefused(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 4)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	view, err := m.View(0, format.PageSize)
	if err != nil {
		t.Fatal(err)
	}
	copy(view, []byte("committed marker"))
	if err := m.Remap(2 * format.PageSize); err != nil {
		t.Fatal("remap:", err)
	}
	if err := m.Grow(3 * format.PageSize); err == nil {
		t.Fatal("grow below the physical extent succeeded")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != 4*format.PageSize {
		t.Fatalf("file size = %d, want unchanged 4 pages", st.Size())
	}
	r, err := OpenImmutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:16]) != "committed marker" {
		t.Fatalf("committed data = %q, want committed marker", got[:16])
	}
}

// TestCloseAfterRemapFailure pins the fail-closed contract: a Mapping in
// the post-remap-failure state (data nil, size zero) closes cleanly and
// refuses views with WrongState, exactly like Rust replace_map's
// map=None/len=0 state.
func TestCloseAfterRemapFailure(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 4)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Force the fail-closed state produced by a remap failure (the
	// fallback path unmaps before mapping; Linux failure unmaps here).
	m.data = nil
	m.size = 0
	if _, err := m.View(0, 1); err == nil {
		t.Fatal("view on fail-closed mapping succeeded")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close on fail-closed mapping: %v", err)
	}
	if m.data != nil || m.size != 0 {
		t.Fatal("fail-closed mapping changed on close")
	}
}

// TestViewRefetchAfterGrow pins the writer view discipline: views taken
// before Grow are invalidated (mremap may move the mapping); fresh views
// after Grow observe the written data, and the mapping stays writable.
func TestViewRefetchAfterGrow(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 4)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	view, err := m.View(0, format.PageSize)
	if err != nil {
		t.Fatal(err)
	}
	copy(view, []byte("pre-grow marker"))
	// The view is dead after Grow; writer code must re-fetch.
	if err := m.Grow(8 * format.PageSize); err != nil {
		t.Fatal("grow:", err)
	}
	view, err = m.View(0, format.PageSize)
	if err != nil {
		t.Fatal("re-fetch:", err)
	}
	if string(view[:15]) != "pre-grow marker" {
		t.Fatalf("re-fetched page 0 = %q, want pre-grow marker", view[:15])
	}
	page5, err := m.View(5*format.PageSize, format.PageSize)
	if err != nil {
		t.Fatal(err)
	}
	copy(page5, []byte("post-grow marker"))
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Remap(8 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	got, err := r.Page(5)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:16]) != "post-grow marker" {
		t.Fatalf("page 5 = %q, want post-grow marker", got[:16])
	}
}
