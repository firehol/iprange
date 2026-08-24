//go:build v4work && linux

// Maintenance tests (Rust publication/maintenance_tests.rs): stable
// exact-name listing with optional content evidence, exact removal
// of complete/partial/absent outputs and reservations, the wrong
// directory identity, content and header-binding refusals, the
// cancellation/stop/sink control surface, and the Windows
// housekeeping refusals.

package publication

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// maintenanceTestCompleteOutput builds one complete private
// publication output and returns its path and attempt identity (Rust
// complete_output fixture).
func maintenanceTestCompleteOutput(t *testing.T, dir, mainName string) (string, [16]byte) {
	t.Helper()
	prepared, _ := attemptTestPrepared(t, dir, mainName)
	attempt := prepared.attempt.attemptIDOf()
	// The private output name derives from the attempt identity; no
	// second destination bind is needed.
	name, err := privateName(outputPrefix, attempt)
	if err != nil {
		t.Fatalf("output name: %v", err)
	}
	// Close the fixture output, its lifetime lock, and the bound
	// destination directory exactly where the Rust complete_output
	// fixture drops the PreparedOutput; the file itself stays at its
	// private name.
	prepared.attempt.destinationOf().directory().Close()
	if err := prepared.Close(); err != nil {
		t.Fatalf("close fixture output: %v", err)
	}
	return filepath.Join(dir, name), attempt
}

// maintenanceTestListTemps lists the abandoned publication temps of
// one directory (Rust listed).
func maintenanceTestListTemps(t *testing.T, dir string) []abandonedPublicationTempEntry {
	t.Helper()
	var entries []abandonedPublicationTempEntry
	summary, err := listAbandonedPublicationTemps(dir, noopCheck, func(entry *abandonedPublicationTempEntry) error {
		entries = append(entries, *entry)
		return nil
	})
	if err != nil {
		t.Fatalf("list temps: %v", err)
	}
	if summary.entries != uint64(len(entries)) {
		t.Fatalf("summary entries %d, want %d", summary.entries, len(entries))
	}
	return entries
}

// maintenanceTestListReservations lists the abandoned reservation
// artifacts of one directory (Rust listed_reservations).
func maintenanceTestListReservations(t *testing.T, dir string) []abandonedReservationEntry {
	t.Helper()
	var entries []abandonedReservationEntry
	summary, err := listAbandonedReservationArtifacts(dir, noopCheck, func(entry *abandonedReservationEntry) error {
		entries = append(entries, *entry)
		return nil
	})
	if err != nil {
		t.Fatalf("list reservations: %v", err)
	}
	if summary.entries != uint64(len(entries)) {
		t.Fatalf("summary entries %d, want %d", summary.entries, len(entries))
	}
	return entries
}

// maintenanceTestFindEntry returns one listed entry by attempt id
// (Rust entry/reservation_entry).
func maintenanceTestFindEntry[T any](t *testing.T, entries []T, attempt [16]byte, attemptOf func(entry T) [16]byte) T {
	t.Helper()
	for _, entry := range entries {
		if attemptOf(entry) == attempt {
			return entry
		}
	}
	t.Fatalf("listed entry %x missing", attempt)
	var zero T
	return zero
}

func maintenanceTempAttempt(entry abandonedPublicationTempEntry) [16]byte    { return entry.attempt }
func maintenanceReservationAttempt(entry abandonedReservationEntry) [16]byte { return entry.attempt }

// TestMaintenanceListingReportsOnlyStableExactNamesAndOptionalContent
// Evidence ports
// listing_reports_only_stable_exact_names_and_optional_content_
// evidence.
func TestMaintenanceListingReportsOnlyStableExactNamesAndOptionalContentEvidence(t *testing.T) {
	dir := t.TempDir()
	completePath, completeAttempt := maintenanceTestCompleteOutput(t, dir, "result.v4")
	if _, err := os.Lstat(completePath); err != nil {
		t.Fatalf("complete output missing: %v", err)
	}
	partialID := [16]byte{2}
	if err := os.WriteFile(filepath.Join(dir, maintenanceTestName(outputPrefix, partialID)), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".iprange-publish-NOT-AN-ATTEMPT.tmp"), []byte("foreign"), 0o600); err != nil {
		t.Fatalf("write foreign: %v", err)
	}

	entries := maintenanceTestListTemps(t, dir)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	completeEntry := maintenanceTestFindEntry(t, entries, completeAttempt, maintenanceTempAttempt)
	if completeEntry.tuple == nil {
		t.Fatal("complete entry tuple missing")
	}
	if completeEntry.tuple.databaseID != testFixtureDBID {
		t.Fatalf("complete tuple database id %x, want fixture", completeEntry.tuple.databaseID)
	}
	if completeEntry.tuple.transactionID != 1 {
		t.Fatalf("complete tuple transaction id %d, want 1", completeEntry.tuple.transactionID)
	}
	if completeEntry.tuple.commitNonce != testFixtureNonce {
		t.Fatalf("complete tuple nonce %x, want fixture", completeEntry.tuple.commitNonce)
	}
	if completeEntry.digest == nil {
		t.Fatal("complete entry digest missing")
	}
	if completeEntry.digest.byteLength != 2*uint64(format.PageSize) {
		t.Fatalf("complete digest length %d, want two pages", completeEntry.digest.byteLength)
	}
	partialEntry := maintenanceTestFindEntry(t, entries, partialID, maintenanceTempAttempt)
	if partialEntry.tuple != nil {
		t.Fatal("partial entry tuple present")
	}
	if partialEntry.digest != nil {
		t.Fatal("partial entry digest present")
	}
}

// TestMaintenanceExactRemovalHandlesCompletePartialAndAlreadyAbsent
// Outputs ports
// exact_removal_handles_complete_partial_and_already_absent_outputs.
func TestMaintenanceExactRemovalHandlesCompletePartialAndAlreadyAbsentOutputs(t *testing.T) {
	dir := t.TempDir()
	before := countProcessFds(t)
	completePath, completeAttempt := maintenanceTestCompleteOutput(t, dir, "result.v4")
	partialID := [16]byte{4}
	partialPath := filepath.Join(dir, maintenanceTestName(outputPrefix, partialID))
	if err := os.WriteFile(partialPath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	entries := maintenanceTestListTemps(t, dir)
	completeEntry := maintenanceTestFindEntry(t, entries, completeAttempt, maintenanceTempAttempt)
	partialEntry := maintenanceTestFindEntry(t, entries, partialID, maintenanceTempAttempt)

	result, err := removeAbandonedPublicationTemp(dir, completeEntry.directoryIdentity, completeEntry.attempt, completeEntry.artifactIdentity, completeEntry.tuple, completeEntry.digest, noopCheck)
	if err != nil {
		t.Fatalf("remove complete: %v", err)
	}
	if !result.SourcePresent {
		t.Fatal("complete removal reports source absent")
	}
	if result.CleanupState != CleanupStateClean {
		t.Fatalf("complete cleanup state = %v, want clean", result.CleanupState)
	}
	if result.Cause != nil {
		t.Fatalf("complete removal cause: %v", result.Cause)
	}
	if _, err := os.Lstat(completePath); err == nil {
		t.Fatal("complete output still exists")
	}

	result, err = removeAbandonedPublicationTemp(dir, partialEntry.directoryIdentity, partialEntry.attempt, partialEntry.artifactIdentity, nil, nil, noopCheck)
	if err != nil {
		t.Fatalf("remove partial: %v", err)
	}
	if !result.SourcePresent {
		t.Fatal("partial removal reports source absent")
	}
	if _, err := os.Lstat(partialPath); err == nil {
		t.Fatal("partial output still exists")
	}

	result, err = removeAbandonedPublicationTemp(dir, partialEntry.directoryIdentity, partialEntry.attempt, partialEntry.artifactIdentity, nil, nil, noopCheck)
	if err != nil {
		t.Fatalf("remove absent: %v", err)
	}
	if result.SourcePresent {
		t.Fatal("absent removal reports source present")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("output removals left %d descriptors open", after-before)
	}
}

// maintenanceTestName builds one private artifact name for the tests
// (Rust name/reservation_name).
func maintenanceTestName(prefix string, attempt [16]byte) string {
	name, err := privateName(prefix, attempt)
	if err != nil {
		panic(err)
	}
	return name
}

// TestMaintenanceRemovalRejectsChangedIdentityContentAndDirectory
// ports removal_rejects_changed_identity_content_and_directory.
func TestMaintenanceRemovalRejectsChangedIdentityContentAndDirectory(t *testing.T) {
	dir := t.TempDir()
	before := countProcessFds(t)
	completePath, completeAttempt := maintenanceTestCompleteOutput(t, dir, "result.v4")
	entry := maintenanceTestFindEntry(t, maintenanceTestListTemps(t, dir), completeAttempt, maintenanceTempAttempt)

	wrongDirectory := entry.directoryIdentity
	wrongDirectory.Bytes[0] ^= 1
	_, err := removeAbandonedPublicationTemp(dir, wrongDirectory, entry.attempt, entry.artifactIdentity, entry.tuple, entry.digest, noopCheck)
	if codeOf(err) != format.CodeDirectoryIdentityMismatch {
		t.Fatalf("wrong-directory problem = %v, want directory identity mismatch", err)
	}

	if err := os.WriteFile(completePath, []byte("changed"), 0o600); err != nil {
		t.Fatalf("change output: %v", err)
	}
	_, err = removeAbandonedPublicationTemp(dir, entry.directoryIdentity, entry.attempt, entry.artifactIdentity, entry.tuple, entry.digest, noopCheck)
	if codeOf(err) != format.CodeCleanupConflict {
		t.Fatalf("changed-content problem = %v, want cleanup conflict", err)
	}
	if _, err := os.Lstat(completePath); err != nil {
		t.Fatal("changed output disappeared")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("refusing removals left %d descriptors open", after-before)
	}
}

// TestMaintenanceListingHonorsCancellationStopAndSinkFailure ports
// listing_honors_cancellation_stop_and_sink_failure.
func TestMaintenanceListingHonorsCancellationStopAndSinkFailure(t *testing.T) {
	dir := t.TempDir()
	maintenanceTestCompleteOutput(t, dir, "result.v4")

	cancelled := &resolverTestCancellation{}
	cancelled.cancelled.Store(true)
	_, err := listAbandonedPublicationTemps(dir, cancelled.check, func(*abandonedPublicationTempEntry) error { return nil })
	if codeOf(err) != format.CodeCancelled {
		t.Fatalf("cancelled listing problem = %v, want cancelled", err)
	}

	_, err = listAbandonedPublicationTemps(dir, noopCheck, func(*abandonedPublicationTempEntry) error { return errMaintenanceSinkStop })
	if codeOf(err) != format.CodeStoppedBySink {
		t.Fatalf("stopped listing problem = %v, want stopped by sink", err)
	}

	_, err = listAbandonedPublicationTemps(dir, noopCheck, func(*abandonedPublicationTempEntry) error {
		return problem(format.CodeInvalidArgument, "sink")
	})
	if codeOf(err) != format.CodeSinkFailed {
		t.Fatalf("sink failure problem = %v, want sink failed", err)
	}
}

// TestMaintenanceReservationListingReportsBoundPolicyPhaseAndPrevious
// Evidence ports
// reservation_listing_reports_bound_policy_phase_and_previous_
// evidence.
func TestMaintenanceReservationListingReportsBoundPolicyPhaseAndPreviousEvidence(t *testing.T) {
	failDir := t.TempDir()
	failMain := filepath.Join(failDir, "fail.v4")
	runAttemptCrashChild(t, failMain, "publish", resolverPreMainPoints[0])
	failEntries := maintenanceTestListReservations(t, failDir)
	if len(failEntries) != 1 {
		t.Fatalf("fail entries = %d, want 1", len(failEntries))
	}
	fail := failEntries[0]
	if fail.evidence == nil {
		t.Fatal("fail evidence missing")
	}
	if fail.evidence.policy != abandonedReservationPolicyFailIfExists {
		t.Fatalf("fail policy = %v, want fail-if-exists", fail.evidence.policy)
	}
	if fail.evidence.phase != abandonedReservationPhasePrepared {
		t.Fatalf("fail phase = %v, want prepared", fail.evidence.phase)
	}
	if fail.evidence.output.tuple.databaseID != testFixtureDBID {
		t.Fatalf("fail output database id %x, want fixture", fail.evidence.output.tuple.databaseID)
	}
	if fail.evidence.output.tuple.transactionID != 1 {
		t.Fatalf("fail output transaction id %d, want 1", fail.evidence.output.tuple.transactionID)
	}
	if fail.evidence.output.tuple.commitNonce != testFixtureNonce {
		t.Fatalf("fail output nonce %x, want fixture", fail.evidence.output.tuple.commitNonce)
	}
	if fail.evidence.output.digest.byteLength != 2*uint64(format.PageSize) {
		t.Fatalf("fail output digest length %d, want the two fixture pages", fail.evidence.output.digest.byteLength)
	}
	if fail.evidence.previous != nil {
		t.Fatal("fail previous evidence present")
	}

	replaceDir := t.TempDir()
	replaceMain := filepath.Join(replaceDir, "replace.v4")
	runAttemptCrashChild(t, replaceMain, "replace", resolverPreMainPoints[0])
	replaceEntries := maintenanceTestListReservations(t, replaceDir)
	if len(replaceEntries) != 1 {
		t.Fatalf("replace entries = %d, want 1", len(replaceEntries))
	}
	replace := replaceEntries[0]
	if replace.evidence == nil {
		t.Fatal("replace evidence missing")
	}
	if replace.evidence.policy != abandonedReservationPolicyReplaceExisting {
		t.Fatalf("replace policy = %v, want replace-existing", replace.evidence.policy)
	}
	if replace.evidence.phase != abandonedReservationPhasePrepared {
		t.Fatalf("replace phase = %v, want prepared", replace.evidence.phase)
	}
	if replace.evidence.previous == nil {
		t.Fatal("replace previous evidence missing")
	}
	if replace.evidence.previous.digest.byteLength != uint64(len("previous bytes")) {
		t.Fatalf("replace previous length = %d, want %d", replace.evidence.previous.digest.byteLength, len("previous bytes"))
	}
}

// TestMaintenanceReservationListingIncludesMalformedExactNamesWithout
// Evidence ports
// reservation_listing_includes_malformed_exact_names_without_
// evidence.
func TestMaintenanceReservationListingIncludesMalformedExactNamesWithoutEvidence(t *testing.T) {
	dir := t.TempDir()
	malformedID := [16]byte{7}
	if err := os.WriteFile(filepath.Join(dir, maintenanceTestName(reservationPrefix, malformedID)), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".iprange-reservation-NOT-AN-ATTEMPT.tmp"), []byte("foreign"), 0o600); err != nil {
		t.Fatalf("write foreign: %v", err)
	}

	entries := maintenanceTestListReservations(t, dir)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].attempt != malformedID {
		t.Fatalf("attempt %x, want %x", entries[0].attempt, malformedID)
	}
	if entries[0].evidence != nil {
		t.Fatal("malformed entry evidence present")
	}
}

// TestMaintenanceReservationRemovalHandlesBoundMalformedAndAlready
// AbsentArtifacts ports
// reservation_removal_handles_bound_malformed_and_already_absent_
// artifacts.
func TestMaintenanceReservationRemovalHandlesBoundMalformedAndAlreadyAbsentArtifacts(t *testing.T) {
	dir := t.TempDir()
	before := countProcessFds(t)
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	bound := maintenanceTestListReservations(t, dir)[0]
	boundPath := filepath.Join(dir, maintenanceTestName(reservationPrefix, bound.attempt))
	result, err := removeAbandonedReservationArtifact(dir, bound.directoryIdentity, bound.attempt, bound.artifactIdentity, noopCheck)
	if err != nil {
		t.Fatalf("remove bound: %v", err)
	}
	if !result.SourcePresent {
		t.Fatal("bound removal reports source absent")
	}
	if _, err := os.Lstat(boundPath); err == nil {
		t.Fatal("bound reservation still exists")
	}

	malformedID := [16]byte{8}
	malformedPath := filepath.Join(dir, maintenanceTestName(reservationPrefix, malformedID))
	if err := os.WriteFile(malformedPath, []byte("partial"), 0o600); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	malformed := maintenanceTestFindEntry(t, maintenanceTestListReservations(t, dir), malformedID, maintenanceReservationAttempt)
	result, err = removeAbandonedReservationArtifact(dir, malformed.directoryIdentity, malformed.attempt, malformed.artifactIdentity, noopCheck)
	if err != nil {
		t.Fatalf("remove malformed: %v", err)
	}
	if !result.SourcePresent {
		t.Fatal("malformed removal reports source absent")
	}
	result, err = removeAbandonedReservationArtifact(dir, malformed.directoryIdentity, malformed.attempt, malformed.artifactIdentity, noopCheck)
	if err != nil {
		t.Fatalf("remove absent: %v", err)
	}
	if result.SourcePresent {
		t.Fatal("absent removal reports source present")
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("reservation removals left %d descriptors open", after-before)
	}
}

// TestMaintenanceReservationRemovalRejectsWrongDirectoryIdentityAnd
// HeaderBinding ports
// reservation_removal_rejects_wrong_directory_identity_and_header_
// binding.
func TestMaintenanceReservationRemovalRejectsWrongDirectoryIdentityAndHeaderBinding(t *testing.T) {
	dir := t.TempDir()
	before := countProcessFds(t)
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	bound := maintenanceTestListReservations(t, dir)[0]

	wrongDirectory := bound.directoryIdentity
	wrongDirectory.Bytes[0] ^= 1
	_, err := removeAbandonedReservationArtifact(dir, wrongDirectory, bound.attempt, bound.artifactIdentity, noopCheck)
	if codeOf(err) != format.CodeDirectoryIdentityMismatch {
		t.Fatalf("wrong-directory problem = %v, want directory identity mismatch", err)
	}

	sourcePath := filepath.Join(dir, maintenanceTestName(reservationPrefix, bound.attempt))
	copiedID := [16]byte{9}
	copiedPath := filepath.Join(dir, maintenanceTestName(reservationPrefix, copiedID))
	if err := copyFile(sourcePath, copiedPath); err != nil {
		t.Fatalf("copy reservation: %v", err)
	}
	copied := maintenanceTestFindEntry(t, maintenanceTestListReservations(t, dir), copiedID, maintenanceReservationAttempt)
	if copied.evidence != nil {
		t.Fatal("copied entry evidence present")
	}
	_, err = removeAbandonedReservationArtifact(dir, copied.directoryIdentity, copied.attempt, copied.artifactIdentity, noopCheck)
	if codeOf(err) != format.CodeCleanupConflict {
		t.Fatalf("copied-binding problem = %v, want cleanup conflict", err)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("refusing reservation removals left %d descriptors open", after-before)
	}
}

// TestMaintenanceReservationListingHonorsCancellationStopAndSink
// Failure ports reservation_listing_honors_cancellation_stop_and_
// sink_failure.
func TestMaintenanceReservationListingHonorsCancellationStopAndSinkFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, maintenanceTestName(reservationPrefix, [16]byte{10})), []byte("partial"), 0o600); err != nil {
		t.Fatalf("write reservation: %v", err)
	}

	cancelled := &resolverTestCancellation{}
	cancelled.cancelled.Store(true)
	_, err := listAbandonedReservationArtifacts(dir, cancelled.check, func(*abandonedReservationEntry) error { return nil })
	if codeOf(err) != format.CodeCancelled {
		t.Fatalf("cancelled listing problem = %v, want cancelled", err)
	}

	_, err = listAbandonedReservationArtifacts(dir, noopCheck, func(*abandonedReservationEntry) error { return errMaintenanceSinkStop })
	if codeOf(err) != format.CodeStoppedBySink {
		t.Fatalf("stopped listing problem = %v, want stopped by sink", err)
	}

	_, err = listAbandonedReservationArtifacts(dir, noopCheck, func(*abandonedReservationEntry) error {
		return problem(format.CodeInvalidArgument, "sink")
	})
	if codeOf(err) != format.CodeSinkFailed {
		t.Fatalf("sink failure problem = %v, want sink failed", err)
	}
}

// TestMaintenanceWindowsHousekeepingIsRefused ports the Rust
// non-windows arms of list_windows_housekeeping and
// remove_windows_housekeeping.
func TestMaintenanceWindowsHousekeepingIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := listWindowsHousekeeping(dir, noopCheck); codeOf(err) != format.CodeOSUnsupported {
		t.Fatalf("list housekeeping problem = %v, want os unsupported", err)
	}
	var identity LocalFileIdentity
	identity.Kind = identityKind
	var attempt [16]byte
	attempt[0] = 1
	if err := removeWindowsHousekeeping(dir, identity, attempt, 0, identity, nil, noopCheck); codeOf(err) != format.CodeOSUnsupported {
		t.Fatalf("remove housekeeping problem = %v, want os unsupported", err)
	}
}
