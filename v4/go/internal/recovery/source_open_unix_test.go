//go:build !windows

package recovery

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"golang.org/x/sys/unix"
)

// A FIFO database path must be refused promptly, not blocked waiting
// for a writer, in both recovery open arms (Rust open_read_only and
// live_namespace open_rw). The wave-19.21 Go defect: the immutable
// arm hung forever on a FIFO and the quiescent arm accepted it.
func TestOpenSourceFifoIsRefusedWithoutBlocking(t *testing.T) {
	for _, flags := range []int{os.O_RDONLY, os.O_RDWR} {
		path := t.TempDir() + "/fifo"
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		opened := make(chan error, 1)
		go func() {
			file, err := openSourceFilePlatform(path, flags)
			if file != nil {
				file.Close()
			}
			opened <- err
		}()
		select {
		case err := <-opened:
			var typed *format.Error
			if !errors.As(err, &typed) || typed.Code != format.CodeInvalidArgument {
				t.Fatalf("flags %v: error = %v, want invalid-argument", flags, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("flags %v: open blocked on the fifo", flags)
		}
	}
}
