//go:build netbsd

package mapping

import "github.com/firehol/iprange/v4/go/internal/format"

// RenameNoReplace is unavailable on NetBSD (Rust
// Directory::rename_noreplace: only linux, apple, and freebsd have a
// no-replace primitive). The writer classifies this refusal as the
// preparation failure with the attempt discarded, matching Rust's
// first-namespace-op Unsupported path, so no residue accumulates.
func RenameNoReplace(oldpath, newpath string, expectedDevice, expectedInode uint64) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "rename_noreplace is not available on this target"}
}
