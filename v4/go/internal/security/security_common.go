// Platform-neutral security helpers (the POSIX surface lives in
// security.go; the Windows live surface is a tracked SOW-0026 item and
// refuses through the same typed class).

package security

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// unsupported reports the typed OS-unsupported class used by every
// honest refusal of a platform surface (CodeOSUnsupported).
func unsupported(detail string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: detail}
}
