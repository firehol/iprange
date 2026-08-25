//go:build windows

package mapping

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// coordinationSupported reports the exclusive lifetime-lock machine availability.
const coordinationSupported = false

// Mapping is the Windows stub: the type and method surface exist only so
// the reader core cross-compiles. OpenImmutable always refuses, so these
// methods are unreachable at runtime on Windows until the platform
// completion work (SOW-0026) implements the real owner. The type holds no
// descriptor: the raw *os.File never escapes the mapping owner on any
// platform.
type Mapping struct{}

// OpenImmutable refuses every Windows open in milestone 1.
func OpenImmutable(path string, _ func(clean string) error) (*Mapping, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// OpenMutable refuses every Windows open in milestone 1.
func OpenMutable(path string, _ func(clean string) error) (*Mapping, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// OpenMutableShared refuses every Windows open in milestone 1 (the live
// writer path; the sidecar writer lease is also refused on Windows, so
// this stub is unreachable until the platform milestone).
func OpenMutableShared(path string, _ func(clean string) error) (*Mapping, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// OpenLiveReader refuses every Windows open in milestone 1 (the live
// reader path; live coordination is not implemented on Windows yet).
func OpenLiveReader(path string, _ func(clean string) error) (*Mapping, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Unmap is unreachable on Windows: no mapping is ever created.
func (m *Mapping) Unmap() error { return nil }

// Create refuses every Windows creation in milestone 1.
func Create(path string, _ uint64, _ func(clean string) error) (*Mapping, error) {
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

// SetUnreadablePages satisfies the common surface; unreachable on
// Windows in milestone 1 (the worker refuses to start there and every
// mapping open refuses, so no session list is ever applied).
func (m *Mapping) SetUnreadablePages(_ []uint32) error { return nil }

// VisitPage refuses every access; unreachable on Windows in milestone 1.
func (m *Mapping) VisitPage(_ uint32, _ func(page []byte) error) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Remap satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) Remap(committedBytes uint64) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// VerifyIdentity satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) VerifyIdentity(path string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// PhysicalSize satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) PhysicalSize() uint64 { return 0 }

// FileIdentity satisfies the common surface; unreachable on Windows in
// milestone 1.
func (m *Mapping) FileIdentity() (uint64, uint64, error) {
	return 0, 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Grow satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) Grow(newSize uint64) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Shrink satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) Shrink(newSize uint64) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Flush satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) FlushRange(offset, length uint64) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping is not supported"}
}

// FlushPage is unsupported on windows (FlushRange stub).
func (m *Mapping) FlushPage(pgno uint32) error { return m.FlushRange(0, 0) }

// FileSize is unsupported on windows.
func (m *Mapping) FileSize() (uint64, error) {
	return 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping is not supported"}
}

func (m *Mapping) Flush() error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// SyncFile satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) SyncFile() error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}

// Close satisfies the common surface; unreachable on Windows in milestone 1.
func (m *Mapping) Close() error { return nil }

// Region refuses every region probe; unreachable on Windows in
// milestone 1 (the Windows stub holds no descriptor or mapping bytes).
func (m *Mapping) Region() (uintptr, uint64, error) {
	return 0, 0, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}
