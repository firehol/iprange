//go:build linux

package random

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// Design section 5 puts the entropy draws under getrandom(2) rather than
// crypto/rand. The reason is a descriptor hazard, not a preference: the first
// crypto/rand use on Linux takes a descriptor-backed reader and arms a runtime
// timer, and any timer arm initializes the network poller
// (runtime/time.go:455-461), whose two descriptors
// (runtime/netpoll_epoll.go:21-31) are allocated without a failure path. Under
// a low RLIMIT_NOFILE that turns an identity draw into
// "fatal error: runtime: eventfd failed" — the process dies instead of
// answering, which is the exact failure design section 10 measures.
//
// Two independent pins, one per child process:
//
//   - a draw must consume no descriptor the process still holds after it
//     returns, at a normal limit;
//   - a draw must survive a table one descriptor short of the poller pair,
//     which is where a poller-provoking draw dies rather than fails.

// entropyChildEnv selects the child role ("delta" or "tight").
const entropyChildEnv = "IPRANGE_GO_RANDOM_ENTROPY_CHILD"

func TestEntropyDrawRegistersNoDescriptors(t *testing.T) {
	report, err := runEntropyChild("delta")
	if err != nil {
		t.Fatalf("delta child: %v\n%s", err, report)
	}
	if strings.Contains(report, "fatal error:") || strings.Contains(report, "runtime:") {
		t.Fatalf("the entropy draw initialized the runtime network poller:\n%s", report)
	}
	const marker = "ENTROPY_DELTA="
	index := strings.Index(report, marker)
	if index < 0 {
		t.Fatalf("the delta child never reported its descriptor delta:\n%s", report)
	}
	rest := report[index+len(marker):]
	if end := strings.IndexAny(rest, " \n"); end >= 0 {
		rest = rest[:end]
	}
	delta, parseErr := strconv.Atoi(rest)
	if parseErr != nil {
		t.Fatalf("unusable descriptor delta %q:\n%s", rest, report)
	}
	if delta != 0 {
		t.Fatalf("one entropy draw left %d descriptors held by the process; the draw must "+
			"take none (a crypto/rand reader and the poller pair it provokes are the "+
			"hazard this owner exists to prevent):\n%s", delta, report)
	}
	if !strings.Contains(report, "ENTROPY_DELTA_OK") {
		t.Fatalf("the delta child did not complete its draws:\n%s", report)
	}
}

func TestEntropyDrawSurvivesPollerShortTable(t *testing.T) {
	if os.Getenv(entropyChildEnv) == "tight" {
		runTightEntropyChild()
		return // unreachable: the child exits itself
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run", "TestEntropyDrawSurvivesPollerShortTable")
	cmd.Env = append(os.Environ(), entropyChildEnv+"=tight")
	out, execErr := cmd.CombinedOutput()
	text := string(out)
	if strings.Contains(text, "fatal error:") || strings.Contains(text, "runtime:") {
		t.Fatalf("an entropy draw initialized the runtime network poller and aborted the "+
			"process under RLIMIT_NOFILE %d instead of answering:\n%s", tightEntropyLimit, text)
	}
	if execErr != nil {
		t.Fatalf("the tight entropy child failed (%v):\n%s", execErr, text)
	}
	if !strings.Contains(text, "ENTROPY_TIGHT_OK") {
		t.Fatalf("the tight child never reported a completed draw:\n%s", text)
	}
}

// runEntropyChild runs the "delta" role in a fresh process, so the
// before/after table read brackets only the draws under test.
func runEntropyChild(role string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(self, "-test.run", "TestEntropyDrawRegistersNoDescriptors")
	cmd.Env = append(os.Environ(), entropyChildEnv+"="+role)
	out, cmdErr := cmd.CombinedOutput()
	return string(out), cmdErr
}

func runDeltaEntropyChild() int {
	before, ok := calleropen.FreeDescriptors()
	if !ok {
		fmt.Println("ENTROPY_DELTA=err free-descriptors")
		return 2
	}
	var buffer [32]byte
	if err := Entropy(buffer[:]); err != nil {
		fmt.Println("ENTROPY_DELTA=err entropy:" + err.Error())
		return 1
	}
	if _, err := Nonzero128(); err != nil {
		fmt.Println("ENTROPY_DELTA=err nonzero:" + err.Error())
		return 1
	}
	after, ok := calleropen.FreeDescriptors()
	if !ok {
		fmt.Println("ENTROPY_DELTA=err free-descriptors-after")
		return 2
	}
	fmt.Printf("ENTROPY_DELTA=%d BEFORE=%d AFTER=%d ENTROPY_DELTA_OK\n", after-before, before, after)
	return 0
}

// tightEntropyLimit leaves one number to a process holding only the three
// standard streams. The poller claims two, so a draw that provokes it dies on
// the second allocation; one free slot is still enough for the draws
// themselves, which take none.
const tightEntropyLimit = 4

func runTightEntropyChild() {
	// The coverage runtime initializes the network poller when it opens its
	// counter file at exit. Under the table this child is about to claim, that
	// write dies in netpollinit with EMFILE, and the case would read as a
	// product poller regression caused by the instrumentation. Only the parent's
	// run is measured: a re-exec role child never emits coverage data. This is
	// the first statement so no poller-capable work happens before it.
	os.Unsetenv("GOCOVERDIR")
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{
		Cur: tightEntropyLimit, Max: tightEntropyLimit,
	}); err != nil {
		fmt.Println("ENTROPY_TIGHT_ERR=setrlimit:" + err.Error())
		os.Exit(2)
	}
	var buffer [32]byte
	if err := Entropy(buffer[:]); err != nil {
		fmt.Println("ENTROPY_TIGHT_ERR=entropy:" + err.Error())
		os.Exit(1)
	}
	if buffer == [32]byte{} {
		fmt.Println("ENTROPY_TIGHT_ERR=zero-fill")
		os.Exit(1)
	}
	if _, err := Nonzero128(); err != nil {
		fmt.Println("ENTROPY_TIGHT_ERR=nonzero:" + err.Error())
		os.Exit(1)
	}
	fmt.Println("ENTROPY_TIGHT_OK")
	os.Exit(0)
}

func TestMain(m *testing.M) {
	if role := os.Getenv(entropyChildEnv); role == "delta" {
		os.Exit(runDeltaEntropyChild())
	}
	m.Run()
}
