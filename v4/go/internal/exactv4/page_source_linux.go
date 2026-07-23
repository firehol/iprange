//go:build linux

package exactv4

import (
	"math"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func readFilePageAtStatus(
	_ *os.File,
	fd int,
	offset uint64,
	page *[PageSize]byte,
) pageSourceStatus {
	initialOffset := offset
	expected := len(page)
	actual := 0
	remaining := page[:]
	for len(remaining) != 0 {
		if offset > math.MaxInt64 {
			return pageSourceStatus{code: pageSourceErrOffsetOverflow}
		}
		count, err := unix.Pread(fd, remaining, int64(offset))
		if count > 0 {
			actual += count
			next, ok := checkedAdd(offset, uint64(count))
			if !ok {
				return pageSourceStatus{code: pageSourceErrOffsetOverflow}
			}
			offset = next
			remaining = remaining[count:]
		}
		if err != nil {
			return pageSourceStatusFromErrno(err)
		}
		if count <= 0 {
			return pageSourceStatus{
				code:     pageSourceErrShortRead,
				offset:   initialOffset,
				expected: expected,
				actual:   actual,
			}
		}
	}
	return pageSourceStatus{}
}

func pageSourceStatusFromErrno(err error) pageSourceStatus {
	status := pageSourceStatus{code: pageSourceErrIO, ioKind: pageIOOther}
	errno, ok := err.(syscall.Errno)
	if !ok {
		return status
	}
	status.rawOSCode = uint64(errno)
	status.hasRaw = true
	switch errno {
	case unix.ENOENT:
		status.ioKind = pageIONotFound
	case unix.EACCES, unix.EPERM:
		status.ioKind = pageIOPermissionDenied
	case unix.EINVAL, unix.EBADF, unix.ESPIPE:
		status.ioKind = pageIOInvalidInput
	case unix.EINTR:
		status.ioKind = pageIOInterrupted
	case unix.ENOMEM:
		status.ioKind = pageIOOutOfMemory
	}
	return status
}
