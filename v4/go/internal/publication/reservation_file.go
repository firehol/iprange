//go:build !windows

// Durable ownership states for one publication reservation inode
// (Rust publication/reservation_file.rs). The reservation file is the
// two-page dual-block record created privately, renamed to the
// coordination twin, and armed with state 2; every state transition
// re-proves the exact custody facts at the exact physical steps. The
// observed checkpoint variants and the worker enter_output probes are
// recorded with the 4-10/4-11 chunks; this file ports the plain
// variants with their crash points.

package publication

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// reservationDraft is the created but not yet initialized reservation
// (Rust ReservationDraft): the private name and file with the
// mapping, identity, and header proven during prepare_header.
type reservationDraft struct {
	name           string
	file           *os.File
	mapping        *mapping.Mapping
	identity       *live.FileIdentity
	header         *reservationHeader
	state1Selected bool
}

// Close releases the draft resources (Rust drop; the cleanup machine
// consumes the same owner fields later).
func (d *reservationDraft) Close() error { return closeReservationOwner(d.file, d.mapping) }

// createReservationDraft creates the private reservation name and
// file of one attempt (Rust ReservationDraft::create).
func createReservationDraft(output *preparedOutput) (*reservationDraft, error) {
	if err := output.verifyPrivate(); err != nil {
		return nil, err
	}
	destination := output.attempt.destinationOf()
	name, err := destination.reservationName(output.attempt.attemptIDOf())
	if err != nil {
		return nil, err
	}
	file, err := destination.create(name)
	if err != nil {
		return nil, err
	}
	return &reservationDraft{name: name, file: file}, nil
}

// reservationDraftFailure is one initialization failure carrying the
// still-owned draft (Rust Failure<ReservationDraft>).
type reservationDraftFailure struct {
	owner *reservationDraft
	cause error
}

func (f *reservationDraftFailure) Error() string { return f.cause.Error() }
func (f *reservationDraftFailure) Unwrap() error { return f.cause }

// initialize runs prepare, state-1 write, and the state-1 lock and
// proof without a checkpoint (Rust ReservationDraft::initialize).
func (d *reservationDraft) initialize(output *preparedOutput) (*privateReservation, *reservationDraftFailure) {
	return d.initializeObserved(output, nil)
}

// initializeObserved runs the same machine with one exact checkpoint
// after the state-1 selection, before the operation lock (Rust
// ReservationDraft::initialize_observed; a failing checkpoint is the
// Error::Checkpoint clone-through arm).
func (d *reservationDraft) initializeObserved(output *preparedOutput, afterSelection func(live.FileIdentity) error) (*privateReservation, *reservationDraftFailure) {
	if err := initializeReservationObserved(d, output, afterSelection); err != nil {
		return nil, &reservationDraftFailure{owner: d, cause: err}
	}
	// The initialized reservation fields are present by construction:
	// the machine succeeded through prepare_header.
	return &privateReservation{
		name:     d.name,
		file:     d.file,
		mapping:  d.mapping,
		identity: *d.identity,
		header:   *d.header,
	}, nil
}

func initializeReservation(d *reservationDraft, output *preparedOutput) error {
	return initializeReservationObserved(d, output, nil)
}

func initializeReservationObserved(d *reservationDraft, output *preparedOutput, afterSelection func(live.FileIdentity) error) error {
	if err := prepareHeader(d, output); err != nil {
		return err
	}
	if err := writeState1(d); err != nil {
		return err
	}
	return lockState1With(d, output, afterSelection)
}

// prepareHeader proves the destination state, binds the reservation
// inode identity, sizes and maps the file, and builds the state-1
// header (Rust prepare_header).
func prepareHeader(d *reservationDraft, output *preparedOutput) error {
	if err := output.verifyPrivate(); err != nil {
		return err
	}
	if output.previous != nil {
		if err := output.verifyDestinationBeforeMain(); err != nil {
			return err
		}
	}
	destination := output.attempt.destinationOf()
	directory := destination.directory()
	identity, err := live.RegularIdentity(d.file, directory.Identity())
	if err != nil {
		return err
	}
	d.identity = &identity
	if err := directory.VerifyName(d.name, identity); err != nil {
		return err
	}
	if err := destination.secureCreated(d.file); err != nil {
		return err
	}
	if err := d.file.Truncate(reservationFileSize); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "truncate: " + err.Error()}
	}
	mapped, err := mapping.MapFile(d.file, reservationFileSize, true)
	if err != nil {
		return err
	}
	d.mapping = mapped
	d.header = reservationHeaderFor(output, identity)
	return nil
}

// writeState1 encodes and durably flushes the state-1 record (Rust
// write_state1).
func writeState1(d *reservationDraft) error {
	if d.mapping == nil || d.header == nil {
		return reservationHeaderInvariantError()
	}
	page, err := d.mapping.Page(0)
	if err != nil {
		return err
	}
	if err := d.header.encodeReservationHeader(page); err != nil {
		return err
	}
	if err := d.mapping.FlushPage(0); err != nil {
		return err
	}
	if err := live.SyncFile(d.file); err != nil {
		return err
	}
	fault.Crash("publication.after_reservation_state1_sync")
	return nil
}

// lockState1With proves the state-1 record, remembers the selection,
// runs the optional selection checkpoint, takes the operation lock,
// and re-proves the record (Rust lock_state1_with; the checkpoint
// failure is the Error::Checkpoint arm).
func lockState1With(d *reservationDraft, output *preparedOutput, afterSelection func(live.FileIdentity) error) error {
	if d.mapping == nil || d.header == nil || d.identity == nil {
		return reservationHeaderInvariantError()
	}
	header := *d.header
	if err := verifyDraftPrivate(d, output, header, 0); err != nil {
		return err
	}
	d.state1Selected = true
	if afterSelection != nil {
		if err := afterSelection(*d.identity); err != nil {
			return checkpointFailure(err)
		}
	}
	if err := live.LockFile(d.file, reservationOperationLock, live.LockExclusive); err != nil {
		return err
	}
	return verifyDraftPrivate(d, output, header, 0)
}

// checkpointFailure wraps one machine checkpoint problem in the
// Error::Checkpoint clone-through class (Rust maps the publication
// problem into reservation_file::Error::Checkpoint /
// main_file::Error::Checkpoint; the composition folds return it
// unchanged).
func checkpointFailure(err error) error {
	var fe *format.Error
	if errors.As(err, &fe) {
		return &checkpointProblem{problem: fe}
	}
	return &checkpointProblem{problem: sdkProblem(err)}
}

// verifyDraftPrivate proves the initialized draft reservation at its
// private position (Rust verify_private).
func verifyDraftPrivate(d *reservationDraft, output *preparedOutput, header reservationHeader, block int) error {
	if d.mapping == nil || d.identity == nil {
		return reservationHeaderInvariantError()
	}
	return verifyReservation(d.file, d.mapping, output, reservationExpected{
		identity:            *d.identity,
		privateName:         d.name,
		header:              header,
		block:               block,
		reservationLocation: reservationLocationPrivate,
		outputLocation:      outputLocationPrivate,
	})
}

// privateReservation is the initialized reservation at its private
// dot name with the operation lock held (Rust PrivateReservation).
type privateReservation struct {
	name     string
	file     *os.File
	mapping  *mapping.Mapping
	identity live.FileIdentity
	header   reservationHeader
}

// Close releases the reservation resources (Rust drop).
func (p *privateReservation) Close() error { return closeReservationOwner(p.file, p.mapping) }

// acquire moves the reservation to the canonical coordination twin
// with an atomic no-replace rename and no checkpoint (Rust
// PrivateReservation::acquire).
func (p *privateReservation) acquire(output *preparedOutput) (canonicalReservation, *acquiringReservationFailure) {
	return p.acquireObserved(output, nil)
}

// acquireObserved runs the same machine with one exact checkpoint
// between the atomic rename and the directory sync (Rust
// PrivateReservation::acquire_observed; the checkpoint failure is the
// Error::Checkpoint arm).
func (p *privateReservation) acquireObserved(output *preparedOutput, afterRename func(live.FileIdentity) error) (canonicalReservation, *acquiringReservationFailure) {
	owner := acquiringReservation{reservation: *p}
	if err := acquireReservation(&owner, output, afterRename); err != nil {
		return canonicalReservation{}, &acquiringReservationFailure{owner: owner, cause: err}
	}
	return canonicalReservation{
		name:     owner.reservation.name,
		file:     owner.reservation.file,
		mapping:  owner.reservation.mapping,
		identity: owner.reservation.identity,
		header:   owner.reservation.header,
	}, nil
}

// acquiringReservation is the acquire failure owner: the private
// reservation plus whether the namespace call started (Rust
// AcquiringReservation).
type acquiringReservation struct {
	reservation          privateReservation
	namespaceCallStarted bool
}

// acquiringReservationFailure is one acquisition failure carrying the
// still-owned acquiring reservation (Rust
// Failure<AcquiringReservation>). The owner rides by value so the
// success path of the machine stays on the stack (Rust moves the
// owner; Go copies it into the failure only on the failure path).
type acquiringReservationFailure struct {
	owner acquiringReservation
	cause error
}

func (f *acquiringReservationFailure) Error() string { return f.cause.Error() }
func (f *acquiringReservationFailure) Unwrap() error { return f.cause }

// acquireReservation re-proves the private reservation, verifies the
// directory, renames it to the coordination twin, runs the optional
// after-rename checkpoint, syncs the directory, and re-proves the
// canonical placement (Rust acquire).
func acquireReservation(owner *acquiringReservation, output *preparedOutput, afterRename func(live.FileIdentity) error) error {
	if err := verifyPrivateReservation(&owner.reservation, output); err != nil {
		return err
	}
	destination := output.attempt.destinationOf()
	if err := destination.directory().Verify(); err != nil {
		return err
	}
	owner.namespaceCallStarted = true
	if err := destination.directory().RenameNoReplace(owner.reservation.name, owner.reservation.file, destination.coordinationName()); err != nil {
		return err
	}
	if afterRename != nil {
		if err := afterRename(owner.reservation.identity); err != nil {
			return checkpointFailure(err)
		}
	}
	fault.Crash("publication.after_reservation_rename")
	if err := destination.directory().Sync(); err != nil {
		return err
	}
	fault.Crash("publication.after_reservation_directory_sync")
	return verifyCanonicalAt(
		owner.reservation.file,
		owner.reservation.mapping,
		output,
		canonicalExpected(
			owner.reservation.identity,
			owner.reservation.name,
			owner.reservation.header,
			selectedBlock(owner.reservation.header),
			outputLocationPrivate,
		),
	)
}

// verifyPrivateReservation proves one initialized private reservation
// (Rust verify_private_reservation).
func verifyPrivateReservation(reservation *privateReservation, output *preparedOutput) error {
	return verifyReservation(reservation.file, reservation.mapping, output, reservationExpected{
		identity:            reservation.identity,
		privateName:         reservation.name,
		header:              reservation.header,
		block:               selectedBlock(reservation.header),
		reservationLocation: reservationLocationPrivate,
		outputLocation:      outputLocationPrivate,
	})
}

// canonicalReservation is the reservation under its coordination twin
// after the atomic rename (Rust CanonicalReservation).
type canonicalReservation struct {
	name     string
	file     *os.File
	mapping  *mapping.Mapping
	identity live.FileIdentity
	header   reservationHeader
}

// Close releases the reservation resources (Rust drop).
func (c *canonicalReservation) Close() error { return closeReservationOwner(c.file, c.mapping) }

// arm writes and selects state 2 over the canonical reservation with
// no checkpoint (Rust CanonicalReservation::arm).
func (c *canonicalReservation) arm(output *preparedOutput) (armedReservation, *armingReservationFailure) {
	return c.armObserved(output, nil)
}

// armObserved runs the same machine with one exact checkpoint after
// the state-2 selection and before the final canonical proof (Rust
// CanonicalReservation::arm_observed; the checkpoint failure is the
// Error::Checkpoint arm).
func (c *canonicalReservation) armObserved(output *preparedOutput, afterSelection func(live.FileIdentity) error) (armedReservation, *armingReservationFailure) {
	target, ok := c.header.state2()
	if !ok {
		return armedReservation{}, &armingReservationFailure{
			owner: armingReservation{reservation: *c, target: &target},
			cause: reservationHeaderInvariantError(),
		}
	}
	owner := armingReservation{reservation: *c, target: &target}
	if err := armWith(&owner, output, afterSelection); err != nil {
		return armedReservation{}, &armingReservationFailure{owner: owner, cause: err}
	}
	return armedReservation{
		name:     owner.reservation.name,
		file:     owner.reservation.file,
		mapping:  owner.reservation.mapping,
		identity: owner.reservation.identity,
		header:   target,
	}, nil
}

// resumeArmed resumes an armed reservation after a crash (Rust
// CanonicalReservation::resume_armed: state 2 must already be
// selected).
func (c *canonicalReservation) resumeArmed(output *preparedOutput) (armedReservation, *canonicalReservationFailure) {
	if c.header.state != reservationStateMainMayHaveBeenAttempted || c.header.sequence != 2 {
		return armedReservation{}, &canonicalReservationFailure{owner: *c, cause: reservationHeaderInvariantError()}
	}
	if err := verifyCanonicalReservation(c, output); err != nil {
		return armedReservation{}, &canonicalReservationFailure{owner: *c, cause: err}
	}
	return armedReservation{
		name:     c.name,
		file:     c.file,
		mapping:  c.mapping,
		identity: c.identity,
		header:   c.header,
	}, nil
}

// canonicalReservationFailure is one resume failure carrying the
// still-owned canonical reservation (Rust Failure<CanonicalReservation>).
type canonicalReservationFailure struct {
	owner canonicalReservation
	cause error
}

func (f *canonicalReservationFailure) Error() string { return f.cause.Error() }
func (f *canonicalReservationFailure) Unwrap() error { return f.cause }

// armingReservation is the arm failure owner: the canonical
// reservation, the derived state-2 target, and whether the durable
// state 2 was selected (Rust ArmingReservation).
type armingReservation struct {
	reservation    canonicalReservation
	target         *reservationHeader
	state2Selected bool
}

// armingReservationFailure is one arm failure carrying the still-owned
// arming reservation (Rust Failure<ArmingReservation>).
type armingReservationFailure struct {
	owner armingReservation
	cause error
}

func (f *armingReservationFailure) Error() string { return f.cause.Error() }
func (f *armingReservationFailure) Unwrap() error { return f.cause }

// armWith proves the canonical reservation, writes and selects the
// state-2 record at the exact physical steps, runs the optional
// after-selection checkpoint, and re-proves the canonical placement
// (Rust arm_with).
func armWith(owner *armingReservation, output *preparedOutput, afterSelection func(live.FileIdentity) error) error {
	if owner.target == nil {
		return reservationHeaderInvariantError()
	}
	if err := verifyCanonicalReservation(&owner.reservation, output); err != nil {
		return err
	}
	destination := output.attempt.destinationOf()
	if output.previous != nil {
		if err := output.verifyDestinationBeforeMain(); err != nil {
			return err
		}
	} else if err := destination.directory().RequireAbsent(destination.mainName()); err != nil {
		return err
	}
	target := *owner.target
	page, err := owner.reservation.mapping.Page(1)
	if err != nil {
		return err
	}
	if err := target.encodeReservationHeader(page); err != nil {
		return err
	}
	if err := owner.reservation.mapping.FlushPage(1); err != nil {
		return err
	}
	fault.Crash("publication.after_reservation_state2_write")
	if err := live.SyncFile(owner.reservation.file); err != nil {
		return err
	}
	fault.Crash("publication.after_reservation_state2_sync")
	if err := selectExact(owner.reservation.mapping, target, 1); err != nil {
		return err
	}
	owner.state2Selected = true
	fault.Crash("publication.after_reservation_state2_selection")
	if afterSelection != nil {
		if err := afterSelection(owner.reservation.identity); err != nil {
			return checkpointFailure(err)
		}
	}
	return verifyCanonicalAt(
		owner.reservation.file,
		owner.reservation.mapping,
		output,
		canonicalExpected(owner.reservation.identity, owner.reservation.name, target, 1, outputLocationPrivate),
	)
}

// verifyCanonicalReservation proves one canonical reservation (Rust
// verify_canonical_reservation).
func verifyCanonicalReservation(reservation *canonicalReservation, output *preparedOutput) error {
	return verifyCanonicalAt(
		reservation.file,
		reservation.mapping,
		output,
		canonicalExpected(
			reservation.identity,
			reservation.name,
			reservation.header,
			selectedBlock(reservation.header),
			outputLocationPrivate,
		),
	)
}

// verifyCanonicalAt runs one canonical-reservation verification (Rust
// verify_canonical).
func verifyCanonicalAt(file *os.File, m *mapping.Mapping, output *preparedOutput, expected reservationExpected) error {
	return verifyReservation(file, m, output, expected)
}

// canonicalExpected builds the canonical custody expectation (Rust
// canonical_expected).
func canonicalExpected(identity live.FileIdentity, privateName string, header reservationHeader, block int, outputLocation outputLocation) reservationExpected {
	return reservationExpected{
		identity:            identity,
		privateName:         privateName,
		header:              header,
		block:               block,
		reservationLocation: reservationLocationCanonical,
		outputLocation:      outputLocation,
	}
}

// selectedBlock derives the selected block index of one record (Rust
// selected_block: sequence 1 selects block 0, sequence 2 block 1).
func selectedBlock(header reservationHeader) int {
	return int(header.sequence - 1)
}

// armedReservation is the canonical reservation with the durable
// state-2 record selected (Rust ArmedReservation).
type armedReservation struct {
	name     string
	file     *os.File
	mapping  *mapping.Mapping
	identity live.FileIdentity
	header   reservationHeader
}

// Close releases the reservation resources (Rust drop).
func (a *armedReservation) Close() error { return closeReservationOwner(a.file, a.mapping) }

// verifyBeforeMain proves the armed reservation with the output still
// private (Rust ArmedReservation::verify_before_main).
func (a *armedReservation) verifyBeforeMain(output *preparedOutput) error {
	return a.verifyAt(output, outputLocationPrivate)
}

// verifyAfterMain proves the armed reservation with the output at the
// canonical main (Rust ArmedReservation::verify_after_main).
func (a *armedReservation) verifyAfterMain(output *preparedOutput) error {
	return a.verifyAt(output, outputLocationMain)
}

func (a *armedReservation) verifyAt(output *preparedOutput, location outputLocation) error {
	return verifyCanonicalAt(a.file, a.mapping, output, canonicalExpected(a.identity, a.name, a.header, 1, location))
}

// reservationHeaderFor builds the state-1 record of one prepared
// output and reservation identity (Rust header()). The basename
// length is bounded by the destination name-max proof at bind, so the
// Rust try_from overflow arm (HeaderInvariant) is unreachable in Go.
func reservationHeaderFor(output *preparedOutput, identity live.FileIdentity) *reservationHeader {
	destination := output.attempt.destinationOf()
	header := reservationHeader{
		state:               reservationStatePrepared,
		databaseID:          output.meta.DatabaseID,
		transactionID:       output.meta.TxnID,
		commitNonce:         output.meta.CommitNonce,
		attemptID:           output.attempt.attemptIDOf(),
		reservationIdentity: reservationIdentityBytes(identity),
		policy:              output.policy,
		outputByteLength:    output.byteLength,
		outputIdentity:      reservationIdentityBytes(output.attempt.identityOf()),
		outputSHA512:        output.sha512,
		basenameLen:         uint32(len(destination.mainName())),
		basenameCommitment:  destination.basenameCommitmentValue(),
		securityCommitment:  destination.securityCommitment(),
		sequence:            1,
	}
	if output.previous != nil {
		header.previous = reservationPrevious{
			identity:   reservationIdentityBytes(output.previous.identity),
			byteLength: output.previous.byteLength,
			sha512:     output.previous.sha512,
		}
		header.previousPresent = true
	}
	return &header
}

// reservationIdentityBytes encodes one device+inode pair into the
// reservation payload (Rust Identity::encode: device and inode
// little-endian at bytes 0..16, zero tail).
func reservationIdentityBytes(identity live.FileIdentity) [32]byte {
	device, inode := live.IdentityDeviceInode(&identity)
	return localIdentityFromDeviceInode(device, inode).Bytes
}

// Fixed reservation-machine failure classes (Rust reservation_file.rs
// Error arms; problem.go maps every class verbatim).
func reservationCodecError() error {
	return problem(format.CodeFormatInvalid, "reservation record is malformed")
}

func reservationHeaderChangedError() error {
	return problem(format.CodeConflict, "reservation record changed")
}

func reservationHeaderInvariantError() error {
	return problem(format.CodeFormatInvalid, "reservation state is inconsistent")
}

func reservationLengthChangedError() error {
	return problem(format.CodeConflict, "reservation length changed")
}

// closeReservationOwner releases the mapped view and the reservation
// descriptor (Rust drop of the reservation owners; the slice-H
// cleanup machine consumes the same owner fields).
func closeReservationOwner(file *os.File, m *mapping.Mapping) error {
	var first error
	if m != nil {
		if err := m.Close(); err != nil && first == nil {
			first = err
		}
	}
	if file != nil {
		if err := file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
