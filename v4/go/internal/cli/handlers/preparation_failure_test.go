package handlers

import (
	"errors"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// TestSnapshotPreparationFailureDetails verifies the snapshot
// preparation-failure adapter emits the full six-member details object
// (Rust snapshot.rs preparation_details): cleanup_state, cleanup,
// coordination_cleanup, housekeeping, visible_housekeeping, and output
// (null when no attempt artifact existed).
func TestSnapshotPreparationFailureDetails(t *testing.T) {
	attempt := &iprangedb.PrivateOutputAttempt{
		PublicationAttemptID: [16]byte{1},
		DirectoryIdentity:    iprangedb.FileIdentity{Kind: 1, Bytes: [32]byte{2}},
		BasenameEncoding:     1,
		Basename:             []byte("snapshot.iprange"),
		Identity:             iprangedb.FileIdentity{Kind: 1, Bytes: [32]byte{3}},
		IdentityPresent:      true,
		CreationSecurity:     iprangedb.CreationSecurity{Kind: 1, Commitment: [32]byte{4}},
	}
	failure := &iprangedb.SnapshotPreparationFailure{
		Cause:               &iprangedb.Error{Code: iprangedb.ErrorConflict, Detail: "destination occupied"},
		Cleanup:             iprangedb.CleanupStateClean,
		Output:              attempt,
		CleanupArtifacts:    iprangedb.CleanupArtifacts{},
		CoordinationCleanup: iprangedb.CoordinationCleanupNone,
		Housekeeping:        iprangedb.HousekeepingNone,
		VisibleHousekeeping: nil,
	}
	herr := snapshotPreparationFailure(failure)
	if herr == nil {
		t.Fatal("snapshotPreparationFailure returned nil")
	}
	if herr.Outcome != "not_started" {
		t.Errorf("outcome = %q, want not_started", herr.Outcome)
	}
	details, ok := herr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details type = %T, want map[string]any", herr.Details)
	}
	for _, member := range []string{"cleanup_state", "cleanup", "coordination_cleanup", "housekeeping", "visible_housekeeping", "output"} {
		if _, present := details[member]; !present {
			t.Errorf("details missing member %q", member)
		}
	}
	if details["cleanup_state"] != "clean" {
		t.Errorf("cleanup_state = %v, want clean", details["cleanup_state"])
	}
	if _, ok := details["cleanup"].(map[string]any); !ok {
		t.Errorf("cleanup = %T, want object", details["cleanup"])
	}
	if _, ok := details["coordination_cleanup"].(map[string]any); !ok {
		t.Errorf("coordination_cleanup = %T, want object", details["coordination_cleanup"])
	}
	if _, ok := details["housekeeping"].(map[string]any); !ok {
		t.Errorf("housekeeping = %T, want object", details["housekeeping"])
	}
	if _, ok := details["visible_housekeeping"].([]any); !ok {
		t.Errorf("visible_housekeeping = %T, want array", details["visible_housekeeping"])
	}
	output, ok := details["output"].(map[string]any)
	if !ok {
		t.Fatalf("output = %T, want object", details["output"])
	}
	if output["publication_attempt_id"] != HexID(&attempt.PublicationAttemptID) {
		t.Errorf("output publication_attempt_id = %v", output["publication_attempt_id"])
	}
	if output["basename_encoding"] != attempt.BasenameEncoding {
		t.Errorf("output basename_encoding = %v", output["basename_encoding"])
	}
	if output["basename"] != string(attempt.Basename) {
		t.Errorf("output basename = %v", output["basename"])
	}
	if _, present := output["identity"]; !present {
		t.Error("output missing identity")
	}
	if _, present := output["creation_security"]; !present {
		t.Error("output missing creation_security")
	}
}

// TestSnapshotPreparationFailureOutputNull verifies the output member
// is null when no attempt artifact existed (Rust Option projection).
func TestSnapshotPreparationFailureOutputNull(t *testing.T) {
	failure := &iprangedb.SnapshotPreparationFailure{
		Cause:   &iprangedb.Error{Code: iprangedb.ErrorInvalidArgument, Detail: "budget"},
		Cleanup: iprangedb.CleanupStateClean,
	}
	herr := snapshotPreparationFailure(failure)
	details := herr.Details.(map[string]any)
	if details["output"] != nil {
		t.Errorf("output = %v, want null", details["output"])
	}
}

// TestAlgebraPreparationErrorDetails verifies the algebra
// publish_set preparation-failure adapter emits the full six-member
// details object (Rust algebra.rs algebra_preparation_error).
func TestAlgebraPreparationErrorDetails(t *testing.T) {
	attempt := &iprangedb.PrivateOutputAttempt{
		PublicationAttemptID: [16]byte{9},
		BasenameEncoding:     1,
		Basename:             []byte("set.iprange"),
		IdentityPresent:      true,
	}
	var ledger iprangedb.CleanupArtifacts
	ledger.Push(iprangedb.CleanupArtifact{Kind: iprangedb.ArtifactPrivateOutput, Error: errors.New("removal unproven")})
	failure := &iprangedb.AlgebraPreparationFailure{
		Cause:               &iprangedb.Error{Code: iprangedb.ErrorNameInvalid, Detail: "bad name"},
		Cleanup:             iprangedb.CleanupStateResiduePossible,
		Output:              attempt,
		CleanupArtifacts:    ledger,
		CoordinationCleanup: iprangedb.CoordinationCleanupCleanupGuard,
		Housekeeping:        iprangedb.HousekeepingVisible,
		VisibleHousekeeping: []iprangedb.HousekeepingArtifact{},
	}
	herr := algebraPreparationError(failure)
	if herr == nil {
		t.Fatal("algebraPreparationError returned nil")
	}
	if herr.Outcome != "not_started" {
		t.Errorf("outcome = %q, want not_started", herr.Outcome)
	}
	details, ok := herr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details type = %T, want map[string]any", herr.Details)
	}
	for _, member := range []string{"cleanup_state", "cleanup", "coordination_cleanup", "housekeeping", "visible_housekeeping", "output"} {
		if _, present := details[member]; !present {
			t.Errorf("details missing member %q", member)
		}
	}
	if details["cleanup_state"] != "residue_possible" {
		t.Errorf("cleanup_state = %v, want residue_possible", details["cleanup_state"])
	}
	if _, ok := details["cleanup"].(map[string]any); !ok {
		t.Errorf("cleanup = %T, want object", details["cleanup"])
	}
	if _, ok := details["coordination_cleanup"].(map[string]any); !ok {
		t.Errorf("coordination_cleanup = %T, want object", details["coordination_cleanup"])
	}
	if _, ok := details["housekeeping"].(map[string]any); !ok {
		t.Errorf("housekeeping = %T, want object", details["housekeeping"])
	}
	if _, ok := details["visible_housekeeping"].([]any); !ok {
		t.Errorf("visible_housekeeping = %T, want array", details["visible_housekeeping"])
	}
	if _, ok := details["output"].(map[string]any); !ok {
		t.Errorf("output = %T, want object", details["output"])
	}
}

// TestFeedPreparationErrorDetails verifies the immutable-feed
// preparation-failure adapter emits the five fact members without
// cleanup_state (Rust publish.rs preparation_error) and derives the
// outcome from the facts: not_published exactly when a private attempt
// exists or the cleanup ledger is non-empty.
func TestFeedPreparationErrorDetails(t *testing.T) {
	attempt := &iprangedb.PrivateOutputAttempt{
		PublicationAttemptID: [16]byte{7},
		BasenameEncoding:     1,
		Basename:             []byte("feed.iprange"),
		IdentityPresent:      true,
	}
	failure := &iprangedb.ImmutableFeedPreparationFailure{
		Cause:               &iprangedb.Error{Code: iprangedb.ErrorConflict, Detail: "occupied"},
		Cleanup:             iprangedb.CleanupStateClean,
		Output:              attempt,
		CleanupArtifacts:    iprangedb.CleanupArtifacts{},
		CoordinationCleanup: iprangedb.CoordinationCleanupNone,
		Housekeeping:        iprangedb.HousekeepingNone,
		VisibleHousekeeping: nil,
	}
	herr := preparationError(failure, "", "")
	if herr == nil {
		t.Fatal("preparationError returned nil")
	}
	if herr.Outcome != "not_published" {
		t.Errorf("outcome = %q, want not_published (attempt exists)", herr.Outcome)
	}
	details, ok := herr.Details.(map[string]any)
	if !ok {
		t.Fatalf("details type = %T, want map[string]any", herr.Details)
	}
	for _, member := range []string{"output", "cleanup", "coordination_cleanup", "housekeeping", "visible_housekeeping"} {
		if _, present := details[member]; !present {
			t.Errorf("details missing member %q", member)
		}
	}
	if _, present := details["cleanup_state"]; present {
		t.Error("feed preparation details must not carry cleanup_state (Rust publish.rs)")
	}
	if _, ok := details["output"].(map[string]any); !ok {
		t.Errorf("output = %T, want object", details["output"])
	}

	// No attempt and an empty ledger: not_started with a null output.
	empty := &iprangedb.ImmutableFeedPreparationFailure{
		Cause:   &iprangedb.Error{Code: iprangedb.ErrorInvalidArgument, Detail: "budget"},
		Cleanup: iprangedb.CleanupStateClean,
	}
	herr = preparationError(empty, "", "")
	if herr.Outcome != "not_started" {
		t.Errorf("empty outcome = %q, want not_started", herr.Outcome)
	}
	details = herr.Details.(map[string]any)
	if details["output"] != nil {
		t.Errorf("empty output = %v, want null", details["output"])
	}

	// A residue ledger with no attempt: not_published.
	var ledger iprangedb.CleanupArtifacts
	ledger.Push(iprangedb.CleanupArtifact{Kind: iprangedb.ArtifactPrivateOutput, Error: errors.New("unproven")})
	residue := &iprangedb.ImmutableFeedPreparationFailure{
		Cause:            &iprangedb.Error{Code: iprangedb.ErrorConflict, Detail: "x"},
		Cleanup:          iprangedb.CleanupStateResiduePossible,
		CleanupArtifacts: ledger,
	}
	herr = preparationError(residue, "", "")
	if herr.Outcome != "not_published" {
		t.Errorf("residue outcome = %q, want not_published (ledger non-empty)", herr.Outcome)
	}

	// A failing input source wins: source code/message and not_started.
	herr = preparationError(failure, "input_format", "line 1 exploded")
	if herr.Outcome != "not_started" {
		t.Errorf("source outcome = %q, want not_started", herr.Outcome)
	}
	if herr.Code != "input_format" || herr.Message != "line 1 exploded" {
		t.Errorf("source code/message = %q/%q, want input_format/line 1 exploded", herr.Code, herr.Message)
	}
}
