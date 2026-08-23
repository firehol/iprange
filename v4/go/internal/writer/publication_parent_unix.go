//go:build !windows

package writer

// Destination-parent proof, POSIX variant (Rust publication/namespace/
// unix.rs Directory::open): the parent is opened with O_DIRECTORY |
// O_NOFOLLOW, so a symlink parent surfaces ELOOP and any other
// non-directory surfaces ENOTDIR, both folding to NamespaceError::Io and
// the generic IO problem class. The Rust NotDirectory -> Conflict arm is
// unreachable from a path open on POSIX; the Windows variant keeps that
// arm (windows.rs NotDirectory is reachable there).

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// CheckPublicationParent proves one destination parent is a plain
// directory and classifies every refusal exactly like Rust
// Destination::bind -> Directory::open (publication/namespace.rs): a
// missing parent is Missing -> NameNotFound "publication name is
// missing", any other stat failure is the IO class, and every
// non-directory parent is the IO class with the exact Rust detail
// "publication filesystem operation failed" (ELOOP/ENOTDIR ->
// NamespaceError::Io). nil means the parent is a plain directory.
func CheckPublicationParent(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
		}
		return &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	if !fi.IsDir() {
		return &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return nil
}
