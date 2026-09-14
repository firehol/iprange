//go:build unix && !(linux || darwin || freebsd)

package handlers

import (
	"errors"
	"fmt"
)

// installDescriptorLimit has no RLIMIT_NOFILE installer on this platform.
// golang.org/x/sys/unix declares Setrlimit with per-platform rlimit field
// widths, and the two implemented variants (fd_pressure_limit_posix_test.go
// for linux/darwin, fd_pressure_limit_bsd_test.go for freebsd) match those
// widths only there, so the remaining unix systems land here.
//
// Returning the unsupported error is deliberate: the re-exec'd child exits
// with fdPressureSetrlimitFailed, skipIfHostCannotPressurize turns that into
// a skip, and the pressured cases report the platform gap instead of passing
// without ever pressuring the process. The limit-free baseline case does not
// call this function and still runs everywhere.
//
// A platform gains real coverage by adding its own variant with the matching
// rlimit field width, as the two implemented files do.
func installDescriptorLimit(_ uint64) error {
	return fmt.Errorf("lowering RLIMIT_NOFILE is not implemented on this platform: %w",
		errors.ErrUnsupported)
}
