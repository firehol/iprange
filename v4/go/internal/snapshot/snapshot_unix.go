// Device+inode capture for the live snapshot self-replacement probe
// (Rust publication namespace identity over the open file). This file
// provides the POSIX variant; the windows variant lives in
// snapshot_windows.go and is the real probe over the retained
// destination handle.

//go:build !windows

package snapshot

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"golang.org/x/sys/unix"
)

// openDestinationNoFollow opens the destination main name without
// following a final symlink (Rust Directory::open_regular O_NOFOLLOW).
// O_NONBLOCK makes a FIFO swapped in between the caller's lstat and
// this open return immediately instead of blocking until a writer
// appears; the authoritative-fd regular check then refuses the fifo
// with the same conflict class the caller already returns for a
// deterministic non-regular destination (regular files ignore
// O_NONBLOCK, so publication behavior is unchanged).
func openDestinationNoFollow(path string) (*os.File, error) {
	file, err := calleropen.Open(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		// Rust open_regular_with_links classifies the open failure by
		// its errno: the no-follow refusal of a final symlink is
		// NotRegular (the same Conflict detail the opened-descriptor
		// check gives), while any other failure — the ENXIO of an
		// AF_UNIX socket, the ENOENT of a vanished name — propagates as
		// the io class and the caller folds the absent case to "not a
		// rejection".
		if live.IsNofollowSymlink(err) {
			return nil, &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
	}
	return file, nil
}

// fileIdentityOf captures the device+inode of one open descriptor.
func fileIdentityOf(f *os.File) (uint64, uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

// directoryIdentityOf proves the destination parent and captures its
// device+inode (Rust Destination::bind Directory::open identity; the
// reject_live_self same-filesystem rule compares the destination file
// against it).
//
// The proof is the engine's one authoritative directory bind
// (live.OpenDirectory mirrors Rust Directory::open: open with
// O_DIRECTORY|O_NOFOLLOW, the directory check, require_local_filesystem,
// name_max) and it must run before the destination node is classified
// by openDestinationNoFollow. A destination under a filesystem that
// cannot carry the live contract (procfs, sysfs, tmpfs, a network mount)
// is then the durability refusal at every node shape, which is what
// binary-format-v4.md requires of a replacement publication: classifying
// the node first would answer the namespace collision class for a
// directory, FIFO, or symlink that happens to already sit at the
// destination name, downgrading the durability refusal to a conflict
// over a filesystem the exchange can never be durable on.
func directoryIdentityOf(path string) (device uint64, inode uint64, err error) {
	dir, err := live.OpenDirectory(path)
	if err != nil {
		// The bind's own vocabulary decides the class here, with the same
		// fold publication.namespaceProblem applies at the machine
		// boundary: the unsupported kind (a filesystem outside the
		// durability whitelist, or one whose name_max cannot be proved)
		// is the durability refusal, and every other bind failure keeps
		// the plain io class the path probe gave it before.
		if nerr, ok := live.AsNamespaceError(err); ok && nerr.Kind == live.NamespaceUnsupported {
			return 0, 0, &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "filesystem lacks required durable namespace operations"}
		}
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	dir.Close()
	// The bind proved this name is a qualified directory; the numbers
	// the cross-filesystem rule compares are taken from that same name.
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}

// fileLinksOf reports the link count of one open descriptor (Rust
// regular_identity nlink rule: a publication destination must have
// exactly one link).
func fileLinksOf(f *os.File) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return uint64(st.Nlink), nil
}
