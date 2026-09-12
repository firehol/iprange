//go:build dragonfly

package live

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// direntReclen returns the record length of one dirent record. The
// dragonfly Dirent has no Reclen field, so the length is derived from
// Namlen exactly like Go os/dirent_dragonfly.go: the 16-byte fixed
// header plus name and NUL terminator, rounded up to the 8-byte
// boundary every kernel record is padded to.
func direntReclen(buf []byte) (uint64, bool) {
	namlen, ok := readInt(buf, unsafe.Offsetof(unix.Dirent{}.Namlen), unsafe.Sizeof(unix.Dirent{}.Namlen))
	if !ok {
		return 0, false
	}
	return (16 + namlen + 1 + 7) &^ 7, true
}
