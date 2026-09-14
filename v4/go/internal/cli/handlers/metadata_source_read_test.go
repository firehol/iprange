//go:build unix

package handlers

import (
	"errors"
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"golang.org/x/sys/unix"
)

// POSIX FIFO arms of the metadata-source owner. The platform-neutral
// read-loop, bounded-cap, and class-mapping pins are in
// metadata_read_test.go; these two need mkfifo, so they stay behind the
// unix tag. Every call into the owner here is guarded by runOpenedCall:
// a lost O_NONBLOCK must fail the test inside the deadline instead of
// leaving the suite waiting on a writer that never comes.

// openMetadataSourceNoBlock must decide regularity on the descriptor it
// opened: calling the helper directly on a standing FIFO reproduces the
// swap-race state (the caller's path check already passed) without
// racing. Dropping O_NONBLOCK wedges the open until a writer appears and
// trips the watchdog; dropping the descriptor check hands the caller a
// FIFO that the metadata commit would publish as empty content.
func TestOpenMetadataSourceNoBlockRefusesOpenedFifo(t *testing.T) {
	path := t.TempDir() + "/meta.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	var err error
	runOpenedCall(t, "openMetadataSourceNoBlock(fifo)", func() {
		var file *os.File
		file, err = openMetadataSourceNoBlock(path)
		if file != nil {
			_ = file.Close()
		}
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openMetadataSourceNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
	}
}

// The pre-open refusal and the authoritative post-open refusal must be
// the same answer: a node that appears at the open instant is refused
// with the class a standing node produces.
func TestReadMetadataFileSwapClassMatchesStandingNode(t *testing.T) {
	path := t.TempDir() + "/meta.fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	var standing []byte
	var herr *rpc.HandlerError
	runOpenedCall(t, "readMetadataFile(standing fifo)", func() {
		standing, herr = readMetadataFile(path)
	})
	if herr == nil || herr.Code != "invalid_path" || herr.Outcome != "not_started" {
		t.Fatalf("readMetadataFile(standing fifo) = %+v, want invalid_path/not_started", herr)
	}
	if standing != nil {
		t.Fatal("readMetadataFile returned content with a refusal")
	}
	var file *os.File
	var err error
	runOpenedCall(t, "openMetadataSourceNoBlock(fifo)", func() {
		file, err = openMetadataSourceNoBlock(path)
	})
	if !errors.Is(err, errOpenedNotRegular) {
		t.Fatalf("openMetadataSourceNoBlock(fifo) = %v, want %v", err, errOpenedNotRegular)
	}
	if file != nil {
		_ = file.Close()
	}
	// The caller maps that sentinel to metadataSourceNotRegular, so the
	// two classes are identical by construction.
	mapped := metadataSourceNotRegular(path)
	if mapped.Code != herr.Code || mapped.Outcome != herr.Outcome || mapped.Message != herr.Message {
		t.Fatalf("post-open refusal %+v differs from pre-open refusal %+v", mapped, herr)
	}
}
