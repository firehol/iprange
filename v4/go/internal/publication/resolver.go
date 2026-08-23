//go:build !windows

// Restart completion or removal of one exact fail-if-exists
// publication (Rust publication/resolver.rs): the authority is
// reconciled, the main output classified against the reservation
// record, and the attempt is completed, abandoned, or left
// unresolved with the exact cleanup ledger and access facts. Every
// functionally-owned inspected value (output and reservation) is
// closed exactly where Rust drops it. The replacement-policy branch
// of the Rust resolve entry is recorded with the slice-K replacement
// resolver; no caller can pass a replacement header before that
// slice lands (the public ResolvePublication surface is slice N).

package publication

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// resolveMode selects completion or removal of one interrupted
// publication (Rust Mode).
type resolveMode uint8

const (
	resolveModeComplete resolveMode = iota
	resolveModeRemove
)

// resolve resolves one interrupted fail-if-exists publication at path
// (Rust resolver::resolve). supplied carries the caller's own
// publication result when one exists; mode selects completion or
// removal; check is the cancellation checkpoint.
func resolve(path string, supplied *PublicationResult, mode resolveMode, check func() error) (PublicationResult, error) {
	base, err := inspectResolution(path, supplied, check)
	if err != nil {
		return PublicationResult{}, err
	}
	// base.exact and base.later are owned by this call; every arm of
	// dispatch closes or consumes them, and the error path closes
	// them here (Rust drops the resolution value on Err).
	main, err := inspectMainOutput(base.destination, base.header, check)
	if err != nil {
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		base.destination.directory().Close()
		return PublicationResult{}, resolverProblem(err)
	}
	result, err := dispatch(resolution{
		destination: base.destination,
		header:      base.header,
		seed:        base.seed,
		exact:       base.exact,
		later:       base.later,
		main:        main,
	}, mode, check)
	if err != nil {
		closeInspectedReservation(base.exact)
		closeInspectedReservation(base.later)
		if main != nil {
			_ = main.Close()
		}
		base.destination.directory().Close()
		return PublicationResult{}, resolverProblem(err)
	}
	base.destination.directory().Close()
	return result, nil
}

// closeInspectedReservation releases one inspected reservation when
// the resolver still owns it (Rust drops the moved value on the
// error path).
func closeInspectedReservation(reservation *inspectedReservation) {
	if reservation != nil {
		_ = reservation.Close()
	}
}

// resolution is one fully inspected non-replacement resolution (Rust
// Resolution).
type resolution struct {
	destination *destination
	header      reservationHeader
	seed        seed
	exact       *inspectedReservation
	later       *inspectedReservation
	main        *inspectedOutput
}

// dispatch classifies one inspected main and runs the exact arm
// (Rust dispatch). Each arm takes ownership of the exact/later/main
// values and closes them on its own terminal paths; on a propagated
// error the caller (resolve) closes them.
func dispatch(resolution resolution, mode resolveMode, check func() error) (PublicationResult, error) {
	switch {
	case resolution.main != nil && resolution.main.content == outputContentDesired:
		return resolveDesired(resolution.destination, resolution.header, resolution.seed, resolution.exact, resolution.later, resolution.main, check)
	case resolution.main != nil:
		return resolveOther(resolution.destination, resolution.header, resolution.seed, resolution.exact, resolution.later, resolution.main, check)
	default:
		return resolveAbsent(resolution.destination, resolution.header, resolution.seed, resolution.exact, resolution.later, mode, check)
	}
}

// resolveOther resolves a main that is present but not the desired
// bytes (Rust resolve_other): a synchronize failure is the unprovable
// outcome with no discard, otherwise the foreign main is preserved
// and the attempt artifacts are discarded.
func resolveOther(destination *destination, header reservationHeader, s seed, exact, later *inspectedReservation, main *inspectedOutput, check func() error) (PublicationResult, error) {
	if err := requireNoLater(later); err != nil {
		return PublicationResult{}, err
	}
	if err := synchronize(destination, main, check); err != nil {
		if isCancelled(err) {
			return PublicationResult{}, err
		}
		// Rust drops the owned exact reservation and inspected main
		// at the end of resolve_other; nothing was discarded on this
		// arm.
		closeInspectedReservation(exact)
		if main != nil {
			_ = main.Close()
		}
		return outcomeUnknown(s, reservationIdentityOf(header), resolverProblem(err)), nil
	}
	return abandon(destination, header, s, exact, main, DestinationContentOther, check)
}

// resolveAbsent resolves an absent main (Rust resolve_absent): remove
// mode discards everything; complete mode requires the exact private
// output and reservation to resume the attempt.
func resolveAbsent(destination *destination, header reservationHeader, s seed, exact, later *inspectedReservation, mode resolveMode, check func() error) (PublicationResult, error) {
	if err := requireNoLater(later); err != nil {
		return PublicationResult{}, err
	}
	if mode == resolveModeComplete {
		return completeAbsent(destination, header, s, exact, check)
	}
	return abandon(destination, header, s, exact, nil, DestinationContentAbsent, check)
}

// completeAbsent resumes the interrupted attempt from its exact
// private output and reservation (Rust complete_absent). The
// inspected reservation is consumed by arm (the machine closes the
// owner); the resumed prepared output is closed by this function on
// every terminal path, exactly where Rust drops it.
func completeAbsent(destination *destination, header reservationHeader, s seed, reservation *inspectedReservation, check func() error) (PublicationResult, error) {
	if reservation == nil {
		return PublicationResult{}, unresolvable("completion requires the exact recorded reservation inode")
	}
	inspected, err := inspectPrivateOutputExact(destination, header, check)
	if err != nil {
		return PublicationResult{}, resolverProblem(err)
	}
	if inspected == nil {
		return PublicationResult{}, unresolvable("completion requires the exact prepared output inode")
	}
	output, err := resumePreparedOutput(destination, header, inspected)
	if err != nil {
		return PublicationResult{}, outputProblem(err)
	}
	if err := checkCancellation(check); err != nil {
		_ = output.Close()
		return PublicationResult{}, err
	}
	reservationIdentity := reservation.identity
	armed, armFailure := arm(reservation, output)
	if armFailure != nil {
		_ = output.Close()
		if armFailure.unknown {
			return recordCancellation(outcomeUnknown(s, reservationIdentity, armFailure.problem), check), nil
		}
		return PublicationResult{}, armFailure.problem
	}
	result := resumeArmed(s, output, armed)
	_ = output.Close()
	return recordCancellation(result, check), nil
}

// armFailure classes one arm refusal (Rust ArmFailure): unknown means
// the durable state-2 record was selected so the outcome is
// unprovable; the plain problem is a refusal before that selection.
type armFailure struct {
	problem error
	unknown bool
}

// arm resumes one inspected reservation into its armed state (Rust
// arm). The inspected fields are moved into the machine owner; on an
// acquisition or arm refusal the failure owner is closed exactly
// where Rust drops it, and the inspected value must not be closed by
// the caller afterwards.
func arm(inspected *inspectedReservation, output *preparedOutput) (armedReservation, *armFailure) {
	var canonical canonicalReservation
	switch inspected.location {
	case reservationLocationPrivate:
		private := privateReservation{
			name:     inspected.name,
			file:     inspected.file,
			mapping:  inspected.mapping,
			identity: inspected.identity,
			header:   inspected.header,
		}
		acquired, failure := private.acquire(output)
		if failure != nil {
			problem := reservationProblem(failure.cause)
			if isNamespaceExists(failure.cause) {
				_ = failure.owner.reservation.Close()
				return armedReservation{}, &armFailure{problem: conflictProblem("another coordination inode won publication")}
			}
			unknown := failure.owner.namespaceCallStarted
			_ = failure.owner.reservation.Close()
			return armedReservation{}, &armFailure{problem: problem, unknown: unknown}
		}
		canonical = acquired
	default:
		canonical = canonicalReservation{
			name:     inspected.name,
			file:     inspected.file,
			mapping:  inspected.mapping,
			identity: inspected.identity,
			header:   inspected.header,
		}
	}
	switch canonical.header.state {
	case reservationStatePrepared:
		armed, failure := canonical.arm(output)
		if failure != nil {
			problem := reservationProblem(failure.cause)
			unknown := failure.owner.state2Selected
			_ = failure.owner.reservation.Close()
			return armedReservation{}, &armFailure{problem: problem, unknown: unknown}
		}
		return armed, nil
	default: // reservationStateMainMayHaveBeenAttempted
		armed, failure := canonical.resumeArmed(output)
		if failure != nil {
			problem := reservationProblem(failure.cause)
			_ = failure.owner.Close()
			return armedReservation{}, &armFailure{problem: problem}
		}
		return armed, nil
	}
}

// resolveDesired resolves a main that exactly matches the
// reservation record (Rust resolve_desired): the desired main stays
// published, the exact private output and reservation are discarded,
// and any later canonical owner is retained and reported. All four
// inspected values are closed at the scope end exactly where Rust
// drops them.
func resolveDesired(destination *destination, header reservationHeader, s seed, reservation, later *inspectedReservation, main *inspectedOutput, check func() error) (result PublicationResult, err error) {
	defer func() {
		if main != nil {
			_ = main.Close()
		}
		if later != nil {
			_ = later.Close()
		}
		if reservation != nil {
			_ = reservation.Close()
		}
	}()
	var private *inspectedOutput
	defer func() {
		if private != nil {
			_ = private.Close()
		}
	}()
	if err := synchronize(destination, main, check); err != nil {
		if isCancelled(err) {
			return PublicationResult{}, err
		}
		return recordCancellation(outcomeUnknown(s, reservationIdentityOf(header), resolverProblem(err)), check), nil
	}
	private, err = inspectPrivateOutput(destination, header, check)
	if err != nil {
		if isCancelled(err) {
			return PublicationResult{}, err
		}
		if err := checkCancellation(check); err != nil {
			return PublicationResult{}, err
		}
		cleanupCause := resolverProblem(err)
		summary := discardRecovered(&s, destination, nil, reservationOwnerOf(reservation))
		summary.artifacts.push(s.artifact(ArtifactPrivateOutput, nameSlotPrivateOutput, identityOptional{present: true, identity: identityFromEncoded(header.outputIdentity)}, cleanupCause))
		computed, finalErr := finalLater(destination, header, reservation, later, summary)
		if finalErr != nil {
			return recordCancellation(desiredProblem(s, header, summary, finalErr), check), nil
		}
		if computed != nil && computed != later {
			defer func() { _ = computed.Close() }()
		}
		return recordCancellation(publishedOutputResult(s, summary, cleanupCause, desiredContext{
			destination: destination, header: header, reservation: reservation, later: computed, main: main,
		}), check), nil
	}
	if err := checkCancellation(check); err != nil {
		return PublicationResult{}, err
	}
	var privateOwner *outputOwner
	if private != nil {
		owner := outputOwnerOf(private)
		privateOwner = &owner
	}
	summary := discardRecovered(&s, destination, privateOwner, reservationOwnerOf(reservation))
	computed, finalErr := finalLater(destination, header, reservation, later, summary)
	if finalErr != nil {
		return recordCancellation(desiredProblem(s, header, summary, finalErr), check), nil
	}
	if computed != nil && computed != later {
		defer func() { _ = computed.Close() }()
	}
	return recordCancellation(desiredResult(s, summary, desiredContext{
		destination: destination, header: header, reservation: reservation, later: computed, main: main,
	}), check), nil
}

// abandon discards one interrupted attempt without resuming it (Rust
// abandon): the private output and reservation are removed, the
// destination is re-proved against the classified content, and the
// result is NotPublished only when every proof holds.
func abandon(destination *destination, header reservationHeader, s seed, reservation *inspectedReservation, main *inspectedOutput, content DestinationContent, check func() error) (PublicationResult, error) {
	if reservation != nil {
		defer func() { _ = reservation.Close() }()
	}
	if main != nil {
		defer func() { _ = main.Close() }()
	}
	private, err := inspectPrivateOutput(destination, header, check)
	if err != nil {
		return PublicationResult{}, resolverProblem(err)
	}
	if private != nil {
		defer func() { _ = private.Close() }()
	}
	if err := checkCancellation(check); err != nil {
		return PublicationResult{}, err
	}
	var privateOwner *outputOwner
	if private != nil {
		owner := outputOwnerOf(private)
		privateOwner = &owner
	}
	summary := discardRecovered(&s, destination, privateOwner, reservationOwnerOf(reservation))
	verification := verifyDestination(destination, content, main, summary)
	if verification == nil {
		verification = verifyNoLater(destination, reservation, summary)
	}
	cause := verification
	if cause == nil {
		cause = firstProblem(&summary.artifacts)
	}
	publication := PublicationNotPublished
	if verification != nil {
		publication = PublicationOutcomeUnknown
	}
	mainAccess := AccessPolicyUnclassified
	coordination := AccessPolicyUnclassified
	destinationContent := content
	if publication == PublicationNotPublished {
		if main != nil {
			mainAccess = main.access
		} else {
			mainAccess = AccessPolicyAbsent
		}
		coordination = coordinationAccess(summary, reservation, nil)
	}
	result := s.resultWithHousekeeping(finalState{
		reservationIdentity:               reservationIdentityOf(header),
		mainNamespaceMayHaveBeenAttempted: header.state == reservationStateMainMayHaveBeenAttempted,
		publication:                       publication,
		destinationContent:                destinationContent,
		mainAccessPolicy:                  mainAccess,
		coordinationAccessPolicy:          coordination,
	}, summary.artifacts, summary.housekeeping, summary.visibleHousekeeping, cause)
	if publication != PublicationNotPublished {
		result.DestinationContent = DestinationContentUnclassified
		result.MainAccessPolicy = AccessPolicyUnclassified
		result.CoordinationAccessPolicy = AccessPolicyUnclassified
	}
	return recordCancellation(result, check), nil
}

// outputOwnerOf builds the cleanup owner of one inspected output
// (Rust output_owner).
func outputOwnerOf(inspected *inspectedOutput) outputOwner {
	return outputOwner{
		file:     inspected.file,
		identity: inspected.identity,
		name:     inspected.name,
	}
}

// reservationOwnerOf builds the cleanup owner of one inspected
// reservation (Rust reservation_owner; nil stays nil so the discard
// machines keep their Option shape).
func reservationOwnerOf(reservation *inspectedReservation) *reservationOwner {
	if reservation == nil {
		return nil
	}
	return &reservationOwner{
		file:        reservation.file,
		identity:    identityOptional{present: true, identity: reservation.identity},
		privateName: reservation.name,
		location:    ownerLocationOf(reservation.location),
	}
}

// ownerLocationOf maps one inspected reservation location to the
// cleanup owner location (Rust ReservationLocation).
func ownerLocationOf(location reservationLocation) ownerLocation {
	if location == reservationLocationCanonical {
		return ownerLocationCanonical
	}
	return ownerLocationPrivate
}

// verifyLater re-proves one later canonical reservation (Rust
// verify_later).
func verifyLater(later *inspectedReservation, destination *destination) error {
	if later == nil {
		return nil
	}
	return resolverProblem(later.verify(destination))
}

// reservationIdentityOf decodes the reservation identity of one
// header (Rust reservation_identity, an expect on the selected
// record).
func reservationIdentityOf(header reservationHeader) live.FileIdentity {
	device, inode, ok := identityFromEncodedBytes(header.reservationIdentity)
	if !ok {
		panic("selected reservation identity is valid")
	}
	return live.IdentityFromDeviceInode(device, inode)
}

// identityFromEncoded decodes one encoded identity payload (Rust
// Identity::decode, valid on the written records).
func identityFromEncoded(bytes [32]byte) live.FileIdentity {
	device, inode, ok := identityFromEncodedBytes(bytes)
	if !ok {
		panic("encoded identity is valid")
	}
	return live.IdentityFromDeviceInode(device, inode)
}

// identityFromEncodedBytes decodes one raw identity payload (Rust
// Identity::decode: device and inode little-endian at bytes 0..16,
// zero tail) via the portable local identity kind check.
func identityFromEncodedBytes(bytes [32]byte) (device, inode uint64, ok bool) {
	return LocalFileIdentity{Kind: identityKind, Bytes: bytes}.deviceInode()
}

// requireNoLater refuses a later canonical owner (Rust
// require_no_later).
func requireNoLater(later *inspectedReservation) error {
	if later != nil {
		return conflictProblem("another publication reservation currently owns the destination")
	}
	return nil
}

// unresolvable builds the fixed unresolvable problem (Rust
// unresolvable).
func unresolvable(detail string) *format.Error {
	return problem(format.CodeUnresolvable, detail)
}

// isCancelled reports whether one problem is the cancellation class
// (Rust ErrorCode::Cancelled).
func isCancelled(err error) bool {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return false
	}
	return fe.Code == format.CodeCancelled
}

// isNamespaceExists reports whether one error is the namespace
// exists class (Rust Error::Namespace(NamespaceError::Exists)).
func isNamespaceExists(err error) bool {
	var ne *live.NamespaceError
	if errors.As(err, &ne) {
		return ne.Kind == live.NamespaceExists
	}
	return false
}

// resolverProblem folds one resolver boundary error into the exact
// problem class (Rust map_err(Problem::namespace)/Problem::sdk at the
// inspection and verification boundaries): namespace errors map to
// their fixed publication classes, formatted problems pass through,
// and everything else becomes the SDK class.
func resolverProblem(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := live.AsNamespaceError(err); ok {
		return namespaceProblem(err)
	}
	var fe *format.Error
	if errors.As(err, &fe) {
		return fe
	}
	return sdkProblem(err)
}
