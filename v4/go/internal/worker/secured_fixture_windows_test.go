//go:build windows && (amd64 || arm64)

package worker

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// createSecuredAttemptFile creates one creator-only private artifact
// through the platform authority (Rust security::create_private): the
// Windows arm establishes the protected single-user DACL at creation.
func createSecuredAttemptFile(t *testing.T, path string, profile security.Profile) *os.File {
	t.Helper()
	file, err := security.CreatePrivate(path, profile, false)
	if err != nil {
		t.Fatalf("fixture create attempt: %v", err)
	}
	return file
}
