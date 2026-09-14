//go:build unix

package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// The writer arms keep the plain io class when the writer-target open fails
// for a reason other than the no-follow symlink: only the no-follow symlink
// errno is folded into the wrong-state class (Rust
// Directory::open_regular_with_links classifies every other errno as
// NamespaceError::IoAt -> Error::Io). This is the counterpart of
// TestWriterArmsRefuseSymlinkedLiveDatabaseAsWrongState, and it lives in this
// unix-tagged file because its trigger needs POSIX mode bits.
//
// The missing Windows mechanism: Go's os.Chmod on Windows maps only the
// read-only attribute (syscall.Chmod toggles FILE_ATTRIBUTE_READONLY from
// S_IWRITE and ignores every other bit), so a 0000 directory stays readable
// and traversable and the path under it simply does not exist — the arms then
// answer their missing-path class instead of the io class asserted here.
// Expressing this denial on Windows needs a deny ACE (FILE_TRAVERSE |
// FILE_READ_DATA) installed with SetFileSecurity, which this suite does not
// have. Removal criterion: a Windows-native trigger for that denial exists and
// all seven arms run green on the qualified Windows host; then this test
// returns to the platform-neutral file.
func TestWriterArmsKeepIoClassForOtherOpenFailures(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "rows.csv")
	if err := os.WriteFile(csv, []byte("192.0.2.0-192.0.2.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := newLiveFeed(t, dir, "db.iprange")
	// An inaccessible directory component makes the open itself fail
	// with EACCES, which is not the no-follow symlink class.
	blocker := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(blocker, "db.iprange")
	if err := os.Chmod(blocker, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocker, 0o700) })
	for _, arm := range writerArmsOn(dir) {
		t.Run(arm.name, func(t *testing.T) {
			_, herr := arm.call(rpc.NewSessionState(), mustJSON(t, arm.params(link)))
			if herr == nil {
				t.Fatalf("%s accepted an unopenable writer target", arm.name)
			}
			if herr.Code != "io" {
				t.Fatalf("%s: code=%q, want io for the %s target", arm.name, herr.Code, target)
			}
		})
	}
}
