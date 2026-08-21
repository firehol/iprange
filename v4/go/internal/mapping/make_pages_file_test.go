package mapping

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// makePagesFile creates a page-aligned file of pageCount zero pages. It
// is a portable test helper (plain os/file operations, no syscalls) shared
// by the linux-tagged mapping tests and the v4work necessary-work pins, so
// it lives in an untagged file and compiles under every cross-vet and
// cross-test build of this package, including Windows.
func makePagesFile(t *testing.T, dir string, pageCount int) string {
	t.Helper()
	path := filepath.Join(dir, "writer.iprdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, format.PageSize)
	for i := 0; i < pageCount; i++ {
		if _, err := f.Write(zeros); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
