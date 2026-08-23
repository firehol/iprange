//go:build !windows

// Retained-directory machine (Rust publication/namespace/unix.rs
// Directory). Parent directories open with O_DIRECTORY|O_NOFOLLOW,
// must sit on a durability-approved local filesystem with a proven
// name_max, and every name operation executes against the retained
// directory descriptor (fstatat/openat/unlinkat) so a path-swap race
// cannot redirect it. The single-link and cross-filesystem rules are
// enforced per operation exactly like Rust.

package live

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// dirEntry is one retained directory entry (Rust namespace::Entry):
// identity, link count, and regular-file classification.
type dirEntry struct {
	identity FileIdentity
	links    uint64
	regular  bool
}

// directory is one open retained directory (Rust Directory).
type directory struct {
	file    *os.File
	id      FileIdentity
	nameMax int
}

func (d *directory) close() { _ = d.file.Close() }

func (d *directory) identity() FileIdentity { return d.id }

// openDirectory binds one directory with O_DIRECTORY|O_NOFOLLOW, proves
// it is a directory on a durability-approved local filesystem, and
// captures its name_max (Rust Directory::open: open, is_dir,
// require_local_filesystem, fpathconf). A missing path is the Missing
// class; every other open failure stays the Io class exactly like
// Rust, which special-cases only NotFound.
func openDirectory(path string) (*directory, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nsMissingError()
		}
		return nil, nsIoError("open directory", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, nsIoError("inspect directory", err)
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
	return &directory{
		file:    f,
		id:      FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)},
		nameMax: nameMax,
	}, nil
}

// entry inspects one name without following a final symlink (Rust
// Directory::entry fstatat AT_SYMLINK_NOFOLLOW); the bool reports an
// absent name.
func (d *directory) entry(name string) (dirEntry, bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(d.file.Fd()), name, &st, atNofollow)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return dirEntry{}, false, nil
		}
		return dirEntry{}, false, nsIoError("inspect retained name", err)
	}
	return dirEntry{
		identity: FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)},
		links:    uint64(st.Nlink),
		regular:  st.Mode&unix.S_IFMT == unix.S_IFREG,
	}, true, nil
}

// create creates one name exclusively with creator-only mode (Rust
// Directory::create: require_name_lengths, openat O_CREAT|O_EXCL with
// O_NOFOLLOW and 0600). EEXIST is the Exists class; an overlong name
// fails the name_max proof as InvalidName before any syscall.
func (d *directory) create(name string) (*os.File, error) {
	if err := d.requireNameLengths(name); err != nil {
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

// openRegular opens one name without following symlinks and proves the
// retained regular identity with the single-link and cross-filesystem
// rules (Rust Directory::open_regular_with_links + regular_identity).
// An absent name reports (nil, nil).
func (d *directory) openRegular(name string, writable bool) (*regularFile, error) {
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
		return nil, nsIoError("open retained file", err)
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, nsIoError("inspect retained file", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		f.Close()
		return nil, nsNotRegularError()
	}
	if uint64(st.Dev) != d.id.device {
		f.Close()
		return nil, nsCrossFilesystemError()
	}
	if st.Nlink != 1 {
		f.Close()
		return nil, nsLinkCountError()
	}
	return &regularFile{
		file:     f,
		identity: FileIdentity{device: uint64(st.Dev), inode: uint64(st.Ino)},
	}, nil
}

// requireAbsent refuses a name that exists (Rust Directory::require_absent).
func (d *directory) requireAbsent(name string) error {
	_, found, err := d.entry(name)
	if err != nil {
		return err
	}
	if found {
		return nsExistsError()
	}
	return nil
}

// verifyName proves the name still names the expected identity as one
// regular single-link file (Rust Directory::verify_name).
func (d *directory) verifyName(name string, expected FileIdentity) error {
	found, present, err := d.entry(name)
	if err != nil {
		return err
	}
	if !present {
		return nsMissingError()
	}
	if !found.regular {
		return nsNotRegularError()
	}
	if found.identity != expected {
		return nsIdentityChangedError()
	}
	if found.links != 1 {
		return nsLinkCountError()
	}
	return nil
}

// unlinkExact removes one name only when it still names the expected
// identity (Rust Directory::unlink_exact). An absent name reports
// (false, nil).
func (d *directory) unlinkExact(name string, expected FileIdentity) (bool, error) {
	found, present, err := d.entry(name)
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	if !found.regular {
		return false, nsNotRegularError()
	}
	if found.identity != expected {
		return false, nsIdentityChangedError()
	}
	if found.links != 1 {
		return false, nsLinkCountError()
	}
	if err := unix.Unlinkat(int(d.file.Fd()), name, 0); err != nil {
		return false, nsIoError("unlink exact file", err)
	}
	return true, nil
}

// sync synchronizes the directory (Rust Directory::sync). EINVAL is
// the Unsupported class: filesystems that cannot sync a directory
// cannot prove name durability.
func (d *directory) sync() error {
	if err := d.file.Sync(); err != nil {
		if errors.Is(err, unix.EINVAL) {
			return nsUnsupportedError()
		}
		return nsIoError("synchronize retained directory", err)
	}
	return nil
}

// requireNameLengths proves every name fits the directory name_max
// (Rust Directory::require_name_lengths).
func (d *directory) requireNameLengths(names ...string) error {
	for _, name := range names {
		if len(name) > d.nameMax {
			return nsInvalidNameError()
		}
	}
	return nil
}

// regularFile is one opened retained regular file (Rust namespace::Regular).
type regularFile struct {
	file     *os.File
	identity FileIdentity
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
