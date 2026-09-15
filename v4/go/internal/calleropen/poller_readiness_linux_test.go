//go:build linux

package calleropen

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// The poller-readiness decision (design section 6) is a claim about the whole
// process, so each case is measured in a fresh process: the call that first
// registers a descriptor or arms a timer is the one that creates the runtime
// network poller, and in a long-lived test binary some unrelated earlier path
// could have done it already. Each child installs its own RLIMIT_NOFILE and
// claims its own held descriptors with raw syscalls before the measured call,
// so the descriptor table the decision reads is exactly the one the case
// describes.
//
// The pins, and the regression each one detects:
//
//   - taking the decision allocates no poller. A per-process eager creation
//     would charge every operation for two permanent descriptors it never
//     asked for, which is design section 10's poller-free promise for every
//     arm except the resolver arm, and would lift every arm's minimum
//     descriptor band by two;
//   - the decision is ready exactly when free = soft - in_use >= 4, so the
//     gate boundary cannot drift without a failing case;
//   - an authorized ResolverAllowed leaves exactly the one poller the owner
//     created deliberately (on Linux: one anon_inode:[eventpoll] plus one
//     anon_inode:[eventfd]). A partial poller, or one that only appeared by
//     accident inside net, is the state the runtime cannot survive;
//   - a refused ResolverAllowed leaves no poller at all, which is the only
//     state in which the process provably cannot reach the unfailable
//     eventfd creation;
//   - the descriptor-table read itself registers nothing, and its two arms
//     (the kernel table walk and the fcntl scan) agree with each other and
//     with the occupancy the case established.

// readinessChildEnv marks the re-exec'd child and carries its spec,
// "role:softLimit:heldDescriptors".
const readinessChildEnv = "IPRANGE_GO_CALLEROPEN_READINESS_CHILD"

// maybeServeReadinessChild serves the child role before any test runs. The
// case is Linux-only: it identifies the poller by the anon_inode link names
// the kernel exposes under /proc, which the kqueue platforms do not have
// (design section 13.5 records those platforms as unmeasured here).
func maybeServeReadinessChild() (int, bool) {
	spec := os.Getenv(readinessChildEnv)
	if spec == "" {
		return 0, false
	}
	return runReadinessChildProcess(spec), true
}

// readinessCase is one child scenario.
type readinessCase struct {
	role      string
	softLimit int
	held      int
}

var readinessCases = []readinessCase{
	// free = softLimit - (3 stdio + held); pollerDemandFree is the boundary.
	{role: "decision", softLimit: 6, held: 0},  // free 3 -> refused
	{role: "decision", softLimit: 7, held: 0},  // free 4 -> ready
	{role: "decision", softLimit: 12, held: 5}, // free 4 -> ready
	{role: "decision", softLimit: 12, held: 6}, // free 3 -> refused
	{role: "resolver-allowed", softLimit: 4096, held: 0},
	{role: "resolver-refused", softLimit: 6, held: 0},
	{role: "table-read", softLimit: 4096, held: 4},
	// The decision is taken once, in the session entry, and the resolver runs
	// later, after the handler's own opens. This case establishes the
	// decision with a generous table and then occupies it, so the call that
	// would create the poller is judged against the table it actually faces.
	{role: "late-exhaustion", softLimit: 12, held: 0},
	// Section 6 makes the decision one per process and explicitly not a
	// per-request gate: an "unavailable" recorded while the table was tight
	// must keep governing after the table becomes generous again. The child
	// occupies the table, takes the decision, then gives every slot back, so
	// the call below is judged against a table that would permit it.
	{role: "decision-sticky", softLimit: 4096, held: 0},
}

// TestPollerReadinessDecision pins the decision, its boundary, and the
// poller-free promise of everything that is not the resolver.
func TestPollerReadinessDecision(t *testing.T) {
	// The boundary is asserted against the literal the design records, not
	// against the constant under test: a case that derived its expectation
	// from pollerDemandFree would stay green while the gate drifted down to
	// zero free slots, which is the state where the poller's allocation
	// aborts the process. 2 (epoll fd + eventfd) + 1 (the resolver's own
	// datagram socket) + 1 margin.
	if pollerDemandFree != 4 {
		t.Fatalf("pollerDemandFree = %d, want the 4 measured slots (poller pair plus the "+
			"resolver's socket plus one); the cases below are calibrated to that number",
			pollerDemandFree)
	}
	for _, tc := range readinessCases {
		t.Run(fmt.Sprintf("%s/%d/%d", tc.role, tc.softLimit, tc.held), func(t *testing.T) {
			report := runReadinessCase(t, tc)
			if report.baselinePoller != 0 {
				t.Fatalf("%s: the process already held %d poller descriptors before the "+
					"measured call, so this case would pass without judging anything (raw %q)",
					tc.role, report.baselinePoller, report.raw)
			}
			switch tc.role {
			case "decision":
				wantReady := tc.softLimit-(3+tc.held) >= 4
				if !report.decided {
					t.Fatalf("the decision was never taken (raw %q)", report.raw)
				}
				if report.ready != wantReady {
					t.Fatalf("soft %d with %d held: decision ready = %v, want %v (raw %q)",
						tc.softLimit, tc.held, report.ready, wantReady, report.raw)
				}
				if !report.freeOK {
					t.Fatalf("FreeDescriptors refused to read the table (raw %q)", report.raw)
				}
				if report.free != tc.softLimit-(3+tc.held) {
					t.Fatalf("free descriptors = %d, want %d for soft %d with %d held (raw %q)",
						report.free, tc.softLimit-(3+tc.held), tc.softLimit, tc.held, report.raw)
				}
				// Section 10: taking the decision must not buy the poller.
				if report.poller != 0 {
					t.Fatalf("taking the decision created the network poller (%d descriptors): "+
						"they are permanent, so every arm would pay for a poller it never asked "+
						"for (raw %q)", report.poller, report.raw)
				}
			case "resolver-allowed":
				if !report.allowed {
					t.Fatalf("ResolverAllowed refused a process with %d free descriptors (raw %q)",
						report.free, report.raw)
				}
				if report.poller != 2 {
					t.Fatalf("an authorized resolver left %d poller descriptors, want the one pair "+
						"the owner creates deliberately (raw %q)", report.poller, report.raw)
				}
				if runtime.GOOS == "linux" && (report.eventpoll != 1 || report.eventfd != 1) {
					t.Fatalf("an authorized resolver left (%d eventpoll, %d eventfd), want (1, 1); a "+
						"partial poller is the state the runtime cannot survive (raw %q)",
						report.eventpoll, report.eventfd, report.raw)
				}
			case "resolver-refused":
				if report.allowed {
					t.Fatalf("ResolverAllowed permitted net for a process with %d free descriptors; "+
						"entering net there is the unfailable poller creation (raw %q)",
						report.free, report.raw)
				}
				if report.poller != 0 {
					t.Fatalf("a refused resolver still reached the poller (%d descriptors, %d "+
						"eventpoll, %d eventfd) (raw %q)", report.poller, report.eventpoll,
						report.eventfd, report.raw)
				}
			case "late-exhaustion":
				// The immutable decision said ready; the table the resolver
				// meets does not have pollerDemandFree slots left, and the
				// poller's creation has no failure path. Answering the
				// resolver's reference class is the only acceptable result.
				if !report.decided || !report.ready {
					t.Fatalf("the decision on the generous table was %v/%v, want decided+ready (raw %q)",
						report.ready, report.decided, report.raw)
				}
				if report.allowed {
					t.Fatalf("ResolverAllowed let the resolver enter net with %d free "+
						"descriptors after the handler's own opens, although the poller "+
						"needs %d and its creation cannot fail (raw %q)",
						report.free, pollerDemandFree, report.raw)
				}
				if report.poller != 0 {
					t.Fatalf("the declined resolver left %d poller descriptors behind (raw %q)",
						report.poller, report.raw)
				}
			case "decision-sticky":
				if !report.decided || report.ready {
					t.Fatalf("the decision taken with %d free descriptors must record ready=false; "+
						"the child proved the table was starved before deciding (raw %q)",
						tc.softLimit, report.raw)
				}
				if !report.freeOK || report.free != tc.softLimit-3 {
					t.Fatalf("the child did not give every held descriptor back (free %d, ok %v, "+
						"want %d), so the refusal below would prove nothing (raw %q)",
						report.free, report.freeOK, tc.softLimit-3, report.raw)
				}
				if report.allowed {
					t.Fatalf("ResolverAllowed let the resolver enter net after the recorded decision "+
						"said unavailable, on a table with %d free descriptors; section 6 makes the "+
						"decision one per process, not a per-request gate (raw %q)",
						report.free, report.raw)
				}
				if report.poller != 0 {
					t.Fatalf("the resolver that the recorded decision declined still reached the "+
						"poller (%d descriptors) (raw %q)", report.poller, report.raw)
				}
			case "table-read":
				if report.poller != 0 {
					t.Fatalf("the descriptor-table read registered the network poller (%d "+
						"descriptors): taking the decision may never be the thing that needs "+
						"the poller (raw %q)", report.poller, report.raw)
				}
				if report.walkCount != 3+tc.held {
					t.Fatalf("the kernel table walk counted %d descriptors, want %d (raw %q)",
						report.walkCount, 3+tc.held, report.raw)
				}
				if report.scanFree != report.free {
					t.Fatalf("the fcntl scan says %d free and the kernel table walk says %d; the "+
						"two arms of one owner must not disagree (raw %q)",
						report.scanFree, report.free, report.raw)
				}
			default:
				t.Fatalf("unknown role %q", tc.role)
			}
		})
	}
}

// runReadinessCase re-executes this test binary in the child role.
func runReadinessCase(t *testing.T, tc readinessCase) readinessReport {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestPollerReadinessDecision")
	command.Env = append(os.Environ(), fmt.Sprintf("%s=%s:%d:%d",
		readinessChildEnv, tc.role, tc.softLimit, tc.held))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %d/%d: child exited with %v\n%s", tc.role, tc.softLimit, tc.held, err, output)
	}
	report, parseErr := parseReadinessReport(string(output))
	if parseErr != nil {
		t.Fatalf("%s %d/%d: unusable child report: %v\n%s", tc.role, tc.softLimit, tc.held, parseErr, output)
	}
	if report.role != tc.role {
		t.Fatalf("%s %d/%d: child served role %q\n%s", tc.role, tc.softLimit, tc.held, report.role, output)
	}
	if report.err != "none" {
		t.Fatalf("%s %d/%d: child setup failed: %s (raw %q)", tc.role, tc.softLimit, tc.held, report.err, report.raw)
	}
	return report
}

// readinessReport is one child's observation of its own descriptor table.
type readinessReport struct {
	role           string
	free           int
	scanFree       int
	walkCount      int
	freeOK         bool
	decided        bool
	ready          bool
	allowed        bool
	poller         int
	eventpoll      int
	eventfd        int
	baselinePoller int
	err            string
	raw            string
}

func parseReadinessReport(text string) (readinessReport, error) {
	var report readinessReport
	report.free, report.scanFree, report.walkCount = -1, -1, -1
	report.poller, report.eventpoll, report.eventfd = -1, -1, -1
	report.err = "none"
	report.raw = text
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "READINESS ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			name, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch name {
			case "READINESS":
			case "ROLE":
				report.role = value
			case "FREE":
				report.free = readinessInt(value)
			case "FREEOK":
				report.freeOK = value == "1"
			case "SCANFREE":
				report.scanFree = readinessInt(value)
			case "WALKCOUNT":
				report.walkCount = readinessInt(value)
			case "DECIDED":
				report.decided = value == "1"
			case "READY":
				report.ready = value == "1"
			case "ALLOWED":
				report.allowed = value == "1"
			case "POLLER":
				report.poller = readinessInt(value)
			case "EVENTPOLL":
				report.eventpoll = readinessInt(value)
			case "EVENTFD":
				report.eventfd = readinessInt(value)
			case "BASELINE":
				report.baselinePoller = readinessInt(value)
			case "ERR":
				report.err = value
			}
		}
		return report, nil
	}
	return report, fmt.Errorf("no READINESS line in %q", text)
}

func readinessInt(text string) int {
	value, _ := strconv.Atoi(text)
	return value
}

// runReadinessChildProcess serves one case in a fresh process: it installs the
// limit, claims the held descriptors with raw syscalls (an os.Open here would
// register the poller and destroy the state the case measures), performs the
// one measured call, and prints the table observation.
func runReadinessChildProcess(spec string) int {
	parts := strings.Split(spec, ":")
	if len(parts) != 3 {
		fmt.Fprintf(os.Stderr, "readiness child: malformed spec %q\n", spec)
		return 2
	}
	role := parts[0]
	softLimit, err := strconv.Atoi(parts[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "readiness child: limit %q: %v\n", parts[1], err)
		return 2
	}
	held, err := strconv.Atoi(parts[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "readiness child: held %q: %v\n", parts[2], err)
		return 2
	}

	fail := func(reason string) int {
		fmt.Printf("READINESS ROLE=%s FREE=-1 SCANFREE=-1 WALKCOUNT=-1 FREEOK=0 DECIDED=0 READY=0 "+
			"ALLOWED=0 POLLER=-1 EVENTPOLL=-1 EVENTFD=-1 BASELINE=-1 ERR=%s\n", role, reason)
		return 0
	}

	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{
		Cur: uint64(softLimit), Max: uint64(softLimit),
	}); err != nil {
		return fail("setrlimit:" + err.Error())
	}
	// The hold target is created with raw syscalls for the same reason the
	// holds themselves are raw: os.CreateTemp goes through os.OpenFile and
	// would register the descriptor with the poller before the measurement.
	target := fmt.Sprintf("%s/iprange-readiness-hold-%d", os.TempDir(), os.Getpid())
	if err := writeRegularFileWithSyscalls(target, []byte("hold\n")); err != nil {
		return fail("fixture:" + err.Error())
	}
	for claimed := 0; claimed < held; claimed++ {
		if _, err := unix.Open(target, unix.O_RDONLY, 0); err != nil {
			_ = os.Remove(target)
			return fail("hold:" + err.Error())
		}
	}
	_ = os.Remove(target)

	_, baselinePoller, _, _, ok := readDescriptorTable()
	if !ok {
		return fail("baseline-table")
	}

	var (
		free       = -1
		scanFree   = -1
		walkCount  = 0
		freeOK     bool
		decided    bool
		ready      bool
		allowed    bool
		errMessage = "none"
	)
	switch role {
	case "decision":
		InitPollerReadiness()
		free, freeOK = FreeDescriptors()
		scanFree = freeDescriptorsByScan(uint64(softLimit))
		walkCount, _, _, _, ok = readDescriptorTable()
		if !ok {
			return fail("walk")
		}
		ready, decided = PollerReadiness()
	case "resolver-allowed", "resolver-refused":
		InitPollerReadiness()
		free, freeOK = FreeDescriptors()
		allowed = ResolverAllowed()
	case "late-exhaustion":
		InitPollerReadiness()
		ready, decided = PollerReadiness()
		// Occupy the table the way a handler does: descriptors claimed after
		// the decision, held for the duration of the request.
		target := fmt.Sprintf("%s/iprange-readiness-late-%d", os.TempDir(), os.Getpid())
		if err := writeRegularFileWithSyscalls(target, []byte("late\n")); err != nil {
			return fail("late-fixture:" + err.Error())
		}
		claimed := 0
		for ; claimed < 12-(3+held)-pollerDemandFree+1; claimed++ {
			if _, err := unix.Open(target, unix.O_RDONLY, 0); err != nil {
				break
			}
		}
		_ = os.Remove(target)
		free, freeOK = FreeDescriptors()
		allowed = ResolverAllowed()
	case "decision-sticky":
		// Occupy the table the way a starved process does, take the decision
		// there, then release every descriptor. The release is what makes this
		// case a discriminator: without it the gate's own table re-read would
		// refuse for the same reason the decision did, and a resolver that
		// ignored the recorded decision would look correct.
		wantClaimed := softLimit - 3 - (pollerDemandFree - 1)
		target := fmt.Sprintf("%s/iprange-readiness-sticky-%d", os.TempDir(), os.Getpid())
		if err := writeRegularFileWithSyscalls(target, []byte("sticky\n")); err != nil {
			return fail("sticky-fixture:" + err.Error())
		}
		claimed := make([]int, 0, wantClaimed)
		for len(claimed) < wantClaimed {
			fd, err := unix.Open(target, unix.O_RDONLY, 0)
			if err != nil {
				break
			}
			claimed = append(claimed, fd)
		}
		_ = os.Remove(target)
		if len(claimed) != wantClaimed {
			return fail("sticky-occupy")
		}
		InitPollerReadiness()
		ready, decided = PollerReadiness()
		for _, fd := range claimed {
			unix.Close(fd)
		}
		free, freeOK = FreeDescriptors()
		allowed = ResolverAllowed()
	case "table-read":
		free, freeOK = FreeDescriptors()
		scanFree = freeDescriptorsByScan(uint64(softLimit))
		walkCount, _, _, _, ok = readDescriptorTable()
		if !ok {
			return fail("walk")
		}
	default:
		errMessage = "unknown-role:" + role
	}

	_, poller, eventpoll, eventfd, ok := readDescriptorTable()
	if !ok {
		return fail("final-table")
	}
	fmt.Printf("READINESS ROLE=%s FREE=%d SCANFREE=%d WALKCOUNT=%d FREEOK=%d DECIDED=%d READY=%d "+
		"ALLOWED=%d POLLER=%d EVENTPOLL=%d EVENTFD=%d BASELINE=%d ERR=%s\n",
		role, free, scanFree, walkCount, boolToInt(freeOK), boolToInt(decided), boolToInt(ready),
		boolToInt(allowed), poller-baselinePoller, eventpoll, eventfd, baselinePoller, errMessage)
	return 0
}

// readDescriptorTable counts this process's descriptors, and the runtime
// poller among them, through the kernel's own table view using raw syscalls
// only. The os package is not an option here: os.ReadDir on /proc/self/fd is
// itself a pollable registration, so an os-side observation would report the
// poller for having observed it. Returns ok=false when the table cannot be
// read at all.
//
// The counts are (descriptor entries, poller descriptors, eventpoll, eventfd);
// entries excludes "." and "..", which some kernels list.
func readDescriptorTable() (entries, poller, eventpoll, eventfd int, ok bool) {
	dirFD, err := unix.Openat(unix.AT_FDCWD, "/proc/self/fd",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return 0, 0, 0, 0, false
	}
	defer unix.Close(dirFD)
	// The read's own descriptor is released when it returns and is not part
	// of the table being counted (mirrors procSelfFdCount).
	selfName := strconv.Itoa(dirFD)
	var buffer [1024]byte
	for calls := 0; calls <= maxScanDescriptors+1; calls++ {
		n, getErr := unix.Getdents(dirFD, buffer[:])
		if errors.Is(getErr, unix.EINTR) {
			continue
		}
		if getErr != nil || n < 0 {
			return 0, 0, 0, 0, false
		}
		if n == 0 {
			return entries, poller, eventpoll, eventfd, true
		}
		for offset := 0; offset < n; {
			if offset+dirent64HeaderSize > n {
				return 0, 0, 0, 0, false
			}
			reclen := int(uint16(buffer[offset+dirent64ReclenOffset]) |
				uint16(buffer[offset+dirent64ReclenOffset+1])<<8)
			if reclen < dirent64HeaderSize || offset+reclen > n {
				return 0, 0, 0, 0, false
			}
			name := buffer[offset+dirent64HeaderSize : offset+reclen]
			if end := bytes.IndexByte(name, 0); end >= 0 {
				name = name[:end]
			}
			if !isDotEntry(name) && string(name) != selfName {
				entries++
				target := make([]byte, 256)
				length, linkErr := unix.Readlinkat(dirFD, string(name), target)
				if linkErr != nil {
					return 0, 0, 0, 0, false
				}
				switch string(target[:length]) {
				case "anon_inode:[eventpoll]":
					eventpoll++
					poller++
				case "anon_inode:[eventfd]":
					eventfd++
					poller++
				}
			}
			offset += reclen
		}
	}
	return 0, 0, 0, 0, false
}
