//go:build !windows

// Explicit inspection and offline removal of canonical publication
// residue (Rust publication/residue.rs + residue/linux.rs): the
// coordination inode is classified (absent, a selectable publication
// reservation, a live reader-table sidecar, or unselectable residue),
// an unselectable coordination is removed only after the operation
// lock, the selectable refusals, and the retained-handle proofs, and
// the destination main is hashed but never changed. The Rust
// gc_barrier availability calls are #[cfg(windows)] and absent here
// like every other Go publication surface (SOW-0026 refuses Windows opens
// at destination bind); the sidecar header read that only feeds the
// barrier is omitted for the same reason.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// residueCoordination is the current classification of the canonical
// coordination name (Rust PublicationResidueCoordination).
type residueCoordination uint8

const (
	residueCoordinationAbsent residueCoordination = iota
	residueCoordinationPublicationReservation
	residueCoordinationLiveSidecar
	residueCoordinationUnselectable
)

// residueMainContent classifies one retained destination main (Rust
// PublicationResidueMainContent).
type residueMainContent uint8

const (
	residueMainContentV4 residueMainContent = iota
	residueMainContentOther
)

// residueTuple is the readable meta identity of one v4 main (Rust
// PublicationTuple).
type residueTuple struct {
	databaseID    [16]byte
	transactionID uint64
	commitNonce   [16]byte
}

// residueDigest is the offline evidence digest of one main (Rust
// PublicationDigest).
type residueDigest struct {
	byteLength uint64
	sha512     [64]byte
}

// residueMain is the stable evidence of a main that offline removal
// never changes (Rust PublicationResidueMain).
type residueMain struct {
	identity     LocalFileIdentity
	content      residueMainContent
	tuple        *residueTuple
	digest       residueDigest
	accessPolicy AccessPolicy
}

// residueHandle is the same-process authority for one exact
// canonical coordination inode (Rust PublicationResidueHandle +
// residue::linux::Handle). removeResidue consumes the handle: the
// removal either closes the retained descriptors or transfers the
// residual authority into the returned removal result's handle; the
// caller's inspected copy must not be reused after the call (Rust
// move semantics; Go has no move, so the consumed copy stays
// aliased to closed descriptors).
type residueHandle struct {
	destination          *destination
	coordination         *os.File
	coordinationIdentity live.FileIdentity
	retired              *retiredResidue
}

// retiredResidue is the retained state after the coordination
// unlink succeeded (Rust Retired).
type retiredResidue struct {
	main         *residueMainGuard
	housekeeping Housekeeping
	visible      []HousekeepingArtifact
}

// residueInspection is one read-only residue inspection (Rust
// PublicationResidueInspection).
type residueInspection struct {
	directoryIdentity    LocalFileIdentity
	coordinationIdentity *LocalFileIdentity
	coordination         residueCoordination
	publication          *PublicationResult
	handle               *residueHandle
}

// residueRemoval is the factual result of one offline
// canonical-residue removal attempt (Rust PublicationResidueRemoval).
type residueRemoval struct {
	directoryIdentity        LocalFileIdentity
	coordinationIdentity     LocalFileIdentity
	main                     *residueMain
	laterCoordination        residueCoordination
	coordinationAccessPolicy AccessPolicy
	cleanup                  CleanupArtifacts
	coordinationCleanup      CoordinationCleanup
	housekeeping             Housekeeping
	visibleHousekeeping      []HousekeepingArtifact
	handle                   *residueHandle
	cause                    error
}

// cleanupState reports the cleanup class of one removal (Rust
// PublicationResidueRemoval::cleanup_state).
func (r *residueRemoval) cleanupState() CleanupState {
	if r.cleanup.Empty() && r.coordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// inspectResidue inspects publication residue without changing any
// file or namespace entry (Rust inspect_publication_residue).
func inspectResidue(path string, check func() error) (residueInspection, error) {
	if err := live.Checkpoint(check); err != nil {
		return residueInspection{}, sdkProblem(err)
	}
	destination, err := bindDestination(path)
	if err != nil {
		return residueInspection{}, namespaceProblem(err)
	}
	directoryIdentity := directoryLocalIdentity(destination)
	regular, err := destination.directory().OpenRegular(destination.coordinationName(), true)
	if err != nil {
		destination.directory().Close()
		return residueInspection{}, namespaceProblem(err)
	}
	if regular == nil {
		publication, err := inspectAbsentResidue(destination, check)
		destination.directory().Close()
		if err != nil {
			return residueInspection{}, err
		}
		return residueInspection{
			directoryIdentity:    directoryIdentity,
			coordinationIdentity: nil,
			coordination:         residueCoordinationAbsent,
			publication:          publication,
			handle:               nil,
		}, nil
	}
	if err := destination.directory().VerifyName(destination.coordinationName(), regular.Identity); err != nil {
		_ = regular.File.Close()
		destination.directory().Close()
		return residueInspection{}, namespaceProblem(err)
	}
	coordination, publication, err := classifyCoordinationResidue(destination, regular)
	if err != nil {
		_ = regular.File.Close()
		destination.directory().Close()
		return residueInspection{}, err
	}
	if err := destination.directory().Verify(); err != nil {
		_ = regular.File.Close()
		destination.directory().Close()
		return residueInspection{}, namespaceProblem(err)
	}
	if err := destination.directory().VerifyName(destination.coordinationName(), regular.Identity); err != nil {
		_ = regular.File.Close()
		destination.directory().Close()
		return residueInspection{}, namespaceProblem(err)
	}
	identity := residueLocalIdentity(&regular.Identity)
	return residueInspection{
		directoryIdentity:    directoryIdentity,
		coordinationIdentity: &identity,
		coordination:         coordination,
		publication:          publication,
		handle: &residueHandle{
			destination:          destination,
			coordination:         regular.File,
			coordinationIdentity: regular.Identity,
			retired:              nil,
		},
	}, nil
}

// inspectAbsentResidue reconstructs the publication evidence from the
// private reservation scan when no coordination inode exists (Rust
// inspect's absent arm).
func inspectAbsentResidue(destination *destination, check func() error) (*PublicationResult, error) {
	discovered, err := discoverReservation(destination, check)
	if err != nil {
		return nil, err
	}
	if discovered == nil {
		return nil, nil
	}
	publication, err := reconstructResidue(destination, discovered.header, discovered.access)
	if err != nil {
		_ = discovered.Close()
		return nil, err
	}
	_ = discovered.Close()
	return &publication, nil
}

// removeResidue removes one unselectable canonical coordination inode
// after certified quiescence (Rust remove_publication_residue).
func removeResidue(handle residueHandle, check func() error) (residueRemoval, error) {
	if err := live.Checkpoint(check); err != nil {
		if handle.retired != nil && handle.retired.main != nil {
			handle.retired.main.Close()
		}
		closeResidueAuthority(&handle)
		return residueRemoval{}, sdkProblem(err)
	}
	if handle.retired != nil {
		return finishRetiredResidue(handle), nil
	}
	if err := verifyCoordinationResidue(&handle); err != nil {
		closeResidueAuthority(&handle)
		return residueRemoval{}, err
	}
	if err := live.LockFileCancellable(handle.coordination, reservationOperationLock, live.LockExclusive, check); err != nil {
		closeResidueAuthority(&handle)
		return residueRemoval{}, sdkProblem(err)
	}
	if err := verifyCoordinationResidue(&handle); err != nil {
		closeResidueAuthority(&handle)
		return residueRemoval{}, err
	}
	if err := rejectSelectableResidue(handle.coordination); err != nil {
		closeResidueAuthority(&handle)
		return residueRemoval{}, err
	}
	main, err := inspectMainResidue(handle.destination, check)
	if err != nil {
		closeResidueAuthority(&handle)
		return residueRemoval{}, err
	}
	if err := live.Checkpoint(check); err != nil {
		if main != nil {
			main.Close()
		}
		closeResidueAuthority(&handle)
		return residueRemoval{}, sdkProblem(err)
	}
	retired, err := retireResidueCoordination(handle.destination, handle.coordination, handle.coordinationIdentity)
	if err != nil {
		if main != nil {
			main.Close()
		}
		closeResidueAuthority(&handle)
		return residueRemoval{}, err
	}
	handle.retired = &retiredResidue{
		main:         main,
		housekeeping: retired.housekeeping,
		visible:      retired.visible,
	}
	if retired.cause != nil {
		return incompleteResidue(handle, retired.cause), nil
	}
	return finishRetiredResidue(handle), nil
}

// finishRetiredResidue completes a retired removal: the retirement
// retry proof, the main proof, the final coordination-reuse
// classification, and the portable facts (Rust finish_retired). The
// coordination file closes exactly when the consumed handle would
// drop in Rust: on success here, and on every error return of
// removeResidue; the retained-handle incomplete results keep it open
// for the caller's retry.
func finishRetiredResidue(handle residueHandle) residueRemoval {
	if cause := retryRetiredResidueHandle(&handle); cause != nil {
		return incompleteResidue(handle, cause)
	}
	main := handle.retired.main
	later, err := finishRemovalResidue(&handle, main)
	if err != nil {
		return incompleteResidue(handle, err)
	}
	directoryIdentity := directoryLocalIdentity(handle.destination)
	coordinationIdentity := residueLocalIdentity(&handle.coordinationIdentity)
	var evidence *residueMain
	if main != nil {
		evidence = &main.evidence
	}
	retired := handle.retired
	result := residueRemoval{
		directoryIdentity:        directoryIdentity,
		coordinationIdentity:     coordinationIdentity,
		main:                     evidence,
		laterCoordination:        later.kind,
		coordinationAccessPolicy: later.access,
		cleanup:                  newCleanupArtifacts(),
		coordinationCleanup:      CoordinationCleanupNone,
		housekeeping:             retired.housekeeping,
		visibleHousekeeping:      retired.visible,
		handle:                   nil,
		cause:                    nil,
	}
	if main != nil {
		main.Close()
	}
	closeResidueAuthority(&handle)
	return result
}

// closeResidueAuthority releases the coordination descriptor and the
// retained destination directory of one residue handle (Rust drops
// the handle at the end of every remove terminal that does not return
// it for the caller's retry).
func closeResidueAuthority(handle *residueHandle) {
	if handle.coordination != nil {
		_ = handle.coordination.Close()
		handle.coordination = nil
	}
	handle.destination.directory().Close()
}

// retryRetiredResidueHandle re-proves the unlink of one retired
// handle after the directory synchronization (Rust retry_retirement:
// the platform arm checks the retained coordination link count, and
// the housekeeping evidence merges into the retained state).
func retryRetiredResidueHandle(handle *residueHandle) error {
	retired := handle.retired
	retried := retryResidueRetirement(handle.coordination)
	retired.housekeeping = retired.housekeeping.merge(retried.housekeeping)
	retired.visible = append(retired.visible, retried.visible...)
	return retried.cause
}

// classifyCoordinationResidue classifies one opened coordination
// inode (Rust classify_coordination): a bound selectable reservation
// reconstructs its publication, a selectable live sidecar reports the
// LiveSidecar class, anything else is unselectable residue.
func classifyCoordinationResidue(destination *destination, regular *live.RegularFile) (residueCoordination, *PublicationResult, error) {
	if header, ok, err := selectedBoundResidueHeader(destination, regular); err != nil {
		return 0, nil, err
	} else if ok {
		// The gc-barrier availability call is Windows-only and
		// absent here.
		access := reservationAccessResidue(regular, header)
		publication, err := reconstructResidue(destination, header, access)
		if err != nil {
			return 0, nil, err
		}
		return residueCoordinationPublicationReservation, &publication, nil
	}
	// The live-sidecar header read feeds only the Windows gc barrier
	// and is omitted here like every other Go publication surface.
	selectable, err := live.HasSelectableHeader(regular.File)
	if err != nil {
		return 0, nil, sdkProblem(err)
	}
	if selectable {
		return residueCoordinationLiveSidecar, nil, nil
	}
	return residueCoordinationUnselectable, nil, nil
}

// selectedBoundResidueHeader selects the reservation record of one
// complete coordination-shaped file and proves its binding (Rust
// selected_bound_header; a wrong-size or unselectable file yields no
// header, not an error).
func selectedBoundResidueHeader(destination *destination, regular *live.RegularFile) (reservationHeader, bool, error) {
	mapped, ok, err := reservationMappingResidue(regular.File)
	if err != nil {
		return reservationHeader{}, false, err
	}
	if !ok {
		return reservationHeader{}, false, nil
	}
	defer mapped.Close()
	bytes, err := mapped.View(0, reservationFileSize)
	if err != nil {
		return reservationHeader{}, false, sdkProblem(err)
	}
	selected, err := selectReservation(bytes)
	if err != nil {
		return reservationHeader{}, false, nil
	}
	if err := requireBound(destination, selected.header, regular.Identity, nil); err != nil {
		return reservationHeader{}, false, nil
	}
	return selected.header, true, nil
}

// reservationMappingResidue maps one coordination-sized file
// read-only (Rust residue::linux::reservation_mapping: a wrong size
// is the None marker, a failed map is an sdk problem).
func reservationMappingResidue(file *os.File) (*mapping.Mapping, bool, error) {
	st, err := file.Stat()
	if err != nil {
		return nil, false, sdkProblem(&format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()})
	}
	if uint64(st.Size()) != reservationFileSize {
		return nil, false, nil
	}
	mapped, err := mapping.MapFile(file, reservationFileSize, false)
	if err != nil {
		return nil, false, sdkProblem(err)
	}
	return mapped, true, nil
}

// reconstructResidue builds the publication evidence of one
// selectable reservation (Rust reconstruct: prepared state reports
// NotPublished, anything else OutcomeUnknown).
func reconstructResidue(destination *destination, header reservationHeader, coordinationAccess AccessPolicy) (PublicationResult, error) {
	s, err := reconstructSeed(destination, header)
	if err != nil {
		return PublicationResult{}, namespaceProblem(err)
	}
	publication := PublicationOutcomeUnknown
	if header.state == reservationStatePrepared {
		publication = PublicationNotPublished
	}
	return s.result(finalState{
		reservationIdentity:               reservationIdentityOf(header),
		mainNamespaceMayHaveBeenAttempted: header.state == reservationStateMainMayHaveBeenAttempted,
		publication:                       publication,
		destinationContent:                DestinationContentUnclassified,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          coordinationAccess,
	}, newCleanupArtifacts(), nil), nil
}

// reservationAccessResidue derives the creator-only policy of one
// coordination reservation (Rust reservation_access).
func reservationAccessResidue(regular *live.RegularFile, header reservationHeader) AccessPolicy {
	if commitment, err := security.CreatorOnlyCommitment(regular.File); err == nil && commitment == header.securityCommitment {
		return AccessPolicyCreatorOnly
	}
	return AccessPolicyChangedOrUnproven
}

// verifyCoordinationResidue re-proves the retained coordination
// inode before and after the operation lock (Rust verify_coordination;
// every failure is the ownership-changed class).
func verifyCoordinationResidue(handle *residueHandle) error {
	if err := handle.destination.directory().Verify(); err != nil {
		return cleanupConflictProblem("canonical coordination ownership changed")
	}
	if err := handle.destination.directory().VerifyName(handle.destination.coordinationName(), handle.coordinationIdentity); err != nil {
		return cleanupConflictProblem("canonical coordination ownership changed")
	}
	return nil
}

// rejectSelectableResidue refuses removal of a selectable
// coordination inode (Rust reject_selectable: selectable reservation
// records and live sidecars need their operation-specific resolver).
func rejectSelectableResidue(file *os.File) error {
	if mapped, ok, err := reservationMappingResidue(file); err != nil {
		return err
	} else if ok {
		selectable := false
		if bytes, err := mapped.View(0, reservationFileSize); err == nil {
			selectable = containsSelectableHeader(bytes)
		}
		_ = mapped.Close()
		if selectable {
			return problem(format.CodeConflict, "selectable coordination requires its operation-specific resolver")
		}
	}
	selectable, err := live.HasSelectableHeader(file)
	if err != nil {
		return sdkProblem(err)
	}
	if selectable {
		return problem(format.CodeConflict, "selectable coordination requires its operation-specific resolver")
	}
	return nil
}

// finishRemovalResidue runs the post-removal directory synchronization
// and the main proof (Rust finish_removal).
func finishRemovalResidue(handle *residueHandle, main *residueMainGuard) (residueFinalCoordination, error) {
	if err := handle.destination.directory().Sync(); err != nil {
		return residueFinalCoordination{}, namespaceProblem(err)
	}
	if err := handle.destination.directory().Verify(); err != nil {
		return residueFinalCoordination{}, namespaceProblem(err)
	}
	if main != nil {
		if err := main.verify(handle.destination); err != nil {
			return residueFinalCoordination{}, err
		}
	} else {
		if err := handle.destination.directory().RequireAbsent(handle.destination.mainName()); err != nil {
			return residueFinalCoordination{}, cleanupConflictProblem("destination main appeared during removal")
		}
	}
	return finalCoordinationResidue(handle)
}

// finalCoordinationResidue classifies whatever now owns the
// coordination name after the removal (Rust final_coordination: the
// removed inode returning is a conflict, unselectable reuse is a
// conflict, a selectable reservation or sidecar is reported).
func finalCoordinationResidue(handle *residueHandle) (residueFinalCoordination, error) {
	regular, err := handle.destination.directory().OpenRegular(handle.destination.coordinationName(), true)
	if err != nil {
		return residueFinalCoordination{}, cleanupConflictProblem("coordination reuse cannot be inspected")
	}
	if regular == nil {
		return residueFinalCoordination{kind: residueCoordinationAbsent, access: AccessPolicyAbsent}, nil
	}
	defer regular.File.Close()
	if regular.Identity == handle.coordinationIdentity {
		return residueFinalCoordination{}, cleanupConflictProblem("removed coordination inode returned to its canonical name")
	}
	kind, publication, err := classifyCoordinationResidue(handle.destination, regular)
	if err != nil {
		return residueFinalCoordination{}, err
	}
	var access AccessPolicy
	switch kind {
	case residueCoordinationPublicationReservation:
		if publication == nil {
			return residueFinalCoordination{}, problem(format.CodeCleanupConflict, "reservation classification reconstructs publication")
		}
		access = publication.CoordinationAccessPolicy
	case residueCoordinationLiveSidecar:
		access = AccessPolicyChangedOrUnproven
	case residueCoordinationAbsent:
		panic("opened coordination is present")
	case residueCoordinationUnselectable:
		return residueFinalCoordination{}, cleanupConflictProblem("coordination name was reused by an unselectable inode")
	}
	if err := handle.destination.directory().VerifyName(handle.destination.coordinationName(), regular.Identity); err != nil {
		return residueFinalCoordination{}, cleanupConflictProblem("coordination reuse changed during inspection")
	}
	return residueFinalCoordination{kind: kind, access: access}, nil
}

// incompleteResidue builds the retained-handle removal result of one
// failed removal (Rust incomplete: the caller retries with the
// returned handle).
func incompleteResidue(handle residueHandle, cause error) residueRemoval {
	retired := handle.retired
	var evidence *residueMain
	if retired != nil && retired.main != nil {
		evidence = &retired.main.evidence
	}
	housekeeping := HousekeepingNone
	visible := []HousekeepingArtifact(nil)
	if retired != nil {
		housekeeping = retired.housekeeping
		visible = retired.visible
	}
	return residueRemoval{
		directoryIdentity:        directoryLocalIdentity(handle.destination),
		coordinationIdentity:     residueLocalIdentity(&handle.coordinationIdentity),
		main:                     evidence,
		laterCoordination:        residueCoordinationUnselectable,
		coordinationAccessPolicy: AccessPolicyUnclassified,
		cleanup:                  newCleanupArtifacts(),
		coordinationCleanup:      CoordinationCleanupCleanupGuard,
		housekeeping:             housekeeping,
		visibleHousekeeping:      visible,
		handle:                   &handle,
		cause:                    cause,
	}
}

// residueFinalCoordination is the post-removal coordination class
// and access (Rust FinalCoordination).
type residueFinalCoordination struct {
	kind   residueCoordination
	access AccessPolicy
}

// residueLocalIdentity reports the portable identity of one retained
// inode (Rust namespace::local).
func residueLocalIdentity(identity *live.FileIdentity) LocalFileIdentity {
	return LocalFileIdentityFromDeviceInode(live.IdentityDeviceInode(identity))
}
