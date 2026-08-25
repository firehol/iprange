//go:build windows

// Explicit discovery and removal of authenticated Windows GC pairs
// (Rust publication/gc_maintenance.rs). Listing is constant-memory and
// grants no deletion authority: every candidate is inspected against
// its envelope and reported with the exact problem class. Removal
// takes the caller-certified expected identities and resolves the pair
// through the common GC move resolver.

package live

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// gcHousekeepingCandidateKind classifies one scanned GC name (Rust
// WindowsHousekeepingCandidateKind).
type gcHousekeepingCandidateKind uint8

const (
	gcCandidateEnvelope gcHousekeepingCandidateKind = iota
	gcCandidateInertPayload
)

// gcHousekeepingEntry is one exact scanned housekeeping candidate
// (Rust WindowsHousekeepingEntry). The basename is the encoded form in
// the platform basename encoding.
type gcHousekeepingEntry struct {
	DirectoryIdentity LocalFileIdentity
	CandidateKind     gcHousekeepingCandidateKind
	BasenameEncoding  uint16
	Basename          []byte
	Identity          *LocalFileIdentity
	AttemptID         *[16]byte
	Ordinal           *uint32
	Artifact          *HousekeepingArtifact
	Problem           *format.Error
}

// gcHousekeepingList is one completed constant-memory housekeeping
// scan (Rust WindowsHousekeepingList).
type gcHousekeepingList struct {
	DirectoryIdentity LocalFileIdentity
	Entries           uint64
}

// gcHousekeepingRemoval is the factual terminal state of one exact
// housekeeping removal (Rust WindowsHousekeepingRemoval).
type gcHousekeepingRemoval struct {
	Housekeeping Housekeeping
	Visible      []HousekeepingArtifact
	Cause        *format.Error
}

// gcHousekeepingPayload is the optional exact content evidence of one
// removal (Rust HousekeepingPayloadIdentity: the digest plus the
// publication tuple).
type gcHousekeepingPayload struct {
	ByteLength    uint64
	SHA512        [64]byte
	DatabaseID    [16]byte
	TransactionID uint64
	CommitNonce   [16]byte
}

// gcListHousekeeping streams one constant-memory housekeeping scan of
// the retained directory (Rust gc_maintenance::list): every GC
// candidate name is inspected against its envelope and delivered to
// the sink.
func gcListHousekeeping(path string, check func() error, sink func(entry *gcHousekeepingEntry) error) (gcHousekeepingList, error) {
	if err := check(); err != nil {
		return gcHousekeepingList{}, err
	}
	dir, err := OpenDirectory(path)
	if err != nil {
		return gcHousekeepingList{}, gcNamespaceProblem(err)
	}
	defer dir.Close()
	directoryIdentity := LocalFileIdentityFromDeviceInode(dir.Identity().device, dir.Identity().inode)
	var entries uint64
	err = dir.Scan(func(bytes []byte) error {
		if err := check(); err != nil {
			return err
		}
		candidate := gcCandidateOf(bytes)
		if !candidate.matched {
			return nil
		}
		entry := gcInspectHousekeeping(dir, directoryIdentity, bytes, candidate)
		if err := sink(entry); err != nil {
			return err
		}
		entries++
		return nil
	})
	if err != nil {
		return gcHousekeepingList{}, err
	}
	return gcHousekeepingList{DirectoryIdentity: directoryIdentity, Entries: entries}, nil
}

// gcInspectHousekeeping inspects one GC candidate (Rust
// gc_maintenance::inspect): the canonical decode, the envelope open,
// the observed pair, and the artifact projection.
func gcInspectHousekeeping(dir *Directory, directoryIdentity LocalFileIdentity, bytes []byte, candidate gcCandidate) *gcHousekeepingEntry {
	encoded := gcEncodedBasename(bytes)
	kind := gcCandidateEnvelope
	if !candidate.envelope {
		kind = gcCandidateInertPayload
	}
	identity := gcEntryIdentity(dir, bytes)
	attempt, ordinal, decoded := candidate.attempt, candidate.ordinal, candidate.decoded
	if !decoded {
		return gcHousekeepingCandidateEntry(directoryIdentity, kind, encoded, identity, nil, nil, nil,
			gcCleanupConflict("Windows housekeeping name is not canonical"))
	}
	envelopeName, err := gcEnvelopeName(attempt, ordinal)
	if err != nil {
		return gcHousekeepingCandidateEntry(directoryIdentity, kind, encoded, identity, &attempt, &ordinal, nil,
			gcNamespaceProblem(err))
	}
	envelope, openErr := gcOpen(dir, envelopeName, false)
	if openErr != nil {
		return gcHousekeepingCandidateEntry(directoryIdentity, kind, encoded, identity, &attempt, &ordinal, nil, gcProblemOf(openErr))
	}
	if envelope == nil {
		return gcHousekeepingCandidateEntry(directoryIdentity, kind, encoded, identity, &attempt, &ordinal, nil,
			gcCleanupConflict("GC candidate has no authority envelope"))
	}
	observed := gcObservePair(dir, envelope)
	var problem *format.Error
	if observed.state == HousekeepingConflict {
		problem = gcCleanupConflict("GC payload names or identities conflict")
	}
	artifact := gcArtifact(dir.Identity(), envelope, observed)
	return gcHousekeepingCandidateEntry(directoryIdentity, kind, encoded, identity, &attempt, &ordinal, &artifact, problem)
}

// gcEncodedBasename converts one scanned ASCII name to the encoded
// basename of the platform (Rust gc_maintenance::encoded_basename:
// ASCII to UTF-16LE on windows).
func gcEncodedBasename(ascii []byte) []byte {
	return gcNameBytes(string(ascii))
}

// gcEntryIdentity looks up one scanned name (Rust directory.entry).
func gcEntryIdentity(dir *Directory, bytes []byte) *LocalFileIdentity {
	entry, present, err := dir.Entry(string(bytes))
	if err != nil || !present {
		return nil
	}
	value := LocalFileIdentityFromDeviceInode(entry.Identity.device, entry.Identity.inode)
	return &value
}

// gcHousekeepingCandidateEntry builds one entry with the optional
// decodes and problem (Rust gc_maintenance::candidate_entry).
func gcHousekeepingCandidateEntry(directoryIdentity LocalFileIdentity, kind gcHousekeepingCandidateKind, basename []byte, identity *LocalFileIdentity, attempt *[16]byte, ordinal *uint32, artifact *HousekeepingArtifact, problem *format.Error) *gcHousekeepingEntry {
	return &gcHousekeepingEntry{
		DirectoryIdentity: directoryIdentity,
		CandidateKind:     kind,
		BasenameEncoding:  uint16(gcBasenameEncodingValue()),
		Basename:          basename,
		Identity:          identity,
		AttemptID:         attempt,
		Ordinal:           ordinal,
		Artifact:          artifact,
		Problem:           problem,
	}
}

// gcRemoveHousekeeping resolves and best-effort removes one exact GC
// pair (Rust gc_maintenance::remove): the directory identity must
// match, the envelope must exist with the expected identity and
// payload, and the common resolver runs the move.
func gcRemoveHousekeeping(path string, expectedDirectory LocalFileIdentity, attemptID [16]byte, ordinal uint32, expectedEnvelope LocalFileIdentity, expectedPayload *gcHousekeepingPayload, check func() error) (gcHousekeepingRemoval, error) {
	if err := check(); err != nil {
		return gcHousekeepingRemoval{}, err
	}
	if attemptID == [16]byte{} {
		return gcHousekeepingRemoval{}, &format.Error{Code: format.CodeInvalidArgument, Detail: "housekeeping attempt id must be nonzero"}
	}
	dir, err := OpenDirectory(path)
	if err != nil {
		return gcHousekeepingRemoval{}, gcNamespaceProblem(err)
	}
	defer dir.Close()
	if LocalFileIdentityFromDeviceInode(dir.Identity().device, dir.Identity().inode) != expectedDirectory {
		return gcHousekeepingRemoval{}, &format.Error{Code: format.CodeConflict, Detail: "housekeeping directory identity mismatch"}
	}
	envelopeName, err := gcEnvelopeName(attemptID, ordinal)
	if err != nil {
		return gcHousekeepingRemoval{}, gcNamespaceProblem(err)
	}
	envelope, err := gcOpen(dir, envelopeName, true)
	if err != nil {
		return gcHousekeepingRemoval{}, gcCleanupConflict("GC envelope is not resolvable")
	}
	if envelope == nil {
		if err := dir.Verify(); err != nil {
			return gcHousekeepingRemoval{}, gcNamespaceProblem(err)
		}
		if err := dir.RequireAbsent(envelopeName); err != nil {
			return gcHousekeepingRemoval{}, gcCleanupConflict("Windows housekeeping ownership changed")
		}
		inertName, err := gcInertName(attemptID, ordinal)
		if err != nil {
			return gcHousekeepingRemoval{}, gcNamespaceProblem(err)
		}
		if err := dir.RequireAbsent(inertName); err != nil {
			return gcHousekeepingRemoval{}, gcCleanupConflict("Windows housekeeping ownership changed")
		}
		return gcHousekeepingRemoval{Housekeeping: HousekeepingCrashReappearancePossible, Cause: nil}, nil
	}
	if LocalFileIdentityFromDeviceInode(envelope.identity.device, envelope.identity.inode) != expectedEnvelope {
		return gcHousekeepingRemoval{}, gcCleanupConflict("GC envelope identity changed before removal")
	}
	if expectedPayload != nil && !gcPayloadMatchesExpected(envelope.header.payload, expectedPayload) {
		return gcHousekeepingRemoval{}, gcCleanupConflict("GC payload identity changed before removal")
	}
	if err := check(); err != nil {
		return gcHousekeepingRemoval{}, err
	}
	retired := gcResolveExisting(dir, envelope)
	return gcHousekeepingRemoval{
		Housekeeping: retired.housekeeping,
		Visible:      gcSliceOf(retired.visible),
		Cause:        gcProblemOrNil(retired.problem),
	}, nil
}

// gcPayloadMatchesExpected compares one envelope payload against the
// caller-certified expectation (Rust equality of Option<Payload>).
func gcPayloadMatchesExpected(payload *gcPayload, expected *gcHousekeepingPayload) bool {
	if payload == nil {
		return false
	}
	if payload.byteLength != expected.ByteLength || payload.sha512 != expected.SHA512 {
		return false
	}
	if payload.databaseID == [16]byte{} {
		return expected.DatabaseID == [16]byte{} && expected.TransactionID == 0 && expected.CommitNonce == [16]byte{}
	}
	return payload.databaseID == expected.DatabaseID &&
		payload.transactionID == expected.TransactionID &&
		payload.commitNonce == expected.CommitNonce
}

// gcSliceOf folds one optional artifact into the removal ledger (Rust
// visible.into_iter().collect()).
func gcSliceOf(artifact *HousekeepingArtifact) []HousekeepingArtifact {
	if artifact == nil {
		return nil
	}
	return []HousekeepingArtifact{*artifact}
}
