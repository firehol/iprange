//go:build unix

package legacy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
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
	paths := strings.Split(os.Getenv("IPRANGE_GO_LEGACY_POLLER_FREE_ARGS"),
		string(os.PathListSeparator))
	if len(paths) != 3 {
		fmt.Println("LEGACY_POLLER_FREE_ERR=bad-args")
		os.Exit(2)
	}
	entry, list, batch := paths[0], paths[1], paths[2]
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
	fmt.Println("LEGACY_POLLER_FREE_OK")
	os.Exit(0)
}
