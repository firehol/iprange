//go:build !windows

package snapshot

import (
	"testing"

	"golang.org/x/sys/unix"
)

// syscallMkfifo creates one FIFO for the destination-class pins.
func syscallMkfifo(path string) error { return unix.Mkfifo(path, 0o600) }

// bindUnixSocket creates one AF_UNIX socket node at path. The descriptor
// is closed without unlinking, which leaves the named socket in the
// namespace exactly as an external publisher's leftover node would be:
// the shape the open must classify.
func bindUnixSocket(t *testing.T, path string) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Skipf("AF_UNIX sockets are unavailable: %v", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		unix.Close(fd)
		t.Skipf("AF_UNIX sockets are unavailable: %v", err)
	}
	unix.Close(fd)
	t.Cleanup(func() { _ = osRemove(path) })
}

// syscallMknodChardev creates one character-device node at path. The
// device numbers are those of the null device, which is the shape an
// external publisher could leave in a destination slot; on a host where
// the test user has no CAP_MKNOD the call reports EPERM and the caller
// skips that one shape.
func syscallMknodChardev(path string) error {
	return unix.Mknod(path, 0o600|unix.S_IFCHR, int(unix.Mkdev(1, 3)))
}
