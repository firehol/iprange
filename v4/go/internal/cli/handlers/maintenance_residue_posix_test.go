//go:build !windows

package handlers

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// scratchCreationSecurityKind is the creator-only security kind a
// recovery-scratch header records here: the POSIX 0600 creator mode.
const scratchCreationSecurityKind uint16 = 1

// maintenanceScratchArtifact writes one recovery-scratch artifact under its
// exact 62-byte basename and returns its path (POSIX arm). The creator-only
// 0600 mode is the access policy the removal machine proves here, and it is
// the mode the SDK itself creates private artifacts with, so the fixture
// plants what a killed publisher actually leaves behind.
func maintenanceScratchArtifact(t *testing.T, directory string, attempt [16]byte, ordinal uint32) string {
	t.Helper()
	profile, err := security.Capture()
	if err != nil {
		t.Fatalf("capture the creator profile: %v", err)
	}
	header := maintenanceScratchHeader(attempt, ordinal, scratchCreationSecurityKind, profile.Commitment())
	path := maintenanceScratchPath(directory, attempt, ordinal)
	if err := os.WriteFile(path, header[:], 0o600); err != nil {
		t.Fatalf("write the scratch artifact: %v", err)
	}
	return path
}

// maintenancePrivateResidue writes one exact-pattern private artifact of the
// given prefix whose content is neither a readable reservation record nor
// readable v4 geometry, which is what a killed publisher leaves behind
// (POSIX arm: the 0600 mode is the creator-only policy). maintenance.list
// reports such an artifact without its optional evidence members.
func maintenancePrivateResidue(t *testing.T, directory, prefix string, attempt [16]byte) string {
	t.Helper()
	path := maintenancePrivatePath(directory, prefix, attempt)
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write %s residue: %v", prefix, err)
	}
	return path
}
