//go:build unix

// Termination-signal contract tests (wave-10 D1 repair).  Unix-only:
// the tests deliver real SIGINT/SIGTERM to helper subprocesses, and
// (os.Process).Signal is unsupported on Windows for non-Kill signals
// (portability role finding).  Mirrors the Rust process tests in
// v4/rust/iprange-cli/tests/termination_signals.rs.

package rpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---- termination-signal contract (wave-10 D1 repair) ----
//
// The termination-signal watcher must never be ignorable: an idle
// session exits non-zero, a wedged session (full events channel,
// shutdown unreachable) is force-exited by the watchdog, and a signal
// observed during EOF drain wins over the exit-zero EOF path.

const signalTestHelperEnv = "IPRANGE_TEST_SIGNAL_HELPER"

// TestTerminationSignalHelperProcess is selected by env; it binds one
// session to a scenario so the parent test can exercise the real
// signal path of a running process.
func TestTerminationSignalHelperProcess(t *testing.T) {
	mode := os.Getenv(signalTestHelperEnv)
	if mode == "" {
		return
	}
	var err error
	switch mode {
	case "idle":
		// Blocking input with no data: the session waits for a signal.
		r, _ := io.Pipe()
		err = NewSession().Run(r, io.Discard)
	case "wedged":
		// Pipelined input into a writer that never drains: the events
		// channel fills and shutdown cannot begin (stalled stdout).
		input := strings.Builder{}
		for i := 0; i < 512; i++ {
			input.WriteString("{\"jsonrpc\":\"2.0\",\"id\":" + strconv.Itoa(i) +
				",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n")
		}
		err = NewSession().Run(strings.NewReader(input.String()), blockingWriter{})
	case "mid-drain":
		// A slow handler occupies the worker after EOF; a signal sent
		// during the drain must still produce a non-zero exit through
		// the terminationSignal flag (the main loop no longer reads
		// events once EOF began shutdown).
		registry["iprange.v1.slowtest"] = registeredMethod{
			validate: func(params json.RawMessage) error { return nil },
			handle: func(st *SessionState, params json.RawMessage) (any, *HandlerError) {
				time.Sleep(400 * time.Millisecond)
				return map[string]any{"slow": true}, nil
			},
		}
		defer delete(registry, "iprange.v1.slowtest")
		input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.slowtest\",\"params\":{}}\n"
		err = NewSession().Run(strings.NewReader(input), io.Discard)
	case "partial-wedge":
		// The client pipelines a few dozen frames, stops, keeps stdin
		// open and never reads stdout: the worker blocks on the
		// full stdout pipe, the main loop blocks on the full work
		// queue, but the events channel is only PARTIALLY filled, so a
		// delivered Fatal event could never be processed and a
		// delivery-blocking watchdog would never arm.  The
		// process-lifetime watchdog must still force the exit
		// (role-round finding).
		input := strings.Builder{}
		for i := 0; i < 60; i++ {
			input.WriteString("{\"jsonrpc\":\"2.0\",\"id\":" + strconv.Itoa(i) +
				",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n")
		}
		err = NewSession().Run(strings.NewReader(input.String()), blockingWriter{})
	case "drain-wedge":
		// One request whose response blocks the writer, then EOF: the
		// worker is blocked mid-write while the main loop joins it
		// (shutdown drain).  The events channel is empty, so a
		// delivered Fatal event sits unprocessed and a
		// delivery-blocking watchdog would never arm.  The
		// process-lifetime watchdog must still force the exit
		// (role-round finding).
		input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n"
		err = NewSession().Run(strings.NewReader(input), blockingWriter{})
	case "full-stderr":
		// Watcher force-exit independence (external review finding):
		// the parent gives this session a stderr that is a full,
		// never-drained pipe, so the forced-exit diagnostic can never
		// be written.  The idle session must still exit non-zero when
		// signalled, within the bounded watchdog window.
		r, _ := io.Pipe()
		err = NewSession().Run(r, io.Discard)
	case "full-stderr-wedged":
		// Wedged variant of the full-stderr signal shape (external
		// review finding): the signal must be served by the
		// process-lifetime watchdog's detached-diagnostic path, not
		// the graceful idle exit.  The worker blocks on the stalled
		// writer and the main loop on the full work queue, so only
		// the watchdog can exit; stderr is a full, never-drained pipe
		// so the forced-exit diagnostic can never be written.
		input := strings.Builder{}
		for i := 0; i < 512; i++ {
			input.WriteString("{\"jsonrpc\":\"2.0\",\"id\":" + strconv.Itoa(i) +
				",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n")
		}
		err = NewSession().Run(strings.NewReader(input.String()), blockingWriter{})
	case "graceful-fatal-full-stderr":
		// Graceful-fatal-path independence (role-round finding): the
		// session fails because its response write fails (the parent
		// gives fd 1 a stdout that fails every write), and rpc.Run's
		// best-effort diagnostic then cannot be written either (the
		// parent gives fd 2 a full, never-drained pipe).  The process
		// must still exit 1 within the bounded grace window, without
		// any signal.
		os.Exit(Run())
	case "oversized-unterminated":
		// Over-limit frame without a terminator (role-round finding):
		// the parent writes more than InputFrameLimit bytes with no LF
		// and holds stdin open.  The frame is already invalid once the
		// accumulated bytes exceed the ceiling, so the reader must not
		// wait for LF or EOF: the session answers -32001 with id null
		// and exits non-zero (spec iprange-jsonrpc-v1.md framing
		// section).
		err = NewSession().Run(os.Stdin, os.Stdout)
	case "oversized-eof":
		// Exactly LIMIT+1 payload bytes without LF, then EOF
		// (role-round finding, round 2): the EOF-resolved frame is
		// over the ceiling (no terminator exists to strip a CR for),
		// so the reader reports the framing failure: -32001 with id
		// null and a non-zero exit, pinned for Go parity with the
		// Rust reader.
		err = NewSession().Run(os.Stdin, os.Stdout)
	case "eof-first":
		// Supervisor stop sequence: the response for one request is
		// written, EOF follows immediately, and the termination
		// signal lands while the process is inside the EOF tail.
		// The signal must win over the exit-zero outcome (third
		// role-round finding: the pre-fix sigCh grace poll could
		// never win the FIFO receiver queue against the watcher, so
		// this shape exited 0).  The response line on stdout tells
		// the parent exactly when to signal.
		input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n"
		err = NewSession().Run(strings.NewReader(input), os.Stdout)
	default:
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// blockingWriter never accepts a write, like stdout whose pipe buffer
// is full and never drained (the stalled-consumer wedge state).
type blockingWriter struct{}

func (blockingWriter) Write([]byte) (int, error) { select {} }

// startSignalHelper spawns the helper subprocess in one scenario.
func startSignalHelper(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
	cmd.Env = append(os.Environ(), signalTestHelperEnv+"="+mode)
	cmd.Stderr = &bytes.Buffer{}
	return cmd
}

// signalHelperExit runs one helper scenario, delivers one signal, and
// returns the helper's exit code (wantNonZero=true) or its error.
func runSignalTrials(t *testing.T, mode string, sig syscall.Signal, wantExit int) {
	t.Helper()
	for iteration := 0; iteration < 2; iteration++ {
		cmd := startSignalHelper(t, mode)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		// Let the session install its signal watcher and (for wedged
		// and mid-drain) reach the target state before signalling.
		time.Sleep(300 * time.Millisecond)
		if err := cmd.Process.Signal(sig); err != nil {
			cmd.Process.Kill()
			t.Fatalf("signal %v: %v", sig, err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != wantExit {
				t.Fatalf("%s %v: exit = %v, want %d", mode, sig, err, wantExit)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("%s %v: helper did not terminate within 3s", mode, sig)
		}
	}
}

func TestTerminationSignalIdleExitsNonZero(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		runSignalTrials(t, "idle", sig, 1)
	}
}

func TestTerminationSignalWedgedSessionForcesNonZeroExit(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		runSignalTrials(t, "wedged", sig, 1)
	}
}

func TestTerminationSignalDuringDrainWinsOverEOF(t *testing.T) {
	runSignalTrials(t, "mid-drain", syscall.SIGTERM, 1)
}

func TestTerminationSignalPartialWedgeForcesNonZeroExit(t *testing.T) {
	// Role-round finding: partially-filled events channel + wedged
	// main loop.  Delivered Fatal events are never processed; only
	// the process-lifetime watchdog can serve the signal.
	runSignalTrials(t, "partial-wedge", syscall.SIGTERM, 1)
	runSignalTrials(t, "partial-wedge", syscall.SIGINT, 1)
}

func TestTerminationSignalDrainWedgeForcesNonZeroExit(t *testing.T) {
	// Role-round finding: worker blocked mid-write during the EOF
	// drain join.  The events channel is empty; only the
	// process-lifetime watchdog can serve the signal.
	runSignalTrials(t, "drain-wedge", syscall.SIGTERM, 1)
	runSignalTrials(t, "drain-wedge", syscall.SIGINT, 1)
}

func TestOversizedEOFExitsNonZero(t *testing.T) {
	// Role-round finding (round 2): a final unterminated frame of
	// exactly LIMIT+1 bytes at EOF is over the ceiling, so it is a
	// framing failure: one -32001 (id null) and a non-zero exit
	// (spec iprange-jsonrpc-v1.md framing + shutdown sections).  The
	// Go reader always reported the ceiling at EOF; this pins the
	// shape so the Rust reader cannot diverge again.
	for _, tail := range []string{"", "\r"} {
		for iteration := 0; iteration < 2; iteration++ {
			cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
			cmd.Env = append(os.Environ(), signalTestHelperEnv+"=oversized-eof")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatalf("stdin pipe: %v", err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				stdin.Close()
				t.Fatalf("stdout pipe: %v", err)
			}
			cmd.Stderr = &bytes.Buffer{}
			if err := cmd.Start(); err != nil {
				t.Fatalf("start helper: %v", err)
			}
			body := make([]byte, InputFrameLimit+1)
			for i := range body {
				body[i] = 'x'
			}
			if tail == "\r" {
				body[InputFrameLimit] = '\r'
			}
			written := 0
			for written < len(body) {
				take := 65536
				if left := len(body) - written; left < take {
					take = left
				}
				n, werr := stdin.Write(body[written : written+take])
				written += n
				if werr != nil {
					t.Fatalf("write overflowing bytes: %v", werr)
				}
			}
			stdin.Close() // EOF resolves the frame
			lineCh := make(chan string, 1)
			errCh := make(chan error, 1)
			go func() {
				line, rerr := bufio.NewReader(stdout).ReadString('\n')
				if rerr != nil {
					errCh <- rerr
					return
				}
				lineCh <- line
			}()
			select {
			case line := <-lineCh:
				var payload map[string]any
				if err := json.Unmarshal([]byte(line), &payload); err != nil {
					t.Fatalf("bad -32001 json: %v (%q)", err, line)
				}
				if payload["id"] != nil {
					t.Fatalf("expected null id, got %v", payload["id"])
				}
				errorObj, _ := payload["error"].(map[string]any)
				if code, _ := errorObj["code"].(float64); int(code) != -32001 {
					t.Fatalf("expected -32001, got %v", payload["error"])
				}
			case rerr := <-errCh:
				t.Fatalf("read -32001: %v", rerr)
			case <-time.After(3 * time.Second):
				cmd.Process.Kill()
				// Let the reader finish on the pipe EOF before Wait
				// closes the pipe (StdoutPipe: Wait must not run
				// while a read is in flight).
				select {
				case <-errCh:
				case <-time.After(time.Second):
				}
				cmd.Wait()
				stdin.Close()
				t.Fatalf("no -32001 response within 3s (tail %q)", tail)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			select {
			case err := <-done:
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 {
					t.Fatalf("oversized-eof (tail %q): exit = %v, want 1", tail, err)
				}
			case <-time.After(3 * time.Second):
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("oversized-eof: helper did not terminate within 3s")
			}
		}
	}
}

func TestGracefulFatalFullStderrForcedExit(t *testing.T) {
	// Role-round finding: the graceful fatal path (rpc.Run's
	// diagnostic on a session error) must never depend on a blocking
	// stderr write.  The helper runs the real Run() with a stdout
	// that fails every write (/dev/full); the failed response write
	// fails the session.  With stderr a full, undrained pipe the
	// process must still exit 1 within the bounded grace window, with
	// no signal at all.
	devfull, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("/dev/full not available: %v", err)
	}
	defer devfull.Close()
	for iteration := 0; iteration < 2; iteration++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
		cmd.Env = append(os.Environ(), signalTestHelperEnv+"=graceful-fatal-full-stderr")
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
		cmd.Stdout = devfull
		stdin, err := cmd.StdinPipe()
		if err != nil {
			stderrFile.Close()
			syscall.Close(fds[0])
			t.Fatalf("stdin pipe: %v", err)
		}
		if err := cmd.Start(); err != nil {
			stderrFile.Close()
			syscall.Close(fds[0])
			t.Fatalf("start helper: %v", err)
		}
		// Close the parent's copy of the stderr write end through
		// the *os.File, never a raw syscall.Close: the child holds
		// its own duplicate, and the os.File finalizer must never
		// close a later-reused fd number (pre-existing flaky EBADF
		// across this suite under load).
		stderrFile.Close()
		io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n")
		stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				syscall.Close(fds[0])
				t.Fatalf("graceful-fatal full stderr: exit = %v, want 1", err)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
			syscall.Close(fds[0])
			t.Fatalf("graceful-fatal full stderr: helper did not terminate within 3s")
		}
		syscall.Close(fds[0])
	}
}

func TestGracefulFatalDiagnosticStillReported(t *testing.T) {
	// Control for the graceful-fatal fix: with a drained stderr the
	// best-effort diagnostic must still land before the process exits
	// (only the blocked write is abandoned, not the message).
	devfull, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("/dev/full not available: %v", err)
	}
	defer devfull.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
	cmd.Env = append(os.Environ(), signalTestHelperEnv+"=graceful-fatal-full-stderr")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = devfull
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n")
	stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("control: exit = %v, want 1", err)
		}
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("control: helper did not terminate within 3s")
	}
	if !strings.Contains(stderr.String(), "iprange:") {
		t.Fatalf("diagnostic missing from drained stderr: %q", stderr.String())
	}
}

func TestOversizedUnterminatedFrameAnswersAndExits(t *testing.T) {
	// Role-round finding: an over-limit frame that never receives a
	// terminator must still produce the -32001 response (id null) and
	// a non-zero exit (spec iprange-jsonrpc-v1.md framing section);
	// the reader must not wait for LF or EOF once the ceiling is
	// exceeded.
	for iteration := 0; iteration < 2; iteration++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
		cmd.Env = append(os.Environ(), signalTestHelperEnv+"=oversized-unterminated")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			stdin.Close()
			t.Fatalf("stdout pipe: %v", err)
		}
		cmd.Stderr = &bytes.Buffer{}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		// LIMIT+2 bytes without LF; stdin stays open on purpose.
		buf := make([]byte, 65536)
		for i := range buf {
			buf[i] = 'x'
		}
		written := 0
		for written < InputFrameLimit+2 {
			take := len(buf)
			if left := InputFrameLimit + 2 - written; left < take {
				take = left
			}
			n, werr := stdin.Write(buf[:take])
			written += n
			if werr != nil {
				t.Fatalf("write oversized bytes: %v", werr)
			}
		}
		lineCh := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			line, rerr := bufio.NewReader(stdout).ReadString('\n')
			if rerr != nil {
				errCh <- rerr
				return
			}
			lineCh <- line
		}()
		select {
		case line := <-lineCh:
			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatalf("bad -32001 json: %v (%q)", err, line)
			}
			if payload["id"] != nil {
				t.Fatalf("expected null id, got %v", payload["id"])
			}
			errorObj, _ := payload["error"].(map[string]any)
			if code, _ := errorObj["code"].(float64); int(code) != -32001 {
				t.Fatalf("expected -32001, got %v", payload["error"])
			}
		case rerr := <-errCh:
			t.Fatalf("read -32001: %v", rerr)
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			// Let the reader finish on the pipe EOF before Wait
			// closes the pipe (StdoutPipe: Wait must not run
			// while a read is in flight).
			select {
			case <-errCh:
			case <-time.After(time.Second):
			}
			cmd.Wait()
			stdin.Close()
			t.Fatalf("no -32001 response within 3s")
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case err := <-done:
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				stdin.Close()
				t.Fatalf("oversized-unterminated: exit = %v, want 1", err)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
			stdin.Close()
			t.Fatalf("oversized-unterminated: helper did not terminate within 3s")
		}
		stdin.Close()
	}
}

func TestOversizedHeldNonCRFrameAnswersAndExits(t *testing.T) {
	// External review finding: a held-open frame of exactly LIMIT+1
	// bytes whose last byte is not the CR of a CRLF terminator can
	// never become legal -- even a following LF would leave the
	// payload over the ceiling -- so the reader must report -32001
	// (id null) and exit non-zero immediately, without waiting for
	// the terminator or EOF while the peer holds stdin open.
	for iteration := 0; iteration < 2; iteration++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
		cmd.Env = append(os.Environ(), signalTestHelperEnv+"=oversized-unterminated")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			stdin.Close()
			t.Fatalf("stdout pipe: %v", err)
		}
		cmd.Stderr = &bytes.Buffer{}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		// Exactly LIMIT+1 non-CR bytes without LF; stdin stays open
		// on purpose (the decisive shape: the pre-fix reader held the
		// bytes and awaited another byte forever).
		buf := make([]byte, 65536)
		for i := range buf {
			buf[i] = 'x'
		}
		written := 0
		for written < InputFrameLimit+1 {
			take := len(buf)
			if left := InputFrameLimit + 1 - written; left < take {
				take = left
			}
			n, werr := stdin.Write(buf[:take])
			written += n
			if werr != nil {
				t.Fatalf("write oversized bytes: %v", werr)
			}
		}
		lineCh := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			line, rerr := bufio.NewReader(stdout).ReadString('\n')
			if rerr != nil {
				errCh <- rerr
				return
			}
			lineCh <- line
		}()
		select {
		case line := <-lineCh:
			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatalf("bad -32001 json: %v (%q)", err, line)
			}
			if payload["id"] != nil {
				t.Fatalf("expected null id, got %v", payload["id"])
			}
			errorObj, _ := payload["error"].(map[string]any)
			if code, _ := errorObj["code"].(float64); int(code) != -32001 {
				t.Fatalf("expected -32001, got %v", payload["error"])
			}
		case rerr := <-errCh:
			t.Fatalf("read -32001: %v", rerr)
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			// Let the reader finish on the pipe EOF before Wait
			// closes the pipe (StdoutPipe: Wait must not run while a
			// read is in flight).
			select {
			case <-errCh:
			case <-time.After(time.Second):
			}
			cmd.Wait()
			stdin.Close()
			t.Fatalf("no -32001 response within 3s (held LIMIT+1 non-CR)")
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				stdin.Close()
				t.Fatalf("held LIMIT+1 non-CR: exit = %v, want 1", err)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
			stdin.Close()
			t.Fatalf("held LIMIT+1 non-CR: helper did not terminate within 3s")
		}
		stdin.Close()
	}
}

func TestTerminationSignalFullStderrForcedExit(t *testing.T) {
	// External review finding: the watchdog's forced exit must never
	// depend on a blocking diagnostic write.  A full, undrained
	// stderr pipe must not prevent the forced non-zero exit within
	// the bounded watchdog window.
	sig := syscall.SIGTERM
	for iteration := 0; iteration < 2; iteration++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
		// The wedged mode keeps the worker blocked on the stalled
		// writer so the signal is served by the watchdog path, with
		// the same full, undrained stderr pipe (external review
		// finding).
		cmd.Env = append(os.Environ(), signalTestHelperEnv+"=full-stderr-wedged")
		// The diagnostic pipe is built from raw syscalls: an os.Pipe
		// fd is registered with the runtime poller, and os.File
		// writes on the non-blocking fd would wait for writability
		// forever instead of returning EAGAIN.  Raw fds keep the
		// fill loop and the child's fd 2 purely syscall-side.
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatalf("pipe: %v", err)
		}
		// Fill the write side (the test never drains the read side),
		// so the child's stderr writes would block forever.
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
		if err := cmd.Start(); err != nil {
			stderrFile.Close()
			syscall.Close(fds[0])
			t.Fatalf("start helper: %v", err)
		}
		// Close the parent's copy of the stderr write end through
		// the *os.File, never a raw syscall.Close: the child holds
		// its own duplicate, and the os.File finalizer must never
		// close a later-reused fd number (pre-existing flaky EBADF
		// across this suite under load).
		stderrFile.Close()
		// Let the session install its signal watcher.
		time.Sleep(300 * time.Millisecond)
		if err := cmd.Process.Signal(sig); err != nil {
			cmd.Process.Kill()
			syscall.Close(fds[0])
			t.Fatalf("signal %v: %v", sig, err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				syscall.Close(fds[0])
				t.Fatalf("full-stderr %v: exit = %v, want 1", sig, err)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
			syscall.Close(fds[0])
			t.Fatalf("full-stderr %v: helper did not terminate within 3s", sig)
		}
		syscall.Close(fds[0])
	}
}

func TestTerminationSignalEOFFirstWinsOverExitZero(t *testing.T) {
	// Third role-round finding: stdin closes right after one request
	// and the termination signal lands while the process is inside
	// the EOF tail (the parent signals as soon as the response line
	// appears on the helper's stdout).  Pre-fix the sigCh grace poll
	// lost the FIFO receiver queue to the watcher and this shape
	// exited 0 ~100% of trials.
	for iteration := 0; iteration < 3; iteration++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTerminationSignalHelperProcess$")
		cmd.Env = append(os.Environ(), signalTestHelperEnv+"=eof-first")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("stdout pipe: %v", err)
		}
		cmd.Stderr = &bytes.Buffer{}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper: %v", err)
		}
		// The response line is written just before the EOF tail
		// starts; the tail stays alive for the 25 ms grace window.
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("read response: %v", err)
		}
		if !strings.Contains(line, "\"id\":1") {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("response line: %q", line)
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("signal: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				t.Fatalf("eof-first: exit = %v, want 1", err)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("eof-first: helper did not terminate within 3s")
		}
	}
}
