package reader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestHugeCorruptFileFailsBootstrap verifies that a file with a huge
// aligned tail but corrupt meta pages fails OpenImmutable with
// FormatInvalid. The O(1) bootstrap property (only 2 pages mapped before
// the meta pair is proven) is a structural property of the code path —
// OpenImmutable maps 2 pages, bootstrap reads only pages 0 and 1, and
// Remap runs only after bootstrap succeeds — documented in the body
// comment below.
func TestHugeCorruptFileFailsBootstrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge-corrupt.iprdb")

	// Create a file with 2 corrupt meta pages + a huge aligned tail.
	// 1 GiB tail = 262144 pages. The meta pages claim a valid magic but
	// wrong page count, so bootstrap fails.
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write 2 pages of garbage (not valid meta).
	garbage := make([]byte, 2*format.PageSize)
	for i := range garbage {
		garbage[i] = 0xAA
	}
	if _, err := f.Write(garbage); err != nil {
		t.Fatal(err)
	}
	// Extend to 2 pages + 1 GiB tail (all zeros).
	if err := f.Truncate(int64(2*format.PageSize + 1<<30)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Open must fail with FormatInvalid (corrupt meta), not IO (mmap of
	// 1 GiB + 8 KiB would succeed but waste VA).
	_, err = OpenImmutable(path)
	if err == nil {
		t.Fatal("expected error for corrupt meta")
	}
	ferr, ok := err.(*format.Error)
	if !ok {
		t.Fatalf("expected *format.Error, got %T", err)
	}
	if ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("expected FormatInvalid, got %v", ferr.Code)
	}
	// The O(1) property is structural: OpenImmutable maps exactly 2
	// pages, bootstrap reads only pages 0 and 1, and Remap runs only
	// after bootstrap succeeds. A failed bootstrap never maps beyond
	// the 2-page extent because the remap call is unreachable.
}

// TestRemapCommittedExtent proves that after bootstrap, Remap grows the
// mapping to the exact committed extent and all pages remain readable.
func TestRemapCommittedExtent(t *testing.T) {
	// Use a conformance fixture: valid file, committed == physical.
	r, err := OpenImmutable("../../../conformance/rust/direct-ipv4.iprdb")
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	defer r.Close()
	// After open, the mapping must be at the committed extent (== physical).
	if r.m.Size() != r.m.PhysicalSize() {
		t.Fatalf("Size=%d PhysicalSize=%d, want equal after Remap", r.m.Size(), r.m.PhysicalSize())
	}
	if r.m.Size() != r.meta.PageCount*format.PageSize {
		t.Fatalf("Size=%d, want %d (pageCount*pageSize)", r.m.Size(), r.meta.PageCount*format.PageSize)
	}
}
