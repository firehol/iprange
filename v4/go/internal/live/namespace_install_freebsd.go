//go:build freebsd

// FreeBSD no-replace publication without a rename-noreplace primitive:
// the crash-safe linkat transition machine (Rust namespace_mutation.rs
// freebsd arm). The source is hard-linked to the destination, the pair
// is synced, the private alias is unlinked, and the directory is synced
// again; every step is preceded and followed by exact identity proofs,
// and the four named crash points are the same observable states the
// Rust worker records. The exchange primitive is Unsupported on
// freebsd (Rust exchange is linux/apple only); plain renaming is the
// Rust freebsd replace_discarding_destination arm.

package live

import (
	"os"

	"golang.org/x/sys/unix"
)

// RenameNoReplace publishes source at destination without replacement
// through the linkat transition machine (Rust Directory::
// rename_noreplace freebsd arm: regular_identity_any_link,
// require_source, linkat, then finish_noreplace_transition; an EEXIST
// resumption folds through the observed link state).
func (d *Directory) RenameNoReplace(source string, sourceFile *os.File, destination string) error {
	return d.linkNoReplace(source, sourceFile, destination)
}

// RenameExchange reports the no-primitive class (Rust exchange is
// linux/apple only; freebsd refuses).
func (d *Directory) RenameExchange(source, destination string) error {
	return nsUnsupportedError()
}

// RenamePlain renames source to destination replacing an existing
// destination (renameat(2); Rust
// replace_discarding_destination freebsd arm). ENOENT is the Missing
// class, every other failure is the Io class.
func (d *Directory) RenamePlain(source, destination string) error {
	err := unix.Renameat(int(d.file.Fd()), source, int(d.file.Fd()), destination)
	return renameNamespaceResult(err, unix.ENOENT, nsMissingError(), "atomically replace and discard publication destination")
}
