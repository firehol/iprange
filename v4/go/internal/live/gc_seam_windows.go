//go:build windows

// Exported GC seam of the publication surface (the internal machine in
// gc*.go stays package-private; publication composes these entry
// points with its destination and seed facts). The seam mirrors the
// Rust publication arms: retire with and without an observer, resume
// one abandoned envelope, the source barrier, the fresh attempt, and
// the offline list/remove maintenance.

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// GCAuthority identifies one retired artifact for the GC machine
// (Rust publication::gc::Authority).
type GCAuthority struct {
	AttemptID        [16]byte
	Ordinal          uint32
	Kind             ArtifactKind
	DirectoryRole    DirectoryRole
	SourceName       string
	SourceFile       *os.File
	Identity         FileIdentity
	CreationSecurity CreationSecurity
	Payload          *GCPayload
}

// GCPayload is the optional exact content evidence of one retired
// artifact (Rust gc_codec::Payload).
type GCPayload struct {
	ByteLength    uint64
	SHA512        [64]byte
	DatabaseID    [16]byte
	TransactionID uint64
	CommitNonce   [16]byte
}

// GCRetirement is the factual outcome of one GC retirement (Rust
// gc::Retirement).
type GCRetirement struct {
	Problem      *format.Error
	Housekeeping Housekeeping
	Visible      *HousekeepingArtifact
}

// GCRetire retires one exact artifact (Rust gc::retire).
func GCRetire(directory *Directory, authority *GCAuthority) GCRetirement {
	return gcRetirementOf(gcRetire(directory, gcAuthorityOf(authority)))
}

// GCRetireObserved retires one exact artifact and streams the pending
// artifact (Rust gc::retire_observed).
func GCRetireObserved(directory *Directory, authority *GCAuthority, observer func(*HousekeepingArtifact) error) GCRetirement {
	return gcRetirementOf(gcRetireWith(directory, gcAuthorityOf(authority), true, observer))
}

// GCResumeAuthority is the expected identity of one resumed envelope
// (Rust gc::ResumeAuthority).
type GCResumeAuthority struct {
	AttemptID     [16]byte
	Ordinal       uint32
	Kind          ArtifactKind
	DirectoryRole DirectoryRole
	SourceName    string
	Identity      FileIdentity
	Payload       *GCPayload
}

// GCResume resumes one abandoned retirement through its envelope (Rust
// gc::resume); nil means no envelope exists.
func GCResume(directory *Directory, expected *GCResumeAuthority) (*GCRetirement, *format.Error) {
	retired, err := gcResume(directory, gcResumeAuthority{
		attemptID:     expected.AttemptID,
		ordinal:       expected.Ordinal,
		kind:          expected.Kind,
		directoryRole: expected.DirectoryRole,
		sourceName:    expected.SourceName,
		identity:      expected.Identity,
		payload:       gcPayloadOf(expected.Payload),
	})
	if err != nil {
		return nil, err
	}
	if retired == nil {
		return nil, nil
	}
	value := gcRetirementOf(*retired)
	return &value, nil
}

// GCRequireSourceAvailable proves one retained source is not owned by
// housekeeping (Rust gc::require_source_available; the requireAvailable
// barrier of the live lifecycle uses the same machine).
func GCRequireSourceAvailable(directory *Directory, attemptID [16]byte, ordinal uint32, kind ArtifactKind, directoryRole DirectoryRole, sourceName string, identity FileIdentity) *format.Error {
	return gcRequireSourceAvailable(directory, attemptID, ordinal, kind, directoryRole, sourceName, identity)
}

// GCFreshAttempt draws one collision-free attempt identity (Rust
// gc::fresh_attempt).
func GCFreshAttempt(directory *Directory, sourceName string, identity FileIdentity, ordinal uint32, kind ArtifactKind, directoryRole DirectoryRole) ([16]byte, *format.Error) {
	return gcFreshAttempt(directory, sourceName, identity, ordinal, kind, directoryRole)
}

// GCListHousekeeping streams one offline housekeeping scan (Rust
// list_windows_housekeeping windows arm).
func GCListHousekeeping(path string, check func() error, sink func(entry *GCHousekeepingEntry) error) (GCHousekeepingList, error) {
	list, err := gcListHousekeeping(path, check, func(entry *gcHousekeepingEntry) error {
		return sink(&GCHousekeepingEntry{
			DirectoryIdentity: entry.DirectoryIdentity,
			CandidateKind:     GCHousekeepingCandidateKind(entry.CandidateKind),
			BasenameEncoding:  entry.BasenameEncoding,
			Basename:          entry.Basename,
			Identity:          entry.Identity,
			AttemptID:         entry.AttemptID,
			Ordinal:           entry.Ordinal,
			Artifact:          entry.Artifact,
			Problem:           entry.Problem,
		})
	})
	if err != nil {
		return GCHousekeepingList{}, err
	}
	return GCHousekeepingList{DirectoryIdentity: list.DirectoryIdentity, Entries: list.Entries}, nil
}

// GCRemoveHousekeeping resolves and best-effort removes one exact GC
// pair (Rust remove_windows_housekeeping windows arm).
func GCRemoveHousekeeping(path string, expectedDirectory LocalFileIdentity, attemptID [16]byte, ordinal uint32, expectedEnvelope LocalFileIdentity, expectedPayload *GCHousekeepingPayload, check func() error) (GCHousekeepingRemoval, error) {
	removal, err := gcRemoveHousekeeping(path, expectedDirectory, attemptID, ordinal, expectedEnvelope, gcHousekeepingPayloadOf(expectedPayload), check)
	if err != nil {
		return GCHousekeepingRemoval{}, err
	}
	return GCHousekeepingRemoval{
		Housekeeping: removal.Housekeeping,
		Visible:      removal.Visible,
		Cause:        removal.Cause,
	}, nil
}

func gcAuthorityOf(authority *GCAuthority) gcAuthority {
	return gcAuthority{
		attemptID:        authority.AttemptID,
		ordinal:          authority.Ordinal,
		kind:             authority.Kind,
		directoryRole:    authority.DirectoryRole,
		sourceName:       authority.SourceName,
		sourceFile:       authority.SourceFile,
		identity:         authority.Identity,
		creationSecurity: authority.CreationSecurity,
		payload:          gcPayloadOf(authority.Payload),
	}
}

func gcRetirementOf(retirement gcRetirement) GCRetirement {
	return GCRetirement{
		Problem:      retirement.problem,
		Housekeeping: retirement.housekeeping,
		Visible:      retirement.visible,
	}
}

func gcPayloadOf(payload *GCPayload) *gcPayload {
	if payload == nil {
		return nil
	}
	return &gcPayload{
		byteLength:    payload.ByteLength,
		sha512:        payload.SHA512,
		databaseID:    payload.DatabaseID,
		transactionID: payload.TransactionID,
		commitNonce:   payload.CommitNonce,
	}
}

func gcHousekeepingPayloadOf(payload *GCHousekeepingPayload) *gcHousekeepingPayload {
	if payload == nil {
		return nil
	}
	return &gcHousekeepingPayload{
		ByteLength:    payload.ByteLength,
		SHA512:        payload.SHA512,
		DatabaseID:    payload.DatabaseID,
		TransactionID: payload.TransactionID,
		CommitNonce:   payload.CommitNonce,
	}
}

// GCEnvelopeName builds the authenticated envelope name of one
// attempt (Rust gc_name::envelope; the publication attempt-collision
// check uses it for the fixed ordinals).
func GCEnvelopeName(attempt [16]byte, ordinal uint32) (string, error) {
	return gcEnvelopeName(attempt, ordinal)
}

// GCInertName builds the inert payload name of one attempt (Rust
// gc_name::inert).
func GCInertName(attempt [16]byte, ordinal uint32) (string, error) {
	return gcInertName(attempt, ordinal)
}
