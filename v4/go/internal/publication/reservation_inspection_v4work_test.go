//go:build v4work && linux

// Reservation inspection after crash artifacts (Rust
// publication/crash_tests.rs): every reservation crash point leaves
// exactly one discoverable authority with the inspected location,
// state, identity binding, creator-only access, and a held operation
// lock; the duplicate and malformed scans refuse with the exact Rust
// classes, and the scans stay allocation-bounded and cancellable.

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestDiscoverAfterReservationCrashPoints proves discover returns the
// exact surviving authority after every reservation crash point
// (Rust reservation_crashes_leave_one_complete_output_and_selectable_
// authority discover arm): the placement, state, inode identity,
// output binding, creator-only access, and the held operation lock.
func TestDiscoverAfterReservationCrashPoints(t *testing.T) {
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

			// One surviving complete private output binds the record.
			privateOutputs := scanPrefixed(t, dir, outputPrefix)
			if len(privateOutputs) != 1 {
				t.Fatalf("private outputs = %d, want 1", len(privateOutputs))
			}
			outputIdentity := assertCompleteOutput(t, privateOutputs[0])

			destination := testBoundDestination(t, dir)
			inspected, err := discoverReservation(destination, nil)
			if err != nil {
				t.Fatalf("discover after %s: %v", tc.point, err)
			}
			defer inspected.Close()

			if tc.canonical {
				if inspected.location != reservationLocationCanonical {
					t.Fatalf("%s: location %d, want canonical", tc.point, inspected.location)
				}
				if _, err := os.Lstat(filepath.Join(dir, "result.v4.readers")); err != nil {
					t.Fatalf("%s: coordination twin missing: %v", tc.point, err)
				}
				privates := scanPrefixed(t, dir, reservationPrefix)
				if len(privates) != 0 {
					t.Fatalf("%s: private reservations = %d, want none", tc.point, len(privates))
				}
			} else {
				if inspected.location != reservationLocationPrivate {
					t.Fatalf("%s: location %d, want private", tc.point, inspected.location)
				}
				privates := scanPrefixed(t, dir, reservationPrefix)
				if len(privates) != 1 {
					t.Fatalf("%s: private reservations = %d, want 1", tc.point, len(privates))
				}
			}

			selected := checkSelectableReservation(t, reservationDiskPath(t, dir, inspected, tc.canonical))
			if tc.stateAny {
				if selected.state != reservationStatePrepared && selected.state != reservationStateMainMayHaveBeenAttempted {
					t.Fatalf("%s: state %d, want Prepared or MainMayHaveBeenAttempted", tc.point, selected.state)
				}
			} else if selected.state != tc.expected {
				t.Fatalf("%s: state %d, want %d", tc.point, selected.state, tc.expected)
			}
			if inspected.header.state != selected.state {
				t.Fatalf("%s: inspected state %d differs from the selected record %d", tc.point, inspected.header.state, selected.state)
			}
			if selected.outputIdentity != outputIdentity {
				t.Fatalf("%s: record output identity differs from the surviving output", tc.point)
			}
			if inspected.access != AccessPolicyCreatorOnly {
				t.Fatalf("%s: access %d, want creator-only", tc.point, inspected.access)
			}
			assertOperationLockHeld(t, inspected)
			if err := inspected.verify(destination); err != nil {
				t.Fatalf("%s: inspected verify: %v", tc.point, err)
			}
		})
	}
}

// reservationDiskPath resolves the on-disk reservation path of the
// inspected location (the private name for a private reservation, the
// coordination twin for a canonical one).
func reservationDiskPath(t *testing.T, dir string, inspected *inspectedReservation, canonical bool) string {
	t.Helper()
	if canonical {
		return filepath.Join(dir, "result.v4.readers")
	}
	return filepath.Join(dir, inspected.name)
}

// TestDiscoverAfterState2WriteSelectsEitherState proves the
// state2_write crash artifact selects either durable phase exactly
// like Rust (the torn write must never refuse discovery).
func TestDiscoverAfterState2WriteSelectsEitherState(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runReservationCrashChild(t, main, "publication.after_reservation_state2_write")

	destination := testBoundDestination(t, dir)
	inspected, err := discoverReservation(destination, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	defer inspected.Close()
	state := inspected.header.state
	if state != reservationStatePrepared && state != reservationStateMainMayHaveBeenAttempted {
		t.Fatalf("state %d, want Prepared or MainMayHaveBeenAttempted", state)
	}
	if err := inspected.verify(destination); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// TestPrivateScanRequiresOneUniqueBoundReservation proves two bound
// private reservations refuse discovery with the Conflict class
// (Rust private_scan_requires_one_unique_bound_reservation).
func TestPrivateScanRequiresOneUniqueBoundReservation(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runReservationCrashChild(t, main, "publication.after_reservation_state1_sync")
	runReservationCrashChild(t, main, "publication.after_reservation_state1_sync")

	destination := testBoundDestination(t, dir)
	_, err := discoverReservation(destination, nil)
	assertProblemCodeDetail(t, err, format.CodeConflict, "multiple bound private publication reservations exist")

	privates := scanPrefixed(t, dir, reservationPrefix)
	if len(privates) != 2 {
		t.Fatalf("private reservations = %d, want 2", len(privates))
	}
}

// TestMalformedCanonicalNotPrivateScanAuthority proves a malformed
// coordination twin refuses with Unresolvable and is never treated as
// private-scan authority (Rust malformed_canonical_reservation_is_not_
// private_scan_authority); the file stays byte-identical.
func TestMalformedCanonicalNotPrivateScanAuthority(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runReservationCrashChild(t, main, "publication.after_reservation_state1_sync")

	coordination := filepath.Join(dir, "result.v4.readers")
	zeros := make([]byte, reservationFileSize)
	if err := os.WriteFile(coordination, zeros, 0o600); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	_, err := discoverReservation(destination, nil)
	assertProblemCodeDetail(t, err, format.CodeUnresolvable, "publication reservation record is not selectable")

	after, err := os.ReadFile(coordination)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(zeros) {
		t.Fatalf("coordination size %d, want %d", len(after), len(zeros))
	}
	for i, b := range after {
		if b != 0 {
			t.Fatalf("coordination byte %d = %02x, want 00", i, b)
		}
	}
}

// TestPrivateScanIsCancellable proves the inspection checkpoints
// honor the caller cancellation token (Rust
// empty_private_scan_is_cancellable_and_allocation_bounded).
func TestPrivateScanIsCancellable(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 32; i++ {
		if err := os.WriteFile(filepath.Join(dir, "foreign-"+string(rune('a'+i%26))+itoa(i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	destination := testBoundDestination(t, dir)
	inspected, err := discoverReservation(destination, nil)
	if err != nil {
		t.Fatalf("discover over foreign entries: %v", err)
	}
	if inspected != nil {
		t.Fatal("discover found a reservation among foreign entries")
	}

	want := errors.New("cancelled")
	_, err = discoverReservation(destination, func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("cancellation error %v, got %v", want, err)
	}
}
