//go:build !windows

// Retained-directory machine (Rust publication/namespace/unix.rs
// Directory). Parent directories open with O_DIRECTORY|O_NOFOLLOW,
// must sit on a durability-approved local filesystem with a proven
// name_max, and every name operation executes against the retained
// directory descriptor (fstatat/openat/unlinkat) so a path-swap race
// cannot redirect it. The single-link and cross-filesystem rules are
// enforced per operation exactly like Rust. The type is exported:
// internal/publication composes it (Rust Directory is pub(crate) to
// the publication module; Go has no pub(crate), so the live package
// exports the authority and publication composes it).

package live

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// Entry is one retained directory entry (Rust namespace::Entry):
// identity, link count, and regular-file classification.
type Entry struct {
	Identity FileIdentity
	Links    uint64
	Regular  bool
}

// Directory is one open retained directory (Rust Directory).
type Directory struct {
	file    *os.File
	id      FileIdentity
	nameMax int
}

// Close releases the retained directory descriptor.
func (d *Directory) Close() { _ = d.file.Close() }

// Identity returns the retained directory identity.
func (d *Directory) Identity() FileIdentity { return d.id }

// OpenDirectory binds one directory with O_DIRECTORY|O_NOFOLLOW, proves
// it is a directory on a durability-approved local filesystem, and
// captures its name_max (Rust Directory::open: open, is_dir,
// require_local_filesystem, fpathconf). A missing path is the Missing
// class; every other open failure stays the Io class exactly like
// Rust, which special-cases only NotFound.
func OpenDirectory(path string) (*Directory, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nsMissingError()
		}
		return nil, nsPlainIoError("open directory", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, nsPlainIoError("inspect directory", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		f.Close()
		return nil, nsNotDirectoryError()
	}
	if err := requireLocalFilesystem(f); err != nil {
		f.Close()
		return nil, err
	}
	nameMax, err := directoryNameMax(f)
	if err != nil || nameMax <= 0 {
		f.Close()
		return nil, nsUnsupportedError()
	}
	return &Directory{
		file:    f,
		id:      FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)},
		nameMax: nameMax,
	}, nil
}

// Entry inspects one name without following a final symlink (Rust
// Directory::entry fstatat AT_SYMLINK_NOFOLLOW); the bool reports an
// absent name.
func (d *Directory) Entry(name string) (Entry, bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(d.file.Fd()), name, &st, atNofollow)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return Entry{}, false, nil
		}
		return Entry{}, false, nsIoError("inspect retained name", err)
	}
	return Entry{
		Identity: FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)},
		Links:    uint64(st.Nlink),
		Regular:  st.Mode&unix.S_IFMT == unix.S_IFREG,
	}, true, nil
}

// Create creates one name exclusively with creator-only mode (Rust
// Directory::create: require_name_lengths, openat O_CREAT|O_EXCL with
// O_NOFOLLOW and 0600). EEXIST is the Exists class; an overlong name
// fails the name_max proof as InvalidName before any syscall.
func (d *Directory) Create(name string) (*os.File, error) {
	if err := d.RequireNameLengths(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(d.file.Fd()), name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		security.CreatorMode)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, nsExistsError()
		}
		return nil, nsIoError("create private file", err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

// OpenRegular opens one name without following symlinks and proves the
// retained regular identity with the single-link and cross-filesystem
// rules (Rust Directory::open_regular_with_links + regular_identity).
// An absent name reports (nil, nil).
func (d *Directory) OpenRegular(name string, writable bool) (*RegularFile, error) {
	return d.openRegularWithLinks(name, writable, true, "open retained file")
}

func (d *Directory) openRegularWithLinks(name string, writable bool, requireSingleLink bool, operation string) (*RegularFile, error) {
	access := unix.O_RDONLY
	if writable {
		access = unix.O_RDWR
	}
	fd, err := unix.Openat(int(d.file.Fd()), name,
		access|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		if isNofollowSymlink(err) {
			return nil, nsNotRegularError()
		}
		return nil, nsIoError(operation, err)
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, nsPlainIoError("inspect retained file", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		f.Close()
		return nil, nsNotRegularError()
	}
	if uint64(st.Dev) != d.id.device {
		f.Close()
		return nil, nsCrossFilesystemError()
	}
	if requireSingleLink && st.Nlink != 1 {
		f.Close()
		return nil, nsLinkCountError(uint64(st.Nlink))
	}
	return &RegularFile{
		File:     f,
		Identity: FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)},
	}, nil
}

// RequireAbsent refuses a name that exists (Rust Directory::require_absent).
func (d *Directory) RequireAbsent(name string) error {
	_, found, err := d.Entry(name)
	if err != nil {
		return err
	}
	if found {
		return nsExistsError()
	}
	return nil
}

// VerifyName proves the name still names the expected identity as one
// regular single-link file (Rust Directory::verify_name).
func (d *Directory) VerifyName(name string, expected FileIdentity) error {
	found, present, err := d.Entry(name)
	if err != nil {
		return err
	}
	if !present {
		return nsMissingError()
	}
	if !found.Regular {
		return nsNotRegularError()
	}
	if found.Identity != expected {
		return nsIdentityChangedError()
	}
	if found.Links != 1 {
		return nsLinkCountError(found.Links)
	}
	return nil
}

// UnlinkExact removes one name only when it still names the expected
// identity (Rust Directory::unlink_exact). An absent name reports
// (false, nil).
func (d *Directory) UnlinkExact(name string, expected FileIdentity) (bool, error) {
	found, present, err := d.Entry(name)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	if !found.Regular {
		return false, nsNotRegularError()
	}
	if found.Identity != expected {
		return false, nsIdentityChangedError()
	}
	if found.Links != 1 {
		return false, nsLinkCountError(found.Links)
	}
	if err := unix.Unlinkat(int(d.file.Fd()), name, 0); err != nil {
		return false, nsIoError("unlink exact file", err)
	}
	return true, nil
}

// Sync synchronizes the directory (Rust Directory::sync). EINVAL is
// the Unsupported class: filesystems that cannot sync a directory
// cannot prove name durability.
func (d *Directory) Sync() error {
	if err := unix.Fsync(int(d.file.Fd())); err != nil {
		if errors.Is(err, unix.EINVAL) {
			return nsUnsupportedError()
		}
		return nsIoError("synchronize retained directory", err)
	}
	return nil
}

// RequireNameLengths proves every name fits the directory name_max
// (Rust Directory::require_name_lengths).
func (d *Directory) RequireNameLengths(names ...string) error {
	for _, name := range names {
		if len(name) > d.nameMax {
			return nsInvalidNameError()
		}
	}
	return nil
}

// Verify proves the retained directory still names the same directory
// on the same local filesystem (Rust Directory::verify: metadata,
// is_dir + identity, require_local_filesystem). Metadata failures stay
// the plain Io class, the wrong directory is IdentityChanged.
func (d *Directory) Verify() error {
	var st unix.Stat_t
	if err := unix.Fstat(int(d.file.Fd()), &st); err != nil {
		return nsPlainIoError("inspect directory", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR ||
		uint64(st.Dev) != d.id.device || uint64(st.Ino) != d.id.inode {
		return nsIdentityChangedError()
	}
	return requireLocalFilesystem(d.file)
}

// Scan visits every entry of the retained directory in constant memory
// (Rust Directory::scan over namespace_scan.rs): the directory proves
// before and after the readdir stream, "." and ".." are skipped, and
// the visitor receives each raw name. Stream failures are the IoAt
// classes of the exact Rust operation labels. The final verify runs
// even when the visitor failed and takes precedence over its error,
// exactly like Rust.
func (d *Directory) Scan(visitor func([]byte) error) error {
	if err := d.Verify(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(d.file.Fd()), ".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nsIoError("open retained directory stream", err)
	}
	defer unix.Close(fd)
	visited := scanDirStream(fd, visitor)
	if err := d.Verify(); err != nil {
		return err
	}
	return visited
}

// RegularFile is one opened retained regular file (Rust namespace::Regular).
type RegularFile struct {
	File     *os.File
	Identity FileIdentity
}

// validNameComponent proves one component is a valid Name (Rust
// Name::new): not empty, not "." or "..", no separator, no NUL byte.
func validNameComponent(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, '/') || strings.IndexByte(name, 0) >= 0 {
		return nsInvalidNameError()
	}
	return nil
}
