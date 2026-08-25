// Exact reservation and prepared-output custody checks (Rust
// publication/reservation_verify.rs). One verification proves the
// reservation inode against the output evidence, its placement at the
// private or canonical name, and the exact dual-block record; every
// read runs over the mapped reservation view with no intermediate
// copy.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// reservationLocation is the namespace position of one reservation
// (Rust ReservationLocation).
type reservationLocation uint8

const (
	reservationLocationPrivate reservationLocation = iota
	reservationLocationCanonical
)

// reservationExpected is the exact custody expectation of one
// reservation verification (Rust Expected).
type reservationExpected struct {
	identity            live.FileIdentity
	privateName         string
	header              reservationHeader
	block               int
	reservationLocation reservationLocation
	outputLocation      outputLocation
}

// verifyReservation runs the three-part custody proof (Rust verify):
// inode evidence, namespace placement, and record contents.
func verifyReservation(file *os.File, m *mapping.Mapping, output *preparedOutput, expected reservationExpected) error {
	if err := verifyReservationInode(file, output, expected); err != nil {
		return err
	}
	if err := verifyReservationLocation(output, expected); err != nil {
		return err
	}
	return verifyReservationContents(file, m, expected.header, expected.block)
}

// verifyReservationInode proves the output is still at its exact
// position, the retained directory is stable, and the reservation
// file is the expected single-link inode with the creator commitment
// (Rust verify_inode).
func verifyReservationInode(file *os.File, output *preparedOutput, expected reservationExpected) error {
	switch expected.outputLocation {
	case outputLocationPrivate:
		if err := output.verifyPrivate(); err != nil {
			return err
		}
	case outputLocationMain:
		if err := output.verifyMain(); err != nil {
			return err
		}
	}
	destination := output.attempt.destinationOf()
	directory := destination.directory()
	if err := directory.Verify(); err != nil {
		return err
	}
	identity, err := live.RegularIdentity(file, directory.Identity())
	if err != nil {
		return err
	}
	if identity != expected.identity {
		return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	return destination.verifyCreated(file)
}

// verifyReservationLocation proves the reservation name placement and
// the private-name absence at the canonical position (Rust
// verify_location, including the gc-barrier availability call with
// the reservation artifact kind).
func verifyReservationLocation(output *preparedOutput, expected reservationExpected) error {
	destination := output.attempt.destinationOf()
	directory := destination.directory()
	name := expected.privateName
	kind := ArtifactPrivateReservation
	if expected.reservationLocation == reservationLocationCanonical {
		name = destination.coordinationName()
		kind = ArtifactOwnedCoordination
	}
	if err := requireSourceAvailable(directory, expected.header.attemptID, 1, kind, DirectoryRoleDestination, name, expected.identity); err != nil {
		return err
	}
	if err := directory.VerifyName(name, expected.identity); err != nil {
		return err
	}
	if expected.reservationLocation == reservationLocationCanonical {
		return directory.RequireAbsent(expected.privateName)
	}
	return nil
}

// verifyReservationContents proves the reservation file keeps its
// exact size and its selected record (Rust verify_contents).
func verifyReservationContents(file *os.File, m *mapping.Mapping, header reservationHeader, block int) error {
	size, err := fstatSize(file)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	if size != reservationFileSize {
		return reservationLengthChangedError()
	}
	return selectExact(m, header, block)
}

// selectExact proves the mapped reservation still selects the exact
// expected record at its block (Rust select_exact: any select refusal
// is the codec class, any selection mismatch is the header-changed
// class).
func selectExact(m *mapping.Mapping, header reservationHeader, block int) error {
	bytes, err := m.View(0, reservationFileSize)
	if err != nil {
		return err
	}
	selected, err := selectReservation(bytes)
	if err != nil {
		return reservationCodecError()
	}
	if selected.header != header || selected.block != block {
		return reservationHeaderChangedError()
	}
	return nil
}
