//go:build v4work && linux

// Publication attempt crash matrix (Rust publication/crash_tests.rs
// main_crashes_expose_only_the_complete_desired_inode_behind_
// reservation, retirement_crashes_leave_a_normally_openable_complete_
// main, and replacement_crashes_preserve_exact_previous_or_desired_
// state): every publication.after_main_* / after_previous_unlink /
// after_reservation_unlink / after_retirement_sync point exits the
// child with code 86 at the exact physical step, and the parent
// proves the surviving main, the previous bytes, the reservation
// placement, and the selectable authority per point. The Rust
// ImmutableReader::open refusal on the armed main is wired with the
// reader authority slices (J resolver, N publish retrofit), not here.

package publication

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
	attemptCrashChildTest = "^TestAttemptCrashChild$"
	attemptCrashActionEnv = "IPRANGE_V4_ATTEMPT_CRASH_ACTION"
	attemptCrashPathEnv   = "IPRANGE_V4_ATTEMPT_CRASH_PATH"
	attemptCrashTimeout   = 60 * time.Second
)

// TestAttemptCrashChild drives one full fail-if-exists or replacement
// publication at the named path with the armed crash point and dies
// at it with Rust's code 86 (Rust crash_child).
func TestAttemptCrashChild(t *testing.T) {
	action := os.Getenv(attemptCrashActionEnv)
	path := os.Getenv(attemptCrashPathEnv)
	if action == "" || path == "" {
		t.Skip("not an attempt crash child")
	}
	created, err := createOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	secured, failure := created.secure()
	if failure != nil {
		t.Fatal(failure)
	}
	attempt, file := secured.intoParts()
	finished, _ := testFinishedOutput(t, file)
	prepared, prepareFailure := attempt.prepareCancellable(finished, nil)
	if prepareFailure != nil {
		t.Fatal(prepareFailure)
	}
	noopCheck := func() error { return nil }
	if action == "replace" {
		// The replaced main is any retained regular file (Rust writes
		// raw previous bytes before binding).
		if err := os.WriteFile(path, []byte("previous bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		bound, bindFailure := bindPrevious(prepared, noopCheck)
		if bindFailure != nil {
			t.Fatal(bindFailure)
		}
		// Rust's replacement crash child runs the plain machine
		// (replace_existing_cancellable); the observed publish child
		// covers the observer wiring.
		if _, failure := replaceExistingCancellable(bound, noopCheck); failure != nil {
			t.Fatal(failure)
		}
	} else {
		if _, failure := failIfExistsCancellableObserved(prepared, noopCheck, noopAttemptObserver); failure != nil {
			t.Fatal(failure)
		}
	}
	t.Fatal("configured publication crash point was not reached")
}

// runAttemptCrashChild spawns this test binary as the crash child
// with one publication point armed and requires exit code 86.
func runAttemptCrashChild(t *testing.T, path, action, point string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), attemptCrashTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+attemptCrashChildTest)
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") || strings.HasPrefix(kv, "IPRANGE_V4_ATTEMPT_CRASH_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		attemptCrashActionEnv+"="+action,
		attemptCrashPathEnv+"="+path,
		"IPRANGE_V4_TEST_CRASH_AT="+point,
	)
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("crash child at %s: err=%v, want exit 86", point, err)
	}
}

// assertCompleteMain proves the main file is the complete finished
// fixture (Rust assert_complete_output) and returns its portable
// identity bytes.
func assertCompleteMain(t *testing.T, main string) [32]byte {
	t.Helper()
	if _, err := os.Lstat(main); err != nil {
		t.Fatalf("main missing: %v", err)
	}
	return assertCompleteOutput(t, main)
}

// assertNoPublicationArtifacts proves the directory has neither
// private outputs nor private reservations (Rust Artifacts).
func assertNoPublicationArtifacts(t *testing.T, dir string) {
	t.Helper()
	if privates := scanPrefixed(t, dir, outputPrefix); len(privates) != 0 {
		t.Fatalf("private outputs = %d, want none", len(privates))
	}
	if privates := scanPrefixed(t, dir, reservationPrefix); len(privates) != 0 {
		t.Fatalf("private reservations = %d, want none", len(privates))
	}
}

func TestAttemptMainCrashesExposeOnlyTheCompleteDesiredInodeBehindReservation(t *testing.T) {
	for _, point := range []string{
		"publication.after_main_rename",
		"publication.after_main_sync",
		"publication.after_main_directory_sync",
		"publication.after_main_proof",
	} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			main := filepath.Join(dir, "result.v4")
			runAttemptCrashChild(t, main, "publish", point)

			mainIdentity := assertCompleteMain(t, main)
			assertNoPublicationArtifacts(t, dir)
			coordination := filepath.Join(dir, "result.v4.readers")
			if _, err := os.Lstat(coordination); err != nil {
				t.Fatalf("%s: coordination missing: %v", point, err)
			}
			selected := assertSelectableReservation(t, coordination)
			if selected.state != reservationStateMainMayHaveBeenAttempted {
				t.Fatalf("%s: selected state %d, want MainMayHaveBeenAttempted", point, selected.state)
			}
			if selected.outputIdentity != mainIdentity {
				t.Fatalf("%s: reservation output identity differs from the main", point)
			}
		})
	}
}

func TestAttemptRetirementCrashesLeaveACompleteMain(t *testing.T) {
	for _, point := range []string{
		"publication.after_reservation_unlink",
		"publication.after_retirement_sync",
	} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			main := filepath.Join(dir, "result.v4")
			runAttemptCrashChild(t, main, "publish", point)

			assertCompleteMain(t, main)
			assertNoPublicationArtifacts(t, dir)
			if _, err := os.Lstat(filepath.Join(dir, "result.v4.readers")); !os.IsNotExist(err) {
				t.Fatalf("%s: coordination still present after the retirement crash", point)
			}
		})
	}
}

func TestAttemptReplacementCrashesPreserveExactPreviousOrDesiredState(t *testing.T) {
	replacementPoints := []string{
		"publication.after_reservation_state1_sync",
		"publication.after_reservation_rename",
		"publication.after_reservation_directory_sync",
		"publication.after_reservation_state2_write",
		"publication.after_reservation_state2_sync",
		"publication.after_reservation_state2_selection",
		"publication.after_main_rename",
		"publication.after_main_sync",
		"publication.after_main_directory_sync",
		"publication.after_main_proof",
		"publication.after_previous_unlink",
		"publication.after_reservation_unlink",
		"publication.after_retirement_sync",
	}
	for _, point := range replacementPoints {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			main := filepath.Join(dir, "result.v4")
			runAttemptCrashChild(t, main, "replace", point)

			switch {
			case strings.Contains(point, "reservation_") && !strings.Contains(point, "unlink"):
				// The main never moved: the previous bytes are intact
				// and the new complete output is still private.
				bytes, err := os.ReadFile(main)
				if err != nil {
					t.Fatalf("%s: read previous main: %v", point, err)
				}
				if string(bytes) != "previous bytes" {
					t.Fatalf("%s: main content %q, want the previous bytes", point, bytes)
				}
				privates := scanPrefixed(t, dir, outputPrefix)
				if len(privates) != 1 {
					t.Fatalf("%s: private outputs = %d, want 1", point, len(privates))
				}
				assertCompleteOutput(t, privates[0])
			case strings.Contains(point, "after_main_"):
				// The exchange moved the new output to main and the
				// previous bytes to the private name.
				assertCompleteMain(t, main)
				privates := scanPrefixed(t, dir, outputPrefix)
				if len(privates) != 1 {
					t.Fatalf("%s: private outputs = %d, want 1", point, len(privates))
				}
				bytes, err := os.ReadFile(privates[0])
				if err != nil {
					t.Fatalf("%s: read private previous: %v", point, err)
				}
				if string(bytes) != "previous bytes" {
					t.Fatalf("%s: private content %q, want the previous bytes", point, bytes)
				}
			default:
				// Retirement points: the previous was unlinked and the
				// coordination was retired by the crash point.
				assertCompleteMain(t, main)
				assertNoPublicationArtifacts(t, dir)
				coordination := filepath.Join(dir, "result.v4.readers")
				if point == "publication.after_previous_unlink" {
					if _, err := os.Lstat(coordination); err != nil {
						t.Fatalf("%s: coordination missing before the reservation unlink", point)
					}
				} else if _, err := os.Lstat(coordination); !os.IsNotExist(err) {
					t.Fatalf("%s: coordination still present after the retirement crash", point)
				}
			}
		})
	}
}
