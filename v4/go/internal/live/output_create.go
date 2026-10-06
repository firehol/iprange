package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// CaptureSecurityProfile captures the creator identity (security.Profile
// twin of Rust publication::security::Profile::capture) for CLI output
// creation that must install the protected descriptor itself.
func CaptureSecurityProfile() (security.Profile, error) {
	return security.Capture()
}

// CreatePrivateOutput exclusively creates one file with the protected
// single-user DACL for the captured profile (Rust
// publication::create_private_artifact, the conformance-support arm the
// Rust CLI output creator uses for the same purpose). Windows only:
// the protected DACL machine exists only there.
func CreatePrivateOutput(path string, profile security.Profile) (*os.File, error) {
	return createPrivateOutput(path, profile)
}
