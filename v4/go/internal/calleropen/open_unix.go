//go:build unix

package calleropen

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// syscallMode mirrors os.syscallMode: the permission bits of the open.
func syscallMode(mode os.FileMode) uint32 { return uint32(mode.Perm()) }

func openPath(path string, flags int, perm os.FileMode) (*os.File, error) {
	var (
		fd  int
		err error
	)
	for {
		fd, err = unix.Open(path, flags|unix.O_CLOEXEC, uint32(syscallMode(perm)))
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if flags&unix.O_NONBLOCK != 0 {
		// Leave the descriptor in blocking mode (see the package
		// comment): os.NewFile re-reads the flags with F_GETFL and
		// attaches a non-blocking handle to the netpoller.
		fl, ferr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if ferr != nil {
			unix.Close(fd)
			return nil, &os.PathError{Op: "fcntl", Path: path, Err: ferr}
		}
		if _, ferr := unix.FcntlInt(uintptr(fd), unix.F_SETFL, fl&^unix.O_NONBLOCK); ferr != nil {
			unix.Close(fd)
			return nil, &os.PathError{Op: "fcntl", Path: path, Err: ferr}
		}
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: path, Err: unix.EINVAL}
	}
	return file, nil
}

func blockingFile(fd int, name string) (*os.File, error) {
	fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "fcntl", Path: name, Err: err}
	}
	if fl&unix.O_NONBLOCK != 0 {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, fl&^unix.O_NONBLOCK); err != nil {
			unix.Close(fd)
			return nil, &os.PathError{Op: "fcntl", Path: name, Err: err}
		}
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: name, Err: unix.EINVAL}
	}
	return file, nil
}
