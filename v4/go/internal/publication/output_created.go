// Owned construction of one private publication output (Rust
// publication/output.rs CreatedOutput / secure_created). The private
// output file is created at its final mapped destination path with a
// random nonzero attempt id, then secured under the destination
// creator-only policy; the returned output attempt owns the secured
// inode identity for the rest of the publication flow.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/random"
)

// createdOutput is one freshly created private publication output
// (Rust CreatedOutput).
type createdOutput struct {
	destination *destination
	attemptID   [16]byte
	name        string
	file        *os.File
}

// createOutput creates one private output for the destination at path
// (Rust CreatedOutput::create).
func createOutput(path string) (*createdOutput, error) {
	return createOutputWith(path, false)
}

// createOutputAbsent additionally proves the main and coordination
// names are absent (Rust CreatedOutput::create_absent; the
// fail-if-exists publication flow uses it).
func createOutputAbsent(path string) (*createdOutput, error) {
	return createOutputWith(path, true)
}

func createOutputWith(path string, requireAbsent bool) (*createdOutput, error) {
	d, err := bindDestination(path)
	if err != nil {
		return nil, err
	}
	if requireAbsent {
		if err := d.requireFailIfExistsAvailable(); err != nil {
			// The bound destination directory closes here exactly
			// where the Rust bind would drop on the refusal.
			d.directory().Close()
			return nil, err
		}
	}
	attemptID, name, file, err := createAttempt(d)
	if err != nil {
		// No private file exists yet; only the bound destination
		// directory needs the close (Rust drops the Destination).
		d.directory().Close()
		return nil, err
	}
	return &createdOutput{destination: d, attemptID: attemptID, name: name, file: file}, nil
}

// createAttempt allocates one random nonzero attempt id and creates its
// private output file (Rust output.rs create_attempt non-windows arm).
// The Rust windows arm retries on namespace collisions; Go publication
// refuses Windows opens at destination bind (SOW-0026), so that retry loop
// is unreachable and intentionally absent.
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

// facts reports the portable private-output facts of one created
// output; the identity is best-effort (Rust CreatedOutput::facts maps
// the any-link identity inspection error to None).
func (c *createdOutput) facts() PrivateOutputAttempt {
	var identity *live.FileIdentity
	if inspected, err := live.RegularIdentityAnyLink(c.file, c.destination.directory().Identity()); err == nil {
		identity = &inspected
	}
	return outputFacts(c.destination, c.attemptID, c.name, identity)
}

// file exposes the created descriptor (Rust CreatedOutput::file).
func (c *createdOutput) fileHandle() *os.File { return c.file }

// destination exposes the bound destination (Rust
// CreatedOutput::destination).
func (c *createdOutput) destinationOf() *destination { return c.destination }

// name exposes the private output name (Rust CreatedOutput::name).
func (c *createdOutput) nameOf() string { return c.name }

// createdOutputFailure is one created-output failure carrying the
// still-owned created output (Rust Failure<CreatedOutput>).
type createdOutputFailure struct {
	owner *createdOutput
	cause error
}

func (f *createdOutputFailure) Error() string { return f.cause.Error() }
func (f *createdOutputFailure) Unwrap() error { return f.cause }

// secure proves the created output still carries the creator-only
// security commitment and returns the secured output attempt (Rust
// CreatedOutput::secure). The inline owner preserves zero-allocation
// cleanup authority on failure.
func (c *createdOutput) secure() (*securedOutput, *createdOutputFailure) {
	identity, err := secureCreated(c)
	if err != nil {
		return nil, &createdOutputFailure{owner: c, cause: err}
	}
	return &securedOutput{
		attempt: outputAttempt{
			destination: c.destination,
			attemptID:   c.attemptID,
			name:        c.name,
			identity:    identity,
		},
		file: c.file,
	}, nil
}

// secureCreated proves the created file's regular identity, applies
// and re-proves the creator-only policy, and verifies the name binding
// (Rust output.rs secure_created).
func secureCreated(created *createdOutput) (live.FileIdentity, error) {
	directory := created.destination.directory()
	identity, err := live.RegularIdentity(created.file, directory.Identity())
	if err != nil {
		return live.FileIdentity{}, err
	}
	if err := directory.VerifyName(created.name, identity); err != nil {
		return live.FileIdentity{}, err
	}
	if err := created.destination.secureCreated(created.file); err != nil {
		return live.FileIdentity{}, err
	}
	secured, err := live.RegularIdentity(created.file, directory.Identity())
	if err != nil {
		return live.FileIdentity{}, err
	}
	if secured != identity {
		return live.FileIdentity{}, &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	if err := directory.VerifyName(created.name, identity); err != nil {
		return live.FileIdentity{}, err
	}
	if err := created.destination.verifyCreated(created.file); err != nil {
		return live.FileIdentity{}, err
	}
	return identity, nil
}
