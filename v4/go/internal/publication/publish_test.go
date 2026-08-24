//go:build v4work && linux

// Reservation-path Publish composition tests (Rust snapshot api
// publish flow over publication/workflow.rs): the exact published
// facts with no residue, the fail-if-exists refusals for an existing
// main and an existing coordination twin, the replacement success
// with the previous-main evidence, the cancelled preparation discard,
// and the missing-previous bind refusal. Every cycle pins the
// process-fd count, including the fixture teardown, so the
// composition's own closes are proven.

package publication

import (
	"crypto/sha512"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// publishTestAttempt creates one composition attempt for main under
// the booking policy and builds the two finished meta pages into the
// attempt file (Rust snapshot publish flow: workflow::create then
// constructing the Finished). The caller consumes the attempt with
// Finish; a helper failure abandons the attempt with Close.
func publishTestAttempt(t *testing.T, main string, policy PublicationPolicy) (*PublishAttempt, FinishedOutput, [64]byte) {
	t.Helper()
	attempt, failure := CreatePublishAttempt(main, policy)
	if failure != nil {
		t.Fatalf("create publish attempt: %v", failure)
	}
	page0, page1, sum := testFinishedPages()
	if _, err := attempt.File().WriteAt(page0, 0); err != nil {
		attempt.Close()
		t.Fatalf("write meta page 0: %v", err)
	}
	if _, err := attempt.File().WriteAt(page1, format.PageSize); err != nil {
		attempt.Close()
		t.Fatalf("write meta page 1: %v", err)
	}
	mapped, err := mapping.MapFile(attempt.File(), 2*format.PageSize, false)
	if err != nil {
		attempt.Close()
		t.Fatalf("map finished output: %v", err)
	}
	meta, ok := format.ParseIdentity(page0)
	if !ok {
		attempt.Close()
		t.Fatal("test meta page does not parse")
	}
	return attempt, FinishedOutput{File: attempt.File(), Mapping: mapped, Meta: meta}, sum
}

// publishTestMain returns the exact digest of one main file.
func publishTestMain(t *testing.T, main string) [64]byte {
	t.Helper()
	bytes, err := os.ReadFile(main)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	return sha512.Sum512(bytes)
}

// TestPublishFailIfExistsSuccessReturnsExactPublishedFactsAndNo
// Residue ports the snapshot publish success over the reservation
// path.
func TestPublishFailIfExistsSuccessReturnsExactPublishedFactsAndNoResidue(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, finished, sum := publishTestAttempt(t, main, PolicyFailIfExists)
	before := countProcessFds(t)
	result, failure := attempt.Finish(finished, noopCheck)
	if failure != nil {
		t.Fatalf("publish: %v", failure)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("successful publish left %d descriptors open", after-before)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", result.CleanupState())
	}
	if result.Attempt.DatabaseID != testFixtureDBID {
		t.Fatalf("database id %x, want fixture", result.Attempt.DatabaseID)
	}
	if result.Attempt.TransactionID != 1 {
		t.Fatalf("transaction id %d, want 1", result.Attempt.TransactionID)
	}
	if result.Attempt.CommitNonce != testFixtureNonce {
		t.Fatalf("commit nonce %x, want fixture", result.Attempt.CommitNonce)
	}
	if result.Attempt.OutputByteLength != 2*uint64(format.PageSize) {
		t.Fatalf("output byte length %d, want two pages", result.Attempt.OutputByteLength)
	}
	if result.Attempt.OutputSHA512 != sum {
		t.Fatalf("output sha512 %x, want fixture", result.Attempt.OutputSHA512)
	}
	if got := publishTestMain(t, main); got != sum {
		t.Fatalf("main digest %x, want fixture", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "result.v4.readers")); err == nil {
		t.Fatal("coordination twin still exists")
	}
	if leftovers := scanPrefixed(t, dir, outputPrefix); len(leftovers) != 0 {
		t.Fatalf("private outputs left: %v", leftovers)
	}
	// The attempt is consumed on the terminal (the residue-handle
	// rule): the file exposure goes nil and a second Finish refuses
	// with the invalid-argument class.
	if attempt.File() != nil {
		t.Fatal("attempt file still exposed after Finish")
	}
	if _, failure := attempt.Finish(finished, noopCheck); codeOf(failure.Cause) != format.CodeInvalidArgument {
		t.Fatalf("consumed attempt problem = %v, want invalid argument", failure.Cause)
	}
}

// TestPublishFailIfExistsRefusesAnExistingMainAndCoordination ports
// the create_absent refusals at CreatePublishAttempt.
func TestPublishFailIfExistsRefusesAnExistingMainAndCoordination(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	if err := os.WriteFile(main, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write main: %v", err)
	}
	before := countProcessFds(t)
	_, failure := CreatePublishAttempt(main, PolicyFailIfExists)
	if codeOf(failure.Cause) != format.CodeNameExists {
		t.Fatalf("existing-main problem = %v, want name exists", failure.Cause)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("existing-main refusal left %d descriptors open", after-before)
	}
	if bytes, readErr := os.ReadFile(main); readErr != nil || string(bytes) != "existing" {
		t.Fatalf("main changed: err=%v bytes=%q", readErr, bytes)
	}
	if leftovers := scanPrefixed(t, dir, outputPrefix); len(leftovers) != 0 {
		t.Fatalf("private outputs left: %v", leftovers)
	}

	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "result.v4.readers"), []byte("twin"), 0o600); err != nil {
		t.Fatalf("write coordination: %v", err)
	}
	before = countProcessFds(t)
	_, failure = CreatePublishAttempt(filepath.Join(dir2, "result.v4"), PolicyFailIfExists)
	if codeOf(failure.Cause) != format.CodeNameExists {
		t.Fatalf("existing-twin problem = %v, want name exists", failure.Cause)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("existing-twin refusal left %d descriptors open", after-before)
	}
	if leftovers := scanPrefixed(t, dir2, outputPrefix); len(leftovers) != 0 {
		t.Fatalf("private outputs left: %v", leftovers)
	}
}

// TestPublishReplacementOverPreviousMain ports the snapshot
// replacement success with the previous evidence.
func TestPublishReplacementOverPreviousMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	if err := os.WriteFile(main, []byte("previous bytes"), 0o600); err != nil {
		t.Fatalf("write previous main: %v", err)
	}
	attempt, finished, sum := publishTestAttempt(t, main, PolicyReplaceExisting)
	before := countProcessFds(t)
	result, failure := attempt.Finish(finished, noopCheck)
	if failure != nil {
		t.Fatalf("publish: %v", failure)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("replacement publish left %d descriptors open", after-before)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", result.CleanupState())
	}
	if result.Attempt.PreviousDestination == nil {
		t.Fatal("previous destination evidence missing")
	}
	if result.Attempt.PreviousDestination.ByteLength != uint64(len("previous bytes")) {
		t.Fatalf("previous byte length %d, want %d", result.Attempt.PreviousDestination.ByteLength, len("previous bytes"))
	}
	if got := publishTestMain(t, main); got != sum {
		t.Fatalf("main digest %x, want fixture", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "result.v4.readers")); err == nil {
		t.Fatal("coordination twin still exists")
	}
	if leftovers := scanPrefixed(t, dir, outputPrefix); len(leftovers) != 0 {
		t.Fatalf("private outputs left: %v", leftovers)
	}
}

// TestPublishPreparationFailureDiscardsTheAttempt ports the
// cancelled prepare arm: the attempt is discarded, the cancellation
// class survives, and no descriptor survives.
func TestPublishPreparationFailureDiscardsTheAttempt(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, finished, _ := publishTestAttempt(t, main, PolicyFailIfExists)
	cancelled := &resolverTestCancellation{}
	cancelled.cancelled.Store(true)
	before := countProcessFds(t)
	_, failure := attempt.Finish(finished, cancelled.check)
	if failure == nil {
		t.Fatal("cancelled publish succeeded")
	}
	if codeOf(failure.Cause) != format.CodeCancelled {
		t.Fatalf("problem = %v, want cancelled", failure.Cause)
	}
	if failure.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup state = %v, want clean", failure.CleanupState())
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("cancelled publish left %d descriptors open", after-before)
	}
	if leftovers := scanPrefixed(t, dir, outputPrefix); len(leftovers) != 0 {
		t.Fatalf("discarded private outputs left: %v", leftovers)
	}
	if _, err := os.Lstat(main); err == nil {
		t.Fatal("main appeared")
	}
}

// TestPublishReplacementRefusesAMissingPreviousMain ports the bind
// refusal with the discard evidence.
func TestPublishReplacementRefusesAMissingPreviousMain(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, finished, _ := publishTestAttempt(t, main, PolicyReplaceExisting)
	before := countProcessFds(t)
	_, failure := attempt.Finish(finished, noopCheck)
	if failure == nil {
		t.Fatal("missing-previous publish succeeded")
	}
	if codeOf(failure.Cause) != format.CodeNameNotFound {
		t.Fatalf("problem = %v, want name not found", failure.Cause)
	}
	if after := countProcessFds(t); after > before {
		t.Fatalf("missing-previous publish left %d descriptors open", after-before)
	}
	if leftovers := scanPrefixed(t, dir, outputPrefix); len(leftovers) != 0 {
		t.Fatalf("discarded private outputs left: %v", leftovers)
	}
}
