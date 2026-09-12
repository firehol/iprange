//go:build linux || darwin || freebsd || netbsd || openbsd

package live

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// direntReclen returns the record length of one dirent record (Go os
// dir_unix.go direntReclen over the platform Dirent layout): these
// targets store the record length in the Dirent.Reclen field.
// dragonfly, whose Dirent has no Reclen field, uses
// scan_dirent_dragonfly.go.
func direntReclen(buf []byte) (uint64, bool) {
	return readInt(buf, unsafe.Offsetof(unix.Dirent{}.Reclen), unsafe.Sizeof(unix.Dirent{}.Reclen))
}
