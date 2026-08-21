//go:build v4work

// Child-process crash-consistency tests for the four FreeBSD no-replace
// crash points (Rust namespace_mutation.rs link_noreplace:
// after_noreplace_link, after_noreplace_link_sync,
// after_noreplace_alias_unlink, after_noreplace_alias_sync). The real
// machine runs in a spawned child of this test binary; internal/fault
// makes the child exit 86 at the exact physical step, and the parent
// then recovers the transition from the crash state and proves the
// destination-only final state. Production builds compile the crash
// points to no-ops, so this suite is v4work-only.

package mapping

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	linkCrashChildTest    = "^TestLinkNoReplaceCrashChild$"
	linkCrashChildSpawned = "IPRANGE_V4_TEST_SPAWNED"
	linkCrashChildAction  = "IPRANGE_V4_TEST_ACTION"
	linkCrashChildSource  = "IPRANGE_V4_TEST_SOURCE"
	linkCrashChildDest    = "IPRANGE_V4_TEST_DESTINATION"
	linkCrashChildDevice  = "IPRANGE_V4_TEST_DEVICE"
	linkCrashChildInode   = "IPRANGE_V4_TEST_INODE"
	linkCrashChildTimeout = 60 * time.Second
)

// TestLinkNoReplaceCrashRecovery crashes the machine at each of the
// four physical steps and recovers: the two early crash states (both
// names, two links each) finish through the Linked transition; the two
// late states (destination only) prove as Complete. Every crash state
// ends with exactly one name for the output inode.
func TestLinkNoReplaceCrashRecovery(t *testing.T) {
	for _, point := range []string{
		"publication.freebsd.after_noreplace_link",
		"publication.freebsd.after_noreplace_link_sync",
		"publication.freebsd.after_noreplace_alias_unlink",
		"publication.freebsd.after_noreplace_alias_sync",
	} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			source, device, inode := linkAttempt(t, dir, "attempt")
			destination := filepath.Join(dir, "out.iprdb")
			runLinkCrashChild(t, dir, source, destination, device, inode, point)

			// The crash state: the two link/sync points leave both
			// names; the two unlink/sync points leave the destination
			// only. Recover through the transition either way.
			if err := finishNoReplaceTransition(dir, source, destination, device, inode); err != nil {
				t.Fatalf("recovery at %s: %v", point, err)
			}
			if exists, _ := linkProbe(t, source); exists {
				t.Fatalf("source alias survived recovery at %s", point)
			}
			entry, err := entryIdentity(destination)
			if err != nil {
				t.Fatalf("destination after recovery at %s: %v", point, err)
			}
			if !entry.regular || entry.nlink != 1 || entry.device != device || entry.inode != inode {
				t.Fatalf("destination after recovery at %s = %+v, want the output inode with one link", point, entry)
			}
		})
	}
}

// runLinkCrashChild spawns this test binary as the action's child and
// requires it to die with exit code 86 (Rust run_child).
func runLinkCrashChild(t *testing.T, dir, source, destination string, device, inode uint64, crashPoint string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), linkCrashChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+linkCrashChildTest)
	env := make([]string, 0, len(os.Environ())+7)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		linkCrashChildSpawned+"=1",
		linkCrashChildAction+"=link",
		linkCrashChildSource+"="+source,
		linkCrashChildDest+"="+destination,
		linkCrashChildDevice+"="+strconv.FormatUint(device, 10),
		linkCrashChildInode+"="+strconv.FormatUint(inode, 10),
		"IPRANGE_V4_TEST_CRASH_AT="+crashPoint,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("crash child at %s timed out", crashPoint)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 86 {
			return
		}
		t.Fatalf("crash child at %s exited %d, want 86", crashPoint, exitErr.ExitCode())
	}
	if err != nil {
		t.Fatalf("crash child at %s: %v", crashPoint, err)
	}
	t.Fatalf("crash child at %s exited 0: the crash point was not reached", crashPoint)
}

// TestLinkNoReplaceCrashChild is the subprocess entry point: it runs the
// machine once with the crash variable armed and dies at the named
// physical step.
func TestLinkNoReplaceCrashChild(t *testing.T) {
	if os.Getenv(linkCrashChildSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	time.AfterFunc(linkCrashChildTimeout, func() { os.Exit(1) })
	source := os.Getenv(linkCrashChildSource)
	destination := os.Getenv(linkCrashChildDest)
	device, err := strconv.ParseUint(os.Getenv(linkCrashChildDevice), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	inode, err := strconv.ParseUint(os.Getenv(linkCrashChildInode), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	_ = st
	if err := linkNoReplace(filepath.Dir(destination), source, destination, device, inode); err != nil {
		t.Fatal(err)
	}
	t.Fatal("machine completed without reaching the crash point")
}
