// Exact discovery of one restart-authoritative publication reservation
// (Rust publication/reservation_inspection.rs, 451 lines read in
// full). The machine finds the canonical coordination twin or scans
// the private reservation names, proves the bound evidence, takes the
// operation lock, and returns the inspected owner; the resolver and
// cleanup slices compose it. All record reads run over mapped views.
// The Rust gc_barrier availability calls are #[cfg(windows)] and
// compile to nothing on POSIX (Phase-2 GC surface), so they are
// absent here like every earlier slice.

package publication

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// errReservationInvalid is the non-selectable reservation evidence
// marker (Rust ReadError::Invalid; strict_record maps it to the
// Unresolvable class).
var errReservationInvalid = &format.Error{Code: format.CodeUnresolvable, Detail: "publication reservation record is not selectable"}

// inspectedReservation is one discovered publication reservation with
// its lock held (Rust Inspected).
type inspectedReservation struct {
	name     string
	file     *os.File
	mapping  *mapping.Mapping
	identity live.FileIdentity
	header   reservationHeader
	location reservationLocation
	access   AccessPolicy
}

// Close releases the inspected reservation resources (Rust drop; the
// resolver and cleanup machines consume the same owner fields).
func (r *inspectedReservation) Close() error {
	return closeReservationOwner(r.file, r.mapping)
}

// verify re-proves the inspected reservation at its inspected
// position (Rust Inspected::verify).
func (r *inspectedReservation) verify(destination *destination) error {
	if err := destination.directory().Verify(); err != nil {
		return err
	}
	name := r.name
	if r.location == reservationLocationCanonical {
		name = destination.coordinationName()
	}
	if err := destination.directory().VerifyName(name, r.identity); err != nil {
		return err
	}
	selected, err := readSelected(r.mapping)
	if err != nil {
		return strictRecord(err)
	}
	if selected.header != r.header {
		return conflictProblem("publication reservation changed after inspection")
	}
	return nil
}

// unlockOperation releases the operation lock of one inspected
// reservation (Rust Inspected::unlock_operation).
func (r *inspectedReservation) unlockOperation() error {
	return live.UnlockFile(r.file, reservationOperationLock)
}

// relockOperation re-acquires the operation lock and re-proves the
// inspected reservation (Rust Inspected::relock_operation).
func (r *inspectedReservation) relockOperation(destination *destination, check func() error) error {
	if err := lockOperationFile(r.file, check); err != nil {
		return err
	}
	return r.verify(destination)
}

// discoverReservation finds the restart-authoritative reservation of
// one destination: the canonical coordination twin first, then one
// bound private reservation (Rust discover).
func discoverReservation(destination *destination, check func() error) (*inspectedReservation, error) {
	inspected, err := inspectedCanonical(destination, check)
	if err != nil {
		return nil, err
	}
	if inspected != nil {
		return inspected, nil
	}
	found, err := scanPrivateReservations(destination, check)
	if err != nil {
		return nil, err
	}
	if err := destination.directory().RequireAbsent(destination.coordinationName()); err != nil {
		if found != nil {
			_ = found.Close()
		}
		return nil, conflictProblem("coordination changed during reservation scan")
	}
	return found, nil
}

// inspectedCanonical inspects the coordination twin if it exists
// (Rust canonical).
func inspectedCanonical(destination *destination, check func() error) (*inspectedReservation, error) {
	if err := live.Checkpoint(check); err != nil {
		return nil, err
	}
	regular, err := openCanonicalRegular(destination)
	if err != nil {
		return nil, err
	}
	if regular == nil {
		return nil, nil
	}
	return inspectCanonicalReservation(destination, regular, check)
}

// exactPrivateReservation inspects the private reservation of one
// caller-supplied header (Rust exact_private).
func exactPrivateReservation(destination *destination, expected reservationHeader, check func() error) (inspected *inspectedReservation, err error) {
	if err := live.Checkpoint(check); err != nil {
		return nil, err
	}
	name, err := destination.reservationName(expected.attemptID)
	if err != nil {
		return nil, err
	}
	regular, err := destination.directory().OpenRegular(name, true)
	if err != nil {
		return nil, err
	}
	if regular == nil {
		return nil, nil
	}
	// ownership: the mapped view and the descriptor stay owned by this
	// function until the successful return transfers them to the
	// inspected owner; every error and skip path closes both, exactly
	// like the Rust drop of mapping and regular.
	var mapped *mapping.Mapping
	defer func() {
		if inspected == nil {
			_ = closeReservationOwner(regular.File, mapped)
		}
	}()
	if err := lockOperation(regular, check); err != nil {
		return nil, err
	}
	if err := destination.directory().VerifyName(name, regular.Identity); err != nil {
		return nil, err
	}
	mapped, err = mapReservation(regular.File)
	if err != nil {
		return nil, strictRecord(err)
	}
	selected, err := readSelected(mapped)
	if err != nil {
		return nil, strictRecord(err)
	}
	if err := requireBound(destination, selected.header, regular.Identity, &expected.attemptID); err != nil {
		return nil, err
	}
	if selected.header != expected {
		return nil, conflictProblem("caller result and private reservation disagree")
	}
	return inspectedReservationOf(name, regular, mapped, selected, reservationLocationPrivate), nil
}

// inspectCanonicalReservation proves one canonical coordination twin
// (Rust inspect_canonical).
func inspectCanonicalReservation(destination *destination, regular *live.RegularFile, check func() error) (inspected *inspectedReservation, err error) {
	// ownership: the regular descriptor and the mapped view stay owned
	// by this function until the successful return transfers them to
	// the inspected owner; every error path closes both, exactly like
	// the Rust drop of regular and mapping.
	var mapped *mapping.Mapping
	defer func() {
		if inspected == nil {
			_ = closeReservationOwner(regular.File, mapped)
		}
	}()
	mapped, err = mapReservation(regular.File)
	if err != nil {
		return nil, strictRecord(err)
	}
	selected, err := readSelected(mapped)
	if err != nil {
		return nil, strictRecord(err)
	}
	if err := lockOperation(regular, check); err != nil {
		return nil, err
	}
	rechecked, err := readSelected(mapped)
	if err != nil {
		return nil, strictRecord(err)
	}
	if rechecked != selected {
		return nil, conflictProblem("publication reservation changed while acquiring its lock")
	}
	if err := requireBound(destination, selected.header, regular.Identity, nil); err != nil {
		return nil, err
	}
	privateName, err := destination.reservationName(selected.header.attemptID)
	if err != nil {
		return nil, err
	}
	if err := finishCanonicalTransition(destination, privateName, regular.Identity); err != nil {
		return nil, err
	}
	if err := destination.directory().VerifyName(destination.coordinationName(), regular.Identity); err != nil {
		return nil, err
	}
	rechecked, err = readSelected(mapped)
	if err != nil {
		return nil, strictRecord(err)
	}
	if rechecked != selected {
		return nil, conflictProblem("publication reservation changed during inspection")
	}
	if err := destination.directory().Verify(); err != nil {
		return nil, err
	}
	return inspectedReservationOf(privateName, regular, mapped, selected, reservationLocationCanonical), nil
}

// scanPrivateReservations scans the retained directory for one bound
// private reservation (Rust scan_private).
func scanPrivateReservations(destination *destination, check func() error) (*inspectedReservation, error) {
	var found *inspectedReservation
	err := destination.directory().Scan(func(bytes []byte) error {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		attemptID, ok := privateAttempt(reservationPrefix, bytes)
		if !ok {
			return nil
		}
		name := string(bytes)
		// Rust Name::new validates the component shape; a name that
		// parsed as the exact private reservation shape is never
		// empty, never "." or "..", and carries no separator or NUL.
		candidate, err := inspectPrivateReservation(destination, name, attemptID, check)
		if err != nil {
			return err
		}
		if candidate == nil {
			return nil
		}
		if found != nil {
			// The second candidate is owned and must close before the
			// conflict return, exactly like Rust drops it.
			_ = candidate.Close()
			return conflictProblem("multiple bound private publication reservations exist")
		}
		found = candidate
		return nil
	})
	if err != nil {
		// The first candidate is owned until this point; the conflict
		// arm above already closed the second one (Rust drops both on
		// the error return).
		if found != nil {
			_ = found.Close()
		}
		return nil, err
	}
	return found, nil
}

// inspectPrivateReservation proves one private reservation candidate
// (Rust inspect_private). Entries that cannot be a bound reservation
// are skipped, exactly like Rust.
func inspectPrivateReservation(destination *destination, name string, attemptID [16]byte, check func() error) (inspected *inspectedReservation, err error) {
	regular, err := destination.directory().OpenRegular(name, true)
	switch {
	case err != nil && invalidPrivateEntry(err):
		return nil, nil
	case err != nil:
		return nil, err
	case regular == nil:
		return nil, nil
	}
	// ownership: same rule as inspectCanonicalReservation - the skip
	// returns are Ok(None) and must close the opened regular and the
	// mapped view exactly like Rust drops them on the None arm.
	var mapped *mapping.Mapping
	defer func() {
		if inspected == nil {
			_ = closeReservationOwner(regular.File, mapped)
		}
	}()
	mapped, err = mapReservation(regular.File)
	if err != nil {
		if errors.Is(err, errReservationInvalid) {
			return nil, nil
		}
		return nil, strictRecord(err)
	}
	selected, err := readSelected(mapped)
	if err != nil {
		if errors.Is(err, errReservationInvalid) {
			return nil, nil
		}
		return nil, strictRecord(err)
	}
	if err := requireBound(destination, selected.header, regular.Identity, &attemptID); err != nil {
		return nil, nil
	}
	if err := lockOperation(regular, check); err != nil {
		return nil, err
	}
	if err := destination.directory().VerifyName(name, regular.Identity); err != nil {
		return nil, conflictProblem("private reservation changed during inspection")
	}
	rechecked, err := readSelected(mapped)
	if err != nil {
		return nil, strictRecord(err)
	}
	if rechecked != selected {
		return nil, conflictProblem("private reservation changed during inspection")
	}
	return inspectedReservationOf(name, regular, mapped, selected, reservationLocationPrivate), nil
}

// requireBound proves the reservation binds this destination, inode,
// and optional filename attempt (Rust require_bound).
func requireBound(destination *destination, header reservationHeader, identity live.FileIdentity, filenameAttempt *[16]byte) error {
	if header.reservationIdentity != reservationIdentityBytes(identity) {
		return conflictProblem("reservation self identity does not match its inode")
	}
	if filenameAttempt != nil && *filenameAttempt != header.attemptID {
		return conflictProblem("private reservation name has another attempt id")
	}
	// The basename length is bounded by the destination name-max proof
	// at bind, so the Rust try_from overflow arm is unreachable in Go.
	basenameLen := uint32(len(destination.mainName()))
	if header.basenameLen != basenameLen || header.basenameCommitment != destination.basenameCommitmentValue() {
		return destinationNameMismatchProblem()
	}
	return nil
}

// mapReservation maps one reservation file after the exact-size proof
// (Rust map_reservation; a wrong file size is the Invalid marker).
func mapReservation(file *os.File) (*mapping.Mapping, error) {
	st, err := file.Stat()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if uint64(st.Size()) != reservationFileSize {
		return nil, errReservationInvalid
	}
	return mapping.MapFile(file, reservationFileSize, true)
}

// readSelected decodes the selectable reservation record of one
// mapped view (Rust read_selected; a select refusal is the Invalid
// marker).
func readSelected(m *mapping.Mapping) (selectedReservation, error) {
	bytes, err := m.View(0, reservationFileSize)
	if err != nil {
		return selectedReservation{}, err
	}
	selected, err := selectReservation(bytes)
	if err != nil {
		return selectedReservation{}, errReservationInvalid
	}
	return selected, nil
}

// inspectedReservationOf builds the inspected owner with the
// creator-only evidence classification (Rust inspected).
func inspectedReservationOf(name string, regular *live.RegularFile, m *mapping.Mapping, selected selectedReservation, location reservationLocation) *inspectedReservation {
	access := AccessPolicyChangedOrUnproven
	if commitment, err := security.CreatorOnlyCommitment(regular.File); err == nil && commitment == selected.header.securityCommitment {
		access = AccessPolicyCreatorOnly
	}
	return &inspectedReservation{
		name:     name,
		file:     regular.File,
		mapping:  m,
		identity: regular.Identity,
		header:   selected.header,
		location: location,
		access:   access,
	}
}

// lockOperation takes the exclusive operation lock of one opened
// artifact (Rust lock_operation).
func lockOperation(regular *live.RegularFile, check func() error) error {
	return lockOperationFile(regular.File, check)
}

// lockOperationFile takes the exclusive operation lock with
// cancellation and re-checks the caller cancellation (Rust
// lock_operation_file).
func lockOperationFile(file *os.File, check func() error) error {
	if err := live.LockFileCancellable(file, reservationOperationLock, live.LockExclusive, check); err != nil {
		return err
	}
	return live.Checkpoint(check)
}

// invalidPrivateEntry classifies one entry that cannot be a private
// reservation (Rust invalid_private_entry).
func invalidPrivateEntry(err error) bool {
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		return false
	}
	switch nerr.Kind {
	case live.NamespaceNotRegular, live.NamespaceLinkCount, live.NamespaceCrossFilesystem:
		return true
	case live.NamespaceIoAt:
		return live.IsNofollowSymlink(nerr.Err)
	}
	return false
}

// strictRecord maps one reservation read refusal (Rust strict_record:
// Invalid to the fixed Unresolvable problem, SDK errors preserve
// their class with the fixed SDK detail).
func strictRecord(err error) *format.Error {
	if errors.Is(err, errReservationInvalid) {
		return errReservationInvalid
	}
	return sdkProblem(err)
}

// conflictProblem builds the fixed conflict class of one inspection
// refusal (Rust conflict).
func conflictProblem(detail string) error {
	return problem(format.CodeConflict, detail)
}

// destinationNameMismatchProblem builds the fixed destination-name
// mismatch class (Rust name_mismatch).
func destinationNameMismatchProblem() error {
	return problem(format.CodeDestinationNameMismatch, "reservation belongs to another destination name")
}
