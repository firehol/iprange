//go:build linux && amd64

// Byte-identical cross-language worker-wire fixtures (SOW-0025
// milestone-3 gate fix): the four worker-wire envelopes below were
// produced by the ACTUAL Rust codec (v4/rust/iprange-livedb/src/worker
// wire.rs, wire_validation.rs, wire_recovery.rs, wire_publication.rs,
// wire_cleanup.rs) and are asserted byte-identical by the Go reader
// AND the Go writer. The in-process Go doubles share the Go codecs, so
// in-language round trips could hide a symmetric field-order/width bug
// that breaks the Rust byte contract; these Rust-produced vectors close
// that proof gap.
//
// Rust commit (v4/rust tree at generation time): 304d99a2350f
// (repo HEAD `git rev-parse --short=12 HEAD` when the temporary Rust
// generator file was present in the working tree; it was never
// committed; generation ran `nice cargo test
// --manifest-path v4/rust/Cargo.toml -p iprange-livedb --lib
// wire_vector_gen -- --nocapture`).
//
// Representative values (identical in the Rust generator and here):
//
// PROGRESS (Rust wire::progress):
//   checked_unique_pages=1, finding_count=2,
//   untraversable_subgraphs=3,
//   bounded_possible_span_addresses=Cardinality129(bit128=0,hi=0,lo=4)
//     (Rust Cardinality129::from_u128(4)),
//   has_unbounded_unknown=true,
//   reason_counts[PageCrcMismatch (index 9)]=1, all other reasons 0,
//   object_counts[RangeTree (index 2)]=1, all other objects 0.
//
// RECOVERY OUTCOME (Rust wire_recovery::write_outcome, failure arm):
//   tag=1;
//   report.pages.examined=6, report.pages.accepted=5, every other
//     report counter and cardinality 0, has_unbounded_unknown=false,
//     unknown_envelopes=0;
//   scratch=none, output=none, cleanup=empty, coordination=None,
//     housekeeping=None, visible_housekeeping=empty;
//   cause={code=Io(31), no errno, "deliberate recovery failure cause"};
//   retained problem={code=CleanupConflict(42), errno=17,
//     "exact worker detail outside any static registry"}.
//
// PUBLICATION RESULT (Rust wire_publication::result):
//   attempt.database_id=0x11*16, transaction_id=5,
//     commit_nonce=0x22*16, publication_attempt_id=0x33*16,
//     directory_identity=identity(11,12),
//     destination_basename_encoding=1, destination_basename="out.v4",
//     output_identity=identity(21,22), output_byte_length=4096,
//     output_sha512=0x44*64, policy=ReplaceExisting,
//     previous_destination=identity(41,42), byte_length=4096,
//     sha512=0x88*64, reservation_identity=identity(31,32),
//     creation_security={kind=1, commitment=0x55*32};
//   main_namespace_may_have_been_attempted=true,
//   publication=Published, destination_content=Desired,
//   later_canonical=None, live_lineage=SameGenerationExactBytes,
//   later_attempt_or_sidecar_id=0x66*16,
//   later_selected_transaction_id=7,
//   later_selected_commit_nonce=0x77*16,
//   main_access_policy=CreatorOnly, coordination_access_policy=Absent,
//   cleanup=empty, coordination_cleanup=None, housekeeping=None,
//   visible_housekeeping=empty, cause=None.
//
//   identity(dev,inode) is the portable unix identity: kind=1, device
//   little-endian at bytes 0..8, inode little-endian at bytes 8..16.
//
// CLEANUP RESULT (Rust wire_cleanup::write_result):
//   output.publication_attempt_id=0x39*16,
//     output.directory_identity=identity(51,52),
//     output.basename_encoding=1,
//     output.basename=".iprange-publish-vector.tmp",
//     output.identity=identity(53,54) present,
//     output.creation_security={kind=1, commitment=0x5a*32};
//   artifact=PrivateOutput/Destination, directory_identity=(51,52),
//     basename_encoding=1,
//     basename=".iprange-publish-vector.tmp", identity=(53,54),
//     creation_security={kind=1, commitment=0x5a*32},
//     unpublished_tail=none,
//     error={code=Io(31), no errno, "deliberate cleanup residue"};
//   housekeeping=None, visible_housekeeping=empty, scratch=None.
//
// Regeneration procedure:
//   1. Re-create the temporary Rust test at
//      v4/rust/iprange-livedb/src/worker/wire_vector_gen_test.rs
//      (module registered in worker.rs with
//      `#[cfg(all(test, unix))]
//      #[path = "worker/wire_vector_gen_test.rs"]
//      mod wire_vector_gen;`), using the exact values in the table
//      above and printing "NAME: <hex of payload bytes>" after each
//      writer's finish(), with payload_len read back from the control.
//   2. `nice cargo test --manifest-path v4/rust/Cargo.toml -p
//      iprange-livedb --lib wire_vector_gen -- --nocapture`.
//   3. Copy the four printed hexes below, delete the temporary file
//      and its mod line, and re-run these tests.
//
// If a direction ever diverges from the Rust vector, that is a REAL
// cross-language wire divergence: report it as a finding, do not edit
// the fixture to hide it.

package worker

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

const rustVectorProgressHex = "0100000000000000020000000000000003000000000000000000000000000000000400000000000000010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000100000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

const rustVectorRecoveryOutcomeHex = "0106000000000000000500000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000101001f000000002100000064656c69626572617465207265636f76657279206661696c757265206361757365012a00000001110000002f000000657861637420776f726b65722064657461696c206f75747369646520616e7920737461746963207265676973747279"

const rustVectorPublicationResultHex = "111111111111111111111111111111110500000000000000222222222222222222222222222222223333333333333333333333333333333301000b000000000000000c00000000000000000000000000000000000000000000000100060000006f75742e7634010015000000000000001600000000000000000000000000000000000000000000000010000000000000444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444444440201010029000000000000002a000000000000000000000000000000000000000000000000100000000000008888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888888801001f00000000000000200000000000000000000000000000000000000000000000010055555555555555555555555555555555555555555555555555555555555555550102010101010166666666666666666666666666666666010700000000000000017777777777777777777777777777777702010001010000"

const rustVectorCleanupResultHex = "393939393939393939393939393939390100330000000000000034000000000000000000000000000000000000000000000001001b0000002e697072616e67652d7075626c6973682d766563746f722e746d70010100350000000000000036000000000000000000000000000000000000000000000001005a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a0101010100330000000000000034000000000000000000000000000000000000000000000001001b0000002e697072616e67652d7075626c6973682d766563746f722e746d7001010035000000000000003600000000000000000000000000000000000000000000000101005a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a001f000000001a00000064656c6962657261746520636c65616e75702072657369647565010000"

// rustVectorControl creates a fresh control whose payload is the exact
// fixture bytes with the sealed payload length, the shape a Rust
// writer finish() leaves behind (Rust Writer::finish sets
// PAYLOAD_LEN_AT).
func rustVectorControl(t *testing.T, payload []byte) *Control {
	t.Helper()
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	t.Cleanup(c.Close)
	if err := c.WritePayload(0, payload); err != nil {
		t.Fatal("seed payload:", err)
	}
	if err := c.SetPayloadLen(len(payload)); err != nil {
		t.Fatal("seed payload len:", err)
	}
	return c
}

// rustVectorPayload returns the sealed payload bytes of one control.
func rustVectorPayload(t *testing.T, c *Control) []byte {
	t.Helper()
	length, err := c.PayloadLen()
	if err != nil {
		t.Fatal("payload len:", err)
	}
	return c.data[offPayload : offPayload+length]
}

func rustVectorFixture(t *testing.T, hexValue string) []byte {
	t.Helper()
	value, err := hex.DecodeString(hexValue)
	if err != nil {
		t.Fatal("fixture hex:", err)
	}
	return value
}

// rustVector16/32/64 build the repeated-byte arrays used by the
// fixture value table.
func rustVector16(value byte) (out [16]byte) {
	for i := range out {
		out[i] = value
	}
	return
}

func rustVector32(value byte) (out [32]byte) {
	for i := range out {
		out[i] = value
	}
	return
}

func rustVector64(value byte) (out [64]byte) {
	for i := range out {
		out[i] = value
	}
	return
}

// rustVectorIdentity is identity(device, inode) from the fixture
// table: kind 1 with the little-endian device/inode pair.
func rustVectorIdentity(device, inode uint64) publication.LocalFileIdentity {
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}

// rustVectorI32 returns a pointer to one int32 fixture value.
func rustVectorI32(value int32) *int32 { return &value }

// equalProblemError compares one decoded error against the exact
// format.Error code and detail of the fixture (the decoded
// non-errno problem is always a *format.Error; errno is dropped by
// WireProblem.Err like every Go arm).
func equalProblemError(err error, code format.ErrorCode, detail string) bool {
	formatted, ok := err.(*format.Error)
	return ok && formatted.Code == code && formatted.Detail == detail
}

// equalPublicationResult is the field-wise fixture comparison (the
// struct holds slices and an error interface, so == is not usable).
func equalPublicationResult(got, want publication.PublicationResult) bool {
	if got.Attempt.DatabaseID != want.Attempt.DatabaseID ||
		got.Attempt.TransactionID != want.Attempt.TransactionID ||
		got.Attempt.CommitNonce != want.Attempt.CommitNonce ||
		got.Attempt.PublicationAttemptID != want.Attempt.PublicationAttemptID ||
		got.Attempt.DirectoryIdentity != want.Attempt.DirectoryIdentity ||
		got.Attempt.DestinationBasenameEncoding != want.Attempt.DestinationBasenameEncoding ||
		!bytes.Equal(got.Attempt.DestinationBasename, want.Attempt.DestinationBasename) ||
		got.Attempt.OutputIdentity != want.Attempt.OutputIdentity ||
		got.Attempt.OutputByteLength != want.Attempt.OutputByteLength ||
		got.Attempt.OutputSHA512 != want.Attempt.OutputSHA512 ||
		got.Attempt.PublicationPolicy != want.Attempt.PublicationPolicy ||
		got.Attempt.ReservationIdentity != want.Attempt.ReservationIdentity ||
		got.Attempt.CreationSecurity != want.Attempt.CreationSecurity {
		return false
	}
	switch {
	case (got.Attempt.PreviousDestination == nil) != (want.Attempt.PreviousDestination == nil):
		return false
	case got.Attempt.PreviousDestination != nil:
		gotPrevious := got.Attempt.PreviousDestination
		wantPrevious := want.Attempt.PreviousDestination
		if gotPrevious.Identity != wantPrevious.Identity ||
			gotPrevious.ByteLength != wantPrevious.ByteLength ||
			gotPrevious.SHA512 != wantPrevious.SHA512 {
			return false
		}
	}
	if got.MainNamespaceMayHaveBeenAttempted != want.MainNamespaceMayHaveBeenAttempted ||
		got.Publication != want.Publication ||
		got.DestinationContent != want.DestinationContent ||
		got.LaterCanonical != want.LaterCanonical ||
		got.MainAccessPolicy != want.MainAccessPolicy ||
		got.CoordinationAccessPolicy != want.CoordinationAccessPolicy ||
		got.CoordinationCleanup != want.CoordinationCleanup ||
		got.Housekeeping != want.Housekeeping {
		return false
	}
	switch {
	case (got.LiveLineage == nil) != (want.LiveLineage == nil):
		return false
	case got.LiveLineage != nil && *got.LiveLineage != *want.LiveLineage:
		return false
	case (got.LaterAttemptOrSidecarID == nil) != (want.LaterAttemptOrSidecarID == nil):
		return false
	case got.LaterAttemptOrSidecarID != nil && *got.LaterAttemptOrSidecarID != *want.LaterAttemptOrSidecarID:
		return false
	case (got.LaterSelectedTransactionID == nil) != (want.LaterSelectedTransactionID == nil):
		return false
	case got.LaterSelectedTransactionID != nil && *got.LaterSelectedTransactionID != *want.LaterSelectedTransactionID:
		return false
	case (got.LaterSelectedCommitNonce == nil) != (want.LaterSelectedCommitNonce == nil):
		return false
	case got.LaterSelectedCommitNonce != nil && *got.LaterSelectedCommitNonce != *want.LaterSelectedCommitNonce:
		return false
	case (got.Cause == nil) != (want.Cause == nil):
		return false
	case got.Cause != nil:
		wantProblem, ok := want.Cause.(*format.Error)
		if !ok || !equalProblemError(got.Cause, wantProblem.Code, wantProblem.Detail) {
			return false
		}
	}
	if got.Cleanup.Len() != want.Cleanup.Len() || len(got.VisibleHousekeeping) != len(want.VisibleHousekeeping) {
		return false
	}
	return true
}

// equalCleanupArtifact is the field-wise fixture comparison for one
// cleanup artifact.
func equalCleanupArtifact(got, want publication.CleanupArtifact) bool {
	if got.Kind != want.Kind ||
		got.DirectoryRole != want.DirectoryRole ||
		got.DirectoryIdentity != want.DirectoryIdentity ||
		got.BasenameEncoding != want.BasenameEncoding ||
		!bytes.Equal(got.Basename, want.Basename) ||
		(got.Identity == nil) != (want.Identity == nil) ||
		(got.CreationSecurity == nil) != (want.CreationSecurity == nil) ||
		(got.UnpublishedTail == nil) != (want.UnpublishedTail == nil) {
		return false
	}
	if got.Identity != nil && *got.Identity != *want.Identity {
		return false
	}
	if got.CreationSecurity != nil && *got.CreationSecurity != *want.CreationSecurity {
		return false
	}
	wantProblem, ok := want.Error.(*format.Error)
	if !ok {
		return false
	}
	return equalProblemError(got.Error, wantProblem.Code, wantProblem.Detail)
}

// rustVectorProgressValue is the PROGRESS fixture (wire table above).
func rustVectorProgressValue() ProgressWire {
	value := ProgressWire{
		CheckedUniquePages:     1,
		FindingCount:           2,
		UntraversableSubgraphs: 3,
		HasUnboundedUnknown:    true,
	}
	value.BoundedPossibleSpanAddresses, _ = format.NewCardinality129(0, 0, 4)
	value.ReasonCounts[validation.ReasonPageCrcMismatch] = 1
	value.ObjectCounts[validation.ObjectRangeTree] = 1
	return value
}

// rustVectorRecoveryFailureValue is the RECOVERY OUTCOME failure-arm
// fixture.
func rustVectorRecoveryFailureValue() *recovery.RecoveryPreparationFailure {
	return &recovery.RecoveryPreparationFailure{
		Report: recovery.RecoveryReport{
			Pages: recovery.RecoveryPageCounts{Examined: 6, Accepted: 5},
		},
		Cleanup:             publication.NewCleanupArtifacts(),
		CoordinationCleanup: publication.CoordinationCleanupNone,
		Housekeeping:        publication.HousekeepingNone,
		Cause:               &format.Error{Code: format.CodeIO, Detail: "deliberate recovery failure cause"},
	}
}

// rustVectorRetainedProblemValue is the retained-problem fixture of
// the RECOVERY OUTCOME failure arm.
func rustVectorRetainedProblemValue() *WireProblem {
	return &WireProblem{
		Code:   format.CodeCleanupConflict,
		OSCode: rustVectorI32(17),
		Detail: "exact worker detail outside any static registry",
	}
}

// rustVectorPublicationResultValue is the PUBLICATION RESULT fixture.
func rustVectorPublicationResultValue() publication.PublicationResult {
	lineage := publication.LiveLineageSameGenerationExactBytes
	laterID := rustVector16(0x66)
	laterTransaction := uint64(7)
	laterNonce := rustVector16(0x77)
	return publication.PublicationResult{
		Attempt: publication.PublicationAttempt{
			DatabaseID:                  rustVector16(0x11),
			TransactionID:               5,
			CommitNonce:                 rustVector16(0x22),
			PublicationAttemptID:        rustVector16(0x33),
			DirectoryIdentity:           rustVectorIdentity(11, 12),
			DestinationBasenameEncoding: 1,
			DestinationBasename:         []byte("out.v4"),
			OutputIdentity:              rustVectorIdentity(21, 22),
			OutputByteLength:            4096,
			OutputSHA512:                rustVector64(0x44),
			PublicationPolicy:           publication.PolicyReplaceExisting,
			PreviousDestination: &publication.PreviousDestination{
				Identity:   rustVectorIdentity(41, 42),
				ByteLength: 4096,
				SHA512:     rustVector64(0x88),
			},
			ReservationIdentity: rustVectorIdentity(31, 32),
			CreationSecurity: publication.CreationSecurity{
				Kind:       1,
				Commitment: rustVector32(0x55),
			},
		},
		MainNamespaceMayHaveBeenAttempted: true,
		Publication:                       publication.PublicationPublished,
		DestinationContent:                publication.DestinationContentDesired,
		LaterCanonical:                    publication.LaterCanonicalNone,
		LiveLineage:                       &lineage,
		LaterAttemptOrSidecarID:           &laterID,
		LaterSelectedTransactionID:        &laterTransaction,
		LaterSelectedCommitNonce:          &laterNonce,
		MainAccessPolicy:                  publication.AccessPolicyCreatorOnly,
		CoordinationAccessPolicy:          publication.AccessPolicyAbsent,
		Cleanup:                           publication.NewCleanupArtifacts(),
		CoordinationCleanup:               publication.CoordinationCleanupNone,
		Housekeeping:                      publication.HousekeepingNone,
	}
}

// rustVectorCleanupDiscardValue is the CLEANUP RESULT fixture.
func rustVectorCleanupDiscardValue() *EarlyDiscard {
	outputIdentity := rustVectorIdentity(53, 54)
	security := publication.CreationSecurity{Kind: 1, Commitment: rustVector32(0x5a)}
	return &EarlyDiscard{
		Output: publication.PrivateOutputAttempt{
			PublicationAttemptID: rustVector16(0x39),
			DirectoryIdentity:    rustVectorIdentity(51, 52),
			BasenameEncoding:     1,
			Basename:             []byte(".iprange-publish-vector.tmp"),
			Identity:             outputIdentity,
			IdentityPresent:      true,
			CreationSecurity:     security,
		},
		Artifact: &publication.CleanupArtifact{
			Kind:              publication.ArtifactPrivateOutput,
			DirectoryRole:     publication.DirectoryRoleDestination,
			DirectoryIdentity: rustVectorIdentity(51, 52),
			BasenameEncoding:  1,
			Basename:          []byte(".iprange-publish-vector.tmp"),
			Identity:          &outputIdentity,
			CreationSecurity:  &security,
			Error:             &format.Error{Code: format.CodeIO, Detail: "deliberate cleanup residue"},
		},
		Housekeeping: publication.HousekeepingNone,
	}
}

// TestRustVectorProgressDecode asserts the Go progress reader decodes
// the Rust-produced PROGRESS envelope to the fixture values.
func TestRustVectorProgressDecode(t *testing.T) {
	c := rustVectorControl(t, rustVectorFixture(t, rustVectorProgressHex))
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal("open reader:", err)
	}
	got, err := readProgress(r)
	if err != nil {
		t.Fatal("decode progress:", err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal("finish:", err)
	}
	if want := rustVectorProgressValue(); got != want {
		t.Fatalf("decoded progress = %+v, want %+v", got, want)
	}
}

// TestRustVectorRecoveryOutcomeDecode asserts the Go recovery-outcome
// reader decodes the Rust-produced RECOVERY OUTCOME failure arm to the
// fixture values.
func TestRustVectorRecoveryOutcomeDecode(t *testing.T) {
	c := rustVectorControl(t, rustVectorFixture(t, rustVectorRecoveryOutcomeHex))
	outcome, retained, err := ReadRecoveryOutcome(c)
	if err != nil {
		t.Fatal("decode recovery outcome:", err)
	}
	if outcome.Result != nil || outcome.Failure == nil {
		t.Fatalf("decoded outcome = result %+v failure %+v, want the failure arm", outcome.Result, outcome.Failure)
	}
	failure := outcome.Failure
	want := rustVectorRecoveryFailureValue()
	if failure.Report != want.Report {
		t.Fatalf("decoded report = %+v, want %+v", failure.Report, want.Report)
	}
	if failure.Scratch != nil || failure.Output != nil {
		t.Fatalf("decoded scratch/output = %+v/%+v, want both nil", failure.Scratch, failure.Output)
	}
	if failure.Cleanup.Len() != 0 {
		t.Fatalf("decoded cleanup has %d artifacts, want none", failure.Cleanup.Len())
	}
	if failure.CoordinationCleanup != want.CoordinationCleanup || failure.Housekeeping != want.Housekeeping {
		t.Fatalf("decoded coordination/housekeeping = %v/%v, want %v/%v",
			failure.CoordinationCleanup, failure.Housekeeping, want.CoordinationCleanup, want.Housekeeping)
	}
	if len(failure.VisibleHousekeeping) != 0 {
		t.Fatalf("decoded visible housekeeping has %d artifacts, want none", len(failure.VisibleHousekeeping))
	}
	if !equalProblemError(failure.Cause, format.CodeIO, "deliberate recovery failure cause") {
		t.Fatalf("decoded cause = %v, want Io(31) 'deliberate recovery failure cause'", failure.Cause)
	}
	wantRetained := rustVectorRetainedProblemValue()
	if retained == nil || retained.Code != wantRetained.Code ||
		retained.OSCode == nil || *retained.OSCode != *wantRetained.OSCode ||
		retained.Detail != wantRetained.Detail {
		t.Fatalf("decoded retained = %+v, want %+v", retained, wantRetained)
	}
}

// TestRustVectorPublicationResultDecode asserts the Go publication
// result reader decodes the Rust-produced PUBLICATION RESULT envelope
// to the fixture values.
func TestRustVectorPublicationResultDecode(t *testing.T) {
	c := rustVectorControl(t, rustVectorFixture(t, rustVectorPublicationResultHex))
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal("open reader:", err)
	}
	got, err := readPublicationResult(r)
	if err != nil {
		t.Fatal("decode publication result:", err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal("finish:", err)
	}
	want := rustVectorPublicationResultValue()
	if !equalPublicationResult(got, want) {
		t.Fatalf("decoded publication result = %+v, want %+v", got, want)
	}
}

// TestRustVectorCleanupResultDecode asserts the Go cleanup result
// reader decodes the Rust-produced CLEANUP RESULT envelope to the
// fixture values.
func TestRustVectorCleanupResultDecode(t *testing.T) {
	c := rustVectorControl(t, rustVectorFixture(t, rustVectorCleanupResultHex))
	discarded, scratch, err := ReadCleanupResult(c)
	if err != nil {
		t.Fatal("decode cleanup result:", err)
	}
	if scratch != nil {
		t.Fatalf("decoded scratch = %+v, want nil", scratch)
	}
	want := rustVectorCleanupDiscardValue()
	if !equalPrivateOutput(&discarded.Output, &want.Output) {
		t.Fatalf("decoded output = %+v, want %+v", discarded.Output, want.Output)
	}
	if discarded.Artifact == nil || want.Artifact == nil {
		t.Fatalf("decoded artifact = %+v, want %+v", discarded.Artifact, want.Artifact)
	}
	if !equalCleanupArtifact(*discarded.Artifact, *want.Artifact) {
		t.Fatalf("decoded artifact = %+v, want %+v", *discarded.Artifact, *want.Artifact)
	}
	if discarded.Housekeeping != want.Housekeeping || len(discarded.VisibleHousekeeping) != 0 {
		t.Fatalf("decoded housekeeping = %v/%d artifacts, want None/none",
			discarded.Housekeeping, len(discarded.VisibleHousekeeping))
	}
}

// TestRustVectorProgressEncode asserts the Go progress writer emits
// the Rust PROGRESS envelope byte-identically.
func TestRustVectorProgressEncode(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	t.Cleanup(c.Close)
	value := rustVectorProgressValue()
	w := NewWireWriter(c)
	if err := writeProgress(w, &value); err != nil {
		t.Fatal("write progress:", err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal("seal:", err)
	}
	want := rustVectorFixture(t, rustVectorProgressHex)
	if got := rustVectorPayload(t, c); !bytes.Equal(got, want) {
		t.Fatalf("encoded progress differs from the Rust vector:\n got %x\nwant %x", got, want)
	}
}

// TestRustVectorRecoveryOutcomeEncode asserts the Go recovery-outcome
// writer emits the Rust RECOVERY OUTCOME failure arm byte-identically.
func TestRustVectorRecoveryOutcomeEncode(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	t.Cleanup(c.Close)
	outcome := &RecoveryOutcome{Failure: rustVectorRecoveryFailureValue()}
	if err := WriteRecoveryOutcome(c, outcome, rustVectorRetainedProblemValue()); err != nil {
		t.Fatal("write recovery outcome:", err)
	}
	want := rustVectorFixture(t, rustVectorRecoveryOutcomeHex)
	if got := rustVectorPayload(t, c); !bytes.Equal(got, want) {
		t.Fatalf("encoded recovery outcome differs from the Rust vector:\n got %x\nwant %x", got, want)
	}
}

// TestRustVectorPublicationResultEncode asserts the Go publication
// result writer emits the Rust PUBLICATION RESULT envelope
// byte-identically.
func TestRustVectorPublicationResultEncode(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	t.Cleanup(c.Close)
	value := rustVectorPublicationResultValue()
	w := NewWireWriter(c)
	if err := writePublicationResult(w, &value); err != nil {
		t.Fatal("write publication result:", err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal("seal:", err)
	}
	want := rustVectorFixture(t, rustVectorPublicationResultHex)
	if got := rustVectorPayload(t, c); !bytes.Equal(got, want) {
		t.Fatalf("encoded publication result differs from the Rust vector:\n got %x\nwant %x", got, want)
	}
}

// TestRustVectorCleanupResultEncode asserts the Go cleanup result
// writer emits the Rust CLEANUP RESULT envelope byte-identically.
func TestRustVectorCleanupResultEncode(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	t.Cleanup(c.Close)
	if err := WriteCleanupResult(c, rustVectorCleanupDiscardValue(), nil); err != nil {
		t.Fatal("write cleanup result:", err)
	}
	want := rustVectorFixture(t, rustVectorCleanupResultHex)
	if got := rustVectorPayload(t, c); !bytes.Equal(got, want) {
		t.Fatalf("encoded cleanup result differs from the Rust vector:\n got %x\nwant %x", got, want)
	}
}
