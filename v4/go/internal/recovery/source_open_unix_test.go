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
// arm hung forever on a FIFO and the quiescent arm accepted it. The
// code class mirrors the Rust arms: invalid_argument for the
// read-only arm (require_regular_file) and wrong_state for the
// read-write arm (open_rw NotRegular -> WrongMode).
func TestOpenSourceFifoIsRefusedWithoutBlocking(t *testing.T) {
	for _, immutable := range []bool{true, false} {
		// The immutable arm is database_file::open_read_only
		// (require_regular_file -> InvalidArgument); the quiescent arm
		// is live_namespace::open_rw (NotRegular -> WrongMode ->
		// wrong_state).
		want := format.CodeInvalidArgument
		if !immutable {
			want = format.CodeWrongState
		}
		path := t.TempDir() + "/fifo"
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		opened := make(chan error, 1)
		go func() {
			file, err := openSourceFile(path, immutable)
			if file != nil {
				file.Close()
			}
			opened <- err
		}()
		select {
		case err := <-opened:
			var typed *format.Error
			if !errors.As(err, &typed) || typed.Code != want {
				t.Fatalf("immutable %v: error = %v, want %v", immutable, err, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("immutable %v: open blocked on the fifo", immutable)
		}
	}
}

// The quiescent arm classifies the database path from its retained
// parent directory (Rust live_namespace::open_rw over Directory::open +
// open_regular), so a symlink, a node on a non-local filesystem, and a
// multi-link file answer the namespace classes Rust reports: the
// symlink and the multi-link file are the wrong-mode ownership class
// (NotRegular/LinkCount -> WrongMode), never the path-level open
// failure a raw path open would produce.
func TestOpenSourceQuiescentClassifiesThroughParentDirectory(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/target"
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := dir + "/link"
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	hard := dir + "/hard"
	if err := os.Link(target, hard); err != nil {
		t.Fatalf("link: %v", err)
	}
	for name, want := range map[string]format.ErrorCode{
		link: format.CodeWrongState,
		hard: format.CodeWrongState,
	} {
		file, err := openSourceFile(name, false)
		if file != nil {
			file.Close()
		}
		var typed *format.Error
		if !errors.As(err, &typed) || typed.Code != want {
			t.Fatalf("%s: error = %v, want %v", name, err, want)
		}
	}
}
