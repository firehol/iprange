//go:build windows

package iprangedb

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// writeScratchArtifact creates one exact scratch artifact in the
// directory. The Windows arm must establish the protected
// creator-only DACL (security.CreatePrivate); a plain write leaves
// the inherited ACL, and the GC removal proof then refuses the
// artifact as access-policy-mismatched.
func writeScratchArtifact(t *testing.T, directory string, attempt [16]byte, ordinal uint32) {
	t.Helper()
	header := scratchPublicHeader(attempt, ordinal)
	name := scratchPublicName(attempt, ordinal)
	profile, err := security.Capture()
	if err != nil {
		t.Fatal(err)
	}
	file, err := security.CreatePrivate(filepath.Join(directory, name), profile, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(header[:]); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
