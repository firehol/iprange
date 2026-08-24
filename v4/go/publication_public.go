// Public publication-resolution, canonical-residue, and
// abandoned-artifact maintenance surfaces (Rust publication.rs
// re-exports): resolve completes or removes one interrupted
// publication, the residue inspector classifies and the remover
// cleans the canonical coordination artifact of a destination, and
// the maintenance scanners list and remove the exact private
// publication temps and reservations of one directory in constant
// memory. Every entry point accepts the shared cancellation token.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// FileIdentity is the exact local identity of one retained inode (Rust
// validation::LocalFileIdentity): the platform kind tag (1 = POSIX)
// and the encoded identity bytes (device little-endian, inode
// little-endian, zero padding).
type FileIdentity = publication.LocalFileIdentity

// PublicationResolutionMode is the requested terminal action for one
// exact interrupted publication (Rust PublicationResolutionMode):
// Complete finishes the interrupted publication, Remove removes both
// artifacts.
type PublicationResolutionMode = publication.PublicationResolutionMode

const (
	PublicationResolutionComplete = publication.PublicationResolutionComplete
	PublicationResolutionRemove   = publication.PublicationResolutionRemove
)

// ResolvePublication resolves one interrupted publication using a
// supplied result or the retained reservation (Rust
// publication::resolve_publication): Complete completes the
// interrupted publication, Remove removes the retained artifacts; a
// nil supplied result resolves from the reservation authority alone.
// The returned result classifies the outcome exactly like the
// publication results; the supplied result is never mutated.
func ResolvePublication(path string, supplied *PublicationResult, mode PublicationResolutionMode, cancellation *CancellationToken) (PublicationResult, error) {
	result, err := publication.ResolvePublication(path, supplied, mode, publicationCheck(cancellation))
	if err != nil {
		return PublicationResult{}, publicError(err)
	}
	return result, nil
}

// PublicationResidueCoordination is the current classification of the
// canonical coordination name (Rust PublicationResidueCoordination).
type PublicationResidueCoordination = publication.PublicationResidueCoordination

const (
	PublicationResidueCoordinationAbsent                 = publication.PublicationResidueCoordinationAbsent
	PublicationResidueCoordinationPublicationReservation = publication.PublicationResidueCoordinationPublicationReservation
	PublicationResidueCoordinationLiveSidecar            = publication.PublicationResidueCoordinationLiveSidecar
	PublicationResidueCoordinationUnselectable           = publication.PublicationResidueCoordinationUnselectable
)

// PublicationResidueMainContent classifies one retained destination
// main (Rust PublicationResidueMainContent).
type PublicationResidueMainContent = publication.PublicationResidueMainContent

const (
	PublicationResidueMainContentV4    = publication.PublicationResidueMainContentV4
	PublicationResidueMainContentOther = publication.PublicationResidueMainContentOther
)

// PublicationResidueMain is the stable evidence of one destination
// main that offline removal never changes (Rust
// PublicationResidueMain).
type PublicationResidueMain = publication.PublicationResidueMain

// PublicationResidueHandle is the same-process authority for one
// exact canonical coordination inode (Rust PublicationResidueHandle).
// Remove consumes the authority exactly like the Rust move; Close
// releases the retained descriptors without namespace work. A
// consumed handle must not be reused.
type PublicationResidueHandle = publication.PublicationResidueHandle

// PublicationResidueInspection is one read-only residue inspection
// (Rust PublicationResidueInspection).
type PublicationResidueInspection = publication.PublicationResidueInspection

// PublicationResidueRemoval is the factual result of one offline
// canonical-residue removal attempt (Rust PublicationResidueRemoval).
type PublicationResidueRemoval = publication.PublicationResidueRemoval

// PublicationTuple is the readable meta identity of one v4 main
// (Rust PublicationTuple).
type PublicationTuple = publication.PublicationTuple

// PublicationDigest is the exact complete-file digest evidence of one
// v4 main (Rust PublicationDigest).
type PublicationDigest = publication.PublicationDigest

// InspectPublicationResidue inspects publication residue without
// changing any file or namespace entry (Rust
// inspect_publication_residue): the coordination inode is classified
// and, when selectable, the publication facts are reconstructed; the
// returned handle, when present, is the removal authority.
func InspectPublicationResidue(path string, cancellation *CancellationToken) (*PublicationResidueInspection, error) {
	inspection, err := publication.InspectPublicationResidue(path, publicationCheck(cancellation))
	if err != nil {
		return nil, publicError(err)
	}
	return inspection, nil
}

// RemovePublicationResidue removes one canonical coordination residue
// after the caller certified its quiescence (Rust
// remove_publication_residue). The handle is consumed by the call.
func RemovePublicationResidue(handle *PublicationResidueHandle, cancellation *CancellationToken) (*PublicationResidueRemoval, error) {
	removal, err := publication.RemovePublicationResidue(handle, publicationCheck(cancellation))
	if err != nil {
		return nil, publicError(err)
	}
	return removal, nil
}

// AbandonedReservationPolicy is the publication policy recorded in
// one authenticated private reservation (Rust
// AbandonedReservationPolicy).
type AbandonedReservationPolicy = publication.AbandonedReservationPolicy

const (
	AbandonedReservationPolicyFailIfExists              = publication.AbandonedReservationPolicyFailIfExists
	AbandonedReservationPolicyReplaceExisting           = publication.AbandonedReservationPolicyReplaceExisting
	AbandonedReservationPolicyReplaceExistingNoRollback = publication.AbandonedReservationPolicyReplaceExistingNoRollback
)

// AbandonedReservationPhase is the durable namespace phase of one
// private reservation (Rust AbandonedReservationPhase).
type AbandonedReservationPhase = publication.AbandonedReservationPhase

const (
	AbandonedReservationPhasePrepared                 = publication.AbandonedReservationPhasePrepared
	AbandonedReservationPhaseMainMayHaveBeenAttempted = publication.AbandonedReservationPhaseMainMayHaveBeenAttempted
)

// PublicationOutputEvidence is the exact attempted-output identity
// and content evidence of one reservation (Rust
// PublicationOutputEvidence).
type PublicationOutputEvidence = publication.PublicationOutputEvidence

// AbandonedReservationPrevious is the exact previous-destination
// evidence of one replacement reservation (Rust evidence.previous).
type AbandonedReservationPrevious = publication.AbandonedReservationPrevious

// AbandonedReservationEvidence is the authenticated fields of one
// selectable private reservation (Rust AbandonedReservationEvidence).
type AbandonedReservationEvidence = publication.AbandonedReservationEvidence

// AbandonedReservationEntry is one stable exact-pattern private
// reservation (Rust AbandonedReservationEntry).
type AbandonedReservationEntry = publication.AbandonedReservationEntry

// AbandonedReservationList is one completed constant-memory private
// reservation scan (Rust AbandonedReservationList).
type AbandonedReservationList = publication.AbandonedReservationList

// AbandonedPublicationTempEntry is one stable exact-pattern private
// publication output (Rust AbandonedPublicationTempEntry).
type AbandonedPublicationTempEntry = publication.AbandonedPublicationTempEntry

// AbandonedPublicationTempList is one completed constant-memory
// private-output scan (Rust AbandonedPublicationTempList).
type AbandonedPublicationTempList = publication.AbandonedPublicationTempList

// AbandonedArtifactRemoval is the factual outcome of one exact
// abandoned-artifact removal (Rust AbandonedArtifactRemoval).
type AbandonedArtifactRemoval = publication.AbandonedArtifactRemoval

// ErrMaintenanceSinkStop stops one abandonment listing scan (Rust
// *SinkControl::Stop): returning it from the sink ends the scan with
// the StoppedBySink class.
var ErrMaintenanceSinkStop = publication.ErrMaintenanceSinkStop

// ListAbandonedPublicationTemps lists the stable no-follow regular
// private publication outputs of one directory in constant memory
// (Rust list_abandoned_publication_temps). The sink receives every
// entry; returning ErrMaintenanceSinkStop stops the scan.
func ListAbandonedPublicationTemps(directory string, cancellation *CancellationToken, sink func(entry *AbandonedPublicationTempEntry) error) (AbandonedPublicationTempList, error) {
	list, err := publication.ListAbandonedPublicationTemps(directory, publicationCheck(cancellation), sink)
	if err != nil {
		return AbandonedPublicationTempList{}, publicError(err)
	}
	return list, nil
}

// RemoveAbandonedPublicationTemp removes one exact private output
// after caller-certified quiescence (Rust
// remove_abandoned_publication_temp): readable content requires the
// exact tuple and digest evidence, partial content requires both
// absent.
func RemoveAbandonedPublicationTemp(directory string, expectedDirectory FileIdentity, attempt [16]byte, expectedArtifact FileIdentity, expectedTuple *PublicationTuple, expectedDigest *PublicationDigest, cancellation *CancellationToken) (AbandonedArtifactRemoval, error) {
	removal, err := publication.RemoveAbandonedPublicationTemp(directory, expectedDirectory, attempt, expectedArtifact, expectedTuple, expectedDigest, publicationCheck(cancellation))
	if err != nil {
		return AbandonedArtifactRemoval{}, publicError(err)
	}
	return removal, nil
}

// ListAbandonedReservationArtifacts lists the stable no-follow
// regular private publication reservations of one directory in
// constant memory (Rust list_abandoned_reservation_artifacts). The
// sink receives every entry; returning ErrMaintenanceSinkStop stops
// the scan.
func ListAbandonedReservationArtifacts(directory string, cancellation *CancellationToken, sink func(entry *AbandonedReservationEntry) error) (AbandonedReservationList, error) {
	list, err := publication.ListAbandonedReservationArtifacts(directory, publicationCheck(cancellation), sink)
	if err != nil {
		return AbandonedReservationList{}, publicError(err)
	}
	return list, nil
}

// RemoveAbandonedReservationArtifact removes one exact private
// reservation after caller-certified quiescence (Rust
// remove_abandoned_reservation_artifact).
func RemoveAbandonedReservationArtifact(directory string, expectedDirectory FileIdentity, attempt [16]byte, expectedArtifact FileIdentity, cancellation *CancellationToken) (AbandonedArtifactRemoval, error) {
	removal, err := publication.RemoveAbandonedReservationArtifact(directory, expectedDirectory, attempt, expectedArtifact, publicationCheck(cancellation))
	if err != nil {
		return AbandonedArtifactRemoval{}, publicError(err)
	}
	return removal, nil
}

// publicationCheck adapts the SDK cancellation token to the
// publication machine check contract: the machine classifies
// checkpoint failures by the format problem surface (unknown errors
// fold to the SDK IO class), so the cancelled state converts to the
// exact Cancelled problem instead of being collapsed. A nil token
// stays uncancellable.
func publicationCheck(token *CancellationToken) func() error {
	if token == nil {
		return nil
	}
	return func() error {
		if token.IsCancelled() {
			return &format.Error{Code: format.CodeCancelled, Detail: "operation was cancelled"}
		}
		return nil
	}
}
