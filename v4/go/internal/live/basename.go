// One platform basename copied into a fixed 512-byte buffer (Rust
// live_writer/result.rs LocalBasename): the raw bytes plus their
// encoding tag.  On POSIX the copy is allocation-free; on Windows
// the UTF-16LE encoding pass allocates bounded scratch.

package live

import (
	"runtime"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/pathname"
)

// maxBasenameBytes is the portable result bound (Rust
// MAX_BASENAME_BYTES).
const maxBasenameBytes = 512

// LocalBasename is one platform basename (Rust LocalBasename).
type LocalBasename struct {
	encoding uint16
	length   uint16
	bytes    [maxBasenameBytes]byte
}

// rustFileName returns the final path component the way Rust
// Path::file_name does; the canonical implementation is the shared
// pathname package (v4/go/internal/pathname), which mirrors the Rust
// std::path component state machine and is differentially tested
// against rustc.
func rustFileName(path string) (string, bool) {
	return pathname.FileName(path)
}

// platformBasenameFromPath copies the platform encoding of the
// file-name component (Rust LocalBasename::from_path): bytes:raw on
// unix and the UTF-16LE units on windows, each under its platform
// encoding tag.
func platformBasenameFromPath(path string) (LocalBasename, error) {
	name, ok := rustFileName(path)
	if !ok {
		return LocalBasename{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "database path has no file name"}
	}
	bytes := []byte(name)
	encoding := uint16(1)
	content := bytes
	if runtime.GOOS == "windows" {
		content = Utf16LEBytes(name)
		encoding = 2
	}
	if len(content) > maxBasenameBytes {
		return LocalBasename{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "database basename exceeds the portable result bound"}
	}
	var out LocalBasename
	out.encoding = encoding
	out.length = uint16(len(content))
	copy(out.bytes[:], content)
	return out, nil
}

// localBasenameFromPath is the canonical SDK basename constructor
// (Rust LocalBasename::from_path), exported for the public facade
// and the CLI adapters so the platform bytes are produced by one
// implementation.
func LocalBasenameFromPath(path string) (LocalBasename, error) {
	return platformBasenameFromPath(path)
}

// localBasenameFromPath keeps the historical unexported name for the
// live machine internals.
func localBasenameFromPath(path string) (LocalBasename, error) {
	return LocalBasenameFromPath(path)
}

// encoding returns the platform encoding tag (Rust
// LocalBasename::encoding).
func (b LocalBasename) encodingValue() uint16 {
	return b.encoding
}

// bytesValue returns the copied basename bytes (Rust
// LocalBasename::as_bytes).
func (b LocalBasename) bytesValue() []byte {
	return b.bytes[:b.length]
}

// FileName returns the final path component with exact Rust
// Path::file_name semantics (Rust LocalBasename::from_path and the
// publication/live_namespace bind sites): the raw path is not
// normalized, trailing separators and trailing "." components are
// dropped, mid-path ".." is an ordinary component that is never
// resolved, a ".."-terminated path has no name, and the Windows
// volume prefix is not a name.
func FileName(path string) (string, bool) {
	return pathname.FileName(path)
}

// HasFileName reports whether the path has a file-name component the
// way Rust Path::file_name does (Rust require_publication_parent and
// require_creatable_parent both gate on file_name().is_none()):
// empty, ".", "..", separator-only, volume-only, and ".."-terminated
// paths have no file name; mid-path ".." is an ordinary component.
func HasFileName(path string) bool {
	_, ok := pathname.FileName(path)
	return ok
}

// FileParent returns the parent path the way Rust Path::parent does
// on the same component stream as Path::file_name: trailing
// separators and trailing "." components are dropped first, the
// final component and its preceding separator run are removed, a
// remaining all-separator prefix reduces to the root separator, and
// an empty parent becomes "." (the handler default that mirrors
// Rust's filter-empty + unwrap_or(".") at require_publication_parent
// and require_creatable_parent call sites).
func FileParent(path string) string {
	return pathname.ParentOrDot(path)
}
