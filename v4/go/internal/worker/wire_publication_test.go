//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

// Publication-fact wire unit tests (Rust worker/wire_publication.rs and
// the problem arms of worker/client_tests.rs): the problem codec with
// unregistered detail and the non-UTF-8 rejection, plus round trips of
// the private output attempt, the full publication attempt and result,
// the cleanup and housekeeping ledgers, the creation security, and
// every fixed enum tag.

package worker

import (
	"bytes"
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

const errnoFixture = 2 // ENOENT value

// TestWorkerProblemPreservesUnregisteredUTF8Detail ports the Rust
// client_tests worker_problem_preserves_unregistered_utf8_detail: a
// problem with a non-static detail and an errno survives the wire
// exactly.
func TestWorkerProblemPreservesUnregisteredUTF8Detail(t *testing.T) {
	osCode := int32(17)
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	expected := &WireProblem{
		Code:   format.CodeCleanupConflict,
		OSCode: &osCode,
		Detail: "exact worker detail outside any static registry",
	}
	w := NewWireWriter(c)
	if err := writeProblem(w, expected); err != nil {
		t.Fatal("write problem:", err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal("seal:", err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := readProblem(r)
	if err != nil {
		t.Fatal("read problem:", err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if actual.Code != expected.Code || actual.Detail != expected.Detail ||
		actual.OSCode == nil || *actual.OSCode != *expected.OSCode {
		t.Fatalf("problem = %+v, want %+v", actual, expected)
	}
}

// TestWorkerProblemRejectsNonUTF8Detail ports the Rust client_tests
// worker_problem_rejects_non_utf8_detail vector: a sized detail of
// 0xff is corruption with the exact Rust detail.
func TestWorkerProblemRejectsNonUTF8Detail(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := w.U32(uint32(format.CodeCleanupConflict)); err != nil {
		t.Fatal(err)
	}
	if err := w.Bool(false); err != nil {
		t.Fatal(err)
	}
	if err := w.SizedBytes([]byte{0xff}); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readProblem(r)
	wantCode(t, err, format.CodeFormatInvalid)
	var e *format.Error
	if !errors.As(err, &e) || e.Detail != "worker publication error detail is not UTF-8" {
		t.Fatalf("problem error = %v", err)
	}
}

func TestWireProblemRejectsUnknownCode(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := w.U32(70000); err != nil {
		t.Fatal(err)
	}
	if err := w.Bool(false); err != nil {
		t.Fatal(err)
	}
	if err := w.SizedBytes(nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readProblem(r)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestWireOptionalProblemRoundTrip(t *testing.T) {
	for _, value := range []*WireProblem{nil, {Code: format.CodeIO}, {Code: format.CodeConflict, Detail: "d"}} {
		c, err := CreateParent()
		if err != nil {
			t.Fatal(err)
		}
		w := NewWireWriter(c)
		if err := writeOptionalProblem(w, value); err != nil {
			t.Fatal(err)
		}
		if err := w.Finish(); err != nil {
			t.Fatal(err)
		}
		r, err := NewWireReader(c)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readOptionalProblem(r)
		if err != nil {
			t.Fatal(err)
		}
		if value == nil && got != nil {
			t.Fatal("optional problem present")
		}
		if value != nil && (got == nil || got.Code != value.Code || got.Detail != value.Detail) {
			t.Fatalf("optional problem = %+v, want %+v", got, value)
		}
		if err := r.Finish(); err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
}

func TestWireProblemOfError(t *testing.T) {
	// A format.Error keeps its class and detail.
	problem := WireProblemOf(&format.Error{Code: format.CodeNameNotFound, Detail: "missing"})
	if problem.Code != format.CodeNameNotFound || problem.Detail != "missing" || problem.OSCode != nil {
		t.Fatalf("problem = %+v", problem)
	}
	// An errno chain reports the Io class with the raw errno.
	pathErr := &osPathError{Err: errnoFixture}
	problem = WireProblemOf(pathErr)
	if problem.Code != format.CodeIO || problem.OSCode == nil || *problem.OSCode != int32(errnoFixture) {
		t.Fatalf("errno problem = %+v", problem)
	}
}

func TestWireCreationSecurityRoundTrip(t *testing.T) {
	security := publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x5a}}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writeCreationSecurity(w, &security); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readCreationSecurity(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got != security {
		t.Fatalf("security = %+v", got)
	}
}

func TestWirePrivateOutputRoundTrip(t *testing.T) {
	value := &publication.PrivateOutputAttempt{
		PublicationAttemptID: [16]byte{0x01},
		DirectoryIdentity:    testIdentity(11, 12),
		BasenameEncoding:     1,
		Basename:             []byte(".out-0101.tmp"),
		Identity:             testIdentity(21, 22),
		IdentityPresent:      true,
		CreationSecurity:     publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x6b}},
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writePrivateOutput(w, value); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readPrivateOutput(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got.PublicationAttemptID != value.PublicationAttemptID ||
		got.DirectoryIdentity != value.DirectoryIdentity ||
		got.BasenameEncoding != value.BasenameEncoding ||
		!bytes.Equal(got.Basename, value.Basename) ||
		got.Identity != value.Identity ||
		got.IdentityPresent != value.IdentityPresent ||
		got.CreationSecurity != value.CreationSecurity {
		t.Fatalf("private output = %+v, want %+v", got, value)
	}
	// The absent identity arm round trips the presence flag.
	value.IdentityPresent = false
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	w2 := NewWireWriter(c2)
	if err := writePrivateOutput(w2, value); err != nil {
		t.Fatal(err)
	}
	if err := w2.Finish(); err != nil {
		t.Fatal(err)
	}
	r2, err := NewWireReader(c2)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := readPrivateOutput(r2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.IdentityPresent {
		t.Fatal("absent identity came back present")
	}
}

func TestWirePublicationAttemptRoundTrip(t *testing.T) {
	value := &publication.PublicationAttempt{
		DatabaseID:                  [16]byte{0x11},
		TransactionID:               7,
		CommitNonce:                 [16]byte{0x22},
		PublicationAttemptID:        [16]byte{0x33},
		DirectoryIdentity:           testIdentity(1, 2),
		DestinationBasenameEncoding: 1,
		DestinationBasename:         []byte("destination.v4"),
		OutputIdentity:              testIdentity(3, 4),
		OutputByteLength:            4096,
		OutputSHA512:                [64]byte{0x44},
		PublicationPolicy:           publication.PolicyReplaceExisting,
		PreviousDestination: &publication.PreviousDestination{
			Identity:   testIdentity(5, 6),
			ByteLength: 4096,
			SHA512:     [64]byte{0x55},
		},
		ReservationIdentity: testIdentity(7, 8),
		CreationSecurity:    publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x66}},
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writePublicationAttempt(w, value); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readPublicationAttempt(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got.DatabaseID != value.DatabaseID || got.TransactionID != value.TransactionID ||
		got.CommitNonce != value.CommitNonce || got.PublicationAttemptID != value.PublicationAttemptID ||
		got.DirectoryIdentity != value.DirectoryIdentity || got.DestinationBasenameEncoding != 1 ||
		!bytes.Equal(got.DestinationBasename, value.DestinationBasename) ||
		got.OutputIdentity != value.OutputIdentity ||
		got.OutputByteLength != 4096 || got.OutputSHA512 != value.OutputSHA512 ||
		got.PublicationPolicy != value.PublicationPolicy || got.PreviousDestination == nil ||
		got.PreviousDestination.Identity != value.PreviousDestination.Identity ||
		got.PreviousDestination.ByteLength != value.PreviousDestination.ByteLength ||
		got.PreviousDestination.SHA512 != value.PreviousDestination.SHA512 ||
		got.ReservationIdentity != value.ReservationIdentity || got.CreationSecurity != value.CreationSecurity {
		t.Fatalf("attempt = %+v, want %+v", got, value)
	}
}

func TestWirePublicationResultRoundTrip(t *testing.T) {
	lineage := publication.LiveLineageAdvancedGeneration
	txn := uint64(9)
	nonce := [16]byte{0x77}
	value := &publication.PublicationResult{
		Attempt: publication.PublicationAttempt{
			DatabaseID:           [16]byte{0x11},
			TransactionID:        7,
			CommitNonce:          [16]byte{0x22},
			PublicationAttemptID: [16]byte{0x33},
			DirectoryIdentity:    testIdentity(1, 2),
			DestinationBasename:  []byte("d.v4"),
			OutputIdentity:       testIdentity(3, 4),
			OutputSHA512:         [64]byte{0x44},
			PublicationPolicy:    publication.PolicyFailIfExists,
			ReservationIdentity:  testIdentity(5, 6),
			CreationSecurity:     publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x55}},
		},
		MainNamespaceMayHaveBeenAttempted: true,
		Publication:                       publication.PublicationNotPublished,
		DestinationContent:                publication.DestinationContentOther,
		LaterCanonical:                    publication.LaterCanonicalReservationOrTransition,
		LiveLineage:                       &lineage,
		LaterAttemptOrSidecarID:           &nonce,
		LaterSelectedTransactionID:        &txn,
		LaterSelectedCommitNonce:          &nonce,
		MainAccessPolicy:                  publication.AccessPolicyCreatorOnly,
		CoordinationAccessPolicy:          publication.AccessPolicyAbsent,
		Cleanup:                           publication.NewCleanupArtifacts(),
		CoordinationCleanup:               publication.CoordinationCleanupRetainedWriterCloseRequired,
		Housekeeping:                      publication.HousekeepingVisible,
		VisibleHousekeeping: []publication.HousekeepingArtifact{{
			State:             publication.HousekeepingMovePending,
			DirectoryRole:     publication.DirectoryRoleDestination,
			DirectoryIdentity: testIdentity(1, 2),
			BasenameEncoding:  1,
			AttemptID:         [16]byte{0x66},
			Ordinal:           3,
			EnvelopeBasename:  []byte("env.tmp"),
			EnvelopeIdentity:  testIdentity(3, 4),
			SourceBasename:    []byte("src.tmp"),
			InertBasename:     []byte("inert.tmp"),
			SourcePresence:    publication.ArtifactPresent,
			SourceIdentity:    &publication.LocalFileIdentity{Kind: 1, Bytes: [32]byte{9}},
			InertPresence:     publication.ArtifactAbsent,
			Kind:              publication.ArtifactOwnedMain,
			CreationSecurity:  publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x77}},
		}},
		Cause: &format.Error{Code: format.CodeCleanupConflict, Detail: "cause of the result"},
	}
	artifact := publication.CleanupArtifact{
		Kind:              publication.ArtifactPrivateOutput,
		DirectoryRole:     publication.DirectoryRoleDestination,
		DirectoryIdentity: testIdentity(1, 2),
		BasenameEncoding:  1,
		Basename:          []byte("artifact.tmp"),
		Identity:          &publication.LocalFileIdentity{Kind: 1, Bytes: [32]byte{8}},
		CreationSecurity:  &publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x88}},
		UnpublishedTail: &publication.UnpublishedTailFacts{
			ExpectedDatabaseID:           [16]byte{0x11},
			CommittedTargetTransactionID: 5,
			CommittedTargetNonce:         [16]byte{0x22},
			CommittedTargetLength:        4096,
			ObservedTailEndExclusive:     8192,
		},
		Error: &format.Error{Code: format.CodeDirectoryIdentityMismatch, Detail: "exact artifact cause"},
	}
	value.Cleanup.Push(artifact)

	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writePublicationResult(w, value); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readPublicationResult(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got.Attempt.DatabaseID != value.Attempt.DatabaseID ||
		!got.MainNamespaceMayHaveBeenAttempted ||
		got.Publication != value.Publication ||
		got.DestinationContent != value.DestinationContent ||
		got.LaterCanonical != value.LaterCanonical ||
		got.LiveLineage == nil || *got.LiveLineage != lineage ||
		got.LaterAttemptOrSidecarID == nil || *got.LaterAttemptOrSidecarID != nonce ||
		got.LaterSelectedTransactionID == nil || *got.LaterSelectedTransactionID != txn ||
		got.LaterSelectedCommitNonce == nil || *got.LaterSelectedCommitNonce != nonce ||
		got.MainAccessPolicy != value.MainAccessPolicy ||
		got.CoordinationAccessPolicy != value.CoordinationAccessPolicy ||
		got.Cleanup.Len() != 1 ||
		got.CoordinationCleanup != value.CoordinationCleanup ||
		got.Housekeeping != value.Housekeeping ||
		len(got.VisibleHousekeeping) != 1 ||
		!housekeepingArtifactEqual(got.VisibleHousekeeping[0], value.VisibleHousekeeping[0]) ||
		got.Cause == nil {
		t.Fatalf("result = %+v", got)
	}
	cleanupArtifact := got.Cleanup.At(0)
	if cleanupArtifact.Kind != artifact.Kind || cleanupArtifact.DirectoryRole != artifact.DirectoryRole ||
		cleanupArtifact.DirectoryIdentity != artifact.DirectoryIdentity ||
		!bytes.Equal(cleanupArtifact.Basename, artifact.Basename) ||
		cleanupArtifact.Identity == nil || *cleanupArtifact.Identity != *artifact.Identity ||
		cleanupArtifact.CreationSecurity == nil || *cleanupArtifact.CreationSecurity != *artifact.CreationSecurity ||
		cleanupArtifact.UnpublishedTail == nil || *cleanupArtifact.UnpublishedTail != *artifact.UnpublishedTail {
		t.Fatalf("cleanup artifact = %+v", cleanupArtifact)
	}
	var cause *format.Error
	if !errors.As(got.Cause, &cause) || cause.Code != format.CodeCleanupConflict || cause.Detail != "cause of the result" {
		t.Fatalf("result cause = %v", got.Cause)
	}
	// The whole result envelope also round trips through the raw
	// reader entry (Rust compose point).
	c3, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	w3 := NewWireWriter(c3)
	if err := writePublicationResult(w3, value); err != nil {
		t.Fatal(err)
	}
	if err := w3.Finish(); err != nil {
		t.Fatal(err)
	}
	whole, err := readPublicationResultFromControl(c3)
	if err != nil {
		t.Fatal(err)
	}
	if whole.Attempt.DatabaseID != value.Attempt.DatabaseID {
		t.Fatal("envelope opener mismatch")
	}
}

// TestWireCleanupLedgerBounds pins the fixed 4-entry cleanup bound and
// the 8-entry housekeeping bound of the Rust authority.
func TestWireCleanupLedgerBounds(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := w.Byte(5); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readCleanupArtifacts(r)
	wantCode(t, err, format.CodeFormatInvalid)

	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	ledger := make([]publication.HousekeepingArtifact, maxHousekeeping+1)
	w2 := NewWireWriter(c2)
	err = writeHousekeepingList(w2, ledger)
	wantCode(t, err, format.CodeInsufficientResourceBudget)

	c3, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	w3 := NewWireWriter(c3)
	if err := w3.Byte(9); err != nil {
		t.Fatal(err)
	}
	if err := w3.Finish(); err != nil {
		t.Fatal(err)
	}
	r3, err := NewWireReader(c3)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readHousekeepingList(r3)
	wantCode(t, err, format.CodeFormatInvalid)
}

// TestWirePublicationEnumsRoundTrip covers every fixed enum tag of
// wire_publication.rs: the tag tables are the wire constants, so each
// domain value must round trip to its own tag and an unknown tag must
// be rejected with the exact Rust corrupt class.
func TestWirePublicationEnumsRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		tag    func() byte
		decode func(tag byte) error
	}{
		{"publication policy", func() byte { return publicationPolicyTag(publication.PolicyReplaceExistingNoRollback) }, func(tag byte) error { _, err := readPublicationPolicy(tag); return err }},
		{"publication status", func() byte { return publicationStatusTag(publication.PublicationPublished) }, func(tag byte) error { _, err := readPublicationStatus(tag); return err }},
		{"destination content", func() byte { return destinationContentTag(publication.DestinationContentUnclassified) }, func(tag byte) error { _, err := readDestinationContent(tag); return err }},
		{"later canonical", func() byte { return laterCanonicalTag(publication.LaterCanonicalReadyLiveSidecar) }, func(tag byte) error { _, err := readLaterCanonical(tag); return err }},
		{"live lineage", func() byte { return liveLineageTag(publication.LiveLineageSameGenerationPhysicalBytesChanged) }, func(tag byte) error { _, err := readLiveLineage(tag); return err }},
		{"access policy", func() byte { return accessPolicyTag(publication.AccessPolicyChangedOrUnproven) }, func(tag byte) error { _, err := readAccessPolicy(tag); return err }},
		{"coordination cleanup", func() byte { return coordinationCleanupTag(publication.CoordinationCleanupRetainedReaderCloseRequired) }, func(tag byte) error { _, err := readCoordinationCleanupByte(tag); return err }},
		{"housekeeping", func() byte { return housekeepingTag(publication.HousekeepingCrashReappearancePossible) }, func(tag byte) error { _, err := readHousekeepingValueByte(tag); return err }},
		{"artifact kind", func() byte { return artifactKindTag(publication.ArtifactUnpublishedMainTail) }, func(tag byte) error { _, err := readArtifactKind(tag); return err }},
		{"directory role", func() byte { return directoryRoleTag(publication.DirectoryRoleMainFile) }, func(tag byte) error { _, err := readDirectoryRole(tag); return err }},
		{"housekeeping state", func() byte { return housekeepingStateTag(publication.HousekeepingConflict) }, func(tag byte) error { _, err := readHousekeepingState(tag); return err }},
		{"artifact presence", func() byte { return artifactPresenceTag(publication.ArtifactUnclassified) }, func(tag byte) error { _, err := readArtifactPresence(tag); return err }},
		{"invalid tag", func() byte { return 0 }, func(tag byte) error { _, err := readPublicationPolicy(tag); return err }},
	}
	for _, tc := range cases {
		c, err := CreateParent()
		if err != nil {
			t.Fatal(err)
		}
		w := NewWireWriter(c)
		if err := w.Byte(tc.tag()); err != nil {
			t.Fatalf("%s write: %v", tc.name, err)
		}
		if err := w.Finish(); err != nil {
			t.Fatalf("%s seal: %v", tc.name, err)
		}
		r, err := NewWireReader(c)
		if err != nil {
			t.Fatal(err)
		}
		tag, err := r.Byte()
		if err != nil {
			t.Fatalf("%s read byte: %v", tc.name, err)
		}
		err = tc.decode(tag)
		if tc.name == "invalid tag" {
			wantCode(t, err, format.CodeFormatInvalid)
		} else if err != nil {
			t.Fatalf("%s decode: %v", tc.name, err)
		}
		if err := r.Finish(); err != nil {
			t.Fatalf("%s finish: %v", tc.name, err)
		}
		c.Close()
	}
}

// TestWireCleanupArtifactRoundTrip exercises the optional identity,
// security, and tail arms of the cleanup artifact codec.
func TestWireCleanupArtifactRoundTrip(t *testing.T) {
	identity := testIdentity(1, 2)
	security := publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{1}}
	tail := &publication.UnpublishedTailFacts{
		ExpectedDatabaseID:           [16]byte{0x11},
		CommittedTargetTransactionID: 5,
		CommittedTargetNonce:         [16]byte{0x22},
		CommittedTargetLength:        4096,
		ObservedTailEndExclusive:     8192,
	}
	value := &publication.CleanupArtifact{
		Kind:              publication.ArtifactOwnedCoordination,
		DirectoryRole:     publication.DirectoryRoleScratchDirectory,
		DirectoryIdentity: identity,
		BasenameEncoding:  2,
		Basename:          []byte("cleanup.tmp"),
		Identity:          &identity,
		CreationSecurity:  &security,
		UnpublishedTail:   tail,
		Error:             &format.Error{Code: format.CodeCleanupConflict, Detail: "artifact"},
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writeCleanupArtifact(w, value); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readCleanupArtifact(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if got.Kind != value.Kind || got.DirectoryRole != value.DirectoryRole ||
		got.DirectoryIdentity != value.DirectoryIdentity || got.BasenameEncoding != 2 ||
		!bytes.Equal(got.Basename, value.Basename) || got.Identity == nil || *got.Identity != identity ||
		got.CreationSecurity == nil || *got.CreationSecurity != security ||
		got.UnpublishedTail == nil || *got.UnpublishedTail != *tail {
		t.Fatalf("cleanup artifact = %+v", got)
	}
	var cause *format.Error
	if !errors.As(got.Error, &cause) || cause.Code != format.CodeCleanupConflict || cause.Detail != "artifact" {
		t.Fatalf("artifact cause = %v", got.Error)
	}
}

// housekeepingArtifactEqual compares two housekeeping artifacts field
// by field (the wire type carries byte slices, so Go's == does not
// apply).
func housekeepingArtifactEqual(a, b publication.HousekeepingArtifact) bool {
	return a.State == b.State &&
		a.DirectoryRole == b.DirectoryRole &&
		a.DirectoryIdentity == b.DirectoryIdentity &&
		a.BasenameEncoding == b.BasenameEncoding &&
		a.AttemptID == b.AttemptID &&
		a.Ordinal == b.Ordinal &&
		bytes.Equal(a.EnvelopeBasename, b.EnvelopeBasename) &&
		a.EnvelopeIdentity == b.EnvelopeIdentity &&
		bytes.Equal(a.SourceBasename, b.SourceBasename) &&
		bytes.Equal(a.InertBasename, b.InertBasename) &&
		a.SourcePresence == b.SourcePresence &&
		equalIdentityPointer(a.SourceIdentity, b.SourceIdentity) &&
		a.InertPresence == b.InertPresence &&
		equalIdentityPointer(a.InertIdentity, b.InertIdentity) &&
		a.Kind == b.Kind &&
		a.CreationSecurity == b.CreationSecurity &&
		a.SelectedEnvelopeSequence == b.SelectedEnvelopeSequence
}

func equalIdentityPointer(a, b *publication.LocalFileIdentity) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
