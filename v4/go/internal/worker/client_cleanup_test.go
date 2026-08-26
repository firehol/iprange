//go:build linux && amd64

// Cleanup-mode wire and arm tests (Rust worker/cleanup.rs + wire_
// cleanup.rs): the publication seam's three discard arms run against
// the real secured-attempt fixture, the CleanupRecoveryAttempt opcode
// runs against the real worker binary (the discard facts travel the
// wire and the artifact disappears), the parent DiscardRecoveryAttempt
// arm runs against the real binary and the in-process double
// (guard-pending Conflict and the mapped-fault class), the scratch
// checkpoint runs the real checkpointed removal machine (exact
// removal and the changed-link-count residue), and a mismatched build
// identity refuses the handshake exactly like the Rust
// verify_request (control.rs verify_request:214-223).

package worker

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
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

// TestCleanupCheckpointNilIsClean pins the nil-checkpoint arm of the
// worker scratch cleanup (Rust client/recovery.rs cleanup_checkpoint:
// a nil checkpoint is the clean nil cleanup).
func TestCleanupCheckpointNilIsClean(t *testing.T) {
	if cleanup := CleanupCheckpoint(nil, nil); cleanup != nil {
		t.Fatalf("nil checkpoint cleanup = %+v, want nil", cleanup)
	}
}

// TestCleanupCheckpointWithoutDirectoryReportsConflict pin the
// missing-directory arm (Rust cleanup_checkpoint: the Conflict class
// with the verbatim detail and one residue per entry).
func TestCleanupCheckpointWithoutDirectoryReportsConflict(t *testing.T) {
	checkpoint := &ScratchCheckpoint{
		AttemptID:         [16]byte{0x10},
		DirectoryIdentity: publication.LocalFileIdentityFromDeviceInode(1, 2),
		CreationSecurity:  publication.CreationSecurity{Kind: 1, Commitment: [32]byte{0x20}},
		Entries: []ScratchCheckpointEntry{
			{Ordinal: 0, Identity: publication.LocalFileIdentityFromDeviceInode(3, 4)},
			{Ordinal: 1, Identity: publication.LocalFileIdentityFromDeviceInode(5, 6)},
		},
	}
	cleanup := CleanupCheckpoint(nil, checkpoint)
	if cleanup == nil {
		t.Fatal("present checkpoint cleanup is nil")
	}
	if cleanup.AttemptID != checkpoint.AttemptID || cleanup.DirectoryIdentity != checkpoint.DirectoryIdentity ||
		cleanup.CreationSecurityKind != checkpoint.CreationSecurity.Kind || cleanup.CreationSecurityCommitment != checkpoint.CreationSecurity.Commitment {
		t.Fatalf("scratch cleanup identity facts = %+v", cleanup)
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
		if residue.Problem.Code != format.CodeConflict || residue.Problem.Detail != "checkpointed recovery scratch cleanup failed" {
			t.Fatalf("residue %d problem = %+v, want the missing-directory Conflict", index, residue.Problem)
		}
	}
}

// TestCleanupCheckpointRemovesHeaderlessExactScratch runs the real
// checkpointed removal (Rust client_tests
// checkpoint_cleanup_removes_headerless_exact_scratch): the
// checkpointed machine does not re-read the ownership header, so a
// bare 0600 exact-name file is removed and the cleanup is clean.
func TestCleanupCheckpointRemovesHeaderlessExactScratch(t *testing.T) {
	directory := t.TempDir()
	checkpoint := createScratchCheckpointFixture(t, directory, false)
	cleanup := CleanupCheckpoint(&directory, checkpoint)
	if cleanup == nil {
		t.Fatal("present checkpoint cleanup is nil")
	}
	if !cleanup.Clean() {
		t.Fatalf("cleanup not clean: %+v", cleanup.Residues)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal("read directory:", err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory retained %d entries", len(entries))
	}
}

// TestCleanupCheckpointReportsChangedLinkCountWithoutUnlinking runs
// the changed-link-count arm (Rust client_tests
// checkpoint_cleanup_reports_changed_link_count_without_unlinking):
// an aliased artifact reports one residue and the alias survives.
func TestCleanupCheckpointReportsChangedLinkCountWithoutUnlinking(t *testing.T) {
	directory := t.TempDir()
	checkpoint := createScratchCheckpointFixture(t, directory, true)
	cleanup := CleanupCheckpoint(&directory, checkpoint)
	if cleanup == nil {
		t.Fatal("present checkpoint cleanup is nil")
	}
	if cleanup.Clean() {
		t.Fatal("changed-link-count cleanup reported clean")
	}
	if len(cleanup.Residues) != 1 {
		t.Fatalf("residues = %d, want 1", len(cleanup.Residues))
	}
	if cleanup.Residues[0].Problem.Code != format.CodeCleanupConflict {
		t.Fatalf("residue problem = %+v, want cleanup conflict", cleanup.Residues[0].Problem)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal("read directory:", err)
	}
	if len(entries) != 2 {
		t.Fatalf("directory retained %d entries, want 2", len(entries))
	}
}

// createScratchCheckpointFixture builds one checkpoint over one real
// exact-name scratch file (Rust client_tests create_checkpoint: a
// fresh 0600 file named by the checkpoint basename, optionally
// aliased, with the directory and artifact identities captured live).
func createScratchCheckpointFixture(t *testing.T, directory string, alias bool) *ScratchCheckpoint {
	t.Helper()
	attemptID := [16]byte{0x32}
	ordinal := uint32(9)
	name := string(checkpointBasename(attemptID, ordinal))
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("create scratch file:", err)
	}
	if alias {
		if err := os.Link(path, filepath.Join(directory, "alias")); err != nil {
			file.Close()
			t.Fatal("link alias:", err)
		}
	}
	st, err := file.Stat()
	if err != nil {
		file.Close()
		t.Fatal("stat scratch file:", err)
	}
	file.Close()
	dirStat, err := os.Stat(directory)
	if err != nil {
		t.Fatal("stat directory:", err)
	}
	sys, ok := dirStat.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("directory stat is not a unix stat")
	}
	artifactSys := st.Sys().(*syscall.Stat_t)
	return &ScratchCheckpoint{
		AttemptID:         attemptID,
		DirectoryIdentity: publication.LocalFileIdentityFromDeviceInode(uint64(sys.Dev), uint64(sys.Ino)),
		CreationSecurity:  publication.CreationSecurity{Kind: 1, Commitment: [32]byte{0x6b}},
		Entries: []ScratchCheckpointEntry{
			{Ordinal: ordinal, Identity: publication.LocalFileIdentityFromDeviceInode(uint64(artifactSys.Dev), uint64(artifactSys.Ino))},
		},
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
	discarded, scratch, err := discardRecoveryAttempt(destination, facts, nil, nil)
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

// TestDiscardRecoveryAttemptRealBinaryScratch runs the real
// checkpointed-scratch removal over the real worker: a request
// carrying a scratch checkpoint and a real (empty) scratch directory
// returns a clean scratch cleanup, proving the checkpointed removal
// machine inside the worker session (Rust worker/client/recovery.rs
// cleanup_checkpoint over remove_checkpointed_scratch).
func TestDiscardRecoveryAttemptRealBinaryScratch(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	directory := t.TempDir()
	destination, facts, _ := cleanupAttemptFixture(t, directory)
	scratchDirectory := filepath.Join(directory, "scratch")
	if err := os.Mkdir(scratchDirectory, 0o700); err != nil {
		t.Fatal("create scratch directory:", err)
	}
	checkpoint := &ScratchCheckpoint{
		AttemptID:         [16]byte{0x30},
		DirectoryIdentity: scratchDirectoryIdentity(t, scratchDirectory),
		CreationSecurity:  publication.CreationSecurity{Kind: 1, Commitment: [32]byte{0x40}},
		Entries: []ScratchCheckpointEntry{
			{Ordinal: 0, Identity: publication.LocalFileIdentityFromDeviceInode(9, 10)},
			{Ordinal: 1, Identity: publication.LocalFileIdentityFromDeviceInode(11, 12)},
		},
	}
	discarded, scratch, err := discardRecoveryAttempt(destination, facts, &scratchDirectory, checkpoint)
	if err != nil {
		t.Fatal("discard recovery attempt:", err)
	}
	if discarded == nil || !discarded.Clean() {
		t.Fatalf("discarded = %+v, want a clean discard", discarded)
	}
	if scratch == nil {
		t.Fatal("scratch cleanup is nil for a checkpointed request")
	}
	if !scratch.Clean() {
		t.Fatalf("scratch cleanup not clean: %+v", scratch.Residues)
	}
}

// scratchDirectoryIdentity captures the portable identity of one
// directory for the wire scratch checkpoint fixture.
func scratchDirectoryIdentity(t *testing.T, path string) publication.LocalFileIdentity {
	t.Helper()
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal("stat scratch directory:", err)
	}
	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("directory stat is not a unix stat")
	}
	return publication.LocalFileIdentityFromDeviceInode(uint64(sys.Dev), uint64(sys.Ino))
}

// TestDiscardRecoveryAttemptGuardPending runs the cleanup arm against
// the in-process double: a complete terminal that retains unexpected
// cleanup authority is the verbatim Rust Conflict (cleanup.rs:73-76).
func TestDiscardRecoveryAttemptGuardPending(t *testing.T) {
	armDouble(t, "complete_guard")
	directory := t.TempDir()
	destination, facts, _ := cleanupAttemptFixture(t, directory)
	_, _, err := discardRecoveryAttempt(destination, facts, nil, nil)
	wantConflictDetail(t, err, "isolated recovery cleanup retained unexpected authority")
}

// TestDiscardRecoveryAttemptFault runs the cleanup arm against the
// in-process double: a mapped fault folds through faultProblem with
// the role detail (Rust cleanup.rs:78-79 over recovery.rs:525-534).
func TestDiscardRecoveryAttemptFault(t *testing.T) {
	armDouble(t, "fault")
	directory := t.TempDir()
	destination, facts, _ := cleanupAttemptFixture(t, directory)
	_, _, err := discardRecoveryAttempt(destination, facts, nil, nil)
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

// TestHandshakeHeaderMismatches extends the build-id handshake refusal
// to every remaining header field verify_request checks (Rust
// control.rs:214-223): a patched magic, protocol, state (away from
// Request), or zeroed parent pid makes the real worker exit before
// WorkerReady, so the parent handshake reports the exact protocol
// Conflict class (client.rs handshake parity).
func TestHandshakeHeaderMismatches(t *testing.T) {
	binary := buildRealWorker(t)
	workerCandidatesHook = func() ([]string, error) { return []string{binary}, nil }
	t.Cleanup(func() { workerCandidatesHook = nil })

	variants := []struct {
		name  string
		patch func(data []byte)
	}{
		{"magic", func(data []byte) { copy(data[offMagic:offMagic+8], []byte("XXXXXXXX")) }},
		{"protocol", func(data []byte) { format.PutU32(data[offProtocol:offProtocol+4], 2) }},
		{"state", func(data []byte) { format.PutU32(data[offState:offState+4], stateComplete) }},
		{"parent-pid", func(data []byte) { format.PutU32(data[offParentPID:offParentPID+4], 0) }},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			control, err := CreateParent()
			if err != nil {
				t.Fatal("create parent:", err)
			}
			defer control.Close()
			variant.patch(control.data)
			child, err := SpawnWorker(control)
			if err != nil {
				t.Fatal("spawn worker:", err)
			}
			err = Handshake(child, control)
			child.Abort()
			wantConflictDetail(t, err, "SDK worker version or protocol does not match")
		})
	}
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
