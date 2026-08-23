// Problem-mapping tests (Rust publication/problem.rs). Every
// NamespaceError arm and the output/reservation/replacement/main/sdk
// folds are pinned to the Rust-verbatim code and detail strings.

package publication

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// problemCase is one problem-mapping expectation (Rust problem.rs).
type problemCase struct {
	name   string
	err    error
	code   format.ErrorCode
	detail string
}

// TestNamespaceProblemArms pins Problem::namespace for every
// NamespaceError class (problem.rs arm order).
func TestNamespaceProblemArms(t *testing.T) {
	tests := []problemCase{
		{"invalid name", &live.NamespaceError{Kind: live.NamespaceInvalidName}, format.CodeNameInvalid, "invalid destination name"},
		{"not directory", &live.NamespaceError{Kind: live.NamespaceNotDirectory}, format.CodeConflict, "destination parent is not a directory"},
		{"not regular", &live.NamespaceError{Kind: live.NamespaceNotRegular}, format.CodeConflict, "publication name is not a regular file"},
		{"exists", &live.NamespaceError{Kind: live.NamespaceExists}, format.CodeNameExists, "publication name already exists"},
		{"missing", &live.NamespaceError{Kind: live.NamespaceMissing}, format.CodeNameNotFound, "publication name is missing"},
		{"identity changed", &live.NamespaceError{Kind: live.NamespaceIdentityChanged}, format.CodeConflict, "publication inode identity changed"},
		{"link count zero", &live.NamespaceError{Kind: live.NamespaceLinkCount, Links: 0}, format.CodeConflict, "publication inode has no links"},
		{"link count changed", &live.NamespaceError{Kind: live.NamespaceLinkCount, Links: 2}, format.CodeConflict, "publication inode link count changed"},
		{"cross filesystem", &live.NamespaceError{Kind: live.NamespaceCrossFilesystem}, format.CodePublicationUnsupported, "publication inode is on another filesystem"},
		{"access policy", &live.NamespaceError{Kind: live.NamespaceAccessPolicy}, format.CodeAccessPolicyUnsupported, "creator-only access policy is not proved"},
		{"unsupported", &live.NamespaceError{Kind: live.NamespaceUnsupported}, format.CodeDurabilityUnsupported, "filesystem lacks required durable namespace operations"},
		{"forked handle", &live.NamespaceError{Kind: live.NamespaceForkedHandle}, format.CodeForkedHandle, "publication handle crossed fork"},
		{"plain io", &live.NamespaceError{Kind: live.NamespaceIo, Op: "open directory", Err: errors.New("io")}, format.CodeIO, "publication filesystem operation failed"},
		{"io at", &live.NamespaceError{Kind: live.NamespaceIoAt, Op: "create private file", Err: errors.New("io")}, format.CodeIO, "create private file"},
		probeNofollowCase(),
	}
	for _, tt := range tests {
		got := namespaceProblem(tt.err)
		if got.Code != tt.code || got.Detail != tt.detail {
			t.Errorf("%s: got (%d, %q), want (%d, %q)", tt.name, got.Code, got.Detail, tt.code, tt.detail)
		}
	}
}

// TestNamespaceProblemSecurityPassthrough pins the creator-only
// security class reported directly by the security owner (Rust folds
// it into NamespaceError::AccessPolicy at the machine boundary; both
// fold to the same problem).
func TestNamespaceProblemSecurityPassthrough(t *testing.T) {
	got := namespaceProblem(&format.Error{Code: format.CodeAccessPolicyUnsupported, Detail: "creator-only access policy is not proved"})
	if got.Code != format.CodeAccessPolicyUnsupported || got.Detail != "creator-only access policy is not proved" {
		t.Errorf("got (%d, %q)", got.Code, got.Detail)
	}
}

// TestSdkProblem pins Problem::sdk: the code is preserved and the
// detail is the fixed string; os_code is dropped (design decision 6).
func TestSdkProblem(t *testing.T) {
	got := sdkProblem(&format.Error{Code: format.CodeCancelled, Detail: "inner detail"})
	if got.Code != format.CodeCancelled || got.Detail != "publication SDK operation failed" {
		t.Errorf("typed sdk: got (%d, %q)", got.Code, got.Detail)
	}
	got = sdkProblem(errors.New("raw"))
	if got.Code != format.CodeIO || got.Detail != "publication SDK operation failed" {
		t.Errorf("raw sdk: got (%d, %q)", got.Code, got.Detail)
	}
}

// TestOutputProblemArms pins Problem::output (problem.rs output arms;
// the Gc arm is Windows-only Phase 2 and is not ported).
func TestOutputProblemArms(t *testing.T) {
	// Namespace cause folds through the namespace table.
	got := outputProblem(&live.NamespaceError{Kind: live.NamespaceMissing})
	if got.Code != format.CodeNameNotFound || got.Detail != "publication name is missing" {
		t.Errorf("namespace cause: got (%d, %q)", got.Code, got.Detail)
	}
	// Access-policy security cause keeps the class.
	got = outputProblem(&format.Error{Code: format.CodeAccessPolicyUnsupported})
	if got.Code != format.CodeAccessPolicyUnsupported || got.Detail != "creator-only access policy is not proved" {
		t.Errorf("security cause: got (%d, %q)", got.Code, got.Detail)
	}
	// Bootstrap class (Rust Error::Bootstrap -> FormatInvalid "output
	// metadata is malformed").
	got = outputProblem(&format.Error{Code: format.CodeFormatInvalid})
	if got.Code != format.CodeFormatInvalid || got.Detail != "output metadata is malformed" {
		t.Errorf("bootstrap: got (%d, %q)", got.Code, got.Detail)
	}
	// The finished-output conflict arms carry the Rust-verbatim fixed
	// details from the producer.
	for _, detail := range []string{"finished output metadata changed", "finished output length changed"} {
		got = outputProblem(&format.Error{Code: format.CodeConflict, Detail: detail})
		if got.Code != format.CodeConflict || got.Detail != detail {
			t.Errorf("finished class %q: got (%d, %q)", detail, got.Code, got.Detail)
		}
	}
	// Any other SDK cause folds through sdkProblem.
	got = outputProblem(errors.New("raw"))
	if got.Code != format.CodeIO || got.Detail != "publication SDK operation failed" {
		t.Errorf("sdk cause: got (%d, %q)", got.Code, got.Detail)
	}
}

// TestReservationProblemArms pins Problem::reservation (problem.rs
// reservation arms; Gc and Checkpoint are Windows-only / 4-10-4-11 and
// are not ported).
func TestReservationProblemArms(t *testing.T) {
	// Namespace cause folds through the namespace table.
	got := reservationProblem(&live.NamespaceError{Kind: live.NamespaceExists})
	if got.Code != format.CodeNameExists || got.Detail != "publication name already exists" {
		t.Errorf("namespace cause: got (%d, %q)", got.Code, got.Detail)
	}
	// Output cause folds through the output table.
	got = reservationProblem(&format.Error{Code: format.CodeFormatInvalid})
	if got.Code != format.CodeFormatInvalid || got.Detail != "output metadata is malformed" {
		t.Errorf("output cause: got (%d, %q)", got.Code, got.Detail)
	}
	// Codec and header-invariant classes carry the producer's
	// Rust-verbatim fixed details.
	for _, detail := range []string{"reservation record is malformed", "reservation state is inconsistent"} {
		got = reservationProblem(&format.Error{Code: format.CodeFormatInvalid, Detail: detail})
		if got.Code != format.CodeFormatInvalid || got.Detail != detail {
			t.Errorf("codec class %q: got (%d, %q)", detail, got.Code, got.Detail)
		}
	}
	// Header-changed and length-changed conflict classes carry the
	// producer's Rust-verbatim fixed details.
	for _, detail := range []string{"reservation record changed", "reservation length changed"} {
		got = reservationProblem(&format.Error{Code: format.CodeConflict, Detail: detail})
		if got.Code != format.CodeConflict || got.Detail != detail {
			t.Errorf("conflict class %q: got (%d, %q)", detail, got.Code, got.Detail)
		}
	}
	// SDK cause folds through sdkProblem.
	got = reservationProblem(errors.New("raw"))
	if got.Code != format.CodeIO || got.Detail != "publication SDK operation failed" {
		t.Errorf("sdk cause: got (%d, %q)", got.Code, got.Detail)
	}
}

// TestReplacementProblemArms pins Problem::replacement (problem.rs
// replacement arms).
func TestReplacementProblemArms(t *testing.T) {
	got := replacementProblem(&format.Error{Code: format.CodeConflict, Detail: "replacement source and destination identities match"})
	if got.Code != format.CodeConflict || got.Detail != "replacement source and destination identities match" {
		t.Errorf("same identity: got (%d, %q)", got.Code, got.Detail)
	}
	got = replacementProblem(&format.Error{Code: format.CodeConflict, Detail: "replacement destination content changed"})
	if got.Code != format.CodeConflict || got.Detail != "replacement destination content changed" {
		t.Errorf("content changed: got (%d, %q)", got.Code, got.Detail)
	}
	// Output cause folds through the output table.
	got = replacementProblem(&format.Error{Code: format.CodeFormatInvalid})
	if got.Code != format.CodeFormatInvalid || got.Detail != "output metadata is malformed" {
		t.Errorf("output cause: got (%d, %q)", got.Code, got.Detail)
	}
}

// TestMainProblemArms pins Problem::main (problem.rs main arms; the
// Checkpoint clone-through arm, the Gc arm that is Windows-only Phase
// 2, and the test-only Injected arm).
func TestMainProblemArms(t *testing.T) {
	// The Checkpoint arm passes through unchanged (Rust
	// Error::Checkpoint(problem) => problem.clone()).
	injected := &checkpointProblem{problem: problem(format.CodeIO, "injected checkpoint")}
	if got := mainProblem(injected); got.Code != format.CodeIO || got.Detail != "injected checkpoint" {
		t.Errorf("checkpoint cause: got (%d, %q)", got.Code, got.Detail)
	}
	if got := reservationProblem(injected); got.Code != format.CodeIO || got.Detail != "injected checkpoint" {
		t.Errorf("reservation checkpoint cause: got (%d, %q)", got.Code, got.Detail)
	}
	// Reservation cause folds through the reservation table.
	got := mainProblem(&format.Error{Code: format.CodeConflict, Detail: "reservation record changed"})
	if got.Code != format.CodeConflict || got.Detail != "reservation record changed" {
		t.Errorf("reservation cause: got (%d, %q)", got.Code, got.Detail)
	}
	// The retired-link cleanup-conflict arms carry the producer's
	// Rust-verbatim fixed details.
	for _, detail := range []string{"retired previous destination still has a link", "retired reservation still has a link"} {
		got = mainProblem(&format.Error{Code: format.CodeCleanupConflict, Detail: detail})
		if got.Code != format.CodeCleanupConflict || got.Detail != detail {
			t.Errorf("retired-link class %q: got (%d, %q)", detail, got.Code, got.Detail)
		}
	}
	// Namespace cause folds through the namespace table.
	got = mainProblem(&live.NamespaceError{Kind: live.NamespaceForkedHandle})
	if got.Code != format.CodeForkedHandle || got.Detail != "publication handle crossed fork" {
		t.Errorf("namespace cause: got (%d, %q)", got.Code, got.Detail)
	}
}

// TestCleanupConflictProblem pins Problem::cleanup_conflict (the
// cleanup.rs fold surface).
func TestCleanupConflictProblem(t *testing.T) {
	got := cleanupConflictProblem("reservation cleanup failed")
	if got.Code != format.CodeCleanupConflict || got.Detail != "reservation cleanup failed" {
		t.Errorf("got (%d, %q)", got.Code, got.Detail)
	}
}

// TestAsProblem pins the exported public-problem projection (the
// facade converts the fixed problem; a wrapped problem keeps its code
// and detail).
func TestAsProblem(t *testing.T) {
	wrapped := &format.Error{Code: format.CodeCleanupConflict, Detail: "retired previous destination still has a link"}
	got := AsProblem(wrapped)
	if got != wrapped {
		t.Errorf("AsProblem did not return the fixed problem")
	}
	got = AsProblem(errors.New("raw"))
	if got.Code != format.CodeIO || got.Detail != "publication SDK operation failed" {
		t.Errorf("raw AsProblem: got (%d, %q)", got.Code, got.Detail)
	}
}
