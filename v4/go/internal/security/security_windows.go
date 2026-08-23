//go:build windows

// Windows stub of the creator-only security surface. The whole live
// and worker-control surface refuses before any path access on
// Windows (live lock machine, worker control build tags); the Windows
// SID-based creator profile (Rust security/windows.rs) is a tracked M5
// item. The stub keeps the package compiling on every target and
// refuses every policy operation honestly.
package security

import (
	"os"
)

// Profile is the creator identity captured before creation (Rust
// security Profile). The Windows shape carries the SID; the stub keeps
// the POSIX field layout so callers compile on every target.
type Profile struct {
	uid        uint32
	commitment [32]byte
}

// Capture records the effective creator identity. The Windows SID
// capture lands with the M5 surface; every policy operation refuses
// before the profile is observed.
func Capture() (*Profile, error) {
	return &Profile{}, nil
}

// Commitment returns the captured commitment.
func (p *Profile) Commitment() [32]byte { return p.commitment }

// SecureCreatorOnly refuses on Windows: the creator-only proof needs
// the SID machinery, tracked with the M5 live surface.
func SecureCreatorOnly(*os.File, *Profile) error {
	return unsupported("creator-only access policy requires the Windows SID machinery, tracked with the M5 live surface")
}

// CreatorOnlyCommitment refuses on Windows (Rust
// creator_only_commitment Windows arm, M5).
func CreatorOnlyCommitment(*os.File) ([32]byte, error) {
	return [32]byte{}, unsupported("creator-only access policy requires the Windows SID machinery, tracked with the M5 live surface")
}
