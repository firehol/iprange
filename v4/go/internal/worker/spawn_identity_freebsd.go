//go:build freebsd

package worker

import (
	"os"

	"golang.org/x/sys/unix"
)

// nullStdioIsDevice verifies the calleropen descriptor names the null
// character device. FreeBSD encodes dev_t as (major << 24) | minor, so
// the identity is checked with the platform's own encoding rather than
// a shared helper.
func nullStdioIsDevice(file *os.File) bool {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return false
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return false
	}
	major, minor := nullDeviceIdentity()
	return uint32(stat.Rdev>>24) == major && uint32(stat.Rdev&0xffffff) == minor
}
