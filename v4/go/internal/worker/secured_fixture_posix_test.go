//go:build !windows

package worker

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// createSecuredAttemptFile creates one creator-only private artifact
// through the platform authority (Rust security::secure_creator_only):
// the POSIX arm creates the 0600 file and strips any inherited ACL.
func createSecuredAttemptFile(t *testing.T, path string, profile security.Profile) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("fixture create attempt: %v", err)
	}
	if err := security.SecureCreatorOnly(file, profile); err != nil {
		file.Close()
		t.Fatalf("fixture secure attempt: %v", err)
	}
	return file
}
