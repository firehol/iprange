//go:build !windows

package snapshot

import (
	"errors"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/unix"
)

// A FIFO swapped in between the caller's lstat and the destination open
// must never block the publication probe (wave-19.22 portability P1 same
// class: openDestinationNoFollow lacked O_NONBLOCK). The refusal uses
// the same conflict class as the deterministic non-regular destination.
func TestOpenDestinationNoFollowFifoIsRefusedWithoutBlocking(t *testing.T) {
	path := t.TempDir() + "/fifo"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	opened := make(chan error, 1)
	go func() {
		file, err := openDestinationNoFollow(path)
		if file != nil {
			file.Close()
		}
		opened <- err
	}()
	select {
	case err := <-opened:
		var typed *format.Error
		if !errors.As(err, &typed) || typed.Code != format.CodeConflict {
			t.Fatalf("error = %v, want conflict", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("openDestinationNoFollow blocked on the fifo")
	}
}
