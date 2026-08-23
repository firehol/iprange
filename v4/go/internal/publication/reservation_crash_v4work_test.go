//go:build v4work && linux

// Reservation crash matrix (Rust publication/crash_tests.rs
// reservation_crashes_leave_one_complete_output_and_selectable_
// authority): every publication.after_reservation_* point exits the
// child with code 86 at the exact physical step, and the parent
// verifies the reservation placement, the selectable authority, the
// complete private output, and the exact output identity binding.

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

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

const (
	reservationCrashChildTest = "^TestReservationCrashChild$"
	reservationCrashActionEnv = "IPRANGE_V4_PUBLICATION_CRASH_ACTION"
	reservationCrashPathEnv   = "IPRANGE_V4_PUBLICATION_CRASH_PATH"
	reservationCrashTimeout   = 60 * time.Second
)

// TestReservationCrashChild drives one full reservation lifecycle at
// the named path with the armed crash point and dies at it with
// Rust's code 86 (Rust crash_child).
func TestReservationCrashChild(t *testing.T) {
	action := os.Getenv(reservationCrashActionEnv)
	path := os.Getenv(reservationCrashPathEnv)
	if action == "" || path == "" {
		t.Skip("not a reservation crash child")
	}
	switch action {
	case "reserve":
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
		draft, err := createReservationDraft(prepared)
		if err != nil {
			t.Fatal(err)
		}
		private, initFailure := draft.initialize(prepared)
		if initFailure != nil {
			t.Fatal(initFailure)
		}
		canonical, acquireFailure := private.acquire(prepared)
		if acquireFailure != nil {
			t.Fatal(acquireFailure)
		}
		armed, armFailure := canonical.arm(prepared)
		if armFailure != nil {
			t.Fatal(armFailure)
		}
		_ = armed.Close()
	default:
		t.Fatalf("unknown reservation crash action %q", action)
	}
}

// runReservationCrashChild spawns this test binary as the crash child
// with one reservation point armed and requires exit code 86.
func runReservationCrashChild(t *testing.T, path, point string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), reservationCrashTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run="+reservationCrashChildTest)
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "IPRANGE_V4_TEST_") || strings.HasPrefix(kv, "IPRANGE_V4_PUBLICATION_CRASH_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		reservationCrashActionEnv+"=reserve",
		reservationCrashPathEnv+"="+path,
		"IPRANGE_V4_TEST_CRASH_AT="+point,
	)
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("crash child at %s: err=%v, want exit 86", point, err)
	}
}

func TestReservationCrashPointsLeaveSelectableAuthority(t *testing.T) {
	cases := []struct {
		point     string
		canonical bool
		expected  reservationState
		stateAny  bool
	}{
		{"publication.after_reservation_state1_sync", false, reservationStatePrepared, false},
		{"publication.after_reservation_rename", true, reservationStatePrepared, false},
		{"publication.after_reservation_directory_sync", true, reservationStatePrepared, false},
		{"publication.after_reservation_state2_write", true, 0, true},
		{"publication.after_reservation_state2_sync", true, reservationStateMainMayHaveBeenAttempted, false},
		{"publication.after_reservation_state2_selection", true, reservationStateMainMayHaveBeenAttempted, false},
	}
	for _, tc := range cases {
		t.Run(tc.point, func(t *testing.T) {
			dir := t.TempDir()
			main := filepath.Join(dir, "result.v4")
			runReservationCrashChild(t, main, tc.point)

			if _, err := os.Lstat(main); !os.IsNotExist(err) {
				t.Fatalf("%s: main exists after the crash", tc.point)
			}

			// Exactly one complete private output survives with the
			// fixture identity.
			privateOutputs := scanPrefixed(t, dir, outputPrefix)
			if len(privateOutputs) != 1 {
				t.Fatalf("%s: private outputs = %d, want 1", tc.point, len(privateOutputs))
			}
			outputIdentity := assertCompleteOutput(t, privateOutputs[0])

			// The reservation is either private or canonical with a
			// selectable authority.
			var reservationPath string
			coordination := filepath.Join(dir, "result.v4.readers")
			if tc.canonical {
				if _, err := os.Lstat(coordination); err != nil {
					t.Fatalf("%s: coordination twin missing: %v", tc.point, err)
				}
				if privates := scanPrefixed(t, dir, reservationPrefix); len(privates) != 0 {
					t.Fatalf("%s: private reservations = %d, want none", tc.point, len(privates))
				}
				reservationPath = coordination
			} else {
				if _, err := os.Lstat(coordination); !os.IsNotExist(err) {
					t.Fatalf("%s: coordination twin must not exist before the rename", tc.point)
				}
				privates := scanPrefixed(t, dir, reservationPrefix)
				if len(privates) != 1 {
					t.Fatalf("%s: private reservations = %d, want 1", tc.point, len(privates))
				}
				reservationPath = privates[0]
			}

			selected := assertSelectableReservation(t, reservationPath)
			if tc.stateAny {
				if selected.state != reservationStatePrepared && selected.state != reservationStateMainMayHaveBeenAttempted {
					t.Fatalf("%s: selected state %d, want Prepared or MainMayHaveBeenAttempted", tc.point, selected.state)
				}
			} else if selected.state != tc.expected {
				t.Fatalf("%s: selected state %d, want %d", tc.point, selected.state, tc.expected)
			}
			if selected.outputIdentity != outputIdentity {
				t.Fatalf("%s: reservation output identity differs from the surviving output", tc.point)
			}
		})
	}
}

func scanPrefixed(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			names = append(names, filepath.Join(dir, entry.Name()))
		}
	}
	return names
}

// assertCompleteOutput proves one private output is the exact finished
// fixture (Rust assert_complete_output) and returns its local identity.
func assertCompleteOutput(t *testing.T, path string) [32]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open private output: %v", err)
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		t.Fatalf("stat private output: %v", err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private output: %v", err)
	}
	if uint64(st.Size()) != 2*format.PageSize {
		t.Fatalf("private output size %d, want %d", st.Size(), 2*format.PageSize)
	}
	opened, err := bootstrap.Open(bytes[0:format.PageSize], bytes[format.PageSize:2*format.PageSize], uint64(st.Size()), bootstrap.ModeImmutableReader)
	if err != nil {
		t.Fatalf("private output does not reopen: %v", err)
	}
	if opened.Meta.DatabaseID != testFixtureDBID || opened.Meta.TxnID != 1 || opened.Meta.CommitNonce != testFixtureNonce {
		t.Fatal("private output meta differs from the fixture identity")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatalf("fstat private output: %v", err)
	}
	// The output shares its device with the retained directory; the
	// directory inode value is not part of the regular-identity proof.
	directoryIdentity := live.IdentityFromDeviceInode(uint64(stat.Dev), 0)
	identity, err := live.RegularIdentity(file, directoryIdentity)
	if err != nil {
		t.Fatalf("identity of private output: %v", err)
	}
	return reservationIdentityBytes(identity)
}

func assertSelectableReservation(t *testing.T, path string) reservationHeader {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if len(bytes) != reservationFileSize {
		t.Fatalf("reservation size %d, want %d", len(bytes), reservationFileSize)
	}
	selected, err := selectReservation(bytes)
	if err != nil {
		t.Fatalf("reservation does not select: %v", err)
	}
	return selected.header
}
