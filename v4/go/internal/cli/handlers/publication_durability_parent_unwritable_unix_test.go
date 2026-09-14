//go:build unix

package handlers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// The second trigger of the pre-visibility definite refusal needs POSIX mode
// bits, so it lives in this unix-tagged file while
// TestMetadataPublicationBeforeVisibilityIsDefiniteRefusal keeps the
// trigger that is platform-neutral.
//
// The missing Windows mechanism: Go's os.Chmod on Windows maps only the
// read-only attribute (syscall.Chmod toggles FILE_ATTRIBUTE_READONLY from
// S_IWRITE and ignores every other bit), so a 0500 parent directory stays
// writable and the private temporary would be created normally. Expressing
// this denial on Windows needs a deny-write ACE (FILE_ADD_FILE |
// FILE_ADD_SUBDIRECTORY) installed with SetFileSecurity, which this suite
// does not have. Removal criterion: a Windows-native trigger for that denial
// exists and this case runs green on the qualified Windows host; then it
// returns to the platform-neutral file.
func TestMetadataPublicationUnwritableParentIsDefiniteRefusal(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "metadata.json")
	// Searchable but not writable: only the creation of the private
	// name can fail, so the destination never comes into existence.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, herr := MetadataOutput(destination, []byte("payload"),
		iprangedb.PolicyReplaceExisting, 1<<20, 8)
	if herr == nil {
		t.Fatal("an unwritable parent was accepted")
	}
	if herr.Code != "io" || herr.Outcome != "read_only_failure" {
		t.Fatalf("code=%q outcome=%q, want io/read_only_failure",
			herr.Code, herr.Outcome)
	}
	requireNoPublicationFacts(t, herr.Details)
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after a pre-publication refusal: %v", err)
	}
}
