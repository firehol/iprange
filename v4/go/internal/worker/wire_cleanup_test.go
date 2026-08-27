//go:build linux || darwin || freebsd || windows

// Cleanup-mode wire unit tests (Rust worker/wire_cleanup.rs): the
// cleanup request and result envelopes, the checkpoint and scratch
// codecs with their exact consistency cross-checks, and the checkpoint
// basename vector (Rust artifact_name.rs scratch_name).

package worker

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

func testScratchCheckpoint() *ScratchCheckpoint {
	security := publication.CreationSecurity{Kind: creationSecurityKind, Commitment: [32]byte{0x5a}}
	return &ScratchCheckpoint{
		AttemptID:         [16]byte{0x41},
		DirectoryIdentity: testIdentity(11, 12),
		CreationSecurity:  security,
		Entries: []ScratchCheckpointEntry{
			{Ordinal: 7, Identity: testIdentity(21, 22)},
			{Ordinal: 8, Identity: testIdentity(31, 32)},
		},
	}
}

func TestCleanupRequestRoundTrip(t *testing.T) {
	scratch := testScratchCheckpoint()
	directory := "/tmp/scratch-dir"
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteCleanupRequest(c, "/tmp/output.v4", testOutputAttempt(), &directory, scratch); err != nil {
		t.Fatal("write request:", err)
	}
	request, err := ReadCleanupRequest(c)
	if err != nil {
		t.Fatal("read request:", err)
	}
	if request.DestinationPath != "/tmp/output.v4" || request.ScratchDirectory == nil ||
		*request.ScratchDirectory != directory || request.Scratch == nil ||
		request.Scratch.AttemptID != [16]byte{0x41} || len(request.Scratch.Entries) != 2 {
		t.Fatalf("request = %+v", request)
	}
	// The scratch-free arm round trips too.
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if err := WriteCleanupRequest(c2, "/tmp/output.v4", testOutputAttempt(), nil, nil); err != nil {
		t.Fatal(err)
	}
	request2, err := ReadCleanupRequest(c2)
	if err != nil {
		t.Fatal(err)
	}
	if request2.ScratchDirectory != nil || request2.Scratch != nil {
		t.Fatalf("scratch-free request = %+v", request2)
	}
	// A scratch checkpoint without its directory is corruption (Rust
	// wire_cleanup::read_request disagreement check).
	c3, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	w := NewWireWriter(c3)
	if err := w.Path("/tmp/output.v4"); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateOutput(w, testOutputAttempt()); err != nil {
		t.Fatal(err)
	}
	if err := w.OptionalPath(nil); err != nil {
		t.Fatal(err)
	}
	if err := writeScratchCheckpoint(w, scratch); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	_, err = ReadCleanupRequest(c3)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestCleanupResultRoundTrip(t *testing.T) {
	artifact := publication.CleanupArtifact{
		Kind:              publication.ArtifactPrivateOutput,
		DirectoryRole:     publication.DirectoryRoleDestination,
		DirectoryIdentity: testIdentity(1, 2),
		Basename:          []byte("residue.tmp"),
		Error:             &format.Error{Code: format.CodeCleanupConflict, Detail: "unproved removal"},
	}
	discarded := &EarlyDiscard{
		Output:              *testOutputAttempt(),
		Artifact:            &artifact,
		Housekeeping:        publication.HousekeepingCrashReappearancePossible,
		VisibleHousekeeping: nil,
	}
	if discarded.Clean() {
		t.Fatal("discard with artifact reports clean")
	}
	scratch := &ScratchCleanup{
		AttemptID:                  [16]byte{0x41},
		DirectoryIdentity:          testIdentity(11, 12),
		CreationSecurityKind:       creationSecurityKind,
		CreationSecurityCommitment: [32]byte{0x5a},
		Residues: []ScratchResidue{{
			Ordinal:                    7,
			DirectoryIdentity:          testIdentity(11, 12),
			Basename:                   checkpointBasename([16]byte{0x41}, 7),
			Identity:                   testIdentity(21, 22),
			CreationSecurityKind:       creationSecurityKind,
			CreationSecurityCommitment: [32]byte{0x5a},
			Problem: ScratchProblem{
				Code:   format.CodeCleanupConflict,
				OSCode: nil,
				Detail: "scratch residue",
			},
		}},
		Housekeeping:        publication.HousekeepingNone,
		VisibleHousekeeping: nil,
	}
	if scratch.Clean() {
		t.Fatal("scratch with residue reports clean")
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteCleanupResult(c, discarded, scratch); err != nil {
		t.Fatal("write result:", err)
	}
	gotDiscarded, gotScratch, err := ReadCleanupResult(c)
	if err != nil {
		t.Fatal("read result:", err)
	}
	if gotDiscarded.Artifact == nil || gotDiscarded.Output.PublicationAttemptID != testOutputAttempt().PublicationAttemptID ||
		gotDiscarded.Housekeeping != publication.HousekeepingCrashReappearancePossible {
		t.Fatalf("discarded = %+v", gotDiscarded)
	}
	if gotScratch == nil || gotScratch.AttemptID != [16]byte{0x41} || len(gotScratch.Residues) != 1 ||
		gotScratch.Residues[0].Ordinal != 7 || gotScratch.Residues[0].Problem.Detail != "scratch residue" {
		t.Fatalf("scratch = %+v", gotScratch)
	}
	// The clean arms round trip too.
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	cleanDiscard := &EarlyDiscard{Output: *testOutputAttempt()}
	if !cleanDiscard.Clean() {
		t.Fatal("clean discard reports residue")
	}
	if err := WriteCleanupResult(c2, cleanDiscard, nil); err != nil {
		t.Fatal(err)
	}
	got2, gotScratch2, err := ReadCleanupResult(c2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Artifact != nil || got2.Housekeeping != publication.HousekeepingNone || gotScratch2 != nil {
		t.Fatalf("clean result = %+v %v", got2, gotScratch2)
	}
}

func TestCheckpointBasenameVector(t *testing.T) {
	// Rust artifact_name.rs scratch_name for attempt 0x32 repeated and
	// ordinal 9: ".iprange-scratch-" + "32"*16 + "-" + "00000009" +
	// ".tmp", exactly 62 bytes.
	var attempt [16]byte
	for index := range attempt {
		attempt[index] = 0x32
	}
	got := checkpointBasename(attempt, 9)
	want := ".iprange-scratch-32323232323232323232323232323232-00000009.tmp"
	if string(got) != want {
		t.Fatalf("basename = %q, want %q", got, want)
	}
	if len(got) != scratchNameLength {
		t.Fatalf("basename length = %d, want %d", len(got), scratchNameLength)
	}
	// The ordinal is zero-padded lowercase hex (Rust write_ordinal).
	got = checkpointBasename([16]byte{}, 0xabcdef01)
	want = ".iprange-scratch-00000000000000000000000000000000-abcdef01.tmp"
	if string(got) != want {
		t.Fatalf("basename = %q, want %q", got, want)
	}
}

func TestScratchCheckpointWireRejections(t *testing.T) {
	checkpoint := testScratchCheckpoint()
	// A zero attempt identity is corruption on read.
	bad := *checkpoint
	bad.AttemptID = [16]byte{}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writeScratchCheckpoint(w, &bad); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readScratchCheckpoint(r)
	wantCode(t, err, format.CodeFormatInvalid)
	// An invalid entry identity kind is corruption.
	bad = *testScratchCheckpoint()
	bad.Entries = []ScratchCheckpointEntry{{Ordinal: 1, Identity: publication.LocalFileIdentity{Kind: 9, Bytes: [32]byte{1}}}}
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	w2 := NewWireWriter(c2)
	if err := writeScratchCheckpoint(w2, &bad); err != nil {
		t.Fatal(err)
	}
	if err := w2.Finish(); err != nil {
		t.Fatal(err)
	}
	r2, err := NewWireReader(c2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readScratchCheckpoint(r2)
	wantCode(t, err, format.CodeFormatInvalid)
	// Duplicate ordinals are corruption (Rust duplicate-authority
	// check).
	dup := *testScratchCheckpoint()
	dup.Entries = []ScratchCheckpointEntry{
		{Ordinal: 1, Identity: testIdentity(21, 22)},
		{Ordinal: 1, Identity: testIdentity(31, 32)},
	}
	c3, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	w3 := NewWireWriter(c3)
	if err := writeScratchCheckpoint(w3, &dup); err != nil {
		t.Fatal(err)
	}
	if err := w3.Finish(); err != nil {
		t.Fatal(err)
	}
	r3, err := NewWireReader(c3)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readScratchCheckpoint(r3)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestScratchCleanupWireRejections(t *testing.T) {
	cleanup := &ScratchCleanup{
		AttemptID:                  [16]byte{0x41},
		DirectoryIdentity:          testIdentity(11, 12),
		CreationSecurityKind:       creationSecurityKind,
		CreationSecurityCommitment: [32]byte{0x5a},
		Residues: []ScratchResidue{{
			Ordinal:                    7,
			DirectoryIdentity:          testIdentity(11, 12),
			Basename:                   checkpointBasename([16]byte{0x41}, 7),
			Identity:                   testIdentity(21, 22),
			CreationSecurityKind:       creationSecurityKind,
			CreationSecurityCommitment: [32]byte{0x5a},
		}},
	}
	// A residue whose basename is not the exact checkpoint name fails
	// the authority check (Rust basename != expected_basename).
	bad := *cleanup
	bad.Residues = []ScratchResidue{cleanup.Residues[0]}
	bad.Residues[0].Basename = []byte("wrong.tmp")
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := NewWireWriter(c)
	if err := writeScratchCleanup(w, &bad); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readScratchCleanup(r)
	wantCode(t, err, format.CodeFormatInvalid)
	// A residue directory different from the recorded directory fails.
	bad = *cleanup
	bad.Residues = []ScratchResidue{cleanup.Residues[0]}
	bad.Residues[0].DirectoryIdentity = testIdentity(99, 99)
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	w2 := NewWireWriter(c2)
	if err := writeScratchCleanup(w2, &bad); err != nil {
		t.Fatal(err)
	}
	if err := w2.Finish(); err != nil {
		t.Fatal(err)
	}
	r2, err := NewWireReader(c2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readScratchCleanup(r2)
	wantCode(t, err, format.CodeFormatInvalid)
}
