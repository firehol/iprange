//go:build unix

package handlers

import (
	"errors"
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

// openDirectCsvNoBlock must decide regularity on the descriptor it
// opened: calling the helper directly on a standing FIFO reproduces the
// swap-race state (the caller's path check already passed) without
// racing. Dropping O_NONBLOCK wedges the open until a writer appears
// and trips the watchdog; dropping the descriptor check hands the
// caller a FIFO whose header walk then reports a valid empty CSV.
func TestOpenDirectCsvNoBlockRefusesOpenedFifo(t *testing.T) {
	path := t.TempDir() + "/input.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		file, err := openDirectCsvNoBlock(path)
		if file != nil {
			_ = file.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errOpenedNotRegular) {
			t.Fatalf("openDirectCsvNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openDirectCsvNoBlock blocked on the fifo")
	}
}

// The CSV arm maps the opened-descriptor refusal to the same class and
// message its path check produces, so a node that appears at the open
// instant cannot be consumed as empty input.
func TestOpenDirectCsvSwapClassMatchesStandingNode(t *testing.T) {
	path := t.TempDir() + "/input.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	standing, herr := openDirectCsv(path, 1<<20, false)
	if standing != nil {
		t.Fatal("openDirectCsv returned a source with a refusal")
	}
	if herr == nil || herr.Code != "invalid_path" {
		t.Fatalf("openDirectCsv(standing fifo) = %+v, want invalid_path", herr)
	}
	if _, err := openDirectCsvNoBlock(path); !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openDirectCsvNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
	}
	want := csvFailure("invalid_path", "direct CSV input is not a regular file: "+path)
	if want.Code != herr.Code || want.Message != herr.Message {
		t.Fatalf("standing refusal %+v differs from the arm class %+v", herr, want)
	}
}
