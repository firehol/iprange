//go:build linux || darwin

package recovery

// Live-arm ordering pins (wave-19.24): the live candidate inspection and
// the live recovery source must classify the meta pair and prove the
// namespace over the lifetime-locked descriptor before any reader
// mapping geometry decision exists. Rust runs live_namespace::identity,
// verify_path, and read_classified in that order ahead of the mapping
// (recovery/inspection.rs:97-115, recovery/source_guard/live.rs
// open_file/bind_candidate), so:
//
//   - a short or junk live main is the unproven-generation class (or the
//     candidate-changed class for the recovery source), never the
//     format-invalid class of a two-page geometry refusal;
//   - a hard-linked live main is the wrong-state class of the identity
//     capture, never a coordination failure;
//   - a parent the namespace cannot bind as a directory is the io class.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// junkLiveFile writes one regular file that is not a v4 database and is
// deliberately smaller than two pages, so a mapping-level geometry
// refusal and the classification answer differ.
func junkLiveFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// expectCode runs one inspection and requires the given refusal class.
func expectInspectionCode(t *testing.T, label string, err error, want format.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: inspection accepted the input", label)
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%s: %v is not a typed v4 error", label, err)
	}
	if fe.Code != want {
		t.Fatalf("%s: code = %v, want %v (detail %q)", label, fe.Code, want, fe.Detail)
	}
}

// A short junk live main has no provable generation order: the answer
// must come from the classification of the meta pair, not from the
// two-page geometry refusal of the reader mapping.
func TestLiveInspectionClassifiesBeforeMappingGeometry(t *testing.T) {
	liveGate(t)
	dir := t.TempDir()
	junk := junkLiveFile(t, dir, "junk.iprdb", 5000)
	_, err := inspect(t, junk, RecoveryInspectionLive)
	expectInspectionCode(t, "short junk live main", err, format.CodeLiveRecoveryCurrentGenerationUnprovable)
}

// A zero-length live main is the same class for the same reason: there
// is nothing to classify, so the order is unproven.
func TestLiveInspectionOfZeroLengthMainIsUnprovable(t *testing.T) {
	liveGate(t)
	dir := t.TempDir()
	empty := junkLiveFile(t, dir, "empty.iprdb", 0)
	_, err := inspect(t, empty, RecoveryInspectionLive)
	expectInspectionCode(t, "zero-length live main", err, format.CodeLiveRecoveryCurrentGenerationUnprovable)
}

// A hard-linked live main is the wrong-state class of the single-link
// identity capture (Rust live_namespace::identity over
// retained_regular_identity(require_single_link = true)); the live
// source arm must not fold that namespace refusal into a coordination
// failure, which would advertise a reader-table problem the caller can
// retry.
func TestLiveInspectionOfHardLinkedMainIsWrongState(t *testing.T) {
	liveGate(t)
	main := createLiveRecoveryPair(t)
	link := main + ".link"
	if err := os.Link(main, link); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}
	_, err := inspect(t, link, RecoveryInspectionLive)
	expectInspectionCode(t, "hard-linked live main", err, format.CodeWrongState)
}

// The live recovery source applies the same ordering with the
// candidate-bound classes: a junk live main yields the
// candidate-changed class, matching Rust bind_candidate refusing an
// unproven order rather than surfacing a format problem.
func TestRecoverLiveClassifiesBeforeMappingGeometry(t *testing.T) {
	liveGate(t)
	dir := t.TempDir()
	junk := junkLiveFile(t, dir, "junk.iprdb", 5000)
	candidate := &RecoveryCandidate{
		Label:          CandidateNewest,
		MetaPage:       1,
		SourceIdentity: publication.LocalFileIdentity{},
		TransactionID:  3,
	}
	_, failure := RecoverLive(junk, candidate, filepath.Join(dir, "out.iprdb"), apiLiveTestBudget(), nil, nil)
	if failure == nil {
		t.Fatal("recover live accepted a junk source")
	}
	var fe *format.Error
	if !errors.As(failure.Cause, &fe) {
		t.Fatalf("cause %v is not a typed v4 error", failure.Cause)
	}
	if fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("recover live on junk: code = %v, want %v", fe.Code, format.CodeRecoveryCandidateChanged)
	}
}
