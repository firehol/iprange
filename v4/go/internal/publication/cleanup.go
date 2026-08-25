// Exact discard of direct-publication artifacts before main
// publication (Rust publication/cleanup.rs). One removal proves the
// retained name went away, then the directory sync and verify run; a
// removal that cannot be proved pushes one exact cleanup artifact into
// the result ledger. The posix arm runs the unlink machine; the
// windows arm runs the authenticated GC transition
// (cleanup_discard_windows.go).

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

// discardAttempt discards one secured output attempt whose identity
// is established (Rust discard_attempt).

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

// discardCreated discards one freshly created output whose identity
// is best-effort (Rust discard_created; the platform arm selects the
// unix removal or the Windows GC transition).
func discardCreated(created *createdOutput) earlyDiscard {
	return discardCreatedPlatform(created)
}

// discardAttempt discards one secured output attempt whose identity
// is established (Rust discard_attempt; platform arm).
func discardAttempt(attempt *outputAttempt, file *os.File) earlyDiscard {
	return discardAttemptPlatform(attempt, file)
}

// discardOwnersWith is the shared discard core (Rust
// discard_owners_with; the platform arm selects the unix removal
// machine or the Windows GC transition).
func discardOwnersWith(s *seed, destination *destination, output outputOwner, hasOutput bool, reservation reservationOwner, hasReservation bool, checkpoint func(cleanupPoint) error) cleanupSummary {
	return discardOwnersWithPlatform(s, destination, output, hasOutput, reservation, hasReservation, checkpoint)
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

// discardOne proves one private output fully gone (Rust discard_one):
// the identity must be established, then the output removal runs and
// the directory sync proves it.

// finishOne finishes one early removal by syncing the directory and
// proving zero remaining links (Rust finish_one). The sync and verify
// failures fold to namespace problems; the link proof failure is the
// fixed cleanup conflict.

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

// removeReservation builds the removal of one private reservation
// (Rust remove_reservation): the candidate name set follows the
// owner's location; a missing owner identity is re-proved from the
// open file.

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

// unlinkNames unlinks the first candidate name that still names the
// expected identity (Rust unlink_names). Absent, replaced, and non-
// regular candidates are skipped; a changed link count is a hard
// namespace error; any other failure is kept as the first problem and
// reported only when no name could be unlinked.

// requireUnlinked proves the inode has zero links after the exact
// unlink (Rust require_unlinked).

// links reports the current link count of one open file (Rust links);
// failures fold to namespace problems.

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
		artifacts.Push(s.artifact(r.kind, r.name, r.identity, problem))
	}
}

// links reports the current link count of one open file (Rust links);
// failures fold to namespace problems. The shared finishRemoval uses
// it on every platform.
func links(file *os.File) (uint64, error) {
	count, err := live.RegularLinkCount(file)
	if err != nil {
		return 0, namespaceProblem(err)
	}
	return count, nil
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
