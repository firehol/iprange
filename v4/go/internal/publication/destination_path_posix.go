//go:build !windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainComponent returns the final path component of path with exact
// Rust Path::file_name semantics (the canonical live helper): the
// raw path is not normalized, trailing separators and trailing "."
// components are ignored, mid-path ".." is an ordinary component,
// and a path terminating in ".." has no component.  This is the
// single authoritative component split for publication destinations
// (external review round 5 finding: the previous hand-rolled split
// returned the trailing "." itself, so "archive.iprange/." was
// rejected while Rust accepts it).
func mainComponent(path string) (string, bool) {
	return live.FileName(path)
}

// parentOfPath returns the parent directory of path with exact Rust
// Path::parent semantics over the unix separator (the canonical live
// helper; Rust namespace::parent): paths without a directory
// component bind the current directory.
func parentOfPath(path string) string {
	return live.FileParent(path)
}

// platformBasenameEncoding is the unix PosixBytes tag (Rust
// BasenameEncoding::PosixBytes).
func platformBasenameEncoding() basenameEncoding {
	return basenameEncodingPosixBytes
}

// platformEncodedBytes keeps one name's bytes raw on unix.
func platformEncodedBytes(name string) []byte {
	return []byte(name)
}

// destinationCreate creates one private name with the unprotected
// 0600 open; the creator-only proof is applied separately by
// secureCreated (Rust Destination::create unix arm + secure_created).
func destinationCreate(dir *live.Directory, name string, _ security.Profile) (*os.File, error) {
	return dir.Create(name)
}
