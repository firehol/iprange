// Shared cleanup and housekeeping facts of the retained-namespace
// machine (Rust publication/types.rs + validation::LocalFileIdentity;
// the Go package graph makes live the single authority because the GC
// retirement machinery and the windows cleanup barrier both live
// here). Publication and worker wrap these types for their wire and
// result surfaces; the constant values are the Rust enum orders, so
// the GC envelope codec maps them to the specification numbers.

package live

import "github.com/firehol/iprange/v4/go/internal/format"

// LocalFileIdentity is the portable local identity of one retained
// inode (Rust validation::LocalFileIdentity): the identity kind plus
// the 32-byte encoded payload that travels in reservation records,
// public result facts, and GC envelopes.
type LocalFileIdentity struct {
	Kind  uint16
	Bytes [32]byte
}

// LocalFileIdentityFromDeviceInode builds the portable identity of
// one device+inode pair (Rust namespace::local_identity). The
// platform arm selects the identity kind and the byte layout.
func LocalFileIdentityFromDeviceInode(device, inode uint64) LocalFileIdentity {
	return localIdentityFromDeviceInode(device, inode)
}

// DeviceInode decodes the portable identity to the internal
// device+inode pair (Rust Identity::decode); ok is false otherwise.
func (f LocalFileIdentity) DeviceInode() (device, inode uint64, ok bool) {
	return f.deviceInode()
}

// ArtifactKind classifies one retained namespace artifact (Rust
// ArtifactKind; the enum order is the Rust declaration order, the
// GC envelope codes map by kind in gc_codec.go).
type ArtifactKind uint8

const (
	ArtifactPrivateOutput ArtifactKind = iota
	ArtifactPrivateReservation
	ArtifactOwnedCoordination
	ArtifactAuthorizedScratch
	ArtifactOwnedMain
	ArtifactUnpublishedMainTail
)

// DirectoryRole classifies the directory a retained artifact lives in
// (Rust DirectoryRole).
type DirectoryRole uint8

const (
	DirectoryRoleDestination DirectoryRole = iota
	DirectoryRoleScratchDirectory
	DirectoryRoleMainFile
)

// Housekeeping classifies the abandoned-residue housekeeping evidence
// of one failed operation (Rust Housekeeping).
type Housekeeping uint8

const (
	HousekeepingNone Housekeeping = iota
	HousekeepingCrashReappearancePossible
	HousekeepingVisible
)

// Merge folds two housekeeping classes (Rust Housekeeping::merge:
// Visible dominates, then CrashReappearancePossible).
func (h Housekeeping) Merge(other Housekeeping) Housekeeping { return h.merge(other) }

func (h Housekeeping) merge(other Housekeeping) Housekeeping {
	if h == HousekeepingVisible || other == HousekeepingVisible {
		return HousekeepingVisible
	}
	if h == HousekeepingCrashReappearancePossible || other == HousekeepingCrashReappearancePossible {
		return HousekeepingCrashReappearancePossible
	}
	return HousekeepingNone
}

// HousekeepingState classifies one visible housekeeping artifact (Rust
// HousekeepingState).
type HousekeepingState uint8

const (
	HousekeepingMovePending HousekeepingState = iota
	HousekeepingMoveAmbiguous
	HousekeepingInert
	HousekeepingConflict
)

// ArtifactPresence classifies one housekeeping artifact slot (Rust
// ArtifactPresence).
type ArtifactPresence uint8

const (
	ArtifactAbsent ArtifactPresence = iota
	ArtifactPresent
	ArtifactUnclassified
)

// CreationSecurity is the creator-only security evidence of one
// created artifact (Rust CreationSecurity: the commitment kind and the
// 32-byte creator-only commitment).
type CreationSecurity struct {
	Kind       uint16
	Commitment [32]byte
}

// HousekeepingArtifact is one visible housekeeping artifact of an
// abandoned live or publication artifact (Rust HousekeepingArtifact;
// produced by the GC resolver and streamed by the windows
// housekeeping list).
type HousekeepingArtifact struct {
	State                    HousekeepingState
	DirectoryRole            DirectoryRole
	DirectoryIdentity        LocalFileIdentity
	BasenameEncoding         uint16
	AttemptID                [16]byte
	Ordinal                  uint32
	EnvelopeBasename         []byte
	EnvelopeIdentity         LocalFileIdentity
	SourceBasename           []byte
	InertBasename            []byte
	SourcePresence           ArtifactPresence
	SourceIdentity           *LocalFileIdentity
	InertPresence            ArtifactPresence
	InertIdentity            *LocalFileIdentity
	Kind                     ArtifactKind
	CreationSecurity         CreationSecurity
	SelectedEnvelopeSequence uint64
}

// GCHousekeepingCandidateKind classifies one scanned GC name (Rust
// WindowsHousekeepingCandidateKind).
type GCHousekeepingCandidateKind uint8

const (
	GCCandidateEnvelope GCHousekeepingCandidateKind = iota
	GCCandidateInertPayload
)

// GCHousekeepingEntry is one exact scanned housekeeping candidate
// (Rust WindowsHousekeepingEntry).
type GCHousekeepingEntry struct {
	DirectoryIdentity LocalFileIdentity
	CandidateKind     GCHousekeepingCandidateKind
	BasenameEncoding  uint16
	Basename          []byte
	Identity          *LocalFileIdentity
	AttemptID         *[16]byte
	Ordinal           *uint32
	Artifact          *HousekeepingArtifact
	Problem           *format.Error
}

// GCHousekeepingList is one completed constant-memory scan (Rust
// WindowsHousekeepingList).
type GCHousekeepingList struct {
	DirectoryIdentity LocalFileIdentity
	Entries           uint64
}

// GCHousekeepingRemoval is the factual terminal state of one removal
// (Rust WindowsHousekeepingRemoval).
type GCHousekeepingRemoval struct {
	Housekeeping Housekeeping
	Visible      []HousekeepingArtifact
	Cause        *format.Error
}

// GCHousekeepingPayload is the optional exact content evidence of one
// removal (Rust HousekeepingPayloadIdentity).
type GCHousekeepingPayload struct {
	ByteLength    uint64
	SHA512        [64]byte
	DatabaseID    [16]byte
	TransactionID uint64
	CommitNonce   [16]byte
}
