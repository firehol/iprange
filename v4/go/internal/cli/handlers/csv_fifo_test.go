package handlers

import (
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"golang.org/x/sys/unix"
)

// openDirectCsv must refuse a FIFO input promptly with invalid_path
// through its pre-stat, never block waiting for a writer (wave-19.22
// operations same-class hardening; the no-block open helper covers the
// swap race after the stat).
func TestOpenDirectCsvFifoIsRefusedWithoutBlocking(t *testing.T) {
	path := t.TempDir() + "/input.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	done := make(chan *rpc.HandlerError, 1)
	go func() {
		_, herr := openDirectCsv(path, 1<<20, false)
		done <- herr
	}()
	select {
	case herr := <-done:
		if herr == nil || herr.Code != "invalid_path" {
			t.Fatalf("openDirectCsv(fifo) = %+v, want invalid_path", herr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openDirectCsv blocked on the fifo")
	}
}
