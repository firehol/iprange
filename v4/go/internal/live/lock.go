// Package live owns the external live-reader sidecar coordination of the
// v4 database: the fixed reader-table file (header + slots), the gate /
// writer-lease / slot byte-range locks, and the namespace and cleanup
// facts every live handle needs. It mirrors the Rust live_sidecar,
// live_lock, live_namespace, and live_cleanup modules exactly; the
// public OpenLiveReader / OpenLiveWriter / CreateLive surfaces compose
// this owner.
//
// The sidecar is host-local coordination state (spec section 15), never
// part of the portable v4 main-file bytes, and is supported only on
// filesystems whose byte-range locks are owned by an open file
// description and released automatically when its last descriptor
// closes. Linux and macOS provide F_OFD_SETLK; every other platform is
// refused before path access (the Windows live surface is a tracked M5
// item and stays an honest refusal here).
package live

import (
	"os"
	"time"
)

// lockMode is the advisory byte-range lock kind (Rust live_lock Mode).
type lockMode uint8

const (
	lockShared lockMode = iota
	lockExclusive
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
func lock(f *os.File, offset uint64, mode lockMode) error {
	_, err := lockSet(f, offset, mode, true)
	return err
}

// tryLock attempts a non-blocking byte-range lock and reports whether
// it was acquired (Rust live_lock::try_lock).
func tryLock(f *os.File, offset uint64, mode lockMode) (bool, error) {
	return lockSet(f, offset, mode, false)
}

// unlock releases a held byte-range lock (Rust live_lock::unlock).
func unlock(f *os.File, offset uint64) error {
	return lockUnlock(f, offset)
}

// lockCancellable polls tryLock every millisecond until it succeeds or
// the checkpoint cancels (Rust live_lock::lock_cancellable).
func lockCancellable(f *os.File, offset uint64, mode lockMode, check func() error) error {
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

// lockSet is implemented per platform: OFD fcntl on Linux and macOS,
// typed refusal elsewhere.
var lockSet func(f *os.File, offset uint64, mode lockMode, wait bool) (bool, error)

// lockUnlock is implemented per platform.
var lockUnlock func(f *os.File, offset uint64) error
