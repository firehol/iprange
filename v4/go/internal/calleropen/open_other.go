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
