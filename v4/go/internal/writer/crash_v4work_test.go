//go:build v4work

// Child-process crash-consistency tests for the four commit crash points
// (Rust live_crash_tests.rs: commit_crashes_select_only_a_complete_generation,
// reclamation_crashes_preserve_a_complete_readable_generation,
// process_death_releases_reader_and_writer_locks). The real publication
// path runs in a spawned child of this test binary; internal/fault makes
// the child exit 86 at the exact physical step named by
// IPRANGE_V4_TEST_CRASH_AT, and every crash state must re-open with either
// the complete old generation or the complete new one - never a torn mix.
// Production builds compile the crash points to no-ops, so this suite is
// v4work-only (the same configuration the fault machinery lives in).

package writer

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/reader"
)

const (
	crashChildTest   = "^TestCrashChild$"
	crashChildAction = "IPRANGE_V4_TEST_ACTION"
	crashChildPath   = "IPRANGE_V4_TEST_PATH"
)

// crashBudget is the publication-budget shape used by the child actions
// (the shape the rest of the publication tests use).
func crashBudget() PageBudget {
	return PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
}

// runCrashChild spawns this test binary as the action's child process and
// requires it to die with exit code 86 (Rust live_crash_tests.rs
// run_child). An empty crashPoint means the child exits 86 itself after
// the open (the lock-release shape).
func runCrashChild(t *testing.T, path, action, crashPoint string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run="+crashChildTest)
	cmd.Env = append(os.Environ(),
		crashChildAction+"="+action,
		crashChildPath+"="+path,
	)
	if crashPoint != "" {
		cmd.Env = append(cmd.Env, "IPRANGE_V4_TEST_CRASH_AT="+crashPoint)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 86 {
			return
		}
		t.Fatalf("crash child %s at %q exited %d, want 86", action, crashPoint, exitErr.ExitCode())
	}
	if err != nil {
		t.Fatalf("crash child %s at %q: %v", action, crashPoint, err)
	}
	t.Fatalf("crash child %s at %q exited 0: the crash point was not reached", action, crashPoint)
}

// TestCrashChild is the subprocess entry point (the Rust
// #[ignore]-marked crash_child). It runs only when spawned by
// runCrashChild; in a normal suite run it skips.
func TestCrashChild(t *testing.T) {
	action := os.Getenv(crashChildAction)
	if action == "" {
		t.Skip("subprocess entry point")
	}
	path := os.Getenv(crashChildPath)
	if path == "" {
		t.Fatal("missing " + crashChildPath)
	}
	switch action {
	case "commit":
		crashChildCommit(t, path)
	case "reclaim":
		crashChildReclaim(t, path)
	case "reader":
		if _, err := reader.OpenImmutable(path); err != nil {
			t.Fatal(err)
		}
		os.Exit(86)
	case "writer":
		if _, err := Open(path, crashBudget(), nil); err != nil {
			t.Fatal(err)
		}
		os.Exit(86)
	default:
		t.Fatalf("unknown crash child action %q", action)
	}
	t.Fatal("configured crash point was not reached")
}

// crashChildCommit runs the full edit -> prepare -> publish cycle in the
// child (Rust crash_child "commit": one direct transaction assigning
// [10,20] value 123 then committing).
func crashChildCommit(t *testing.T, path string) {
	t.Helper()
	c, err := Open(path, crashBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	commitAssign(t, c, 7, 10, 20, 123)
}

// crashChildReclaim runs one bounded reclamation publish in the child
// (Rust crash_child "reclaim": writer.reclaim(10, 10_000)).
func crashChildReclaim(t *testing.T, path string) {
	t.Helper()
	c, err := Open(path, crashBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := c.PrepareReclamation(nil, 10, 10_000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil {
		t.Fatal("reclamation selected nothing")
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("reclamation publish = %v (%v)", res.Status, res.Err)
	}
}

// commitAssign commits one direct range assignment in a fresh draft over
// the committed generation.
func commitAssign(t *testing.T, c *Core, nonce byte, from, to, value uint32) {
	t.Helper()
	if err := c.StartDraft([16]byte{nonce}); err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	if _, err := store.AssignV4(from, to, value); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireDraftLength(); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish = %v (%v)", res.Status, res.Err)
	}
}

// TestCrashCommitSelectsCompleteGeneration pins the commit crash contract
// (Rust commit_crashes_select_only_a_complete_generation): a crash before
// the private sync leaves the previous generation selected and the value
// absent; a crash after the meta write or sync exposes either the
// complete old generation or the complete new one - never a torn mix.
func TestCrashCommitSelectsCompleteGeneration(t *testing.T) {
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
			path := makeEmptyDBPages(t, 64)
			runCrashChild(t, path, "commit", tc.point)

			if tc.wantTxn == 1 {
				// Rust reads the committed generation past the
				// unpublished tail with LiveReader and reopens the
				// writer. The Go surfaces mirror this split: the
				// immutable reader refuses the tail exactly like Rust
				// ImmutableReader::open (ImmutableLengthMismatch), and
				// the writer open is the recovery surface (committed
				// bootstrap + tail trim, Rust live_writer open_locked).
				if _, err := reader.OpenImmutable(path); err == nil {
					t.Fatalf("immutable reader accepted the unpublished tail after %s", tc.point)
				}
				c, err := Open(path, crashBudget(), nil)
				if err != nil {
					t.Fatalf("writer open after %s: %v", tc.point, err)
				}
				if c.base.Meta.TxnID != 1 {
					c.Close()
					t.Fatalf("writer txn after %s = %d, want 1", tc.point, c.base.Meta.TxnID)
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			}

			r, err := reader.OpenImmutable(path)
			if err != nil {
				t.Fatalf("reader open after %s: %v", tc.point, err)
			}
			if got := r.Meta().TxnID; got != tc.wantTxn {
				r.Close()
				t.Fatalf("reader txn after %s = %d, want %d", tc.point, got, tc.wantTxn)
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
				t.Fatalf("lookup 10 after %s = (%d, %v), want (123, true)", tc.point, got, found)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestCrashReclamationPreservesCompleteGeneration pins the reclamation
// crash contract (Rust
// reclamation_crashes_preserve_a_complete_readable_generation): two
// commits, then a reclamation publish crashing at the four commit points
// must never lose a committed range; the selected generation is txn 3
// before the meta write and txn 4 after it.
func TestCrashReclamationPreservesCompleteGeneration(t *testing.T) {
	cases := []struct {
		point   string
		wantTxn uint64
	}{
		{"commit.before_private_sync", 3},
		{"commit.after_private_sync", 3},
		{"commit.after_meta_write", 4},
		{"commit.after_meta_sync", 4},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			path := makeEmptyDBPages(t, 64)
			c, err := Open(path, crashBudget(), nil)
			if err != nil {
				t.Fatal(err)
			}
			commitAssign(t, c, 1, 10, 20, 1)
			commitAssign(t, c, 2, 12, 18, 2)
			if c.base.Meta.TxnID != 3 {
				c.Close()
				t.Fatalf("setup txn = %d, want 3", c.base.Meta.TxnID)
			}
			// The child writer needs the exclusive lifetime lock.
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}

			runCrashChild(t, path, "reclaim", tc.point)

			r, err := reader.OpenImmutable(path)
			if err != nil {
				t.Fatalf("reader open after %s: %v", tc.point, err)
			}
			defer r.Close()
			if got := r.Meta().TxnID; got != tc.wantTxn {
				t.Fatalf("reader txn after %s = %d, want %d", tc.point, got, tc.wantTxn)
			}
			got, found, err := r.LookupDirect4(11)
			if err != nil || !found || got != 1 {
				t.Fatalf("lookup 11 after %s = (%d, %v, %v), want (1, true, nil)", tc.point, got, found, err)
			}
			got, found, err = r.LookupDirect4(15)
			if err != nil || !found || got != 2 {
				t.Fatalf("lookup 15 after %s = (%d, %v, %v), want (2, true, nil)", tc.point, got, found, err)
			}
		})
	}
}

// TestProcessDeathReleasesLocks pins the lock-release contract (Rust
// process_death_releases_reader_and_writer_locks): a child that dies
// while holding the reader or writer lifetime claim releases it, so the
// parent can immediately re-open in the same shape.
func TestProcessDeathReleasesLocks(t *testing.T) {
	path := makeEmptyDBPages(t, 8)

	runCrashChild(t, path, "reader", "")
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatalf("reader re-open after reader death: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	runCrashChild(t, path, "writer", "")
	c, err := Open(path, crashBudget(), nil)
	if err != nil {
		t.Fatalf("writer re-open after writer death: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
