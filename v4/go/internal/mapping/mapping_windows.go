//go:build windows

package mapping

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Mapping is the milestone-1 Windows stub: the type and method surface exist
// only so the reader core cross-compiles. OpenImmutable always refuses, so
// these methods are unreachable at runtime on Windows until the platform
// milestone implements the real owner. The type holds no descriptor: the
// raw *os.File never escapes the mapping owner on any platform.
type Mapping struct{}

// OpenImmutable refuses every Windows open in milestone 1.
func OpenImmutable(path string, _ func(clean string) error) (*Mapping, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Size satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) Size() uint64 { return 0 }

// View refuses every access; unreachable on Windows in milestone 1.
func (m *Mapping) View(off, length uint64) ([]byte, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Page refuses every access; unreachable on Windows in milestone 1.
func (m *Mapping) Page(pgno uint32) ([]byte, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Close satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) Close() error { return nil }
