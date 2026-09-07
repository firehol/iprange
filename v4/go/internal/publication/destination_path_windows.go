//go:build windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainComponent returns the final path component of path with exact
// Rust Path::file_name semantics over the Windows separators (the
// canonical live helper): the raw path is not normalized, trailing
// separators and trailing "." components are ignored, mid-path ".."
// is an ordinary component, a path terminating in ".." has no
// component, and a bare volume has no component (Rust
// Component::Prefix is not a file name).
func mainComponent(path string) (string, bool) {
	return live.FileName(path)
}

// parentOfPath returns the parent directory of path with exact Rust
// Path::parent semantics over the Windows separators (the canonical
// live helper): paths without a directory component bind the current
// directory.
func parentOfPath(path string) string {
	return live.FileParent(path)
}

// platformBasenameEncoding is the Windows UTF-16LE tag (Rust
// BasenameEncoding::WindowsUtf16Le).
func platformBasenameEncoding() basenameEncoding {
	return basenameEncodingWindowsUtf16Le
}

// platformEncodedBytes encodes one name as UTF-16LE code units for
// the basename commitment and platform name facts (Rust Name::bytes
// on Windows); the shared helper produces the same units for ASCII
// and non-ASCII names.
func platformEncodedBytes(name string) []byte {
	return live.Utf16LEBytes(name)
}

// destinationCreate creates one private name with the protected
// creator-only descriptor of the destination profile (Rust
// Destination::create windows arm: security::create_private with
// write-through); the secureCreated proof then verifies the live
// commitment, exactly like the Rust flow.
func destinationCreate(dir *live.Directory, name string, profile security.Profile) (*os.File, error) {
	return dir.CreateSecured(name, profile)
}
