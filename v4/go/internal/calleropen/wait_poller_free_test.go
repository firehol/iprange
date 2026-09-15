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
}

func runWaitPollerFreeChild() {
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
	fmt.Println("WAIT_POLLER_FREE_OK")
	os.Exit(0)
}
