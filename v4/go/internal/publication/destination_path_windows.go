//go:build windows

package publication

import (
	"os"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainComponent returns the final path component of path with Rust
// Path::file_name semantics over the Windows separators: the raw path
// is not normalized, trailing separators are ignored, and a path
// terminating in ".." has no component.
func mainComponent(path string) (string, bool) {
	p := trimTrailingSeparators(path)
	if i := lastSeparator(p); i >= 0 {
		p = p[i+1:]
	}
	if p == "" || p == ".." {
		return "", false
	}
	return p, true
}

// parentOfPath returns the parent directory of path with Rust Path::
// parent semantics over the Windows separators: paths without a
// directory component bind the current directory.
func parentOfPath(path string) string {
	p := trimTrailingSeparators(path)
	if i := lastSeparator(p); i >= 0 {
		if i == 0 {
			return "\\"
		}
		return p[:i]
	}
	return "."
}

// lastSeparator finds the last slash or backslash of path.
func lastSeparator(path string) int {
	slash := strings.LastIndexByte(path, '/')
	backslash := strings.LastIndexByte(path, '\\')
	if slash > backslash {
		return slash
	}
	return backslash
}

func trimTrailingSeparators(path string) string {
	for len(path) > 1 && (path[len(path)-1] == '/' || path[len(path)-1] == '\\') {
		path = path[:len(path)-1]
	}
	return path
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
