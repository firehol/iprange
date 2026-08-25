//go:build windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/random"
)

// createAttempt allocates one collision-free attempt id and creates
// its private output file (Rust output.rs create_attempt windows arm).
func createAttempt(d *destination) ([16]byte, string, *os.File, error) {
	return createAttemptPlatform(d)
}

// createAttemptPlatform allocates one collision-free attempt id and
// creates its private output file (Rust output.rs create_attempt
// windows arm): the output, reservation, and both GC names of ordinals
// 0 and 1 must be absent, and a created-file collision draws another
// id instead of reusing old state.
func createAttemptPlatform(d *destination) ([16]byte, string, *os.File, error) {
	for {
		attemptID, err := random.Nonzero128()
		if err != nil {
			return [16]byte{}, "", nil, err
		}
		name, err := d.outputName(attemptID)
		if err != nil {
			return [16]byte{}, "", nil, err
		}
		collision, err := gcAttemptCollision(d, attemptID, name)
		if err != nil {
			return [16]byte{}, "", nil, err
		}
		if collision {
			continue
		}
		file, err := d.create(name)
		if err != nil {
			if nerr, ok := live.AsNamespaceError(err); ok && nerr.Kind == live.NamespaceExists {
				continue
			}
			return [16]byte{}, "", nil, err
		}
		return attemptID, name, file, nil
	}
}

// gcAttemptCollision reports whether any attempt-derived name already
// exists (Rust output.rs attempt_collision): the private output and
// reservation names plus the GC envelope and inert names of both
// ordinals of this creation.
func gcAttemptCollision(d *destination, attemptID [16]byte, output string) (bool, error) {
	reservation, err := d.reservationName(attemptID)
	if err != nil {
		return false, err
	}
	envelope0, err := live.GCEnvelopeName(attemptID, 0)
	if err != nil {
		return false, err
	}
	inert0, err := live.GCInertName(attemptID, 0)
	if err != nil {
		return false, err
	}
	envelope1, err := live.GCEnvelopeName(attemptID, 1)
	if err != nil {
		return false, err
	}
	inert1, err := live.GCInertName(attemptID, 1)
	if err != nil {
		return false, err
	}
	directory := d.directory()
	for _, name := range []string{output, reservation, envelope0, inert0, envelope1, inert1} {
		switch err := directory.RequireAbsent(name); {
		case err == nil:
		case isNamespaceExistsError(err):
			return true, nil
		default:
			return false, err
		}
	}
	return false, nil
}

// isNamespaceExistsError reports one exists-class namespace error.
func isNamespaceExistsError(err error) bool {
	nerr, ok := live.AsNamespaceError(err)
	return ok && nerr.Kind == live.NamespaceExists
}
