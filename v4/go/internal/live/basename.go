// One platform basename copied without allocation (Rust
// live_writer/result.rs LocalBasename): the raw bytes plus their
// encoding tag, bounded to the portable result bound.

package live

import (
	"path/filepath"
	"runtime"

	"github.com/firehol/iprange/v4/go/internal/format"
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

// platformBasenameFromPath copies the platform encoding of the
// file-name component (Rust LocalBasename::from_path): bytes:raw on
// unix and the UTF-16LE units on windows, each under its platform
// encoding tag.
func platformBasenameFromPath(path string) (LocalBasename, error) {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
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

// bytes returns the copied basename bytes (Rust LocalBasename::as_bytes).
func (b LocalBasename) bytesValue() []byte {
	return b.bytes[:b.length]
}
