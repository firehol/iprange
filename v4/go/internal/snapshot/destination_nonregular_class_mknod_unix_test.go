//go:build unix && !freebsd

package snapshot

import "golang.org/x/sys/unix"

// syscallMknodChardev creates one character-device node at path, sized to
// the null device's numbers: the shape an external publisher could leave
// in a destination slot. Where the test user has no CAP_MKNOD the call
// reports EPERM and the caller skips that one shape.
//
// This arm covers every unix GOOS whose x/sys/unix declares the mknod
// device argument as int; FreeBSD declares it uint64 and has its own arm
// in destination_nonregular_class_mknod_freebsd_test.go.
func syscallMknodChardev(path string) error {
	return unix.Mknod(path, 0o600|unix.S_IFCHR, int(unix.Mkdev(1, 3)))
}
