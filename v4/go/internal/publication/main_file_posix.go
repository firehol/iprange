//go:build !windows

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// unlinkPrevious removes the replaced previous destination's retained
// private name and proves zero links (Rust unlink_previous unix arm).
// A nil previous proves itself retired; a zero-link previous needs no
// unlink. The returned bool reports that an unlink ran, which is when
// the PreviousUnlinked checkpoint fires.
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
	unlinked, err := destination.directory().UnlinkExact(published.output.attempt.nameOf(), published.output.previous.identity)
	if err != nil {
		return false, err
	}
	if !unlinked {
		return false, &live.NamespaceError{Kind: live.NamespaceMissing}
	}
	owner.previousUnlinked = true
	count, err = live.RegularLinkCount(published.output.previous.file)
	if err != nil {
		return false, err
	}
	if count != 0 {
		return false, cleanupConflictProblem("retired previous destination still has a link")
	}
	fault.Crash("publication.after_previous_unlink")
	return true, nil
}

// unlinkReservation removes the coordination reservation name and
// proves zero links (Rust unlink_reservation unix arm).
func unlinkReservation(owner *retiringMain) error {
	published := &owner.published
	destination := published.output.attempt.destinationOf()
	unlinked, err := destination.directory().UnlinkExact(destination.coordinationName(), published.reservation.identity)
	if err != nil {
		return err
	}
	if !unlinked {
		return &live.NamespaceError{Kind: live.NamespaceMissing}
	}
	owner.reservationUnlinked = true
	count, err := live.RegularLinkCount(published.reservation.file)
	if err != nil {
		return err
	}
	if count != 0 {
		return cleanupConflictProblem("retired reservation still has a link")
	}
	fault.Crash("publication.after_reservation_unlink")
	return nil
}
