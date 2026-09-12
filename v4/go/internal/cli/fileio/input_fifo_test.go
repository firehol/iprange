package fileio

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openInput must refuse a FIFO path promptly with inputErrorInvalidPath
// through its pre-stat, never block waiting for a writer (wave-19.22
// operations same-class hardening; the no-block open helper covers the
// swap race after the stat).
func TestOpenInputFifoIsRefusedWithoutBlocking(t *testing.T) {
	path := t.TempDir() + "/input.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	done := make(chan *InputError, 1)
	go func() {
		file, ierr := openInput(path)
		if file != nil {
			file.Close()
		}
		done <- ierr
	}()
	select {
	case ierr := <-done:
		if ierr == nil || ierr.kind != inputErrorInvalidPath {
			t.Fatalf("openInput(fifo) = %+v, want invalid_path", ierr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openInput blocked on the fifo")
	}
}
