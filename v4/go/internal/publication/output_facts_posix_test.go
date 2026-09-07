//go:build !windows

package publication

import (
	"bytes"
	"path/filepath"
	"testing"
)

// outputFacts must record the platform basename bytes with the
// platform encoding tag (Rust OutputAttempt::facts): on posix the
// private output name bytes stay raw, even when they are not valid
// UTF-8 (Rust Name::bytes on unix keeps the raw CString bytes).
func TestOutputFactsRecordRawPosixNameBytes(t *testing.T) {
	dir := t.TempDir()
	raw := string([]byte{'f', 0x80})
	d, err := bindDestination(filepath.Join(dir, raw))
	if err != nil {
		t.Fatal(err)
	}
	defer d.dir.Close()
	attemptID := [16]byte{0xab}
	name, err := d.outputName(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	facts := outputFacts(d, attemptID, name, nil)
	if facts.BasenameEncoding != 1 {
		t.Fatalf("basename encoding = %d, want the posix tag 1", facts.BasenameEncoding)
	}
	if !bytes.Equal(facts.Basename, []byte(name)) {
		t.Fatalf("basename = % x, want the raw name bytes % x",
			facts.Basename, []byte(name))
	}
}
