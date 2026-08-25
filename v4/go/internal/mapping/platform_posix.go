//go:build !windows

package mapping

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// POSIX platform primitives of the mapping owner: the no-follow open,
// the exclusive create, the mmap/munmap/msync/truncate/dup/stat
// operations, and the protection constants. Windows implements the
// same surface with handles and section objects in platform_windows.go;
// every byte of the shared open/remap/shrink logic is identical on
// both sides (Rust mapping.rs + database_file.rs arms).

const (
	protRead  = unix.PROT_READ
	protWrite = unix.PROT_WRITE
)

// openNoFollow opens the final path component without following a
// symlink, mapping the POSIX O_NOFOLLOW refusal of the Rust
// open path. The caller maps every failure to the IO class with the
// "open" label (Rust open_read_only).
func openNoFollow(clean string, rdwr bool) (*os.File, error) {
	flags := os.O_RDONLY
	if rdwr {
		flags = os.O_RDWR
	}
	f, err := os.OpenFile(clean, flags|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	return f, nil
}

// createNoFollow exclusively creates one 0600 file refusing an
// existing destination and any symlink final component (Rust
// live_namespace::create_private POSIX arm + require_absent).
func createNoFollow(clean string) (*os.File, error) {
	f, err := os.OpenFile(clean, os.O_RDWR|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, &format.Error{Code: format.CodeNameExists, Detail: "destination exists"}
		}
		return nil, &format.Error{Code: format.CodeIO, Detail: "create: " + err.Error()}
	}
	return f, nil
}

// mmapShared maps size bytes of the file with the given protection
// (Rust map_nonempty); size is the exact extent, never rounded.
func mmapShared(f *os.File, size int, prot int) ([]byte, error) {
	data, err := unix.Mmap(int(f.Fd()), 0, size, prot, unix.MAP_SHARED)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "mmap: " + err.Error()}
	}
	return data, nil
}

// munmapShared releases one mapping (Rust memmap2 drop / UnmapViewOfFile
// on windows).
func munmapShared(data []byte) error {
	if err := unix.Munmap(data); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "munmap: " + err.Error()}
	}
	return nil
}

// msyncShared synchronizes the mapped prefix to the file (Rust
// flush_range; FlushViewOfFile on windows).
func msyncShared(data []byte) error {
	if err := unix.Msync(data, unix.MS_SYNC); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "msync: " + err.Error()}
	}
	return nil
}

// truncateFile sets the exact file extent (Rust set_len).
func truncateFile(f *os.File, size int64) error {
	if err := unix.Ftruncate(int(f.Fd()), size); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "ftruncate: " + err.Error()}
	}
	return nil
}

// dupFile duplicates the descriptor as one non-inheritable handle that
// the Mapping owns independently (Rust File::try_clone; the Windows
// arm uses DuplicateHandle with bInheritHandle=false).
func dupFile(f *os.File) (*os.File, error) {
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "dup: " + err.Error()}
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		unix.Close(fd)
		return nil, &format.Error{Code: format.CodeIO, Detail: "fcntl cloexec: " + err.Error()}
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

// statIdentity returns the device+inode pair of the retained handle
// (Rust regular_identity / BY_HANDLE_FILE_INFORMATION on windows).
func statIdentity(f *os.File) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}
