//go:build linux

package calleropen

import (
	"bytes"
	"errors"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FreeDescriptors is the Linux arm of the table read (design section 6):
// the kernel's own view of this process's descriptor table, /proc/self/fd,
// read with raw syscalls, falling back to the fcntl(F_GETFD) scan.
//
// The read must not use the os package: an open handed to os.OpenFile is
// exactly the pollable registration this decision exists to keep off the
// process. unix.Openat/unix.Getdents/unix.Close touch no *os.File, so
// taking the decision cannot itself be the thing that needs the poller. The
// directory fd is held across the walk and is therefore counted as in use,
// which makes the answer conservative by one rather than optimistic.
//
// The walk is bounded by a record budget and a syscall budget, and any
// shortfall, malformed record or unexpected error falls back to the scan
// instead of trusting a partial count. The budgets are why this walk can
// never spin; see poller_table_note.go. The answer is the number of entries,
// so free is soft minus that count: the three standard streams and every
// descriptor the process holds are exactly what the kernel lists.
func FreeDescriptors() (free int, ok bool) {
	var rlimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rlimit); err != nil {
		return 0, false
	}
	soft := uint64(rlimit.Cur)
	if soft > maxScanDescriptors {
		// The scan bound already decides the answer above that window, so
		// take the single-path arm.
		return freeDescriptorsByScan(soft), true
	}
	inUse, walked := procSelfFdCount()
	if !walked || inUse > int(soft) {
		// Only a table that grew under the read can exceed the soft limit;
		// answer from the scan rather than a negative free count.
		return freeDescriptorsByScan(soft), true
	}
	return int(soft) - inUse, true
}

// linux_dirent64 layout (include/uapi/linux/havegetdents.h):
// { ino64 u64 @0, off64 s64 @8, reclen u16 @16, type u8 @18, name[] @19 }.
const (
	dirent64ReclenOffset = 16
	dirent64HeaderSize   = 19
	recordBufferBytes    = 1024
)

// procSelfFdCount returns the number of descriptors the kernel lists for
// this process, and whether the walk completed. /proc/self/fd is the
// kernel's own table view, not a caller-reachable path, and the open is
// O_NONBLOCK so a hostile mount under /proc cannot block it.
func procSelfFdCount() (inUse int, completed bool) {
	dirFD, err := unix.Openat(unix.AT_FDCWD, "/proc/self/fd",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return 0, false
	}
	defer unix.Close(dirFD)
	// The walk's own directory descriptor is listed by the kernel and is
	// released when this call returns, so it is not part of the table the
	// caller will have. Counting it would make the walk one descriptor more
	// conservative than the fcntl scan, and the two arms of one owner must
	// answer the same question (pinned by
	// poller_readiness_linux_test.go's table-read case).
	selfName := strconv.Itoa(dirFD)

	var buffer [recordBufferBytes]byte
	count := 0
	for calls := 0; calls <= maxScanDescriptors+1; calls++ {
		n, err := unix.Getdents(dirFD, unsafe.Slice(&buffer[0], recordBufferBytes))
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil || n < 0:
			return 0, false
		case n == 0:
			return count, true
		}
		for offset := 0; offset < n; {
			if offset+dirent64HeaderSize > n {
				return 0, false // truncated record header
			}
			reclen := int(uint16(buffer[offset+dirent64ReclenOffset]) |
				uint16(buffer[offset+dirent64ReclenOffset+1])<<8)
			if reclen < dirent64HeaderSize || offset+reclen > n {
				return 0, false // malformed record
			}
			name := buffer[offset+dirent64HeaderSize : offset+reclen]
			if end := bytes.IndexByte(name, 0); end >= 0 {
				name = name[:end]
			}
			if !isDotEntry(name) && string(name) != selfName {
				count++
			}
			if count > maxScanDescriptors {
				return 0, false
			}
			offset += reclen
		}
	}
	return 0, false
}

// isDotEntry reports whether one directory record is "." or "..", which
// some kernels list in /proc/self/fd and which are not descriptors.
func isDotEntry(name []byte) bool {
	for _, candidate := range [][]byte{{'.'}, {'.', '.'}} {
		if len(name) >= len(candidate) && string(name[:len(candidate)]) == string(candidate) &&
			(len(name) == len(candidate) || name[len(candidate)] == 0) {
			return true
		}
	}
	return false
}
