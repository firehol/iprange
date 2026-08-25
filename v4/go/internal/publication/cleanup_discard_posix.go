//go:build !windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// discardCreated discards one freshly created output whose identity
// is best-effort (Rust discard_created unix arm).
func discardCreatedPlatform(created *createdOutput) earlyDiscard {
	facts := created.facts()
	var identity identityOptional
	if facts.IdentityPresent {
		if device, inode, ok := facts.Identity.DeviceInode(); ok {
			identity = identityOptional{present: true, identity: live.IdentityFromDeviceInode(device, inode)}
		}
	}
	problem := discardOne(created.destinationOf().directory(), created.nameOf(), created.fileHandle(), identity)
	var artifact *CleanupArtifact
	if problem != nil {
		a := earlyArtifact(&facts, problem)
		artifact = &a
	}
	return earlyDiscard{output: facts, artifact: artifact, housekeeping: HousekeepingNone}
}

// discardAttempt discards one secured output attempt whose identity
// is established (Rust discard_attempt unix arm).
func discardAttemptPlatform(attempt *outputAttempt, file *os.File) earlyDiscard {
	facts := attempt.facts()
	identity := identityOptional{present: true, identity: attempt.identityOf()}
	problem := discardOne(attempt.destinationOf().directory(), attempt.nameOf(), file, identity)
	var artifact *CleanupArtifact
	if problem != nil {
		a := earlyArtifact(&facts, problem)
		artifact = &a
	}
	return earlyDiscard{output: facts, artifact: artifact, housekeeping: HousekeepingNone}
}

// discardOwnersWith is the shared discard core (Rust
// discard_owners_with unix arm): each owner runs behind its
// checkpoint, one directory sync proves every successful removal, and
// each failing removal pushes one exact artifact into the ledger.
func discardOwnersWithPlatform(s *seed, destination *destination, output outputOwner, hasOutput bool, reservation reservationOwner, hasReservation bool, checkpoint func(cleanupPoint) error) cleanupSummary {
	directory := destination.directory()
	hasOutputRemoval := hasOutput
	var outputRemoval removal
	if hasOutput {
		var r removal
		if err := checkpoint(cleanupPointOutputRemoval); err != nil {
			r = failedRemoval(ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: output.identity}, err)
		} else if rr, err := removeOutput(directory, output); err != nil {
			r = failedRemoval(ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: output.identity}, err)
		} else {
			r = rr
		}
		outputRemoval = r
	}
	hasReservationRemoval := hasReservation
	var reservationRemoval removal
	if hasReservation {
		var r removal
		if err := checkpoint(cleanupPointReservationRemoval); err != nil {
			r = failedRemoval(ArtifactPrivateReservation, defaultSlot(reservation.location), reservation.identity, err)
		} else if rr, err := removeReservation(directory, destination.coordinationName(), reservation); err != nil {
			r = failedRemoval(ArtifactPrivateReservation, defaultSlot(reservation.location), reservation.identity, err)
		} else {
			r = rr
		}
		reservationRemoval = r
	}
	needsSync := hasOutputRemoval && outputRemoval.needsSync() ||
		hasReservationRemoval && reservationRemoval.needsSync()
	var syncProblem error
	if needsSync {
		if err := checkpoint(cleanupPointDirectorySync); err != nil {
			syncProblem = err
		} else if err := directory.Sync(); err != nil {
			syncProblem = namespaceProblem(err)
		} else if err := directory.Verify(); err != nil {
			syncProblem = namespaceProblem(err)
		}
	}
	artifacts := newCleanupArtifacts()
	if hasOutputRemoval {
		finishRemoval(s, outputRemoval, syncProblem, &artifacts)
	}
	if hasReservationRemoval {
		finishRemoval(s, reservationRemoval, syncProblem, &artifacts)
	}
	return cleanupSummary{
		artifacts:           artifacts,
		housekeeping:        HousekeepingNone,
		visibleHousekeeping: nil,
		mainAbsent:          directory.RequireAbsent(destination.mainName()) == nil,
		coordinationAbsent:  directory.RequireAbsent(destination.coordinationName()) == nil,
	}
}

// discardOne proves one private output fully gone (Rust discard_one):
// the identity must be established, then the output removal runs and
// the directory sync proves it.
func discardOne(directory *live.Directory, name string, file *os.File, identity identityOptional) error {
	if !identity.present {
		return cleanupConflictProblem("private output identity was not established")
	}
	removal, err := removeOutput(directory, outputOwner{file: file, identity: identity.identity, name: name})
	if err != nil {
		return err
	}
	return finishOne(directory, removal)
}

// finishOne finishes one early removal by syncing the directory and
// proving zero remaining links (Rust finish_one). The sync and verify
// failures fold to namespace problems; the link proof failure is the
// fixed cleanup conflict.
func finishOne(directory *live.Directory, r removal) error {
	if r.state.problem != nil {
		return r.state.problem
	}
	if err := directory.Sync(); err != nil {
		return namespaceProblem(err)
	}
	if err := directory.Verify(); err != nil {
		return namespaceProblem(err)
	}
	count, err := links(r.state.file)
	if err != nil {
		return err
	}
	if count != 0 {
		return cleanupConflictProblem("private output removal was not proved")
	}
	return nil
}

// removeOutput builds the removal of one private output (Rust
// remove_output): the private output name is the single candidate.
func removeOutput(directory *live.Directory, output outputOwner) (removal, error) {
	return removeFile(directory, output.file, output.identity, ArtifactPrivateOutput,
		[2]nameEntry{{name: output.name, slot: nameSlotPrivateOutput, ok: true}})
}

// removeReservation builds the removal of one private reservation
// (Rust remove_reservation): the candidate name set follows the
// owner's location; a missing owner identity is re-proved from the
// open file.
func removeReservation(directory *live.Directory, canonical string, owner reservationOwner) (removal, error) {
	identity := owner.identity
	if !identity.present {
		id, err := live.RegularIdentity(owner.file, directory.Identity())
		if err != nil {
			return removal{}, namespaceProblem(err)
		}
		identity = identityOptional{present: true, identity: id}
	}
	var names [2]nameEntry
	switch owner.location {
	case ownerLocationCanonical:
		names = [2]nameEntry{
			{name: canonical, slot: nameSlotCoordination, ok: true},
			{name: owner.privateName, slot: nameSlotPrivateReservation, ok: true},
		}
	case ownerLocationEither:
		names = [2]nameEntry{
			{name: owner.privateName, slot: nameSlotPrivateReservation, ok: true},
			{name: canonical, slot: nameSlotCoordination, ok: true},
		}
	default: // ownerLocationPrivate
		names = [2]nameEntry{{name: owner.privateName, slot: nameSlotPrivateReservation, ok: true}}
	}
	return removeFile(directory, owner.file, identity.identity, ArtifactPrivateReservation, names)
}

// removeFile runs one exact removal (Rust remove): zero links need no
// unlink, one link unlinks the candidate names in order, and any
// other link count is a fixed cleanup conflict.
func removeFile(directory *live.Directory, file *os.File, identity live.FileIdentity, kind ArtifactKind, names [2]nameEntry) (removal, error) {
	if !names[0].ok {
		panic("cleanup requires one exact name")
	}
	count, err := links(file)
	if err != nil {
		return removal{}, err
	}
	switch {
	case count == 0:
		return awaitingSyncRemoval(kind, names[0].slot, identity, file), nil
	case count != 1:
		return removal{}, cleanupConflictProblem("owned publication artifact has unexpected links")
	}
	slot, unlinked, err := unlinkNames(directory, identity, names)
	if err != nil {
		return removal{}, err
	}
	if unlinked {
		return requireUnlinked(kind, slot, identity, file)
	}
	if count, err := links(file); err != nil {
		return removal{}, err
	} else if count == 0 {
		return awaitingSyncRemoval(kind, names[0].slot, identity, file), nil
	}
	return removal{}, cleanupConflictProblem("owned publication artifact has no exact retained name")
}

// unlinkNames unlinks the first candidate name that still names the
// expected identity (Rust unlink_names). Absent, replaced, and non-
// regular candidates are skipped; a changed link count is a hard
// namespace error; any other failure is kept as the first problem and
// reported only when no name could be unlinked.
func unlinkNames(directory *live.Directory, identity live.FileIdentity, names [2]nameEntry) (nameSlot, bool, error) {
	var firstProblem error
	for _, entry := range names {
		if !entry.ok {
			continue
		}
		unlinked, err := directory.UnlinkExact(entry.name, identity)
		switch {
		case err == nil && unlinked:
			return entry.slot, true, nil
		case err == nil:
			// The name is absent: try the next candidate.
		default:
			nerr, ok := live.AsNamespaceError(err)
			if !ok {
				if firstProblem == nil {
					firstProblem = namespaceProblem(err)
				}
				continue
			}
			switch nerr.Kind {
			case live.NamespaceMissing, live.NamespaceIdentityChanged, live.NamespaceNotRegular:
				// Absent, replaced, and non-regular candidates are
				// skipped exactly like Rust.
			case live.NamespaceLinkCount:
				// A changed link count is a hard namespace error: the
				// inode no longer matches the single-link removal
				// contract.
				return 0, false, namespaceProblem(err)
			default:
				if firstProblem == nil {
					firstProblem = namespaceProblem(err)
				}
			}
		}
	}
	if firstProblem != nil {
		return 0, false, firstProblem
	}
	return 0, false, nil
}

// requireUnlinked proves the inode has zero links after the exact
// unlink (Rust require_unlinked).
func requireUnlinked(kind ArtifactKind, slot nameSlot, identity live.FileIdentity, file *os.File) (removal, error) {
	count, err := links(file)
	if err != nil {
		return removal{}, err
	}
	if count != 0 {
		return removal{}, cleanupConflictProblem("unlinked publication artifact still has links")
	}
	return awaitingSyncRemoval(kind, slot, identity, file), nil
}
