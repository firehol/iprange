//go:build !windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/random"
)

// createAttempt allocates one random nonzero attempt id and creates its
// private output file (Rust output.rs create_attempt non-windows arm).
func createAttempt(d *destination) ([16]byte, string, *os.File, error) {
	attemptID, err := random.Nonzero128()
	if err != nil {
		return [16]byte{}, "", nil, err
	}
	name, err := d.outputName(attemptID)
	if err != nil {
		return [16]byte{}, "", nil, err
	}
	file, err := d.create(name)
	if err != nil {
		return [16]byte{}, "", nil, err
	}
	return attemptID, name, file, nil
}
