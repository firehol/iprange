//go:build unix

package handlers

import (
	"encoding/json"
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

// An exhausted descriptor table must produce an answer, not a wedge.
//
// Under a very low RLIMIT_NOFILE the CLI process has so few spare
// descriptors that a handler's first ordinary open asks the Go runtime to
// create the network poller, and the runtime has no failure path for a
// poller it cannot create: it aborts. Before the session refused such work
// up front, one `direct.replace` at limit 4 or 5 never answered, the
// process survived with `runtime: epollcreate failed with 24` or `fatal
// error: runtime: netpollinit failed` on stderr, and SIGTERM was ignored —
// while the Rust binary answers the io class within milliseconds and exits
// on SIGTERM.
//
// The pin runs the real product binaries (never this test process) in a
// child that lowers its own descriptor limit before exec, so what is
// observed is the released CLI's behavior including the runtime's. The
// boundary is part of the contract: at limits 6 and 8 — where the released
// binaries always worked — every answer must stay byte-identical to the
// unlimited run, so the up-front refusal cannot become a new failure for a
// merely tight process.

// fdPressureChildEnv marks the re-exec'd child that lowers its descriptor
// limit and execs a product binary; its value is that binary.
// fdPressureChildLimitEnv carries the requested RLIMIT_NOFILE.
const (
	fdPressureChildEnv      = "IPRANGE_GO_FD_PRESSURE_CHILD"
	fdPressureChildLimitEnv = "IPRANGE_GO_FD_PRESSURE_CHILD_LIMIT"
)

const (
	// fdPressureAnswerLimit bounds how long a pressured request may take to
	// answer. The Rust reference answers in 1-2 ms.
	fdPressureAnswerLimit = 5 * time.Second
	// fdPressureExitLimit bounds how long a SIGTERM'd pressured process may
	// take to disappear. The Rust reference exits in ~0.06 s.
	fdPressureExitLimit = 2 * time.Second
)

// TestMain serves the pressured child role before any test runs, so a
// re-exec of this binary can never start a second suite.
func TestMain(m *testing.M) {
	if binary := os.Getenv(fdPressureChildEnv); binary != "" {
		os.Exit(runFdPressureChild(binary))
	}
	os.Exit(m.Run())
}

// runFdPressureChild applies the descriptor limit, drops every descriptor
// above the three standard streams, and replaces the image with the product
// binary. Both steps are required for fidelity: the re-exec'd test process
// owns descriptors that exec.Cmd leaves unmarked-close-on-exec, and a
// pressured CLI must start from the clean three-descriptor table a real
// caller gives it.
func runFdPressureChild(binary string) int {
	ceiling := uint64(0)
	if text := os.Getenv(fdPressureChildLimitEnv); text != "" && text != "0" {
		limit, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			os.Stderr.WriteString("fd pressure: bad limit: " + err.Error() + "\n")
			return fdPressureBadLimit
		}
		// A hard limit below the request makes this fail with EPERM; the
		// parent then skips that case instead of accepting a false pass.
		if err := installDescriptorLimit(limit); err != nil {
			fmt.Fprintf(os.Stderr, "fd pressure: setrlimit %s: %v\n", text, err)
			return fdPressureSetrlimitFailed
		}
		ceiling = limit
	}
	closeDescriptorsAboveTwo(ceiling)
	if err := unix.Exec(binary, []string{"iprange", "--jsonrpc"}, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "fd pressure: exec: %v\n", err)
		return fdPressureExecFailed
	}
	return fdPressureExecReturned
}

// Child exit codes, so the parent can tell an unsupported case from a
// refused request.
const (
	fdPressureSetrlimitFailed = 91
	fdPressureBadLimit        = 92
	fdPressureExecFailed      = 93
	fdPressureExecReturned    = 94
)

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

// productBinaries builds the CLI and its worker into one directory: the CLI
// requires the worker beside it, so an out-of-tree worker would change the
// answers under test.
func productBinaries(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	root := moduleRootForTest(t)
	for _, name := range []string{"iprange", "iprange-v4-worker"} {
		destination := filepath.Join(directory, name)
		command := exec.Command("go", "-C", root, "build", "-o", destination, "./cmd/"+name)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, output)
		}
	}
	return directory
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

// fdPressureRun is one pressured product process. The request and answer are
// files rather than pipes, so the parent never holds a descriptor the
// pressured child needs and a wedged child cannot fill a pipe buffer.
type fdPressureRun struct {
	command    *exec.Cmd
	done       chan struct{}
	stdoutPath string
	stderrPath string

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
// when the child itself could not install the requested descriptor limit
// (a workstation whose hard RLIMIT_NOFILE sits below the case).
func (r *fdPressureRun) skipIfHostCannotPressurize(t *testing.T) {
	t.Helper()
	switch code := r.childExitCode(); code {
	case fdPressureSetrlimitFailed, fdPressureBadLimit:
		t.Skipf("host cannot run the product binary under the requested descriptor limit: %s",
			readTail(r.stderrPath))
	}
}

func startFdPressureRun(t *testing.T, binaries string, limit int, request string) *fdPressureRun {
	t.Helper()
	directory := t.TempDir()
	// The request arrives on a pipe whose write end the parent keeps open.
	// A file would reach end-of-stream immediately, and the session treats
	// that as shutdown: the unit under test would be cancelled instead of
	// answered.
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
	}, childEnvironment()...)
	command.Stdin = readEnd
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start pressured process: %v", err)
	}
	run := &fdPressureRun{command: command, done: make(chan struct{}),
		stdoutPath: stdoutPath, stderrPath: stderrPath}
	_ = writeEnd
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
		_ = command.Process.Kill()
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

// TestFdPressureIsRefusedPromptlyAndTerminates pins the io-class refusal at
// the two limits where the handler could previously die inside the runtime,
// the SIGTERM disposition there, and the byte-identical answers at the
// limits where the released binaries already worked.
func TestFdPressureIsRefusedPromptlyAndTerminates(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the product binaries under descriptor pressure")
	}
	binaries := productBinaries(t)
	directory, err := os.MkdirTemp("", "iprange-fd-pressure-")
	if err != nil {
		t.Fatalf("work directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	target := newLiveFeed(t, directory, "target.db")
	rows := filepath.Join(directory, "rows.csv")
	if err := os.WriteFile(rows, []byte("from,to,value\n192.0.2.0,192.0.2.3,1\n"), 0o600); err != nil {
		t.Fatalf("write rows: %v", err)
	}
	const reserveRefusal = "-32010 io/not_started"
	request := directReplaceRequest(target, rows)
	missingRequest := directReplaceRequest(filepath.Join(directory, "absent.db"), rows)

	for _, limit := range []int{4, 5} {
		t.Run("refused_at_"+strconv.Itoa(limit), func(t *testing.T) {
			run := startFdPressureRun(t, binaries, limit, request)
			got := answerClass(t, run.waitForAnswerLine(t))
			if got != "-32010 io/not_started" {
				t.Fatalf("answer = %q, want \"-32010 io/not_started\"", got)
			}
		})
		t.Run("terminates_at_"+strconv.Itoa(limit), func(t *testing.T) {
			run := startFdPressureRun(t, binaries, limit, request)
			run.waitForAnswerLine(t)
			run.terminate(t)
		})
	}

	// At and above the reserve the answers must not change at all: the same
	// request answers identically with or without the limit, so the probe
	// cannot invent a refusal for a merely tight process. The request here
	// refuses before any descriptor-heavy work, which keeps the comparison
	// about the probe rather than about the worker's own table.
	baseline := ""
	for _, limit := range []int{0, 6, 8} {
		name := "unlimited"
		if limit != 0 {
			name = strconv.Itoa(limit)
		}
		t.Run("boundary_"+name, func(t *testing.T) {
			run := startFdPressureRun(t, binaries, limit, missingRequest)
			line := run.waitForAnswerLine(t)
			if limit == 0 {
				if got := answerClass(t, line); got == reserveRefusal {
					t.Fatalf("the reserve probe refused a limit-free process: %s", line)
				}
				baseline = line
				return
			}
			if line != baseline {
				t.Fatalf("answer at limit %s differs from the unlimited baseline:\n got %s\nwant %s",
					name, line, baseline)
			}
		})
	}

	// Real work still completes once the table is generous: the reserve
	// probe must refuse only the processes that cannot resource a request.
	t.Run("work_at_generous_limit", func(t *testing.T) {
		run := startFdPressureRun(t, binaries, 64, request)
		line := run.waitForAnswerLine(t)
		if got := answerClass(t, line); got != "success" {
			t.Fatalf("direct.replace at limit 64 = %q, want success (%s)", got, line)
		}
	})
}

// directReplaceRequest builds one well-formed direct.replace frame.
func directReplaceRequest(target, rows string) string {
	frame := map[string]any{
		"jsonrpc": "2.0",
		"id":      "fd-pressure",
		"method":  "iprange.v1.direct.replace",
		"params": map[string]any{
			"path":     target,
			"input":    map[string]any{"path": rows, "max_line_bytes": 1024},
			"metadata": map[string]any{"mode": "keep"},
			"writer_budget": map[string]any{
				"max_heap_bytes":    "16777216",
				"max_private_pages": "256",
				"max_growth_pages":  "256",
				"max_open_files":    4,
			},
		},
	}
	data, err := json.Marshal(frame)
	if err != nil {
		panic(err)
	}
	return string(data) + "\n"
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
