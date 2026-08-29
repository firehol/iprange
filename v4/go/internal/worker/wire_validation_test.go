//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

// Validation-mode wire unit tests (Rust worker/wire_validation.rs): the
// request with all three mode arms, the completed and operational-
// failure results with the retained problem, the cleanup result, the
// streamed finding envelope, and the validated-generation record with
// its contract-enum rejections.

package worker

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

func TestValidationRequestRoundTrip(t *testing.T) {
	candidate := testCandidate()
	direct := &ValidationRequest{
		Path:              "/tmp/source.v4",
		Mode:              validation.ValidationModeImmutableCurrent,
		Budget:            validation.ValidationBudget{MaxHeapBytes: 1 << 20, MaxOpenFiles: 2},
		UnreadablePages:   []uint32{4},
		DeliveredFindings: 3,
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteValidationRequest(c, direct.Path, direct.Mode, nil, &direct.Budget, direct.UnreadablePages, direct.DeliveredFindings); err != nil {
		t.Fatal("write request:", err)
	}
	request, err := ReadValidationRequest(c)
	if err != nil {
		t.Fatal("read request:", err)
	}
	if request.Path != direct.Path || request.Mode != direct.Mode || request.DeliveredFindings != 3 ||
		len(request.UnreadablePages) != 1 || request.UnreadablePages[0] != 4 {
		t.Fatalf("request = %+v", request)
	}
	// The live arm and the offline-candidate arm round trip too; the
	// offline candidate is consumed inline exactly like Rust.
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if err := WriteValidationRequest(c2, "/v", validation.ValidationModeLiveCurrent, nil, &direct.Budget, nil, 0); err != nil {
		t.Fatal(err)
	}
	request2, err := ReadValidationRequest(c2)
	if err != nil {
		t.Fatal(err)
	}
	if request2.Mode != validation.ValidationModeLiveCurrent {
		t.Fatalf("mode = %v", request2.Mode)
	}
	c3, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	if err := WriteValidationRequest(c3, "/v", validation.ValidationModeOfflineCandidate, candidate, &direct.Budget, nil, 0); err != nil {
		t.Fatal(err)
	}
	request3, err := ReadValidationRequest(c3)
	if err != nil {
		t.Fatal(err)
	}
	if request3.Mode != validation.ValidationModeOfflineCandidate {
		t.Fatalf("offline mode = %v", request3.Mode)
	}
	if request3.Candidate == nil || *request3.Candidate != *candidate {
		t.Fatalf("offline candidate = %+v", request3.Candidate)
	}
	// The offline arm without a candidate refuses before any write.
	c5, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c5.Close()
	err = WriteValidationRequest(c5, "/v", validation.ValidationModeOfflineCandidate, nil, &direct.Budget, nil, 0)
	wantCode(t, err, format.CodeInvalidArgument)
	// An unknown mode tag is corruption.
	c4, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c4.Close()
	w4 := NewWireWriter(c4)
	if err := w4.Path("/v"); err != nil {
		t.Fatal(err)
	}
	if err := w4.Byte(9); err != nil {
		t.Fatal(err)
	}
	if err := w4.Finish(); err != nil {
		t.Fatal(err)
	}
	_, err = ReadValidationRequest(c4)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestValidationResultRoundTrip(t *testing.T) {
	result := &validation.ValidationResult{
		Valid:        true,
		FileIdentity: testIdentity(1, 2),
		Generation: &validation.ValidatedGeneration{
			AddressFamily: format.AddressFamilyIPv4,
			ValueKind:     format.ValueKindDirect,
			StructureKind: format.StructureKindNone,
			ValueTag:      [16]byte{'f', 'i', 'r', 's', 't', '_', 's', 'e', 'e', 'n'},
			DatabaseID:    [16]byte{0x11},
			TransactionID: 7,
			CommitNonce:   [16]byte{0x22},
			PageCount:     10,
			Roots:         [13]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
		},
		Progress: validation.NewProgress(),
	}
	result.Progress.CheckedUniquePages = 5
	if err := validation.CountFinding(&result.Progress, validation.ReasonPageCrcMismatch); err != nil {
		t.Fatal(err)
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteValidationResult(c, result, nil, nil); err != nil {
		t.Fatal("write result:", err)
	}
	gotResult, gotFailure, retained := ReadValidationResult(c)
	if gotResult == nil || gotFailure != nil || retained != nil {
		t.Fatalf("result = %v %v %v", gotResult, gotFailure, retained)
	}
	if !gotResult.Valid || gotResult.FileIdentity != result.FileIdentity ||
		gotResult.Generation == nil || gotResult.Generation.DatabaseID != [16]byte{0x11} ||
		gotResult.Generation.Roots != result.Generation.Roots ||
		gotResult.Progress.CheckedUniquePages != 5 ||
		gotResult.Progress.ReasonCounts[validation.ReasonPageCrcMismatch] != 1 {
		t.Fatalf("wire result = %+v", gotResult)
	}
}

func TestValidationFailureResultRoundTrip(t *testing.T) {
	failure := &validation.ValidationFailure{
		Cause:               &format.Error{Code: format.CodeFaultWorkerFailed, Detail: "worker crashed"},
		Progress:            &validation.ValidationProgress{},
		Cleanup:             publication.NewCleanupArtifacts(),
		CoordinationCleanup: publication.CoordinationCleanupNone,
	}
	failure.Progress.CheckedUniquePages = 2
	if err := validation.CountFinding(failure.Progress, validation.ReasonMetaUnavailable); err != nil {
		t.Fatal(err)
	}
	retained := &WireProblem{Code: format.CodeCleanupConflict, Detail: "retained cleanup"}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteValidationResult(c, nil, failure, retained); err != nil {
		t.Fatal("write failure:", err)
	}
	gotResult, gotFailure, gotRetained := ReadValidationResult(c)
	if gotResult != nil || gotFailure == nil || gotRetained == nil {
		t.Fatalf("failure = %v %v %v", gotResult, gotFailure, gotRetained)
	}
	if gotFailure.Progress.CheckedUniquePages != 2 ||
		gotFailure.Progress.ReasonCounts[validation.ReasonMetaUnavailable] != 1 {
		t.Fatalf("failure progress = %+v", gotFailure.Progress)
	}
	// CodeFaultWorkerFailed is not one of the specific constant
	// variants of the Rust read_error arm, so it survives as a worker
	// operation pair.
	var wire *WireError
	if !errors.As(gotFailure.Cause, &wire) || wire.Code != format.CodeFaultWorkerFailed {
		t.Fatalf("failure cause = %v", gotFailure.Cause)
	}
	if gotRetained.Code != format.CodeCleanupConflict || gotRetained.Detail != "retained cleanup" {
		t.Fatalf("retained = %+v", gotRetained)
	}
	// The cleanup facts of the wire failure are the empty ledger.
	if !gotFailure.Cleanup.Empty() || gotFailure.CoordinationCleanup != publication.CoordinationCleanupNone {
		t.Fatalf("wire failure facts = %+v", gotFailure)
	}
	// A corrupt envelope folds into an operational failure with zero
	// progress, exactly like the Rust read_result wrapper.
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	w := NewWireWriter(c2)
	if err := w.Byte(7); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	_, wrapped, _ := ReadValidationResult(c2)
	if wrapped == nil || wrapped.Cause == nil {
		t.Fatal("corrupt tag did not fold into a failure")
	}
	if wrapped.Progress != (ProgressWire{}) {
		t.Fatalf("wrapped progress = %+v", wrapped.Progress)
	}
}

func TestValidationCleanupResultRoundTrip(t *testing.T) {
	for _, complete := range []bool{false, true} {
		c, err := CreateParent()
		if err != nil {
			t.Fatal(err)
		}
		var problem *WireProblem
		if !complete {
			problem = &WireProblem{Code: format.CodeConflict, Detail: "cleanup worker failed"}
		}
		if err := WriteValidationCleanupResult(c, complete, problem); err != nil {
			t.Fatal(err)
		}
		gotComplete, gotProblem, err := ReadValidationCleanupResult(c)
		if err != nil {
			t.Fatal(err)
		}
		if gotComplete != complete {
			t.Fatalf("complete = %v", gotComplete)
		}
		if problem == nil && gotProblem != nil {
			t.Fatal("unexpected problem")
		}
		if problem != nil && (gotProblem == nil || gotProblem.Code != problem.Code) {
			t.Fatalf("problem = %+v", gotProblem)
		}
		c.Close()
	}
}

func TestValidationFindingRoundTrip(t *testing.T) {
	page := uint32(3)
	related := uint32(9)
	interval := &validation.PhysicalByteInterval{Start: 4096, EndExclusive: 8192}
	fence := &validation.ValidationAddressFence{IPv4: false, FromV6: [16]byte{1}, ToV6: [16]byte{2}}
	finding := &validation.ValidationFinding{
		Sequence:          12,
		Reason:            validation.ReasonPageCrcMismatch,
		Object:            validation.ObjectMeta,
		PageNumber:        &page,
		PhysicalBytes:     interval,
		RelatedPageNumber: &related,
		AddressFence:      fence,
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteValidationFinding(c, finding); err != nil {
		t.Fatal("write finding:", err)
	}
	got, err := ReadValidationFinding(c)
	if err != nil {
		t.Fatal("read finding:", err)
	}
	if got.Sequence != 12 || got.Reason != finding.Reason || got.Object != finding.Object ||
		got.PageNumber == nil || *got.PageNumber != page ||
		got.RelatedPageNumber == nil || *got.RelatedPageNumber != related ||
		got.PhysicalBytes == nil || *got.PhysicalBytes != *interval ||
		got.AddressFence == nil || got.AddressFence.FromV6 != [16]byte{1} {
		t.Fatalf("finding = %+v", got)
	}
	// An invalid object class is corruption.
	bad, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	w := NewWireWriter(bad)
	if err := w.U64(1); err != nil {
		t.Fatal(err)
	}
	if err := w.Byte(0); err != nil {
		t.Fatal(err)
	}
	if err := w.Byte(200); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	_, err = ReadValidationFinding(bad)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestValidatedGenerationRoundTrip(t *testing.T) {
	generation := &validation.ValidatedGeneration{
		AddressFamily: format.AddressFamilyIPv6,
		ValueKind:     format.ValueKindStructured,
		StructureKind: format.StructureKindNetworkEnrichmentV1,
		ValueTag:      [16]byte{0},
		DatabaseID:    [16]byte{0x11},
		TransactionID: 7,
		CommitNonce:   [16]byte{0x22},
		PageCount:     10,
		Roots:         [13]uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writeValidatedGeneration(w, generation); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readValidatedGeneration(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got.AddressFamily != generation.AddressFamily || got.ValueKind != generation.ValueKind ||
		got.StructureKind != generation.StructureKind || got.ValueTag != generation.ValueTag ||
		got.DatabaseID != generation.DatabaseID || got.TransactionID != generation.TransactionID ||
		got.PageCount != generation.PageCount || got.Roots != generation.Roots {
		t.Fatalf("generation = %+v", got)
	}
}

func TestValidatedGenerationRejectsContractEnums(t *testing.T) {
	good := []byte{format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone}
	cases := []struct {
		name   string
		mutate func(page []byte)
		detail string
	}{
		{"address family", func(p []byte) { p[0] = 5 }, "worker address family is invalid"},
		{"value kind", func(p []byte) { p[1] = 9 }, "worker value kind is invalid"},
		{"structure kind", func(p []byte) { p[2] = 7 }, "worker structure kind is invalid"},
	}
	for _, tc := range cases {
		c, err := CreateParent()
		if err != nil {
			t.Fatal(err)
		}
		w := NewWireWriter(c)
		// The generation codec writes 79 bytes: 3 enums + tag + id +
		// txn + nonce + count + 13 roots.
		if err := w.Byte(good[0]); err != nil {
			t.Fatal(err)
		}
		if err := w.Byte(good[1]); err != nil {
			t.Fatal(err)
		}
		if err := w.Byte(good[2]); err != nil {
			t.Fatal(err)
		}
		if err := w.Bytes(make([]byte, 16)); err != nil {
			t.Fatal(err)
		}
		if err := w.Bytes(make([]byte, 16)); err != nil {
			t.Fatal(err)
		}
		if err := w.U64(1); err != nil {
			t.Fatal(err)
		}
		if err := w.Bytes(make([]byte, 16)); err != nil {
			t.Fatal(err)
		}
		if err := w.U64(1); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 13; i++ {
			if err := w.U32(0); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Finish(); err != nil {
			t.Fatal(err)
		}
		tc.mutate(c.data[offPayload : offPayload+3])
		r, err := NewWireReader(c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = readValidatedGeneration(r)
		wantCode(t, err, format.CodeFormatInvalid)
		c.Close()
	}
	// A value tag whose tail after the first zero is nonzero is
	// corruption (Rust ValueTag::from_wire).
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := w.Byte(format.AddressFamilyIPv4); err != nil {
		t.Fatal(err)
	}
	if err := w.Byte(format.ValueKindDirect); err != nil {
		t.Fatal(err)
	}
	if err := w.Byte(format.StructureKindNone); err != nil {
		t.Fatal(err)
	}
	tag := [16]byte{'x', 0, 9} // zero at index 1, nonzero after it
	if err := w.Bytes(tag[:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readValidatedGeneration(r)
	wantCode(t, err, format.CodeFormatInvalid)
	// A tag with no zero at all is corruption.
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	w2 := NewWireWriter(c2)
	if err := w2.Byte(format.AddressFamilyIPv4); err != nil {
		t.Fatal(err)
	}
	if err := w2.Byte(format.ValueKindDirect); err != nil {
		t.Fatal(err)
	}
	if err := w2.Byte(format.StructureKindNone); err != nil {
		t.Fatal(err)
	}
	full := [16]byte{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p'}
	if err := w2.Bytes(full[:]); err != nil {
		t.Fatal(err)
	}
	if err := w2.Finish(); err != nil {
		t.Fatal(err)
	}
	r2, err := NewWireReader(c2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readValidatedGeneration(r2)
	wantCode(t, err, format.CodeFormatInvalid)
}

// wireValidationResultOf converts a domain result including its
// progress arrays through the exported accessors.
func TestWireValidationResultOf(t *testing.T) {
	result := &validation.ValidationResult{
		Valid:        false,
		FileIdentity: testIdentity(9, 10),
		Progress:     validation.NewProgress(),
	}
	result.Progress.CheckedUniquePages = 3
	if err := validation.CountFinding(&result.Progress, validation.ReasonIoError); err != nil {
		t.Fatal(err)
	}
	wireResult := wireValidationResultOf(result)
	if wireResult.Valid || wireResult.FileIdentity != result.FileIdentity ||
		wireResult.Progress.CheckedUniquePages != 3 ||
		wireResult.Progress.ReasonCounts[validation.ReasonIoError] != 1 {
		t.Fatalf("wire result = %+v", wireResult)
	}
}

// Import alignment: the recovery package types are used by the
// offline-candidate request arm of this file's vectors.
var _ = recovery.RecoveryCandidate{}
