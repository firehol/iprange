//go:build windows

package publication

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// outputFacts must record the private output name's UTF-16LE units on
// Windows (Rust Name::bytes): the tag-2 attempt facts of both
// products then carry identical bytes for the same private output
// name, and the opaque per-byte wire form round-trips byte for byte.
func TestOutputFactsRecordUtf16LeUnitsOnWindows(t *testing.T) {
	dir := t.TempDir()
	d, err := bindDestination(filepath.Join(dir, "live.iprange"))
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
	if facts.BasenameEncoding != 2 {
		t.Fatalf("basename encoding = %d, want the windows tag 2", facts.BasenameEncoding)
	}
	want := live.Utf16LEBytes(name)
	if !bytes.Equal(facts.Basename, want) {
		t.Fatalf("basename = % x, want the UTF-16LE units % x",
			facts.Basename, want)
	}
}

// platformEncodedBytes must build proper UTF-16LE code units for any
// name (wave-15 round-3 repair: the previous per-UTF-8-byte NUL
// pairing corrupted the basename commitment of non-ASCII names).
func TestPlatformEncodedBytesUtf16LeUnits(t *testing.T) {
	for _, name := range []string{"live.iprange", "caf\u00e9", "\u03b4"} {
		want := live.Utf16LEBytes(name)
		got := platformEncodedBytes(name)
		if !bytes.Equal(got, want) {
			t.Fatalf("platformEncodedBytes(%q) = % x, want % x",
				name, got, want)
		}
		if len(got)%2 != 0 {
			t.Fatalf("platformEncodedBytes(%q) has an odd length %d",
				name, len(got))
		}
	}
}
