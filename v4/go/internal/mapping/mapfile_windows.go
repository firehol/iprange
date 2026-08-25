//go:build windows

package mapping

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// MapFile is the Windows stub: live coordination is a tracked SOW-0026 item,
// so mapping a coordination artifact read-write refuses exactly like
// every other Windows mapping entry point (mapping_windows.go).
func MapFile(_ *os.File, _ uint64, _ bool) (*Mapping, error) {
	return nil, &format.Error{Code: format.CodeOSUnsupported, Detail: "windows mapping owner not implemented"}
}
