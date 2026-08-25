// Final durability and namespace checks for restart resolution (Rust
// publication/resolver_verification.rs).

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// verifyDestination proves the post-cleanup destination matches the
// classified content (Rust verify_destination).
func verifyDestination(destination *destination, content DestinationContent, main *inspectedOutput, summary cleanupSummary) error {
	switch {
	case content == DestinationContentAbsent && main == nil && summary.mainAbsent:
		return nil
	case content == DestinationContentOther && main != nil:
		return resolverProblem(main.verify(destination))
	default:
		return problem(format.CodeCleanupConflict, "destination changed during publication cleanup")
	}
}

// verifyNoLater proves no coordination owner appeared during the
// cleanup (Rust verify_no_later): an absent coordination name proves
// the cleanup; a still-present canonical owner is re-proved; anything
// else is the foreign-owner conflict.
func verifyNoLater(destination *destination, reservation *inspectedReservation, summary cleanupSummary) error {
	if summary.coordinationAbsent {
		return nil
	}
	if reservation != nil && reservation.location == reservationLocationCanonical {
		return resolverProblem(reservation.verify(destination))
	}
	return conflictProblem("another coordination owner appeared during publication cleanup")
}

// finalLater derives the later canonical reservation after one
// cleanup (Rust final_later): the known later, an absent coordination
// name, or a still-verified exact reservation keep the state; a new
// canonical owner is re-inspected and classified.
func finalLater(destination *destination, header reservationHeader, reservation *inspectedReservation, later *inspectedReservation, summary cleanupSummary) (*inspectedReservation, error) {
	if later != nil || summary.coordinationAbsent {
		return later, nil
	}
	if reservation != nil && reservation.location == reservationLocationCanonical && reservation.verify(destination) == nil {
		return nil, nil
	}
	current, err := inspectedCanonical(destination, noopCheck)
	if err != nil {
		return nil, resolverProblem(err)
	}
	if current != nil && current.header.attemptID == header.attemptID {
		return nil, conflictProblem("publication coordination identity changed for the same attempt")
	}
	return current, nil
}

// synchronize durably flushes one inspected main and re-proves it
// (Rust resolver_verification::synchronize).
func synchronize(destination *destination, main *inspectedOutput, check func() error) error {
	if err := checkCancellation(check); err != nil {
		return err
	}
	if err := syncOutputFile(main.file); err != nil {
		return resolverProblem(err)
	}
	return resolverProblem(main.verify(destination))
}

// checkCancellation runs one cancellation checkpoint (Rust
// CancellationToken::check mapped into the publication problem
// surface).
func checkCancellation(check func() error) error {
	if check == nil {
		return nil
	}
	return check()
}

// syncOutputFile flushes one retained output file (Rust
// namespace::sync_file on the inspected main).
func syncOutputFile(file *os.File) error {
	return live.SyncFile(file)
}

// noopCheck is the always-clean cancellation checkpoint of the
// one-shot inspection helpers (Rust CancellationToken::new).
func noopCheck() error { return nil }
