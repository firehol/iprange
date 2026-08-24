// Package publication is the Go peer of the Rust publication resolver
// (v4/rust/iprange-livedb/src/publication): the portable facts of one
// immutable namespace publication, the reservation codec, the publish
// attempt machine, the resolver, residue inspection, and abandoned
// artifact maintenance. The retained-directory, namespace-mutation,
// and artifact-lock syscall machines stay in internal/live; this
// package owns the publication state machine and its public fact
// shapes over them (Rust publication/{resolver,reservation,
// reservation_file,reservation_inspection,reservation_verify,
// replacement,replacement_resolver,output,cleanup,attempt,main_file,
// residue,maintenance,types,problem}.rs).
//
// Hot paths are allocation-free over mapped views; every result fact
// below is a bounded record. Persistent content is mmap-only: the
// reservation file is decoded from mapped views and no complete
// database page ever exists in owned memory.

package publication

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// PublicationPolicy is the namespace policy selected for one immutable
// publication (Rust PublicationPolicy).
type PublicationPolicy uint8

const (
	PolicyFailIfExists PublicationPolicy = iota
	PolicyReplaceExisting
	PolicyReplaceExistingNoRollback
)

// PublicationStatus classifies one publication outcome (Rust
// PublicationStatus).
type PublicationStatus uint8

const (
	PublicationNotPublished PublicationStatus = iota
	PublicationPublished
	PublicationOutcomeUnknown
)

// DestinationContent describes the destination slot after one
// publication attempt (Rust DestinationContent).
type DestinationContent uint8

const (
	DestinationContentDesired DestinationContent = iota
	DestinationContentPrevious
	DestinationContentAbsent
	DestinationContentOther
	DestinationContentUnclassified
)

// LaterCanonical classifies the canonical reservation observed after
// one publication attempt (Rust LaterCanonical).
type LaterCanonical uint8

const (
	LaterCanonicalNone LaterCanonical = iota
	LaterCanonicalReservationOrTransition
	LaterCanonicalReadyLiveSidecar
)

// LiveLineage classifies the live sidecar observed after a publication
// that lost the destination race (Rust LiveLineage).
type LiveLineage uint8

const (
	LiveLineageSameGenerationExactBytes LiveLineage = iota
	LiveLineageSameGenerationPhysicalBytesChanged
	LiveLineageAdvancedGeneration
)

// AccessPolicy classifies the creator-only security evidence of one
// destination or coordination artifact (Rust AccessPolicy).
type AccessPolicy uint8

const (
	AccessPolicyAbsent AccessPolicy = iota
	AccessPolicyCreatorOnly
	AccessPolicyChangedOrUnproven
	AccessPolicyUnclassified
)

// CleanupState reports the attempt-artifact state after one operation
// exactly like Rust CleanupState: Clean means either the artifact was
// provably removed or nothing needed removal; ResiduePossible means
// removal was attempted but could not be proved.
type CleanupState uint8

const (
	CleanupStateClean CleanupState = iota
	CleanupStateResiduePossible
)

// ArtifactKind classifies one retained namespace artifact (Rust
// ArtifactKind).
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

// CoordinationCleanup is the coordination residue class of one failed
// operation (Rust CoordinationCleanup): which lock or guard the caller
// must still release.
type CoordinationCleanup uint8

const (
	CoordinationCleanupNone CoordinationCleanup = iota
	CoordinationCleanupCleanupGuard
	CoordinationCleanupRetainedReaderCloseRequired
	CoordinationCleanupRetainedWriterCloseRequired
)

// Housekeeping classifies the abandoned live-sidecar housekeeping
// evidence of one failed publication (Rust Housekeeping).
type Housekeeping uint8

const (
	HousekeepingNone Housekeeping = iota
	HousekeepingCrashReappearancePossible
	HousekeepingVisible
)

// merge folds two housekeeping classes (Rust Housekeeping::merge:
// Visible dominates, then CrashReappearancePossible).
// Merge folds two housekeeping classes (Rust Housekeeping::merge; the
// recovery terminal composes the same operator through this exported
// entry).
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

// UnpublishedTailFacts is the exact unpublished main tail evidence of
// one abandoned artifact (Rust UnpublishedTailFacts).
type UnpublishedTailFacts struct {
	ExpectedDatabaseID           [16]byte
	CommittedTargetTransactionID uint64
	CommittedTargetNonce         [16]byte
	CommittedTargetLength        uint64
	ObservedTailEndExclusive     uint64
}

// CleanupArtifact is one exact unresolved artifact of a publication
// attempt (Rust CleanupArtifact). Error is the fixed publication
// problem of the operation that failed; the artifact facts make the
// removal decision exact.
type CleanupArtifact struct {
	Kind              ArtifactKind
	DirectoryRole     DirectoryRole
	DirectoryIdentity LocalFileIdentity
	BasenameEncoding  uint16
	Basename          []byte
	Identity          *LocalFileIdentity
	CreationSecurity  *CreationSecurity
	UnpublishedTail   *UnpublishedTailFacts
	Error             error
}

// cleanupCapacity is the fixed cleanup ledger bound (Rust
// CLEANUP_CAPACITY).
const cleanupCapacity = 4

// CleanupArtifacts is the fixed cleanup ledger of one publication
// attempt (Rust CleanupArtifacts). The capacity is an invariant of the
// ported machine: pushing a fifth artifact fails the owner contract
// exactly like the Rust assert.
type CleanupArtifacts struct {
	entries [cleanupCapacity]CleanupArtifact
	len     int
}

// newCleanupArtifacts returns the empty ledger (Rust
// CleanupArtifacts::new).
func newCleanupArtifacts() CleanupArtifacts { return CleanupArtifacts{} }

// NewCleanupArtifacts returns the empty ledger for the composing
// owners (Rust CleanupArtifacts::new; the recovery terminal builds its
// ledger through this exported entry).
func NewCleanupArtifacts() CleanupArtifacts { return CleanupArtifacts{} }

// Push appends one artifact (Rust CleanupArtifacts::push); the fixed
// capacity is an invariant of the ported machine, so an overflow
// panics exactly like the Rust assert. The recovery terminal composes
// the ledger through this exported entry.
func (c *CleanupArtifacts) Push(artifact CleanupArtifact) {
	if c.len >= cleanupCapacity {
		panic("fixed cleanup ledger overflow")
	}
	c.entries[c.len] = artifact
	c.len++
}

// Len reports the ledger length (Rust CleanupArtifacts::len).
func (c CleanupArtifacts) Len() int { return c.len }

// Empty reports whether the ledger carries no entry (Rust is_empty).
func (c CleanupArtifacts) Empty() bool { return c.len == 0 }

// State reports the cleanup state of the ledger (Rust
// CleanupArtifacts::state).
func (c CleanupArtifacts) State() CleanupState {
	if c.Empty() {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// At returns the entry at index, or nil past the end (Rust get).
func (c CleanupArtifacts) At(index int) *CleanupArtifact {
	if index < 0 || index >= c.len {
		return nil
	}
	return &c.entries[index]
}

// Slice returns the ledger entries as a read-only view (Rust iter;
// the caller must not retain or mutate the returned slice).
func (c CleanupArtifacts) Slice() []CleanupArtifact {
	return c.entries[:c.len]
}

// PreviousDestination is the exact previous destination evidence that
// a replacement policy is allowed to discard (Rust
// PreviousDestination).
type PreviousDestination struct {
	Identity   LocalFileIdentity
	ByteLength uint64
	SHA512     [64]byte
}

// PublicationAttempt is the exact identity of one reservation attempt
// (Rust PublicationAttempt).
type PublicationAttempt struct {
	DatabaseID                  [16]byte
	TransactionID               uint64
	CommitNonce                 [16]byte
	PublicationAttemptID        [16]byte
	DirectoryIdentity           LocalFileIdentity
	DestinationBasenameEncoding uint16
	DestinationBasename         []byte
	OutputIdentity              LocalFileIdentity
	OutputByteLength            uint64
	OutputSHA512                [64]byte
	PublicationPolicy           PublicationPolicy
	PreviousDestination         *PreviousDestination
	ReservationIdentity         LocalFileIdentity
	CreationSecurity            CreationSecurity
}

// PrivateOutputAttempt is the identity of one private output artifact
// (Rust PrivateOutputAttempt). Identity is the retained inode pair as
// a value with IdentityPresent as its Option presence tag, so the
// facts builders never allocate (Rust Option<LocalFileIdentity> is a
// Copy value).
type PrivateOutputAttempt struct {
	PublicationAttemptID [16]byte
	DirectoryIdentity    LocalFileIdentity
	BasenameEncoding     uint16
	Basename             []byte
	Identity             LocalFileIdentity
	IdentityPresent      bool
	CreationSecurity     CreationSecurity
}

// HousekeepingArtifact is one visible housekeeping artifact of an
// abandoned live sidecar (Rust HousekeepingArtifact).
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

// AbandonedArtifactRemoval is the factual outcome of one exact
// abandoned-artifact removal (Rust AbandonedArtifactRemoval).
type AbandonedArtifactRemoval struct {
	SourcePresent       bool
	CleanupState        CleanupState
	Housekeeping        Housekeeping
	VisibleHousekeeping []HousekeepingArtifact
	Cause               error
}

// PublicationResult is the factual outcome of one publish call (Rust
// PublicationResult). A refusal or unprovable outcome still returns a
// result: Status, Cause, and the cleanup facts classify it exactly
// like Rust.
type PublicationResult struct {
	Attempt                           PublicationAttempt
	MainNamespaceMayHaveBeenAttempted bool
	Publication                       PublicationStatus
	DestinationContent                DestinationContent
	LaterCanonical                    LaterCanonical
	LiveLineage                       *LiveLineage
	LaterAttemptOrSidecarID           *[16]byte
	LaterSelectedTransactionID        *uint64
	LaterSelectedCommitNonce          *[16]byte
	MainAccessPolicy                  AccessPolicy
	CoordinationAccessPolicy          AccessPolicy
	Cleanup                           CleanupArtifacts
	CoordinationCleanup               CoordinationCleanup
	Housekeeping                      Housekeeping
	VisibleHousekeeping               []HousekeepingArtifact
	Cause                             error
}

// CleanupState reports the combined cleanup state of the result (Rust
// PublicationResult::cleanup_state).
func (r *PublicationResult) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// PublicationPreparationFailure classes one failed publication before
// the destination provably held the output (Rust
// PublicationPreparationFailure). Cause carries the fixed problem;
// the artifact facts make the recovery decision exact.
type PublicationPreparationFailure struct {
	PublicationAttemptID          [16]byte
	DirectoryIdentity             LocalFileIdentity
	PrivateOutputBasenameEncoding uint16
	PrivateOutputBasename         []byte
	OutputIdentity                LocalFileIdentity
	CreationSecurity              CreationSecurity
	Cleanup                       CleanupArtifacts
	CoordinationCleanup           CoordinationCleanup
	Housekeeping                  Housekeeping
	VisibleHousekeeping           []HousekeepingArtifact
	Cause                         error
}

// CleanupState reports the combined cleanup state of the failure (Rust
// PublicationPreparationFailure::cleanup_state).
func (f *PublicationPreparationFailure) CleanupState() CleanupState {
	if f.Cleanup.Empty() && f.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// Error implements the error surface of the preparation failure (the
// one-shot staging facade returns it as its error value; the resolver
// facade folds it into the result facts).
func (f *PublicationPreparationFailure) Error() string {
	if f == nil {
		return "<nil>"
	}
	return "iprange v4 publication preparation: " + f.Cause.Error()
}

// Unwrap exposes the cause chain (Rust into_error).
func (f *PublicationPreparationFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Cause
}

// AsProblem reports the fixed publish problem of one error, preserving
// Rust-verbatim codes and details (the facade converts it to the
// public error type; Rust Problem carries code/os_code/detail, Go
// drops os_code).
func AsProblem(err error) *format.Error {
	var fe *format.Error
	if errors.As(err, &fe) {
		return fe
	}
	return sdkProblem(err)
}
