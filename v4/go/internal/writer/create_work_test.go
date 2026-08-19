//go:build v4work

// Fault-injection tests for the create write path (Rust
// create_live/write_empty physical half): a failure while writing the
// empty meta pair must surface as the create error, unlink the partial
// file (mapping_create.go deferred cleanup), and leave the destination
// retryable.

package writer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// TestCreateFaultWriteEmpty keeps the armed fault point: after the
// exclusive destination exists, writeEmptyMeta fails, the partial file
// must be removed, and the retried create must succeed.
func TestCreateFaultWriteEmpty(t *testing.T) {
	t.Setenv("IPRANGE_V4_TEST_FAIL_AT", "create.write_empty")
	dir := t.TempDir()
	path := filepath.Join(dir, "fault.iprdb")
	if _, err := Create(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, nil); err == nil {
		t.Fatal("create with write_empty fault succeeded, want error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial file still present after write_empty failure: %v", err)
	}
	if _, err := Create(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, nil); err != nil {
		t.Fatalf("retried create failed: %v", err)
	}
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
}
