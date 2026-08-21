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
)

// crashFixedTag and crashFixed16 build the fixed 16-byte identity fields
// of one direct output (the writer_test package owns its own copies of
// these helpers; this v4work suite needs them in the internal package).
func crashFixedTag(text string) [16]byte {
	var tag [16]byte
	copy(tag[:], text)
	return tag
}

func crashFixed16(value byte) [16]byte {
	var out [16]byte
	for index := range out {
		out[index] = value
	}
	return out
}

// crashDirectSpec is one direct IPv4 output spec with a fixed identity
// (Rust test output_spec parity).
func crashDirectSpec() OutputSpec {
	return OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindDirect,
		StructureKind:  format.StructureKindNone,
		ValueTag:       crashFixedTag("first-seen"),
		DatabaseID:     crashFixed16(3),
		TxnID:          7,
		CommitNonce:    crashFixed16(4),
		FeedIndexLimit: 0,
	}
}

const (
	crashChildTest    = "^TestCrashChild$"
	crashChildSpawned = "IPRANGE_V4_TEST_SPAWNED"
	crashChildAction  = "IPRANGE_V4_TEST_ACTION"
	crashChildPath    = "IPRANGE_V4_TEST_PATH"
	crashChildTimeout = 60 * time.Second
)

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
	ctx, cancel := context.WithTimeout(context.Background(), crashChildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+crashChildTest)
	// Strip any inherited crash-control variables so a stray developer
	// environment cannot redirect the child to a different crash point
	// or action (the inherited value would win in the child's environ).
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") {
			continue
		}
		env = append(env, kv)
	}
	// The spawn marker is the child entry gate: without it a normal
	// suite run skips TestCrashChild regardless of ambient variables.
	cmd.Env = append(env,
		crashChildSpawned+"=1",
		crashChildAction+"="+action,
		crashChildPath+"="+path,
	)
	if crashPoint != "" {
		cmd.Env = append(cmd.Env, "IPRANGE_V4_TEST_CRASH_AT="+crashPoint)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("crash child %s at %q timed out after %s", action, crashPoint, crashChildTimeout)
	}
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
// runCrashChild, which sets the spawn marker; a normal suite run skips
// regardless of ambient variables.
func TestCrashChild(t *testing.T) {
	if os.Getenv(crashChildSpawned) != "1" {
		t.Skip("subprocess entry point")
	}
	// Self-deadline: if an action hangs (for example a leaked lifetime
	// lock), exit instead of lingering - also when the parent already
	// died and can no longer kill us (the go-test timeout path).
	time.AfterFunc(crashChildTimeout, func() { os.Exit(1) })
	action := os.Getenv(crashChildAction)
	if action == "" {
		t.Fatal("missing " + crashChildAction)
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
		if _, err := Open(path, testBudget(), nil); err != nil {
			t.Fatal(err)
		}
		os.Exit(86)
	case "publish_replace":
		crashChildPublishReplace(t, path)
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
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	commitRange(t, c, 7, 10, 20, 123)
}

// crashChildReclaim runs one bounded reclamation publish in the child
// (Rust crash_child "reclaim": writer.reclaim(10, 10_000)).
func crashChildReclaim(t *testing.T, path string) {
	t.Helper()
	c, err := Open(path, testBudget(), nil)
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
			// The fixture's meta has FreeBitmapRoot=0 (no free pages), so
			// every draft allocation grows the file; the
			// before/after_private_sync refusals below depend on that
			// growth being present before the meta write.
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
				if r, err := reader.OpenImmutable(path); err == nil {
					r.Close()
					t.Fatalf("immutable reader accepted the unpublished tail after %s", tc.point)
				} else if errCode(err) != format.CodeFormatInvalid {
					t.Fatalf("immutable reader after %s: want the length-mismatch refusal, got %v", tc.point, err)
				}
				c, err := Open(path, testBudget(), nil)
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
			c, err := Open(path, testBudget(), nil)
			if err != nil {
				t.Fatal(err)
			}
			commitRange(t, c, 1, 10, 20, 1)
			commitRange(t, c, 2, 12, 18, 2)
			if c.base.Meta.TxnID != 3 {
				c.Close()
				t.Fatalf("setup txn = %d, want 3", c.base.Meta.TxnID)
			}
			// The child writer needs the exclusive lifetime lock.
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}

			runCrashChild(t, path, "reclaim", tc.point)

			if tc.wantTxn == 3 {
				// Same recovery shape as the commit test: whether or not
				// the reclamation draft grew an unpublished tail, a
				// crash before the meta write must leave generation 3
				// selectable through the writer open, which trims any
				// tail before the immutable read below.
				rc, err := Open(path, testBudget(), nil)
				if err != nil {
					t.Fatalf("writer open after %s: %v", tc.point, err)
				}
				if rc.base.Meta.TxnID != 3 {
					rc.Close()
					t.Fatalf("writer txn after %s = %d, want 3", tc.point, rc.base.Meta.TxnID)
				}
				if err := rc.Close(); err != nil {
					t.Fatal(err)
				}
			}

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
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatalf("writer re-open after writer death: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

// crashChildPublishReplace replaces one existing main through the
// exchange publication path (Rust crash_child "replace": the replacement
// family crash window lives between the exchange rename and the
// retirement sync, main_file.rs rename_main/unlink_previous/
// sync_retirement). The previous generation is the committed range
// [10,20]=123; the replacement output carries [0,42]=2. The armed crash
// point decides how much of the retirement ran before the child died.
func crashChildPublishReplace(t *testing.T, path string) {
	t.Helper()
	attempt, err := CreateAttempt(path, PolicyReplaceExisting)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewOutputBuilder(attempt.AttemptPath(), crashDirectSpec(), OutputBudget{MaxOutputPages: 100_000}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PushDirectV4(0, 42, 2); err != nil {
		t.Fatal(err)
	}
	if err := b.Finish(); err != nil {
		t.Fatal(err)
	}
	result, err := Publish(attempt, b, PolicyReplaceExisting)
	if err != nil || result.Status != PublicationPublished {
		t.Fatalf("publish replace = %v (%v), want Published", result, err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCrashPublishReplacePreservesExactPreviousOrDesiredState pins the
// replacement-family crash contract (Rust replacement_crashes_preserve_
// exact_previous_or_desired_state): after the exchange rename the main
// holds the complete new output and the private name still holds the
// exchanged previous; once the retirement unlinked it, no private
// artifact survives - the main is always the complete new generation,
// never a torn mix.
func TestCrashPublishReplacePreservesExactPreviousOrDesiredState(t *testing.T) {
	// The exchange primitive is not atomic on every target (Rust guards
	// the equivalent replacement crash test with
	// cfg(any(target_os = "linux", target_vendor = "apple"))): where
	// RENAME_EXCHANGE is unavailable the publish refuses before the
	// rename and there is no crash window to pin.
	if !mapping.ExchangeAvailable() {
		t.Skip("atomic name exchange unavailable")
	}
	for _, point := range []string{
		"publication.after_main_rename",
		"publication.after_previous_unlink",
		"publication.after_retirement_sync",
	} {
		t.Run(point, func(t *testing.T) {
			path := makeEmptyDBPages(t, 64)
			dir := filepath.Dir(path)
			c, err := Open(path, testBudget(), nil)
			if err != nil {
				t.Fatal(err)
			}
			commitRange(t, c, 7, 10, 20, 123)
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, path, "publish_replace", point)

			r, err := reader.OpenImmutable(path)
			if err != nil {
				t.Fatalf("%s: main does not reopen: %v", point, err)
			}
			if v, ok, err := r.LookupDirect4(0); err != nil || !ok || v != 2 {
				t.Fatalf("%s: main lookup = %d ok %v err %v, want 2", point, v, ok, err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			var privates []string
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if len(entry.Name()) >= len(attemptPrefix) && entry.Name()[:len(attemptPrefix)] == attemptPrefix {
					privates = append(privates, filepath.Join(dir, entry.Name()))
				}
			}
			if point == "publication.after_main_rename" {
				if len(privates) != 1 {
					t.Fatalf("%s: private outputs = %d, want 1", point, len(privates))
				}
				// The reader refuses reserved basenames by policy; the
				// test inspects the exchanged previous under a neutral
				// name in the same directory.
				neutral := filepath.Join(dir, "previous-copy.v4")
				if err := os.Rename(privates[0], neutral); err != nil {
					t.Fatalf("%s: rename private artifact: %v", point, err)
				}
				prev, err := reader.OpenImmutable(neutral)
				if err != nil {
					t.Fatalf("%s: exchanged previous does not reopen: %v", point, err)
				}
				if v, ok, err := prev.LookupDirect4(15); err != nil || !ok || v != 123 {
					t.Fatalf("%s: previous lookup = %d ok %v err %v, want 123", point, v, ok, err)
				}
				if err := prev.Close(); err != nil {
					t.Fatal(err)
				}
			} else if len(privates) != 0 {
				t.Fatalf("%s: private outputs = %d, want none", point, len(privates))
			}
		})
	}
}
