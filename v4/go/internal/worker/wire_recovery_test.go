//go:build linux && amd64

// Recovery-mode wire unit tests (Rust worker/wire_recovery.rs): the
// request and outcome envelopes, the streamed unknown envelope, the
// report codec including the callback-checkpoint wire arms of
// worker/client_tests.rs, and the optional scratch attempt.

package worker

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

func testCandidate() *recovery.RecoveryCandidate {
	return &recovery.RecoveryCandidate{
		Label:          recovery.CandidateNewest,
		MetaPage:       1,
		SourceIdentity: testIdentity(1, 2),
		DatabaseID:     [16]byte{0x11},
		TransactionID:  3,
		CommitNonce:    [16]byte{0x22},
	}
}

func testOutputAttempt() *publication.PrivateOutputAttempt {
	return &publication.PrivateOutputAttempt{
		PublicationAttemptID: [16]byte{0x33},
		DirectoryIdentity:    testIdentity(1, 2),
		BasenameEncoding:     1,
		Basename:             []byte(".out.tmp"),
		Identity:             testIdentity(3, 4),
		IdentityPresent:      true,
		CreationSecurity:     publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x44}},
	}
}

func TestRecoveryRequestRoundTrip(t *testing.T) {
	for _, mode := range []WorkerMode{WorkerModeImmutable, WorkerModeOffline, WorkerModeLive} {
		c, err := CreateParent()
		if err != nil {
			t.Fatal(err)
		}
		budget := &recovery.RecoveryBudget{
			MaxHeapBytes:     1 << 20,
			MaxOutputPages:   100,
			MaxOpenFiles:     2,
			MaxScratchBytes:  1 << 18,
			MaxScratchFiles:  2,
			ScratchDirectory: "/tmp/scratch",
		}
		pages := []uint32{3, 9}
		if err := WriteRecoveryRequest(c, "/tmp/source.v4", "/tmp/output.v4", testCandidate(), mode, budget, testOutputAttempt(), pages, 5); err != nil {
			t.Fatalf("write %v: %v", mode, err)
		}
		request, err := ReadRecoveryRequest(c)
		if err != nil {
			t.Fatalf("read %v: %v", mode, err)
		}
		if request.SourcePath != "/tmp/source.v4" || request.DestinationPath != "/tmp/output.v4" ||
			request.Mode != mode || request.Candidate != *testCandidate() || request.DeliveredUnknowns != 5 ||
			len(request.UnreadablePages) != 2 || request.Output.IdentityPresent != true ||
			request.Budget.ScratchDirectory != "/tmp/scratch" {
			t.Fatalf("request = %+v", request)
		}
		if request.Budget.MaxHeapBytes != 1<<20-8 {
			t.Fatalf("heap after list = %d", request.Budget.MaxHeapBytes)
		}
		c.Close()
	}
}

func TestRecoveryOutcomeResultRoundTrip(t *testing.T) {
	outcome := &RecoveryOutcome{
		Result: &recovery.RecoveryResult{
			Report: recovery.RecoveryReport{
				Pages:                        recovery.RecoveryPageCounts{Examined: 10, Accepted: 8, Rejected: 2, IOUnreadable: 1},
				Ranges:                       recovery.RecoveryLogicalCounts{Examined: 9, Accepted: 7, Rejected: 2},
				VerifiedAddresses:            format.CardinalityFromUint64(100),
				RejectedAddresses:            format.CardinalityFromUint64(3),
				BoundedPossibleSpanAddresses: format.FullIPv6Space(),
				HasUnboundedUnknown:          true,
				UnknownEnvelopes:             4,
			},
			Scratch: &recovery.RecoveryScratchAttempt{
				AttemptID:         [16]byte{0x55},
				DirectoryIdentity: testIdentity(1, 2),
				CreationSecurity:  publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x66}},
			},
			Publication: publication.PublicationResult{
				Attempt: publication.PublicationAttempt{
					DatabaseID:          [16]byte{0x11},
					TransactionID:       7,
					CommitNonce:         [16]byte{0x22},
					DirectoryIdentity:   testIdentity(1, 2),
					DestinationBasename: []byte("out.v4"),
					OutputIdentity:      testIdentity(3, 4),
					PublicationPolicy:   publication.PolicyFailIfExists,
					ReservationIdentity: testIdentity(5, 6),
					CreationSecurity:    publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x55}},
				},
				Publication:         publication.PublicationPublished,
				DestinationContent:  publication.DestinationContentDesired,
				LaterCanonical:      publication.LaterCanonicalNone,
				CoordinationCleanup: publication.CoordinationCleanupNone,
				Housekeeping:        publication.HousekeepingNone,
			},
		},
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteRecoveryOutcome(c, outcome, nil); err != nil {
		t.Fatal("write outcome:", err)
	}
	got, retained, err := ReadRecoveryOutcome(c)
	if err != nil {
		t.Fatal("read outcome:", err)
	}
	if retained != nil {
		t.Fatalf("unexpected retained problem: %v", retained)
	}
	if got.Result == nil || got.Failure != nil {
		t.Fatalf("outcome = %+v", got)
	}
	result := got.Result
	if result.Report.Pages.Examined != 10 || result.Report.Ranges.Accepted != 7 ||
		result.Report.Pages.IOUnreadable != 1 || result.Report.UnknownEnvelopes != 4 ||
		!result.Report.HasUnboundedUnknown ||
		result.Report.VerifiedAddresses.Compare(format.CardinalityFromUint64(100)) != 0 ||
		result.Report.BoundedPossibleSpanAddresses.Compare(format.FullIPv6Space()) != 0 ||
		result.Scratch == nil || result.Scratch.AttemptID != [16]byte{0x55} ||
		result.Publication.Publication != publication.PublicationPublished {
		t.Fatalf("result = %+v", result)
	}
}

func TestRecoveryOutcomeFailureRoundTrip(t *testing.T) {
	cleanup := publication.NewCleanupArtifacts()
	cleanup.Push(publication.CleanupArtifact{
		Kind:              publication.ArtifactPrivateOutput,
		DirectoryRole:     publication.DirectoryRoleDestination,
		DirectoryIdentity: testIdentity(1, 2),
		Basename:          []byte("residue.tmp"),
		Error:             &format.Error{Code: format.CodeCleanupConflict, Detail: "residue"},
	})
	outcome := &RecoveryOutcome{
		Failure: &recovery.RecoveryPreparationFailure{
			Report: recovery.RecoveryReport{
				Pages: recovery.RecoveryPageCounts{Examined: 4},
			},
			Scratch: &recovery.RecoveryScratchAttempt{
				AttemptID:         [16]byte{0x55},
				DirectoryIdentity: testIdentity(1, 2),
				CreationSecurity:  publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x66}},
			},
			Output:              testOutputAttempt(),
			Cleanup:             cleanup,
			CoordinationCleanup: publication.CoordinationCleanupCleanupGuard,
			Housekeeping:        publication.HousekeepingCrashReappearancePossible,
			VisibleHousekeeping: nil,
			Cause:               &format.Error{Code: format.CodeLiveRecoveryCoordinationUnavailable, Detail: "coordination"},
		},
	}
	retained := &WireProblem{Code: format.CodeAccessPolicyUnsupported, Detail: "retained"}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteRecoveryOutcome(c, outcome, retained); err != nil {
		t.Fatal("write outcome:", err)
	}
	got, gotRetained, err := ReadRecoveryOutcome(c)
	if err != nil {
		t.Fatal("read outcome:", err)
	}
	if got.Result != nil || got.Failure == nil {
		t.Fatalf("outcome = %+v", got)
	}
	failure := got.Failure
	if failure.Report.Pages.Examined != 4 ||
		failure.Scratch == nil || failure.Scratch.AttemptID != [16]byte{0x55} ||
		failure.Output == nil || !failure.Output.IdentityPresent ||
		failure.Cleanup.Len() != 1 ||
		failure.CoordinationCleanup != publication.CoordinationCleanupCleanupGuard ||
		failure.Housekeeping != publication.HousekeepingCrashReappearancePossible ||
		failure.SourceCleanup != nil {
		t.Fatalf("failure = %+v", failure)
	}
	var cause *format.Error
	if !errors.As(failure.Cause, &cause) || cause.Code != format.CodeLiveRecoveryCoordinationUnavailable {
		t.Fatalf("failure cause = %v", failure.Cause)
	}
	if gotRetained == nil || gotRetained.Code != format.CodeAccessPolicyUnsupported || gotRetained.Detail != "retained" {
		t.Fatalf("retained = %+v", gotRetained)
	}
}

func TestRecoveryUnknownEnvelopeRoundTrip(t *testing.T) {
	page := uint32(3)
	interval := &validation.PhysicalByteInterval{Start: 4096, EndExclusive: 8192}
	fence := &validation.ValidationAddressFence{IPv4: true, From: 0x0a000001, To: 0x0a0000ff}
	envelope := &recovery.RecoveryUnknownEnvelope{
		Sequence:                  11,
		Reason:                    validation.ReasonPageCrcMismatch,
		Object:                    validation.ObjectRangeTree,
		PageNumber:                &page,
		PhysicalBytes:             interval,
		AddressFence:              fence,
		ContributesToPossibleSpan: true,
		HasUnboundedExtent:        false,
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteRecoveryUnknown(c, envelope); err != nil {
		t.Fatal("write unknown:", err)
	}
	got, err := ReadRecoveryUnknown(c)
	if err != nil {
		t.Fatal("read unknown:", err)
	}
	if got.Sequence != 11 || got.Reason != envelope.Reason || got.Object != envelope.Object ||
		got.PageNumber == nil || *got.PageNumber != page ||
		got.PhysicalBytes == nil || *got.PhysicalBytes != *interval ||
		got.AddressFence == nil || got.AddressFence.From != fence.From ||
		!got.ContributesToPossibleSpan || got.HasUnboundedExtent {
		t.Fatalf("envelope = %+v", got)
	}
	// An invalid reason class is corruption.
	bad, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	w := NewWireWriter(bad)
	if err := w.U64(1); err != nil {
		t.Fatal(err)
	}
	if err := w.Byte(200); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	_, err = ReadRecoveryUnknown(bad)
	wantCode(t, err, format.CodeFormatInvalid)
}

// TestCallbackCheckpointsRoundTripCompleteProgress ports the Rust
// client_tests callback_checkpoints_round_trip_complete_progress: a
// recovery report and a validation progress survive the callback
// checkpoint payloads exactly, sealed with their kinds.
func TestCallbackCheckpointsRoundTripCompleteProgress(t *testing.T) {
	control, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	report := &recovery.RecoveryReport{}
	report.Pages.Examined = 19
	report.Ranges.Accepted = 7
	report.UnknownEnvelopes = 3
	report.HasUnboundedUnknown = true
	control.BeginCallbackCheckpoint()
	w := NewWireCallbackWriter(control)
	if err := writeRecoveryReport(w, report); err != nil {
		t.Fatal("write report:", err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	control.SealCallbackCheckpoint(CallbackRecoveryReport)
	gotReport, err := ReadRecoveryCallbackReport(control)
	if err != nil {
		t.Fatal("read report:", err)
	}
	if *gotReport != *report {
		t.Fatalf("report = %+v, want %+v", gotReport, report)
	}

	progress := ProgressWire{
		CheckedUniquePages:           11,
		FindingCount:                 2,
		UntraversableSubgraphs:       1,
		BoundedPossibleSpanAddresses: format.CardinalityFromUint128(0, 29),
	}
	progress.ReasonCounts[validation.ReasonPageCrcMismatch] = 2
	progress.ObjectCounts[validation.ObjectRangeTree] = 11
	control.BeginCallbackCheckpoint()
	w2 := NewWireCallbackWriter(control)
	if err := writeProgress(w2, &progress); err != nil {
		t.Fatal("write progress:", err)
	}
	if err := w2.Finish(); err != nil {
		t.Fatal(err)
	}
	control.SealCallbackCheckpoint(CallbackValidationProgress)
	gotProgress, err := ReadValidationProgress(control)
	if err != nil {
		t.Fatal("read progress:", err)
	}
	if gotProgress == nil || *gotProgress != progress {
		t.Fatalf("progress = %+v, want %+v", gotProgress, progress)
	}
	// A fresh checkpoint without a seal reads nil with no error.
	control.BeginCallbackCheckpoint()
	missing, err := ReadValidationProgress(control)
	if err != nil || missing != nil {
		t.Fatalf("missing progress = %v %v", missing, err)
	}
	// The recovery report reader rejects a missing seal.
	control.BeginCallbackCheckpoint()
	_, err = ReadRecoveryCallbackReport(control)
	wantCode(t, err, format.CodeConflict)
}

func TestRecoveryBudgetRoundTrip(t *testing.T) {
	budget := &recovery.RecoveryBudget{
		MaxHeapBytes:     1 << 20,
		MaxOutputPages:   100,
		MaxOpenFiles:     2,
		MaxScratchBytes:  1 << 18,
		MaxScratchFiles:  2,
		ScratchDirectory: "/tmp/scratch",
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writeRecoveryBudget(w, budget); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readRecoveryBudget(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got != *budget {
		t.Fatalf("budget = %+v", got)
	}
}
