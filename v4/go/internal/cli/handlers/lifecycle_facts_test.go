package handlers

import (
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"golang.org/x/sys/unix"
)

// A FIFO metadata source must be refused promptly with invalid_path,
// never blocked waiting for a writer (Rust read_file_exact pre-stat:
// wave-19.21 Go defect: readMetadataFile opened the path bare and a
// FIFO wedged the whole session).
func TestReadMetadataFileFifoSrcIsRefusedWithoutBlocking(t *testing.T) {
	path := t.TempDir() + "/meta.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	done := make(chan *rpc.HandlerError, 1)
	go func() {
		_, herr := readMetadataFile(path)
		done <- herr
	}()
	select {
	case herr := <-done:
		if herr == nil || herr.Code != "invalid_path" {
			t.Fatalf("readMetadataFile(fifo) = %+v, want invalid_path", herr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readMetadataFile blocked on the fifo")
	}
}

// A missing metadata source is refused with invalid_path before any
// open (Rust read_file_exact).
func TestReadMetadataFileMissingRefusedInvalidPath(t *testing.T) {
	_, herr := readMetadataFile(t.TempDir() + "/does-not-exist")
	if herr == nil || herr.Code != "invalid_path" {
		t.Fatalf("readMetadataFile(missing) = %+v, want invalid_path", herr)
	}
}
