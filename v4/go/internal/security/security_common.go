// Platform-neutral security helpers (the POSIX surface lives in
// security.go; the Windows live surface is a tracked SOW-0026 item and
// refuses through the same typed class).

package security

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// commitmentDomain is the exact domain string of the ownership
// commitment (Rust COMMITMENT_DOMAIN), shared by every platform
// machine.
const commitmentDomain = "IPR4PSEC"

// unsupported reports the typed OS-unsupported class used by every
// honest refusal of a platform surface (CodeOSUnsupported).
func unsupported(detail string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: detail}
}

// accessPolicy reports the Rust AccessPolicy class transported as the
// Go access-policy-unsupported code (shared by the POSIX and Windows
// machines).
func accessPolicy() error {
	return &format.Error{Code: format.CodeAccessPolicyUnsupported, Detail: "creator-only access policy is not proved"}
}

// ioError transports one operating-system failure with the Rust
// operation label (shared by the POSIX and Windows machines).
func ioError(operation string, cause error) error {
	return &format.Error{Code: format.CodeIO, Detail: operation + ": " + cause.Error()}
}
