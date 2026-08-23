//go:build v4work && (linux || darwin)

// Crash-point state matrix for CreateLive, InitializeLive, and
// ResetLiveCoordination (Rust live_crash_tests.rs create/initialize/
// reset arms): each named point exits the child with code 86 at the
// exact physical step, and the parent verifies the resulting artifact
// set, sidecar header state, and the resultless recovery outcome
// through ResolveInterruptedLiveTransition.

package live

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

const (
	crashChildTest = "^TestLiveCrashChild$"
	crashActionEnv = "IPRANGE_V4_TEST_ACTION"
	crashPathEnv   = "IPRANGE_V4_TEST_PATH"
	crashTimeout   = 60 * time.Second
)

// TestLiveCrashChild runs the named action at the named path with the
// armed crash point and dies at it with Rust's code 86.
func TestLiveCrashChild(t *testing.T) {
	action := os.Getenv(crashActionEnv)
	path := os.Getenv(crashPathEnv)
	if action == "" || path == "" {
		t.Skip("not a crash child")
	}
	switch action {
	case "create":
		if _, err := CreateLive(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 2, nil); err != nil {
			t.Fatal(err)
		}
	case "initialize":
		if _, err := InitializeLive(path, 2, nil); err != nil {
			t.Fatal(err)
		}
	case "reset":
		if _, err := ResetLiveCoordination(path, 2, crashResetPolicy(), nil); err != nil {
			t.Fatal(err)
		}
	case "commit":
		w, err := OpenLiveWriter(path, crashWriterBudget(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.BeginDirect(); err != nil {
			t.Fatal(err)
		}
		if changed, err := w.AssignV4(10, 20, 123); err != nil || !changed {
			t.Fatalf("assign: changed=%v err=%v", changed, err)
		}
		if result, err := w.Commit(nil); err != nil || result.Durability != CommitCommitted {
			t.Fatalf("commit: %+v (%v)", result, err)
		}
	default:
		t.Fatalf("unknown crash action %q", action)
	}
}

// runCrashChild spawns this test binary as the crash child with one
// crash point armed and requires exit code 86.
func runCrashChild(t *testing.T, path, action, point string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), crashTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+crashChildTest)
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		crashActionEnv+"="+action,
		crashPathEnv+"="+path,
		"IPRANGE_V4_TEST_CRASH_AT="+point,
	)
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("crash child at %s: err=%v, want exit 86", point, err)
	}
}

func TestCreateCrashPointsLeaveExactArtifacts(t *testing.T) {
	cases := []struct {
		point        string
		mainPresent  bool
		sidecarState sidecarState
	}{
		{"create.after_sidecar_sync", false, stateCreating},
		{"create.after_sidecar_parent_sync", false, stateCreating},
		{"create.after_main_sync", true, stateCreating},
		{"create.after_main_parent_sync", true, stateCreating},
		{"create.after_ready_write", true, stateReady},
		{"create.after_ready_sync", true, stateReady},
		{"create.after_ready_parent_sync", true, stateReady},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			runCrashChild(t, main, "create", tc.point)

			if tc.mainPresent {
				boot := readBootstrap(t, main)
				if boot.Meta.TxnID != 1 {
					t.Fatalf("crashed main txn = %d, want 1", boot.Meta.TxnID)
				}
			} else if _, err := os.Lstat(main); !os.IsNotExist(err) {
				t.Fatalf("main exists before its crash point: %v", err)
			}

			if got := sidecarStateOf(t, main); got != int(tc.sidecarState) {
				t.Fatalf("sidecar state = %d, want %d", got, tc.sidecarState)
			}
		})
	}
}

func TestInitializeCrashPointsLeaveExactArtifacts(t *testing.T) {
	cases := []struct {
		point        string
		sidecarState sidecarState
	}{
		{"live_initialize.after_creating_sync", stateCreating},
		{"live_initialize.after_creating_parent_sync", stateCreating},
		{"live_initialize.after_ready_sync", stateReady},
		{"live_initialize.after_ready_parent_sync", stateReady},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(main + ".readers"); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(main)
			if err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, main, "initialize", tc.point)

			after, err := os.ReadFile(main)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("initialize crash changed the main bytes")
			}
			if got := sidecarStateOf(t, main); got != int(tc.sidecarState) {
				t.Fatalf("sidecar state = %d, want %d", got, tc.sidecarState)
			}
		})
	}
}

// TestLiveWriterCommitCrashPointsSelectOnlyACompleteGeneration arms the
// four writer-core publication crash points through the live writer
// (Rust live_crash_tests::commit_crashes_select_only_a_complete_generation):
// a crash before the private sync leaves generation 1 selected and the
// value absent, a crash after the meta write or sync exposes the
// complete generation 2, and the live writer reopens and trims any
// unpublished tail in both cases.
func TestLiveWriterCommitCrashPointsSelectOnlyACompleteGeneration(t *testing.T) {
	cases := []struct {
		point   string
		wantTxn uint64
	}{
		{"commit.before_private_sync", 1},
		{"commit.after_private_sync", 1},
		{"commit.after_meta_write", 2},
		{"commit.after_meta_sync", 2},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, nil); err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, main, "commit", tc.point)

			// The writer open is the recovery surface: it re-selects
			// the committed generation, scans the reader table, claims
			// the lease, and trims any unpublished tail (Rust
			// live_writer::open_locked).
			w, err := OpenLiveWriter(main, crashWriterBudget(), nil, nil)
			if err != nil {
				t.Fatalf("writer open after %s: %v", tc.point, err)
			}
			info, err := w.BaseInfo()
			if err != nil {
				w.Close()
				t.Fatalf("BaseInfo after %s: %v", tc.point, err)
			}
			if info.TransactionID != tc.wantTxn {
				w.Close()
				t.Fatalf("writer txn after %s = %d, want %d", tc.point, info.TransactionID, tc.wantTxn)
			}
			if result, err := w.Close(); err != nil || result.Outcome != CloseOutcomeClosed {
				t.Fatalf("close after %s = %+v (%v)", tc.point, result, err)
			}

			// The immutable reader refuses the live pair, so the
			// committed payload is proven on a sidecar-free copy of the
			// trimmed main (Rust ImmutableReader::open on the trimmed
			// pair).
			raw, err := os.ReadFile(main)
			if err != nil {
				t.Fatal(err)
			}
			copy := main + ".copy"
			if err := os.WriteFile(copy, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			r, err := reader.OpenImmutable(copy)
			if err != nil {
				t.Fatalf("immutable open after %s: %v", tc.point, err)
			}
			got, found, err := r.LookupDirect4(10)
			if err != nil {
				r.Close()
				t.Fatal(err)
			}
			if tc.wantTxn == 1 && found {
				r.Close()
				t.Fatalf("lookup 10 after %s = %d (found), want absent", tc.point, got)
			}
			if tc.wantTxn == 2 && (!found || got != 123) {
				r.Close()
				t.Fatalf("lookup 10 after %s = (%d,%v), want (123,true)", tc.point, got, found)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestLiveWriterOutcomeUnknownFailClosed drives the post-meta-write
// failure through the live commit barrier in a child process with the
// fault.Fail point armed: the commit reports OutcomeUnknown, every
// later operation fails closed with WrongState, and Close completes
// cleanly with no abort payload because the failed publish abandoned
// the draft (Rust State::OutcomeUnknown + outcome_unknown parity; the
// immutable writer gate lives in outcome_v4work_test.go).
func TestLiveWriterOutcomeUnknownFailClosed(t *testing.T) {
	main := filepath.Join(t.TempDir(), "db.iprdb")
	runOutcomeChild(t, main, "live_outcome_unknown")
}

// runOutcomeChild spawns this test binary as the fail-armed child for
// the live outcome-unknown gate and requires a clean verdict.
func runOutcomeChild(t *testing.T, path, action string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), crashTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestLiveOutcomeUnknownChild$")
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		crashPathEnv+"="+path,
		"IPRANGE_V4_TEST_ACTION="+action,
		"IPRANGE_V4_TEST_FAIL_AT=commit.after_meta_write",
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

// TestLiveOutcomeUnknownChild is the subprocess entry point of the
// outcome-unknown gate; it only runs when the parent armed the path.
func TestLiveOutcomeUnknownChild(t *testing.T) {
	path := os.Getenv(crashPathEnv)
	if path == "" {
		t.Skip("subprocess entry point")
	}
	time.AfterFunc(crashTimeout, func() { os.Exit(1) })

	if _, err := CreateLive(path, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, nil); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLiveWriter(path, crashWriterBudget(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.BeginDirect(); err != nil {
		t.Fatal(err)
	}
	if changed, err := w.AssignV4(10, 20, 5); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	result, err := w.Commit(nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Durability != CommitOutcomeUnknown {
		t.Fatalf("durability = %v, want OutcomeUnknown", result.Durability)
	}
	if result.CoordinationCleanup != CoordinationCleanupRetainedWriterCloseRequired {
		t.Fatalf("coordination = %v, want retained writer close", result.CoordinationCleanup)
	}
	if _, err := w.BaseInfo(); err == nil {
		t.Fatal("writer stayed healthy after an OutcomeUnknown commit")
	} else {
		expectCode(t, err, format.CodeWrongState)
	}
	// The failed publish abandoned the draft (Rust
	// WriterCore::outcome_unknown), so close re-selects the already
	// complete new generation and completes cleanly with no abort
	// payload (Rust close: had_pending = core.has_draft() = false).
	closeResult, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeResult.Outcome != CloseOutcomeClosed {
		t.Fatalf("close outcome = %v, want Closed (cause %v)", closeResult.Outcome, closeResult.Cause)
	}
	if closeResult.AbortOutcome != nil {
		t.Fatalf("close abort = %v, want none", *closeResult.AbortOutcome)
	}

	// The post-meta-write failure left the complete new generation
	// behind (Rust live_crash_tests after_meta_write expectation).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copy := path + ".copy"
	if err := os.WriteFile(copy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := reader.OpenImmutable(copy)
	if err != nil {
		t.Fatalf("immutable open: %v", err)
	}
	if r.Meta().TxnID != 2 {
		r.Close()
		t.Fatalf("txn = %d, want 2", r.Meta().TxnID)
	}
	got, found, err := r.LookupDirect4(10)
	if err != nil {
		r.Close()
		t.Fatal(err)
	}
	if !found || got != 5 {
		r.Close()
		t.Fatalf("lookup 10 = (%d,%v), want (5,true)", got, found)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

// crashWriterBudget is the page budget of the crash children; the live
// writer tests compile under the v4work tag too, so the shared
// liveWriterTestBudget helper is reused here.
func crashWriterBudget() writer.PageBudget {
	return liveWriterTestBudget()
}

// crashResetPolicy is the reset policy of the crash children: the
// rollback-safe exchange when the host supports it, the discarding
// replacement otherwise (Rust live_crash_tests::reset_policy).
func crashResetPolicy() LiveResetPolicy {
	if mapping.ExchangeAvailable() {
		return LiveResetRollbackSafe
	}
	return LiveResetDiscardPrevious
}

// TestCreateCrashResiduesRecoverWithoutTheLostResult drives the
// resultless recovery of interrupted creates (Rust
// live_crash_tests::creation_crashes_are_recoverable_without_the_lost_result):
// a crash before the main exists rolls the sidecar back to Removed; a
// crash after the main sync completes the pair to Completed and the
// live reader opens.
func TestCreateCrashResiduesRecoverWithoutTheLostResult(t *testing.T) {
	for _, point := range []string{"create.after_sidecar_sync", "create.after_sidecar_parent_sync"} {
		t.Run(point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			runCrashChild(t, main, "create", point)
			recovered, err := ResolveInterruptedLiveTransition(main, LiveTransitionResolutionRollback, nil)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != LiveResidueStatusRemoved {
				t.Fatalf("status = %v, want Removed", recovered.Status)
			}
			if _, err := os.Lstat(main); !os.IsNotExist(err) {
				t.Fatalf("main exists after rollback: %v", err)
			}
			if _, err := os.Lstat(main + ".readers"); !os.IsNotExist(err) {
				t.Fatalf("sidecar exists after rollback: %v", err)
			}
		})
	}
	for _, point := range []string{"create.after_main_sync", "create.after_main_parent_sync"} {
		t.Run(point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			runCrashChild(t, main, "create", point)
			recovered, err := ResolveInterruptedLiveTransition(main, LiveTransitionResolutionComplete, nil)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != LiveResidueStatusCompleted {
				t.Fatalf("status = %v, want Completed", recovered.Status)
			}
			lr, err := OpenLiveReader(main, nil)
			if err != nil {
				t.Fatal(err)
			}
			lr.Close()
		})
	}
}

// TestInitializeCrashResiduesRecoverWithoutTheLostResult drives the
// resultless recovery of interrupted initializes (Rust
// live_crash_tests::initialization_crashes_are_recoverable_without_the_lost_result):
// every initialize crash point completes to Completed or Ready and the
// live reader opens.
func TestInitializeCrashResiduesRecoverWithoutTheLostResult(t *testing.T) {
	for _, point := range []string{
		"live_initialize.after_creating_sync",
		"live_initialize.after_creating_parent_sync",
		"live_initialize.after_ready_sync",
		"live_initialize.after_ready_parent_sync",
	} {
		t.Run(point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(main + ".readers"); err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, main, "initialize", point)

			recovered, err := ResolveInterruptedLiveTransition(main, LiveTransitionResolutionComplete, nil)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != LiveResidueStatusCompleted && recovered.Status != LiveResidueStatusReady {
				t.Fatalf("status = %v, want Completed or Ready", recovered.Status)
			}
			lr, err := OpenLiveReader(main, nil)
			if err != nil {
				t.Fatal(err)
			}
			lr.Close()
		})
	}
}

// TestResetCrashPointsLeaveRetryableOrReadyDatabase drives the
// resultless recovery of interrupted resets (Rust
// live_crash_tests::reset_crashes_leave_a_retryable_or_ready_database):
// a crash before the installation rolls the private sidecar back to
// Removed and a fresh reset succeeds; a crash after the installation
// completes or reports Ready and the live reader opens.
func TestResetCrashPointsLeaveRetryableOrReadyDatabase(t *testing.T) {
	for _, point := range []string{
		"live_reset.after_creating_sync",
		"live_reset.after_ready_sync",
		"live_reset.after_private_parent_sync",
		"live_reset.before_replace",
	} {
		t.Run(point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(main+".readers", []byte("corrupt"), 0o600); err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, main, "reset", point)

			recovered, err := ResolveInterruptedLiveTransition(main, LiveTransitionResolutionRollback, nil)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != LiveResidueStatusRemoved {
				t.Fatalf("status = %v, want Removed", recovered.Status)
			}
			if _, err := os.Lstat(main + ".readers.reset"); !os.IsNotExist(err) {
				t.Fatalf("reset temp survived rollback: %v", err)
			}
			if _, err := ResetLiveCoordination(main, 2, crashResetPolicy(), nil); err != nil {
				t.Fatal(err)
			}
			lr, err := OpenLiveReader(main, nil)
			if err != nil {
				t.Fatal(err)
			}
			lr.Close()
		})
	}
	for _, point := range []string{"live_reset.after_replace", "live_reset.after_directory_sync"} {
		t.Run(point, func(t *testing.T) {
			main := filepath.Join(t.TempDir(), "db.iprdb")
			if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, nil); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(main+".readers", []byte("corrupt"), 0o600); err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, main, "reset", point)

			recovered, err := ResolveInterruptedLiveTransition(main, LiveTransitionResolutionComplete, nil)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != LiveResidueStatusCompleted && recovered.Status != LiveResidueStatusReady {
				t.Fatalf("status = %v, want Completed or Ready", recovered.Status)
			}
			lr, err := OpenLiveReader(main, nil)
			if err != nil {
				t.Fatal(err)
			}
			lr.Close()
		})
	}
}
