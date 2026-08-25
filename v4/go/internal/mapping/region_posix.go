//go:build !windows

package mapping

import (
	"unsafe"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Region returns the mapped extent of one live mapping (Rust
// mapping.rs region: base, length). An unmapped Mapping refuses with
// the same WrongState detail as View.
func (m *Mapping) Region() (base uintptr, length uint64, err error) {
	if m.data == nil {
		return 0, 0, &format.Error{Code: format.CodeWrongState, Detail: "mapping unavailable"}
	}
	return uintptr(unsafe.Pointer(&m.data[0])), m.size, nil
}
