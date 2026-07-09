//go:build unix && !linux

package iprangedb

// growFileAlloc allocates disk space for the growth region using pwrite-zero-fill.
// This is the portable fallback for non-Linux Unix systems (Darwin, FreeBSD, etc.)
// that do not support fallocate.
func growFileAlloc(fd int, oldLen, newLen int64) error {
	return growFilePwrite(fd, oldLen, newLen)
}
