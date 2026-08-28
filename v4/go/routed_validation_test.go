//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

package iprangedb

// Routed validation facade tests (slice 4-12B): on every worker-supported platform the
// public Validate and ValidateOfflineCandidate entries route through
// the isolated worker client after the preflight. These tests pin the
// worker equivalence (byte-identical domain shapes), the offline
// candidate arm, and the recorded no-fallback stance: a missing
// worker binary surfaces the Rust unsupported class through the
// public error surface.

import (
	"errors"
	"reflect"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// routedValidationFixture builds one immutable direct source with a
// dangling range root, so validation reports findings and the parity
// comparison covers nonzero counters on both arms.
func routedValidationFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/source.v4"
	publicRecoverySource(t, path)
	return path
}

// assertDomainProgressEqual fails unless both progresses carry the
// same counters (the per-reason and per-object arrays are private, so
// the comparison goes through the exported accessors).
func assertDomainProgressEqual(t *testing.T, want, got validation.ValidationProgress) {
	t.Helper()
	if want.CheckedUniquePages != got.CheckedUniquePages {
		t.Errorf("checked pages %d, want %d", got.CheckedUniquePages, want.CheckedUniquePages)
	}
	if want.FindingCount != got.FindingCount {
		t.Errorf("finding count %d, want %d", got.FindingCount, want.FindingCount)
	}
	if want.UntraversableSubgraphs != got.UntraversableSubgraphs {
		t.Errorf("untraversable subgraphs %d, want %d", got.UntraversableSubgraphs, want.UntraversableSubgraphs)
	}
	if want.HasUnboundedUnknown != got.HasUnboundedUnknown {
		t.Errorf("unbounded-unknown %v, want %v", got.HasUnboundedUnknown, want.HasUnboundedUnknown)
	}
	if want.BoundedPossibleSpanAddresses.Compare(got.BoundedPossibleSpanAddresses) != 0 {
		t.Errorf("bounded span addresses %v, want %v", got.BoundedPossibleSpanAddresses, want.BoundedPossibleSpanAddresses)
	}
	for reason := validation.ValidationReason(0); reason < validation.ValidationReasonCount; reason++ {
		if want.FindingsFor(reason) != got.FindingsFor(reason) {
			t.Errorf("findings for reason %d: %d, want %d", reason, got.FindingsFor(reason), want.FindingsFor(reason))
		}
	}
	for object := validation.ValidationObject(0); object < validation.ValidationObjectCount; object++ {
		if want.ExaminedFor(object) != got.ExaminedFor(object) {
			t.Errorf("examined for object %d: %d, want %d", object, got.ExaminedFor(object), want.ExaminedFor(object))
		}
	}
}

// assertDomainResultEqual fails unless both results carry the same
// facts (the routed wire conversion must be byte-identical to the
// in-process machine).
func assertDomainResultEqual(t *testing.T, want, got *validation.ValidationResult) {
	t.Helper()
	if want == nil || got == nil {
		t.Fatalf("result pair (%v, %v), want two completed results", want, got)
	}
	if want.Valid != got.Valid {
		t.Errorf("valid %v, want %v", got.Valid, want.Valid)
	}
	if want.FileIdentity != got.FileIdentity {
		t.Errorf("file identity %v, want %v", got.FileIdentity, want.FileIdentity)
	}
	if !reflect.DeepEqual(want.Generation, got.Generation) {
		t.Errorf("generation %v, want %v", got.Generation, want.Generation)
	}
	assertDomainProgressEqual(t, want.Progress, got.Progress)
}

// TestRoutedValidateMatchesInProcess proves the routed facade returns
// the exact domain shape the in-process machine returns for the same
// fixture: same valid flag, identity, generation, every progress
// counter covered by the mismatch reports, and the identical streamed
// finding sequence.
func TestRoutedValidateMatchesInProcess(t *testing.T) {
	installWorkerForTest(t)
	path := routedValidationFixture(t)
	budget := validation.HeapOnly(1<<20, 1)
	var inProcessFindings []validation.ValidationFinding
	inProcess, inProcessFailure := validation.Validate(path, validation.ValidationModeImmutableCurrent, budget, nil, validation.SinkFunc(func(f *validation.ValidationFinding) (validation.ValidationSinkControl, error) {
		inProcessFindings = append(inProcessFindings, *f)
		return validation.SinkContinue, nil
	}))
	var routedFindings []ValidationFinding
	routed, routedFailure := Validate(path, ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, SinkFunc(func(f *ValidationFinding) (ValidationSinkControl, error) {
		routedFindings = append(routedFindings, *f)
		return SinkContinue, nil
	}))
	if inProcessFailure != nil || routedFailure != nil {
		t.Fatalf("failures: in-process %v, routed %v", inProcessFailure, routedFailure)
	}
	assertDomainResultEqual(t, inProcess, routed)
	if len(inProcessFindings) != len(routedFindings) {
		t.Fatalf("findings %d routed, want %d in-process", len(routedFindings), len(inProcessFindings))
	}
	for index := range inProcessFindings {
		if !reflect.DeepEqual(inProcessFindings[index], routedFindings[index]) {
			t.Errorf("finding %d %v, want %v", index, routedFindings[index], inProcessFindings[index])
		}
	}
}

// TestRoutedValidateOfflineCandidateMatchesInProcess pins the offline
// candidate arm through the routed facade: the worker machine
// validates the identical candidate state and the facade returns the
// in-process domain shape. The dangling-range fixture fails inside
// the sweep (the committed extent exceeds the file), so the parity
// comparison covers the failure arm: same class and same progress.
func TestRoutedValidateOfflineCandidateMatchesInProcess(t *testing.T) {
	installWorkerForTest(t)
	path := routedValidationFixture(t)
	inspection, err := recovery.InspectRecoveryCandidates(path, recovery.RecoveryInspectionImmutable, validation.HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("in-process inspection: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil {
		t.Fatal("no candidate projected")
	}
	inProcess, inProcessFailure := recovery.ValidateOfflineCandidate(path, candidate, validation.HeapOnly(1<<20, 1), nil, nil)
	routed, routedFailure := ValidateOfflineCandidate(path, candidate, HeapOnly(1<<20, 1), nil, nil)
	if (inProcessFailure == nil) != (routedFailure == nil) {
		t.Fatalf("failure arms differ: in-process %v, routed %v", inProcessFailure, routedFailure)
	}
	if inProcessFailure == nil {
		assertDomainResultEqual(t, inProcess, routed)
		return
	}
	// The routed facade converts the cause onto the public class, so
	// the comparison normalizes both sides to the stable code.
	inProcessCode := format.CodePanic
	var wantCause *format.Error
	if errors.As(inProcessFailure.Cause, &wantCause) {
		inProcessCode = wantCause.Code
	}
	routedCode := format.CodePanic
	var publicCause *Error
	if errors.As(routedFailure.Cause, &publicCause) {
		routedCode = format.ErrorCode(publicCause.Code)
	} else if errors.As(routedFailure.Cause, &wantCause) {
		routedCode = wantCause.Code
	}
	if routedCode != inProcessCode {
		t.Errorf("failure class %v, want %v", routedCode, inProcessCode)
	}
	assertDomainProgressEqual(t, *inProcessFailure.Progress, *routedFailure.Progress)
}

// TestRoutedValidateBinaryUnavailablePinsOSUnsupported pins the
// recorded no-fallback stance through every routed surface: with no
// spawn candidate the facade reports the Rust unsupported class with
// the verbatim detail and never falls back to the in-process
// machines.
func TestRoutedValidateBinaryUnavailablePinsOSUnsupported(t *testing.T) {
	t.Cleanup(func() { worker.SetWorkerCandidatesForTest(nil) })
	worker.SetWorkerCandidatesForTest(func() ([]string, error) { return nil, nil })

	result, failure := Validate(t.TempDir()+"/db.v4", ValidationModeImmutableCurrent, HeapOnly(1<<20, 1), nil, nil)
	if result != nil || failure == nil {
		t.Fatalf("result = %v, failure = %v; want the failure arm", result, failure)
	}
	var public *Error
	if !errors.As(failure.Cause, &public) || public.Code != ErrorOSUnsupported {
		t.Fatalf("cause %#v, want the public unsupported class", failure.Cause)
	}
	if public.Detail != "SDK validation/recovery worker is unavailable" {
		t.Fatalf("detail %q, want the verbatim Rust detail", public.Detail)
	}
	if failure.Progress == nil || failure.Progress.CheckedUniquePages != 0 {
		t.Fatalf("progress %v, want zero counters", failure.Progress)
	}
	if failure.CleanupState() != CleanupStateClean {
		t.Fatalf("unavailable-worker failure reported residue: %+v", failure)
	}

	_, err := InspectRecoveryCandidates(t.TempDir()+"/db.v4", RecoveryInspectionImmutable, HeapOnly(0, 1), nil)
	if err == nil {
		t.Fatal("inspection succeeded without a worker")
	}
	if !errors.As(err, &public) || public.Code != ErrorOSUnsupported || public.Detail != "SDK validation/recovery worker is unavailable" {
		t.Fatalf("inspection cause %#v, want the verbatim unsupported class", err)
	}

	_, recoveryFailure := RecoverImmutable(t.TempDir()+"/source.v4", &RecoveryCandidate{Label: RecoveryCandidateNewest}, t.TempDir()+"/out.v4", RecoveryHeapOnly(1024*1024, 100, 2), nil, nil)
	if recoveryFailure == nil {
		t.Fatal("recovery succeeded without a worker")
	}
	if !errors.As(recoveryFailure.Cause, &public) || public.Code != ErrorOSUnsupported || public.Detail != "SDK validation/recovery worker is unavailable" {
		t.Fatalf("recovery cause %#v, want the verbatim unsupported class", recoveryFailure.Cause)
	}
}
