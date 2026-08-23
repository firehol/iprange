//go:build !windows

// Restart resolution for one exact replacement publication (Rust
// publication/replacement_resolver.rs): the exact reservation and
// the pair inspection unlock first (so the two-inode lifetime locks
// can take their role order), the main and private outputs classify
// against the recorded previous and desired evidence, and the
// previous is completed to desired, restored, or refused with the
// exact cleanup ledger. The inspected pair and the base reservations
// close exactly where Rust drops them.

package publication

// replacementDispatch routes one replacement resolution (Rust
// replacement_resolver::dispatch): the operation lock is released
// for the pair inspection and re-acquired, the main class selects
// the arm, and the destination directory closes at the scope end
// (Rust drops the BaseResolution destination).
func replacementDispatch(base baseResolution, mode resolveMode, check func() error) (result PublicationResult, err error) {
	defer base.destination.directory().Close()
	if err := unlockReplacementReservation(base.exact); err != nil {
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, resolverProblem(err)
	}
	if err := unlockReplacementReservation(base.later); err != nil {
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, resolverProblem(err)
	}
	pair, err := inspectReplacementPair(base.destination, base.header, check)
	if err != nil {
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, err
	}
	if err := relockReplacementReservation(base.exact, base.destination, check); err != nil {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, resolverProblem(err)
	}
	if err := relockReplacementReservation(base.later, base.destination, check); err != nil {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, resolverProblem(err)
	}
	switch {
	case pair.main != nil && pair.main.content == replacementContentPrevious:
		if err := requireNoLater(base.later); err != nil {
			pair.main.closeIfNonNil()
			pair.private.closeIfNonNil()
			closeInspectedReservation(base.exact)
			closeInspectedReservation(base.later)
			return PublicationResult{}, err
		}
		return resolvePreviousReplacement(base, pair, mode, check)
	case pair.main != nil && pair.main.content == replacementContentDesired:
		return resolveDesiredReplacement(base, pair, mode, check)
	case pair.main != nil:
		if err := requireNoLater(base.later); err != nil {
			pair.main.closeIfNonNil()
			pair.private.closeIfNonNil()
			closeInspectedReservation(base.exact)
			closeInspectedReservation(base.later)
			return PublicationResult{}, err
		}
		return resolveOtherReplacement(base, pair, check)
	default:
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, conflictProblem("replacement cannot legitimately leave the destination absent")
	}
}

// resolvePreviousReplacement splits by mode (Rust resolve_previous).
func resolvePreviousReplacement(base baseResolution, pair replacementPair, mode resolveMode, check func() error) (PublicationResult, error) {
	if mode == resolveModeComplete {
		return completePreviousReplacement(base, pair, check)
	}
	return removePreviousReplacement(base, pair, check)
}

// completePreviousReplacement resumes the interrupted replacement
// from its exact previous main, prepared output, and reservation
// (Rust complete_previous): the pair entries move into the previous
// main and the prepared output, the reservation arms, and the attempt
// machine retires the displaced previous.
func completePreviousReplacement(base baseResolution, pair replacementPair, check func() error) (PublicationResult, error) {
	if base.exact == nil {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		return PublicationResult{}, unresolvable("replacement completion requires its exact reservation")
	}
	reservation := base.exact
	base.exact = nil
	if pair.main == nil {
		pair.private.closeIfNonNil()
		closeInspectedReservation(reservation)
		return PublicationResult{}, conflictProblem("replacement previous destination disappeared")
	}
	main := pair.main
	pair.main = nil
	if pair.private == nil {
		_ = main.Close()
		closeInspectedReservation(reservation)
		return PublicationResult{}, unresolvable("replacement completion requires its prepared output")
	}
	private := pair.private
	pair.private = nil
	if err := requirePreviousReplacement(main, base.header); err != nil {
		_ = main.Close()
		_ = private.Close()
		closeInspectedReservation(reservation)
		return PublicationResult{}, err
	}
	if err := requireOutputReplacement(private, base.header); err != nil {
		_ = main.Close()
		_ = private.Close()
		closeInspectedReservation(reservation)
		return PublicationResult{}, err
	}
	previous := &previousMain{
		file:       main.file,
		mapping:    main.mapping,
		identity:   main.identity,
		byteLength: main.byteLength,
		sha512:     main.sha512,
	}
	main.file = nil
	main.mapping = nil
	output, err := resumePreparedOutputReplacement(base.destination, base.header, private, previous)
	if err != nil {
		// The constructor error path already closed the inspected
		// private artifact; the previous main is still owned here
		// and must be closed by this arm (Rust drops the moved
		// PreviousMain inside the failed construction).
		_ = previous.Close()
		closeInspectedReservation(reservation)
		return PublicationResult{}, outputProblem(err)
	}
	private.file = nil
	private.mapping = nil
	if err := checkCancellation(check); err != nil {
		_ = output.Close()
		_ = main.Close()
		closeInspectedReservation(reservation)
		return PublicationResult{}, err
	}
	reservationIdentity := reservation.identity
	armed, armFailure := arm(reservation, output)
	reservation = nil
	if armFailure != nil {
		_ = output.Close()
		if armFailure.unknown {
			return recordCancellation(outcomeUnknown(base.seed, reservationIdentity, armFailure.problem), check), nil
		}
		return PublicationResult{}, armFailure.problem
	}
	result := resumeArmed(base.seed, output, armed)
	_ = output.Close()
	return recordCancellation(result, check), nil
}

// removePreviousReplacement removes the interrupted replacement and
// restores the previous main (Rust remove_previous).
func removePreviousReplacement(base baseResolution, pair replacementPair, check func() error) (PublicationResult, error) {
	if pair.main == nil {
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, conflictProblem("replacement previous destination disappeared")
	}
	if err := requirePreviousReplacement(pair.main, base.header); err != nil {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, err
	}
	return resolveNotDesiredReplacement(base, pair, DestinationContentPrevious, check)
}

// resolveDesiredReplacement resolves a replacement whose main is the
// desired bytes (Rust resolve_desired): the no-rollback remove mode
// is refused, the private artifact is either the recorded output
// (discarded) or a foreign residue (artifact, never removed), and the
// result carries the later-canonical observation.
func resolveDesiredReplacement(base baseResolution, pair replacementPair, mode resolveMode, check func() error) (PublicationResult, error) {
	if base.header.policy == reservationPolicyReplaceExistingNoRollback && mode == resolveModeRemove {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, unresolvable("no-rollback replacement cannot restore a discarded destination")
	}
	if pair.main == nil {
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, conflictProblem("replacement desired destination disappeared")
	}
	if err := synchronizeReplacement(pair.main, base.destination, check); err != nil {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		// Rust propagates every synchronize failure with ? (no
		// outcome-unknown conversion on the replacement arms).
		return PublicationResult{}, err
	}
	owner, removable, foreign := desiredCleanupReplacement(pair.private, base.header)
	var output *outputOwner
	if removable {
		output = &owner
	}
	s := &base.seed
	summary := discardRecovered(s, base.destination, output, reservationOwnerOf(base.exact))
	if foreign != nil {
		summary.artifacts.push(s.artifact(ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: foreign.identity}, conflictProblem("private replacement artifact does not match recorded ownership")))
	}
	verified := resolverProblem(pair.main.verify(base.destination, noopCheck))
	if verified == nil {
		verified = resolverProblem(verifyLater(base.later, base.destination))
	}
	cause := verified
	if cause == nil {
		cause = firstProblem(&summary.artifacts)
	}
	publication := PublicationOutcomeUnknown
	if verified == nil {
		publication = PublicationPublished
	}
	state := finalState{
		reservationIdentity:               reservationIdentityOf(base.header),
		mainNamespaceMayHaveBeenAttempted: attemptedReplacement(base.header.state),
		publication:                       publication,
		destinationContent:                DestinationContentUnclassified,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          AccessPolicyUnclassified,
	}
	if verified == nil {
		state.destinationContent = DestinationContentDesired
		state.mainAccessPolicy = pair.main.access
		state.coordinationAccessPolicy = coordinationAccess(summary, base.exact, base.later)
	}
	result := s.resultWithHousekeeping(state, summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, cause)
	result = withLater(result, base.later)
	pair.main.closeIfNonNil()
	pair.private.closeIfNonNil()
	closeInspectedReservation(base.exact)
	closeInspectedReservation(base.later)
	return recordCancellation(result, check), nil
}

// resolveOtherReplacement resolves a foreign main (Rust
// resolve_other -> resolve_not_desired).
func resolveOtherReplacement(base baseResolution, pair replacementPair, check func() error) (PublicationResult, error) {
	return resolveNotDesiredReplacement(base, pair, DestinationContentOther, check)
}

// resolveNotDesiredReplacement removes the interrupted replacement
// artifacts and restores the classified main (Rust
// resolve_not_desired: the private output is removed only when it is
// the recorded prepared output).
func resolveNotDesiredReplacement(base baseResolution, pair replacementPair, content DestinationContent, check func() error) (PublicationResult, error) {
	if pair.main == nil {
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, conflictProblem("replacement non-desired destination disappeared")
	}
	if err := synchronizeReplacement(pair.main, base.destination, check); err != nil {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		// Rust propagates every synchronize failure with ? (no
		// outcome-unknown conversion on the replacement arms).
		return PublicationResult{}, err
	}
	owner, removable, err := removableReplacementOutput(pair.private, base.header)
	if err != nil {
		pair.main.closeIfNonNil()
		pair.private.closeIfNonNil()
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		return PublicationResult{}, err
	}
	var output *outputOwner
	if removable {
		output = &owner
	}
	s := &base.seed
	summary := discardRecovered(s, base.destination, output, reservationOwnerOf(base.exact))
	verified := resolverProblem(pair.main.verify(base.destination, noopCheck))
	cause := verified
	if cause == nil {
		cause = firstProblem(&summary.artifacts)
	}
	publication := PublicationOutcomeUnknown
	if verified == nil {
		publication = PublicationNotPublished
	}
	state := finalState{
		reservationIdentity:               reservationIdentityOf(base.header),
		mainNamespaceMayHaveBeenAttempted: attemptedReplacement(base.header.state),
		publication:                       publication,
		destinationContent:                DestinationContentUnclassified,
		mainAccessPolicy:                  AccessPolicyUnclassified,
		coordinationAccessPolicy:          AccessPolicyUnclassified,
	}
	if verified == nil {
		state.destinationContent = content
		state.mainAccessPolicy = pair.main.access
		state.coordinationAccessPolicy = coordinationAccess(summary, base.exact, nil)
	}
	result := s.resultWithHousekeeping(state, summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, cause)
	pair.main.closeIfNonNil()
	pair.private.closeIfNonNil()
	closeInspectedReservation(base.exact)
	closeInspectedReservation(base.later)
	return recordCancellation(result, check), nil
}

// removableReplacementOutput keeps the prepared-output owner only
// when the private artifact is exactly the recorded output (Rust
// removable_output: any other private artifact is the conflict
// class). The owner returns by value; the caller takes its address
// only within its own frame.
func removableReplacementOutput(private *inspectedReplacement, header reservationHeader) (outputOwner, bool, error) {
	if private == nil {
		return outputOwner{}, false, nil
	}
	if private.content == replacementContentDesired && reservationIdentityBytes(private.identity) == header.outputIdentity {
		return replacementOwnerOf(private), true, nil
	}
	return outputOwner{}, false, conflictProblem("private replacement artifact does not match the prepared output")
}

// desiredCleanupReplacement separates the recorded private output
// from a foreign private residue (Rust desired_cleanup: the recorded
// output or the recorded previous inode is removed; anything else is
// left as residue with its artifact). The owner returns by value; the
// caller takes its address only within its own frame.
func desiredCleanupReplacement(private *inspectedReplacement, header reservationHeader) (outputOwner, bool, *inspectedReplacement) {
	if private == nil {
		return outputOwner{}, false, nil
	}
	if private.content == replacementContentDesired && reservationIdentityBytes(private.identity) == header.outputIdentity {
		return replacementOwnerOf(private), true, nil
	}
	if private.content == replacementContentPrevious && header.previousPresent && reservationIdentityBytes(private.identity) == header.previous.identity {
		return replacementOwnerOf(private), true, nil
	}
	return outputOwner{}, false, private
}

// replacementOwnerOf builds the cleanup owner of one inspected
// replacement (Rust owner).
func replacementOwnerOf(entry *inspectedReplacement) outputOwner {
	return outputOwner{
		file:     entry.file,
		identity: entry.identity,
		name:     entry.name,
	}
}

// requireOutputReplacement refuses one prepared output that no
// longer matches the reservation (Rust require_output: private
// placement, desired content, recorded identity, creator-only
// access).
func requireOutputReplacement(output *inspectedReplacement, header reservationHeader) error {
	if output.location != outputLocationPrivate ||
		output.content != replacementContentDesired ||
		reservationIdentityBytes(output.identity) != header.outputIdentity ||
		output.access != AccessPolicyCreatorOnly {
		return unresolvable("replacement prepared output does not match its reservation")
	}
	return nil
}

// requirePreviousReplacement refuses one previous main that no
// longer matches the recorded evidence (Rust require_previous).
func requirePreviousReplacement(previous *inspectedReplacement, header reservationHeader) error {
	if !header.previousPresent {
		return conflictProblem("replacement previous evidence is missing")
	}
	if previous.content != replacementContentPrevious || reservationIdentityBytes(previous.identity) != header.previous.identity {
		return conflictProblem("replacement destination no longer matches previous evidence")
	}
	return nil
}

// synchronizeReplacement durably flushes one inspected main and
// re-proves it (Rust replacement_resolver::synchronize).
func synchronizeReplacement(main *inspectedReplacement, destination *destination, check func() error) error {
	if err := checkCancellation(check); err != nil {
		return err
	}
	if err := syncOutputFile(main.file); err != nil {
		return resolverProblem(err)
	}
	return main.verify(destination, check)
}

// unlockReplacementReservation releases the operation lock of one
// base reservation before the pair inspection (Rust unlock).
func unlockReplacementReservation(reservation *inspectedReservation) error {
	if reservation == nil {
		return nil
	}
	return reservation.unlockOperation()
}

// relockReplacementReservation re-acquires the operation lock and
// re-proves one base reservation after the pair inspection (Rust
// relock).
func relockReplacementReservation(reservation *inspectedReservation, destination *destination, check func() error) error {
	if reservation == nil {
		return nil
	}
	return reservation.relockOperation(destination, check)
}

// attemptedReplacement reports whether one header state marks the
// main namespace attempted (Rust attempted).
func attemptedReplacement(state reservationState) bool {
	return state == reservationStateMainMayHaveBeenAttempted
}
