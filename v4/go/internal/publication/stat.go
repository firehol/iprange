//go:build !windows

package publication

import (
	"os"

	"golang.org/x/sys/unix"
)

// fstatSize reports one retained file's size with a raw fstat (Rust
// File::metadata().len(); the raw syscall avoids the os.File.Stat
// wrapper allocation on the machine verify paths).
func fstatSize(file *os.File) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return 0, err
	}
	return uint64(st.Size), nil
}
