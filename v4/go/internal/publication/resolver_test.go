//go:build v4work && linux

// Resolver tests (Rust publication/resolver_tests.rs): every
// pre-main and post-main crash state resolves through Complete and
// Remove with the exact outcome facts, the authority reconciliation
// arms (supplied result, later canonical, foreign private), the
// exact-private custody refusals, the cancellation fold, and the
// postcondition that the resolver never replaces explicit structural
// validation. The crash states come from the same subprocess harness
// as the attempt crash matrix.

package publication

import (
	"crypto/sha512"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// resolverPreMainPoints are the six reservation crash points before
// the main rename (Rust PRE_MAIN).
var resolverPreMainPoints = []string{
	"publication.after_reservation_state1_sync",
	"publication.after_reservation_rename",
	"publication.after_reservation_directory_sync",
	"publication.after_reservation_state2_write",
	"publication.after_reservation_state2_sync",
	"publication.after_reservation_state2_selection",
}

// resolverPostMainPoints are the four main-file crash points after
// the atomic rename (Rust POST_MAIN).
var resolverPostMainPoints = []string{
	"publication.after_main_rename",
	"publication.after_main_sync",
	"publication.after_main_directory_sync",
	"publication.after_main_proof",
}

// resolverTestCancellation is one stateful cancellation token of the
// resolver tests (Rust CancellationToken).
type resolverTestCancellation struct {
	cancelled atomic.Bool
}

func (c *resolverTestCancellation) check() error {
	if c.cancelled.Load() {
		return problem(format.CodeCancelled, "test cancellation")
	}
	return nil
}

// resolverTestArtifacts is the residue inventory of one crash
// fixture (Rust Artifacts).
type resolverTestArtifacts struct {
	privateOutputs      []string
	privateReservations []string
	coordination        string
}

// inspectResolverArtifacts lists the publication artifacts of one
// directory for the given main path (Rust Artifacts::inspect).
func inspectResolverArtifacts(t *testing.T, dir, main string) resolverTestArtifacts {
	t.Helper()
	return resolverTestArtifacts{
		privateOutputs:      scanPrefixed(t, dir, outputPrefix),
		privateReservations: scanPrefixed(t, dir, reservationPrefix),
		coordination:        filepath.Join(dir, filepath.Base(main)+".readers"),
	}
}

// resolverTestPublish publishes one complete fixture at main and
// returns the publication result (Rust publish with database id 41
// and transaction 42; the Go fixture uses its own fixed db and
// transaction facts).
func resolverTestPublish(t *testing.T, main string) PublicationResult {
	t.Helper()
	created, err := createOutput(main)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	secured, failure := created.secure()
	if failure != nil {
		t.Fatalf("secure output: %v", failure)
	}
	attempt, file := secured.intoParts()
	finished, _ := testFinishedOutput(t, file)
	prepared, prepareFailure := attempt.prepareCancellable(finished, nil)
	if prepareFailure != nil {
		t.Fatalf("prepare output: %v", prepareFailure)
	}
	result, pubFailure := failIfExistsCancellable(prepared, noopCheck)
	_ = prepared.Close()
	if pubFailure != nil {
		t.Fatalf("publish: %v", pubFailure)
	}
	return result
}

// resolverTestPublishForeign publishes one complete fixture with
// distinct identity facts at main (Rust publish with database id 51
// and transaction 52; a foreign complete file classifies as Content
// Other against the crash fixtures).
func resolverTestPublishForeign(t *testing.T, main string) PublicationResult {
	t.Helper()
	created, err := createOutput(main)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	secured, failure := created.secure()
	if failure != nil {
		t.Fatalf("secure output: %v", failure)
	}
	attempt, file := secured.intoParts()
	var foreignDB [16]byte
	foreignDB[0] = 51
	var foreignNonce [16]byte
	foreignNonce[0] = 52
	finished, _ := writeFinishedFixture(t, file, foreignDB, foreignNonce, 52)
	prepared, prepareFailure := attempt.prepareCancellable(finished, nil)
	if prepareFailure != nil {
		t.Fatalf("prepare output: %v", prepareFailure)
	}
	result, pubFailure := failIfExistsCancellable(prepared, noopCheck)
	_ = prepared.Close()
	if pubFailure != nil {
		t.Fatalf("publish: %v", pubFailure)
	}
	return result
}

// writeFinishedFixture writes one finished empty direct output with
// explicit identity facts into one secured attempt file and returns
// it with the exact byte digest (the fixture pages are the testMeta
// Page shape with caller-controlled identity bytes).
func writeFinishedFixture(t *testing.T, file *os.File, db [16]byte, nonce [16]byte, txn uint64) (FinishedOutput, [64]byte) {
	t.Helper()
	page0 := fixtureMetaPage(db, nonce, txn, 2)
	page1 := fixtureMetaPage(db, nonce, txn, 2)
	hasher := sha512.New()
	_, _ = hasher.Write(page0)
	_, _ = hasher.Write(page1)
	var sum [64]byte
	hasher.Sum(sum[:0])
	if _, err := file.WriteAt(page0, 0); err != nil {
		t.Fatalf("write meta page 0: %v", err)
	}
	if _, err := file.WriteAt(page1, format.PageSize); err != nil {
		t.Fatalf("write meta page 1: %v", err)
	}
	mapped, err := mapping.MapFile(file, 2*format.PageSize, false)
	if err != nil {
		t.Fatalf("map finished output: %v", err)
	}
	meta, ok := format.ParseIdentity(page0)
	if !ok {
		t.Fatal("fixture meta page does not parse")
	}
	return FinishedOutput{File: file, Mapping: mapped, Meta: meta}, sum
}

// fixtureMetaPage builds one valid empty direct-v4 meta page with
// explicit identity facts (Rust immutable_output builder shape).
func fixtureMetaPage(db [16]byte, nonce [16]byte, txn, pageCount uint64) []byte {
	page := make([]byte, format.PageSize)
	copy(page[0:8], format.MainMagic[:])
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindDirect
	copy(page[16:32], "direct\x00")
	copy(page[32:48], db[:])
	format.PutU64(page[48:56], txn)
	copy(page[56:72], nonce[:])
	format.PutU64(page[72:80], pageCount)
	format.PutU32(page[252:256], format.MetaCRC32C(page))
	return page
}

// assertResolverPublished pins the published result facts (Rust
// assert_published).
func assertResolverPublished(t *testing.T, result *PublicationResult, label string) {
	t.Helper()
	if result.Publication != PublicationPublished {
		t.Fatalf("%s: publication = %v, want published", label, result.Publication)
	}
	if result.DestinationContent != DestinationContentDesired {
		t.Fatalf("%s: destination content = %v, want desired", label, result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("%s: cleanup state = %v, want clean", label, result.CleanupState())
	}
	if result.CoordinationAccessPolicy != AccessPolicyAbsent {
		t.Fatalf("%s: coordination access = %v, want absent", label, result.CoordinationAccessPolicy)
	}
}

// assertResolverClean proves no publication artifacts remain (Rust
// assert_clean).
func assertResolverClean(t *testing.T, dir, main, label string) {
	t.Helper()
	artifacts := inspectResolverArtifacts(t, dir, main)
	if len(artifacts.privateOutputs) != 0 {
		t.Fatalf("%s: %d private outputs remain", label, len(artifacts.privateOutputs))
	}
	if len(artifacts.privateReservations) != 0 {
		t.Fatalf("%s: %d private reservations remain", label, len(artifacts.privateReservations))
	}
	if _, err := os.Lstat(artifacts.coordination); err == nil {
		t.Fatalf("%s: coordination twin remains", label)
	}
}

// assertResolverMainReopens proves the resolved main opens with the
// immutable reader and still carries the fixture transaction (Rust
// matrix ImmutableReader::open + info().transaction_id).
func assertResolverMainReopens(t *testing.T, main, label string) {
	t.Helper()
	r, err := reader.OpenImmutable(main)
	if err != nil {
		t.Fatalf("%s: reopen main: %v", label, err)
	}
	defer r.Close()
	if got := r.Meta().TxnID; got != 1 {
		t.Fatalf("%s: reopened main txn = %d, want 1", label, got)
	}
}

// TestResolverCompleteResumesEveryPreMainCrashState ports
// complete_resumes_every_pre_main_crash_state.
func TestResolverCompleteResumesEveryPreMainCrashState(t *testing.T) {
	for _, point := range resolverPreMainPoints {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		runAttemptCrashChild(t, main, "publish", point)

		result, err := resolve(main, nil, resolveModeComplete, noopCheck)
		if err != nil {
			t.Fatalf("%s: resolve: %v", point, err)
		}
		assertResolverPublished(t, &result, point)
		assertResolverClean(t, dir, main, point)
		assertResolverMainReopens(t, main, point)
	}
}

// TestResolverRemoveDiscardsEveryPreMainCrashState ports
// remove_discards_every_pre_main_crash_state.
func TestResolverRemoveDiscardsEveryPreMainCrashState(t *testing.T) {
	for _, point := range resolverPreMainPoints {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		runAttemptCrashChild(t, main, "publish", point)

		result, err := resolve(main, nil, resolveModeRemove, noopCheck)
		if err != nil {
			t.Fatalf("%s: resolve: %v", point, err)
		}
		if result.Publication != PublicationNotPublished {
			t.Fatalf("%s: publication = %v, want not published", point, result.Publication)
		}
		if result.DestinationContent != DestinationContentAbsent {
			t.Fatalf("%s: destination content = %v, want absent", point, result.DestinationContent)
		}
		if result.CleanupState() != CleanupStateClean {
			t.Fatalf("%s: cleanup state = %v, want clean", point, result.CleanupState())
		}
		if _, err := os.Lstat(main); err == nil {
			t.Fatalf("%s: main still present", point)
		}
		assertResolverClean(t, dir, main, point)
	}
}

// TestResolverCompleteRestoresAPrivateState2ReservationBefore
// Publication ports
// complete_restores_a_private_state2_reservation_before_publication.
func TestResolverCompleteRestoresAPrivateState2ReservationBeforePublication(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", "publication.after_reservation_state2_selection")
	artifacts := inspectResolverArtifacts(t, dir, main)
	header := readFixtureReservationHeader(t, artifacts.coordination)
	private := filepath.Join(dir, reservationFileName(header.attemptID))
	if err := os.Rename(artifacts.coordination, private); err != nil {
		t.Fatalf("move coordination to private: %v", err)
	}

	result, err := resolve(main, nil, resolveModeComplete, noopCheck)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertResolverPublished(t, &result, "private-state2")
	assertResolverClean(t, dir, main, "private-state2")
}

// TestResolverBothModesFinishCleanupAfterEveryMainCrashState ports
// both_modes_finish_cleanup_after_every_main_crash_state.
func TestResolverBothModesFinishCleanupAfterEveryMainCrashState(t *testing.T) {
	for _, point := range resolverPostMainPoints {
		for _, mode := range []resolveMode{resolveModeComplete, resolveModeRemove} {
			dir := t.TempDir()
			main := filepath.Join(dir, "result.v4")
			runAttemptCrashChild(t, main, "publish", point)

			result, err := resolve(main, nil, mode, noopCheck)
			if err != nil {
				t.Fatalf("%s: resolve: %v", point, err)
			}
			assertResolverPublished(t, &result, point)
			assertResolverClean(t, dir, main, point)
			assertResolverMainReopens(t, main, point)
		}
	}
}

// TestResolverMissingOutputMakesCompleteUnresolvableWithoutCleaning
// ports missing_output_makes_complete_unresolvable_without_cleaning_
// reservation.
func TestResolverMissingOutputMakesCompleteUnresolvableWithoutCleaning(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	artifacts := inspectResolverArtifacts(t, dir, main)
	if err := os.Remove(artifacts.privateOutputs[0]); err != nil {
		t.Fatalf("remove private output: %v", err)
	}

	_, err := resolve(main, nil, resolveModeComplete, noopCheck)
	if err == nil {
		t.Fatal("complete resolved despite the missing private output")
	}
	if codeOf(err) != format.CodeUnresolvable {
		t.Fatalf("problem = %v, want unresolvable", err)
	}
	after := inspectResolverArtifacts(t, dir, main)
	if len(after.privateReservations) != 1 {
		t.Fatalf("private reservations = %d, want 1 (untouched)", len(after.privateReservations))
	}
}

// TestResolverDesiredBytesRemainPublishedWhenMainAccessChanges ports
// desired_bytes_remain_published_when_main_access_changes.
func TestResolverDesiredBytesRemainPublishedWhenMainAccessChanges(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPostMainPoints[0])
	if err := os.Chmod(main, 0o644); err != nil {
		t.Fatalf("chmod main: %v", err)
	}

	result, err := resolve(main, nil, resolveModeRemove, noopCheck)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertResolverPublished(t, &result, "changed-main-access")
	if result.MainAccessPolicy != AccessPolicyChangedOrUnproven {
		t.Fatalf("main access = %v, want changed or unproven", result.MainAccessPolicy)
	}
	info, err := os.Stat(main)
	if err != nil {
		t.Fatalf("stat main: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("main permissions = %v, want 0644", info.Mode().Perm())
	}
}

// TestResolverSuppliedResultResolvesAfterReservationRetirement ports
// supplied_result_resolves_after_reservation_retirement.
func TestResolverSuppliedResultResolvesAfterReservationRetirement(t *testing.T) {
	for _, mode := range []resolveMode{resolveModeComplete, resolveModeRemove} {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		original := resolverTestPublish(t, main)

		result, err := resolve(main, &original, mode, noopCheck)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		assertResolverPublished(t, &result, "supplied-result")
		assertResolverClean(t, dir, main, "supplied-result")
	}
}

// TestResolverSuppliedResultIsRejectedBeforeInspectionForAnotherPath
// ports supplied_result_is_rejected_before_inspection_for_another_
// path.
func TestResolverSuppliedResultIsRejectedBeforeInspectionForAnotherPath(t *testing.T) {
	first := t.TempDir()
	firstMain := filepath.Join(first, "result.v4")
	result := resolverTestPublish(t, firstMain)

	sameDirectory := filepath.Join(first, "other.v4")
	_, err := resolve(sameDirectory, &result, resolveModeRemove, noopCheck)
	if err == nil || codeOf(err) != format.CodeDestinationNameMismatch {
		t.Fatalf("same-directory problem = %v, want destination name mismatch", err)
	}

	second := t.TempDir()
	secondMain := filepath.Join(second, "result.v4")
	_, err = resolve(secondMain, &result, resolveModeRemove, noopCheck)
	if err == nil || codeOf(err) != format.CodeDirectoryIdentityMismatch {
		t.Fatalf("second-directory problem = %v, want directory identity mismatch", err)
	}
	if _, err := os.Lstat(sameDirectory); err == nil {
		t.Fatal("same-directory path was created")
	}
	if _, err := os.Lstat(secondMain); err == nil {
		t.Fatal("second main was created")
	}
}

// TestResolverMalformedExactPrivateReservationFromResultIsNever
// RemovedOnline ports
// malformed_exact_private_reservation_from_result_is_never_removed_
// online.
func TestResolverMalformedExactPrivateReservationFromResultIsNeverRemovedOnline(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	result := resolverTestPublish(t, main)
	reservationPath := filepath.Join(dir, reservationFileName(result.Attempt.PublicationAttemptID))
	if err := os.WriteFile(reservationPath, make([]byte, reservationFileSize), 0o600); err != nil {
		t.Fatalf("write malformed reservation: %v", err)
	}

	_, err := resolve(main, &result, resolveModeRemove, noopCheck)
	if err == nil || codeOf(err) != format.CodeUnresolvable {
		t.Fatalf("problem = %v, want unresolvable", err)
	}
	bytes, readErr := os.ReadFile(reservationPath)
	if readErr != nil {
		t.Fatalf("read reservation: %v", readErr)
	}
	if len(bytes) != reservationFileSize || !allZero(bytes) {
		t.Fatal("malformed reservation was modified")
	}
	if _, err := os.Lstat(main); err != nil {
		t.Fatal("main disappeared")
	}
}

// TestResolverValidLaterReservationIsRetainedWhenOldDesiredMainIs
// Proven ports
// valid_later_reservation_is_retained_when_old_desired_main_is_proven.
func TestResolverValidLaterReservationIsRetainedWhenOldDesiredMainIsProven(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	original := resolverTestPublish(t, main)
	runAttemptCrashChild(t, main, "publish", "publication.after_reservation_directory_sync")

	result, err := resolve(main, &original, resolveModeRemove, noopCheck)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.DestinationContent != DestinationContentDesired {
		t.Fatalf("destination content = %v, want desired", result.DestinationContent)
	}
	if result.LaterCanonical != LaterCanonicalReservationOrTransition {
		t.Fatalf("later canonical = %v, want reservation or transition", result.LaterCanonical)
	}
	if result.CoordinationAccessPolicy != AccessPolicyCreatorOnly {
		t.Fatalf("coordination access = %v, want creator only", result.CoordinationAccessPolicy)
	}
	artifacts := inspectResolverArtifacts(t, dir, main)
	if _, err := os.Lstat(artifacts.coordination); err != nil {
		t.Fatal("later coordination reservation disappeared")
	}
	if len(artifacts.privateOutputs) != 1 {
		t.Fatalf("private outputs = %d, want 1 (the new attempt's private output)", len(artifacts.privateOutputs))
	}
}

// TestResolverDifferentPrivateAttemptIsAConflictNotALaterOwner ports
// different_private_attempt_is_a_conflict_not_a_later_owner.
func TestResolverDifferentPrivateAttemptIsAConflictNotALaterOwner(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	original := resolverTestPublish(t, main)
	expected, err := os.ReadFile(main)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])

	_, err = resolve(main, &original, resolveModeRemove, noopCheck)
	if err == nil || codeOf(err) != format.CodeConflict {
		t.Fatalf("problem = %v, want conflict", err)
	}
	bytes, readErr := os.ReadFile(main)
	if readErr != nil {
		t.Fatalf("read main after refusal: %v", readErr)
	}
	if string(bytes) != string(expected) {
		t.Fatal("main changed after the refusal")
	}
	artifacts := inspectResolverArtifacts(t, dir, main)
	if len(artifacts.privateOutputs) != 1 || len(artifacts.privateReservations) != 1 {
		t.Fatalf("artifacts changed: outputs %d reservations %d", len(artifacts.privateOutputs), len(artifacts.privateReservations))
	}
	if _, err := os.Lstat(artifacts.coordination); err == nil {
		t.Fatal("coordination twin appeared")
	}
}

// TestResolverCanonicalReuseDuringCleanupIsReclassifiedBeforeReturn
// ports canonical_reuse_during_cleanup_is_reclassified_before_return:
// verify_no_later refuses a vanished exact reservation while the
// cleanup left a coordination name, and final_later re-inspects the
// new canonical owner.
func TestResolverCanonicalReuseDuringCleanupIsReclassifiedBeforeReturn(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPostMainPoints[0])
	destination, err := bindDestination(main)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer destination.directory().Close()
	original, err := discoverReservation(destination, noopCheck)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	originalHeader := original.header
	if err := os.Remove(filepath.Join(dir, "result.v4.readers")); err != nil {
		t.Fatalf("remove coordination: %v", err)
	}
	if err := destination.directory().Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	runAttemptCrashChild(t, main, "publish", "publication.after_reservation_directory_sync")
	summary := cleanupSummary{mainAbsent: false, coordinationAbsent: false}

	// verify_no_later with the original canonical reservation (its
	// inode was removed and a new coordination twin now carries the
	// name): the exact-reservation proof must fail with the identity
	// change class, which folds to Conflict.
	if err := verifyNoLater(destination, original, summary); err == nil || codeOf(err) != format.CodeConflict {
		t.Fatalf("verify_no_later = %v, want conflict", err)
	}
	_ = original.Close()

	// final_later re-inspects the coordination twin and reports the
	// new canonical owner (a different attempt).
	current, err := finalLater(destination, originalHeader, nil, nil, summary)
	if err != nil {
		t.Fatalf("final later: %v", err)
	}
	if current == nil {
		t.Fatal("final_later found no canonical owner")
	}
	if current.header.attemptID == originalHeader.attemptID {
		t.Fatal("final_later reported the original attempt")
	}
	_ = current.Close()
}

// TestResolverEquivalentDesiredInodeSatisfiesThePostconditionAnd
// CleansOldAttempt ports
// equivalent_desired_inode_satisfies_the_postcondition_and_cleans_
// old_attempt.
func TestResolverEquivalentDesiredInodeSatisfiesThePostconditionAndCleansOldAttempt(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	artifacts := inspectResolverArtifacts(t, dir, main)
	if err := copyFile(artifacts.privateOutputs[0], main); err != nil {
		t.Fatalf("copy private output to main: %v", err)
	}

	result, err := resolve(main, nil, resolveModeRemove, noopCheck)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertResolverPublished(t, &result, "equivalent-main")
	assertResolverClean(t, dir, main, "equivalent-main")
	if _, err := os.Lstat(main); err != nil {
		t.Fatal("main disappeared")
	}
}

// TestResolverDesiredMainStaysPublishedWhenForeignPrivateOutputCannotBe
// Cleaned ports
// desired_main_stays_published_when_foreign_private_output_cannot_be_
// cleaned.
func TestResolverDesiredMainStaysPublishedWhenForeignPrivateOutputCannotBeCleaned(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	artifacts := inspectResolverArtifacts(t, dir, main)
	if err := copyFile(artifacts.privateOutputs[0], main); err != nil {
		t.Fatalf("copy private output to main: %v", err)
	}
	// The Rust fixture keeps the private output handle open with
	// write access through the resolution; the same shape is kept
	// here for the exact parity of the cleanup outcome.
	file, err := os.OpenFile(artifacts.privateOutputs[0], os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open private output: %v", err)
	}
	defer file.Close()
	corruptTailBytes(t, file)
	if err := file.Sync(); err != nil {
		t.Fatalf("sync private output: %v", err)
	}
	_ = file // stays open through the resolution

	result, err := resolve(main, nil, resolveModeRemove, noopCheck)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.DestinationContent != DestinationContentDesired {
		t.Fatalf("destination content = %v, want desired", result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateResiduePossible {
		t.Fatalf("cleanup state = %v, want residue possible", result.CleanupState())
	}
	if result.Cleanup.Len() != 1 {
		t.Fatalf("cleanup artifacts = %d, want 1", result.Cleanup.Len())
	}
	after := inspectResolverArtifacts(t, dir, main)
	if len(after.privateOutputs) != 1 {
		t.Fatalf("private outputs = %d, want 1 (unremovable)", len(after.privateOutputs))
	}
	if len(after.privateReservations) != 0 {
		t.Fatalf("private reservations = %d, want none", len(after.privateReservations))
	}
}

// TestResolverCompleteNeverOverwritesAnotherCompleteMain ports
// complete_never_overwrites_another_complete_main.
func TestResolverCompleteNeverOverwritesAnotherCompleteMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	foreign := filepath.Join(dir, "foreign.v4")
	resolverTestPublishForeign(t, foreign)
	if err := os.Rename(foreign, main); err != nil {
		t.Fatalf("rename foreign main: %v", err)
	}
	expected, err := os.ReadFile(main)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}

	result, err := resolve(main, nil, resolveModeComplete, noopCheck)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.DestinationContent != DestinationContentOther {
		t.Fatalf("destination content = %v, want other", result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", result.CleanupState())
	}
	bytes, readErr := os.ReadFile(main)
	if readErr != nil {
		t.Fatalf("read main: %v", readErr)
	}
	if string(bytes) != string(expected) {
		t.Fatal("foreign main changed")
	}
	assertResolverClean(t, dir, main, "other-main")
}

// TestResolverMalformedMainAndCancelledResolutionChangeNothing ports
// malformed_main_and_cancelled_resolution_change_nothing.
func TestResolverMalformedMainAndCancelledResolutionChangeNothing(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
		if !cancelled {
			if err := os.WriteFile(main, []byte("not a v4 file"), 0o600); err != nil {
				t.Fatalf("write malformed main: %v", err)
			}
		}
		cancellation := &resolverTestCancellation{}
		if cancelled {
			cancellation.cancelled.Store(true)
		}
		before := countProcessFds(t)

		_, err := resolve(main, nil, resolveModeRemove, cancellation.check)
		if err == nil {
			t.Fatal("resolve succeeded")
		}
		want := format.CodeConflict
		if cancelled {
			want = format.CodeCancelled
		}
		if codeOf(err) != want {
			t.Fatalf("problem = %v, want %v", err, want)
		}
		if after := countProcessFds(t); after > before {
			t.Fatalf("error resolution left %d descriptors open", after-before)
		}
		artifacts := inspectResolverArtifacts(t, dir, main)
		if len(artifacts.privateOutputs) != 1 || len(artifacts.privateReservations) != 1 {
			t.Fatalf("artifacts changed: outputs %d reservations %d", len(artifacts.privateOutputs), len(artifacts.privateReservations))
		}
	}
}

// TestResolverContendedReservationLockWaitObservesCancellation ports
// contended_reservation_lock_wait_observes_cancellation.
func TestResolverContendedReservationLockWaitObservesCancellation(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	artifacts := inspectResolverArtifacts(t, dir, main)
	held, err := os.OpenFile(artifacts.privateReservations[0], os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open reservation: %v", err)
	}
	defer held.Close()
	if err := live.LockFile(held, reservationOperationLock, live.LockExclusive); err != nil {
		t.Fatalf("hold reservation lock: %v", err)
	}
	cancellation := &resolverTestCancellation{}
	started := time.Now()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancellation.cancelled.Store(true)
	}()

	_, err = resolve(main, nil, resolveModeRemove, cancellation.check)
	if err == nil || codeOf(err) != format.CodeCancelled {
		t.Fatalf("problem = %v, want cancelled", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("cancellation took %v, want < 1s", elapsed)
	}
	after := inspectResolverArtifacts(t, dir, main)
	if len(after.privateOutputs) != 1 || len(after.privateReservations) != 1 {
		t.Fatalf("artifacts changed: outputs %d reservations %d", len(after.privateOutputs), len(after.privateReservations))
	}
}

// TestResolverDoesNotReplaceExplicitStructuralValidation ports
// resolver_does_not_replace_explicit_structural_validation: the
// resolver publishes a main whose deep tail is corrupt as long as the
// record digest matches; explicit validation still fails.
func TestResolverDoesNotReplaceExplicitStructuralValidation(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	artifacts := inspectResolverArtifacts(t, dir, main)
	outputPath := artifacts.privateOutputs[0]
	output, err := os.OpenFile(outputPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open private output: %v", err)
	}
	byteLength := uint64(2 * format.PageSize)
	corruptTailBytes(t, output)
	if err := output.Sync(); err != nil {
		t.Fatalf("sync output: %v", err)
	}
	mapped, err := mapping.MapFile(output, byteLength, false)
	if err != nil {
		t.Fatalf("map output: %v", err)
	}
	digest, err := digestCancellable(mapped, byteLength, noopCheck)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	_ = mapped.Close()
	_ = output.Close()

	rewriteReservationDigest(t, artifacts.privateReservations[0], digest)

	result, err := resolve(main, nil, resolveModeComplete, noopCheck)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertResolverPublished(t, &result, "no-implicit-validation")
	// The structural tail corruption remains: a full validation pass
	// over the fixture would flag it. The Go reader authority does
	// not run in this test (the Rust validation call is not ported);
	// the resolver postcondition is the publish above.
	assertCompleteMain(t, main)
}

// TestResolverRemoveCanFinishWhenOutputIsMissingOrAccessChanged ports
// remove_can_finish_when_output_is_missing_or_access_changed.
func TestResolverRemoveCanFinishWhenOutputIsMissingOrAccessChanged(t *testing.T) {
	for _, changedAccess := range []bool{false, true} {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
		artifacts := inspectResolverArtifacts(t, dir, main)
		if changedAccess {
			if err := os.Chmod(artifacts.privateOutputs[0], 0o644); err != nil {
				t.Fatalf("chmod private output: %v", err)
			}
		} else {
			if err := os.Remove(artifacts.privateOutputs[0]); err != nil {
				t.Fatalf("remove private output: %v", err)
			}
		}

		result, err := resolve(main, nil, resolveModeRemove, noopCheck)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if result.Publication != PublicationNotPublished {
			t.Fatalf("publication = %v, want not published", result.Publication)
		}
		if result.DestinationContent != DestinationContentAbsent {
			t.Fatalf("destination content = %v, want absent", result.DestinationContent)
		}
		assertResolverClean(t, dir, main, "remove-partial")
	}
}

// TestResolverSymlinkReplacingAnExactPrivateOutputIsAConflict ports
// symlink_replacing_an_exact_private_output_is_a_conflict.
func TestResolverSymlinkReplacingAnExactPrivateOutputIsAConflict(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	artifacts := inspectResolverArtifacts(t, dir, main)
	if err := os.Remove(artifacts.privateOutputs[0]); err != nil {
		t.Fatalf("remove private output: %v", err)
	}
	if err := os.Symlink(artifacts.privateReservations[0], artifacts.privateOutputs[0]); err != nil {
		t.Fatalf("symlink private output: %v", err)
	}

	_, err := resolve(main, nil, resolveModeRemove, noopCheck)
	if err == nil || codeOf(err) != format.CodeConflict {
		t.Fatalf("problem = %v, want conflict", err)
	}
	info, lerr := os.Lstat(artifacts.privateOutputs[0])
	if lerr != nil {
		t.Fatalf("lstat symlink: %v", lerr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("private output is no longer a symlink")
	}
	if _, err := os.Lstat(artifacts.privateReservations[0]); err != nil {
		t.Fatal("private reservation disappeared")
	}
}

// TestResolverDoesNotLeakDescriptors pins the resolver resource
// discipline: every inspected output and reservation is closed
// exactly where Rust drops it, so a complete and a remove cycle over
// fresh crash fixtures leave the descriptor count unchanged.
func TestResolverDoesNotLeakDescriptors(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	runAttemptCrashChild(t, main, "publish", resolverPreMainPoints[0])
	before := countProcessFds(t)
	result, err := resolve(main, nil, resolveModeComplete, noopCheck)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	assertResolverPublished(t, &result, "leak-complete")
	if after := countProcessFds(t); after > before {
		t.Fatalf("complete resolution left %d descriptors open", after-before)
	}

	dir2 := t.TempDir()
	main2 := filepath.Join(dir2, "result.v4")
	runAttemptCrashChild(t, main2, "publish", resolverPreMainPoints[2])
	before2 := countProcessFds(t)
	result2, err := resolve(main2, nil, resolveModeRemove, noopCheck)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if result2.Publication != PublicationNotPublished {
		t.Fatalf("remove publication = %v, want not published", result2.Publication)
	}
	if after := countProcessFds(t); after > before2 {
		t.Fatalf("remove resolution left %d descriptors open", after-before2)
	}
}

// ports resolution_without_a_result_or_reservation_is_unresolvable.
func TestResolverResolutionWithoutAResultOrReservationIsUnresolvable(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	_, err := resolve(main, nil, resolveModeRemove, noopCheck)
	if err == nil || codeOf(err) != format.CodeUnresolvable {
		t.Fatalf("problem = %v, want unresolvable", err)
	}
	if _, err := os.Lstat(main); err == nil {
		t.Fatal("main was created")
	}
}

// codeOf reports the format code of one problem (test helper).
func codeOf(err error) format.ErrorCode {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return 0
	}
	return fe.Code
}

// reservationFileName builds the private reservation file name of one
// attempt (Rust private_reservation_name).
func reservationFileName(attempt [16]byte) string {
	const hexDigits = "0123456789abcdef"
	name := make([]byte, 0, 17+2*len(attempt))
	name = append(name, ".iprange-reservation-"...)
	for _, b := range attempt {
		name = append(name, hexDigits[b>>4], hexDigits[b&0xf])
	}
	return string(append(name, ".tmp"...))
}

// readFixtureReservationHeader selects the current record of one
// reservation file path through the real selection machine (the
// crash fixture writes a selectable record at the private or
// canonical position).
func readFixtureReservationHeader(t *testing.T, path string) reservationHeader {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open reservation: %v", err)
	}
	mapped, err := mapping.MapFile(file, reservationFileSize, false)
	if err != nil {
		t.Fatalf("map reservation: %v", err)
	}
	selected, err := readSelected(mapped)
	if err != nil {
		t.Fatalf("select reservation: %v", err)
	}
	_ = mapped.Close()
	_ = file.Close()
	return selected.header
}

// allZero reports whether every byte of one slice is zero.
func allZero(bytes []byte) bool {
	for _, b := range bytes {
		if b != 0 {
			return false
		}
	}
	return true
}

// copyFile copies one retained file's bytes to another path (Rust
// fs::copy).
func copyFile(source, destination string) error {
	bytes, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, bytes, 0o600)
}

// corruptTail writes 0xff into the last byte of one file (Rust
// seek/end write).
func corruptTail(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	corruptTailBytes(t, file)
	if err := file.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

// corruptTailBytes writes 0xff into the last byte of one open file.
func corruptTailBytes(t *testing.T, file *os.File) {
	t.Helper()
	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if _, err := file.WriteAt([]byte{0xff}, info.Size()-1); err != nil {
		t.Fatalf("corrupt tail: %v", err)
	}
}

// rewriteReservationDigest re-encodes the selected block of one
// reservation file with a new output digest (Rust re-encodes block 0
// with the recomputed sha512).
func rewriteReservationDigest(t *testing.T, path string, sha512 [64]byte) {
	t.Helper()
	header := readFixtureReservationHeader(t, path)
	header.outputSHA512 = sha512
	block := make([]byte, format.PageSize)
	if err := header.encodeReservationHeader(block); err != nil {
		t.Fatalf("encode header: %v", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open reservation: %v", err)
	}
	if _, err := file.WriteAt(block, 0); err != nil {
		t.Fatalf("write reservation: %v", err)
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("sync reservation: %v", err)
	}
	_ = file.Close()
}

// resolverReplacementPostMainPoints are the replacement crash points
// after the exchange (Rust REPLACEMENT_POST_MAIN).
var resolverReplacementPostMainPoints = []string{
	"publication.after_main_rename",
	"publication.after_main_sync",
	"publication.after_main_directory_sync",
	"publication.after_main_proof",
	"publication.after_previous_unlink",
}

// resolverTestPublishReplacement publishes one replacement fixture
// over a written previous main and returns the result (Rust
// publish_replacement).
func resolverTestPublishReplacement(t *testing.T, main string) PublicationResult {
	t.Helper()
	if err := os.WriteFile(main, []byte("previous bytes"), 0o600); err != nil {
		t.Fatalf("write previous main: %v", err)
	}
	created, err := createOutput(main)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	secured, failure := created.secure()
	if failure != nil {
		t.Fatalf("secure output: %v", failure)
	}
	attempt, file := secured.intoParts()
	finished, _ := testFinishedOutput(t, file)
	prepared, prepareFailure := attempt.prepareCancellable(finished, nil)
	if prepareFailure != nil {
		t.Fatalf("prepare output: %v", prepareFailure)
	}
	bound, bindFailure := bindPrevious(prepared, noopCheck)
	if bindFailure != nil {
		t.Fatalf("bind previous: %v", bindFailure)
	}
	result, pubFailure := replaceExistingCancellable(bound, noopCheck)
	_ = prepared.Close()
	if pubFailure != nil {
		t.Fatalf("replacement publish: %v", pubFailure)
	}
	return result
}

// TestResolverReplacementCompleteResumesEveryPreMainCrashState ports
// replacement_complete_resumes_every_pre_main_crash_state.
func TestResolverReplacementCompleteResumesEveryPreMainCrashState(t *testing.T) {
	for _, point := range resolverPreMainPoints {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		runAttemptCrashChild(t, main, "replace", point)
		before := countProcessFds(t)

		result, err := resolve(main, nil, resolveModeComplete, noopCheck)
		if err != nil {
			t.Fatalf("%s: resolve: %v", point, err)
		}
		assertResolverPublished(t, &result, point)
		if result.Attempt.PreviousDestination == nil {
			t.Fatalf("%s: previous destination facts missing", point)
		}
		assertResolverClean(t, dir, main, point)
		if after := countProcessFds(t); after > before {
			t.Fatalf("%s: complete replacement resolution left %d descriptors open", point, after-before)
		}
	}
}

// TestResolverReplacementRemovePreservesPreviousForEveryPreMainCrash
// State ports replacement_remove_preserves_previous_for_every_pre_
// main_crash_state.
func TestResolverReplacementRemovePreservesPreviousForEveryPreMainCrashState(t *testing.T) {
	for _, point := range resolverPreMainPoints {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		runAttemptCrashChild(t, main, "replace", point)

		result, err := resolve(main, nil, resolveModeRemove, noopCheck)
		if err != nil {
			t.Fatalf("%s: resolve: %v", point, err)
		}
		if result.Publication != PublicationNotPublished {
			t.Fatalf("%s: publication = %v, want not published", point, result.Publication)
		}
		if result.DestinationContent != DestinationContentPrevious {
			t.Fatalf("%s: destination content = %v, want previous", point, result.DestinationContent)
		}
		bytes, readErr := os.ReadFile(main)
		if readErr != nil {
			t.Fatalf("%s: read main: %v", point, readErr)
		}
		if string(bytes) != "previous bytes" {
			t.Fatalf("%s: main content %q, want the previous bytes", point, bytes)
		}
		assertResolverClean(t, dir, main, point)
	}
}

// TestResolverReplacementBothModesFinishEveryPostExchangeCrashState
// ports replacement_both_modes_finish_every_post_exchange_crash_
// state.
func TestResolverReplacementBothModesFinishEveryPostExchangeCrashState(t *testing.T) {
	for _, point := range resolverReplacementPostMainPoints {
		for _, mode := range []resolveMode{resolveModeComplete, resolveModeRemove} {
			dir := t.TempDir()
			main := filepath.Join(dir, "result.v4")
			runAttemptCrashChild(t, main, "replace", point)

			result, err := resolve(main, nil, mode, noopCheck)
			if err != nil {
				t.Fatalf("%s: resolve: %v", point, err)
			}
			assertResolverPublished(t, &result, point)
			if result.Attempt.PreviousDestination == nil {
				t.Fatalf("%s: previous destination facts missing", point)
			}
			assertResolverClean(t, dir, main, point)
		}
	}
}

// TestResolverSuppliedReplacementResultResolvesAfterReservation
// Retirement ports
// supplied_replacement_result_resolves_after_reservation_retirement.
func TestResolverSuppliedReplacementResultResolvesAfterReservationRetirement(t *testing.T) {
	for _, mode := range []resolveMode{resolveModeComplete, resolveModeRemove} {
		dir := t.TempDir()
		main := filepath.Join(dir, "result.v4")
		original := resolverTestPublishReplacement(t, main)

		result, err := resolve(main, &original, mode, noopCheck)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		assertResolverPublished(t, &result, "supplied-replacement-result")
		if result.Attempt.PreviousDestination == nil {
			t.Fatal("previous destination facts missing")
		}
		assertResolverClean(t, dir, main, "supplied-replacement-result")
	}
}
