//go:build windows

package publication

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// discardCreated discards one freshly created output whose identity
// is best-effort (Rust cleanup/windows.rs discard_created): the GC
// retirement needs the exact identity, so a missing one becomes the
// fixed cleanup artifact.
func discardCreatedPlatform(created *createdOutput) earlyDiscard {
	facts := created.facts()
	device, inode, ok := facts.Identity.DeviceInode()
	if !ok {
		problem := cleanupConflictProblem("private output identity was not established")
		a := earlyArtifact(&facts, problem)
		return earlyDiscard{output: facts, artifact: &a, housekeeping: HousekeepingNone}
	}
	retirement := gcRetireOutput(created.destinationOf().directory(), created.attemptID, created.nameOf(), created.fileHandle(),
		live.IdentityFromDeviceInode(device, inode), facts.CreationSecurity, nil)
	return gcEarlyDiscard(facts, retirement)
}

// discardAttempt discards one secured output attempt whose identity
// is established (Rust cleanup/windows.rs discard_attempt via
// retire_output).
func discardAttemptPlatform(attempt *outputAttempt, file *os.File) earlyDiscard {
	facts := attempt.facts()
	retirement := gcRetireOutput(attempt.destinationOf().directory(), attempt.attemptIDOf(), attempt.nameOf(), file,
		attempt.identityOf(), facts.CreationSecurity, nil)
	return gcEarlyDiscard(facts, retirement)
}

// gcRetireOutput retires one private output through its attempt-bound
// envelope (Rust cleanup/windows.rs retire_output: ordinal 0, private
// output kind, destination role).
func gcRetireOutput(directory *live.Directory, attemptID [16]byte, name string, file *os.File, identity live.FileIdentity, creationSecurity CreationSecurity, payload *live.GCPayload) live.GCRetirement {
	return live.GCRetire(directory, &live.GCAuthority{
		AttemptID:        attemptID,
		Ordinal:          0,
		Kind:             ArtifactPrivateOutput,
		DirectoryRole:    DirectoryRoleDestination,
		SourceName:       name,
		SourceFile:       file,
		Identity:         identity,
		CreationSecurity: creationSecurity,
		Payload:          payload,
	})
}

// gcEarlyDiscard folds one GC retirement into the early-discard facts
// (Rust cleanup/windows.rs early: the retirement problem becomes the
// exact artifact, the visible ledger rides the housekeeping facts).
func gcEarlyDiscard(facts PrivateOutputAttempt, retirement live.GCRetirement) earlyDiscard {
	var artifact *CleanupArtifact
	if retirement.Problem != nil {
		a := earlyArtifact(&facts, retirement.Problem)
		artifact = &a
	}
	var visible []HousekeepingArtifact
	if retirement.Visible != nil {
		visible = append(visible, *retirement.Visible)
	}
	return earlyDiscard{
		output:              facts,
		artifact:            artifact,
		housekeeping:        retirement.Housekeeping,
		visibleHousekeeping: visible,
	}
}

// discardOwnersWith is the Windows GC transition of the shared discard
// core (Rust cleanup/windows.rs discard_owners): every owner retires
// through its attempt-bound envelope and the retirement facts absorb
// into the ledger, the housekeeping class, and the visible artifacts.
func discardOwnersWithPlatform(s *seed, destination *destination, output outputOwner, hasOutput bool, reservation reservationOwner, hasReservation bool, checkpoint func(cleanupPoint) error) cleanupSummary {
	directory := destination.directory()
	artifacts := newCleanupArtifacts()
	housekeeping := HousekeepingNone
	var visible []HousekeepingArtifact
	if hasOutput {
		var retirement live.GCRetirement
		if err := checkpoint(cleanupPointOutputRemoval); err != nil {
			retirement = live.GCRetirement{Problem: formatProblem(err), Housekeeping: HousekeepingNone}
		} else {
			retirement = gcRetireOutput(directory, s.attemptID, output.name, output.file, output.identity, s.creationSecurity, gcSeedPayload(s))
		}
		gcAbsorb(s, &artifacts, &housekeeping, &visible, retirement,
			ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: output.identity})
	}
	if hasReservation {
		identity := reservation.identity
		if !identity.present {
			if id, err := live.RegularIdentity(reservation.file, directory.Identity()); err == nil {
				identity = identityOptional{present: true, identity: id}
			}
		}
		var retirement live.GCRetirement
		source, sourceOK := gcReservationSource(destination, reservation, identity)
		if err := checkpoint(cleanupPointReservationRemoval); err != nil {
			retirement = live.GCRetirement{Problem: formatProblem(err), Housekeeping: HousekeepingNone}
		} else if !sourceOK {
			retirement = live.GCRetirement{Problem: cleanupConflictProblem("reservation cleanup has no exact retained source name"), Housekeeping: HousekeepingNone}
		} else {
			retirement = live.GCRetire(directory, &live.GCAuthority{
				AttemptID:        s.attemptID,
				Ordinal:          1,
				Kind:             source.kind,
				DirectoryRole:    DirectoryRoleDestination,
				SourceName:       source.name,
				SourceFile:       reservation.file,
				Identity:         source.identity,
				CreationSecurity: s.creationSecurity,
				Payload:          nil,
			})
		}
		kind, slot := ArtifactPrivateReservation, defaultSlot(reservation.location)
		if sourceOK {
			kind, slot = source.kind, source.slot
		}
		gcAbsorb(s, &artifacts, &housekeeping, &visible, retirement, kind, slot, identity)
	}
	return cleanupSummary{
		artifacts:           artifacts,
		housekeeping:        housekeeping,
		visibleHousekeeping: visible,
		mainAbsent:          directory.RequireAbsent(destination.mainName()) == nil,
		coordinationAbsent:  directory.RequireAbsent(destination.coordinationName()) == nil,
	}
}

// gcReservationSource chooses the exact canonical/private source of
// one reservation owner (Rust cleanup/windows.rs reservation_source):
// the name that still verifies against the identity decides the
// artifact kind and name slot.
func gcReservationSource(destination *destination, owner reservationOwner, identity identityOptional) (reservationSource, bool) {
	private := func() (reservationSource, bool) {
		if !identity.present {
			return reservationSource{}, false
		}
		if err := destination.directory().VerifyName(owner.privateName, identity.identity); err != nil {
			return reservationSource{}, false
		}
		return reservationSource{name: owner.privateName, kind: ArtifactPrivateReservation, slot: nameSlotPrivateReservation, identity: identity.identity}, true
	}
	canonical := func() (reservationSource, bool) {
		if !identity.present {
			return reservationSource{}, false
		}
		coordination := destination.coordinationName()
		if err := destination.directory().VerifyName(coordination, identity.identity); err != nil {
			return reservationSource{}, false
		}
		return reservationSource{name: coordination, kind: ArtifactOwnedCoordination, slot: nameSlotCoordination, identity: identity.identity}, true
	}
	switch owner.location {
	case ownerLocationPrivate:
		return private()
	case ownerLocationCanonical:
		return canonical()
	default: // ownerLocationEither
		if source, ok := private(); ok {
			return source, true
		}
		return canonical()
	}
}

// reservationSource is the exact retained name of one reservation
// owner (Rust cleanup/windows.rs reservation_source tuple).
type reservationSource struct {
	name     string
	kind     ArtifactKind
	slot     nameSlot
	identity live.FileIdentity
}

// gcAbsorb folds one GC retirement into the discard summary (Rust
// cleanup/windows.rs absorb): the retirement problem pushes one exact
// artifact, the housekeeping class merges, and the visible ledger
// appends.
func gcAbsorb(s *seed, artifacts *CleanupArtifacts, housekeeping *Housekeeping, visible *[]HousekeepingArtifact, retirement live.GCRetirement, kind ArtifactKind, slot nameSlot, identity identityOptional) {
	if retirement.Problem != nil {
		artifacts.Push(s.artifact(kind, slot, identity, retirement.Problem))
	}
	*housekeeping = housekeeping.Merge(retirement.Housekeeping)
	if retirement.Visible != nil {
		*visible = append(*visible, *retirement.Visible)
	}
}

// gcSeedPayload builds the exact output payload of one prepared seed
// (Rust Seed::output_payload: the full database tuple).
func gcSeedPayload(s *seed) *live.GCPayload {
	return &live.GCPayload{
		ByteLength:    s.outputByteLength,
		SHA512:        s.outputSHA512,
		DatabaseID:    s.databaseID,
		TransactionID: s.transactionID,
		CommitNonce:   s.commitNonce,
	}
}

// formatProblem folds one checkpoint error to its fixed problem shape
// (the checkpoint wrapper keeps the exact problem; every other error
// is already a final problem).
func formatProblem(err error) *format.Error {
	var fe *format.Error
	if errors.As(err, &fe) {
		return fe
	}
	if checkpoint := asCheckpointProblem(err); checkpoint != nil {
		return checkpoint
	}
	return &format.Error{Code: format.CodeIO, Detail: err.Error()}
}
