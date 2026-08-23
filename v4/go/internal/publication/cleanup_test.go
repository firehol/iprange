//go:build linux

// Cleanup machine tests (Rust publication/cleanup.rs unix arms): the
// early discards of created and attempted outputs, the fixed cleanup
// conflicts of the link-count and name-slot proof arms, the removal
// order for private/canonical/either reservation owners, the
// checkpoint order of discard_with, the recovered no-checkpoint
// discard, the summary absence flags, and the exact artifact facts
// drawn from the result seed.

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// cleanupTestPrepared builds one really prepared output of a secured
// attempt inside dir (Rust output.rs prepare path, like
// output_prepared_test.go).
func cleanupTestPrepared(t *testing.T, dir, mainName string) *preparedOutput {
	t.Helper()
	attempt, file := testSecuredAttempt(t, dir, mainName)
	finished, _ := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare output: %v", failure)
	}
	return prepared
}

// cleanupTestReservation creates one private reservation file and
// returns its file, identity, and private name (the reservation
// content is irrelevant: the discard machine proves names and link
// counts only).
func cleanupTestReservation(t *testing.T, d *destination, attemptID [16]byte) (*os.File, live.FileIdentity, string) {
	t.Helper()
	name, err := d.reservationName(attemptID)
	if err != nil {
		t.Fatalf("reservation name: %v", err)
	}
	file, err := d.create(name)
	if err != nil {
		t.Fatalf("create reservation file: %v", err)
	}
	identity, err := live.RegularIdentity(file, d.directory().Identity())
	if err != nil {
		file.Close()
		t.Fatalf("reservation identity: %v", err)
	}
	return file, identity, name
}

// recordingCleanupCheckpoint records every checkpoint call and fails
// the configured points (Rust tests inject the same closure shape).
type recordingCleanupCheckpoint struct {
	calls []cleanupPoint
	fail  map[cleanupPoint]error
}

func (c *recordingCleanupCheckpoint) run(p cleanupPoint) error {
	c.calls = append(c.calls, p)
	return c.fail[p]
}

func (c *recordingCleanupCheckpoint) want(t *testing.T, points ...cleanupPoint) {
	t.Helper()
	if len(c.calls) != len(points) {
		t.Fatalf("checkpoint calls = %v, want %v", c.calls, points)
	}
	for i := range points {
		if c.calls[i] != points[i] {
			t.Fatalf("checkpoint calls = %v, want %v", c.calls, points)
		}
	}
}

// assertCleanupArtifact pins one exact ledger artifact of the discard
// machine (Rust CleanupArtifact facts). identity is the portable
// local identity the artifact must carry (nil for the absent arm).
func assertCleanupArtifact(t *testing.T, artifact *CleanupArtifact, kind ArtifactKind, basename string, identity *LocalFileIdentity, security CreationSecurity, problem error) {
	t.Helper()
	if artifact == nil {
		t.Fatal("cleanup artifact is nil")
	}
	if artifact.Kind != kind {
		t.Fatalf("artifact kind %v, want %v", artifact.Kind, kind)
	}
	if artifact.DirectoryRole != DirectoryRoleDestination {
		t.Fatalf("artifact directory role %v", artifact.DirectoryRole)
	}
	if string(artifact.Basename) != basename {
		t.Fatalf("artifact basename %q, want %q", artifact.Basename, basename)
	}
	if artifact.BasenameEncoding != basenameEncodingKind {
		t.Fatalf("artifact basename encoding %d", artifact.BasenameEncoding)
	}
	if (artifact.Identity == nil) != (identity == nil) {
		t.Fatalf("artifact identity %v, want %v", artifact.Identity, identity)
	}
	if identity != nil && *artifact.Identity != *identity {
		t.Fatalf("artifact identity %+v, want %+v", *artifact.Identity, *identity)
	}
	if artifact.CreationSecurity == nil || *artifact.CreationSecurity != security {
		t.Fatalf("artifact creation security %+v, want %+v", artifact.CreationSecurity, security)
	}
	if artifact.UnpublishedTail != nil {
		t.Fatalf("artifact unpublished tail %+v, want nil", artifact.UnpublishedTail)
	}
	if !sameProblem(artifact.Error, problem) {
		t.Fatalf("artifact error %v, want %v", artifact.Error, problem)
	}
}

// sameProblem compares two problems by code and detail, the Go peer
// of the Rust Problem value equality (fresh constructions of one
// fixed problem are distinct pointers but equal values).
func sameProblem(a, b error) bool {
	var fa, fb *format.Error
	if errors.As(a, &fa) && errors.As(b, &fb) {
		return fa.Code == fb.Code && fa.Detail == fb.Detail
	}
	return a == b
}

// cleanupLocalIdentity converts one retained identity to its portable
// local form (Rust namespace::local_identity).
func cleanupLocalIdentity(identity *live.FileIdentity) *LocalFileIdentity {
	if identity == nil {
		return nil
	}
	converted := localIdentityFromDeviceInode(live.IdentityDeviceInode(identity))
	return &converted
}

func TestDiscardCreatedRemovesPrivateOutput(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	created, err := createOutput(filepath.Join(dir, "result.v4"))
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	discard := discardCreated(created)
	if discard.artifact != nil {
		t.Fatalf("discard artifact %+v, want nil", discard.artifact)
	}
	if discard.output.Identity == nil {
		t.Fatal("created output facts carry no identity")
	}
	if _, present, err := d.directory().Entry(created.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present after discard")
	}
	count, err := live.RegularLinkCount(created.fileHandle())
	if err != nil {
		t.Fatalf("link count: %v", err)
	}
	if count != 0 {
		t.Fatalf("output links %d after discard, want 0", count)
	}
	if discard.housekeeping != HousekeepingNone {
		t.Fatalf("housekeeping %v", discard.housekeeping)
	}
	if len(discard.visibleHousekeeping) != 0 {
		t.Fatalf("visible housekeeping %v", discard.visibleHousekeeping)
	}
}

func TestDiscardCreatedIdentityNotEstablishedConflict(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	created, err := createOutput(filepath.Join(dir, "result.v4"))
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	problem := discardOne(d.directory(), created.nameOf(), created.fileHandle(), nil)
	assertProblemCodeDetail(t, problem, format.CodeCleanupConflict, "private output identity was not established")
}

func TestDiscardAttemptRemovesPrivateOutput(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	defer file.Close()
	discard := discardAttempt(&attempt, file)
	if discard.artifact != nil {
		t.Fatalf("discard artifact %+v, want nil", discard.artifact)
	}
	if _, present, err := d.directory().Entry(attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present after discard")
	}
	count, err := live.RegularLinkCount(file)
	if err != nil {
		t.Fatalf("link count: %v", err)
	}
	if count != 0 {
		t.Fatalf("output links %d after discard, want 0", count)
	}
}

func TestFailedAttemptCarriesExactArtifact(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	defer file.Close()
	facts := attempt.facts()
	problem := cleanupConflictProblem("injected discard failure")
	discard := failedAttempt(facts, problem)
	if discard.artifact == nil {
		t.Fatal("failed attempt has no artifact")
	}
	assertCleanupArtifact(t, discard.artifact, ArtifactPrivateOutput, attempt.nameOf(), facts.Identity, facts.CreationSecurity, problem)
	if !reflect.DeepEqual(discard.output, facts) {
		t.Fatalf("discard output facts %+v, want %+v", discard.output, facts)
	}
	// failedAttempt performs no namespace work: the name must remain.
	if _, present, err := d.directory().Entry(attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("private output name removed by failedAttempt")
	}
}

func TestConfirmedAbsentRunsNoNamespaceWork(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	defer file.Close()
	discard := confirmedAbsent(attempt.facts())
	if discard.artifact != nil {
		t.Fatalf("confirmed absent artifact %+v, want nil", discard.artifact)
	}
	if _, present, err := d.directory().Entry(attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("private output name removed by confirmedAbsent")
	}
}

func TestDiscardWithOutputOnly(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	seed := captureSeed(prepared)
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{}}
	summary := discardWith(&seed, prepared, nil, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval, cleanupPointDirectorySync)
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
	}
	if summary.housekeeping != HousekeepingNone {
		t.Fatalf("housekeeping %v", summary.housekeeping)
	}
	if !summary.mainAbsent || !summary.coordinationAbsent {
		t.Fatalf("absence flags main=%v coordination=%v, want true true", summary.mainAbsent, summary.coordinationAbsent)
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present after discardWith")
	}
	count, err := live.RegularLinkCount(prepared.file)
	if err != nil {
		t.Fatalf("link count: %v", err)
	}
	if count != 0 {
		t.Fatalf("output links %d after discardWith, want 0", count)
	}
}

func TestDiscardWithAlreadyAbsentOutput(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	name := prepared.attempt.nameOf()
	if _, err := d.directory().UnlinkExact(name, prepared.attempt.identityOf()); err != nil {
		t.Fatalf("pre-unlink output: %v", err)
	}
	seed := captureSeed(prepared)
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{}}
	summary := discardWith(&seed, prepared, nil, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval, cleanupPointDirectorySync)
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty (zero-link removal needs no unlink)", summary.artifacts.Slice())
	}
}

func TestDiscardWithPrivateReservation(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	owner := &reservationOwner{
		file:        reservationFile,
		identity:    &identity,
		privateName: name,
		location:    ownerLocationPrivate,
	}
	seed := captureSeed(prepared)
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{}}
	summary := discardWith(&seed, prepared, owner, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval, cleanupPointReservationRemoval, cleanupPointDirectorySync)
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
	}
	if _, present, err := d.directory().Entry(name); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present after discard")
	}
	count, err := live.RegularLinkCount(reservationFile)
	if err != nil {
		t.Fatalf("link count: %v", err)
	}
	if count != 0 {
		t.Fatalf("reservation links %d after discard, want 0", count)
	}
}

func TestDiscardWithCanonicalReservationRemovesPrivateName(t *testing.T) {
	// Canonical location with only the private name retained (the
	// canonical twin never appeared): the canonical candidate is
	// skipped and the private name proves the removal.
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	owner := &reservationOwner{
		file:        reservationFile,
		identity:    &identity,
		privateName: name,
		location:    ownerLocationCanonical,
	}
	seed := captureSeed(prepared)
	summary := discardWith(&seed, prepared, owner, func(cleanupPoint) error { return nil })
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
	}
	if _, present, err := d.directory().Entry(name); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present after discard")
	}
}

func TestDiscardWithCanonicalReservationRemovesCanonicalName(t *testing.T) {
	// Canonical location with only the canonical twin retained: the
	// canonical name proves the removal, the private candidate is
	// skipped as missing.
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	canonical := d.coordinationName()
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, canonical)); err != nil {
		t.Fatalf("rename reservation to canonical: %v", err)
	}
	owner := &reservationOwner{
		file:        reservationFile,
		identity:    &identity,
		privateName: name,
		location:    ownerLocationCanonical,
	}
	seed := captureSeed(prepared)
	summary := discardWith(&seed, prepared, owner, func(cleanupPoint) error { return nil })
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
	}
	if _, present, err := d.directory().Entry(canonical); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("canonical reservation name still present after discard")
	}
}

func TestDiscardWithEitherReservationPreferPrivate(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	owner := &reservationOwner{
		file:        reservationFile,
		identity:    &identity,
		privateName: name,
		location:    ownerLocationEither,
	}
	seed := captureSeed(prepared)
	summary := discardWith(&seed, prepared, owner, func(cleanupPoint) error { return nil })
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
	}
	if _, present, err := d.directory().Entry(name); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("either-location private name still present after discard")
	}
}

func TestRemoveReservationUnexpectedLinksConflict(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	// A second name makes the reservation inode have two links; the
	// removal contract requires exactly one.
	if err := os.Link(filepath.Join(dir, name), filepath.Join(dir, d.coordinationName())); err != nil {
		t.Fatalf("link reservation alias: %v", err)
	}
	owner := &reservationOwner{
		file:        reservationFile,
		identity:    &identity,
		privateName: name,
		location:    ownerLocationPrivate,
	}
	_, err := removeReservation(d.directory(), d.coordinationName(), *owner)
	assertProblemCodeDetail(t, err, format.CodeCleanupConflict, "owned publication artifact has unexpected links")
}

func TestRemoveReservationUnexpectedLinksIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	if err := os.Link(filepath.Join(dir, name), filepath.Join(dir, d.coordinationName())); err != nil {
		t.Fatalf("link reservation alias: %v", err)
	}
	owner := &reservationOwner{file: reservationFile, identity: &identity, privateName: name, location: ownerLocationPrivate}
	seed := captureSeed(prepared)
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{}}
	summary := discardWith(&seed, prepared, owner, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval, cleanupPointReservationRemoval, cleanupPointDirectorySync)
	artifact := summary.artifacts.At(0)
	if artifact == nil {
		t.Fatal("ledger empty, want the unexpected-links artifact")
	}
	assertCleanupArtifact(t, artifact, ArtifactPrivateReservation, name, cleanupLocalIdentity(&identity), seed.creationSecurity,
		cleanupConflictProblem("owned publication artifact has unexpected links"))
	if summary.artifacts.Len() != 1 {
		t.Fatalf("ledger %+v, want one artifact", summary.artifacts.Slice())
	}
	// The conflicted removal changes nothing: both names remain.
	if _, present, err := d.directory().Entry(name); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("private reservation name removed by the conflicted discard")
	}
	if _, present, err := d.directory().Entry(d.coordinationName()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("canonical alias removed by the conflicted discard")
	}
	count, err := live.RegularLinkCount(reservationFile)
	if err != nil {
		t.Fatalf("link count: %v", err)
	}
	if count != 2 {
		t.Fatalf("reservation links %d after conflict, want 2", count)
	}
}

func TestDiscardNoExactRetainedNameConflict(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	// Rename the retained name away: the inode still has one link,
	// but none of the machine's candidate names.
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, "foreign.tmp")); err != nil {
		t.Fatalf("rename reservation away: %v", err)
	}
	owner := &reservationOwner{file: reservationFile, identity: &identity, privateName: name, location: ownerLocationPrivate}
	seed := captureSeed(prepared)
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{}}
	summary := discardWith(&seed, prepared, owner, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval, cleanupPointReservationRemoval, cleanupPointDirectorySync)
	artifact := summary.artifacts.At(0)
	assertCleanupArtifact(t, artifact, ArtifactPrivateReservation, name, cleanupLocalIdentity(&identity), seed.creationSecurity,
		cleanupConflictProblem("owned publication artifact has no exact retained name"))
	if _, present, err := d.directory().Entry("foreign.tmp"); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("foreign name removed by the conflicted discard")
	}
}

func TestDiscardCheckpointFailsOutputRemoval(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	seed := captureSeed(prepared)
	injected := problem(format.CodeIO, "injected checkpoint failure")
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{cleanupPointOutputRemoval: injected}}
	summary := discardWith(&seed, prepared, nil, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval)
	if summary.artifacts.Len() != 1 {
		t.Fatalf("ledger %+v, want one artifact", summary.artifacts.Slice())
	}
	outputIdentity := prepared.attempt.identityOf()
	assertCleanupArtifact(t, summary.artifacts.At(0), ArtifactPrivateOutput, prepared.attempt.nameOf(),
		cleanupLocalIdentity(&outputIdentity), seed.creationSecurity, injected)
	// The checkpoint refusal performs no namespace work.
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("private output name removed despite the checkpoint refusal")
	}
}

func TestDiscardCheckpointFailsReservationRemoval(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	owner := &reservationOwner{file: reservationFile, identity: &identity, privateName: name, location: ownerLocationCanonical}
	seed := captureSeed(prepared)
	injected := problem(format.CodeIO, "injected checkpoint failure")
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{cleanupPointReservationRemoval: injected}}
	summary := discardWith(&seed, prepared, owner, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval, cleanupPointReservationRemoval, cleanupPointDirectorySync)
	if summary.artifacts.Len() != 1 {
		t.Fatalf("ledger %+v, want one artifact", summary.artifacts.Slice())
	}
	// The canonical-location default slot is the coordination name.
	assertCleanupArtifact(t, summary.artifacts.At(0), ArtifactPrivateReservation, d.coordinationName(), cleanupLocalIdentity(&identity),
		seed.creationSecurity, injected)
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present after the output checkpoint passed")
	}
}

func TestDiscardBothCheckpointsFailConsumeDistinctSlots(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	owner := &reservationOwner{file: reservationFile, identity: &identity, privateName: name, location: ownerLocationPrivate}
	seed := captureSeed(prepared)
	injected := problem(format.CodeIO, "injected checkpoint failure")
	checkpoint := &recordingCleanupCheckpoint{fail: map[cleanupPoint]error{
		cleanupPointOutputRemoval:      injected,
		cleanupPointReservationRemoval: injected,
	}}
	summary := discardWith(&seed, prepared, owner, checkpoint.run)
	checkpoint.want(t, cleanupPointOutputRemoval, cleanupPointReservationRemoval)
	if summary.artifacts.Len() != 2 {
		t.Fatalf("ledger %+v, want two artifacts", summary.artifacts.Slice())
	}
	if string(summary.artifacts.At(0).Basename) != prepared.attempt.nameOf() {
		t.Fatalf("first artifact basename %q", summary.artifacts.At(0).Basename)
	}
	if string(summary.artifacts.At(1).Basename) != name {
		t.Fatalf("second artifact basename %q", summary.artifacts.At(1).Basename)
	}
}

func TestCleanupSeedNameSlotConsumedOnce(t *testing.T) {
	dir := t.TempDir()
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	seed := captureSeed(prepared)
	identity := prepared.attempt.identityOf()
	problem := cleanupConflictProblem("injected")
	_ = seed.artifact(ArtifactPrivateOutput, nameSlotPrivateOutput, &identity, problem)
	defer func() {
		if recover() == nil {
			t.Fatal("double artifact name consumption did not panic")
		}
	}()
	_ = seed.artifact(ArtifactPrivateOutput, nameSlotPrivateOutput, &identity, problem)
}

func TestDiscardRecoveredRemovesBothOwners(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, identity, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	outputIdentity := prepared.attempt.identityOf()
	seed := captureSeed(prepared)
	output := &outputOwner{file: prepared.file, identity: outputIdentity, name: prepared.attempt.nameOf()}
	reservation := &reservationOwner{file: reservationFile, identity: &identity, privateName: name, location: ownerLocationPrivate}
	summary := discardRecovered(&seed, prepared.attempt.destinationOf(), output, reservation)
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
	}
	if _, present, err := d.directory().Entry(prepared.attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private output name still present after discardRecovered")
	}
	if _, present, err := d.directory().Entry(name); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present after discardRecovered")
	}
}

func TestDiscardRecoveredInfersReservationIdentity(t *testing.T) {
	// The reservation owner without an identity re-proves it from
	// the open file (Rust remove_reservation regular_identity arm).
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	reservationFile, _, name := cleanupTestReservation(t, d, prepared.attempt.attemptIDOf())
	defer reservationFile.Close()
	outputIdentity := prepared.attempt.identityOf()
	seed := captureSeed(prepared)
	output := &outputOwner{file: prepared.file, identity: outputIdentity, name: prepared.attempt.nameOf()}
	reservation := &reservationOwner{file: reservationFile, privateName: name, location: ownerLocationPrivate}
	summary := discardRecovered(&seed, prepared.attempt.destinationOf(), output, reservation)
	if !summary.artifacts.Empty() {
		t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
	}
	if _, present, err := d.directory().Entry(name); err != nil {
		t.Fatalf("entry: %v", err)
	} else if present {
		t.Fatal("private reservation name still present after identity inference")
	}
}

func TestDiscardSummaryAbsenceFlags(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	seed := captureSeed(prepared)

	summary := discardWith(&seed, prepared, nil, func(cleanupPoint) error { return nil })
	if !summary.mainAbsent || !summary.coordinationAbsent {
		t.Fatalf("absence flags after clean discard = (%v, %v), want (true, true)",
			summary.mainAbsent, summary.coordinationAbsent)
	}

	// One retained main name flips its absence flag only.
	mainName := d.mainName()
	if err := os.WriteFile(filepath.Join(dir, mainName), []byte("x"), 0o600); err != nil {
		t.Fatalf("create main: %v", err)
	}
	prepared2 := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared2.Close()
	seed2 := captureSeed(prepared2)
	summary2 := discardWith(&seed2, prepared2, nil, func(cleanupPoint) error { return nil })
	if summary2.mainAbsent || !summary2.coordinationAbsent {
		t.Fatalf("absence flags with retained main = (%v, %v), want (false, true)",
			summary2.mainAbsent, summary2.coordinationAbsent)
	}

	// Both retained names flip both flags.
	if err := os.WriteFile(filepath.Join(dir, d.coordinationName()), []byte("y"), 0o600); err != nil {
		t.Fatalf("create coordination: %v", err)
	}
	prepared3 := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared3.Close()
	seed3 := captureSeed(prepared3)
	summary3 := discardWith(&seed3, prepared3, nil, func(cleanupPoint) error { return nil })
	if summary3.mainAbsent || summary3.coordinationAbsent {
		t.Fatalf("absence flags with retained names = (%v, %v), want (false, false)",
			summary3.mainAbsent, summary3.coordinationAbsent)
	}
}

func TestFinishOneRemovalNotProved(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	defer file.Close()
	// No unlink happened: the sync proves nothing and the link proof
	// fails with the fixed detail.
	r := awaitingSyncRemoval(ArtifactPrivateOutput, nameSlotPrivateOutput, attempt.identityOf(), file)
	problem := finishOne(d.directory(), r)
	assertProblemCodeDetail(t, problem, format.CodeCleanupConflict, "private output removal was not proved")
	if _, present, err := d.directory().Entry(attempt.nameOf()); err != nil {
		t.Fatalf("entry: %v", err)
	} else if !present {
		t.Fatal("private output name removed by finishOne")
	}
}

func TestUnlinkedArtifactStillHasLinks(t *testing.T) {
	dir := t.TempDir()
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	defer file.Close()
	if err := os.Link(filepath.Join(dir, attempt.nameOf()), filepath.Join(dir, "alias.tmp")); err != nil {
		t.Fatalf("link alias: %v", err)
	}
	_, problem := requireUnlinked(ArtifactPrivateOutput, nameSlotPrivateOutput, attempt.identityOf(), file)
	assertProblemCodeDetail(t, problem, format.CodeCleanupConflict, "unlinked publication artifact still has links")
}

func TestFinishRemovalPublicationArtifactNotProved(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	seed := captureSeed(prepared)
	// The removal never unlinked the file: with no sync problem, the
	// link proof pushes the fixed artifact.
	identity := prepared.attempt.identityOf()
	r := awaitingSyncRemoval(ArtifactPrivateOutput, nameSlotPrivateOutput, identity, prepared.file)
	var artifacts CleanupArtifacts
	finishRemoval(&seed, r, nil, &artifacts)
	if artifacts.Len() != 1 {
		t.Fatalf("ledger %+v, want one artifact", artifacts.Slice())
	}
	assertCleanupArtifact(t, artifacts.At(0), ArtifactPrivateOutput, prepared.attempt.nameOf(),
		cleanupLocalIdentity(&identity), seed.creationSecurity,
		cleanupConflictProblem("publication artifact removal was not proved"))
	_ = d
}

func TestFinishRemovalSyncProblemWinsOverLinkProof(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	seed := captureSeed(prepared)
	r := awaitingSyncRemoval(ArtifactPrivateOutput, nameSlotPrivateOutput, prepared.attempt.identityOf(), prepared.file)
	injected := problem(format.CodeIO, "injected sync failure")
	var artifacts CleanupArtifacts
	finishRemoval(&seed, r, injected, &artifacts)
	if artifacts.Len() != 1 {
		t.Fatalf("ledger %+v, want one artifact", artifacts.Slice())
	}
	if artifacts.At(0).Error != injected {
		t.Fatalf("artifact error %v, want the injected sync problem", artifacts.At(0).Error)
	}
	_ = d
}

func TestDiscardOneRemovalFailedCarriesProblem(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	defer file.Close()
	// A foreign hard link trips the unexpected-links arm before any
	// unlink; discardOne returns the fixed conflict.
	if err := os.Link(filepath.Join(dir, attempt.nameOf()), filepath.Join(dir, "alias.tmp")); err != nil {
		t.Fatalf("link alias: %v", err)
	}
	identity := attempt.identityOf()
	problem := discardOne(d.directory(), attempt.nameOf(), file, &identity)
	assertProblemCodeDetail(t, problem, format.CodeCleanupConflict, "owned publication artifact has unexpected links")
}
