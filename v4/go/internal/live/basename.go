// One platform basename copied without allocation (Rust
// live_writer/result.rs LocalBasename): the raw bytes plus their
// encoding tag, bounded to the portable result bound.

package live

import (
	"path/filepath"
	"runtime"
	"strings"

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

// rustFileName computes the final path component the way Rust
// Path::file_name does through Components: repeated separators
// collapse, trailing separators are trimmed, "." components are
// normalized away everywhere, but ".." components are never resolved
// against earlier components (Rust Components keeps ParentDir as an
// ordinary component).  The result is the last normal component, or
// false when the path ends in ".." or has none (Rust
// Component::ParentDir / RootDir / Prefix / no-component are all
// mapped to None by file_name).
func rustFileName(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	// The Windows volume prefix (Rust Component::Prefix) is not a
	// file-name component.  filepath.VolumeName returns "" on unix,
	// so this block is a no-op there.
	body := path
	if vol := filepath.VolumeName(path); vol != "" {
		body = path[len(vol):]
	}
	separators := "/"
	if runtime.GOOS == "windows" {
		separators = `/\`
	}
	parts := strings.FieldsFunc(body, func(r rune) bool {
		return strings.ContainsRune(separators, r)
	})
	// Rust normalizes "." components away (parse_single_component
	// maps b"." to None in body position), so a trailing "." never
	// becomes the final component.
	for len(parts) > 0 && parts[len(parts)-1] == "." {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return "", false
	}
	last := parts[len(parts)-1]
	if last == ".." {
		return "", false
	}
	return last, true
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
