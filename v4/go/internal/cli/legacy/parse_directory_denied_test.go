// The companion of the empty-set case: a directory the caller may not open
// keeps failing, on every platform.
//
// The recorded decision at "Bare-directory input" preserves the failures the
// C `fopen()` reports before any read happens (EACCES, EIO, ENOENT), so the
// empty-set result belongs only to a directory whose open *succeeded* and
// whose read then failed as a directory (readLegacyInput, parse.go). Turning
// a refused open into an empty set would report a successful load of a set
// that was never read, which is the defect class this file pins.
//
// The fixture is platform-specific because the platform decides how an open is
// refused; the assertion is not, and it runs on every supported target.
package legacy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryWhoseOpenFailsIsNotMadeEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "host"), []byte("192.0.2.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(dir, "unreadable")
	if err := os.Mkdir(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(denied, "f"), []byte("10.0.0.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reason := denyOpeningDirectory(t, denied); reason != "" {
		t.Skipf("%s: the case needs an open the platform refuses, and this host cannot provide one (%s)",
			denied, reason)
	}

	o := DefaultOptions()
	o.Sources = []SourceSpec{{Kind: SourcePath, Arg: denied}}
	_, err := loadAllImpl(o, emptyReader{})
	if err == nil {
		t.Fatalf("a directory the caller may not open loaded as %v; a refused open must fail the load, "+
			"not become the empty set", o.Sources)
	}
	text := err.Error()
	if !strings.HasPrefix(text, "iprange: "+denied+" - ") {
		t.Errorf("refused open lost its own diagnostic: %q", text)
	}
	if !strings.HasSuffix(text, "iprange: Cannot load ipset: "+denied) {
		t.Errorf("refused open lost the load context naming the path: %q", text)
	}
}
