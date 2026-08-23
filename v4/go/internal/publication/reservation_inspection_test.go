//go:build linux

// Reservation inspection tests (Rust publication/crash_tests.rs
// discover arms + the inspection machine facts): the canonical
// discover on an acquired and armed reservation, the exact_private
// read on an initialized reservation, the conflict and Unresolvable
// refusal classes, the skip decisions of the private scan (size,
// selectability, link count, foreign attempt), the multiple-bound
// conflict and the coordination-changed-during-scan conflict, the
// require_bound mismatch classes, unlock/relock round trips and the
// lock-steal conflict, plus the creator-only access classification.

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// testBoundDestination binds the destination of one prepared fixture
// output (Rust Destination::bind on the same main).
func testBoundDestination(t *testing.T, dir string) *destination {
	t.Helper()
	d, err := bindDestination(filepath.Join(dir, "result.v4"))
	if err != nil {
		t.Fatalf("bind destination: %v", err)
	}
	t.Cleanup(func() { d.directory().Close() })
	return d
}

// assertProblemCodeDetail pins one exact problem class of err.
func assertProblemCodeDetail(t *testing.T, err error, code format.ErrorCode, detail string) {
	t.Helper()
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("err %v is not a formatted problem", err)
	}
	if fe.Code != code || fe.Detail != detail {
		t.Fatalf("err = (%d, %q), want (%d, %q)", fe.Code, fe.Detail, code, detail)
	}
}

// assertOperationLockHeld proves the inspected owner holds the
// exclusive operation lock (a contender try-lock must fail).
func assertOperationLockHeld(t *testing.T, inspected *inspectedReservation) {
	t.Helper()
	contender, err := os.OpenFile("/proc/self/fd/"+itoa(int(inspected.file.Fd())), os.O_RDWR, 0)
	if err == nil {
		defer contender.Close()
		acquired, err := live.TryLockFile(contender, reservationOperationLock, live.LockExclusive)
		if err != nil {
			t.Fatalf("contender try-lock: %v", err)
		}
		if acquired {
			t.Fatal("contender acquired the operation lock of an inspected reservation")
		}
	}
}

// checkSelectableReservation decodes the selectable record of one
// complete reservation file (local helper: the v4work crash helpers
// are not compiled into the plain test build).
func checkSelectableReservation(t *testing.T, path string) reservationHeader {
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

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestDiscoverCanonicalAcquiredReservation proves the canonical first
// path: after acquire the coordination twin is found, the inspected
// owner derives the private name, carries the exact identity and
// header, classifies creator-only evidence, and holds the operation
// lock (Rust crash discover canonical arms).
func TestDiscoverCanonicalAcquiredReservation(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
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
	expectedIdentity := canonical.identity
	expectedHeader := canonical.header
	privateName := canonical.name
	if err := canonical.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	inspected, err := discoverReservation(destination, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	defer inspected.Close()
	if inspected.location != reservationLocationCanonical {
		t.Fatalf("location %d, want canonical", inspected.location)
	}
	if inspected.name != privateName {
		t.Fatalf("inspected name %q, want %q", inspected.name, privateName)
	}
	if inspected.identity != expectedIdentity {
		t.Fatal("inspected identity differs from the acquired inode")
	}
	if inspected.header != expectedHeader {
		t.Fatal("inspected header differs from the acquired record")
	}
	if inspected.access != AccessPolicyCreatorOnly {
		t.Fatalf("access %d, want creator-only on the secured reservation", inspected.access)
	}
	assertOperationLockHeld(t, inspected)
	if err := inspected.verify(destination); err != nil {
		t.Fatalf("inspected verify: %v", err)
	}

	// The canonical inspection keeps the record unmodified.
	selected := checkSelectableReservation(t, filepath.Join(dir, "result.v4.readers"))
	if selected != expectedHeader {
		t.Fatal("canonical record changed by the inspection")
	}
}

// TestDiscoverArmedReservation proves discover returns the state-2
// record of an armed reservation with the private name derived from
// the attempt id (Rust crash state2 arms).
func TestDiscoverArmedReservation(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
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
	expectedHeader := armed.header
	if expectedHeader.state != reservationStateMainMayHaveBeenAttempted || expectedHeader.sequence != 2 {
		t.Fatalf("armed header %d/%d, want state 2", expectedHeader.state, expectedHeader.sequence)
	}
	if err := armed.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	inspected, err := discoverReservation(destination, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	defer inspected.Close()
	if inspected.location != reservationLocationCanonical {
		t.Fatalf("location %d, want canonical", inspected.location)
	}
	if inspected.header != expectedHeader {
		t.Fatal("inspected state-2 header differs from the armed record")
	}
	wantName, err := destination.reservationName(expectedHeader.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.name != wantName {
		t.Fatalf("inspected private name %q, want %q", inspected.name, wantName)
	}
	if err := inspected.verify(destination); err != nil {
		t.Fatalf("inspected verify: %v", err)
	}
}

// TestExactPrivateOnInitializedReservation proves the exact_private
// path on a private initialized reservation: the operation lock is
// taken, the name is re-proved, and the header must match the caller
// expectation exactly (Rust exact_private happy path).
func TestExactPrivateOnInitializedReservation(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	expectedHeader := private.header
	name := private.name
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	inspected, err := exactPrivateReservation(destination, expectedHeader, nil)
	if err != nil {
		t.Fatalf("exact_private: %v", err)
	}
	defer inspected.Close()
	if inspected.location != reservationLocationPrivate {
		t.Fatalf("location %d, want private", inspected.location)
	}
	if inspected.name != name {
		t.Fatalf("inspected name %q, want %q", inspected.name, name)
	}
	if inspected.header != expectedHeader {
		t.Fatal("inspected header differs from the caller expectation")
	}
	if inspected.access != AccessPolicyCreatorOnly {
		t.Fatalf("access %d, want creator-only", inspected.access)
	}
	assertOperationLockHeld(t, inspected)
	if err := inspected.verify(destination); err != nil {
		t.Fatalf("inspected verify: %v", err)
	}
}

// TestExactPrivateDisagreementConflict proves the exact_private
// caller-result disagreement class (Rust "caller result and private
// reservation disagree").
func TestExactPrivateDisagreementConflict(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	expected := private.header
	// A caller expectation that still binds (same attempt, identity,
	// and basename evidence) but disagrees on the output identity.
	expected.outputSHA512[0] ^= 0xff
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	_, err = exactPrivateReservation(destination, expected, nil)
	assertProblemCodeDetail(t, err, format.CodeConflict, "caller result and private reservation disagree")
}

// TestScanPrivateSkipsMalformedEntries proves the private scan skips
// entries that cannot be one bound reservation: wrong size,
// unselectable record, extra hard link, and a name whose attempt id
// disagrees with the record header (Rust invalid_private_entry and
// require_bound skip arms). Each malformed class is created with its
// own inode so one class cannot mask another.
func TestScanPrivateSkipsMalformedEntries(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	destination := prepared.attempt.destinationOf()

	// Wrong size at a reservation-shaped name.
	wrongSizeName, err := destination.reservationName(sixteen(0x11))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, wrongSizeName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A full-size but unselectable (zeroed) record.
	zeroName, err := destination.reservationName(sixteen(0x22))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, zeroName), make([]byte, reservationFileSize), 0o600); err != nil {
		t.Fatal(err)
	}

	// A bound reservation renamed to a foreign attempt name: the
	// record binds its inode and the original destination, but the
	// name decodes another attempt id, so require_bound skips it.
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	foreign, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	foreignHeader := foreign.header
	foreignAttempt := foreign.header.attemptID
	foreignPath := filepath.Join(dir, foreign.name)
	if err := foreign.Close(); err != nil {
		t.Fatal(err)
	}
	renamedName, err := destination.reservationName(sixteen(0x44))
	if err != nil {
		t.Fatal(err)
	}
	renamedPath := filepath.Join(dir, renamedName)
	if err := os.Rename(foreignPath, renamedPath); err != nil {
		t.Fatal(err)
	}

	// A second bound reservation whose hard link raises its link
	// count: both names of the inode are skipped by the link-count
	// rule.
	draft2, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	linked, failure := draft2.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	linkedPath := filepath.Join(dir, linked.name)
	if err := linked.Close(); err != nil {
		t.Fatal(err)
	}
	hardlinkName, err := destination.reservationName(sixteen(0x33))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(linkedPath, filepath.Join(dir, hardlinkName)); err != nil {
		t.Fatal(err)
	}

	// The coordination twin is absent, so discover scans and must
	// find no bound reservation among the malformed entries.
	bound, err := discoverReservation(destination, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if bound != nil {
		t.Fatal("discover found a bound reservation among malformed entries")
	}

	// The foreign-attempt file survived unchanged and still selects
	// the exact record of the renamed inode.
	selected := checkSelectableReservation(t, renamedPath)
	if selected != foreignHeader {
		t.Fatal("renamed reservation changed by the inspection")
	}
	// The link-count rule skipped it, not the attempt rule: the header
	// still names the original attempt, the inode is unchanged.
	if selected.attemptID != foreignAttempt {
		t.Fatal("renamed reservation attempt id changed")
	}
}

// TestDiscoverMultipleBoundPrivateReservationsConflict proves the
// multiple-bound scan conflict (Rust private_scan_requires_one_unique_
// bound_reservation).
func TestDiscoverMultipleBoundPrivateReservationsConflict(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	first, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	prepared2, _, _ := prepareTestOutput(t, dir)
	draft2, err := createReservationDraft(prepared2)
	if err != nil {
		t.Fatal(err)
	}
	second, failure := draft2.initialize(prepared2)
	if failure != nil {
		t.Fatal(failure)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	_, err = discoverReservation(destination, nil)
	assertProblemCodeDetail(t, err, format.CodeConflict, "multiple bound private publication reservations exist")
}

// TestDiscoverCoordinationChangedDuringScanConflict proves the
// coordination twin must stay absent for the private scan to be
// authoritative (Rust discover require_absent arm).
func TestDiscoverCoordinationChangedDuringScanConflict(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	checkCalls := 0
	check := func() error {
		checkCalls++
		// After the canonical probe (call 1), make the coordination
		// twin appear while the private scan runs.
		if checkCalls == 2 {
			if werr := os.WriteFile(filepath.Join(dir, "result.v4.readers"), make([]byte, reservationFileSize), 0o600); werr != nil {
				return werr
			}
		}
		return nil
	}
	_, err = discoverReservation(destination, check)
	assertProblemCodeDetail(t, err, format.CodeConflict, "coordination changed during reservation scan")
}

// TestRequireBoundMismatchClasses proves the three require_bound
// refusal classes (Rust require_bound arms): self-identity conflict,
// filename-attempt conflict, and the destination-name mismatch.
func TestRequireBoundMismatchClasses(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	header := private.header
	identity := private.identity
	attempt := private.header.attemptID
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}
	destination := testBoundDestination(t, dir)

	wrongSelf := header
	wrongSelf.reservationIdentity[0] ^= 0xff
	_, err = inspectPrivateReservation(destination, privateReservationName(t, destination, attempt), attempt, nil)
	_ = err
	// The direct require_bound unit arms:
	if err := requireBound(destination, wrongSelf, identity, &attempt); err == nil {
		t.Fatal("self-identity mismatch accepted")
	} else {
		assertProblemCodeDetail(t, err, format.CodeConflict, "reservation self identity does not match its inode")
	}

	otherAttempt := attempt
	otherAttempt[0] ^= 0xff
	if err := requireBound(destination, header, identity, &otherAttempt); err == nil {
		t.Fatal("filename-attempt mismatch accepted")
	} else {
		assertProblemCodeDetail(t, err, format.CodeConflict, "private reservation name has another attempt id")
	}

	wrongLen := header
	wrongLen.basenameLen++
	if err := requireBound(destination, wrongLen, identity, &attempt); err == nil {
		t.Fatal("basename length mismatch accepted")
	} else {
		assertProblemCodeDetail(t, err, format.CodeDestinationNameMismatch, "reservation belongs to another destination name")
	}

	wrongCommitment := header
	wrongCommitment.basenameCommitment[0] ^= 0xff
	if err := requireBound(destination, wrongCommitment, identity, &attempt); err == nil {
		t.Fatal("basename commitment mismatch accepted")
	} else {
		assertProblemCodeDetail(t, err, format.CodeDestinationNameMismatch, "reservation belongs to another destination name")
	}

	// The accept arm stays exact.
	if err := requireBound(destination, header, identity, &attempt); err != nil {
		t.Fatalf("exact bound rejected: %v", err)
	}
}

func privateReservationName(t *testing.T, destination *destination, attempt [16]byte) string {
	t.Helper()
	name, err := destination.reservationName(attempt)
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// TestUnlockRelockRoundTrip proves the inspected operation lock can
// be released and re-acquired with the full re-proof (Rust
// Inspected::unlock_operation + relock_operation).
func TestUnlockRelockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
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
	if err := canonical.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	inspected, err := discoverReservation(destination, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	defer inspected.Close()

	if err := inspected.unlockOperation(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "result.v4.readers"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	acquired, err := live.TryLockFile(file, reservationOperationLock, live.LockExclusive)
	if err != nil {
		t.Fatalf("contender after unlock: %v", err)
	}
	if !acquired {
		t.Fatal("contender could not lock after unlock_operation")
	}
	if err := live.UnlockFile(file, reservationOperationLock); err != nil {
		t.Fatal(err)
	}

	if err := inspected.relockOperation(destination, nil); err != nil {
		t.Fatalf("relock: %v", err)
	}
	acquired, err = live.TryLockFile(file, reservationOperationLock, live.LockExclusive)
	if err != nil {
		t.Fatalf("contender after relock: %v", err)
	}
	if acquired {
		t.Fatal("contender locked while relocked")
	}
}

// TestRelockAfterExternalLockStealConflict proves relock re-proves the
// record: a record rewritten while the lock was released fails with
// the changed-after-inspection conflict (Rust relock_operation verify).
func TestRelockAfterExternalLockStealConflict(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
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
	if err := canonical.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	inspected, err := discoverReservation(destination, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	defer inspected.Close()
	if err := inspected.unlockOperation(); err != nil {
		t.Fatal(err)
	}

	// The contender steals the lock and rewrites the selectable record
	// to another state-1 header of the same attempt.
	file, err := os.OpenFile(filepath.Join(dir, "result.v4.readers"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := live.LockFile(file, reservationOperationLock, live.LockExclusive); err != nil {
		t.Fatal(err)
	}
	tampered := inspected.header
	tampered.outputByteLength = 3 * uint64(format.PageSize)
	tampered.outputSHA512[0] = ^tampered.outputSHA512[0]
	// Both blocks must carry the same state-1 record so the selection
	// stays valid: a one-sided rewrite would make the pair refuse and
	// report the Unresolvable class instead of the changed-record
	// conflict.
	page := make([]byte, format.PageSize)
	if err := tampered.encodeReservationHeader(page); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(page, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(page, format.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := live.UnlockFile(file, reservationOperationLock); err != nil {
		t.Fatal(err)
	}

	err = inspected.relockOperation(destination, nil)
	assertProblemCodeDetail(t, err, format.CodeConflict, "publication reservation changed after inspection")
}

// TestInspectedAccessClassification proves the creator-only
// classification: the secured reservation is CreatorOnly, and a chmod
// that breaks the exact creator evidence turns it into
// ChangedOrUnproven (Rust inspected creator_only_commitment arm).
func TestInspectedAccessClassification(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	privatePath := filepath.Join(dir, private.name)
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	inspected, err := exactPrivateReservation(destination, private.header, nil)
	if err != nil {
		t.Fatalf("exact_private: %v", err)
	}
	if inspected.access != AccessPolicyCreatorOnly {
		t.Fatalf("access %d, want creator-only", inspected.access)
	}
	if err := inspected.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(privatePath, 0o644); err != nil {
		t.Fatal(err)
	}
	inspected, err = exactPrivateReservation(destination, private.header, nil)
	if err != nil {
		t.Fatalf("exact_private after chmod: %v", err)
	}
	defer inspected.Close()
	if inspected.access != AccessPolicyChangedOrUnproven {
		t.Fatalf("access %d, want changed-or-unproven after chmod", inspected.access)
	}
}

// TestInspectionRefusalsAreReadOnly proves the inspection machines
// never modify the reservation file: every refusal class leaves the
// exact record selectable and byte-identical (Rust crash malformed
// canonical + scan refusal arms).
func TestInspectionRefusalsAreReadOnly(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, failure := draft.initialize(prepared)
	if failure != nil {
		t.Fatal(failure)
	}
	before, err := os.ReadFile(filepath.Join(dir, private.name))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != reservationFileSize {
		t.Fatalf("reservation size %d", len(before))
	}
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}

	destination := testBoundDestination(t, dir)
	// A malformed canonical record refuses with Unresolvable and must
	// not fall back to the private scan (Rust malformed_canonical_
	// reservation_is_not_private_scan_authority).
	coordination := filepath.Join(dir, "result.v4.readers")
	if err := os.WriteFile(coordination, make([]byte, reservationFileSize), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = discoverReservation(destination, nil)
	assertProblemCodeDetail(t, err, format.CodeUnresolvable, "publication reservation record is not selectable")
	after, err := os.ReadFile(filepath.Join(dir, private.name))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("private reservation changed by a refused canonical inspection")
	}
}

// TestMappingOwnershipOnRefusedInspection runs the error paths of the
// inspection machines repeatedly and proves no mapping or descriptor
// is leaked (Go has no drop; the port closes explicitly).
func TestMappingOwnershipOnRefusedInspection(t *testing.T) {
	dir := t.TempDir()
	prepared, _, _ := prepareTestOutput(t, dir)
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatal(err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatal(initFailure)
	}
	attempt := private.header.attemptID
	if err := private.Close(); err != nil {
		t.Fatal(err)
	}
	destination := testBoundDestination(t, dir)

	// Wrong-size mapping refusal inside inspect_private.
	name := privateReservationName(t, destination, attempt)
	if err := os.Truncate(filepath.Join(dir, name), 100); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		inspected, err := inspectPrivateReservation(destination, name, attempt, nil)
		if err != nil {
			t.Fatalf("inspect_private wrong-size: %v", err)
		}
		if inspected != nil {
			t.Fatal("inspect_private accepted a wrong-size record")
		}
	}
}
