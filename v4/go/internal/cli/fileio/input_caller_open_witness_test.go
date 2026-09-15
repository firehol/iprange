//go:build unix

package fileio

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
	"golang.org/x/sys/unix"
)

// The text-input parser must open every caller path through the
// caller-open owner, which is what judges the descriptor that open(2)
// returned instead of the caller's earlier path stat.
//
// The existing pins call the owners directly, so they keep passing when a
// caller stops using them: replacing one of the two opens here with a plain
// os.Open removes the protection with no test failure at all. These are the
// caller half, mirroring the Rust judge-witness control
// (v4/rust/iprange-cli/src/io/caller_open.rs assert_control: an arm that
// read its fixture must have asked the descriptor judgment about it), and
// they are unix-tagged because the O_NONBLOCK open is a property of the
// POSIX arm of each owner (the Windows arm has no FIFO open hazard and
// opens plainly). The platform-neutral class pins of the same owners stay
// in opened_guard_test.go.
func TestTextInputSourceOpensEveryPathThroughCallerOpen(t *testing.T) {
	t.Run("a plain input path", func(t *testing.T) {
		dir := t.TempDir()
		ranges := filepath.Join(dir, "ranges.txt")
		writeCallerOpenInput(t, ranges, "198.51.100.0-198.51.100.7\n")

		witness, stop := calleropen.WatchOpensForTest()
		defer stop()
		drainCallerOpenInput(t, []string{ranges}, true)
		requirePromptCallerOpenInput(t, witness, "openInput (input.go)", ranges)
	})

	t.Run("an @file-list and the input it names", func(t *testing.T) {
		dir := t.TempDir()
		ranges := filepath.Join(dir, "ranges.txt")
		writeCallerOpenInput(t, ranges, "198.51.100.0-198.51.100.7\n")
		list := filepath.Join(dir, "list.txt")
		writeCallerOpenInput(t, list, ranges+"\n")

		witness, stop := calleropen.WatchOpensForTest()
		defer stop()
		drainCallerOpenInput(t, []string{"@" + list}, true)
		// The two opens are separate reverts, so each path is pinned on
		// its own: the list file (expandPaths) and the path the list
		// named (openInput).
		requirePromptCallerOpenInput(t, witness, "expandPaths @file-list open (input.go)", list)
		requirePromptCallerOpenInput(t, witness, "openInput (input.go)", ranges)
	})
}

// drainCallerOpenInput runs the production source to end-of-input, so the
// opens under test are the ones a real feed build performs.
func drainCallerOpenInput(t *testing.T, paths []string, expandAtPaths bool) {
	t.Helper()
	source, err := NewTextInputSource4(paths, opt4(32, true), expandAtPaths, 16)
	if err != nil {
		t.Fatalf("NewTextInputSource4: %v", err)
	}
	defer source.Close()
	rows := 0
	for {
		batch, err := source.NextBatch()
		if err != nil {
			t.Fatalf("NextBatch: %v", err)
		}
		if batch == nil {
			break
		}
		rows += len(batch)
	}
	if rows != 1 {
		t.Fatalf("the fixture produced %d ranges, want 1: the input was not read as expected", rows)
	}
}

// requirePromptCallerOpenInput asserts that the parser opened the path it
// was given through calleropen.Open, with the prompt O_NONBLOCK the owner
// is required to pass. Zero calls means the caller opened the path itself,
// which is the defect: it judges a path check that may already be stale,
// and a FIFO behind that check wedges the request inside open(2). A missing
// flag means the owner lost the behaviour that answers instead of waiting.
func requirePromptCallerOpenInput(t *testing.T, witness *calleropen.Witness, label, path string) {
	t.Helper()
	counts := witness.Counts(path)
	if counts.OpenCalls == 0 {
		t.Fatalf("%s: %s was never opened through calleropen.Open, so the caller opened the path itself",
			label, path)
	}
	if counts.LastFlags&unix.O_NONBLOCK == 0 {
		t.Fatalf("%s: %s was opened through calleropen.Open with flags %#x, which lacks O_NONBLOCK",
			label, path, counts.LastFlags)
	}
}

// writeCallerOpenInput creates one input file for the parser.
func writeCallerOpenInput(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
