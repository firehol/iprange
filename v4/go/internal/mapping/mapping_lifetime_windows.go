//go:build windows

package mapping

import (
	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// coordinationSupported reports the exclusive lifetime-lock machine
// availability (true on windows: LockFileEx byte ranges are the proven
// live coordination primitive, Rust live_lock windows platform).
const coordinationSupported = true

// The lifetime lock is the shared byte-range lock at offset 1<<44,
// mirroring the Rust MAIN_LIFETIME_LOCK (live_sidecar.rs): every reader
// holds it shared from open to close, and every writer must hold it
// exclusively to overwrite the main file. Windows LockFileEx ranges
// are per-handle and release automatically when the handle closes,
// exactly like the OFD contract on Linux and macOS.
const (
	lifetimeLockOffset = 1 << 44
	lifetimeLockLen    = 1
)

// requireLiveCoordination permits live opens on Windows (per-handle
// byte-range coordination is proven here).
func requireLiveCoordination() error { return nil }

// overlappedLifetime builds the OVERLAPPED of the lifetime byte range.
func overlappedLifetime() *windows.Overlapped {
	return &windows.Overlapped{
		Offset:     uint32(lifetimeLockOffset & 0xFFFFFFFF),
		OffsetHigh: uint32((lifetimeLockOffset >> 32) & 0xFFFFFFFF),
	}
}

// lockLifetimeShared takes the shared byte-range lifetime lock on fd.
// The lock is blocking (no LOCKFILE_FAIL_IMMEDIATELY), mirroring Rust
// live_lock lock() with wait=true: an immutable open waits for a writer
// holding the exclusive lock instead of failing immediately.
func lockLifetimeShared(fd int) error {
	if err := windows.LockFileEx(windows.Handle(fd), 0, 0, lifetimeLockLen, 0, overlappedLifetime()); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "lifetime lock: " + err.Error()}
	}
	return nil
}

// lockLifetimeExclusive takes the exclusive byte-range lifetime lock on
// fd (LOCKFILE_EXCLUSIVE_LOCK on the same byte range), mirroring Rust
// live_lock exclusive mode: exactly one live writer may hold it, and
// every immutable open waits for the writer to release it before
// mapping.
func lockLifetimeExclusive(fd int) error {
	if err := windows.LockFileEx(windows.Handle(fd), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, lifetimeLockLen, 0, overlappedLifetime()); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "lifetime lock: " + err.Error()}
	}
	return nil
}

// unlockLifetime releases the held byte-range lifetime lock on fd.
func unlockLifetime(fd int) error {
	if err := windows.UnlockFileEx(windows.Handle(fd), 0, lifetimeLockLen, 0, overlappedLifetime()); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "lifetime unlock: " + err.Error()}
	}
	return nil
}
