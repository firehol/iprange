//go:build windows

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// unlinkPrevious retires the replaced previous destination through
// its attempt-bound GC envelope (Rust unlink_previous windows arm:
// ordinal 0, private output kind, the exact previous content payload).
func unlinkPrevious(owner *retiringMain) (bool, error) {
	published := &owner.published
	if published.output.previous == nil {
		owner.previousRetiredProven = true
		return false, nil
	}
	destination := published.output.attempt.destinationOf()
	if err := published.output.previous.verifyPrivateOrRetired(destination, published.output.attempt.nameOf()); err != nil {
		return false, err
	}
	count, err := live.RegularLinkCount(published.output.previous.file)
	if err != nil {
		return false, err
	}
	if count == 0 {
		owner.previousUnlinked = true
		return false, nil
	}
	retirement := live.GCRetire(destination.directory(), &live.GCAuthority{
		AttemptID:     published.output.attempt.attemptIDOf(),
		Ordinal:       0,
		Kind:          ArtifactPrivateOutput,
		DirectoryRole: DirectoryRoleDestination,
		SourceName:    published.output.attempt.nameOf(),
		SourceFile:    published.output.previous.file,
		Identity:      published.output.previous.identity,
		CreationSecurity: CreationSecurity{
			Kind:       creationSecurityKind,
			Commitment: destination.securityCommitment(),
		},
		Payload: &live.GCPayload{
			ByteLength: published.output.previous.byteLength,
			SHA512:     published.output.previous.sha512,
		},
	})
	if err := absorbGCRetirement(owner, retirement); err != nil {
		return false, err
	}
	owner.previousUnlinked = true
	owner.previousRetiredProven = true
	fault.Crash("publication.after_previous_unlink")
	return true, nil
}

// unlinkReservation retires the coordination reservation through its
// attempt-bound GC envelope (Rust unlink_reservation windows arm:
// ordinal 1, owned-coordination kind, no content payload).
func unlinkReservation(owner *retiringMain) error {
	published := &owner.published
	destination := published.output.attempt.destinationOf()
	retirement := live.GCRetire(destination.directory(), &live.GCAuthority{
		AttemptID:     published.output.attempt.attemptIDOf(),
		Ordinal:       1,
		Kind:          ArtifactOwnedCoordination,
		DirectoryRole: DirectoryRoleDestination,
		SourceName:    destination.coordinationName(),
		SourceFile:    published.reservation.file,
		Identity:      published.reservation.identity,
		CreationSecurity: CreationSecurity{
			Kind:       creationSecurityKind,
			Commitment: destination.securityCommitment(),
		},
		Payload: nil,
	})
	if err := absorbGCRetirement(owner, retirement); err != nil {
		return err
	}
	owner.reservationUnlinked = true
	owner.reservationRetiredProven = true
	fault.Crash("publication.after_reservation_unlink")
	return nil
}

// absorbGCRetirement folds one GC retirement into the retiring owner
// (Rust absorb_gc): the housekeeping class merges, the visible ledger
// appends, and a retirement problem fails the step.
func absorbGCRetirement(owner *retiringMain, retirement live.GCRetirement) error {
	owner.housekeeping = owner.housekeeping.Merge(retirement.Housekeeping)
	if retirement.Visible != nil {
		owner.visibleHousekeeping = append(owner.visibleHousekeeping, *retirement.Visible)
	}
	if retirement.Problem != nil {
		return retirement.Problem
	}
	return nil
}
