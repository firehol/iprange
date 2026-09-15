//go:build windows

package handlers

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// scratchCreationSecurityKind is the creator-only security kind a
// recovery-scratch header records here: the Windows protected single-ACE
// creator DACL that security.CreatePrivate installs.
const scratchCreationSecurityKind uint16 = 2

// maintenanceScratchArtifact creates one recovery-scratch artifact under its
// exact 62-byte basename and returns its path (Windows arm). A plain write
// inherits the parent directory's ACL, and both the GC resolver and the
// retirement authority prove a protected single-ACE creator DACL, so the
// residue is created through the SDK's own creator-only creation and its
// header records the commitment that created file's descriptor actually
// carries.
func maintenanceScratchArtifact(t *testing.T, directory string, attempt [16]byte, ordinal uint32) string {
	t.Helper()
	path := maintenanceScratchPath(directory, attempt, ordinal)
	file, commitment := createMaintenanceResidue(t, path)
	header := maintenanceScratchHeader(attempt, ordinal, scratchCreationSecurityKind, commitment)
	if _, err := file.Write(header[:]); err != nil {
		file.Close()
		t.Fatalf("write the scratch artifact: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close the scratch artifact: %v", err)
	}
	return path
}

// maintenancePrivateResidue creates one exact-pattern private artifact of the
// given prefix whose content is neither a readable reservation record nor
// readable v4 geometry, which is what a killed publisher leaves behind
// (Windows arm: created through the SDK creator-only creation, because the
// retirement proves that policy on the retained handle). maintenance.list
// reports such an artifact without its optional evidence members.
func maintenancePrivateResidue(t *testing.T, directory, prefix string, attempt [16]byte) string {
	t.Helper()
	path := maintenancePrivatePath(directory, prefix, attempt)
	file, _ := createMaintenanceResidue(t, path)
	if _, err := file.Write([]byte("partial")); err != nil {
		file.Close()
		t.Fatalf("write %s residue: %v", prefix, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s residue: %v", prefix, err)
	}
	return path
}

// createMaintenanceResidue creates one exclusive creator-only private file
// through the SDK creation the products use (security.CreatePrivate with the
// write-through descriptor of Directory.CreateSecured) and returns the open
// handle with the commitment its protected DACL proves.
func createMaintenanceResidue(t *testing.T, path string) (*os.File, [32]byte) {
	t.Helper()
	profile, err := security.Capture()
	if err != nil {
		t.Fatalf("capture the creator profile: %v", err)
	}
	file, err := security.CreatePrivate(path, profile, true)
	if err != nil {
		t.Fatalf("create %s through the SDK creator-only creation: %v", path, err)
	}
	commitment, err := security.CreatorOnlyCommitment(file)
	if err != nil {
		file.Close()
		t.Fatalf("prove the creator-only policy of %s: %v", path, err)
	}
	return file, commitment
}
