//go:build windows

// Exact local namespace operations for live main and coordination
// files on Windows (Rust live_namespace.rs over the retained-directory
// machine in publication/namespace/windows.rs). Main and sidecar final
// components are opened through the retained parent directory and
// never follow a reparse point, must be regular files with one link on
// the same NTFS volume, and every handle retains its opened identity;
// path re-checks compare the current path entry against the retained
// identity. The identity pair is the volume serial and file index
// projection documented in directory_windows.go.

package live

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// FileIdentity is the retained local identity of one opened descriptor
// (Rust publication::namespace Identity): the volume serial and file
// index pair on Windows. Path verification compares a fresh handle
// inspection of the path entry against the retained identity, and the
// single-link rule uses the hard-link count.
type FileIdentity struct {
	device uint64
	inode  uint64
}

// identityOf captures the retained identity of an open descriptor
// (Rust live_namespace::identity: retained_regular_identity with the
// single-link requirement; the wrong-mode classes fold to the Rust
// ownership-changed detail).
func identityOf(f *os.File) (FileIdentity, error) {
	info, err := handleInfo(f)
	if err != nil {
		return FileIdentity{}, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || info.NumberOfLinks != 1 {
		return FileIdentity{}, &format.Error{Code: format.CodeWrongState, Detail: "live file ownership changed"}
	}
	return identityFromInfo(info), nil
}

// parentOf mirrors Rust Path::parent: a single-component path has the
// empty parent (whose open reports Missing), "." and ".." have no
// parent at all.
func parentOf(clean string) (string, error) {
	if clean == "." || clean == ".." {
		return "", &format.Error{Code: format.CodeInvalidArgument, Detail: "database path has no parent directory"}
	}
	if !strings.ContainsRune(clean, filepath.Separator) {
		return "", nil
	}
	return filepath.Dir(clean), nil
}

// bindPath binds the parent directory of path and its final component
// (Rust live_namespace::bind_path: Path::parent/file_name,
// Directory::open, Name::from_component with the Windows component
// rules).
func bindPath(path string) (*Directory, string, error) {
	clean := filepath.Clean(path)
	parent, err := parentOf(clean)
	if err != nil {
		return nil, "", err
	}
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", &format.Error{Code: format.CodeInvalidArgument, Detail: "database path has no file name"}
	}
	dir, err := OpenDirectory(parent)
	if err != nil {
		return nil, "", err
	}
	if err := validWindowsNameComponent(name); err != nil {
		dir.Close()
		return nil, "", err
	}
	return dir, name, nil
}

// validWindowsNameComponent proves one component is a valid Windows
// Name (Rust Name::new windows arm): not empty, not "." or "..", only
// ASCII characters, no NUL, slash, backslash, or colon.
func validWindowsNameComponent(name string) error {
	if name == "" || name == "." || name == ".." {
		return nsInvalidNameError()
	}
	for i := 0; i < len(name); i++ {
		letter := name[i]
		if letter > 0x7F || letter == 0 || letter == '/' || letter == '\\' || letter == ':' {
			return nsInvalidNameError()
		}
	}
	return nil
}

// verifyPath re-checks that path still names the retained identity as
// one regular single-link file (Rust live_namespace::verify_path).
func verifyPath(path string, expected FileIdentity) error {
	return verifyPathInner(path, expected, true)
}

// verifyPathInner re-checks that path still names the retained identity
// as one regular file (Rust live_namespace::verify_path_inner); the
// caller selects the single-link rule (verify_path) or the any-link
// rule (verify_path_any_link, the validation source).
func verifyPathInner(path string, expected FileIdentity, requireSingleLink bool) error {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		return nsMap(err)
	}
	defer dir.Close()
	entry, found, err := dir.Entry(name)
	if err != nil {
		return nsMap(err)
	}
	if !found {
		return &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	}
	if !entry.Regular {
		return &format.Error{Code: format.CodeWrongState, Detail: "live path no longer names a regular file"}
	}
	if (requireSingleLink && entry.Links != 1) || entry.Identity != expected {
		return &format.Error{Code: format.CodeWrongState, Detail: "live path identity changed"}
	}
	return nil
}

// openRw opens the final path component read-write without following
// reparse points and proves the retained regular identity (Rust
// live_namespace::open_rw: bind_path + Directory::open_regular with
// the single-link and cross-volume rules).
func openRw(path string) (*os.File, FileIdentity, error) {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		return nil, FileIdentity{}, nsMap(err)
	}
	defer dir.Close()
	regular, err := dir.OpenRegular(name, true)
	if err != nil {
		return nil, FileIdentity{}, nsMap(err)
	}
	if regular == nil {
		return nil, FileIdentity{}, &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	}
	return regular.File, regular.Identity, nil
}

// createPrivate creates one coordination artifact with the protected
// creator-only descriptor refusing an existing destination (Rust
// live_namespace::create_private + security::secure_creator_only
// windows arm). A nil failure returns the created artifact; a
// non-nil failure reports the exact Rust facts (cause, cleanup outcome,
// and the identity of the artifact when it was proven and then failed
// the creator-only proof).
func createPrivate(path string, authority cleanupAuthority) (createdPrivate, *privateCreationFailure) {
	clean := filepath.Clean(path)
	cleanFailure := func(cause error) *privateCreationFailure {
		return &privateCreationFailure{cause: cause}
	}
	dir, name, err := bindPath(clean)
	if err != nil {
		return createdPrivate{}, cleanFailure(nsMap(err))
	}
	defer dir.Close()
	// The creator profile is captured before creation (Rust
	// live_namespace::create_private captures before Directory.create).
	profile, err := security.Capture()
	if err != nil {
		return createdPrivate{}, cleanFailure(err)
	}
	f, err := dir.CreateSecured(name, profile)
	if err != nil {
		return createdPrivate{}, cleanFailure(nsMap(err))
	}
	identity, err := identityOf(f)
	if err != nil {
		f.Close()
		return createdPrivate{}, &privateCreationFailure{
			cause:   err,
			cleanup: cleanupOutcomeFailed(&format.Error{Code: format.CodeUnresolvable, Detail: "created live artifact has no proven local identity"}),
		}
	}
	// Creator-only surface: the protected descriptor must commit to the
	// captured profile (Rust security::secure_creator_only windows arm).
	// On failure the artifact is removed exactly when the path still
	// names the created inode; the removal outcome and the proven
	// identity are retained for the caller fold, exactly like the Rust
	// and POSIX flows.
	if err := security.SecureCreatorOnly(f, profile); err != nil {
		f.Close()
		return createdPrivate{}, &privateCreationFailure{
			cause:    liveSecurityError(err),
			cleanup:  removeExact(clean, identity),
			identity: &identity,
		}
	}
	return createdPrivate{file: f, identity: identity}, nil
}

// removeExact removes the path only when it still names the retained
// identity and re-proves the parent directory (Rust
// live_cleanup::remove windows remove_exact: verify_name,
// unlink_exact, Directory.sync, require_absent).
func removeExact(path string, expected FileIdentity) cleanupOutcome {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	defer dir.Close()
	if err := dir.VerifyName(name, expected); err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	removed, err := dir.UnlinkExact(name, expected)
	if err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	if !removed {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"})
	}
	if err := dir.Sync(); err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	if err := dir.RequireAbsent(name); err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	return cleanupOutcome{}
}

// syncParent re-proves the parent directory of path for durability of
// a name change (Rust live_namespace::sync_parent windows arm:
// Directory::sync = verify; Windows exposes no directory name-sync
// primitive).
func syncParent(path string) error {
	clean := filepath.Clean(path)
	parent, err := parentOf(clean)
	if err != nil {
		return err
	}
	dir, err := OpenDirectory(parent)
	if err != nil {
		return nsMap(err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return nsMap(err)
	}
	return nil
}

// publicIdentity converts a retained local identity into the portable
// device+inode pair reported by the SDK (Rust
// live_namespace::public_identity; the SDK models a local identity as
// its device and inode numbers).
func publicIdentity(identity FileIdentity) (device uint64, inode uint64) {
	return identity.device, identity.inode
}

// parentIdentity captures the identity of the parent directory of path
// (Rust live_namespace::parent_identity: Directory::open; a missing
// parent reports the Io(NotFound) class, unlike the namespace helpers
// that map Missing to NameNotFound).
func parentIdentity(path string) (FileIdentity, error) {
	clean := filepath.Clean(path)
	parent, err := parentOf(clean)
	if err != nil {
		return FileIdentity{}, err
	}
	dir, err := OpenDirectory(parent)
	if err != nil {
		return FileIdentity{}, nsMapParentIdentity(err)
	}
	defer dir.Close()
	return dir.id, nil
}

// pathIdentity reports the identity of the path entry when it is one
// regular single-link file, nil when it is absent, and WrongMode
// otherwise (Rust live_namespace::path_identity).
func pathIdentity(path string) (*FileIdentity, error) {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		if nerr, ok := AsNamespaceError(err); ok && nerr.Kind == NamespaceMissing {
			return nil, nil
		}
		return nil, nsMap(err)
	}
	defer dir.Close()
	found, present, err := dir.Entry(name)
	if err != nil {
		return nil, nsMap(err)
	}
	if !present {
		return nil, nil
	}
	if !found.Regular || found.Links != 1 {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "live path is not one regular file"}
	}
	identity := found.Identity
	return &identity, nil
}
