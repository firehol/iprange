//go:build !unix

package calleropen

import "os"

// openPath is the fallback for platforms without the POSIX O_NONBLOCK
// open hazard: the plain os.OpenFile path is already correct there,
// because no caller passes O_NONBLOCK.
func openPath(path string, flags int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags, perm)
}

func blockingFile(fd int, name string) (*os.File, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrInvalid}
	}
	return file, nil
}

// NonBlocking is the platform's O_NONBLOCK open flag. The platforms
// without the POSIX poller hazard have no such flag to pass, and
// os.OpenFile is already the correct path there.
const NonBlocking = 0
