// Windows stubs for the live snapshot self-replacement probe. The
// snapshot To machine refuses live snapshot sources before any path
// access (CheckSupported), so the probe is unreachable here; the stubs
// keep the package compiling, following the mapping package's
// platform-stub pattern.

//go:build windows

package snapshot

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// openDestinationNoFollow satisfies the common surface; unreachable on
// Windows in milestone 1.
func openDestinationNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

// fileIdentityOf satisfies the common surface; unreachable on Windows
// in milestone 1.
func fileIdentityOf(f *os.File) (uint64, uint64, error) {
	return 0, 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "live snapshot self-replacement probe not implemented on windows"}
}

// directoryIdentityOf satisfies the common surface; unreachable on
// Windows in milestone 1.
func directoryIdentityOf(path string) (uint64, uint64, error) {
	return 0, 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "live snapshot self-replacement probe not implemented on windows"}
}

// fileLinksOf satisfies the common surface; unreachable on Windows in
// milestone 1.
func fileLinksOf(f *os.File) (uint64, error) {
	return 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "live snapshot self-replacement probe not implemented on windows"}
}
