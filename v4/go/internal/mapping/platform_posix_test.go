//go:build !windows

package mapping

import (
	"errors"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/unix"
)

// A FIFO swapped in between a caller's pre-stat and the no-follow open
// must never block the mapping open (wave-19.22 performance/portability
// P1: the wave-19.21 mapping arm lacked O_NONBLOCK, so the stat-then-open
// sequence wedged on a FIFO race; Rust open_read_only/open_regular have
// O_NONBLOCK in the open itself). Regular files ignore O_NONBLOCK. The
// refusal class mirrors Rust per arm: invalid_argument read-only,
// wrong_state read-write.
func TestOpenNoFollowFifoIsRefusedWithoutBlocking(t *testing.T) {
	for _, rdwr := range []bool{false, true} {
		want := format.CodeInvalidArgument
		if rdwr {
			want = format.CodeWrongState
		}
		path := t.TempDir() + "/fifo"
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		opened := make(chan error, 1)
		go func() {
			file, err := openNoFollow(path, rdwr)
			if file != nil {
				file.Close()
			}
			opened <- err
		}()
		select {
		case err := <-opened:
			var typed *format.Error
			if !errors.As(err, &typed) || typed.Code != want {
				t.Fatalf("rdwr %v: error = %v, want %v", rdwr, err, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("rdwr %v: open blocked on the fifo", rdwr)
		}
	}
}
