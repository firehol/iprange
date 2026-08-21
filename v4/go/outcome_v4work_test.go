//go:build v4work

// Outcome-unknown fail-closed gate (SOW-0025 chunk-6, level-1 finding):
// after a publication failure past the alternate meta write, the writer
// must refuse every mutating entry point with WrongState and only Close
// remains legal (Rust State::OutcomeUnknown + require_healthy parity).
// A preparation failure must abort with the TransactionAborted class
// while leaving the writer healthy (Rust abort_after parity). The
// fault.Fail points are v4work-only and compile out of production.

package iprangedb

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	outcomeChildTest    = "^TestOutcomeUnknownChild$"
	outcomeChildSpawned = "IPRANGE_V4_TEST_SPAWNED"
	outcomeChildPath    = "IPRANGE_V4_TEST_PATH"
	outcomeChildAction  = "IPRANGE_V4_TEST_ACTION"
	outcomeChildTimeout = 60 * time.Second
)

// runOutcomeChild spawns this test binary with one fault point armed and
// requires a clean child verdict. The strict env strip prevents a stray
// ambient control variable from redirecting the child (chunk-5 pattern).
func runOutcomeChild(t *testing.T, path, action string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), outcomeChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+outcomeChildTest)
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") {
			continue
		}
		env = append(env, kv)
	}
	faultPoint := map[string]string{
		"outcome_unknown": "commit.after_meta_write",
		"abort_class":     "commit.prepare",
		"abort_nested":    "commit.prepare,commit.discard_unpublished",
	}[action]
	if faultPoint == "" {
		t.Fatalf("unknown action %q", action)
	}
	cmd.Env = append(env,
		outcomeChildSpawned+"=1",
		outcomeChildPath+"="+path,
		outcomeChildAction+"="+action,
		"IPRANGE_V4_TEST_FAIL_AT="+faultPoint,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("outcome child %s timed out", action)
		}
		t.Fatalf("outcome child %s failed: %v", action, err)
	}
}

// TestWriterOutcomeUnknownFailClosed drives both fault points through the
// real public facade in a child process.
func TestWriterOutcomeUnknownFailClosed(t *testing.T) {
	dir := t.TempDir()
	runOutcomeChild(t, filepath.Join(dir, "outcome.iprdb"), "outcome_unknown")
	runOutcomeChild(t, filepath.Join(dir, "aborted.iprdb"), "abort_class")
	runOutcomeChild(t, filepath.Join(dir, "abort-nested.iprdb"), "abort_nested")
}

// TestOutcomeUnknownChild is the subprocess entry point; it only runs
// when the parent set the spawn marker.
func TestOutcomeUnknownChild(t *testing.T) {
	if os.Getenv(outcomeChildSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	// Self-deadline so a hang cannot linger after the parent died.
	time.AfterFunc(outcomeChildTimeout, func() { os.Exit(1) })

	path := os.Getenv(outcomeChildPath)
	action := os.Getenv(outcomeChildAction)
	if _, err := Create(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTag{}); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(10), IPv4(20), 5); err != nil {
		t.Fatal(err)
	}
	res, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}

	switch action {
	case "abort_nested":
		// Both faults fire: the preparation failure aborts the commit and
		// the abandonment discard fails too, so the chain nests the
		// CleanupInProgress class (code 64, Rust CleanupIncomplete)
		// around the original cause, exactly like Rust abort_after_source.
		if res.Status != CommitNotCommitted {
			t.Fatalf("commit status = %v, want NotCommitted", res.Status)
		}
		if !isPubCode(res.Err, ErrorTransactionAborted) {
			t.Fatalf("outer class = %v, want TransactionAborted", res.Err)
		}
		first := errors.Unwrap(res.Err)
		if first == nil {
			t.Fatal("aborted commit carries no nested cause")
		}
		if !isPubCode(first, ErrorCleanupInProgress) {
			t.Fatalf("nested class = %v, want CleanupInProgress", first)
		}
		if errors.Unwrap(first) == nil {
			t.Fatal("nested cleanup error does not expose the original cause")
		}
		// A failed abandonment discard brands the writer unusable
		// (Rust abort_after_source State::Unusable): every mutating
		// entry point fails closed and only Close remains legal.
		if _, err := w.BeginDirect(); !isPubCode(err, ErrorWrongState) {
			t.Fatalf("BeginDirect after nested abort err = %v, want WrongState", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close after nested abort: %v", err)
		}
		if _, err := w.BeginDirect(); !isPubCode(err, ErrorWrongState) {
			t.Fatalf("BeginDirect after close err = %v, want WrongState", err)
		}
		return

	case "outcome_unknown":
		// The fault fires after the alternate meta write: the commit
		// reports OutcomeUnknown and the writer turns unhealthy.
		if res.Status != CommitOutcomeUnknown {
			t.Fatalf("commit status = %v, want OutcomeUnknown (err %v)", res.Status, res.Err)
		}
		if _, err := w.BeginDirect(); !isPubCode(err, ErrorWrongState) {
			t.Fatalf("BeginDirect after outcome-unknown err = %v, want WrongState", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close after outcome-unknown: %v", err)
		}
		if _, err := w.BeginDirect(); !isPubCode(err, ErrorWrongState) {
			t.Fatalf("BeginDirect after close err = %v, want WrongState", err)
		}
	case "abort_class":
		// The fault fires during Prepare: the commit aborts with the
		// TransactionAborted class (code 22) and the draft is gone.
		if res.Status != CommitNotCommitted {
			t.Fatalf("commit status = %v, want NotCommitted", res.Status)
		}
		if !isPubCode(res.Err, ErrorTransactionAborted) {
			t.Fatalf("aborted commit Err = %v, want TransactionAborted class", res.Err)
		}
		// A preparation failure is not fatal: the writer stays healthy
		// and a fresh transaction commits normally.
		tx, err := w.BeginDirect()
		if err != nil {
			t.Fatalf("BeginDirect after aborted commit: %v", err)
		}
		if _, err := tx.AssignV4(IPv4(30), IPv4(40), 7); err != nil {
			t.Fatal(err)
		}
		res, err := tx.Commit()
		if err != nil || res.Status != CommitCommitted {
			t.Fatalf("retry commit = %+v err %v, want committed", res, err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown action %q", action)
	}
}
