//go:build unix

package calleropen

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Design section 5.3 puts every wait that can run inside a session under this
// package's owner. A runtime timer arm initializes the network poller
// (runtime/time.go:455-461: (*timers).addHeap calls netpollGenericInit), and
// the poller's two descriptors are created by calls with no failure path
// (runtime/netpoll_epoll.go:26,:31), so a session that sleeps under a low
// RLIMIT_NOFILE dies with "fatal error: runtime: eventfd failed" instead of
// answering — after the response was already written in the worst case, which
// is why the force-exit waits are on this list too.
//
// The case runs both owners in a re-exec'd child whose soft and hard limit
// leave one descriptor short of what the poller needs. The nanosleep owner
// completes and the child exits 0; a regression to time.Sleep or time.After
// is observed as the fatal runtime abort, the same detection design section 10
// uses against the product binaries.

const waitPollerFreeEnv = "IPRANGE_GO_CALLEROPEN_WAIT_POLLER_FREE_CHILD"

// waitPollerFreeLimit leaves one number to a process holding only the three
// standard streams. The poller claims two (epoll_create1 then eventfd), so an
// owner that provokes it dies on the second allocation; one free slot is
// still enough for the owners themselves, which take no descriptor at all.
// Two free slots would let the poller form and hide the regression.
const waitPollerFreeLimit = 4

func TestTimerFreeWaitsRegisterNoPoller(t *testing.T) {
	if os.Getenv(waitPollerFreeEnv) == "1" {
		runWaitPollerFreeChild()
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
	if !childDescriptorsAreClassifiable() {
		t.Skip("this platform exposes no /proc/self/fd, so the child cannot " +
			"establish its own descriptor baseline; design section 13.5 records " +
			"it as unmeasured by this wave")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run", "TestTimerFreeWaitsRegisterNoPoller")
	cmd.Env = append(os.Environ(), waitPollerFreeEnv+"=1")
	out, execErr := cmd.CombinedOutput()
	text := string(out)
	if strings.Contains(text, "fatal error:") || strings.Contains(text, "runtime:") {
		t.Fatalf("a product wait initialized the runtime network poller; the child "+
			"died under RLIMIT_NOFILE %d instead of finishing its wait:\n%s",
			waitPollerFreeLimit, text)
	}
	if execErr != nil {
		t.Fatalf("the poller-free wait child failed (%v):\n%s", execErr, text)
	}
	if !strings.Contains(text, "WAIT_POLLER_FREE_OK") {
		t.Fatalf("the child never reported a completed wait:\n%s", text)
	}
	// The child caps its table at waitPollerFreeLimit, so it must also be the
	// process that decided what that table contains: a child that skipped the
	// sweep could pass only because nothing it needed happened to be left.
	if !strings.Contains(text, "WAIT_POLLER_FREE_STRAY=") {
		t.Fatalf("the child never reported its descriptor baseline, so it measured a "+
			"table it did not establish:\n%s", text)
	}
}

func runWaitPollerFreeChild() {
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

	// Claim the table before capping it. Go's Linux exec path performs no
	// descriptor sweep in the child, so a descriptor the battery launcher
	// holds without FD_CLOEXEC (it keeps `exec 3>&1 4>&2` for its whole run)
	// would otherwise arrive here and take two of the four numbers this case
	// deliberately leaves, turning a clean wait into an EMFILE failure that
	// reads like a poller regression. See child_fd_baseline_test.go.
	stray, strayOK := closeInheritedDescriptors()
	if !strayOK {
		fmt.Println("WAIT_POLLER_FREE_ERR=descriptor-baseline:" + stray)
		os.Exit(2)
	}
	var current syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &current); err != nil {
		fmt.Println("WAIT_POLLER_FREE_ERR=getrlimit:" + err.Error())
		os.Exit(2)
	}
	tight := syscall.Rlimit{Cur: waitPollerFreeLimit, Max: waitPollerFreeLimit}
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &tight); err != nil {
		fmt.Println("WAIT_POLLER_FREE_ERR=setrlimit:" + err.Error())
		os.Exit(2)
	}
	// The sleep owner: the durations used here are the ones on the session
	// shutdown paths (a few milliseconds, same order as the force-exit grace
	// and the worker poll interval).
	Sleep(3 * time.Millisecond)
	// The bounded-wait owner, both halves: the predicate winning and the
	// deadline expiring.
	if !WaitUntil(20*time.Millisecond, func() bool { return true }) {
		fmt.Println("WAIT_POLLER_FREE_ERR=predicate-lose")
		os.Exit(1)
	}
	if WaitUntil(2*time.Millisecond, func() bool { return false }) {
		fmt.Println("WAIT_POLLER_FREE_ERR=deadline-miss")
		os.Exit(1)
	}
	fmt.Println("WAIT_POLLER_FREE_STRAY=" + stray)
	fmt.Println("WAIT_POLLER_FREE_OK")
	os.Exit(0)
}
