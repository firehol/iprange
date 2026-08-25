//go:build windows

package live

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Windows implements the sidecar byte-range locks and the artifact
// locks with per-handle byte-range LockFileEx ranges (Rust live_lock
// windows platform): LOCKFILE_EXCLUSIVE_LOCK for exclusive,
// LOCKFILE_FAIL_IMMEDIATELY for non-wait try, one byte at the
// caller-supplied offset in an OVERLAPPED structure, ERROR_LOCK_VIOLATION
// for the try-false outcome, and automatic release when the handle
// closes. Windows keeps the exact byte-range contract of the live
// sidecar: no whole-file substitute exists, exactly like the Linux and
// macOS OFD machines.

func init() {
	lockSet = setWindows
	lockUnlock = unlockWindows
	fileLockSet = setWindows
	fileLockUnlock = unlockWindows
}

func overlapped(offset uint64) *windows.Overlapped {
	return &windows.Overlapped{
		Offset:     uint32(offset),
		OffsetHigh: uint32(offset >> 32),
	}
}

func setWindows(f *os.File, offset uint64, mode LockMode, wait bool) (bool, error) {
	flags := uint32(0)
	if mode == LockExclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, overlapped(offset))
	if err == nil {
		return true, nil
	}
	if !wait && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, &format.Error{Code: format.CodeIO, Detail: "byte-range lock: " + err.Error()}
}

func unlockWindows(f *os.File, offset uint64) error {
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped(offset)); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "byte-range unlock: " + err.Error()}
	}
	return nil
}

// requireLiveSupported permits live coordination on Windows
// (per-handle byte-range locks are the proven live coordination
// primitive), mirroring Rust live_lock::require_live_supported.
func requireLiveSupported() error { return nil }
