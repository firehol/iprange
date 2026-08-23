//go:build darwin

package live

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// macOS 10.15+ defines F_OFD_SETLK as 90 (F_OFD_SETLKW 91); x/sys does
// not publish the constants for darwin, so they are declared here
// against the Apple header values, exactly like the mapping owner's
// lifetime lock.
const (
	fOfdSetLK  = 90
	fOfdSetLKW = 91
)

func init() {
	lockSet = setOFD
	lockUnlock = unlockOFD
}

func flock(offset uint64, lockType int16) (unix.Flock_t, error) {
	if uint64(int64(offset)) != offset {
		return unix.Flock_t{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "lock offset exceeds off_t"}
	}
	return unix.Flock_t{Type: lockType, Whence: unix.SEEK_SET, Start: int64(offset), Len: 1}, nil
}

func setOFD(f *os.File, offset uint64, mode lockMode, wait bool) (bool, error) {
	lockType := int16(unix.F_RDLCK)
	if mode == lockExclusive {
		lockType = unix.F_WRLCK
	}
	fl, err := flock(offset, lockType)
	if err != nil {
		return false, err
	}
	command := fOfdSetLK
	if wait {
		command = fOfdSetLKW
	}
	for {
		if err := unix.FcntlFlock(f.Fd(), command, &fl); err == nil {
			return true, nil
		} else if errors.Is(err, unix.EINTR) {
			continue
		} else if !wait && (errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN)) {
			return false, nil
		} else {
			return false, &format.Error{Code: format.CodeIO, Detail: "byte-range lock: " + err.Error()}
		}
	}
}

func unlockOFD(f *os.File, offset uint64) error {
	fl, err := flock(offset, unix.F_UNLCK)
	if err != nil {
		return err
	}
	for {
		if err := unix.FcntlFlock(f.Fd(), fOfdSetLK, &fl); err == nil {
			return nil
		} else if errors.Is(err, unix.EINTR) {
			continue
		} else {
			return &format.Error{Code: format.CodeIO, Detail: "byte-range unlock: " + err.Error()}
		}
	}
}

// requireLiveSupported permits live coordination on darwin (OFD
// byte-range locks are proven here), mirroring Rust
// live_lock::require_live_supported.
func requireLiveSupported() error { return nil }
