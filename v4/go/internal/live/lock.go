// Package live owns the external live-reader sidecar coordination of the
// v4 database: the fixed reader-table file (header + slots), the gate /
// writer-lease / slot byte-range locks, and the namespace and cleanup
// facts every live handle needs. It mirrors the Rust live_sidecar,
// live_lock, live_namespace, and live_cleanup modules exactly; the
// public OpenLiveReader / OpenLiveWriter / CreateLive surfaces compose
// this owner.
//
// The sidecar is host-local coordination state (spec section 15), never
// part of the portable v4 main-file bytes. Two lock surfaces exist.
// The byte-range surface (lock / tryLock / unlock / lockCancellable)
// serves the sidecar itself and is supported only where locks are owned
// by an open file description and released automatically when its last
// descriptor closes: Linux and macOS provide F_OFD_SETLK, every other
// platform is refused before path access (the Windows live surface is a
// tracked M5 item and stays an honest refusal here). The artifact
// surface (LockFile / TryLockFile / UnlockFile / LockFileCancellable)
// locks one complete publication artifact: Linux and macOS keep the
// caller-supplied byte-range offset with OFD locks, FreeBSD locks the
// whole file with flock because it has no OFD locks, and all other
// platforms refuse.
package live

import (
	"os"
	"time"
)

// LockMode is the advisory lock kind (Rust live_lock Mode).
type LockMode uint8

const (
	LockShared LockMode = iota
	LockExclusive
)

// checkpoint runs one cancellation checkpoint; a nil check never cancels
// (Rust CancellationToken::check; the writer core passes the public
// cancellation function, internal callers pass nil).
func checkpoint(check func() error) error {
	if check == nil {
		return nil
	}
	return check()
}

// lock takes a blocking byte-range lock (Rust live_lock::lock).
func lock(f *os.File, offset uint64, mode LockMode) error {
	_, err := lockSet(f, offset, mode, true)
	return err
}

// tryLock attempts a non-blocking byte-range lock and reports whether
// it was acquired (Rust live_lock::try_lock).
func tryLock(f *os.File, offset uint64, mode LockMode) (bool, error) {
	return lockSet(f, offset, mode, false)
}

// unlock releases a held byte-range lock (Rust live_lock::unlock).
func unlock(f *os.File, offset uint64) error {
	return lockUnlock(f, offset)
}

// lockCancellable polls tryLock every millisecond until it succeeds or
// the checkpoint cancels (Rust live_lock::lock_cancellable).
func lockCancellable(f *os.File, offset uint64, mode LockMode, check func() error) error {
	for {
		if err := checkpoint(check); err != nil {
			return err
		}
		acquired, err := tryLock(f, offset, mode)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}

// LockFile locks a complete publication artifact (Rust
// live_lock::lock_file). The offset keeps the established byte range on
// Linux and macOS; FreeBSD locks the whole file and ignores it.
func LockFile(f *os.File, offset uint64, mode LockMode) error {
	_, err := fileLockSet(f, offset, mode, true)
	return err
}

// TryLockFile attempts a non-blocking artifact lock and reports whether
// it was acquired (Rust live_lock::try_lock_file).
func TryLockFile(f *os.File, offset uint64, mode LockMode) (bool, error) {
	return fileLockSet(f, offset, mode, false)
}

// UnlockFile releases a held artifact lock (Rust live_lock::unlock_file).
func UnlockFile(f *os.File, offset uint64) error {
	return fileLockUnlock(f, offset)
}

// LockFileCancellable polls TryLockFile every millisecond until it
// succeeds or the checkpoint cancels (Rust
// live_lock::lock_file_cancellable).
func LockFileCancellable(f *os.File, offset uint64, mode LockMode, check func() error) error {
	for {
		if err := checkpoint(check); err != nil {
			return err
		}
		acquired, err := TryLockFile(f, offset, mode)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
}

// lockSet is implemented per platform: OFD fcntl on Linux and macOS,
// typed refusal elsewhere.
var lockSet func(f *os.File, offset uint64, mode LockMode, wait bool) (bool, error)

// lockUnlock is implemented per platform.
var lockUnlock func(f *os.File, offset uint64) error

// fileLockSet is implemented per platform: OFD byte-range locks at the
// caller offset on Linux and macOS, whole-file flock on FreeBSD, typed
// refusal elsewhere (Rust live_lock lock_file / try_lock_file arms).
var fileLockSet func(f *os.File, offset uint64, mode LockMode, wait bool) (bool, error)

// fileLockUnlock is implemented per platform.
var fileLockUnlock func(f *os.File, offset uint64) error
