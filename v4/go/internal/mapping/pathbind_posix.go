//go:build !windows

package mapping

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/fslocal"
	"github.com/firehol/iprange/v4/go/internal/pathname"
)

// POSIX primitives of the read-side path proof. The parent directory is
// opened O_DIRECTORY | O_NOFOLLOW and the final component is resolved
// under that bound descriptor with AT_SYMLINK_NOFOLLOW, so the proof
// observes exactly the namespace entry the caller named and follows no
// symlink at either step (Rust publication::namespace::bind_path +
// Directory::entry, reached through live_namespace::verify_path_inner).
// The two proofs that use them differ only in the link rule: the live
// arms require one link, the immutable reader arms accept any.

// bindParent opens the parent directory of path without following a
// symlink in its final component (Rust Directory::open inside
// bind_path). The returned descriptor stays open for the caller's
// operation. A missing parent is the name-not-found class, every other
// open failure — including the ENOTDIR that a magic symlink such as
// /proc/self produces — is the plain io class, matching the Rust
// NamespaceError fold of Directory::open.
func bindParent(parent string) (*os.File, error) {
	// calleropen.Open, not os.OpenFile: os.OpenFile marks every opened
	// handle poller-attached on Linux, and initializing the netpoller
	// under a low RLIMIT_NOFILE aborts the process instead of answering
	// io (see internal/calleropen).
	f, err := calleropen.Open(parent, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &format.Error{Code: format.CodeNameNotFound, Detail: "path does not exist"}
		}
		return nil, &format.Error{Code: format.CodeIO, Detail: "open parent directory: " + err.Error()}
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, &format.Error{Code: format.CodeIO, Detail: "inspect parent directory: " + err.Error()}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		f.Close()
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "parent is not a directory"}
	}
	// Rust Directory::open proves the local-filesystem durability
	// contract immediately after the directory check, so a name under a
	// filesystem that cannot carry the live contract (procfs, sysfs,
	// network mounts) is refused with the durability class rather than
	// with whatever the name open happens to report.
	if err := fslocal.RequireLocal(f); errors.Is(err, fslocal.ErrNotLocal) {
		f.Close()
		return nil, &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "filesystem lacks required durable namespace operations"}
	} else if err != nil {
		f.Close()
		return nil, &format.Error{Code: format.CodeIO, Detail: "inspect publication filesystem: " + err.Error()}
	}
	return f, nil
}

// proveLocalNamespace runs the containing-directory proofs Rust's
// bind_path applies before live_namespace::open_rw opens the name. Only
// the durability refusal is reported here: every other bind outcome is
// left to the name open so the classes the arms already report (missing
// parent, non-directory parent, symlinked parent) do not move.
func proveLocalNamespace(path string) error {
	parent, ok := pathname.Parent(path)
	if !ok {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "path has no parent directory"}
	}
	dir, err := bindParent(parent)
	if err != nil {
		var fe *format.Error
		if errors.As(err, &fe) && fe.Code == format.CodeDurabilityUnsupported {
			return fe
		}
		return nil
	}
	defer dir.Close()
	return nil
}

// statAt resolves one final component under a bound parent descriptor
// without following symlinks (Rust Directory::entry). A vanished entry
// is reported as not found; any other failure is the plain io class.
func statAt(dir *os.File, name string) (unix.Stat_t, bool, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return st, true, nil
	} else if err != unix.ENOENT {
		return st, false, &format.Error{Code: format.CodeIO, Detail: "stat path entry: " + err.Error()}
	}
	return st, false, nil
}

// verifyPathAgainstFile is the platform arm of VerifyPathAgainstFile:
// the identity of the opened descriptor is compared against the entry
// the path names under its bound parent, with the any-link rule of the
// immutable reader arms (Rust verify_path_any_link).
func verifyPathAgainstFile(path string, f *os.File) error {
	device, inode, err := statIdentity(f)
	if err != nil {
		return err
	}
	parent, ok := pathname.Parent(path)
	if !ok {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "path has no parent directory"}
	}
	name, ok := pathname.FileName(path)
	if !ok {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "path has no file name"}
	}
	dir, err := bindParent(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	st, found, err := statAt(dir, name)
	if err != nil {
		return err
	}
	if !found {
		return &format.Error{Code: format.CodeNameNotFound, Detail: "path does not exist"}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return &format.Error{Code: format.CodeWrongState, Detail: "path does not name a regular file"}
	}
	if uint64(st.Dev) != device || uint64(st.Ino) != inode {
		return &format.Error{Code: format.CodeWrongState, Detail: "path no longer names the opened file"}
	}
	return nil
}
