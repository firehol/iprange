//go:build !windows

package iprangedb

import (
	"os"
	"path/filepath"
	"testing"
)

// writeScratchArtifact creates one exact scratch artifact in the
// directory (POSIX arm: the 0600 creator-only mode satisfies the
// ownership proof).
func writeScratchArtifact(t *testing.T, directory string, attempt [16]byte, ordinal uint32) {
	t.Helper()
	header := scratchPublicHeader(attempt, ordinal)
	name := scratchPublicName(attempt, ordinal)
	if err := os.WriteFile(filepath.Join(directory, name), header[:], 0o600); err != nil {
		t.Fatal(err)
	}
}
