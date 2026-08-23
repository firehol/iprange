//go:build !windows

// Exact discard of direct-publication artifacts before main
// publication (Rust publication/cleanup.rs unix arm). One removal
// proves the retained name went away, then the directory sync and
// verify run; a removal that cannot be proved pushes one exact
// cleanup artifact into the result ledger. Go refuses Windows
// publication opens at destination bind (M5), so the Rust Windows
// gc-transition arm of cleanup.rs is never reachable and
// intentionally absent.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// identityOptional is the Go peer of Rust Option<Identity> for the
// cleanup machine: a value presence flag that keeps every success
// path on the stack (Rust Copy semantics).
type identityOptional struct {
	identity live.FileIdentity
	present  bool
}

// ownerLocation classifies the name set one cleanup reservation owner
// may hold (Rust cleanup.rs ReservationLocation: Private, Canonical,
// or Either). The reservation custody position of reservation_verify.go
// keeps the reservationLocation name.
type ownerLocation uint8

const (
	ownerLocationPrivate ownerLocation = iota
	ownerLocationCanonical
	ownerLocationEither
)

// reservationOwner is one still-owned private reservation (Rust
// ReservationOwner). identity is absent when the owner cannot present
// the reservation identity yet (Rust Option<Identity>).
type reservationOwner struct {
	file        *os.File
	identity    identityOptional
	privateName string
	location    ownerLocation
}

// outputOwner is one still-owned private output (Rust OutputOwner).
type outputOwner struct {
	file     *os.File
	identity live.FileIdentity
	name     string
}

// cleanupSummary is the factual outcome of one discard (Rust
// Summary): the cleanup ledger, the absent-main and absent-
// coordination proofs, and the housekeeping facts (always none on
// unix).
type cleanupSummary struct {
	artifacts           CleanupArtifacts
	housekeeping        Housekeeping
	visibleHousekeeping []HousekeepingArtifact
	mainAbsent          bool
	coordinationAbsent  bool
}

// earlyDiscard is the factual outcome of one pre-publication discard
// of a created or attempted output (Rust EarlyDiscard).
type earlyDiscard struct {
	output              PrivateOutputAttempt
	artifact            *CleanupArtifact
	housekeeping        Housekeeping
	visibleHousekeeping []HousekeepingArtifact
}

// cleanupPoint is one discard checkpoint (Rust Point).
type cleanupPoint uint8

const (
	cleanupPointOutputRemoval cleanupPoint = iota
	cleanupPointReservationRemoval
	cleanupPointDirectorySync
)

// discardCreated discards one freshly created output whose identity
// is best-effort (Rust discard_created).
func discardCreated(created *createdOutput) earlyDiscard {
	facts := created.facts()
	var identity identityOptional
	if facts.IdentityPresent {
		if device, inode, ok := facts.Identity.deviceInode(); ok {
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
// is established (Rust discard_attempt).
func discardAttempt(attempt *outputAttempt, file *os.File) earlyDiscard {
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

// failedAttempt records one already-failed attempt (Rust
// failed_attempt): the problem becomes the exact artifact with no
// further namespace work.
func failedAttempt(facts PrivateOutputAttempt, problem error) earlyDiscard {
	a := earlyArtifact(&facts, problem)
	return earlyDiscard{output: facts, artifact: &a, housekeeping: HousekeepingNone}
}

// confirmedAbsent records one attempt proved absent (Rust
// confirmed_absent): no artifact, no namespace work.
func confirmedAbsent(facts PrivateOutputAttempt) earlyDiscard {
	return earlyDiscard{output: facts, housekeeping: HousekeepingNone}
}

// discardWith discards one prepared output and its optional
// reservation (Rust discard_with): every removal runs behind its
// checkpoint.
func discardWith(s *seed, output *preparedOutput, reservation *reservationOwner, checkpoint func(cleanupPoint) error) cleanupSummary {
	d := output.attempt.destinationOf()
	identity := output.attempt.identityOf()
	owner := outputOwner{file: output.file, identity: identity, name: output.attempt.nameOf()}
	var res reservationOwner
	hasReservation := reservation != nil
	if hasReservation {
		res = *reservation
	}
	return discardOwnersWith(s, d, owner, true, res, hasReservation, checkpoint)
}

// discardRecovered discards one recovered output and reservation with
// no checkpoint (Rust discard_recovered passes the always-ok closure,
// so recovery paths never re-sync through this machine).
func discardRecovered(s *seed, destination *destination, output *outputOwner, reservation *reservationOwner) cleanupSummary {
	var owner outputOwner
	hasOutput := output != nil
	if hasOutput {
		owner = *output
	}
	var res reservationOwner
	hasReservation := reservation != nil
	if hasReservation {
		res = *reservation
	}
	return discardOwnersWith(s, destination, owner, hasOutput, res, hasReservation, func(cleanupPoint) error { return nil })
}

// discardOwnersWith is the shared discard core (Rust
// discard_owners_with): each owner runs behind its checkpoint, one
// directory sync proves every successful removal, and each failing
// removal pushes one exact artifact into the ledger.
func discardOwnersWith(s *seed, destination *destination, output outputOwner, hasOutput bool, reservation reservationOwner, hasReservation bool, checkpoint func(cleanupPoint) error) cleanupSummary {
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

// earlyArtifact builds the exact artifact of one early discard (Rust
// early_artifact): always the private output of the destination
// directory, carrying the portable attempt facts and the fixed
// problem.
func earlyArtifact(facts *PrivateOutputAttempt, problem error) CleanupArtifact {
	var identity *LocalFileIdentity
	if facts.IdentityPresent {
		converted := facts.Identity
		identity = &converted
	}
	security := facts.CreationSecurity
	return CleanupArtifact{
		Kind:              ArtifactPrivateOutput,
		DirectoryRole:     DirectoryRoleDestination,
		DirectoryIdentity: facts.DirectoryIdentity,
		BasenameEncoding:  facts.BasenameEncoding,
		Basename:          append([]byte(nil), facts.Basename...),
		Identity:          identity,
		CreationSecurity:  &security,
		UnpublishedTail:   nil,
		Error:             problem,
	}
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

// nameEntry is one exact removal candidate (Rust Option<(&Name,
// NameSlot)>): the name to unlink and the slot its success proves.
type nameEntry struct {
	name string
	slot nameSlot
	ok   bool
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

// links reports the current link count of one open file (Rust links);
// failures fold to namespace problems.
func links(file *os.File) (uint64, error) {
	count, err := live.RegularLinkCount(file)
	if err != nil {
		return 0, namespaceProblem(err)
	}
	return count, nil
}

// finishRemoval closes one removal into the ledger (Rust
// finish_removal): a failing removal pushes one exact artifact that
// consumes its name slot from the seed.
func finishRemoval(s *seed, r removal, syncProblem error, artifacts *CleanupArtifacts) {
	var problem error
	switch {
	case r.state.problem != nil:
		problem = r.state.problem
	case syncProblem != nil:
		problem = syncProblem
	default:
		count, err := links(r.state.file)
		switch {
		case err != nil:
			problem = err
		case count != 0:
			problem = cleanupConflictProblem("publication artifact removal was not proved")
		}
	}
	if problem != nil {
		artifacts.push(s.artifact(r.kind, r.name, r.identity, problem))
	}
}

// removal is one exact removal decision of the discard machine (Rust
// Removal): the artifact kind, the proved name slot, the removal
// identity, and the state (needs directory sync, or already failed).
type removal struct {
	kind     ArtifactKind
	name     nameSlot
	identity identityOptional
	state    removalState
}

// removalState is the state of one removal (Rust RemovalState): at
// most one of file and problem is set.
type removalState struct {
	file    *os.File // non-nil: needs the directory sync to prove removal
	problem error    // non-nil: the removal already failed
}

// awaitingSyncRemoval marks one removal that only needs the directory
// sync to prove the name went away (Rust Removal::awaiting_sync).
func awaitingSyncRemoval(kind ArtifactKind, slot nameSlot, identity live.FileIdentity, file *os.File) removal {
	return removal{
		kind:     kind,
		name:     slot,
		identity: identityOptional{present: true, identity: identity},
		state:    removalState{file: file},
	}
}

// failedRemoval marks one removal that already failed (Rust
// Removal::failed). identity is absent when the removal never
// established the inode identity.
func failedRemoval(kind ArtifactKind, slot nameSlot, identity identityOptional, problem error) removal {
	return removal{kind: kind, name: slot, identity: identity, state: removalState{problem: problem}}
}

// needsSync reports whether the removal still needs the directory
// sync (Rust Removal::needs_sync).
func (r removal) needsSync() bool { return r.state.file != nil }

// defaultSlot reports the artifact name slot of one reservation
// location (Rust default_slot).
func defaultSlot(location ownerLocation) nameSlot {
	switch location {
	case ownerLocationPrivate, ownerLocationEither:
		return nameSlotPrivateReservation
	default:
		return nameSlotCoordination
	}
}
