//go:build !windows

package validation

import (
	"errors"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/unix"
)

// A FIFO database path must be refused promptly, not blocked waiting
// for a writer (Rust database_file::open_read_only
// fifo_is_refused_without_blocking pin). The wave-19.21 Go defect:
// validate and recovery.inspect hung forever on a FIFO because this
// open had no O_NONBLOCK and no authoritative-fd regular check.
func TestOpenReadOnlyFifoIsRefusedWithoutBlocking(t *testing.T) {
	path := t.TempDir() + "/fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	opened := make(chan error, 1)
	go func() {
		file, err := openReadOnlyNoFollow(path)
		if file != nil {
			file.Close()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		var typed *format.Error
		if !errors.As(err, &typed) || typed.Code != format.CodeInvalidArgument {
			t.Fatalf("error = %v, want invalid-argument", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open blocked on the fifo")
	}
}
