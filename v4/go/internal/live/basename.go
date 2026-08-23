// One platform basename copied without allocation (Rust
// live_writer/result.rs LocalBasename): the raw bytes plus their
// encoding tag, bounded to the portable result bound.

package live

import (
	"path/filepath"

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

// localBasenameFromPath copies the file-name component of path without
// allocation (Rust LocalBasename::from_path). Unix bytes carry encoding
// 1; an empty or overlong name reports InvalidArgument with the Rust
// detail.
func localBasenameFromPath(path string) (LocalBasename, error) {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return LocalBasename{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "database path has no file name"}
	}
	if len(name) > maxBasenameBytes {
		return LocalBasename{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "database basename exceeds the portable result bound"}
	}
	var out LocalBasename
	out.encoding = 1
	out.length = uint16(len(name))
	copy(out.bytes[:], name)
	return out, nil
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
