//go:build linux

// Atomic no-replace and exchange renames on the retained directory
// descriptor (Rust publication/namespace_mutation.rs Directory::
// rename_noreplace RENAME_NOREPLACE and exchange RENAME_EXCHANGE).
// The syscall runs against the retained directory fd, so a path-swap
// race cannot redirect the operation. The source file parameter is
// required by the freebsd linkat arm of the machine; linux ignores it
// like Rust, which passes the file only for the freebsd arm.

package live

import (
	"os"

	"golang.org/x/sys/unix"
)

// RenameNoReplace atomically renames source to destination only when
// destination does not exist (renameat2 RENAME_NOREPLACE; Rust
// Directory::rename_noreplace). EEXIST is the Exists class, the
// no-primitive family is Unsupported, everything else is the Io class.
func (d *Directory) RenameNoReplace(source string, _ *os.File, destination string) error {
	err := unix.Renameat2(int(d.file.Fd()), source, int(d.file.Fd()), destination, unix.RENAME_NOREPLACE)
	return renameNamespaceResult(err, unix.EEXIST, nsExistsError(), "publish name without replacement")
}

// RenameExchange atomically exchanges source and destination
// (renameat2 RENAME_EXCHANGE; Rust Directory::exchange). ENOENT is the
// Missing class, the no-primitive family is Unsupported, everything
// else is the Io class.
func (d *Directory) RenameExchange(source, destination string) error {
	err := unix.Renameat2(int(d.file.Fd()), source, int(d.file.Fd()), destination, unix.RENAME_EXCHANGE)
	return renameNamespaceResult(err, unix.ENOENT, nsMissingError(), "atomically exchange publication names")
}

// RenamePlain renames source to destination replacing an existing
// destination (renameat(2); Rust
// replace_discarding_destination). ENOENT is the Missing class, the
// no-primitive family is Unsupported, everything else is the Io class.
func (d *Directory) RenamePlain(source, destination string) error {
	err := unix.Renameat(int(d.file.Fd()), source, int(d.file.Fd()), destination)
	return renameNamespaceResult(err, unix.ENOENT, nsMissingError(), "atomically replace and discard publication destination")
}
