//go:build freebsd

package snapshot

import "golang.org/x/sys/unix"

// syscallMknodChardev creates one character-device node at path, sized to
// the null device's numbers: the shape an external publisher could leave
// in a destination slot. Where the test user has no CAP_MKNOD the call
// reports EPERM and the caller skips that one shape.
//
// FreeBSD's x/sys/unix declares the mknod device argument as uint64
// (every other unix GOOS declares it int), so this arm passes unix.Mkdev's
// uint64 directly; see destination_nonregular_class_mknod_unix_test.go for
// the other arm.
func syscallMknodChardev(path string) error {
	return unix.Mknod(path, 0o600|unix.S_IFCHR, unix.Mkdev(1, 3))
}
