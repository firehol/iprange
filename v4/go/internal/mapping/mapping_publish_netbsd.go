//go:build netbsd

package mapping

import "github.com/firehol/iprange/v4/go/internal/format"

// RenameNoReplace is unavailable on NetBSD (Rust
// Directory::rename_noreplace: only linux, apple, and freebsd have a
// no-replace primitive; Windows implements it through
// NtSetInformationFile). The staging writer classifies this refusal
// as the Rust acquire-failure result - NotPublished with the attempt
// discarded (attempt.rs from_private not_published), never a
// preparation failure - and surfaces the Rust-verbatim problem code
// DurabilityUnsupported as the public cause (problem.rs
// NamespaceError::Unsupported); this Go-internal platform marker
// never leaves the mapping owner. No residue accumulates.
func RenameNoReplace(oldpath, newpath string, expectedDevice, expectedInode uint64) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "rename_noreplace is not available on this target"}
}
