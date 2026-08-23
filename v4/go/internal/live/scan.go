//go:build !windows

// Constant-memory retained-directory scan (Rust
// publication/namespace_scan.rs). The visitor receives each raw name
// borrowed from the scan buffer; the slice is valid only for the
// duration of the call, exactly like the Rust readdir borrow. The
// scan runs over one reused getdents/getdirentries buffer: zero
// per-entry allocations and no per-entry syscalls (Rust libc readdir
// parity).

package live

import (
	"errors"
	"io"
	"unsafe"

	"golang.org/x/sys/unix"
)

// directoryScanBufferSize is the reused getdents buffer size (glibc
// readdir uses 32 KiB; the Go os package refills 8 KiB per syscall).
const directoryScanBufferSize = 32 << 10

// readInt returns the size-byte unsigned integer in native byte order
// at offset off (Go os dir_unix.go readInt).
func readInt(b []byte, off, size uintptr) (u uint64, ok bool) {
	if len(b) < int(off+size) {
		return 0, false
	}
	_ = b[off : off+size]
	switch size {
	case 1:
		u = uint64(b[off])
	case 2:
		_ = b[off+1]
		u = uint64(b[off]) | uint64(b[off+1])<<8
	case 4:
		_ = b[off+3]
		u = uint64(b[off]) | uint64(b[off+1])<<8 | uint64(b[off+2])<<16 | uint64(b[off+3])<<24
	case 8:
		_ = b[off+7]
		u = uint64(b[off]) | uint64(b[off+1])<<8 | uint64(b[off+2])<<16 | uint64(b[off+3])<<24 |
			uint64(b[off+4])<<32 | uint64(b[off+5])<<40 | uint64(b[off+6])<<48 | uint64(b[off+7])<<56
	default:
		return 0, false
	}
	return u, true
}

// direntReclen returns the record length of one dirent record (Go os
// dir_unix.go direntReclen over the platform Dirent layout).
func direntReclen(buf []byte) (uint64, bool) {
	return readInt(buf, unsafe.Offsetof(unix.Dirent{}.Reclen), unsafe.Sizeof(unix.Dirent{}.Reclen))
}

// direntName returns the name field of one dirent record followed by
// the record's leftover padding (Go os dir_unix.go). The caller trims
// the NUL terminator and any padding.
func direntName(rec []byte) []byte {
	const namoff = uintptr(unsafe.Offsetof(unix.Dirent{}.Name))
	if len(rec) < int(namoff) {
		return nil
	}
	return rec[namoff:]
}

// scanDirStream visits every entry of the open directory descriptor
// in constant memory: each getdents call fills the reused buffer and
// every record is decoded straight from it. EINTR retries like the os
// package; EOF ends the scan. Stream failures use the exact Rust
// operation label.
func scanDirStream(fd int, visitor func([]byte) error) error {
	buf := make([]byte, directoryScanBufferSize)
	for {
		n, err := unix.ReadDirent(fd, buf)
		for errors.Is(err, unix.EINTR) {
			n, err = unix.ReadDirent(fd, buf)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nsIoError("read retained directory stream", err)
		}
		if n == 0 {
			return nil
		}
		remaining := buf[:n]
		for len(remaining) > 0 {
			reclen, ok := direntReclen(remaining)
			if !ok || reclen > uint64(len(remaining)) || reclen == 0 {
				// A torn record cannot happen on a retained directory;
				// the os package treats it as the end of the buffer.
				break
			}
			rec := remaining[:reclen]
			remaining = remaining[reclen:]
			name := direntName(rec)
			for i, c := range name {
				if c == 0 {
					name = name[:i]
					break
				}
			}
			if string(name) == "." || string(name) == ".." {
				continue
			}
			if err := visitor(name); err != nil {
				return err
			}
		}
	}
}
