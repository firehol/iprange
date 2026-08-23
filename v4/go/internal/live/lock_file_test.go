//go:build !windows

// Artifact lock surface (Rust live_lock lock_file / try_lock_file /
// unlock_file / lock_file_cancellable): exclusive contention, shared
// coexistence, release, and cancellation-check behavior. Linux and
// macOS run the OFD byte-range arm; FreeBSD runs the whole-file flock
// arm with identical observable semantics.

package live

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openLockPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact-lock")
	first, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open first lock handle: %v", err)
	}
	second, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		first.Close()
		t.Fatalf("open second lock handle: %v", err)
	}
	t.Cleanup(func() {
		first.Close()
		second.Close()
	})
	return first, second
}

func TestTryLockFileExclusiveContention(t *testing.T) {
	first, second := openLockPair(t)
	acquired, err := TryLockFile(first, mainLifetimeOffset, LockExclusive)
	if err != nil || !acquired {
		t.Fatalf("first exclusive lock: acquired=%v err=%v", acquired, err)
	}
	acquired, err = TryLockFile(second, mainLifetimeOffset, LockExclusive)
	if err != nil {
		t.Fatalf("contending exclusive lock: %v", err)
	}
	if acquired {
		t.Fatal("second exclusive lock must not be acquired while the first is held")
	}
	if err := UnlockFile(first, mainLifetimeOffset); err != nil {
		t.Fatalf("unlock first handle: %v", err)
	}
	acquired, err = TryLockFile(second, mainLifetimeOffset, LockExclusive)
	if err != nil || !acquired {
		t.Fatalf("exclusive lock after release: acquired=%v err=%v", acquired, err)
	}
}

func TestTryLockFileSharedCoexists(t *testing.T) {
	first, second := openLockPair(t)
	for _, handle := range []*os.File{first, second} {
		acquired, err := TryLockFile(handle, mainLifetimeOffset, LockShared)
		if err != nil || !acquired {
			t.Fatalf("shared lock: acquired=%v err=%v", acquired, err)
		}
	}
	acquired, err := TryLockFile(second, mainLifetimeOffset, LockExclusive)
	if err != nil {
		t.Fatalf("exclusive lock under shared locks: %v", err)
	}
	if acquired {
		t.Fatal("exclusive lock must not be acquired while shared locks are held")
	}
}

func TestLockFileCancellable(t *testing.T) {
	first, second := openLockPair(t)
	acquired, err := TryLockFile(first, mainLifetimeOffset, LockExclusive)
	if err != nil || !acquired {
		t.Fatalf("first exclusive lock: acquired=%v err=%v", acquired, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- LockFileCancellable(second, mainLifetimeOffset, LockShared, nil)
	}()
	// Let the poller contend once, release the holder, and wait for the
	// acquisition (Rust lock_file_cancellable 1ms polling cadence).
	time.Sleep(5 * time.Millisecond)
	if err := UnlockFile(first, mainLifetimeOffset); err != nil {
		t.Fatalf("unlock first handle: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancellable lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellable lock did not acquire after release")
	}
}

func TestLockFileCancellableCheckCancels(t *testing.T) {
	first, second := openLockPair(t)
	acquired, err := TryLockFile(first, mainLifetimeOffset, LockExclusive)
	if err != nil || !acquired {
		t.Fatalf("first exclusive lock: acquired=%v err=%v", acquired, err)
	}
	want := errors.New("cancel")
	err = LockFileCancellable(second, mainLifetimeOffset, LockExclusive, func() error {
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("cancellable lock canceled with %v, got %v", want, err)
	}
}
