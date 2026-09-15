//go:build unix

package legacy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	// The sweep below reads each descriptor's kernel link target. This file is
	// constrained to unix, which is what the package's unix-only rule requires
	// of a test that reaches for golang.org/x/sys/unix.
	"golang.org/x/sys/unix"
)

// The four one-shot inputs of design section 5.2 (a plain file argument, a
// directory entry, the file list behind an @ target, and each file named by
// that list) must be read through the caller-open owner. Reading them with
// os.ReadFile goes through os.OpenFile, which on Linux registers the
// descriptor with the runtime network poller even for a regular file
// (os/file_unix.go:155,:219); the poller's two descriptors are created by
// epoll_create1 and eventfd and neither call has a failure path
// (runtime/netpoll_epoll.go:26,:31), so a process under a low RLIMIT_NOFILE
// dies with "fatal error: runtime: eventfd failed" instead of answering.
//
// The case therefore runs the four reads in a re-exec'd child whose soft and
// hard limit leave one descriptor short of what the poller needs. The honest
// build completes its reads and exits 0; a regression to a pollable open is
// observed as the fatal runtime abort, which is exactly how design section
// 10 detects the same defect in the pressured product processes.

const legacyPollerFreeEnv = "IPRANGE_GO_LEGACY_POLLER_FREE_CHILD"

// legacyPollerFreeLimit is the RLIMIT_NOFILE the child runs under. The child
// holds only the three standard streams when it lowers the limit, so two
// numbers remain: enough for a single read at a time, not enough for the
// poller's eventpoll plus eventfd.
const legacyPollerFreeLimit = 5

func TestLegacyOneShotReadsRegisterNoPoller(t *testing.T) {
	if os.Getenv(legacyPollerFreeEnv) == "1" {
		runLegacyPollerFreeChild()
		return // unreachable: the child exits itself
	}
	// The child establishes its own descriptor table by reading each open
	// descriptor's kernel link target, and only Linux exposes /proc/self/fd to
	// read it from. Where the target is unavailable the child cannot tell
	// "the product registered the poller" from "the launcher left a descriptor
	// open", so the premise of the measurement is missing and its failure would
	// be a host limitation reported as a product regression: skip on the stated
	// reason instead. The question is asked of the kernel at runtime rather
	// than read off a build tag, because a Linux host with /proc unmounted has
	// the same problem the tag would only describe. Design section 13.5
	// records the kqueue platforms as unmeasured by this wave.
	if !legacyDescriptorsAreClassifiable() {
		t.Skip("this platform exposes no /proc/self/fd, so the child cannot " +
			"establish its own descriptor baseline; design section 13.5 records " +
			"it as unmeasured by this wave")
	}
	dir := t.TempDir()
	entries := filepath.Join(dir, "entries")
	if err := os.WriteFile(entries, []byte("192.0.2.1\n192.0.2.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "list")
	if err := os.WriteFile(list, []byte(entries+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	batch := filepath.Join(dir, "batch")
	if err := os.MkdirAll(batch, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"one": "198.51.100.0/24\n",
		"two": "203.0.113.7\n",
	} {
		if err := os.WriteFile(filepath.Join(batch, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run", "TestLegacyOneShotReadsRegisterNoPoller")
	cmd.Env = append(os.Environ(), legacyPollerFreeEnv+"=1")
	cmd.Env = append(cmd.Env, "IPRANGE_GO_LEGACY_POLLER_FREE_ARGS="+
		strings.Join([]string{entries, list, batch}, string(os.PathListSeparator)))
	out, execErr := cmd.CombinedOutput()
	text := string(out)
	if strings.Contains(text, "fatal error:") || strings.Contains(text, "runtime:") {
		t.Fatalf("the legacy one-shot reads registered the runtime network poller; "+
			"the child died under RLIMIT_NOFILE %d instead of answering:\n%s",
			legacyPollerFreeLimit, text)
	}
	if execErr != nil {
		t.Fatalf("the poller-free legacy child failed (%v):\n%s", execErr, text)
	}
	if !strings.Contains(text, "LEGACY_POLLER_FREE_OK") {
		t.Fatalf("the child never reported a completed read set:\n%s", text)
	}
	// The child caps its table at legacyPollerFreeLimit and then performs four
	// real reads, so two descriptors it never claimed are enough to make the
	// first read answer EMFILE. Requiring the baseline report is what keeps
	// "the child freed its table" from becoming an assumption rather than an
	// observation.
	if !strings.Contains(text, "LEGACY_POLLER_FREE_STRAY=") {
		t.Fatalf("the child never reported its descriptor baseline, so it read against a "+
			"table it did not establish:\n%s", text)
	}
}

// TestLegacyOneShotReadsUseTheOwner is the structural half of the same pin.
// The behavioural case above detects a pollable open through the fatal abort
// it provokes, which requires the platform to be out of descriptors; this one
// names the invariant directly, so a regression is reported as the routing
// mistake it is rather than as a resource failure.
func TestLegacyOneShotReadsUseTheOwner(t *testing.T) {
	source, err := os.ReadFile("parse.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(source), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(trimmed, "os.ReadFile(") {
			t.Fatalf("parse.go reads a one-shot input through os.ReadFile, which "+
				"registers the descriptor with the runtime network poller; route it "+
				"through readWholeFile instead: %q", trimmed)
		}
	}
	if got := strings.Count(string(source), "readWholeFile("); got < 4 {
		t.Fatalf("parse.go calls the caller-open owner %d times, want at least the 4 "+
			"one-shot inputs of design section 5.2", got)
	}
}

func runLegacyPollerFreeChild() {
	// Coverage is the parent's business, not the child's: the child exists to
	// measure one thing and exit. A child that inherits GOCOVERDIR opens its own
	// counter file at exit, that open goes through os.OpenFile, and on Linux
	// os.newFile arms the runtime network poller (os/file_unix.go:219 ->
	// internal/poll.(*FD).Init -> netpollGenericInit). Under the descriptor table
	// this case owns, that registration is the allocation with no failure path,
	// and the resulting netpollinit abort reads as a product poller regression
	// caused by the harness. Clearing it here keeps emission off; only the
	// parent's own run contributes coverage data. Must stay the first statement,
	// before any work that could take a descriptor.
	os.Unsetenv("GOCOVERDIR")

	paths := strings.Split(os.Getenv("IPRANGE_GO_LEGACY_POLLER_FREE_ARGS"),
		string(os.PathListSeparator))
	if len(paths) != 3 {
		fmt.Println("LEGACY_POLLER_FREE_ERR=bad-args")
		os.Exit(2)
	}
	entry, list, batch := paths[0], paths[1], paths[2]
	stray, strayOK := closeInheritedDescriptors()
	if !strayOK {
		fmt.Println("LEGACY_POLLER_FREE_ERR=descriptor-baseline:" + stray)
		os.Exit(2)
	}
	var zero syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &zero); err != nil {
		fmt.Println("LEGACY_POLLER_FREE_ERR=getrlimit:" + err.Error())
		os.Exit(2)
	}
	tight := syscall.Rlimit{Cur: legacyPollerFreeLimit, Max: legacyPollerFreeLimit}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &tight); err != nil {
		// The measurement is meaningless without the bound; report rather
		// than pass on a table the child never constrained.
		fmt.Println("LEGACY_POLLER_FREE_ERR=setrlimit:" + err.Error())
		os.Exit(2)
	}
	fail := func(stage string, err error) {
		fmt.Printf("LEGACY_POLLER_FREE_ERR=%s:%v\n", stage, err)
		os.Exit(1)
	}
	// Site 1: a plain file argument, through the loader that owns it.
	options := DefaultOptions()
	options.Sources = []SourceSpec{{Kind: SourcePath, Arg: entry}}
	if _, err := loadAll(options); err != nil {
		fail("load-file", err)
	}
	if _, err := readWholeFile(entry); err != nil {
		fail("read-file", err)
	}
	// Sites 2-4: a directory entry, the file list, and each file the list names.
	o := &Options{Family: V4}
	resolver := NewResolver(1, true, false, V4, false)
	var lastSource string
	var dnsUsed bool
	if _, err := expandAt(o, resolver, batch, &lastSource, &dnsUsed); err != nil {
		fail("expand-directory", err)
	}
	if _, err := expandAt(o, resolver, list, &lastSource, &dnsUsed); err != nil {
		fail("expand-list", err)
	}
	fmt.Println("LEGACY_POLLER_FREE_STRAY=" + stray)
	fmt.Println("LEGACY_POLLER_FREE_OK")
	os.Exit(0)
}

// closeInheritedDescriptors releases every descriptor above the three standard
// streams that this child did not claim, and reports what it released and what
// it deliberately kept. It must run before the child caps RLIMIT_NOFILE, so
// that the two numbers legacyPollerFreeLimit leaves free are the case's own.
//
// Go's Linux exec path installs no descriptor sweep in the child (syscall
// carries no CloseFds arm; the child's table is whatever survived execve), so a
// descriptor an outer launcher holds without FD_CLOEXEC arrives here. The wave
// battery is that launcher: `exec 3>&1 4>&2` keeps its own stdout and stderr
// duplicated for its whole run, and under `go test ./...` those two
// descriptors land in this child, which is why the case passed on its own and
// failed in the battery with "Too many open files" on the first read.
//
// The sweep is narrow on purpose: closing a descriptor the runtime owns would
// turn a host-state problem into a broken process. The standard streams are
// never touched, and a descriptor whose kernel link target names an inode
// family the runtime uses (or that cannot be read at all, because the platform
// exposes no /proc/self/fd) is kept and reported instead of closed; the reads
// below then fail loudly on a table the case did not establish, which is the
// honest verdict.
//
// Discovery uses fcntl(F_GETFD) rather than a directory walk so the sweep takes
// no descriptor of its own. internal/calleropen/child_fd_baseline_test.go owns
// the same sweep for that package's children; it cannot be shared, because an
// unexported test helper cannot cross a package boundary and the alternatives
// would either add a descriptor-destroying call to the product surface or
// change the module's package inventory that the committed coverage reports pin.
// legacyDescriptorsAreClassifiable reports whether this platform lets the
// sweep below identify an open descriptor by its kernel link target. Linux
// exposes /proc/self/fd; darwin uses /dev/fd and netbsd, openbsd and dragonfly
// expose no descriptor link namespace there at all, so a Readlink would fail
// for every number and the sweep could not tell a stray descriptor from a
// poller descriptor. The answer is asked of the kernel rather than read off a
// build tag, because a Linux host with /proc unmounted has the same problem
// the tag would only describe.
func legacyDescriptorsAreClassifiable() bool {
	var buffer [1]byte
	length, err := unix.Readlink("/proc/self/fd/0", buffer[:])
	return err == nil && length > 0
}

func closeInheritedDescriptors() (string, bool) {
	// The scan window is this child's own ceiling, bounded so the sweep is
	// constant work regardless of what the host allows.
	const scanBound = 4096
	window := uint64(scanBound)
	// Rlimit.Max is uint64 on Linux, OpenBSD, NetBSD and Darwin but int64 on
	// FreeBSD and DragonFly, so it is normalized before the comparison; see
	// fd_pressure_limit_bsd_test.go for the same platform split.
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err == nil {
		if hard := uint64(limit.Max); hard > 0 && hard < window {
			window = hard
		}
	}
	var closed, kept []string
	for fd := 3; fd < int(window); fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			continue // not an open descriptor
		}
		number := strconv.Itoa(fd)
		var buffer [256]byte
		length, linkErr := unix.Readlink("/proc/self/fd/"+number, buffer[:])
		switch {
		case linkErr != nil:
			kept = append(kept, number+"=unclassifiable:"+linkErr.Error())
		case strings.HasPrefix(string(buffer[:length]), "anon_inode:"),
			strings.HasPrefix(string(buffer[:length]), "signalfd:"),
			strings.HasPrefix(string(buffer[:length]), "pidfd:"):
			kept = append(kept, number+"=runtime:")
		default:
			if err := unix.Close(fd); err != nil {
				kept = append(kept, number+"=close-failed:"+err.Error())
				continue
			}
			closed = append(closed, number+"="+baseNameOf(string(buffer[:length])))
		}
	}
	if len(closed) == 0 && len(kept) == 0 {
		return "none", true
	}
	return "closed[" + strings.Join(closed, ",") + "] kept[" + strings.Join(kept, ",") + "]", true
}

// baseNameOf shortens one link target enough to recognize it in a report.
func baseNameOf(target string) string {
	if i := strings.LastIndex(target, "/"); i >= 0 {
		return target[i+1:]
	}
	return target
}
