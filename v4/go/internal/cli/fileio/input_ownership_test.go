package fileio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The input core must deterministically close the active file on every
// error path, not only at the normal end-of-input steps: a parse
// failure, a binary-header family mismatch, and an openBinary failure
// must leave core.active nil so no handle survives until garbage
// collection (external review finding; on Windows an open file blocks
// removal of the input path).  Close() must be idempotent and must
// cover early SDK termination.
func TestInputCoreClosesActiveOnEveryErrorPath(t *testing.T) {
	dir := t.TempDir()

	// Parse-failure file: first line is not a range, so openNext fails
	// after opening the file.
	badText := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(badText, []byte("not a range at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewTextInputSource4([]string{badText}, opt4(0, true), true, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.NextBatch(); err == nil {
		t.Fatal("parse-failure file produced no error")
	}
	if source.core.active != nil {
		t.Fatal("parse-failure path left the active input open")
	}
	if source.core.activePath != "" {
		t.Fatalf("parse-failure path left activePath %q", source.core.activePath)
	}
	source.Close() // idempotent, no-op on the closed core

	// Binary-header family mismatch: a v4 binary header read in IPv6
	// mode must fail and close the file.
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	v4bin := filepath.Join(binDir, "v4.bin")
	if err := os.WriteFile(v4bin, append([]byte("iprange binary format v1.0\n"), 0x00), 0o600); err != nil {
		t.Fatal(err)
	}
	source6, err := NewTextInputSource6([]string{v4bin}, opt6(0, true), true, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source6.NextBatch(); err == nil {
		t.Fatal("v4 header in IPv6 mode produced no error")
	}
	if source6.core.active != nil {
		t.Fatal("binary-header mismatch path left the active input open")
	}
	if source6.core.activePath != "" {
		t.Fatalf("binary-header mismatch left activePath %q", source6.core.activePath)
	}
	source6.Close()

	// Early-termination coverage: a healthy source that was drained
	// only partially (more than one 256-range batch of input) must
	// release its still-open file through Close() — the SDK stops
	// iterating early on a budget or conflict abort and the handler's
	// deferred Close is the only deterministic release.
	var plenty strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&plenty, "10.%d.0.%d - 10.%d.0.%d\n", i/250, i%250, i/250, i%250+1)
	}
	open := filepath.Join(dir, "open.txt")
	if err := os.WriteFile(open, []byte(plenty.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	early, err := NewTextInputSource4([]string{open}, opt4(0, true), true, 16)
	if err != nil {
		t.Fatal(err)
	}
	first, err := early.NextBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 256 {
		t.Fatalf("first batch = %d ranges, want 256", len(first))
	}
	if early.core.active == nil {
		t.Fatal("healthy source has no active input before Close")
	}
	early.Close()
	if early.core.active != nil {
		t.Fatal("Close() did not release the active input")
	}
	early.Close() // idempotent
}

// readStep errors must also close the active file: a malformed line
// after the first batch leaves the file open until readStep wraps the
// error.  drainToError forces the mid-stream parse failure.
func TestInputCoreReadStepErrorClosesActive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.txt")
	if err := os.WriteFile(path, []byte("10.0.0.0 - 10.0.0.1\nnot a range\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := NewTextInputSource4([]string{path}, opt4(0, true), true, 16)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.NextBatch()
	if err == nil {
		t.Fatal("mid-stream parse error produced no error")
	}
	if source.core.active != nil {
		t.Fatal("mid-stream parse error left the active input open")
	}
	source.Close()
}
