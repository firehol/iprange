//go:build unix

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// An exhausted descriptor table must produce an answer, not a wedge, and
// the answer must be the class of the operation that actually needed a
// descriptor (wave-19.25 design section 7). The retired per-request probe
// answered -32010 io/not_started for work the process could resource —
// including system.describe (zero descriptors) and the reader.close that
// gives descriptors back — and those refusals were pinned here as if they
// were the contract. The pins below are the measured reference classes;
// the full matrix (bands, occupied tables, runtime states, hostile null
// device, both engines) is owned by v4/cli/fd_pressure_harness.py, which
// this suite also drives for the Go engine so the expectations live in one
// place and the Go side is never graded against itself.

const (
	// fdPressureChildEnv marks the re-exec'd child that lowers its
	// descriptor limit and execs a product binary; its value is that
	// binary. fdPressureChildLimitEnv carries RLIMIT_NOFILE (soft=hard),
	// and fdPressureChildHoldEnv the number of descriptors the launcher
	// must additionally claim on the hold file before exec.
	fdPressureChildEnv      = "IPRANGE_GO_FD_PRESSURE_CHILD"
	fdPressureChildLimitEnv = "IPRANGE_GO_FD_PRESSURE_CHILD_LIMIT"
	fdPressureChildHoldEnv  = "IPRANGE_GO_FD_PRESSURE_CHILD_HOLD"
)

const (
	// fdPressureAnswerLimit bounds how long a pressured request may take to
	// answer. The Rust reference answers in 1-2 ms; the matrix cell budget
	// is 8 s and a cell that needs it is a wedge.
	fdPressureAnswerLimit = 8 * time.Second
	// fdPressureExitLimit bounds how long a SIGTERM'd pressured process may
	// take to disappear. The Rust reference exits in ~0.06 s.
	fdPressureExitLimit = 2 * time.Second
)

// Child exit codes of the launcher role, so the parent can tell a host that
// cannot prepare the table from a product answer.
const (
	fdPressureSetrlimitFailed = 91
	fdPressureBadLimit        = 92
	fdPressureExecFailed      = 93
	fdPressureExecReturned    = 94
)

// TestMain serves the child role before any test runs, so a re-exec of this
// binary can never start a second suite.
func TestMain(m *testing.M) {
	if binary := os.Getenv(fdPressureChildEnv); binary != "" {
		os.Exit(runFdPressureChild(binary))
	}
	os.Exit(m.Run())
}

// runFdPressureChild applies the descriptor limit, drops every descriptor
// above the three standard streams, claims the held descriptors, and
// replaces the image with the product binary. Both steps are required for
// fidelity: the re-exec'd test process owns descriptors that exec.Cmd
// leaves unmarked-close-on-exec, and a pressured CLI must start from the
// clean table a real caller gives it. The held opens are raw open(2):
// os.Open would register the launcher itself with the network poller and
// consume two of the very slots the cell is measuring.
func runFdPressureChild(binary string) int {
	ceiling := uint64(0)
	if text := os.Getenv(fdPressureChildLimitEnv); text != "" && text != "0" {
		limit, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			os.Stderr.WriteString("fd pressure: bad limit: " + err.Error() + "\n")
			return fdPressureBadLimit
		}
		if err := installDescriptorLimit(limit); err != nil {
			fmt.Fprintf(os.Stderr, "fd pressure: setrlimit %s: %v\n", text, err)
			return fdPressureSetrlimitFailed
		}
		ceiling = limit
	}
	closeDescriptorsAboveTwo(ceiling)
	if text := os.Getenv(fdPressureChildHoldEnv); text != "" && text != "0" {
		hold, err := strconv.Atoi(text)
		if err != nil || hold < 0 {
			os.Stderr.WriteString("fd pressure: bad hold: " + text + "\n")
			return fdPressureBadLimit
		}
		file := os.Getenv(fdPressureChildHoldFileEnv)
		if file == "" {
			file = "/dev/null"
		}
		for claimed := 0; claimed < hold; claimed++ {
			if _, err := unix.Open(file, unix.O_RDONLY, 0); err != nil {
				fmt.Fprintf(os.Stderr, "fd pressure: hold %d: %v\n", claimed, err)
				return fdPressureBadLimit
			}
		}
	}
	if err := unix.Exec(binary, []string{"iprange", "--jsonrpc"}, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "fd pressure: exec: %v\n", err)
		return fdPressureExecFailed
	}
	return fdPressureExecReturned
}

// fdPressureChildHoldFileEnv names the file the launcher holds descriptors
// on. The matrix requires it to live outside the work directory.
const fdPressureChildHoldFileEnv = "IPRANGE_GO_FD_PRESSURE_CHILD_HOLD_FILE"

// closeDescriptorsAboveTwo closes every descriptor but 0, 1, and 2. The scan
// stops at the descriptor ceiling (or a bounded default when the limit is
// unrestricted), and failures are ignored: closing a descriptor the process
// does not hold is the common case.
func closeDescriptorsAboveTwo(ceiling uint64) {
	limit := ceiling
	if limit <= 3 {
		limit = 1024
	}
	for descriptor := 3; descriptor < int(limit); descriptor++ {
		_ = unix.Close(descriptor)
	}
}

// productBinaries builds the CLI and its worker into one directory with the
// qualification recipe (design section 15: CGO_ENABLED=0, -trimpath,
// -buildvcs=false). The CLI requires the worker beside it, so an out-of-tree
// worker would change the answers under test.
//
// CGO_ENABLED=0 is not a style choice here: a cgo-enabled binary is
// dynamically linked, and the dynamic loader must open the shared libraries
// before the product's first instruction. Under a low RLIMIT_NOFILE those
// library opens fail and the process dies with "error while loading shared
// libraries" (exit 127) having answered nothing — which is the reference's
// documented host state (design section 13.1), not a product answer. Grading
// the Go side on a dynamic build would therefore record the Go engine's
// lowest bands as host-unsupported for a reason the shipped artifact does not
// have: the static Go build starts and answers at band 3.
func productBinaries(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	root := moduleRootForTest(t)
	for _, name := range []string{"iprange", "iprange-v4-worker"} {
		destination := filepath.Join(directory, name)
		command := exec.Command("go", "-C", root, "build", "-trimpath",
			"-buildvcs=false", "-o", destination, "./cmd/"+name)
		command.Env = append(childEnvironment(), "CGO_ENABLED=0")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, output)
		}
	}
	return directory
}

// referenceBinaries locates the Rust engine, its worker, and the fixture
// tool: the matrix compares both engines against one materialization, so the
// Go side is never graded against itself (design section 10). IPRANGE_V4_RUST_BIN
// names a directory holding all three; otherwise the release workspace output
// is used. The caller skips when neither exists, because the Rust side of the
// matrix is a second engine, not a helper the Go cells depend on.
func referenceBinaries(t *testing.T) (directory string, ok bool) {
	t.Helper()
	candidates := []string{}
	if from := os.Getenv("IPRANGE_V4_RUST_BIN"); from != "" {
		candidates = append(candidates, from)
	}
	if root, err := moduleRootForTestOrSkip(t); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(root),
			"rust", "target", "release"))
	}
	for _, candidate := range candidates {
		complete := true
		for _, name := range []string{"iprange", "iprange-v4-worker", "v4-fixture"} {
			if _, err := os.Stat(filepath.Join(candidate, name)); err != nil {
				complete = false
				break
			}
		}
		if complete {
			return candidate, true
		}
	}
	return "", false
}

// moduleRootForTestOrSkip is moduleRootForTest for callers that may skip
// instead of failing when the tree layout is unexpected.
func moduleRootForTestOrSkip(t *testing.T) (string, error) {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("no go.mod above the working directory")
		}
		directory = parent
	}
}

// moduleRootForTest walks up from the test working directory to the v4/go
// module root.
func moduleRootForTest(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("no go.mod above the working directory")
		}
		directory = parent
	}
}

// fdPressureRun is one pressured product process. The request arrives on a
// pipe (a file would reach EOF and cancel the unit); stdout and stderr are
// files so a wedged child cannot fill a pipe buffer the parent forgot.
type fdPressureRun struct {
	command    *exec.Cmd
	done       chan struct{}
	stdoutPath string
	stderrPath string
	request    io.WriteCloser

	mu     sync.Mutex
	status *os.ProcessState
}

// childExitCode returns the pressured process's exit status, or -1 while it
// is still running.
func (r *fdPressureRun) childExitCode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == nil {
		return -1
	}
	return r.status.ExitCode()
}

// skipIfHostCannotPressurize fails the test on a genuine wedge and skips it
// when the launcher itself could not prepare the table (a hard limit below
// the request, or held descriptors that do not fit): those are host states,
// never product refusals and never coverage (design sections 10 and 13.2).
func (r *fdPressureRun) skipIfHostCannotPressurize(t *testing.T) {
	t.Helper()
	switch code := r.childExitCode(); code {
	case fdPressureSetrlimitFailed, fdPressureBadLimit:
		t.Skipf("host cannot prepare the pressured table: %s",
			readTail(r.stderrPath))
	}
}

func startFdPressureRun(t *testing.T, binaries string, limit int, hold int, holdFile string, request string) *fdPressureRun {
	t.Helper()
	directory := t.TempDir()
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("create request pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = writeEnd.Close()
		_ = readEnd.Close()
	})
	stdoutPath := filepath.Join(directory, "stdout")
	stderrPath := filepath.Join(directory, "stderr")
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create stdout: %v", err)
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create stderr: %v", err)
	}
	t.Cleanup(func() {
		_ = stdout.Close()
		_ = stderr.Close()
	})
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary: %v", err)
	}
	command := exec.Command(self)
	command.Env = append([]string{
		fdPressureChildEnv + "=" + filepath.Join(binaries, "iprange"),
		fdPressureChildLimitEnv + "=" + strconv.Itoa(limit),
		fdPressureChildHoldEnv + "=" + strconv.Itoa(hold),
		fdPressureChildHoldFileEnv + "=" + holdFile,
	}, childEnvironment()...)
	command.Stdin = readEnd
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start pressured process: %v", err)
	}
	run := &fdPressureRun{command: command, done: make(chan struct{}),
		stdoutPath: stdoutPath, stderrPath: stderrPath, request: writeEnd}
	if _, err := writeEnd.Write([]byte(request)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	go func() {
		_ = command.Wait()
		run.mu.Lock()
		run.status = command.ProcessState
		run.mu.Unlock()
		close(run.done)
	}()
	t.Cleanup(func() {
		_ = run.request.Close()
		_ = run.command.Process.Kill()
		select {
		case <-run.done:
		case <-time.After(3 * time.Second):
		}
	})
	return run
}

// waitForAnswerLine returns the first complete response line, failing the
// test when the pressured process does not answer.
func (r *fdPressureRun) waitForAnswerLine(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(fdPressureAnswerLimit)
	for {
		if line := readFirstLine(t, r.stdoutPath); line != "" {
			return line
		}
		select {
		case <-r.done:
			if line := readFirstLine(t, r.stdoutPath); line != "" {
				return line
			}
			r.skipIfHostCannotPressurize(t)
			t.Fatalf("pressured process exited without answering; stderr: %s",
				readTail(r.stderrPath))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pressured process never answered within %v; stderr: %s",
				fdPressureAnswerLimit, readTail(r.stderrPath))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// sendLine writes one further frame to the pressured session. The matrix runs
// follow-up frames in the same process (an open then its close), because a
// released descriptor is only observable in the process that held it.
func (r *fdPressureRun) sendLine(t *testing.T, line string) {
	t.Helper()
	if _, err := r.request.Write([]byte(line)); err != nil {
		t.Fatalf("write follow-up frame: %v", err)
	}
}

// waitForAnswerNumber returns the n-th complete response line (1 based),
// failing when the pressured process dies before producing it or the bound
// expires.
func (r *fdPressureRun) waitForAnswerNumber(t *testing.T, n int) string {
	t.Helper()
	deadline := time.Now().Add(fdPressureAnswerLimit)
	for {
		if line := nthLine(t, r.stdoutPath, n); line != "" {
			return line
		}
		select {
		case <-r.done:
			if line := nthLine(t, r.stdoutPath, n); line != "" {
				return line
			}
			r.skipIfHostCannotPressurize(t)
			t.Fatalf("pressured process exited before answer %d; stderr: %s",
				n, readTail(r.stderrPath))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pressured process never produced answer %d within %v; stderr: %s",
				n, fdPressureAnswerLimit, readTail(r.stderrPath))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// terminate sends SIGTERM and requires the process to disappear.
func (r *fdPressureRun) terminate(t *testing.T) {
	t.Helper()
	if err := r.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	select {
	case <-r.done:
	case <-time.After(fdPressureExitLimit):
		_ = r.command.Process.Kill()
		t.Fatalf("pressured process ignored SIGTERM for %v; stderr: %s",
			fdPressureExitLimit, readTail(r.stderrPath))
	}
}

// stderrText returns the captured stderr of the run (already ended or not).
func (r *fdPressureRun) stderrText(t *testing.T) string {
	t.Helper()
	text, err := os.ReadFile(r.stderrPath)
	if err != nil {
		return ""
	}
	return string(text)
}

// sampledTable lists the target's descriptor links. The sample is taken from
// the parent through /proc, so it never touches the pressured table.
func (r *fdPressureRun) sampledTable(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", r.command.Process.Pid))
	if err != nil {
		t.Fatalf("sample descriptor table: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		link, err := os.Readlink(filepath.Join(
			fmt.Sprintf("/proc/%d/fd", r.command.Process.Pid), entry.Name()))
		if err != nil {
			out = append(out, entry.Name()+"=<unreadable>")
			continue
		}
		out = append(out, entry.Name()+" -> "+link)
	}
	return out
}

// answerClass extracts the response class from one answer line.
func answerClass(t *testing.T, line string) string {
	t.Helper()
	var frame struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Code    string `json:"code"`
				Outcome string `json:"outcome"`
			} `json:"data"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		t.Fatalf("response is not one JSON object %q: %v", line, err)
	}
	if frame.Result != nil {
		return "success"
	}
	return strconv.Itoa(frame.Error.Code) + " " + frame.Error.Data.Code + "/" + frame.Error.Data.Outcome
}

// TestFdPressureReferenceClassesUnderPressure pins the section 7 classes the
// probe could not get right: system.describe answers success at every
// launchable band (the probe refused it with io/not_started through band 8
// although it claims no descriptor), and direct.replace below its own
// minimum answers the io class of its writer's failed open — no longer a
// dispatcher invention. The SIGTERM disposition there must survive.
func TestFdPressureReferenceClassesUnderPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the product binaries under descriptor pressure")
	}
	binaries := productBinaries(t)
	directory, holdFile := pressureWorkdir(t)
	target := newLiveFeed(t, directory, "target.db")
	rows := filepath.Join(directory, "rows.csv")
	if err := os.WriteFile(rows, []byte("from,to,value\n192.0.2.0,192.0.2.3,1\n"), 0o600); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	describe := frameLine("iprange.v1.system.describe", map[string]any{})
	replace := directReplaceRequest(target, rows)

	t.Run("describe_success_bands_3_to_12", func(t *testing.T) {
		for _, limit := range []int{3, 4, 5, 6, 7, 8, 9, 10, 11, 12} {
			t.Run(strconv.Itoa(limit), func(t *testing.T) {
				run := startFdPressureRun(t, binaries, limit, 0, holdFile, describe)
				if got := answerClass(t, run.waitForAnswerLine(t)); got != "success" {
					t.Fatalf("system.describe at limit %d = %q, want success (§7)", limit, got)
				}
				if stderr := run.stderrText(t); strings.Contains(stderr, "runtime:") ||
					strings.Contains(stderr, "fatal error:") {
					t.Fatalf("system.describe at limit %d printed a runtime fatal: %s",
						limit, stderr)
				}
				run.terminate(t)
			})
		}
	})

	t.Run("replace_below_minimum_answers_the_writers_class", func(t *testing.T) {
		// Section 7: at 4 and 5 the writer's own open yields EMFILE and the
		// handler answers io/not_started — that class is now the operation's
		// own answer, not the dispatcher probe's.
		for _, limit := range []int{4, 5} {
			t.Run(strconv.Itoa(limit), func(t *testing.T) {
				run := startFdPressureRun(t, binaries, limit, 0, holdFile, replace)
				if got := answerClass(t, run.waitForAnswerLine(t)); got != "-32010 io/not_started" {
					t.Fatalf("direct.replace at limit %d = %q, want \"-32010 io/not_started\"", limit, got)
				}
				run.terminate(t)
			})
		}
		t.Run("terminates_at_4", func(t *testing.T) {
			run := startFdPressureRun(t, binaries, 4, 0, holdFile, replace)
			run.waitForAnswerLine(t)
			run.terminate(t)
		})
	})

	// Real work still completes once the table is generous.
	t.Run("work_at_generous_limit", func(t *testing.T) {
		run := startFdPressureRun(t, binaries, 64, 0, holdFile, replace)
		line := run.waitForAnswerLine(t)
		if got := answerClass(t, line); got != "success" {
			t.Fatalf("direct.replace at limit 64 = %q, want success (%s)", got, line)
		}
	})
}

// TestFdPressurePollerFreeProcess pins the process-level promise of the wave
// (design section 10): for every arm except host-name resolution, the child's
// descriptor table at the answer contains neither anon_inode:[eventpoll] nor
// anon_inode:[eventfd] — in EVERY band from 3 to 12, not only the bands where
// the poller-readiness decision of section 6 refuses it.
//
// This equality is what makes calleropen's promise a property of the process
// rather than of one package: any regression that reintroduces a pollable
// registration (a bare os.Open of a persistent node, an unowned runtime timer,
// a crypto/rand draw that arms the entropy-warning timer, or an exec.Cmd that
// opens the null device for the worker) puts those two descriptors in the
// sampled table and fails a cell here, even though the request itself still
// answers correctly. The resolver arm, the only arm allowed to reach the
// poller, is pinned by the committed matrix harness with the authorized-pair
// equality (v4/cli/fd_pressure_harness.py).
func TestFdPressurePollerFreeProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the product binaries under descriptor pressure")
	}
	binaries := productBinaries(t)
	directory, holdFile := pressureWorkdir(t)
	target := newLiveFeed(t, directory, "target.db")
	rows := filepath.Join(directory, "rows.csv")
	if err := os.WriteFile(rows, []byte("from,to,value\n192.0.2.0,192.0.2.3,1\n"), 0o600); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	cases := []struct {
		name    string
		request string
	}{
		{"system.describe", frameLine("iprange.v1.system.describe", map[string]any{})},
		{"direct.replace", directReplaceRequest(target, rows)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, limit := range []int{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 64} {
				t.Run(strconv.Itoa(limit), func(t *testing.T) {
					run := startFdPressureRun(t, binaries, limit, 0, holdFile, tc.request)
					run.waitForAnswerLine(t)
					for _, entry := range run.sampledTable(t) {
						if strings.Contains(entry, "anon_inode:[eventpoll]") ||
							strings.Contains(entry, "anon_inode:[eventfd]") {
							t.Fatalf("%s at band %d put the runtime network poller in the "+
								"process table (section 10): %v", tc.name, limit, entry)
						}
					}
					if stderr := run.stderrText(t); strings.Contains(stderr, "runtime:") ||
						strings.Contains(stderr, "fatal error:") {
						t.Fatalf("%s at band %d printed a runtime fatal: %s", tc.name, limit, stderr)
					}
					run.terminate(t)
				})
			}
		})
	}
}

// TestFdPressureReaderCloseIsAlwaysAnswered pins the releasing operation
// (design sections 5.7 and 7): reader.close exists to hand descriptors back,
// so when its open succeeded the close is answered success at every band
// where the pair is reachable, and never takes the io class the retired probe
// invented for it. The retired probe refused this close at bands 6..8
// (immutable) and 8..10 (live) after a successful open, leaking the reader.
func TestFdPressureReaderCloseIsAlwaysAnswered(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the product binaries under descriptor pressure")
	}
	binaries := productBinaries(t)
	directory, holdFile := pressureWorkdir(t)
	live := newLiveFeed(t, directory, "live-reader.db")
	open := frameLine("iprange.v1.reader.open", map[string]any{
		"source": map[string]any{"path": live, "mode": "live"},
	})
	for _, limit := range []int{7, 8, 9, 10, 11, 12, 64} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			run := startFdPressureRun(t, binaries, limit, 0, holdFile, open)
			line := run.waitForAnswerLine(t)
			if got := answerClass(t, line); got != "success" {
				// Below the live reader's own minimum the refusal belongs to the
				// open (section 7: io/read_only_failure) and there is nothing to
				// release, so this band is not a close case.
				if got != "-32010 io/read_only_failure" {
					t.Fatalf("reader.open at band %d = %q, want success or the "+
						"section 7 below-minimum class", limit, got)
				}
				run.terminate(t)
				t.Skipf("reader.open at band %d is below its minimum", limit)
			}
			reader := answerStringField(t, line, "reader")
			run.sendLine(t, frameLine("iprange.v1.reader.close",
				map[string]any{"reader": reader}))
			closeLine := run.waitForAnswerNumber(t, 2)
			if got := answerClass(t, closeLine); got != "success" {
				t.Fatalf("reader.close after a successful open at band %d = %q, "+
					"want success (a releasing operation is never refused, §7)", limit, got)
			}
			run.terminate(t)
		})
	}
}

// TestFdPressureOccupiedTableIsHostStateOrReference verifies the launcher's
// occupied-table rules: bands whose limit cannot supply 3 stdio + the held
// descriptors are host state (the run skips, it never passes as coverage
// and never counts as a refusal), and where the table fits, the answer is
// the same class as the fresh-table cell.
func TestFdPressureOccupiedTableIsHostStateOrReference(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the product binaries under descriptor pressure")
	}
	binaries := productBinaries(t)
	directory, holdFile := pressureWorkdir(t)
	_ = directory
	describe := frameLine("iprange.v1.system.describe", map[string]any{})
	for _, limit := range []int{6, 8, 10, 12} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			run := startFdPressureRun(t, binaries, limit, 3, holdFile, describe)
			// skipIfHostCannotPressurize covers bands where even the
			// launcher's three holds do not fit; the surviving cells must
			// still answer system.describe with success.
			line := run.waitForAnswerLine(t)
			if got := answerClass(t, line); got != "success" {
				t.Fatalf("system.describe at limit %d with 3 held = %q, want success", limit, got)
			}
			run.terminate(t)
		})
	}
}

// TestFdPressureMatrixViaCommittedHarness drives the committed section 10
// matrix harness (v4/cli/fd_pressure_harness.py) for the Go engine over a
// tight smoke band set, so the shipped expectations in that file — the
// section 7 table, the poller pin, the warm-up traps, the host-state rules
// — run inside go test, not only at milestone sweeps. The full grid
// (bands 3..12, both engines, every run) belongs to the harness itself.
func TestFdPressureMatrixViaCommittedHarness(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the product binaries under descriptor pressure")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not available to run the committed matrix harness")
	}
	root := moduleRootForTest(t)
	harness := filepath.Join(filepath.Dir(root), "cli", "fd_pressure_harness.py")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("matrix harness %s: %v", harness, err)
	}
	binaries := productBinaries(t)
	rustBin, haveReference := referenceBinaries(t)
	if !haveReference {
		t.Skip("the matrix needs the Rust engine, worker and v4-fixture (build " +
			"cargo build --release --all-features --bins --examples --manifest-path v4/rust/Cargo.toml " +
			"or set IPRANGE_V4_RUST_BIN); the Go side is not graded against itself")
	}
	fixture := filepath.Join(rustBin, "v4-fixture")
	scratch := t.TempDir()
	type cell struct {
		arm     string
		band    int
		hold    int
		runtime string
		null    string
	}
	cells := []cell{
		{"system.describe", 4, 0, "before", "normal"},
		{"system.describe", 7, 3, "after", "normal"},
		{"reader-open-close-immutable", 4, 0, "before", "normal"},
		{"reader-open-close-immutable", 6, 0, "after", "normal"},
		{"reader-open-close-live", 7, 0, "before", "normal"},
		{"direct.replace", 8, 0, "before", "normal"},
		{"direct.replace", 5, 0, "after", "normal"},
		{"current.publish", 8, 0, "before", "normal"},
		{"current.publish.hostname", 9, 0, "before", "normal"},
		{"maintenance.remove", 64, 0, "before", "normal"},
		{"validate(worker)", 10, 0, "before", "normal"},
		{"recovery.inspect(worker)", 9, 0, "before", "normal"},
		{"system.describe", 64, 0, "before", "fifo"},
		{"validate(worker)", 64, 0, "before", "absent"},
	}
	for _, c := range cells {
		name := fmt.Sprintf("%s/b%d_h%d_%s_%s", c.arm, c.band, c.hold, c.runtime, c.null)
		t.Run(name, func(t *testing.T) {
			command := exec.Command(python, harness, "run-cell",
				"--engine", "go",
				"--go-bin", binaries, "--rust-bin", rustBin,
				"--fixture", fixture, "--scratch", scratch, "--keep-work",
				"--arm", c.arm, "--band", strconv.Itoa(c.band),
				"--hold", strconv.Itoa(c.hold), "--runtime", c.runtime,
				"--null-device", c.null)
			output, err := command.CombinedOutput()
			var record map[string]any
			if json.Unmarshal(output, &record) != nil {
				t.Fatalf("harness did not answer JSON for %s: %v\n%s", name, err, output)
			}
			switch verdict := record["verdict"]; verdict {
			case "pass", "band-gap", "host-unsupported", "blocked":
				t.Logf("verdict %v: %v", verdict, record["why"])
			default:
				t.Fatalf("harness verdict %v for %s: %v — %s", verdict, name,
					record["why"], output)
			}
			if err != nil && record["verdict"] != "host-unsupported" {
				t.Fatalf("harness exited %v for %s: %s", err, name, output)
			}
		})
	}
}

// directReplaceRequest builds one well-formed direct.replace frame.
func directReplaceRequest(target, rows string) string {
	return frameLine("iprange.v1.direct.replace", map[string]any{
		"path":     target,
		"input":    map[string]any{"path": rows, "max_line_bytes": 1024},
		"metadata": map[string]any{"mode": "keep"},
		"writer_budget": map[string]any{
			"max_heap_bytes":    "16777216",
			"max_private_pages": "256",
			"max_growth_pages":  "256",
			"max_open_files":    4,
		},
	})
}

// pressureWorkdir creates the cell directory plus the hold file, which the
// matrix requires to live OUTSIDE the work directory.
func pressureWorkdir(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	outside, err := os.MkdirTemp(filepath.Dir(directory), "fd-pressure-hold-")
	if err != nil {
		t.Fatalf("hold directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	holdFile := filepath.Join(outside, "held")
	if err := os.WriteFile(holdFile, []byte("held descriptor target\n"), 0o600); err != nil {
		t.Fatalf("hold file: %v", err)
	}
	return directory, holdFile
}

// frameLine encodes one request frame (nil params for methods without any).
func frameLine(method string, params map[string]any) string {
	frame := map[string]any{"jsonrpc": "2.0", "id": "fd-pressure", "method": method}
	if params != nil {
		frame["params"] = params
	}
	data, err := json.Marshal(frame)
	if err != nil {
		panic(err)
	}
	return string(data) + "\n"
}

// nthLine returns the n-th complete line of one file (1 based), or "" while
// the file holds fewer complete lines.
func nthLine(t *testing.T, path string, n int) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("open %s: %v", path, err)
		}
		return ""
	}
	defer file.Close()
	text, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	// Only complete lines count: a frame still in flight has no terminator,
	// and returning a partial line would let a truncated answer be parsed.
	complete := strings.Count(string(text), "\n")
	if complete < n {
		return ""
	}
	return strings.SplitN(string(text), "\n", n+1)[n-1]
}

// answerStringField reads one top-level string field out of a response line's
// result object (the handle fields of reader.open and friends).
func answerStringField(t *testing.T, line, field string) string {
	t.Helper()
	var frame struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		t.Fatalf("response is not one JSON object %q: %v", line, err)
	}
	value, ok := frame.Result[field].(string)
	if !ok || value == "" {
		t.Fatalf("response %q has no string field %q", line, field)
	}
	return value
}

// readFirstLine returns the first complete line of one file, or "" while it
// has none.
func readFirstLine(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("open %s: %v", path, err)
		}
		return ""
	}
	defer file.Close()
	text, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	line, _, found := strings.Cut(string(text), "\n")
	if !found {
		return ""
	}
	return line
}

// readTail returns the last bytes of one file for a failure message.
func readTail(path string) string {
	text, err := os.ReadFile(path)
	if err != nil {
		return "<unreadable: " + err.Error() + ">"
	}
	const keep = 400
	if len(text) > keep {
		text = text[len(text)-keep:]
	}
	return string(text)
}

// childEnvironment keeps the environment a child needs to start, without the
// markers that would make it recurse.
func childEnvironment() []string {
	keep := []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL",
		"GOTMPDIR", "GOCACHE", "GOPATH", "GOROOT", "GOFLAGS", "HTTP_PROXY",
		"HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}
	out := make([]string, 0, len(keep))
	for _, name := range keep {
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}
