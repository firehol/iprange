//go:build windows

package live

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestUnlinkExactCannotProveAbsenceWhileItHoldsTheHandle pins the measured
// Windows behavior that makes the POSIX retirement shape unusable for a
// maintenance removal (SOW-0028 wave-19.25), recorded natively on the
// authorized Windows validation host (Windows 11 10.0.26200, go1.26.5
// windows/amd64).
//
// UnlinkExact opens the artifact itself (writable, delete-shared), sets
// FileDispositionInfoEx(DELETE|POSIX_SEMANTICS) — which succeeds — and then
// proves the absence through RequireAbsent, which opens the same name with
// FILE_READ_ATTRIBUTES|READ_CONTROL. Because the disposition handle is still
// open, the name is delete-pending and that open fails with
// ERROR_ACCESS_DENIED (5), reported as the io namespace class at the
// "open retained Windows path" operation. The failure is independent of
// whether the caller also retains and locks the artifact, so the unlink can
// never deliver "removed and proved absent" for one name in a single call on
// Windows.
//
// That is why the Rust reference never unlinks a maintenance artifact it
// still owns, and why the Go maintenance arm retires through the GC envelope
// instead: the envelope records the retirement authority and the housekeeping
// facts, the payload move is a rename driven by the retained handle, and the
// pair is finished best effort (as in Rust, whose finish_housekeeping also
// ignores these unlink errors) rather than by a proven unlink.
func TestUnlinkExactCannotProveAbsenceWhileItHoldsTheHandle(t *testing.T) {
	const name = ".iprange-retained-probe-0000000000000000000000000000.tmp"

	requireDenied := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("UnlinkExact succeeded; the absence proof cannot complete while the disposition handle is open")
		}
		nerr, ok := AsNamespaceError(err)
		if !ok {
			t.Fatalf("error %v is not a namespace error", err)
		}
		if nerr.Kind != NamespaceIo && nerr.Kind != NamespaceIoAt {
			t.Fatalf("error class = %v (op %q), want the io class", nerr.Kind, nerr.Op)
		}
		var errno syscall.Errno
		if !errors.As(nerr.Err, &errno) || errno != windows.ERROR_ACCESS_DENIED {
			t.Fatalf("error %q wraps %v, want ERROR_ACCESS_DENIED (5)", nerr.Op, nerr.Err)
		}
	}

	t.Run("caller_holds_no_handle", func(t *testing.T) {
		directory := t.TempDir()
		dir, expected := seedRetainedProbe(t, directory, name)
		defer dir.Close()
		unlinked, err := dir.UnlinkExact(name, expected)
		if unlinked {
			t.Fatal("UnlinkExact reported the name removed and proved absent")
		}
		requireDenied(t, err)
	})

	t.Run("caller_retains_and_locks", func(t *testing.T) {
		directory := t.TempDir()
		dir, expected := seedRetainedProbe(t, directory, name)
		defer dir.Close()
		regular, err := dir.OpenRegular(name, true)
		if err != nil || regular == nil {
			t.Fatalf("open the owned artifact writable: %v (nil=%v)", err, regular == nil)
		}
		if err := LockFileCancellable(regular.File, MainLifetimeOffset, LockExclusive, nil); err != nil {
			regular.File.Close()
			t.Fatalf("lock the artifact for its lifetime: %v", err)
		}
		defer regular.File.Close()
		unlinked, err := dir.UnlinkExact(name, expected)
		if unlinked {
			t.Fatal("UnlinkExact reported the name removed and proved absent")
		}
		requireDenied(t, err)
	})
}

// seedRetainedProbe creates one small regular file in directory and returns
// the retained directory handle with the identity the unlink proof expects.
func seedRetainedProbe(t *testing.T, directory, name string) (*Directory, FileIdentity) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte("retained"), 0o600); err != nil {
		t.Fatalf("seed the artifact: %v", err)
	}
	dir, err := OpenDirectory(directory)
	if err != nil {
		t.Fatalf("open the directory: %v", err)
	}
	found, present, err := dir.Entry(name)
	if err != nil || !present {
		dir.Close()
		t.Fatalf("the seeded artifact is not visible (present=%v, err=%v)", present, err)
	}
	return dir, found.Identity
}
