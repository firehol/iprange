//go:build unix

// Input-worker stderr contract tripwire (wave-15 round-9): the
// dropped-IPv6 runtime diagnostic must never block the input worker,
// even when stderr is a full, never-drained pipe.  The helper
// process drains an IPv4-mode source made of IPv6-only files with
// its stderr wired to a full pipe; a regression to synchronous (or
// per-message) stderr writes makes the helper hang and this test
// fail.  Mirrors the session-path full-stderr tests in
// v4/go/internal/cli/rpc/session_signal_unix_test.go.

package fileio

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const inputDiagHelperEnv = "IPRANGE_TEST_INPUT_DIAG_HELPER"

// TestInputWorkerNeverBlocksOnFullStderr drives the helper with a
// full, never-drained stderr pipe and asserts the input worker still
// completes: the IPv6-dropped diagnostic is advisory and must not
// stall the source drain or the process exit.
func TestInputWorkerNeverBlocksOnFullStderr(t *testing.T) {
	const files = 16 // the SDK input path cap; rounds below exceed the queue cap
	cmd := exec.Command(os.Args[0], "-test.run=^TestInputStderrDiagHelperProcess$")
	cmd.Env = append(os.Environ(), inputDiagHelperEnv+"="+strconv.Itoa(files))

	var fds [2]int
	if err := syscall.Pipe(fds[:]); err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := syscall.SetNonblock(fds[1], true); err != nil {
		t.Fatalf("setnonblock: %v", err)
	}
	fill := make([]byte, 65536)
	for {
		if _, werr := syscall.Write(fds[1], fill); werr != nil {
			break
		}
	}
	if err := syscall.SetNonblock(fds[1], false); err != nil {
		t.Fatalf("restore blocking: %v", err)
	}
	stderrFile := os.NewFile(uintptr(fds[1]), "stderr-full-pipe")
	cmd.Stderr = stderrFile
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		stderrFile.Close()
		syscall.Close(fds[0])
		t.Fatalf("start helper: %v", err)
	}
	stderrFile.Close() // the child holds its own duplicate
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			syscall.Close(fds[0])
			t.Fatalf("helper failed under full stderr: %v", err)
		}
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		syscall.Close(fds[0])
		t.Fatalf("input worker blocked on the full stderr pipe: helper did not exit within 20s")
	}
	syscall.Close(fds[0])
}

// TestInputStderrDiagHelperProcess is selected by env; it drains an
// IPv4-mode source whose files are all IPv6 lines, firing the
// dropped-IPv6 diagnostic once per file with stderr full.  Twenty
// rounds of 16 files exceed the 256-slot queue cap, so a drop-on-
// overflow regression also wedges nothing (the rounds simply must
// complete).  A synchronous stderr-write regression blocks the first
// diagnostic and the parent times out.
func TestInputStderrDiagHelperProcess(t *testing.T) {
	raw := os.Getenv(inputDiagHelperEnv)
	if raw == "" {
		return
	}
	count, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("bad helper count %q", raw)
	}
	dir := t.TempDir()
	var paths []string
	for i := 0; i < count; i++ {
		path := filepath.Join(dir, fmt.Sprintf("in-%04d.txt", i))
		if err := os.WriteFile(path, []byte("2001:db8::1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	for round := 0; round < 20; round++ {
		source, err := NewTextInputSource4(paths, opt4(0, true), true, 16)
		if err != nil {
			t.Fatal(err)
		}
		for {
			batch, err := source.NextBatch()
			if err != nil {
				source.Close()
				t.Fatal(err)
			}
			if len(batch) == 0 { // the source contract's end marker
				break
			}
		}
		source.Close()
	}
}
