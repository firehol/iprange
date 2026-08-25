//go:build !windows

package publication

import (
	"os"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainComponent returns the final path component of path with Rust
// Path::file_name semantics over the unix separator: the raw path is
// not normalized (no Clean), trailing separators are ignored, and a
// path terminating in ".." has no component.
func mainComponent(path string) (string, bool) {
	p := trimTrailingSeparators(path)
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	if p == "" || p == ".." {
		return "", false
	}
	return p, true
}

// parentOfPath returns the parent directory of path with Rust Path::
// parent semantics over the unix separator (Rust namespace::parent):
// paths without a directory component bind the current directory.
func parentOfPath(path string) string {
	p := trimTrailingSeparators(path)
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		if i == 0 {
			return "/"
		}
		return p[:i]
	}
	return "."
}

func trimTrailingSeparators(path string) string {
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}

// platformBasenameEncoding is the unix PosixBytes tag (Rust
// BasenameEncoding::PosixBytes).
func platformBasenameEncoding() basenameEncoding {
	return basenameEncodingPosixBytes
}

// platformEncodedBytes keeps the ASCII name bytes raw on unix.
func platformEncodedBytes(name string) []byte {
	return []byte(name)
}

// destinationCreate creates one private name with the unprotected
// 0600 open; the creator-only proof is applied separately by
// secureCreated (Rust Destination::create unix arm + secure_created).
func destinationCreate(dir *live.Directory, name string, _ security.Profile) (*os.File, error) {
	return dir.Create(name)
}
