//go:build linux && amd64

// Cleanup-mode wire codecs (Rust worker/wire_cleanup.rs): the
// cleanup-guard request (destination path, private output attempt,
// optional scratch directory and checkpoint) and the discard result
// (the early-discard facts, the optional scratch cleanup). Every field
// order and every consistency cross-check mirrors the Rust authority;
// the checkpoint and scratch encodings repeat the control-page
// validation exactly as the Rust wire layer does.

package worker

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// CleanupRequest is the decoded cleanup request (Rust
// wire_cleanup.rs Request).
type CleanupRequest struct {
	DestinationPath  string
	Output           publication.PrivateOutputAttempt
	ScratchDirectory *string
	Scratch          *ScratchCheckpoint
}

// WriteCleanupRequest writes one cleanup request (Rust
// wire_cleanup::write_request: destination path, the private output
// attempt, the optional scratch directory, and the optional scratch
// checkpoint).
func WriteCleanupRequest(control *Control, destinationPath string, output *publication.PrivateOutputAttempt, scratchDirectory *string, scratch *ScratchCheckpoint) error {
	w := NewWireWriter(control)
	if err := w.Path(destinationPath); err != nil {
		return err
	}
	if err := writePrivateOutput(w, output); err != nil {
		return err
	}
	if err := w.OptionalPath(scratchDirectory); err != nil {
		return err
	}
	if err := writeScratchCheckpoint(w, scratch); err != nil {
		return err
	}
	return w.Finish()
}

// ReadCleanupRequest decodes one cleanup request (Rust
// wire_cleanup::read_request: the scratch path and checkpoint must
// agree).
func ReadCleanupRequest(control *Control) (*CleanupRequest, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	request := &CleanupRequest{}
	if request.DestinationPath, err = r.Path(); err != nil {
		return nil, err
	}
	if request.Output, err = readPrivateOutput(r); err != nil {
		return nil, err
	}
	if request.ScratchDirectory, err = r.OptionalPath(); err != nil {
		return nil, err
	}
	if request.Scratch, err = readScratchCheckpoint(r); err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	if (request.Scratch != nil) != (request.ScratchDirectory != nil) {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker cleanup scratch path and checkpoint disagree"}
	}
	return request, nil
}

// EarlyDiscard is the wire form of the pre-publication discard facts
// (Rust publication::cleanup::EarlyDiscard): the discarded private
// output attempt, the optional unresolved artifact, and the
// housekeeping evidence. The Go publication machine keeps its discard
// facts private, so the worker boundary owns the wire shape.
type EarlyDiscard struct {
	Output              publication.PrivateOutputAttempt
	Artifact            *publication.CleanupArtifact
	Housekeeping        publication.Housekeeping
	VisibleHousekeeping []publication.HousekeepingArtifact
}

// Clean reports whether the discard left no residue at all (Rust
// client discard_clean).
func (d *EarlyDiscard) Clean() bool {
	return d.Artifact == nil && d.Housekeeping == publication.HousekeepingNone && len(d.VisibleHousekeeping) == 0
}

// WriteCleanupResult writes one cleanup result (Rust
// wire_cleanup::write_result: the discarded output, its optional
// artifact as a cleanup ledger, the housekeeping facts, and the
// optional scratch cleanup).
func WriteCleanupResult(control *Control, discarded *EarlyDiscard, scratch *ScratchCleanup) error {
	w := NewWireWriter(control)
	if err := writePrivateOutput(w, &discarded.Output); err != nil {
		return err
	}
	artifacts := publication.NewCleanupArtifacts()
	if discarded.Artifact != nil {
		artifacts.Push(*discarded.Artifact)
	}
	if err := writeCleanupArtifacts(w, &artifacts); err != nil {
		return err
	}
	if err := writeHousekeepingValue(w, discarded.Housekeeping); err != nil {
		return err
	}
	if err := writeHousekeepingList(w, discarded.VisibleHousekeeping); err != nil {
		return err
	}
	if err := writeScratchCleanup(w, scratch); err != nil {
		return err
	}
	return w.Finish()
}

// ReadCleanupResult decodes one cleanup result (Rust
// wire_cleanup::read_result: more than one output residue is
// corruption).
func ReadCleanupResult(control *Control) (*EarlyDiscard, *ScratchCleanup, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, nil, err
	}
	discarded := &EarlyDiscard{}
	if discarded.Output, err = readPrivateOutput(r); err != nil {
		return nil, nil, err
	}
	artifacts, err := readCleanupArtifacts(r)
	if err != nil {
		return nil, nil, err
	}
	if artifacts.Len() > 1 {
		return nil, nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker cleanup returned multiple output residues"}
	}
	if artifacts.Len() == 1 {
		discarded.Artifact = artifacts.At(0)
	}
	housekeeping, err := r.Byte()
	if err != nil {
		return nil, nil, err
	}
	if discarded.Housekeeping, err = readHousekeepingValueByte(housekeeping); err != nil {
		return nil, nil, err
	}
	if discarded.VisibleHousekeeping, err = readHousekeepingArtifacts(r); err != nil {
		return nil, nil, err
	}
	scratch, err := readScratchCleanup(r)
	if err != nil {
		return nil, nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, nil, err
	}
	return discarded, scratch, nil
}

// writeScratchCheckpoint writes one optional scratch checkpoint (Rust
// wire_cleanup::write_checkpoint: presence bit, then the attempt
// identity, the directory and security facts, and the bounded entry
// list).
func writeScratchCheckpoint(w *WireWriter, checkpoint *ScratchCheckpoint) error {
	if checkpoint == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	if err := w.Bytes(checkpoint.AttemptID[:]); err != nil {
		return err
	}
	if err := writeIdentity(w, checkpoint.DirectoryIdentity); err != nil {
		return err
	}
	if err := w.U16(checkpoint.CreationSecurity.Kind); err != nil {
		return err
	}
	if err := w.Bytes(checkpoint.CreationSecurity.Commitment[:]); err != nil {
		return err
	}
	if len(checkpoint.Entries) > scratchEntryCapacity {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker scratch checkpoint entries"}
	}
	if err := w.Byte(uint8(len(checkpoint.Entries))); err != nil {
		return err
	}
	for _, entry := range checkpoint.Entries {
		if err := w.U32(entry.Ordinal); err != nil {
			return err
		}
		if err := writeIdentity(w, entry.Identity); err != nil {
			return err
		}
	}
	return nil
}

// readScratchCheckpoint decodes one optional scratch checkpoint (Rust
// wire_cleanup::read_checkpoint: zero attempt, invalid identity or
// security, or an entry count above the capacity is corruption).
func readScratchCheckpoint(r *WireReader) (*ScratchCheckpoint, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	checkpoint := &ScratchCheckpoint{}
	if checkpoint.AttemptID, err = r.Array16(); err != nil {
		return nil, err
	}
	if checkpoint.DirectoryIdentity, err = readIdentity(r); err != nil {
		return nil, err
	}
	if checkpoint.CreationSecurity.Kind, err = r.U16(); err != nil {
		return nil, err
	}
	if checkpoint.CreationSecurity.Commitment, err = r.Array32(); err != nil {
		return nil, err
	}
	count, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if checkpoint.AttemptID == [16]byte{} ||
		!scratchIdentityValid(checkpoint.DirectoryIdentity) ||
		!scratchSecurityValid(checkpoint.CreationSecurity.Kind, checkpoint.CreationSecurity.Commitment) ||
		count > scratchEntryCapacity {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker scratch checkpoint is invalid"}
	}
	for index := 0; index < int(count); index++ {
		entry := ScratchCheckpointEntry{}
		if entry.Ordinal, err = r.U32(); err != nil {
			return nil, err
		}
		if entry.Identity, err = readIdentity(r); err != nil {
			return nil, err
		}
		if !scratchIdentityValid(entry.Identity) {
			return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker scratch checkpoint is invalid"}
		}
		for _, prior := range checkpoint.Entries {
			if prior.Ordinal == entry.Ordinal || prior.Identity == entry.Identity {
				return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker scratch checkpoint contains duplicate authority"}
			}
		}
		checkpoint.Entries = append(checkpoint.Entries, entry)
	}
	return checkpoint, nil
}

// ScratchProblem is the fixed cleanup problem of one scratch residue
// (Rust recovery::scratch::ScratchProblem: the code class, the
// optional errno, and the exact detail).
type ScratchProblem struct {
	Code   format.ErrorCode
	OSCode *int32
	Detail string
}

// ScratchResidue is one authorized scratch artifact whose durable
// absence was not proved (Rust recovery::scratch::ScratchResidue).
type ScratchResidue struct {
	Ordinal                    uint32
	DirectoryIdentity          publication.LocalFileIdentity
	Basename                   []byte
	Identity                   publication.LocalFileIdentity
	CreationSecurityKind       uint16
	CreationSecurityCommitment [32]byte
	Problem                    ScratchProblem
}

// ScratchCleanup is the terminal facts of one scratch attempt cleanup
// (Rust recovery::scratch::ScratchCleanup).
type ScratchCleanup struct {
	AttemptID                  [16]byte
	DirectoryIdentity          publication.LocalFileIdentity
	CreationSecurityKind       uint16
	CreationSecurityCommitment [32]byte
	Residues                   []ScratchResidue
	Housekeeping               publication.Housekeeping
	VisibleHousekeeping        []publication.HousekeepingArtifact
}

// Clean reports whether the scratch cleanup proved every artifact
// absent (Rust ScratchCleanup::clean plus the client scratch_clean
// housekeeping check).
func (s *ScratchCleanup) Clean() bool {
	return len(s.Residues) == 0 &&
		s.Housekeeping == publication.HousekeepingNone &&
		len(s.VisibleHousekeeping) == 0
}

// writeScratchCleanup writes one optional scratch cleanup (Rust
// wire_cleanup::write_scratch: the attempt identity, the directory and
// security facts, the bounded residue list with the checkpoint-name
// proof, the housekeeping facts).
func writeScratchCleanup(w *WireWriter, cleanup *ScratchCleanup) error {
	if cleanup == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	if err := w.Bytes(cleanup.AttemptID[:]); err != nil {
		return err
	}
	if err := writeIdentity(w, cleanup.DirectoryIdentity); err != nil {
		return err
	}
	if err := w.U16(cleanup.CreationSecurityKind); err != nil {
		return err
	}
	if err := w.Bytes(cleanup.CreationSecurityCommitment[:]); err != nil {
		return err
	}
	if len(cleanup.Residues) > scratchEntryCapacity {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker scratch cleanup residues"}
	}
	if err := w.Byte(uint8(len(cleanup.Residues))); err != nil {
		return err
	}
	for _, residue := range cleanup.Residues {
		if err := w.U32(residue.Ordinal); err != nil {
			return err
		}
		if err := writeIdentity(w, residue.DirectoryIdentity); err != nil {
			return err
		}
		if err := w.SizedBytes(residue.Basename); err != nil {
			return err
		}
		if err := writeIdentity(w, residue.Identity); err != nil {
			return err
		}
		if err := w.U16(residue.CreationSecurityKind); err != nil {
			return err
		}
		if err := w.Bytes(residue.CreationSecurityCommitment[:]); err != nil {
			return err
		}
		problem := WireProblem{Code: residue.Problem.Code, OSCode: residue.Problem.OSCode, Detail: residue.Problem.Detail}
		if err := writeProblem(w, &problem); err != nil {
			return err
		}
	}
	if err := writeHousekeepingValue(w, cleanup.Housekeeping); err != nil {
		return err
	}
	return writeHousekeepingList(w, cleanup.VisibleHousekeeping)
}

// readScratchCleanup decodes one optional scratch cleanup (Rust
// wire_cleanup::read_scratch: every residue must carry the recorded
// directory and security facts, the exact checkpoint basename, and no
// duplicate authority).
func readScratchCleanup(r *WireReader) (*ScratchCleanup, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	cleanup := &ScratchCleanup{}
	if cleanup.AttemptID, err = r.Array16(); err != nil {
		return nil, err
	}
	if cleanup.DirectoryIdentity, err = readIdentity(r); err != nil {
		return nil, err
	}
	if cleanup.CreationSecurityKind, err = r.U16(); err != nil {
		return nil, err
	}
	if cleanup.CreationSecurityCommitment, err = r.Array32(); err != nil {
		return nil, err
	}
	count, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if cleanup.AttemptID == [16]byte{} ||
		!scratchIdentityValid(cleanup.DirectoryIdentity) ||
		!scratchSecurityValid(cleanup.CreationSecurityKind, cleanup.CreationSecurityCommitment) ||
		count > scratchEntryCapacity {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker scratch cleanup is invalid"}
	}
	for index := 0; index < int(count); index++ {
		residue := ScratchResidue{}
		if residue.Ordinal, err = r.U32(); err != nil {
			return nil, err
		}
		if residue.DirectoryIdentity, err = readIdentity(r); err != nil {
			return nil, err
		}
		if residue.Basename, err = r.BoxedBytes(); err != nil {
			return nil, err
		}
		if residue.Identity, err = readIdentity(r); err != nil {
			return nil, err
		}
		if residue.CreationSecurityKind, err = r.U16(); err != nil {
			return nil, err
		}
		if residue.CreationSecurityCommitment, err = r.Array32(); err != nil {
			return nil, err
		}
		problem, err := readProblem(r)
		if err != nil {
			return nil, err
		}
		residue.Problem = ScratchProblem{Code: problem.Code, OSCode: problem.OSCode, Detail: problem.Detail}
		expected := checkpointBasename(cleanup.AttemptID, residue.Ordinal)
		if residue.DirectoryIdentity != cleanup.DirectoryIdentity ||
			!scratchIdentityValid(residue.Identity) ||
			residue.CreationSecurityKind != cleanup.CreationSecurityKind ||
			residue.CreationSecurityCommitment != cleanup.CreationSecurityCommitment ||
			!bytes.Equal(residue.Basename, expected) {
			return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker scratch cleanup authority is inconsistent"}
		}
		for _, prior := range cleanup.Residues {
			if prior.Ordinal == residue.Ordinal || prior.Identity == residue.Identity {
				return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker scratch cleanup authority is inconsistent"}
			}
		}
		cleanup.Residues = append(cleanup.Residues, residue)
	}
	housekeeping, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if cleanup.Housekeeping, err = readHousekeepingValueByte(housekeeping); err != nil {
		return nil, err
	}
	if cleanup.VisibleHousekeeping, err = readHousekeepingArtifacts(r); err != nil {
		return nil, err
	}
	return cleanup, nil
}
