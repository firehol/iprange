//go:build !windows

// Public boundary surface of the publication machine (Rust
// publication.rs re-exports): interrupted-publication resolution,
// canonical residue inspection and removal, and abandoned-artifact
// maintenance. The entry points wrap the internal machine and map its
// facts to the portable public shapes; the SDK facade in the root
// package aliases these types and folds the cancellation token.

package publication

import "github.com/firehol/iprange/v4/go/internal/format"

// PublicationResolutionMode is the requested terminal action for one
// exact interrupted publication (Rust PublicationResolutionMode):
// Complete finishes the interrupted publication, Remove removes both
// artifacts.
type PublicationResolutionMode uint8

const (
	PublicationResolutionComplete PublicationResolutionMode = iota
	PublicationResolutionRemove
)

// ResolvePublication resolves one interrupted publication using a
// supplied result or the retained reservation (Rust
// publication::resolve_publication): Complete completes the
// interrupted publication, Remove removes the retained artifacts; a
// nil supplied result resolves from the reservation authority alone.
// The returned result classifies the outcome exactly like the
// machine results; the supplied result is never mutated.
func ResolvePublication(path string, supplied *PublicationResult, mode PublicationResolutionMode, check func() error) (PublicationResult, error) {
	internalMode := resolveModeComplete
	if mode == PublicationResolutionRemove {
		internalMode = resolveModeRemove
	}
	return resolve(path, supplied, internalMode, check)
}

// PublicationTuple is the readable meta identity of one v4 main
// (Rust PublicationTuple).
type PublicationTuple struct {
	DatabaseID    [16]byte
	TransactionID uint64
	CommitNonce   [16]byte
}

// PublicationDigest is the exact complete-file digest evidence of one
// v4 main (Rust PublicationDigest).
type PublicationDigest struct {
	ByteLength uint64
	SHA512     [64]byte
}

// PublicationResidueCoordination is the current classification of the
// canonical coordination name (Rust PublicationResidueCoordination).
type PublicationResidueCoordination uint8

const (
	PublicationResidueCoordinationAbsent PublicationResidueCoordination = iota
	PublicationResidueCoordinationPublicationReservation
	PublicationResidueCoordinationLiveSidecar
	PublicationResidueCoordinationUnselectable
)

// PublicationResidueMainContent classifies one retained destination
// main (Rust PublicationResidueMainContent).
type PublicationResidueMainContent uint8

const (
	PublicationResidueMainContentV4 PublicationResidueMainContent = iota
	PublicationResidueMainContentOther
)

// PublicationResidueMain is the stable evidence of one destination
// main that offline removal never changes (Rust
// PublicationResidueMain).
type PublicationResidueMain struct {
	Identity     LocalFileIdentity
	Content      PublicationResidueMainContent
	Tuple        *PublicationTuple
	Digest       PublicationDigest
	AccessPolicy AccessPolicy
}

// PublicationResidueHandle is the same-process authority for one
// exact canonical coordination inode (Rust PublicationResidueHandle):
// removeResidue consumes the authority exactly like the Rust move, and
// Close releases the retained descriptors without transferring a
// cleanup obligation (Rust close). A consumed handle must not be
// reused.
type PublicationResidueHandle struct {
	inner *residueHandle
}

// Close releases the retained descriptors of one residue authority
// without namespace work (Rust PublicationResidueHandle::close). The
// handle is consumed.
func (h *PublicationResidueHandle) Close() {
	if h == nil || h.inner == nil {
		return
	}
	closeResidueAuthority(h.inner)
	h.inner = nil
}

// Remove removes the canonical coordination residue after certified
// quiescence (Rust remove_publication_residue). The handle is
// consumed; a removal that still carries residual authority returns
// it in the removal result for the caller's retry.
func (h *PublicationResidueHandle) Remove(check func() error) (*PublicationResidueRemoval, error) {
	if h == nil || h.inner == nil {
		return nil, problem(format.CodeInvalidArgument, "publication residue handle is already consumed")
	}
	removal, err := removeResidue(*h.inner, check)
	h.inner = nil
	if err != nil {
		return nil, err
	}
	return mapResidueRemoval(removal), nil
}

// PublicationResidueInspection is one read-only residue inspection
// (Rust PublicationResidueInspection). The handle, when present,
// authorizes the removal of the inspected coordination inode.
type PublicationResidueInspection struct {
	DirectoryIdentity    LocalFileIdentity
	CoordinationIdentity *LocalFileIdentity
	Coordination         PublicationResidueCoordination
	Publication          *PublicationResult
	Handle               *PublicationResidueHandle
}

// PublicationResidueRemoval is the factual result of one offline
// canonical-residue removal attempt (Rust PublicationResidueRemoval).
// Cause classifies the refused or incomplete removal; the cleanup
// facts and the residual handle (when the removal still holds
// authority) complete the recovery decision.
type PublicationResidueRemoval struct {
	DirectoryIdentity        LocalFileIdentity
	CoordinationIdentity     LocalFileIdentity
	Main                     *PublicationResidueMain
	LaterCoordination        PublicationResidueCoordination
	CoordinationAccessPolicy AccessPolicy
	Cleanup                  CleanupArtifacts
	CoordinationCleanup      CoordinationCleanup
	Housekeeping             Housekeeping
	VisibleHousekeeping      []HousekeepingArtifact
	Handle                   *PublicationResidueHandle
	Cause                    error
}

// CleanupState reports the combined cleanup state of one removal
// (Rust PublicationResidueRemoval::cleanup_state).
func (r *PublicationResidueRemoval) CleanupState() CleanupState {
	if r.Cleanup.Empty() && r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// InspectPublicationResidue inspects publication residue without
// changing any file or namespace entry (Rust
// inspect_publication_residue): the coordination inode is classified
// and, when selectable, the publication facts are reconstructed; the
// returned handle, when present, is the removal authority.
func InspectPublicationResidue(path string, check func() error) (*PublicationResidueInspection, error) {
	inspection, err := inspectResidue(path, check)
	if err != nil {
		return nil, err
	}
	out := &PublicationResidueInspection{
		DirectoryIdentity: inspection.directoryIdentity,
		Coordination:      mapResidueCoordination(inspection.coordination),
		Publication:       inspection.publication,
	}
	if inspection.coordinationIdentity != nil {
		identity := *inspection.coordinationIdentity
		out.CoordinationIdentity = &identity
	}
	if inspection.handle != nil {
		out.Handle = &PublicationResidueHandle{inner: inspection.handle}
	}
	return out, nil
}

// RemovePublicationResidue removes one canonical coordination residue
// after the caller certified its quiescence (Rust
// remove_publication_residue). The handle is consumed by the call.
func RemovePublicationResidue(handle *PublicationResidueHandle, check func() error) (*PublicationResidueRemoval, error) {
	return handle.Remove(check)
}

// AbandonedReservationPolicy is the publication policy recorded in
// one authenticated private reservation (Rust
// AbandonedReservationPolicy).
type AbandonedReservationPolicy uint8

const (
	AbandonedReservationPolicyFailIfExists AbandonedReservationPolicy = iota
	AbandonedReservationPolicyReplaceExisting
	AbandonedReservationPolicyReplaceExistingNoRollback
)

// AbandonedReservationPhase is the durable namespace phase of one
// private reservation (Rust AbandonedReservationPhase).
type AbandonedReservationPhase uint8

const (
	AbandonedReservationPhasePrepared AbandonedReservationPhase = iota
	AbandonedReservationPhaseMainMayHaveBeenAttempted
)

// PublicationOutputEvidence is the exact attempted-output identity
// and content evidence of one reservation (Rust
// PublicationOutputEvidence).
type PublicationOutputEvidence struct {
	Identity LocalFileIdentity
	Tuple    PublicationTuple
	Digest   PublicationDigest
}

// AbandonedReservationPrevious is the exact previous-destination
// evidence of one replacement reservation (Rust evidence.previous:
// Option<(LocalFileIdentity, PublicationDigest)>).
type AbandonedReservationPrevious struct {
	Identity LocalFileIdentity
	Digest   PublicationDigest
}

// AbandonedReservationEvidence is the authenticated fields of one
// selectable private reservation (Rust AbandonedReservationEvidence).
type AbandonedReservationEvidence struct {
	Policy   AbandonedReservationPolicy
	Phase    AbandonedReservationPhase
	Output   PublicationOutputEvidence
	Previous *AbandonedReservationPrevious
}

// AbandonedReservationEntry is one stable exact-pattern private
// reservation (Rust AbandonedReservationEntry).
type AbandonedReservationEntry struct {
	DirectoryIdentity    LocalFileIdentity
	ArtifactIdentity     LocalFileIdentity
	PublicationAttemptID [16]byte
	Evidence             *AbandonedReservationEvidence
}

// AbandonedReservationList is one completed constant-memory private
// reservation scan (Rust AbandonedReservationList).
type AbandonedReservationList struct {
	DirectoryIdentity LocalFileIdentity
	Entries           uint64
}

// AbandonedPublicationTempEntry is one stable exact-pattern private
// publication output (Rust AbandonedPublicationTempEntry). Tuple and
// digest are both present or both absent (readable evidence).
type AbandonedPublicationTempEntry struct {
	DirectoryIdentity    LocalFileIdentity
	ArtifactIdentity     LocalFileIdentity
	PublicationAttemptID [16]byte
	Tuple                *PublicationTuple
	Digest               *PublicationDigest
}

// AbandonedPublicationTempList is one completed constant-memory
// private-output scan (Rust AbandonedPublicationTempList).
type AbandonedPublicationTempList struct {
	DirectoryIdentity LocalFileIdentity
	Entries           uint64
}

// ErrMaintenanceSinkStop stops one abandonment listing scan (Rust
// *SinkControl::Stop): returning it from the sink ends the scan with
// the StoppedBySink class.
var ErrMaintenanceSinkStop = errMaintenanceSinkStop

// ListAbandonedPublicationTemps lists the stable no-follow regular
// private publication outputs of one directory in constant memory
// (Rust list_abandoned_publication_temps). The sink receives every
// entry; returning ErrMaintenanceSinkStop stops the scan.
func ListAbandonedPublicationTemps(path string, check func() error, sink func(entry *AbandonedPublicationTempEntry) error) (AbandonedPublicationTempList, error) {
	list, err := listAbandonedPublicationTemps(path, check, func(entry *abandonedPublicationTempEntry) error {
		return sink(mapAbandonedPublicationTempEntry(entry))
	})
	if err != nil {
		return AbandonedPublicationTempList{}, err
	}
	return AbandonedPublicationTempList{DirectoryIdentity: list.directoryIdentity, Entries: list.entries}, nil
}

// RemoveAbandonedPublicationTemp removes one exact private output
// after caller-certified quiescence (Rust
// remove_abandoned_publication_temp): readable content requires the
// exact tuple and digest evidence, partial content requires both
// absent.
func RemoveAbandonedPublicationTemp(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, expectedArtifact LocalFileIdentity, expectedTuple *PublicationTuple, expectedDigest *PublicationDigest, check func() error) (AbandonedArtifactRemoval, error) {
	var tuple *residueTuple
	var digest *residueDigest
	if expectedTuple != nil {
		tuple = &residueTuple{
			databaseID:    expectedTuple.DatabaseID,
			transactionID: expectedTuple.TransactionID,
			commitNonce:   expectedTuple.CommitNonce,
		}
	}
	if expectedDigest != nil {
		digest = &residueDigest{byteLength: expectedDigest.ByteLength, sha512: expectedDigest.SHA512}
	}
	return removeAbandonedPublicationTemp(path, expectedDirectory, attempt, expectedArtifact, tuple, digest, check)
}

// ListAbandonedReservationArtifacts lists the stable no-follow
// regular private publication reservations of one directory in
// constant memory (Rust list_abandoned_reservation_artifacts). The
// sink receives every entry; returning ErrMaintenanceSinkStop stops
// the scan.
func ListAbandonedReservationArtifacts(path string, check func() error, sink func(entry *AbandonedReservationEntry) error) (AbandonedReservationList, error) {
	list, err := listAbandonedReservationArtifacts(path, check, func(entry *abandonedReservationEntry) error {
		return sink(mapAbandonedReservationEntry(entry))
	})
	if err != nil {
		return AbandonedReservationList{}, err
	}
	return AbandonedReservationList{DirectoryIdentity: list.directoryIdentity, Entries: list.entries}, nil
}

// RemoveAbandonedReservationArtifact removes one exact private
// reservation after caller-certified quiescence (Rust
// remove_abandoned_reservation_artifact).
func RemoveAbandonedReservationArtifact(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, expectedArtifact LocalFileIdentity, check func() error) (AbandonedArtifactRemoval, error) {
	return removeAbandonedReservationArtifact(path, expectedDirectory, attempt, expectedArtifact, check)
}

// mapResidueCoordination converts the internal coordination class.
func mapResidueCoordination(value residueCoordination) PublicationResidueCoordination {
	switch value {
	case residueCoordinationAbsent:
		return PublicationResidueCoordinationAbsent
	case residueCoordinationPublicationReservation:
		return PublicationResidueCoordinationPublicationReservation
	case residueCoordinationLiveSidecar:
		return PublicationResidueCoordinationLiveSidecar
	default:
		return PublicationResidueCoordinationUnselectable
	}
}

func mapPublicationResidueCoordination(value PublicationResidueCoordination) residueCoordination {
	switch value {
	case PublicationResidueCoordinationAbsent:
		return residueCoordinationAbsent
	case PublicationResidueCoordinationPublicationReservation:
		return residueCoordinationPublicationReservation
	case PublicationResidueCoordinationLiveSidecar:
		return residueCoordinationLiveSidecar
	default:
		return residueCoordinationUnselectable
	}
}

func mapResidueMainContent(value residueMainContent) PublicationResidueMainContent {
	if value == residueMainContentV4 {
		return PublicationResidueMainContentV4
	}
	return PublicationResidueMainContentOther
}

func mapPublicationTuple(value residueTuple) *PublicationTuple {
	return &PublicationTuple{
		DatabaseID:    value.databaseID,
		TransactionID: value.transactionID,
		CommitNonce:   value.commitNonce,
	}
}

// residueTupleToPublication converts one readable tuple value (the
// output-evidence conversion).
func residueTupleToPublication(value residueTuple) PublicationTuple {
	return PublicationTuple{
		DatabaseID:    value.databaseID,
		TransactionID: value.transactionID,
		CommitNonce:   value.commitNonce,
	}
}

func mapPublicationDigest(value residueDigest) PublicationDigest {
	return PublicationDigest{ByteLength: value.byteLength, SHA512: value.sha512}
}

// mapResidueMain converts one retained main evidence (nil stays nil).
func mapResidueMain(main *residueMain) *PublicationResidueMain {
	if main == nil {
		return nil
	}
	out := &PublicationResidueMain{
		Identity:     main.identity,
		Content:      mapResidueMainContent(main.content),
		Digest:       mapPublicationDigest(main.digest),
		AccessPolicy: main.accessPolicy,
	}
	if main.tuple != nil {
		out.Tuple = mapPublicationTuple(*main.tuple)
	}
	return out
}

// mapResidueRemoval converts one removal result; a residual authority
// inside the internal removal becomes the exported handle of the
// result.
func mapResidueRemoval(removal residueRemoval) *PublicationResidueRemoval {
	out := &PublicationResidueRemoval{
		DirectoryIdentity:        removal.directoryIdentity,
		CoordinationIdentity:     removal.coordinationIdentity,
		Main:                     mapResidueMain(removal.main),
		LaterCoordination:        mapResidueCoordination(removal.laterCoordination),
		CoordinationAccessPolicy: removal.coordinationAccessPolicy,
		Cleanup:                  removal.cleanup,
		CoordinationCleanup:      removal.coordinationCleanup,
		Housekeeping:             removal.housekeeping,
		VisibleHousekeeping:      removal.visibleHousekeeping,
		Cause:                    removal.cause,
	}
	if removal.handle != nil {
		out.Handle = &PublicationResidueHandle{inner: removal.handle}
	}
	return out
}

// mapAbandonedPublicationTempEntry converts one listed private output.
func mapAbandonedPublicationTempEntry(entry *abandonedPublicationTempEntry) *AbandonedPublicationTempEntry {
	out := &AbandonedPublicationTempEntry{
		DirectoryIdentity:    entry.directoryIdentity,
		ArtifactIdentity:     entry.artifactIdentity,
		PublicationAttemptID: entry.attempt,
	}
	if entry.tuple != nil {
		out.Tuple = mapPublicationTuple(*entry.tuple)
	}
	if entry.digest != nil {
		digest := mapPublicationDigest(*entry.digest)
		out.Digest = &digest
	}
	return out
}

// mapAbandonedReservationEntry converts one listed private
// reservation.
func mapAbandonedReservationEntry(entry *abandonedReservationEntry) *AbandonedReservationEntry {
	out := &AbandonedReservationEntry{
		DirectoryIdentity:    entry.directoryIdentity,
		ArtifactIdentity:     entry.artifactIdentity,
		PublicationAttemptID: entry.attempt,
	}
	if entry.evidence != nil {
		out.Evidence = &AbandonedReservationEvidence{
			Policy: AbandonedReservationPolicy(entry.evidence.policy),
			Phase:  AbandonedReservationPhase(entry.evidence.phase),
			Output: PublicationOutputEvidence{
				Identity: entry.evidence.output.identity,
				Tuple:    residueTupleToPublication(entry.evidence.output.tuple),
				Digest:   mapPublicationDigest(entry.evidence.output.digest),
			},
		}
		if entry.evidence.previous != nil {
			out.Evidence.Previous = &AbandonedReservationPrevious{
				Identity: entry.evidence.previous.identity,
				Digest:   mapPublicationDigest(entry.evidence.previous.digest),
			}
		}
	}
	return out
}
