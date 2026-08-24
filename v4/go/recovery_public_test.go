package iprangedb

// Public recovery facade tests: the RecoverImmutable wiring over the
// public sink and cancellation surfaces, the public error class of a
// failing sink, and the nil-budget boundary refusal.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// publicRecoverySource builds one incomplete direct source at path (the
// internal api-test fixture peer: one dangling range root).
func publicRecoverySource(t *testing.T, path string) {
	t.Helper()
	builder, err := writer.NewOutputBuilder(path, writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindDirect,
		StructureKind:  format.StructureKindNone,
		ValueTag:       [16]byte{},
		DatabaseID:     [16]byte{0x11},
		TxnID:          1,
		CommitNonce:    [16]byte{0x22},
		FeedIndexLimit: 0,
	}, writer.OutputBudget{MaxOutputPages: 100}, 0, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	meta.PageCount = 3
	meta.RangeRoot = 2
	meta.RangeRecordCount = 1
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	var page [format.PageSize]byte
	if err := meta.EncodeMapped(page[:]); err != nil {
		t.Fatalf("EncodeMapped: %v", err)
	}
	for _, offset := range []int64{0, format.PageSize} {
		if _, err := file.WriteAt(page[:], offset); err != nil {
			t.Fatalf("write meta at %d: %v", offset, err)
		}
	}
	if err := file.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
}

// TestRecoverImmutablePublicSinkFailureReportsThePublicErrorClass
// proves the facade converts the internal failure cause onto the
// public error type with the exact class.
func TestRecoverImmutablePublicSinkFailureReportsThePublicErrorClass(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	outputPath := filepath.Join(dir, "output.v4")
	publicRecoverySource(t, sourcePath)
	inspection, err := InspectRecoveryCandidates(sourcePath, RecoveryInspectionImmutable, HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("InspectRecoveryCandidates: %v", err)
	}
	candidate := inspection.Candidate(0)
	if candidate == nil {
		t.Fatal("no candidate")
	}
	sink := RecoverySinkFunc(func(*RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		return RecoverySinkContinue, errors.New("injected public sink failure")
	})
	result, failure := RecoverImmutable(sourcePath, candidate, outputPath, RecoveryHeapOnly(1024*1024, 100, 2), sink, NewCancellationToken())
	if failure == nil {
		t.Fatal("recovery succeeded despite the sink failure")
	}
	if result != nil {
		t.Fatal("failed recovery returned a result")
	}
	var public *Error
	if !errors.As(failure.Cause, &public) || public.Code != ErrorSinkFailed {
		t.Fatalf("cause %#v, want the public SinkFailed class", failure.Cause)
	}
	if failure.Report.UnknownEnvelopes != 1 {
		t.Fatalf("unknown envelopes %d, want 1", failure.Report.UnknownEnvelopes)
	}
}

// TestRecoverImmutablePublicNilBudgetRefusesBeforeAnyPathAccess pins
// the facade boundary guard (the snapshot nil-budget precedent).
func TestRecoverImmutablePublicNilBudgetRefusesBeforeAnyPathAccess(t *testing.T) {
	result, failure := RecoverImmutable("/nonexistent/source.v4", nil, "/nonexistent/output.v4", nil, nil, nil)
	if failure == nil {
		t.Fatal("nil budget accepted")
	}
	if result != nil {
		t.Fatal("nil-budget refusal returned a result")
	}
	var public *Error
	if !errors.As(failure.Cause, &public) || public.Code != ErrorInvalidArgument {
		t.Fatalf("cause %#v, want the public InvalidArgument class", failure.Cause)
	}
}

// TestRecoverImmutablePublicCancellationRefusesBeforeTheAttempt pins
// the pre-creation cancellation position through the public token.
func TestRecoverImmutablePublicCancellationRefusesBeforeTheAttempt(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.v4")
	outputPath := filepath.Join(dir, "output.v4")
	publicRecoverySource(t, sourcePath)
	inspection, err := InspectRecoveryCandidates(sourcePath, RecoveryInspectionImmutable, HeapOnly(0, 1), nil)
	if err != nil {
		t.Fatalf("InspectRecoveryCandidates: %v", err)
	}
	candidate := inspection.Candidate(0)
	token := NewCancellationToken()
	token.Cancel()
	_, failure := RecoverImmutable(sourcePath, candidate, outputPath, RecoveryHeapOnly(1024*1024, 100, 2), nil, token)
	if failure == nil {
		t.Fatal("cancelled recovery accepted")
	}
	var public *Error
	if !errors.As(failure.Cause, &public) || public.Code != ErrorCancelled {
		t.Fatalf("cause %#v, want the public Cancelled class", failure.Cause)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("output still present after the pre-creation refusal: %v", err)
	}
}
