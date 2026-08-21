//go:build v4work

package writer_test

// The fail-if-exists rename race: after the twin/main re-checks pass,
// a destination appearing in the window before the no-replace rename
// turns the rename into the outcome_unknown refusal (Rust from_armed:
// !desired_proven retains the private artifact as recovery residue).
// The race window is a few syscalls, so the test arms the
// publish.fie_before_rename fault point instead of racing the
// filesystem; the classification is the same branch the real EEXIST
// takes.

import (
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/writer"
)

func TestPublishRenameRaceOutcomeUnknown(t *testing.T) {
	dir := t.TempDir()
	destination := dir + "/output.iprdb"
	attempt, b := stagedBuilder(t, destination, writer.PolicyFailIfExists)
	t.Setenv("IPRANGE_V4_TEST_FAIL_AT", "publish.fie_before_rename")
	result, err := writer.Publish(attempt, b, writer.PolicyFailIfExists)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Status != writer.PublicationOutcomeUnknown {
		t.Fatalf("status = %v, want OutcomeUnknown", result.Status)
	}
	if result.DestinationContent != writer.DestinationContentUnclassified {
		t.Fatalf("content = %v, want Unclassified", result.DestinationContent)
	}
	if result.Cleanup != writer.CleanupStateClean {
		t.Fatalf("cleanup = %v, want Clean", result.Cleanup)
	}
	// The residue contract: the private artifact is retained so the
	// caller can recover or remove it.
	if _, err := os.Lstat(attempt.AttemptPath()); err != nil {
		t.Fatalf("attempt file missing after outcome_unknown, want retained: %v", err)
	}
	if err := os.Remove(attempt.AttemptPath()); err != nil {
		t.Fatal(err)
	}
	closeBuilder(t, b)
}
