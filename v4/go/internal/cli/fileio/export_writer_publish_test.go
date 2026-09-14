//go:build unix

package fileio

import (
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// An export destination that names an existing directory is refused
// with io (Rust fs::rename reports the raw EISDIR, and file_error maps
// only the genuine name-exists errnos to name_exists). The claim
// name_exists would assert that a comparable artifact already occupies
// the name, which is not what happened, and it made the export family
// answer differently from the publish family for the same physical
// condition.
func TestExportWriterDestinationDirectoryIsIO(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "destination-dir")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []iprangedb.PublicationPolicy{
		iprangedb.PolicyReplaceExisting, iprangedb.PolicyReplaceExistingNoRollback,
	} {
		writer, herr := NewExportWriter(destination, policy, ExportBudget{MaxRows: 16, MaxOutputBytes: 1 << 20, MaxOpenFiles: 3})
		if herr != nil {
			t.Fatalf("NewExportWriter: %v", herr)
		}
		if herr := writer.WriteLine([]byte("1.0.0.0,1.0.0.1,7"), U64(1)); herr != nil {
			writer.Abort()
			t.Fatalf("WriteLine: %v", herr)
		}
		if _, herr := writer.Finish(); herr == nil {
			writer.Abort()
			t.Fatalf("policy %v published over a directory", policy)
		} else if herr.Code != "io" {
			writer.Abort()
			t.Fatalf("policy %v over a directory: code = %q, want io", policy, herr.Code)
		}
	}
}

// A destination that genuinely is an existing artifact keeps the
// name-exists class under fail_if_exists, so the io refusal above is
// the directory arm and not a blanket change.
func TestExportWriterFailIfExistsKeepsNameExists(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "taken.csv")
	if err := os.WriteFile(destination, []byte("header\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, herr := NewExportWriter(destination, iprangedb.PolicyFailIfExists, ExportBudget{MaxRows: 16, MaxOutputBytes: 1 << 20, MaxOpenFiles: 3})
	if herr != nil {
		t.Fatalf("NewExportWriter: %v", herr)
	}
	if herr := writer.WriteLine([]byte("1.0.0.0,1.0.0.1,7"), U64(1)); herr != nil {
		writer.Abort()
		t.Fatalf("WriteLine: %v", herr)
	}
	if _, herr := writer.Finish(); herr == nil || herr.Code != "name_exists" {
		writer.Abort()
		t.Fatalf("publish over an existing name: %+v, want name_exists", herr)
	}
}
