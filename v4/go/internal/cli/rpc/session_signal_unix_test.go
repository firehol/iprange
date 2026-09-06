//go:build unix

// Termination-signal contract tests (wave-10 D1 repair).  Unix-only:
// the tests deliver real SIGINT/SIGTERM to helper subprocesses, and
// (os.Process).Signal is unsupported on Windows for non-Kill signals
// (portability role finding).  Mirrors the Rust process tests in
// v4/rust/iprange-cli/tests/termination_signals.rs.

package rpc

import (
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
		// openainer and never reads stdout: the worker blocks on the
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
