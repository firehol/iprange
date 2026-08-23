//go:build !windows

// One fail-if-exists or replacement publication composed from the
// explicit ownership states (Rust publication/attempt.rs). The
// machine creates and initializes the reservation, arms state 2 over
// the coordination twin, publishes the main name atomically per
// policy, and retires the reservation and any replaced previous;
// every outcome carries the exact cleanup ledger and housekeeping
// facts. The Rust observed variants are ported fully (the slice-F
// "recorded with 4-10/4-11" deferrals are implemented here); the
// observer is the crash-test hook, inert in production.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// publicationCheckpoint is one observed machine check point (Rust
// PublicationCheckpoint): exactly one of the preparation failure or
// the provisional result is set. Production passes a no-op observer;
// the crash tests observe every check point.
type publicationCheckpoint struct {
	preparation *PublicationPreparationFailure
	result      *PublicationResult
}

// attemptPoint is one publication machine checkpoint (Rust
// attempt.rs Point).
type attemptPoint uint8

const (
	attemptPointReservationCreated attemptPoint = iota
	attemptPointState1Selected
	attemptPointReservationAcquired
	attemptPointState2Selected
	attemptPointDesiredProven
	attemptPointCleanupOutput
	attemptPointCleanupReservation
	attemptPointCleanupDirectorySync
)

// failIfExistsCancellable publishes one prepared output with the
// fail-if-exists policy behind the cancellation check (Rust
// fail_if_exists_cancellable). output.previous must be nil and
// output.policy must be the fail-if-exists policy (Rust debug
// asserts; the facade enforces them).
func failIfExistsCancellable(output *preparedOutput, check func() error) (PublicationResult, *PublicationPreparationFailure) {
	return publishWithObserver(output, check, cancellationCheckpoint(check), false, func(*publicationCheckpoint) error { return nil })
}

// failIfExistsCancellableObserved publishes with the observer hook
// (Rust fail_if_exists_cancellable_observed).
func failIfExistsCancellableObserved(output *preparedOutput, check func() error, observer func(*publicationCheckpoint) error) (PublicationResult, *PublicationPreparationFailure) {
	return publishWithObserver(output, check, cancellationCheckpoint(check), true, observer)
}

// replaceExistingCancellable publishes one prepared output with a
// replacement policy behind the cancellation check (Rust
// replace_existing_cancellable). output.previous must be present and
// the policy must be a replacement policy (Rust debug asserts).
func replaceExistingCancellable(output *preparedOutput, check func() error) (PublicationResult, *PublicationPreparationFailure) {
	return publishWithObserver(output, check, cancellationCheckpoint(check), false, func(*publicationCheckpoint) error { return nil })
}

// cancellationCheckpoint builds the machine checkpoint closure that
// runs the caller's cancellation check at every non-cleanup point
// (Rust publish_with_observer checkpoint wiring; cleanup must finish
// even when the caller was cancelled).
func cancellationCheckpoint(check func() error) func(attemptPoint) error {
	return func(point attemptPoint) error {
		if cleanupIgnoresCancellation(point) {
			return nil
		}
		if err := check(); err != nil {
			return sdkProblem(err)
		}
		return nil
	}
}

// resumeArmed resumes one publication from an armed reservation after
// a crash (Rust attempt.rs resume_armed): the main namespace was
// possibly already attempted, so the outcome class follows the
// desired-proof flag.
func resumeArmed(s seed, output *preparedOutput, reservation armedReservation) PublicationResult {
	published, failure := publishProved(output, reservation)
	if failure == nil {
		return finishPublished(s, published, nil)
	}
	if failure.owner.desiredProven {
		return finishPublished(s, publishedMain{output: failure.owner.output, reservation: failure.owner.reservation}, mainProblem(failure.cause))
	}
	return outcomeUnknown(s, failure.owner.reservation.identity, mainProblem(failure.cause))
}

func publishWithObserver(output *preparedOutput, check func() error, checkpoint func(attemptPoint) error, observe bool, observer func(*publicationCheckpoint) error) (PublicationResult, *PublicationPreparationFailure) {
	s := captureSeed(output)
	draft, err := createReservationDraft(output)
	if err != nil {
		return PublicationResult{}, preparation(s, output, nil, reservationProblem(err), checkpoint)
	}
	err = observePreparation(&s, output, optionalFrom(draft.identity), nameSlotPrivateReservation, observe, observer)
	if err == nil {
		err = checkpoint(attemptPointReservationCreated)
	}
	if err != nil {
		owner := draftOwner(draft)
		return PublicationResult{}, preparation(s, output, &owner, err, checkpoint)
	}

	reservation, failure := draft.initializeObserved(output, func(identity live.FileIdentity) error {
		if err := observeNotPublished(&s, output.attempt.identityOf(), identity, nameSlotPrivateReservation, AccessPolicyUnclassified, observe, observer); err != nil {
			return err
		}
		return checkpoint(attemptPointState1Selected)
	})
	if failure != nil {
		cause := reservationProblem(failure.cause)
		if failure.owner.state1Selected {
			if failure.owner.identity == nil {
				panic("selected state 1 has reservation identity")
			}
			owner := draftOwner(failure.owner)
			return notPublished(s, output, owner, *failure.owner.identity, cause, checkpoint), nil
		}
		owner := draftOwner(failure.owner)
		return PublicationResult{}, preparation(s, output, &owner, cause, checkpoint)
	}
	return fromPrivate(s, output, *reservation, check, checkpoint, observe, observer)
}

func fromPrivate(s seed, output *preparedOutput, reservation privateReservation, check func() error, checkpoint func(attemptPoint) error, observe bool, observer func(*publicationCheckpoint) error) (PublicationResult, *PublicationPreparationFailure) {
	canonical, failure := reservation.acquireObserved(output, func(identity live.FileIdentity) error {
		return observeNotPublished(&s, output.attempt.identityOf(), identity, nameSlotCoordination, AccessPolicyCreatorOnly, observe, observer)
	})
	if failure != nil {
		owner := acquiringOwner(failure.owner)
		return notPublished(s, output, owner, failure.owner.reservation.identity, reservationProblem(failure.cause), checkpoint), nil
	}
	return fromCanonical(s, output, canonical, check, checkpoint, observe, observer)
}

func fromCanonical(s seed, output *preparedOutput, reservation canonicalReservation, check func() error, checkpoint func(attemptPoint) error, observe bool, observer func(*publicationCheckpoint) error) (PublicationResult, *PublicationPreparationFailure) {
	if err := checkpoint(attemptPointReservationAcquired); err != nil {
		owner := canonicalOwner(reservation)
		return notPublished(s, output, owner, reservation.identity, err, checkpoint), nil
	}
	if previous := output.previous; previous != nil {
		if err := previous.verifyContent(output.attempt.destinationOf(), check); err != nil {
			owner := canonicalOwner(reservation)
			return notPublished(s, output, owner, reservation.identity, replacementProblem(err), checkpoint), nil
		}
	}
	armed, failure := reservation.armObserved(output, func(identity live.FileIdentity) error {
		if err := observeOutcomeUnknown(&s, identity, observe, observer); err != nil {
			return err
		}
		return checkpoint(attemptPointState2Selected)
	})
	if failure != nil {
		cause := reservationProblem(failure.cause)
		if failure.owner.state2Selected {
			return outcomeUnknown(s, failure.owner.reservation.identity, cause), nil
		}
		owner := armingOwner(failure.owner)
		return notPublished(s, output, owner, failure.owner.reservation.identity, cause, checkpoint), nil
	}
	return fromArmed(s, output, armed, checkpoint, observe, observer)
}

func fromArmed(s seed, output *preparedOutput, reservation armedReservation, checkpoint func(attemptPoint) error, observe bool, observer func(*publicationCheckpoint) error) (PublicationResult, *PublicationPreparationFailure) {
	reservationIdentity := reservation.identity
	published, failure := publishObserved(output, reservation, func(point mainPoint) error {
		if point == mainPointDesiredProven {
			if err := observePublished(&s, reservationIdentity, observe, observer); err != nil {
				return checkpointFailure(err)
			}
			if err := checkpoint(attemptPointDesiredProven); err != nil {
				return checkpointFailure(err)
			}
		}
		return nil
	})
	if failure == nil {
		return finishPublishedObserved(s, published, nil, observe, observer), nil
	}
	cause := mainProblem(failure.cause)
	if failure.owner.desiredProven {
		return finishPublishedObserved(s, publishedMain{output: failure.owner.output, reservation: failure.owner.reservation}, cause, observe, observer), nil
	}
	return outcomeUnknown(s, failure.owner.reservation.identity, cause), nil
}

func observePreparation(s *seed, output *preparedOutput, reservationIdentity identityOptional, reservationSlot nameSlot, enabled bool, observer func(*publicationCheckpoint) error) error {
	if !enabled {
		return nil
	}
	problem := interruptedProblem()
	checkpointSeed := *s
	cleanup := newCleanupArtifacts()
	outputIdentity := output.attempt.identityOf()
	cleanup.push(checkpointSeed.artifact(ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: outputIdentity}, problem))
	cleanup.push(checkpointSeed.artifact(ArtifactPrivateReservation, reservationSlot, reservationIdentity, problem))
	failure := checkpointSeed.preparationWithHousekeeping(cleanup, HousekeepingNone, nil, problem)
	return observer(&publicationCheckpoint{preparation: &failure})
}

func observeNotPublished(s *seed, outputIdentity live.FileIdentity, reservationIdentity live.FileIdentity, reservationSlot nameSlot, coordinationAccessPolicy AccessPolicy, enabled bool, observer func(*publicationCheckpoint) error) error {
	if !enabled {
		return nil
	}
	problem := interruptedProblem()
	checkpointSeed := *s
	cleanup := newCleanupArtifacts()
	cleanup.push(checkpointSeed.artifact(ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: outputIdentity}, problem))
	cleanup.push(checkpointSeed.artifact(ArtifactPrivateReservation, reservationSlot, identityOptional{present: true, identity: reservationIdentity}, problem))
	result := checkpointSeed.result(finalState{
		reservationIdentity:               reservationIdentity,
		mainNamespaceMayHaveBeenAttempted: false,
		publication:                       PublicationNotPublished,
		destinationContent:                DestinationContentUnclassified,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          coordinationAccessPolicy,
	}, cleanup, problem)
	return observer(&publicationCheckpoint{result: &result})
}

func observeOutcomeUnknown(s *seed, reservationIdentity live.FileIdentity, enabled bool, observer func(*publicationCheckpoint) error) error {
	if !enabled {
		return nil
	}
	result := outcomeUnknown(*s, reservationIdentity, interruptedProblem())
	return observer(&publicationCheckpoint{result: &result})
}

func observePublished(s *seed, reservationIdentity live.FileIdentity, enabled bool, observer func(*publicationCheckpoint) error) error {
	if !enabled {
		return nil
	}
	problem := interruptedProblem()
	checkpointSeed := *s
	cleanup := newCleanupArtifacts()
	cleanup.push(checkpointSeed.artifact(ArtifactPrivateReservation, nameSlotCoordination, identityOptional{present: true, identity: reservationIdentity}, problem))
	result := checkpointSeed.result(finalState{
		reservationIdentity:               reservationIdentity,
		mainNamespaceMayHaveBeenAttempted: true,
		publication:                       PublicationPublished,
		destinationContent:                DestinationContentDesired,
		mainAccessPolicy:                  AccessPolicyCreatorOnly,
		coordinationAccessPolicy:          AccessPolicyChangedOrUnproven,
	}, cleanup, problem)
	return observer(&publicationCheckpoint{result: &result})
}

// interruptedProblem is the fixed problem of an observation built at
// a machine fault (Rust interrupted_problem).
func interruptedProblem() *format.Error {
	return problem(format.CodeIO, "mapped output fault interrupted publication")
}

func preparation(s seed, output *preparedOutput, reservation *reservationOwner, cause error, checkpoint func(attemptPoint) error) *PublicationPreparationFailure {
	summary := discardWith(&s, output, reservation, func(point cleanupPoint) error { return checkpoint(cleanupPointOf(point)) })
	failure := s.preparationWithHousekeeping(summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, cause)
	return &failure
}

func notPublished(s seed, output *preparedOutput, reservation reservationOwner, reservationIdentity live.FileIdentity, cause error, checkpoint func(attemptPoint) error) PublicationResult {
	previousUnchanged := false
	if output.previous != nil {
		previousUnchanged = output.previous.verifyContent(output.attempt.destinationOf(), nil) == nil
	}
	summary := discardWith(&s, output, &reservation, func(point cleanupPoint) error { return checkpoint(cleanupPointOf(point)) })
	content := DestinationContentUnclassified
	if previousUnchanged {
		content = DestinationContentPrevious
	} else if summary.mainAbsent {
		content = DestinationContentAbsent
	}
	coordination := AccessPolicyUnclassified
	if summary.coordinationAbsent {
		coordination = AccessPolicyAbsent
	}
	return s.resultWithHousekeeping(finalState{
		reservationIdentity:               reservationIdentity,
		mainNamespaceMayHaveBeenAttempted: false,
		publication:                       PublicationNotPublished,
		destinationContent:                content,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          coordination,
	}, summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, cause)
}

func outcomeUnknown(s seed, reservationIdentity live.FileIdentity, cause error) PublicationResult {
	return s.result(finalState{
		reservationIdentity:               reservationIdentity,
		mainNamespaceMayHaveBeenAttempted: true,
		publication:                       PublicationOutcomeUnknown,
		destinationContent:                DestinationContentUnclassified,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          AccessPolicyChangedOrUnproven,
	}, newCleanupArtifacts(), cause)
}

func finishPublished(s seed, published publishedMain, cause error) PublicationResult {
	return finishPublishedObserved(s, published, cause, false, func(*publicationCheckpoint) error { return nil })
}

func finishPublishedObserved(s seed, published publishedMain, cause error, observe bool, observer func(*publicationCheckpoint) error) PublicationResult {
	reservationIdentity := published.reservation.identity
	retirement, retirementFailure := published.retire()
	if retirementFailure == nil {
		return s.resultWithHousekeeping(finalState{
			reservationIdentity:               retirement.reservationIdentity,
			mainNamespaceMayHaveBeenAttempted: true,
			publication:                       PublicationPublished,
			destinationContent:                DestinationContentDesired,
			mainAccessPolicy:                  AccessPolicyCreatorOnly,
			coordinationAccessPolicy:          AccessPolicyAbsent,
		}, newCleanupArtifacts(), retirement.housekeeping, retirement.visibleHousekeeping, cause)
	}
	owner := retirementFailure.owner
	retirementProblem := mainProblem(retirementFailure.cause)
	mainAccess := AccessPolicyChangedOrUnproven
	if owner.published.output.verifyMain() == nil {
		mainAccess = AccessPolicyCreatorOnly
	}
	coordination := AccessPolicyChangedOrUnproven
	if owner.reservationRetiredProven {
		coordination = AccessPolicyAbsent
	} else if owner.published.reservation.verifyAfterMain(owner.published.output) == nil {
		coordination = AccessPolicyCreatorOnly
	}
	cleanup := newCleanupArtifacts()
	if !owner.previousRetiredProven {
		if previous := owner.published.output.previous; previous != nil {
			cleanup.push(s.artifact(ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: previous.identity}, retirementProblem))
		}
	}
	if !owner.reservationRetiredProven {
		cleanup.push(s.artifact(ArtifactPrivateReservation, nameSlotCoordination, identityOptional{present: true, identity: reservationIdentity}, retirementProblem))
	}
	finalCause := cause
	if finalCause == nil {
		finalCause = retirementProblem
	}
	return s.resultWithHousekeeping(finalState{
		reservationIdentity:               reservationIdentity,
		mainNamespaceMayHaveBeenAttempted: true,
		publication:                       PublicationPublished,
		destinationContent:                DestinationContentDesired,
		mainAccessPolicy:                  mainAccess,
		coordinationAccessPolicy:          coordination,
	}, cleanup, owner.housekeeping, owner.visibleHousekeeping, finalCause)
}

func draftOwner(d *reservationDraft) reservationOwner {
	return reservationOwner{
		file:        d.file,
		identity:    optionalFrom(d.identity),
		privateName: d.name,
		location:    ownerLocationPrivate,
	}
}

func acquiringOwner(reservation acquiringReservation) reservationOwner {
	return reservationOwner{
		file:        reservation.reservation.file,
		identity:    identityOptional{present: true, identity: reservation.reservation.identity},
		privateName: reservation.reservation.name,
		location:    ownerLocationEither,
	}
}

func canonicalOwner(reservation canonicalReservation) reservationOwner {
	return reservationOwner{
		file:        reservation.file,
		identity:    identityOptional{present: true, identity: reservation.identity},
		privateName: reservation.name,
		location:    ownerLocationCanonical,
	}
}

func armingOwner(reservation armingReservation) reservationOwner {
	return canonicalOwner(reservation.reservation)
}

// optionalFrom converts one pointer identity to the value optional of
// the cleanup machine (nil is the absent arm).
func optionalFrom(identity *live.FileIdentity) identityOptional {
	if identity == nil {
		return identityOptional{}
	}
	return identityOptional{present: true, identity: *identity}
}

// cleanupPointOf maps one cleanup machine checkpoint to the
// publication machine point (Rust cleanup_point).
func cleanupPointOf(point cleanupPoint) attemptPoint {
	switch point {
	case cleanupPointOutputRemoval:
		return attemptPointCleanupOutput
	case cleanupPointReservationRemoval:
		return attemptPointCleanupReservation
	default:
		return attemptPointCleanupDirectorySync
	}
}

// cleanupIgnoresCancellation reports whether one publication point is
// a cleanup checkpoint that never cancels (Rust
// cleanup_ignores_cancellation: cleanup must finish even when the
// caller was cancelled).
func cleanupIgnoresCancellation(point attemptPoint) bool {
	switch point {
	case attemptPointCleanupOutput, attemptPointCleanupReservation, attemptPointCleanupDirectorySync:
		return true
	}
	return false
}
