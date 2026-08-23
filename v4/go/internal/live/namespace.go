//go:build !windows

// Exact local namespace operations for live main and coordination files
// (Rust live_namespace.rs + publication/namespace.rs subset the sidecar
// needs). Main and sidecar final components are opened without following
// symlinks, must be regular files with one link, and every handle
// retains its opened descriptor identity; path re-checks compare the
// current path entry against the retained inode.

package live

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// fileIdentity is the retained local identity of one opened descriptor
// (Rust publication::namespace Identity): the device+inode pair. Path
// verification compares a fresh stat of the path entry against the
// retained identity with os.SameFile, and the single-link rule uses the
// hard-link count.
type fileIdentity struct {
	info os.FileInfo
}

// identityOf captures the retained identity of an open descriptor
// (Rust live_namespace::identity: retained_regular_identity with the
// single-link requirement).
func identityOf(f *os.File) (fileIdentity, error) {
	st, err := f.Stat()
	if err != nil {
		return fileIdentity{}, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if !st.Mode().IsRegular() {
		return fileIdentity{}, &format.Error{Code: format.CodeWrongState, Detail: "live artifact is not one regular file"}
	}
	if links, ok := mapping.RegularLinkCount(st); !ok || links != 1 {
		return fileIdentity{}, &format.Error{Code: format.CodeWrongState, Detail: "live artifact is not one regular file"}
	}
	return fileIdentity{info: st}, nil
}

// verifyPath re-checks that path still names the retained identity as
// one regular single-link file (Rust live_namespace::verify_path).
func verifyPath(path string, expected fileIdentity) error {
	now, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return &format.Error{Code: format.CodeNameNotFound, Detail: "live path removed while open"}
		}
		return &format.Error{Code: format.CodeIO, Detail: "lstat: " + err.Error()}
	}
	if !now.Mode().IsRegular() {
		return &format.Error{Code: format.CodeWrongState, Detail: "live path no longer names a regular file"}
	}
	if links, ok := mapping.RegularLinkCount(now); !ok || links != 1 {
		return &format.Error{Code: format.CodeWrongState, Detail: "live path identity changed"}
	}
	if !os.SameFile(now, expected.info) {
		return &format.Error{Code: format.CodeWrongState, Detail: "live path identity changed"}
	}
	return nil
}

// openRw opens the final path component without following symlinks and
// proves the retained identity (Rust live_namespace::open_rw).
func openRw(path string) (*os.File, fileIdentity, error) {
	clean := filepath.Clean(path)
	f, err := os.OpenFile(clean, os.O_RDWR|unix.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fileIdentity{}, &format.Error{Code: format.CodeNameNotFound, Detail: "live path does not exist"}
		}
		// O_NOFOLLOW on a symbolic link reports ELOOP: the final
		// component must not be a link (Rust Directory::open_regular
		// NotRegular class -> WrongMode -> WrongState).
		if errors.Is(err, unix.ELOOP) {
			return nil, fileIdentity{}, &format.Error{Code: format.CodeWrongState, Detail: "live path is not one regular file"}
		}
		return nil, fileIdentity{}, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	identity, err := identityOf(f)
	if err != nil {
		f.Close()
		return nil, fileIdentity{}, err
	}
	if err := verifyPath(clean, identity); err != nil {
		f.Close()
		return nil, fileIdentity{}, err
	}
	return f, identity, nil
}

// createPrivate creates one coordination artifact with creator-only
// access (0600, independent of umask) refusing an existing destination
// (Rust live_namespace::create_private + security::secure_creator_only
// POSIX profile). On failure after creation the artifact is removed
// exactly when the path still names the created inode.
func createPrivate(path string, authority cleanupAuthority) (*os.File, fileIdentity, error) {
	clean := filepath.Clean(path)
	f, err := os.OpenFile(clean, os.O_RDWR|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fileIdentity{}, &format.Error{Code: format.CodeNameExists, Detail: "live coordination artifact exists"}
		}
		// A missing parent directory reports NameNotFound, exactly like
		// Rust bind_path (NamespaceError::Missing).
		if os.IsNotExist(err) {
			return nil, fileIdentity{}, &format.Error{Code: format.CodeNameNotFound, Detail: "live coordination parent directory does not exist"}
		}
		// ELOOP (symlink loop in a parent component) and every other
		// errno stay CodeIO: Rust Directory::create special-cases only
		// EEXIST; the not-regular class applies to open_regular, not
		// to create.
		return nil, fileIdentity{}, &format.Error{Code: format.CodeIO, Detail: "create: " + err.Error()}
	}
	identity, err := identityOf(f)
	if err != nil {
		f.Close()
		// Rust cannot remove exactly without the identity; it records an
		// unresolvable cleanup and reports CleanupIncomplete.
		return nil, fileIdentity{}, combineErrors(err, &format.Error{
			Code:   format.CodeUnresolvable,
			Detail: "created live artifact has no proven local identity",
		})
	}
	// secure_creator_only core: the mode is exactly 0600 independent of
	// umask. ACL removal and the ownership commitment surface land with
	// the 4-3 creation-security slice.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		outcome := removeExact(clean, identity)
		cause := &format.Error{Code: format.CodeIO, Detail: "reader table ownership: " + err.Error()}
		return nil, fileIdentity{}, combineErrors(cause, outcome.cause)
	}
	if err := verifyPath(clean, identity); err != nil {
		f.Close()
		outcome := removeExact(clean, identity)
		return nil, fileIdentity{}, combineErrors(err, outcome.cause)
	}
	return f, identity, nil
}

// removeExact removes the path only when it still names the retained
// identity and synchronizes the parent directory (Rust
// live_cleanup::remove POSIX remove_exact: verify_name, unlink_exact,
// directory.sync, require_absent).
func removeExact(path string, expected fileIdentity) cleanupOutcome {
	clean := filepath.Clean(path)
	now, err := os.Lstat(clean)
	if err != nil {
		if os.IsNotExist(err) {
			return cleanupOutcomeFailed(&format.Error{Code: format.CodeNameNotFound, Detail: "live path removed before cleanup"})
		}
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeIO, Detail: "cleanup lstat: " + err.Error()})
	}
	if !now.Mode().IsRegular() {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeWrongState, Detail: "live artifact is not one regular file"})
	}
	if !os.SameFile(now, expected.info) {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeWrongState, Detail: "live artifact identity changed during cleanup"})
	}
	if links, ok := mapping.RegularLinkCount(now); !ok || links != 1 {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeWrongState, Detail: "live artifact link count changed during cleanup"})
	}
	if err := os.Remove(clean); err != nil {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeIO, Detail: "cleanup remove: " + err.Error()})
	}
	if err := syncParent(clean); err != nil {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeIO, Detail: "cleanup parent sync: " + err.Error()})
	}
	if _, err := os.Lstat(clean); err == nil {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeNameExists, Detail: "live artifact reappeared during cleanup"})
	} else if !os.IsNotExist(err) {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeIO, Detail: "cleanup recheck: " + err.Error()})
	}
	return cleanupOutcome{}
}

// syncParent synchronizes the parent directory of path for durability
// of a name change (Rust live_namespace::sync_parent).
func syncParent(path string) error {
	dir := filepath.Dir(path)
	f, err := os.Open(dir)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "open parent: " + err.Error()}
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "sync parent: " + err.Error()}
	}
	return nil
}
