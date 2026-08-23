//go:build !windows

// Structured result construction after resolver classification (Rust
// publication/resolver_result.rs).

package publication

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// desiredContext is the shared fact inventory of one desired-main
// resolution (Rust DesiredContext).
type desiredContext struct {
	destination *destination
	header      reservationHeader
	reservation *inspectedReservation
	later       *inspectedReservation
	main        *inspectedOutput
}

// recordCancellation folds one post-mutation cancellation into the
// result cause (Rust record_cancellation): when the caller cancelled
// during the cleanup and the result carries no cause or only the
// cleanup cause, the fixed cancellation class replaces it.
func recordCancellation(result PublicationResult, check func() error) PublicationResult {
	if err := checkCancellation(check); err == nil {
		return result
	}
	cleanupCause := firstProblem(&result.Cleanup)
	if result.Cause == nil || problemEqual(result.Cause, cleanupCause) {
		result.Cause = problem(format.CodeCancelled, "publication resolution was cancelled after mutation")
	}
	return result
}

// problemEqual compares two problems by code and detail (Rust
// Problem PartialEq on the cleanup clone; fresh constructions of one
// fixed problem are distinct pointers but equal values).
func problemEqual(a, b error) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	var fa, fb *format.Error
	if errors.As(a, &fa) && errors.As(b, &fb) {
		return fa.Code == fb.Code && fa.Detail == fb.Detail
	}
	return a == b
}

// desiredResult builds the exact published result of one desired main
// (Rust desired_result).
func desiredResult(s seed, summary cleanupSummary, context desiredContext) PublicationResult {
	verification := context.main.verify(context.destination)
	if verification == nil {
		verification = verifyLater(context.later, context.destination)
	}
	cause := verification
	if cause == nil {
		cause = firstProblem(&summary.artifacts)
	}
	result := s.resultWithHousekeeping(
		desiredState(context.header, context.main, summary, context.reservation, context.later, verification == nil),
		summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, cause,
	)
	return withLater(result, context.later)
}

// desiredProblem builds the outcome-unknown result of one failed
// desired-main cleanup (Rust desired_problem).
func desiredProblem(s seed, header reservationHeader, summary cleanupSummary, problem error) PublicationResult {
	return s.resultWithHousekeeping(finalState{
		reservationIdentity:               reservationIdentityOf(header),
		mainNamespaceMayHaveBeenAttempted: true,
		publication:                       PublicationOutcomeUnknown,
		destinationContent:                DestinationContentUnclassified,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          AccessPolicyUnclassified,
	}, summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, problem)
}

// publishedOutputResult builds the published result with the exact
// private-output failure cause (Rust published_output_result).
func publishedOutputResult(s seed, summary cleanupSummary, problem error, context desiredContext) PublicationResult {
	verified := context.main.verify(context.destination)
	if verified == nil {
		verified = verifyLater(context.later, context.destination)
	}
	result := s.resultWithHousekeeping(
		desiredState(context.header, context.main, summary, context.reservation, context.later, verified == nil),
		summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, problem,
	)
	return withLater(result, context.later)
}

// desiredState builds the final state of one desired main (Rust
// desired_state).
func desiredState(header reservationHeader, main *inspectedOutput, summary cleanupSummary, reservation *inspectedReservation, later *inspectedReservation, verified bool) finalState {
	state := finalState{
		reservationIdentity:               reservationIdentityOf(header),
		mainNamespaceMayHaveBeenAttempted: true,
		publication:                       PublicationOutcomeUnknown,
		destinationContent:                DestinationContentUnclassified,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          AccessPolicyUnclassified,
	}
	if verified {
		state.publication = PublicationPublished
		state.destinationContent = DestinationContentDesired
		state.mainAccessPolicy = main.access
		state.coordinationAccessPolicy = coordinationAccess(summary, reservation, later)
	}
	return state
}

// coordinationAccess derives the coordination access policy of one
// post-cleanup state (Rust coordination_access).
func coordinationAccess(summary cleanupSummary, reservation *inspectedReservation, later *inspectedReservation) AccessPolicy {
	if summary.coordinationAbsent {
		return AccessPolicyAbsent
	}
	owner := later
	if owner == nil && reservation != nil && reservation.location == reservationLocationCanonical {
		owner = reservation
	}
	if owner == nil {
		return AccessPolicyUnclassified
	}
	return owner.access
}

// firstProblem reports the first cleanup artifact problem (Rust
// first_problem).
func firstProblem(cleanup *CleanupArtifacts) error {
	if cleanup == nil || cleanup.Empty() {
		return nil
	}
	return cleanup.At(0).Error
}

// withLater records the later-canonical observation (Rust
// LaterResult::with_later).
func withLater(result PublicationResult, later *inspectedReservation) PublicationResult {
	if later != nil && later.location == reservationLocationCanonical {
		result.LaterCanonical = LaterCanonicalReservationOrTransition
	}
	return result
}
