//go:build linux || darwin

package worker

import (
	"os"

	"golang.org/x/sys/unix"
)

// nullStdioIsDevice verifies the calleropen descriptor names the null
// character device: a character device whose major/minor match the
// platform expectation (see nullDeviceIdentity).
func nullStdioIsDevice(file *os.File) bool {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return false
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return false
	}
	major, minor := nullDeviceIdentity()
	return unix.Major(uint64(stat.Rdev)) == major && unix.Minor(uint64(stat.Rdev)) == minor
}
