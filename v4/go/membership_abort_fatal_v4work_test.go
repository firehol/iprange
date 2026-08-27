//go:build v4work

// Fatal-class abort branding of the public membership transaction
// (SOW-0027 4c/4d delta review, wire-integrity finding): DeleteFeed
// reaches abortEdit through edit.DeleteCurrentFeedMembership, and the
// edit must pass the raw internal error into the abort machinery so the
// Io/Format class brands the writer unusable (Rust abort_after). A
// publicError wrap at the call site hides the class from isFatalClass
// and leaves the writer falsely healthy. The fault point fires inside
// the store exactly where a malformed draft cell would surface
// (v4work-only; production builds compile it out).

package iprangedb

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	abortChildTest    = "^TestMembershipAbortFatalChild$"
	abortChildSpawned = "IPRANGE_MEMBERSHIP_ABORT_SPAWNED"
	abortChildPath    = "IPRANGE_MEMBERSHIP_ABORT_PATH"
	abortChildTimeout = 60 * time.Second
)

// runAbortFatalChild spawns this test binary with the mid-edit fatal
// fault armed and requires a clean child verdict (strict env strip so
// no ambient control variable redirects the child).
func runAbortFatalChild(t *testing.T, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), abortChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+abortChildTest)
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") || strings.HasPrefix(kv, "IPRANGE_MEMBERSHIP_ABORT_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		abortChildSpawned+"=1",
		abortChildPath+"="+path,
		"IPRANGE_V4_TEST_FAIL_AT=membership.delete_feed_fatal",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("abort child timed out")
		}
		t.Fatalf("abort child failed: %v", err)
	}
}

// TestMembershipAbortFatalBrandsWriter drives the fatal abort contract
// through the real public facade in a child process.
func TestMembershipAbortFatalBrandsWriter(t *testing.T) {
	requireLiveCreation(t)
	path := memberTxDB(t)
	runAbortFatalChild(t, path)
}

// TestMembershipAbortFatalChild is the subprocess entry point; it only
// runs when the parent set the spawn marker.
func TestMembershipAbortFatalChild(t *testing.T) {
	requireLiveCreation(t)
	if os.Getenv(abortChildSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	// Self-deadline so a hang cannot linger after the parent died.
	time.AfterFunc(abortChildTimeout, func() { os.Exit(1) })

	w, err := OpenLiveWriter(os.Getenv(abortChildPath), DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginMembershipTransaction(nil)
	if err != nil {
		t.Fatal(err)
	}
	feed, err := tx.EnsureFeed(feedName(t, "gamma"))
	if err != nil {
		t.Fatal(err)
	}
	// The armed fault fires inside the edit: the op aborts the draft
	// with TransactionAborted wrapping the fatal FormatInvalid cause,
	// and the writer fails closed (Rust abort_after: Io | Format).
	if err := tx.DeleteFeed(feed); err == nil {
		t.Fatal("DeleteFeed succeeded under the armed mid-edit fault")
	} else if abortCauseCode(err) != ErrorFormatInvalid {
		t.Fatalf("DeleteFeed = %v, want transaction aborted wrapping format invalid", err)
	}
	if _, err := w.BeginMembershipTransaction(nil); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("writer stayed healthy after a fatal abort, BeginMembershipTransaction = %v, want WrongState", err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatalf("close after fatal abort: %v", err)
	}
}
