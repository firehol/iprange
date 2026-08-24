//go:build v4work && linux

// Residue tests (Rust publication/residue_tests.rs + residue/linux.rs
// linux_tests): absence and private-reservation reporting, the
// selectable coordination refusal, durable and exact removal of
// malformed canonical residue, hashed-but-unchanged arbitrary mains,
// the readable v4 main tuple, the changed-or-newly-selectable
// coordination refusals, the cancellation and live-sidecar refusals,
// the hard-link refusal, and the retain-and-retry authority of an
// incomplete removal.

package publication

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// residueCoordinationPath builds the canonical coordination name of
// one main (Rust coordination_path).
func residueCoordinationPath(main string) string {
	return main + ".readers"
}

// writeTestResidueSidecarReady writes one ready live reader-table
// sidecar at path with the fixture identities and capacity 2 (Rust
// Sidecar::create + publish_ready; the header page layout matches
// live/header.go).
func writeTestResidueSidecarReady(t *testing.T, path string) {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[0:8], "IPRDRS4\x00")
	format.PutU16(page[8:10], 68)
	format.PutU16(page[10:12], 16)
	format.PutU32(page[12:16], 1) // state ready
	format.PutU32(page[16:20], 2) // capacity
	copy(page[32:48], []byte{1})  // database id
	copy(page[48:64], []byte{2})  // sidecar id
	crc, ok := format.CRC32CWithZeroed(page, 64, 4)
	if !ok {
		t.Fatal("sidecar header checksum field invalid")
	}
	format.PutU32(page[64:68], crc)
	file := make([]byte, format.PageSize+2*16)
	copy(file, page)
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

// TestResidueAbsenceAndOnePrivateReservationAreReportedWithoutAHandle
// ports absence_and_one_private_reservation_are_reported_without_a_
// handle.
func TestResidueAbsenceAndOnePrivateReservationAreReportedWithoutAHandle(t *testing.T) {
	empty := t.TempDir()
	emptyMain := filepath.Join(empty, "result.v4")
	before := countProcessFds(t)
	inspected, err := inspectResidue(emptyMain, noopCheck)
	if err != nil {
		t.Fatalf("inspect empty: %v", err)
	}
	if inspected.coordination != residueCoordinationAbsent {
		t.Fatalf("coordination = %v, want absent", inspected.coordination)
	}
	if inspected.coordinationIdentity != nil {
		t.Fatal("coordination identity present on absence")
	}
	if inspected.publication != nil {
		t.Fatal("publication present on absence")
	}
	if inspected.handle != nil {
		t.Fatal("handle present on absence")
	}

	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	inspected, err = inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect private: %v", err)
	}
	if inspected.publication == nil {
		t.Fatal("publication missing for a private reservation")
	}
	if inspected.coordination != residueCoordinationAbsent {
		t.Fatalf("coordination = %v, want absent", inspected.coordination)
	}
	if inspected.handle != nil {
		t.Fatal("handle present for a private reservation")
	}
	if inspected.publication.Attempt.DatabaseID != testFixtureDBID {
		t.Fatalf("database id %x, want fixture", inspected.publication.Attempt.DatabaseID)
	}
	if inspected.publication.Attempt.TransactionID != 1 {
		t.Fatalf("transaction id %d, want 1", inspected.publication.Attempt.TransactionID)
	}
	if inspected.publication.MainNamespaceMayHaveBeenAttempted {
		t.Fatal("main namespace may have been attempted, want false")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("inspection cycles left %d descriptors open", after-before)
	}
}

// TestResidueSelectableCanonicalReservationIsReconstructedButNot
// Removed ports
// selectable_canonical_reservation_is_reconstructed_but_not_removed.
func TestResidueSelectableCanonicalReservationIsReconstructedButNotRemoved(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[2])
	before := countProcessFds(t)
	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspected.coordination != residueCoordinationPublicationReservation {
		t.Fatalf("coordination = %v, want publication reservation", inspected.coordination)
	}
	if inspected.publication == nil {
		t.Fatal("publication missing for a selectable reservation")
	}
	if inspected.handle == nil {
		t.Fatal("handle missing for a selectable reservation")
	}

	_, err = removeResidue(*inspected.handle, noopCheck)
	if codeOf(err) != format.CodeConflict {
		t.Fatalf("remove problem = %v, want conflict", err)
	}
	if _, err := os.Lstat(residueCoordinationPath(main)); err != nil {
		t.Fatal("coordination disappeared")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("selectable cycle left %d descriptors open", after-before)
	}
}

// TestResidueMalformedCanonicalResidueIsRemovedDurablyAndExactly
// ports malformed_canonical_residue_is_removed_durably_and_exactly.
func TestResidueMalformedCanonicalResidueIsRemovedDurablyAndExactly(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := residueCoordinationPath(main)
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before := countProcessFds(t)

	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspected.coordination != residueCoordinationUnselectable {
		t.Fatalf("coordination = %v, want unselectable", inspected.coordination)
	}
	if inspected.publication != nil {
		t.Fatal("publication present for unselectable residue")
	}
	result, err := removeResidue(*inspected.handle, noopCheck)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("successful removal left %d descriptors open", after-before)
	}
	if result.cleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", result.cleanupState())
	}
	if result.laterCoordination != residueCoordinationAbsent {
		t.Fatalf("later coordination = %v, want absent", result.laterCoordination)
	}
	if result.main != nil {
		t.Fatal("main evidence present, want none")
	}
	if _, err := os.Lstat(coordination); err == nil {
		t.Fatal("coordination still exists")
	}
	if _, err := os.Lstat(main); err == nil {
		t.Fatal("main still exists")
	}
}

// TestResidueRemovalHashesButNeverChangesAnArbitraryMain ports
// removal_hashes_but_never_changes_an_arbitrary_main.
func TestResidueRemovalHashesButNeverChangesAnArbitraryMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	expected := []byte("arbitrary previous bytes")
	if err := os.WriteFile(main, expected, 0o600); err != nil {
		t.Fatalf("write main: %v", err)
	}
	if err := os.WriteFile(residueCoordinationPath(main), []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before := countProcessFds(t)

	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	result, err := removeResidue(*inspected.handle, noopCheck)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	bytes, err := os.ReadFile(main)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if string(bytes) != string(expected) {
		t.Fatalf("main changed to %q", bytes)
	}
	evidence := result.main
	if evidence == nil {
		t.Fatal("main evidence missing")
	}
	if evidence.content != residueMainContentOther {
		t.Fatalf("content = %v, want other", evidence.content)
	}
	if evidence.tuple != nil {
		t.Fatal("tuple present for arbitrary main")
	}
	if evidence.digest.byteLength != uint64(len(expected)) {
		t.Fatalf("digest length = %d, want %d", evidence.digest.byteLength, len(expected))
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("arbitrary-main removal left %d descriptors open", after-before)
	}
}

// TestResidueRemovalReportsAReadableV4MainTupleWithoutValidatingIts
// Graph ports
// removal_reports_a_readable_v4_main_tuple_without_validating_its_
// graph.
func TestResidueRemovalReportsAReadableV4MainTupleWithoutValidatingItsGraph(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	artifacts := inspectResolverArtifacts(t, dir, main)
	if len(artifacts.privateOutputs) == 0 {
		t.Fatal("no private output fixture")
	}
	if err := copyFile(artifacts.privateOutputs[0], main); err != nil {
		t.Fatalf("copy private output to main: %v", err)
	}
	if err := os.WriteFile(residueCoordinationPath(main), []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before := countProcessFds(t)

	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	result, err := removeResidue(*inspected.handle, noopCheck)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	evidence := result.main
	if evidence == nil {
		t.Fatal("main evidence missing")
	}
	if evidence.content != residueMainContentV4 {
		t.Fatalf("content = %v, want v4", evidence.content)
	}
	if evidence.tuple == nil {
		t.Fatal("tuple missing")
	}
	if evidence.tuple.databaseID != testFixtureDBID {
		t.Fatalf("tuple database id %x, want fixture", evidence.tuple.databaseID)
	}
	if evidence.tuple.transactionID != 1 {
		t.Fatalf("tuple transaction id %d, want 1", evidence.tuple.transactionID)
	}
	if _, err := os.Lstat(main); err != nil {
		t.Fatal("main disappeared")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("v4-tuple removal left %d descriptors open", after-before)
	}
}

// TestResidueChangedOrNewlySelectableCoordinationIsNeverRemoved ports
// changed_or_newly_selectable_coordination_is_never_removed.
func TestResidueChangedOrNewlySelectableCoordinationIsNeverRemoved(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := residueCoordinationPath(main)
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before := countProcessFds(t)
	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if err := os.Remove(coordination); err != nil {
		t.Fatalf("remove coordination: %v", err)
	}
	if err := os.WriteFile(coordination, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("rewrite coordination: %v", err)
	}

	_, err = removeResidue(*inspected.handle, noopCheck)
	if codeOf(err) != format.CodeCleanupConflict {
		t.Fatalf("remove problem = %v, want cleanup conflict", err)
	}
	if bytes, readErr := os.ReadFile(coordination); readErr != nil || string(bytes) != "replacement" {
		t.Fatalf("coordination changed: err=%v bytes=%q", readErr, bytes)
	}

	source := t.TempDir()
	sourceMain := filepath.Join(source, "result.v4")
	runAttemptCrashChild(t, sourceMain, "publish", resolverPreMainPoints[2])
	selectedBytes, err := os.ReadFile(residueCoordinationPath(sourceMain))
	if err != nil {
		t.Fatalf("read selectable coordination: %v", err)
	}
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	inspected, err = inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect again: %v", err)
	}
	if err := os.WriteFile(coordination, selectedBytes, 0o600); err != nil {
		t.Fatalf("write selected coordination: %v", err)
	}

	_, err = removeResidue(*inspected.handle, noopCheck)
	if codeOf(err) != format.CodeConflict {
		t.Fatalf("remove problem = %v, want conflict", err)
	}
	if _, err := os.Lstat(coordination); err != nil {
		t.Fatal("coordination disappeared")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("changed/selectable cycles left %d descriptors open", after-before)
	}
}

// TestResidueCancellationAndAReadyLiveSidecarChangeNothing ports
// cancellation_and_a_ready_live_sidecar_change_nothing.
func TestResidueCancellationAndAReadyLiveSidecarChangeNothing(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := residueCoordinationPath(main)
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before := countProcessFds(t)
	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	cancellation := &resolverTestCancellation{}
	cancellation.cancelled.Store(true)
	_, err = removeResidue(*inspected.handle, cancellation.check)
	if codeOf(err) != format.CodeCancelled {
		t.Fatalf("remove problem = %v, want cancelled", err)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("failed removal left %d descriptors open", after-before)
	}
	if bytes, readErr := os.ReadFile(coordination); readErr != nil || string(bytes) != "malformed" {
		t.Fatalf("coordination changed: err=%v bytes=%q", readErr, bytes)
	}

	if err := os.Remove(coordination); err != nil {
		t.Fatalf("remove coordination: %v", err)
	}
	writeTestResidueSidecarReady(t, coordination)
	inspected, err = inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect sidecar: %v", err)
	}
	if inspected.coordination != residueCoordinationLiveSidecar {
		t.Fatalf("coordination = %v, want live sidecar", inspected.coordination)
	}
	_, err = removeResidue(*inspected.handle, noopCheck)
	if codeOf(err) != format.CodeConflict {
		t.Fatalf("remove problem = %v, want conflict", err)
	}
	if _, err := os.Lstat(coordination); err != nil {
		t.Fatal("coordination disappeared")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("cancellation/sidecar cycles left %d descriptors open", after-before)
	}
}

// TestResidueHardLinkedCoordinationIsRejectedDuringInspection ports
// hard_linked_coordination_is_rejected_during_inspection.
func TestResidueHardLinkedCoordinationIsRejectedDuringInspection(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := residueCoordinationPath(main)
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	if err := os.Link(coordination, filepath.Join(dir, "alias")); err != nil {
		t.Fatalf("hard link: %v", err)
	}
	before := countProcessFds(t)

	_, err := inspectResidue(main, noopCheck)
	if codeOf(err) != format.CodeConflict {
		t.Fatalf("inspect problem = %v, want conflict", err)
	}
	if _, err := os.Lstat(coordination); err != nil {
		t.Fatal("coordination disappeared")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("hard-link inspection left %d descriptors open", after-before)
	}
}

// TestResidueIncompleteRemovalRetainsExactAuthorityForRetry ports
// residue/linux.rs incomplete_removal_retains_exact_authority_for_
// retry.
func TestResidueIncompleteRemovalRetainsExactAuthorityForRetry(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := residueCoordinationPath(main)
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before := countProcessFds(t)

	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	handle := inspected.handle
	if handle == nil {
		t.Fatal("handle missing")
	}
	if err := verifyCoordinationResidue(handle); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := lockOperationFile(handle.coordination, noopCheck); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if err := rejectSelectableResidue(handle.coordination); err != nil {
		t.Fatalf("reject selectable: %v", err)
	}
	mainGuard, err := inspectMainResidue(handle.destination, noopCheck)
	if err != nil {
		t.Fatalf("inspect main: %v", err)
	}
	retired, err := retireResidueCoordination(handle.destination, handle.coordination, handle.coordinationIdentity)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if retired.cause != nil {
		t.Fatalf("retire cause: %v", retired.cause)
	}
	handle.retired = &retiredResidue{
		main:         mainGuard,
		housekeeping: retired.housekeeping,
		visible:      retired.visible,
	}

	incomplete := incompleteResidue(*handle, cleanupConflictProblem("injected directory synchronization failure"))
	if incomplete.coordinationCleanup != CoordinationCleanupCleanupGuard {
		t.Fatalf("coordination cleanup = %v, want cleanup guard", incomplete.coordinationCleanup)
	}
	if incomplete.handle == nil {
		t.Fatal("retained handle missing")
	}
	if _, err := os.Lstat(coordination); err == nil {
		t.Fatal("coordination still exists")
	}

	completed, err := removeResidue(*incomplete.handle, noopCheck)
	if err != nil {
		t.Fatalf("retry remove: %v", err)
	}
	if completed.cause != nil {
		t.Fatalf("retry cause: %v", completed.cause)
	}
	if completed.handle != nil {
		t.Fatal("handle retained on success")
	}
	if completed.coordinationCleanup != CoordinationCleanupNone {
		t.Fatalf("coordination cleanup = %v, want none", completed.coordinationCleanup)
	}
	if completed.laterCoordination != residueCoordinationAbsent {
		t.Fatalf("later coordination = %v, want absent", completed.laterCoordination)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("incomplete-retry cycle left %d descriptors open", after-before)
	}
}

// TestResidueRetryCancellationReleasesRetainedMain ports the
// cancellation drop of a retired handle: a cancelled retry terminal
// consumes the retained authority exactly like the Rust handle drop,
// closing the retired main guard, the coordination descriptor, and
// the destination directory.
func TestResidueRetryCancellationReleasesRetainedMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	coordination := residueCoordinationPath(main)
	if err := os.WriteFile(coordination, []byte("malformed"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	if err := os.WriteFile(main, []byte("arbitrary previous bytes"), 0o600); err != nil {
		t.Fatalf("write main: %v", err)
	}
	before := countProcessFds(t)

	inspected, err := inspectResidue(main, noopCheck)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	handle := inspected.handle
	if handle == nil {
		t.Fatal("handle missing")
	}
	if err := verifyCoordinationResidue(handle); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := lockOperationFile(handle.coordination, noopCheck); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if err := rejectSelectableResidue(handle.coordination); err != nil {
		t.Fatalf("reject selectable: %v", err)
	}
	mainGuard, err := inspectMainResidue(handle.destination, noopCheck)
	if err != nil {
		t.Fatalf("inspect main: %v", err)
	}
	retired, err := retireResidueCoordination(handle.destination, handle.coordination, handle.coordinationIdentity)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if retired.cause != nil {
		t.Fatalf("retire cause: %v", retired.cause)
	}
	handle.retired = &retiredResidue{
		main:         mainGuard,
		housekeeping: retired.housekeeping,
		visible:      retired.visible,
	}

	cancellation := &resolverTestCancellation{}
	cancellation.cancelled.Store(true)
	_, err = removeResidue(*handle, cancellation.check)
	if codeOf(err) != format.CodeCancelled {
		t.Fatalf("retry problem = %v, want cancelled", err)
	}
	if _, err := os.Lstat(coordination); err == nil {
		t.Fatal("coordination still exists")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("cancelled retry left %d descriptors open", after-before)
	}
}
