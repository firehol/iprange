//go:build linux

// Publication attempt machine tests (Rust publication/attempt_tests.rs
// arms): the success facts with no residue, the exact failure
// classifications before and after state 2 with their cleanup
// ledgers, the foreign-name refusals, the post-proof published
// retention, the replacement exchange and race detection, and the
// crash-resume outcome classes.

package publication

import (
	"crypto/sha512"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// attemptTestInjected is the fixed injected checkpoint problem of the
// attempt tests; it rides the Error::Checkpoint clone-through class so
// every composition fold returns it unchanged (Rust Problem::injected).
func attemptTestInjected() error {
	return &checkpointProblem{problem: problem(format.CodeIO, "injected attempt checkpoint failure")}
}

// attemptTestCheckpoint builds the point-aware checkpoint closure of
// the Rust fail_if_exists_with tests: it fails only the named points
// and runs everything else clean.
func attemptTestCheckpoint(fail map[attemptPoint]error, before func(attemptPoint)) func(attemptPoint) error {
	return func(point attemptPoint) error {
		if before != nil {
			before(point)
		}
		return fail[point]
	}
}

func noopAttemptObserver(*publicationCheckpoint) error { return nil }

// attemptTestPrepared builds one fully prepared output and its exact
// byte digest (the Rust prepared_output fixture).
func attemptTestPrepared(t *testing.T, dir, mainName string) (*preparedOutput, [64]byte) {
	t.Helper()
	attempt, file := testSecuredAttempt(t, dir, mainName)
	finished, sum := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare output: %v", failure)
	}
	return prepared, sum
}

// attemptTestReplacement binds one prepared output to a written
// previous main and returns the bound output plus the previous digest
// (the Rust prepared_replacement_output fixture).
func attemptTestReplacement(t *testing.T, prepared *preparedOutput, dir string) (*preparedOutput, [64]byte) {
	t.Helper()
	main := filepath.Join(dir, "result.v4")
	if err := os.WriteFile(main, []byte("previous bytes"), 0o600); err != nil {
		t.Fatalf("write previous main: %v", err)
	}
	bound, bindFailure := bindPrevious(prepared, nil)
	if bindFailure != nil {
		t.Fatalf("bind previous: %v", bindFailure)
	}
	return bound, sha512.Sum512([]byte("previous bytes"))
}

// assertAttemptFacts pins the fixed portable facts of one published
// attempt result (Rust success_returns_exact_published_facts).
func assertAttemptFacts(t *testing.T, result *PublicationResult, prepared *preparedOutput, sum [64]byte) {
	t.Helper()
	attempt := result.Attempt
	if attempt.DatabaseID != testFixtureDBID {
		t.Fatalf("database id %x, want fixture", attempt.DatabaseID)
	}
	if attempt.TransactionID != 1 {
		t.Fatalf("transaction id %d, want 1", attempt.TransactionID)
	}
	if attempt.CommitNonce != testFixtureNonce {
		t.Fatalf("commit nonce %x, want fixture", attempt.CommitNonce)
	}
	identity := prepared.attempt.identityOf()
	wantOutput := *cleanupLocalIdentity(&identity)
	if attempt.OutputIdentity != wantOutput {
		t.Fatalf("output identity %+v, want %+v", attempt.OutputIdentity, wantOutput)
	}
	if attempt.OutputByteLength != prepared.byteLength {
		t.Fatalf("output byte length %d, want %d", attempt.OutputByteLength, prepared.byteLength)
	}
	if attempt.OutputSHA512 != sum {
		t.Fatalf("output sha512 %x, want %x", attempt.OutputSHA512, sum)
	}
	if string(attempt.DestinationBasename) != "result.v4" {
		t.Fatalf("destination basename %q, want result.v4", attempt.DestinationBasename)
	}
	if attempt.PublicationAttemptID != prepared.attempt.attemptIDOf() {
		t.Fatal("publication attempt id differs from the prepared attempt")
	}
	if attempt.PublicationPolicy != PolicyFailIfExists {
		t.Fatalf("publication policy %v, want fail-if-exists", attempt.PublicationPolicy)
	}
}

func TestAttemptSuccessReturnsExactPublishedFactsAndNoResidue(t *testing.T) {
	dir := t.TempDir()
	prepared, sum := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	result, failure := failIfExistsCancellable(prepared, func() error { return nil })
	if failure != nil {
		t.Fatalf("publish preparation: %v", failure)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.DestinationContent != DestinationContentDesired {
		t.Fatalf("destination content = %v, want desired", result.DestinationContent)
	}
	if !result.MainNamespaceMayHaveBeenAttempted {
		t.Fatal("main namespace was not marked attempted")
	}
	if result.MainAccessPolicy != AccessPolicyCreatorOnly {
		t.Fatalf("main access policy = %v, want creator only", result.MainAccessPolicy)
	}
	if result.CoordinationAccessPolicy != AccessPolicyAbsent {
		t.Fatalf("coordination access policy = %v, want absent", result.CoordinationAccessPolicy)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("published result carries cleanup residue")
	}
	if result.Cause != nil {
		t.Fatalf("published result cause = %v, want nil", result.Cause)
	}
	assertAttemptFacts(t, &result, prepared, sum)

	// The main names the output inode; every private artifact is gone.
	entry, present, err := d.directory().Entry(d.mainName())
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present || entry.Identity != prepared.attempt.identityOf() {
		t.Fatal("main does not carry the output inode")
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present")
	}
	reservation, err := d.reservationName(prepared.attempt.attemptIDOf())
	if err != nil {
		t.Fatalf("reservation name: %v", err)
	}
	if _, present, err := d.directory().Entry(reservation); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present")
	}
}

func TestAttemptPreBoundaryFailureReturnsPreparationErrorAfterExactCleanup(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(
		map[attemptPoint]error{attemptPointReservationCreated: attemptTestInjected()}, nil), false, noopAttemptObserver)
	if failure == nil {
		t.Fatal("publish succeeded despite the injected reservation-created failure")
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("result publication = %v, want the zero result", result.Publication)
	}
	assertProblemCodeDetail(t, failure.Cause, format.CodeIO, "injected attempt checkpoint failure")
	if failure.CleanupState() != CleanupStateClean {
		t.Fatal("preparation failure carries cleanup residue")
	}
	if string(failure.PrivateOutputBasename) != prepared.attempt.nameOf() {
		t.Fatalf("private output basename %q, want %q", failure.PrivateOutputBasename, prepared.attempt.nameOf())
	}
	// The draft reservation and the output were both discarded.
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present after the preparation failure")
	}
	reservation, err := d.reservationName(prepared.attempt.attemptIDOf())
	if err != nil {
		t.Fatalf("reservation name: %v", err)
	}
	if _, present, err := d.directory().Entry(reservation); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present after the preparation failure")
	}
	if _, present, err := d.directory().Entry(d.mainName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("main name present after the preparation failure")
	}
}

func TestAttemptState1FailureIsNotPublishedAndCleansBothArtifacts(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(
		map[attemptPoint]error{attemptPointState1Selected: attemptTestInjected()}, nil), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("state1 failure returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.DestinationContent != DestinationContentAbsent {
		t.Fatalf("destination content = %v, want absent", result.DestinationContent)
	}
	if result.MainNamespaceMayHaveBeenAttempted {
		t.Fatal("main namespace marked attempted before state 2")
	}
	if result.CoordinationAccessPolicy != AccessPolicyAbsent {
		t.Fatalf("coordination access policy = %v, want absent", result.CoordinationAccessPolicy)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("state1 failure carries cleanup residue")
	}
	assertProblemCodeDetail(t, result.Cause, format.CodeIO, "injected attempt checkpoint failure")
	// All four names are gone (Rust assert_no_attempt_files).
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	reservation, err := d.reservationName(prepared.attempt.attemptIDOf())
	if err != nil {
		t.Fatalf("reservation name: %v", err)
	}
	if _, present, err := d.directory().Entry(reservation); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present")
	}
	if _, present, err := d.directory().Entry(d.mainName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("main name present after the state1 failure")
	}
}

func TestAttemptAcquiredState1FailureRetiresTheCanonicalReservation(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(
		map[attemptPoint]error{attemptPointReservationAcquired: attemptTestInjected()}, nil), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("acquired failure returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("acquired failure carries cleanup residue")
	}
	assertProblemCodeDetail(t, result.Cause, format.CodeIO, "injected attempt checkpoint failure")
	// The canonical reservation was retired with the output.
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present")
	}
}

func TestAttemptState2FailureRetainsResolverAuthorityWithoutCleanupResidue(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(
		map[attemptPoint]error{attemptPointState2Selected: attemptTestInjected()}, nil), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("state2 failure returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationOutcomeUnknown {
		t.Fatalf("publication = %v, want outcome unknown", result.Publication)
	}
	if result.DestinationContent != DestinationContentUnclassified {
		t.Fatalf("destination content = %v, want unclassified", result.DestinationContent)
	}
	if !result.MainNamespaceMayHaveBeenAttempted {
		t.Fatal("main namespace not marked attempted after state 2")
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("state2 failure carries cleanup residue")
	}
	assertProblemCodeDetail(t, result.Cause, format.CodeIO, "injected attempt checkpoint failure")
	// The resolver authority is untouched: the private output and the
	// armed canonical reservation both remain (Rust
	// state2_failure_retains_resolver_authority).
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("private output name removed after the state2 failure")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("coordination name removed after the state2 failure")
	}
	if _, present, err := d.directory().Entry(d.mainName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("main name present after the state2 failure")
	}
}

func TestAttemptMainRaceAfterState2IsOutcomeUnknownAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	// The racing main appears at the last pre-rename checkpoint; the
	// machine must refuse to overwrite it and report the unprovable
	// outcome (Rust main_race_after_state2).
	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(nil, func(point attemptPoint) {
		if point == attemptPointState2Selected {
			if err := os.WriteFile(filepath.Join(dir, "result.v4"), []byte("racing-main"), 0o600); err != nil {
				t.Fatalf("write racing main: %v", err)
			}
		}
	}), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("racing main returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationOutcomeUnknown {
		t.Fatalf("publication = %v, want outcome unknown", result.Publication)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("racing main outcome carries cleanup residue")
	}
	bytes, err := os.ReadFile(filepath.Join(dir, "result.v4"))
	if err != nil {
		t.Fatalf("read racing main: %v", err)
	}
	if string(bytes) != "racing-main" {
		t.Fatalf("main content %q, want the racing bytes untouched", bytes)
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("private output name removed after the race")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("coordination name removed after the race")
	}
}

func TestAttemptFailedSharedDirectorySyncLedgersBothUnlinkedArtifacts(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(
		map[attemptPoint]error{
			attemptPointState1Selected:       attemptTestInjected(),
			attemptPointCleanupDirectorySync: attemptTestInjected(),
		}, nil), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("failed sync returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.CleanupState() != CleanupStateResiduePossible {
		t.Fatalf("cleanup state = %v, want residue possible", result.CleanupState())
	}
	if result.Cleanup.Len() != 2 {
		t.Fatalf("cleanup artifacts = %d, want 2", result.Cleanup.Len())
	}
	firstArtifact, secondArtifact := result.Cleanup.At(0), result.Cleanup.At(1)
	if firstArtifact.Kind != ArtifactPrivateOutput || secondArtifact.Kind != ArtifactPrivateReservation {
		t.Fatalf("artifact kinds = (%v, %v), want (private output, private reservation)", firstArtifact.Kind, secondArtifact.Kind)
	}
	if !sameProblem(firstArtifact.Error, attemptTestInjected()) || !sameProblem(secondArtifact.Error, attemptTestInjected()) {
		t.Fatal("artifact errors do not carry the injected sync problem")
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	reservation, err := d.reservationName(prepared.attempt.attemptIDOf())
	if err != nil {
		t.Fatalf("reservation name: %v", err)
	}
	if _, present, err := d.directory().Entry(reservation); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present")
	}
}

func TestAttemptIndividualCleanupFailuresReportOnlyTheExactOwnedArtifact(t *testing.T) {
	for _, tc := range []struct {
		point    attemptPoint
		kind     ArtifactKind
		basename func(t *testing.T, prepared *preparedOutput, d *destination) string
		file     string
	}{
		{attemptPointCleanupOutput, ArtifactPrivateOutput, func(t *testing.T, prepared *preparedOutput, d *destination) string {
			return prepared.attempt.nameOf()
		}, "private output"},
		{attemptPointCleanupReservation, ArtifactPrivateReservation, func(t *testing.T, prepared *preparedOutput, d *destination) string {
			name, err := d.reservationName(prepared.attempt.attemptIDOf())
			if err != nil {
				t.Fatalf("reservation name: %v", err)
			}
			return name
		}, "private reservation"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir := t.TempDir()
			prepared, _ := attemptTestPrepared(t, dir, "result.v4")
			defer prepared.Close()
			d := testBoundDestination(t, dir)

			result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(
				map[attemptPoint]error{
					attemptPointState1Selected: attemptTestInjected(),
					tc.point:                   attemptTestInjected(),
				}, nil), false, noopAttemptObserver)
			if failure != nil {
				t.Fatalf("cleanup failure returned a preparation failure: %v", failure)
			}
			if result.Cleanup.Len() != 1 {
				t.Fatalf("cleanup artifacts = %d, want 1", result.Cleanup.Len())
			}
			expectedName := tc.basename(t, prepared, d)
			// The failing removal leaves its artifact on disk; its
			// retained identity is the exact ledger identity.
			survivor, err := os.Open(filepath.Join(dir, expectedName))
			if err != nil {
				t.Fatalf("open %s survivor: %v", tc.file, err)
			}
			identity, err := live.RegularIdentity(survivor, d.directory().Identity())
			survivor.Close()
			if err != nil {
				t.Fatalf("identity of %s survivor: %v", tc.file, err)
			}
			assertCleanupArtifact(t, result.Cleanup.At(0), tc.kind, expectedName, cleanupLocalIdentity(&identity), creationSecurityForExpected(t, prepared), attemptTestInjected())
		})
	}
}

// creationSecurityForExpected returns the portable creation-security
// facts the machine captured for one prepared attempt (the seed
// security profile; the cleanup ledger carries the value copy).
func creationSecurityForExpected(t *testing.T, prepared *preparedOutput) CreationSecurity {
	d := prepared.attempt.destinationOf()
	return CreationSecurity{Kind: creationSecurityKind, Commitment: d.securityCommitment()}
}

func TestAttemptForeignCoordinationIsPreservedWhileOwnedArtifactsAreCleaned(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	coordination := filepath.Join(dir, d.coordinationName())
	if err := os.WriteFile(coordination, []byte("foreign"), 0o600); err != nil {
		t.Fatalf("write foreign coordination: %v", err)
	}

	result, failure := failIfExistsCancellable(prepared, func() error { return nil })
	if failure != nil {
		t.Fatalf("foreign coordination publish preparation: %v", failure)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.DestinationContent != DestinationContentAbsent {
		t.Fatalf("destination content = %v, want absent", result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("foreign coordination outcome carries cleanup residue")
	}
	assertProblemCodeDetail(t, result.Cause, format.CodeNameExists, "publication name already exists")
	bytes, err := os.ReadFile(coordination)
	if err != nil {
		t.Fatalf("read foreign coordination: %v", err)
	}
	if string(bytes) != "foreign" {
		t.Fatalf("foreign coordination content %q, want untouched", bytes)
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	reservation, err := d.reservationName(prepared.attempt.attemptIDOf())
	if err != nil {
		t.Fatalf("reservation name: %v", err)
	}
	if _, present, err := d.directory().Entry(reservation); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present")
	}
}

func TestAttemptExistingMainIsNeverRemovedOrClassifiedWithoutReadingIt(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	main := filepath.Join(dir, "result.v4")
	if err := os.WriteFile(main, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write existing main: %v", err)
	}

	result, failure := failIfExistsCancellable(prepared, func() error { return nil })
	if failure != nil {
		t.Fatalf("existing main publish preparation: %v", failure)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.DestinationContent != DestinationContentUnclassified {
		t.Fatalf("destination content = %v, want unclassified", result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("existing main outcome carries cleanup residue")
	}
	assertProblemCodeDetail(t, result.Cause, format.CodeNameExists, "publication name already exists")
	bytes, err := os.ReadFile(main)
	if err != nil {
		t.Fatalf("read existing main: %v", err)
	}
	if string(bytes) != "existing" {
		t.Fatalf("main content %q, want untouched", bytes)
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present")
	}
}

func TestAttemptPublishedReservationConflictIsReportedAsCleanupResidue(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	// A hard alias appears at the desired-proof observation; the
	// retirement then trips the single-link custody rule and the
	// coordination is ledged as residue (Rust
	// published_reservation_conflict_is_reported_as_cleanup_residue).
	extra := filepath.Join(dir, "extra-link")
	coordination := filepath.Join(dir, d.coordinationName())
	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(nil, func(point attemptPoint) {
		if point == attemptPointDesiredProven {
			if err := os.Link(coordination, extra); err != nil {
				t.Fatalf("hard link coordination: %v", err)
			}
		}
	}), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("hard-linked publish preparation: %v", failure)
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
	if result.Cleanup.Len() != 1 || result.Cleanup.At(0).Kind != ArtifactPrivateReservation {
		t.Fatalf("cleanup artifacts = %d, want one private-reservation artifact", result.Cleanup.Len())
	}
	if string(result.Cleanup.At(0).Basename) != d.coordinationName() {
		t.Fatalf("artifact basename %q, want the coordination name", result.Cleanup.At(0).Basename)
	}
	assertProblemCodeDetail(t, result.Cause, format.CodeConflict, "publication inode link count changed")
	if result.CoordinationAccessPolicy != AccessPolicyChangedOrUnproven {
		t.Fatalf("coordination access policy = %v, want changed-or-unproven", result.CoordinationAccessPolicy)
	}
	if _, present, err := d.directory().Entry(d.mainName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("main name missing after the hard-linked retirement")
	}
	if _, present, err := d.directory().Entry(coordination); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("coordination name missing after the hard-linked retirement")
	}
	if _, err := os.Lstat(extra); err != nil {
		t.Fatalf("extra link missing: %v", err)
	}
}

func TestAttemptPostProofFailureRemainsPublishedAndCleanupStillRuns(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	d := testBoundDestination(t, dir)

	result, failure := publishWithObserver(prepared, nil, attemptTestCheckpoint(
		map[attemptPoint]error{attemptPointDesiredProven: attemptTestInjected()}, nil), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("post-proof failure returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("post-proof failure carries cleanup residue")
	}
	assertProblemCodeDetail(t, result.Cause, format.CodeIO, "injected attempt checkpoint failure")
	if result.CoordinationAccessPolicy != AccessPolicyAbsent {
		t.Fatalf("coordination access policy = %v, want absent", result.CoordinationAccessPolicy)
	}
	if _, present, err := d.directory().Entry(d.mainName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("main name missing after the post-proof failure")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present after the post-proof failure")
	}
}

func TestAttemptReplacementPublishesExactOutputAndRetiresThePreviousInode(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	bound, previousDigest := attemptTestReplacement(t, prepared, dir)
	d := testBoundDestination(t, dir)

	result, failure := replaceExistingCancellable(bound, func() error { return nil })
	if failure != nil {
		t.Fatalf("replacement publish preparation: %v", failure)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.DestinationContent != DestinationContentDesired {
		t.Fatalf("destination content = %v, want desired", result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("replacement outcome carries cleanup residue")
	}
	previous := result.Attempt.PreviousDestination
	if previous == nil {
		t.Fatal("replacement result carries no previous destination facts")
	}
	if previous.SHA512 != previousDigest {
		t.Fatalf("previous sha512 %x, want %x", previous.SHA512, previousDigest)
	}
	if previous.ByteLength != uint64(len("previous bytes")) {
		t.Fatalf("previous byte length %d, want %d", previous.ByteLength, len("previous bytes"))
	}
	entry, present, err := d.directory().Entry(d.mainName())
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present || entry.Identity != prepared.attempt.identityOf() {
		t.Fatal("main does not carry the replacement output inode")
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present")
	}
}

func TestAttemptReplacementState1FailurePreservesAndClassifiesPreviousBytes(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	bound, _ := attemptTestReplacement(t, prepared, dir)
	d := testBoundDestination(t, dir)

	result, failure := publishWithObserver(bound, nil, attemptTestCheckpoint(
		map[attemptPoint]error{attemptPointState1Selected: attemptTestInjected()}, nil), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("replacement state1 failure returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.DestinationContent != DestinationContentPrevious {
		t.Fatalf("destination content = %v, want previous", result.DestinationContent)
	}
	bytes, err := os.ReadFile(filepath.Join(dir, "result.v4"))
	if err != nil {
		t.Fatalf("read previous main: %v", err)
	}
	if string(bytes) != "previous bytes" {
		t.Fatalf("main content %q, want the previous bytes untouched", bytes)
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	reservation, err := d.reservationName(prepared.attempt.attemptIDOf())
	if err != nil {
		t.Fatalf("reservation name: %v", err)
	}
	if _, present, err := d.directory().Entry(reservation); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present")
	}
}

func TestAttemptReplacementPathRaceIsDetectedBeforeState2(t *testing.T) {
	dir := t.TempDir()
	prepared, _ := attemptTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	bound, _ := attemptTestReplacement(t, prepared, dir)
	d := testBoundDestination(t, dir)

	displaced := filepath.Join(dir, "displaced")
	main := filepath.Join(dir, "result.v4")
	result, failure := publishWithObserver(bound, nil, attemptTestCheckpoint(nil, func(point attemptPoint) {
		if point == attemptPointReservationAcquired {
			if err := os.Rename(main, displaced); err != nil {
				t.Fatalf("rename racing main: %v", err)
			}
			if err := os.WriteFile(main, []byte("racing bytes"), 0o600); err != nil {
				t.Fatalf("write racing main: %v", err)
			}
		}
	}), false, noopAttemptObserver)
	if failure != nil {
		t.Fatalf("replacement race returned a preparation failure: %v", failure)
	}
	if result.Publication != PublicationNotPublished {
		t.Fatalf("publication = %v, want not published", result.Publication)
	}
	if result.DestinationContent != DestinationContentUnclassified {
		t.Fatalf("destination content = %v, want unclassified", result.DestinationContent)
	}
	bytes, err := os.ReadFile(main)
	if err != nil {
		t.Fatalf("read racing main: %v", err)
	}
	if string(bytes) != "racing bytes" {
		t.Fatalf("main content %q, want the racing bytes", bytes)
	}
	displacedBytes, err := os.ReadFile(displaced)
	if err != nil {
		t.Fatalf("read displaced previous: %v", err)
	}
	if string(displacedBytes) != "previous bytes" {
		t.Fatalf("displaced content %q, want the previous bytes", displacedBytes)
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present")
	}
}

func TestAttemptResumeArmedBeforeRenamePublishes(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create reservation draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatalf("initialize reservation: %v", initFailure)
	}
	canonical, acquireFailure := private.acquire(prepared)
	if acquireFailure != nil {
		t.Fatalf("acquire reservation: %v", acquireFailure)
	}
	armed, armFailure := canonical.arm(prepared)
	if armFailure != nil {
		t.Fatalf("arm reservation: %v", armFailure)
	}
	defer armed.Close()

	result := resumeArmed(captureSeed(prepared), prepared, armed)
	if result.Publication != PublicationPublished {
		t.Fatalf("resume publication = %v, want published", result.Publication)
	}
	if result.DestinationContent != DestinationContentDesired {
		t.Fatalf("destination content = %v, want desired", result.DestinationContent)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("resume outcome carries cleanup residue")
	}
	if result.Cause != nil {
		t.Fatalf("resume cause = %v, want nil", result.Cause)
	}
	entry, present, err := d.directory().Entry(d.mainName())
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present || entry.Identity != prepared.attempt.identityOf() {
		t.Fatal("main does not carry the resumed output inode")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("coordination name still present after the resume")
	}
}

func TestAttemptResumeArmedAfterRenameIsOutcomeUnknown(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	draft, err := createReservationDraft(prepared)
	if err != nil {
		t.Fatalf("create reservation draft: %v", err)
	}
	private, initFailure := draft.initialize(prepared)
	if initFailure != nil {
		t.Fatalf("initialize reservation: %v", initFailure)
	}
	canonical, acquireFailure := private.acquire(prepared)
	if acquireFailure != nil {
		t.Fatalf("acquire reservation: %v", acquireFailure)
	}
	armed, armFailure := canonical.arm(prepared)
	if armFailure != nil {
		t.Fatalf("arm reservation: %v", armFailure)
	}
	defer armed.Close()

	// Simulate the after_main_rename crash state: the output is at
	// the main name before the resume runs (Rust resume_armed treats
	// any pre-rename verification failure as the unprovable outcome).
	if err := d.directory().RenameNoReplace(prepared.attempt.nameOf(), prepared.file, d.mainName()); err != nil {
		t.Fatalf("rename output to main: %v", err)
	}

	result := resumeArmed(captureSeed(prepared), prepared, armed)
	if result.Publication != PublicationOutcomeUnknown {
		t.Fatalf("resume publication = %v, want outcome unknown", result.Publication)
	}
	if !result.MainNamespaceMayHaveBeenAttempted {
		t.Fatal("resume outcome did not mark the main namespace attempted")
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatal("resume outcome carries cleanup residue")
	}
	// The renamed main and the armed reservation are untouched.
	entry, present, err := d.directory().Entry(d.mainName())
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	if !present || entry.Identity != prepared.attempt.identityOf() {
		t.Fatal("main does not carry the resumed output inode")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("coordination name removed by the resume")
	}
}
