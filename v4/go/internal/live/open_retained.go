package live

// Retained-directory file opens (Rust live_namespace::open_rw and its
// recovery-side consumers): the caller names a path, and the opened
// descriptor is judged only through the retained parent-directory
// handle. Binding the parent first is what makes the refusal classes
// match Rust: Directory::open proves the parent is a directory on a
// durability-approved local filesystem before the name is opened, so a
// path whose directory lives on a non-local filesystem (for example a
// device node under /dev) is the DurabilityUnsupported class rather
// than the wrong-mode class the node itself would produce.

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// OpenRetainedReadWrite opens one path read-write through its retained
// parent directory (Rust live_namespace::open_rw): the parent bind, the
// no-follow non-blocking open, and the regular-file, same-filesystem
// and single-link proofs. Every namespace class maps through the
// canonical table; an absent name is the missing-name class.
func OpenRetainedReadWrite(path string) (*os.File, error) {
	dir, name, err := bindPath(path)
	if err != nil {
		return nil, nsMap(err)
	}
	defer dir.Close()
	regular, err := dir.OpenRegular(name, true)
	if err != nil {
		return nil, nsMap(err)
	}
	if regular == nil {
		return nil, &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	}
	return regular.File, nil
}
