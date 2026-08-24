//go:build linux && amd64

// Cleanup-mode wire and arm tests (Rust worker/cleanup.rs + wire_
// cleanup.rs): the publication seam's three discard arms run against
// the real secured-attempt fixture, the CleanupRecoveryAttempt opcode
// runs against the real worker binary (the discard facts travel the
// wire and the artifact disappears), the parent DiscardRecoveryAttempt
// arm runs against the real binary and the in-process double
// (guard-pending Conflict and the mapped-fault class), the scratch
// checkpoint reports the recorded deferral honestly, and a mismatched
// build identity refuses the handshake exactly like the Rust
// verify_request (control.rs verify_request:214-223).

package worker

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// cleanupAttemptFixture builds one real secured output attempt inside
// directory for the destination main name "out.v4" and returns the
// destination path, the exact portable attempt facts, and the private
// artifact path. The fixture is the worker-test peer of the
// publication package's testSecuredAttempt: the private name derives
// from the attempt id (private_name.go), the file is secured with the
// creator-only policy (security.SecureCreatorOnly), and the facts
// carry the exact directory and file identities and the captured
// commitment, so the worker-side resume machine verifies every proof.
func cleanupAttemptFixture(t *testing.T, directory string) (string, *publication.PrivateOutputAttempt, string) {
	t.Helper()
	main := "out.v4"
	var attemptID [16]byte
	copy(attemptID[:], []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	name := ".iprange-publish-" + hex.EncodeToString(attemptID[:]) + ".tmp"
	privatePath := filepath.Join(directory, name)
	file, err := os.OpenFile(privatePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("fixture create attempt: %v", err)
	}
	defer file.Close()
	profile, err := security.Capture()
	if err != nil {
		t.Fatalf("fixture security capture: %v", err)
	}
	if err := security.SecureCreatorOnly(file, profile); err != nil {
		t.Fatalf("fixture secure attempt: %v", err)
	}
	directoryHandle, err := live.OpenDirectory(directory)
	if err != nil {
		t.Fatalf("fixture open directory: %v", err)
	}
	defer directoryHandle.Close()
	directoryIdentity := directoryHandle.Identity()
	fileIdentity, err := live.RegularIdentity(file, directoryIdentity)
	if err != nil {
		t.Fatalf("fixture file identity: %v", err)
	}
	dirDevice, dirInode := live.IdentityDeviceInode(&directoryIdentity)
	fileDevice, fileInode := live.IdentityDeviceInode(&fileIdentity)
	facts := &publication.PrivateOutputAttempt{
		PublicationAttemptID: attemptID,
		DirectoryIdentity:    publication.LocalFileIdentityFromDeviceInode(dirDevice, dirInode),
		BasenameEncoding:     1,
		Basename:             []byte(name),
		Identity:             publication.LocalFileIdentityFromDeviceInode(fileDevice, fileInode),
		IdentityPresent:      true,
		CreationSecurity: publication.CreationSecurity{
			Kind:       1,
			Commitment: profile.Commitment(),
		},
	}
	return filepath.Join(directory, main), facts, privatePath
}

// equalPrivateOutput compares two portable attempt facts field-wise
// (the wire shape carries a slice, so the structs are not comparable).
func equalPrivateOutput(a, b *publication.PrivateOutputAttempt) bool {
	return a.PublicationAttemptID == b.PublicationAttemptID &&
		a.DirectoryIdentity == b.DirectoryIdentity &&
		a.BasenameEncoding == b.BasenameEncoding &&
		bytes.Equal(a.Basename, b.Basename) &&
		a.Identity == b.Identity &&
		a.IdentityPresent == b.IdentityPresent &&
		a.CreationSecurity == b.CreationSecurity
}

// TestCleanupCheckpointDeferral pins the honest scratch stance of this
// tree (SOW-0025 chunk 4-10: authorized scratch and external sort stay
// a recorded follow-up, not ported): a nil checkpoint is the clean nil
// cleanup, and a present checkpoint reports the deferral as one
// Conflict residue per entry with the exact checkpoint basenames.
func TestCleanupCheckpointDeferral(t *testing.T) {
	if cleanup := CleanupCheckpoint(nil, nil); cleanup != nil {
		t.Fatalf("nil checkpoint cleanup = %+v, want nil", cleanup)
	}
	checkpoint := &ScratchCheckpoint{
		AttemptID:         [16]byte{0x10},
		DirectoryIdentity: publication.LocalFileIdentityFromDeviceInode(1, 2),
		CreationSecurity:  publication.CreationSecurity{Kind: 1, Commitment: [32]byte{0x20}},
		Entries: []ScratchCheckpointEntry{
			{Ordinal: 0, Identity: publication.LocalFileIdentityFromDeviceInode(3, 4)},
			{Ordinal: 1, Identity: publication.LocalFileIdentityFromDeviceInode(5, 6)},
		},
	}
	directory := "authorized-scratch"
	cleanup := CleanupCheckpoint(&directory, checkpoint)
	if cleanup == nil {
		t.Fatal("present checkpoint cleanup is nil")
	}
	if cleanup.AttemptID != checkpoint.AttemptID || cleanup.DirectoryIdentity != checkpoint.DirectoryIdentity ||
		cleanup.CreationSecurityKind != checkpoint.CreationSecurity.Kind || cleanup.CreationSecurityCommitment != checkpoint.CreationSecurity.Commitment {
		t.Fatalf("scratch cleanup identity facts = %+v", cleanup)
	}
	if cleanup.Housekeeping != publication.HousekeepingNone || len(cleanup.VisibleHousekeeping) != 0 {
		t.Fatalf("cleanup housekeeping = %+v / %+v, want none", cleanup.Housekeeping, cleanup.VisibleHousekeeping)
	}
	if len(cleanup.Residues) != len(checkpoint.Entries) {
		t.Fatalf("residues = %d, want %d", len(cleanup.Residues), len(checkpoint.Entries))
	}
	for index, residue := range cleanup.Residues {
		entry := checkpoint.Entries[index]
		if residue.Ordinal != entry.Ordinal || residue.Identity != entry.Identity ||
			residue.DirectoryIdentity != checkpoint.DirectoryIdentity ||
			residue.CreationSecurityKind != checkpoint.CreationSecurity.Kind ||
			residue.CreationSecurityCommitment != checkpoint.CreationSecurity.Commitment {
			t.Fatalf("residue %d = %+v, want the checkpoint entry facts", index, residue)
		}
		if !bytes.Equal(residue.Basename, checkpointBasename(checkpoint.AttemptID, entry.Ordinal)) {
			t.Fatalf("residue %d basename = %q, want the checkpoint basename", index, residue.Basename)
		}
		if residue.Problem.Code != format.CodeConflict || residue.Problem.Detail != "worker scratch cleanup machine is not ported" {
			t.Fatalf("residue %d problem = %+v, want the recorded deferral Conflict", index, residue.Problem)
		}
	}
}

// TestCleanupOpcodeRealBinary runs one CleanupRecoveryAttempt session
// against the real worker binary: a real secured attempt is discarded
// through the publication seam inside the worker, the discard facts
// travel the cleanup-wire result, and the private artifact is gone.
func TestCleanupOpcodeRealBinary(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	directory := t.TempDir()
	destination, facts, privatePath := cleanupAttemptFixture(t, directory)
	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer control.Close()
	control.SetOpcode(OpcodeCleanupRecoveryAttempt)
	if err := WriteCleanupRequest(control, destination, facts, nil, nil); err != nil {
		t.Fatal("write cleanup request:", err)
	}
	child, err := SpawnWorker(control)
	if err != nil {
		t.Fatal("spawn worker:", err)
	}
	if err := Handshake(child, control); err != nil {
		child.Abort()
		t.Fatal("handshake:", err)
	}
	control.SetState(stateRunning)
	driven, driveErr := DriveLoop(child, control, nil, "SDK worker emitted an unexpected event",
		func(uint32, *Process, *Control) (bool, error) { return false, nil })
	if driveErr != nil {
		t.Fatal("drive:", driveErr)
	}
	if !driven.Complete || driven.GuardPending {
		t.Fatalf("drive = %+v, want the clean complete terminal", driven)
	}
	discarded, scratch, err := ReadCleanupResult(control)
	if err != nil {
		t.Fatal("read cleanup result:", err)
	}
	if !discarded.Clean() {
		t.Fatalf("discarded = %+v, want a clean discard", discarded)
	}
	if !equalPrivateOutput(&discarded.Output, facts) {
		t.Fatalf("discarded output = %+v, want %+v", discarded.Output, facts)
	}
	if scratch != nil {
		t.Fatalf("scratch = %+v, want nil for a checkpoint-less request", scratch)
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private attempt %s still exists after the worker discard", privatePath)
	}
}

// TestDiscardRecoveryAttemptRealBinary runs the parent cleanup arm
// against the real worker binary: the complete class returns the
// decoded discard facts and no scratch cleanup.
func TestDiscardRecoveryAttemptRealBinary(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	directory := t.TempDir()
	destination, facts, privatePath := cleanupAttemptFixture(t, directory)
	discarded, scratch, err := DiscardRecoveryAttempt(destination, facts, nil, nil)
	if err != nil {
		t.Fatal("discard recovery attempt:", err)
	}
	if discarded == nil || !discarded.Clean() {
		t.Fatalf("discarded = %+v, want a clean discard", discarded)
	}
	if !equalPrivateOutput(&discarded.Output, facts) {
		t.Fatalf("discarded output = %+v, want %+v", discarded.Output, facts)
	}
	if scratch != nil {
		t.Fatalf("scratch = %+v, want nil", scratch)
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private attempt %s still exists after the discard arm", privatePath)
	}
}

// TestDiscardRecoveryAttemptRealBinaryScratch proves the recorded
// scratch deferral over the real worker: a request carrying a scratch
// checkpoint returns one Conflict residue per checkpoint entry, never
// a fabricated removal.
func TestDiscardRecoveryAttemptRealBinaryScratch(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	directory := t.TempDir()
	destination, facts, _ := cleanupAttemptFixture(t, directory)
	checkpoint := &ScratchCheckpoint{
		AttemptID:         [16]byte{0x30},
		DirectoryIdentity: publication.LocalFileIdentityFromDeviceInode(7, 8),
		CreationSecurity:  publication.CreationSecurity{Kind: 1, Commitment: [32]byte{0x40}},
		Entries: []ScratchCheckpointEntry{
			{Ordinal: 0, Identity: publication.LocalFileIdentityFromDeviceInode(9, 10)},
			{Ordinal: 1, Identity: publication.LocalFileIdentityFromDeviceInode(11, 12)},
		},
	}
	scratchDirectory := filepath.Join(directory, "scratch")
	discarded, scratch, err := DiscardRecoveryAttempt(destination, facts, &scratchDirectory, checkpoint)
	if err != nil {
		t.Fatal("discard recovery attempt:", err)
	}
	if discarded == nil || !discarded.Clean() {
		t.Fatalf("discarded = %+v, want a clean discard", discarded)
	}
	if scratch == nil {
		t.Fatal("scratch cleanup is nil for a checkpointed request")
	}
	if len(scratch.Residues) != len(checkpoint.Entries) {
		t.Fatalf("residues = %d, want %d", len(scratch.Residues), len(checkpoint.Entries))
	}
	for index, residue := range scratch.Residues {
		if residue.Problem.Code != format.CodeConflict || residue.Problem.Detail != "worker scratch cleanup machine is not ported" {
			t.Fatalf("residue %d problem = %+v, want the recorded deferral Conflict", index, residue.Problem)
		}
	}
}

// TestDiscardRecoveryAttemptGuardPending runs the cleanup arm against
// the in-process double: a complete terminal that retains unexpected
// cleanup authority is the verbatim Rust Conflict (cleanup.rs:73-76).
func TestDiscardRecoveryAttemptGuardPending(t *testing.T) {
	armDouble(t, "complete_guard")
	directory := t.TempDir()
	destination, facts, _ := cleanupAttemptFixture(t, directory)
	_, _, err := DiscardRecoveryAttempt(destination, facts, nil, nil)
	wantConflictDetail(t, err, "isolated recovery cleanup retained unexpected authority")
}

// TestDiscardRecoveryAttemptFault runs the cleanup arm against the
// in-process double: a mapped fault folds through faultProblem with
// the role detail (Rust cleanup.rs:78-79 over recovery.rs:525-534).
func TestDiscardRecoveryAttemptFault(t *testing.T) {
	armDouble(t, "fault")
	directory := t.TempDir()
	destination, facts, _ := cleanupAttemptFixture(t, directory)
	_, _, err := DiscardRecoveryAttempt(destination, facts, nil, nil)
	var e *format.Error
	if !errors.As(err, &e) || e.Code != format.CodeIO {
		t.Fatalf("fault class = %v, want the fixed Io publication problem", err)
	}
	if e.Detail != "recovery source mapping faulted" {
		t.Fatalf("fault detail = %q, want the source-role detail", e.Detail)
	}
}

// TestHandshakeBuildIDMismatch spawns the real worker against a
// control whose 64 build-id bytes were overwritten with a different
// valid-length value: the worker's verify_request refuses
// (control.rs verify_request:214-223) and exits before WorkerReady, so
// the parent handshake reports the exact protocol Conflict.
func TestHandshakeBuildIDMismatch(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer control.Close()
	mismatched := bytes.Repeat([]byte{'x'}, buildLen)
	copy(control.data[offBuildID:offBuildID+buildLen], mismatched)
	child, err := SpawnWorker(control)
	if err != nil {
		t.Fatal("spawn worker:", err)
	}
	err = Handshake(child, control)
	child.Abort()
	wantConflictDetail(t, err, "SDK worker version or protocol does not match")
}

// TestHandshakeRealBinarySanity guards the mismatch patch helper: an
// unpatched control with the real binary still completes the version
// handshake.
func TestHandshakeRealBinarySanity(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	control, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer control.Close()
	child, err := SpawnWorker(control)
	if err != nil {
		t.Fatal("spawn worker:", err)
	}
	if err := Handshake(child, control); err != nil {
		child.Abort()
		t.Fatal("handshake:", err)
	}
	child.Abort()
}
