//go:build unix

package calleropen

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The package promise is that the descriptor it hands onward is blocking,
// so os.NewFile cannot attach the handle to the runtime network poller
// (os/file_unix.go derives pollability from F_GETFL for kindNewFile). That
// matters because the poller's initialization has no failure path: under a
// low RLIMIT_NOFILE the runtime aborts with "fatal error: runtime:
// netpollinit failed" instead of the io error the open should have
// reported, and a file-only SDK must not depend on the poller at all.
//
// These pins observe the promise two ways, in a fresh process per case so
// nothing else in the test binary can have touched the poller first:
//
//   - F_GETFL of the descriptor the owner returned must have O_NONBLOCK
//     cleared, and
//   - the descriptor table must gain no descriptor beyond the handle
//     itself, which is where the poller's epoll/kqueue fd (plus the Linux
//     wake eventfd) would appear.
//
// Each case has a control that performs the same open through plain
// os.OpenFile, and must show the opposite of both observations. Without
// the controls, an environment in which the poller is already up (or never
// comes up) would turn the package-side assertions into a silent pass.

// netpollChildEnv names the child role this process must serve instead of
// running the suite.
const netpollChildEnv = "IPRANGE_GO_CALLEROPEN_NETPOLL_CHILD"

// netpollChildTimeout bounds one child. A dropped O_NONBLOCK makes the
// child hang inside open(2) on the writerless FIFO, so the bound is the
// detection, and it is generous because the child also builds fixtures.
const netpollChildTimeout = 30 * time.Second

// TestMain serves the child role before any test runs, so a re-exec of
// this binary can never start a second suite.
func TestMain(m *testing.M) {
	if role := os.Getenv(netpollChildEnv); role != "" {
		os.Exit(runNetpollChild(role))
	}
	if code, served := maybeServeReadinessChild(); served {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// netpollCase is one observed open, its control flag, and what the package
// owes for it.
type netpollCase struct {
	// role selects the child's measured operation.
	role string
	// wantBlocking is the O_NONBLOCK state the package must leave the
	// handed-onward descriptor in (false for the owner cases, true for
	// the plain os.OpenFile controls).
	wantBlocking bool
	// wantPoller reports whether the case is expected to attach the
	// handle to the network poller.
	wantPoller bool
}

var netpollCases = []netpollCase{
	{role: "caller-open-fifo", wantBlocking: false, wantPoller: false},
	{role: "caller-open-regular", wantBlocking: false, wantPoller: false},
	{role: "caller-blocking-wrap", wantBlocking: false, wantPoller: false},
	{role: "plain-openfile-fifo", wantBlocking: true, wantPoller: true},
	{role: "plain-openfile-regular", wantBlocking: true, wantPoller: true},
}

// TestCallerOpenHandsOnwardBlockingDescriptors pins the netpoller-free
// invariant of Open and Blocking, with the plain os.OpenFile opens as the
// controls that prove both observations are sensitive.
func TestCallerOpenHandsOnwardBlockingDescriptors(t *testing.T) {
	for _, tc := range netpollCases {
		t.Run(tc.role, func(t *testing.T) {
			report := runNetpollChildProcess(t, tc.role)
			if report.err != "none" {
				t.Fatalf("%s: the measured open failed: %s (raw %q)", tc.role, report.err, report.raw)
			}
			if report.ours < 0 {
				t.Fatalf("%s: the child opened nothing to observe (raw %q)", tc.role, report.raw)
			}
			if got := report.nonblock == 1; got != tc.wantBlocking {
				t.Fatalf("%s: F_GETFL O_NONBLOCK = %v, want %v (raw %q)",
					tc.role, got, tc.wantBlocking, report.raw)
			}
			if got := len(report.extra) > 0; got != tc.wantPoller {
				if tc.wantPoller {
					t.Fatalf("%s: the plain non-blocking open attached nothing to the poller (%d descriptors at the start, no new descriptor beyond the handle), so this round's poller observation proves nothing (raw %q)",
						tc.role, report.baseline, report.raw)
				}
				t.Fatalf("%s: the caller open attached the handle to the network poller: it gained %v beyond its own descriptor %d, which is what an un-cleared O_NONBLOCK makes os.NewFile do (raw %q)",
					tc.role, report.extra, report.ours, report.raw)
			}
		})
	}
}

// netpollReport is what one child observed around its measured open.
type netpollReport struct {
	role     string
	baseline int
	ours     int
	err      string
	nonblock int // 1 when F_GETFL reports O_NONBLOCK set
	extra    []int
	raw      string
}

// runNetpollChildProcess re-executes this test binary as the requested
// child role and parses its one-line report.
func runNetpollChildProcess(t *testing.T, role string) netpollReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), netpollChildTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestCallerOpenHandsOnwardBlockingDescriptors")
	command.Env = append(os.Environ(), netpollChildEnv+"="+role)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: child exited with %v\n%s", role, err, output)
	}
	report, parseErr := parseNetpollReport(string(output))
	if parseErr != nil {
		t.Fatalf("%s: the child reported nothing usable: %v\n%s", role, parseErr, output)
	}
	if report.role != role {
		t.Fatalf("%s: the child served role %q instead\n%s", role, report.role, output)
	}
	return report
}

func parseNetpollReport(text string) (netpollReport, error) {
	var report netpollReport
	report.ours = -1
	report.nonblock = -1
	scanner := bufio.NewScanner(strings.NewReader(text))
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "NETPOLL ") {
			continue
		}
		found = true
		for _, field := range strings.Fields(line) {
			if field == "NETPOLL" {
				continue
			}
			name, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch name {
			case "ROLE":
				report.role = value
			case "BASELINE":
				report.baseline = atoiField(value)
			case "OURS":
				report.ours = atoiField(value)
			case "ERR":
				report.err = value
			case "NONBLOCK":
				report.nonblock = atoiField(value)
			case "EXTRA":
				if value != "" {
					for _, item := range strings.Split(value, ",") {
						report.extra = append(report.extra, atoiField(item))
					}
				}
			}
		}
	}
	if !found {
		return report, fmt.Errorf("no NETPOLL line in %q", text)
	}
	if report.err == "none" && report.nonblock < 0 {
		return report, fmt.Errorf("report is missing the F_GETFL observation: %q", text)
	}
	return report, nil
}

func atoiField(text string) int {
	value, _ := strconv.Atoi(text)
	return value
}

// runNetpollChild is the child: it builds its fixtures with raw syscalls
// only (an os.OpenFile before the measurement would attach the poller and
// destroy the control it is meant to provide), takes the descriptor-table
// baseline, performs the one measured open, and prints the observation.
func runNetpollChild(role string) int {
	dir, err := os.MkdirTemp("", "iprange-calleropen-netpoll")
	if err != nil {
		fmt.Fprintf(os.Stderr, "netpoll child %s: fixtures: %v\n", role, err)
		return 1
	}
	fifo := filepath.Join(dir, "input.fifo")
	regular := filepath.Join(dir, "input.txt")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "netpoll child %s: mkfifo: %v\n", role, err)
		return 1
	}
	if err := writeRegularFileWithSyscalls(regular, []byte("192.0.2.1\n")); err != nil {
		fmt.Fprintf(os.Stderr, "netpoll child %s: create fixture: %v\n", role, err)
		return 1
	}

	bound := descriptorScanBound()
	before := openDescriptors(bound)
	var (
		ours     = -1
		errText  = "none"
		nonblock = -1
	)
	switch role {
	case "caller-open-fifo":
		ours, errText, nonblock = measure(func() (*os.File, error) {
			return Open(fifo, os.O_RDONLY|unix.O_NONBLOCK, 0)
		})
	case "caller-open-regular":
		ours, errText, nonblock = measure(func() (*os.File, error) {
			return Open(regular, os.O_RDONLY|unix.O_NONBLOCK, 0)
		})
	case "caller-blocking-wrap":
		// Reproduce the state Blocking exists for: a descriptor the
		// caller opened itself, still in non-blocking mode.
		fd, openErr := unix.Open(fifo, os.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if openErr != nil {
			errText = "raw-open:" + openErr.Error()
			break
		}
		flag, flagErr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if flagErr != nil || flag&unix.O_NONBLOCK == 0 {
			unix.Close(fd)
			errText = "raw-open-not-nonblocking"
			break
		}
		ours, errText, nonblock = func() (int, string, int) {
			file, fileErr := Blocking(fd, fifo)
			if fileErr != nil {
				return -1, "blocking:" + fileErr.Error(), -1
			}
			fdNumber := int(file.Fd())
			gotFlags, gotErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
			outcome := "none"
			state := -1
			if gotErr != nil {
				outcome = "fcntl:" + gotErr.Error()
			} else {
				state = boolToInt(gotFlags&unix.O_NONBLOCK != 0)
			}
			_ = file.Close()
			return fdNumber, outcome, state
		}()
	case "plain-openfile-fifo":
		ours, errText, nonblock = measure(func() (*os.File, error) {
			return os.OpenFile(fifo, os.O_RDONLY|unix.O_NONBLOCK, 0)
		})
	case "plain-openfile-regular":
		ours, errText, nonblock = measure(func() (*os.File, error) {
			return os.OpenFile(regular, os.O_RDONLY|unix.O_NONBLOCK, 0)
		})
	default:
		fmt.Fprintf(os.Stderr, "netpoll child: unknown role %q\n", role)
		return 2
	}
	extra := newDescriptors(before, openDescriptors(bound), ours)
	fmt.Printf("NETPOLL ROLE=%s BASELINE=%d OURS=%d ERR=%s NONBLOCK=%d EXTRA=%s\n",
		role, len(before), ours, errText, nonblock, joinDescriptors(extra))
	_ = os.RemoveAll(dir)
	return 0
}

// measure performs one open through the supplied production call, reads the
// flags the handle actually has, closes it, and reports what to print.
func measure(open func() (*os.File, error)) (ours int, errText string, nonblock int) {
	file, err := open()
	if err != nil {
		return -1, err.Error(), -1
	}
	fdNumber := int(file.Fd())
	flags, flagErr := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	outcome := "none"
	state := -1
	if flagErr != nil {
		outcome = "fcntl:" + flagErr.Error()
	} else {
		state = boolToInt(flags&unix.O_NONBLOCK != 0)
	}
	_ = file.Close()
	return fdNumber, outcome, state
}

// writeRegularFileWithSyscalls creates one fixture without any *os.File,
// because os.WriteFile goes through os.OpenFile and would attach the
// network poller before the measurement.
func writeRegularFileWithSyscalls(path string, content []byte) error {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := unix.Write(fd, content)
	closeErr := unix.Close(fd)
	if writeErr != nil {
		return writeErr
	}
	if written != len(content) {
		return fmt.Errorf("wrote %d of %d fixture bytes", written, len(content))
	}
	return closeErr
}

// descriptorScanBound is the exclusive upper bound of the descriptor-table
// scan: the process' own soft limit, capped so a generous RLIMIT_NOFILE
// cannot turn the observation into a long scan. The poller's descriptors
// are allocated low, and the scan reports only the delta, so the cap
// cannot hide a descriptor this test produced.
func descriptorScanBound() int {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil || limit.Cur == 0 {
		return 1024
	}
	bound := int(limit.Cur)
	if bound > 8192 {
		bound = 8192
	}
	return bound
}

// openDescriptors snapshots the descriptor table with fcntl(F_GETFD), which
// neither allocates a descriptor nor touches the runtime poller.
func openDescriptors(bound int) map[int]bool {
	open := map[int]bool{}
	for fd := 0; fd < bound; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			open[fd] = true
		}
	}
	return open
}

// newDescriptors returns the descriptors that appeared between two
// snapshots, excluding the handle the case opened itself.
func newDescriptors(before, after map[int]bool, own int) []int {
	var extra []int
	for fd := range after {
		if before[fd] || fd == own {
			continue
		}
		extra = append(extra, fd)
	}
	sort.Ints(extra)
	return extra
}

func joinDescriptors(descriptors []int) string {
	parts := make([]string, 0, len(descriptors))
	for _, fd := range descriptors {
		parts = append(parts, strconv.Itoa(fd))
	}
	return strings.Join(parts, ",")
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
