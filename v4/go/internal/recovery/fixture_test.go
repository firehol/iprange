package recovery

// Shared recovery fixture helpers (the over-file output-builder open
// dance used by every source and destination fixture).

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/writer"
)

// buildFixtureWriter creates one database file at path and returns the
// over-file output builder (Rust new_owned_with_extent over the
// caller-created file). The path-based Go constructor takes the
// exclusive lifetime lock, which is absent on freebsd, while the
// production recovery surface builds over-file everywhere.
func buildFixtureWriter(t *testing.T, path string, spec writer.OutputSpec, budget writer.OutputBudget, refs int) *writer.OutputBuilder {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	builder, err := writer.NewOutputBuilderOverFile(f, spec, budget, refs)
	if err != nil {
		f.Close()
		t.Fatalf("NewOutputBuilderOverFile: %v", err)
	}
	f.Close()
	return builder
}
