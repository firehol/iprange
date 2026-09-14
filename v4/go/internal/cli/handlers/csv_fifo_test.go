//go:build unix

package handlers

import (
	"errors"
	"os"
	"testing"

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
	var herr *rpc.HandlerError
	runOpenedCall(t, "openDirectCsv(standing fifo)", func() {
		_, herr = openDirectCsv(path, 1<<20, false)
	})
	if herr == nil || herr.Code != "invalid_path" {
		t.Fatalf("openDirectCsv(fifo) = %+v, want invalid_path", herr)
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
	var err error
	runOpenedCall(t, "openDirectCsvNoBlock(fifo)", func() {
		var file *os.File
		file, err = openDirectCsvNoBlock(path)
		if file != nil {
			_ = file.Close()
		}
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openDirectCsvNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
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
	var standing *directCsvSource
	var herr *rpc.HandlerError
	runOpenedCall(t, "openDirectCsv(standing fifo)", func() {
		standing, herr = openDirectCsv(path, 1<<20, false)
	})
	if standing != nil {
		t.Fatal("openDirectCsv returned a source with a refusal")
	}
	if herr == nil || herr.Code != "invalid_path" {
		t.Fatalf("openDirectCsv(standing fifo) = %+v, want invalid_path", herr)
	}
	var err error
	runOpenedCall(t, "openDirectCsvNoBlock(fifo)", func() {
		var file *os.File
		file, err = openDirectCsvNoBlock(path)
		if file != nil {
			_ = file.Close()
		}
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openDirectCsvNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
	}
	want := csvFailure("invalid_path", "direct CSV input is not a regular file: "+path)
	if want.Code != herr.Code || want.Message != herr.Message {
		t.Fatalf("standing refusal %+v differs from the arm class %+v", herr, want)
	}
}
