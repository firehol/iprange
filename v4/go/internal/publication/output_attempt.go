// One secured publication-output attempt (Rust output.rs SecuredOutput
// / OutputAttempt). The attempt owns the destination, the private
// output name, and the secured inode identity; the writer core builds
// the finished output into the attempt's file, then the machine
// prepares it under the artifact lifetime lock.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// securedOutput is the created output after the creator-only proof
// (Rust SecuredOutput). intoParts hands the attempt and its file to
// the writer core.
type securedOutput struct {
	attempt outputAttempt
	file    *os.File
}

// intoParts splits the secured output into the attempt and its file
// (Rust SecuredOutput::into_parts; the file fd keeps its lock and
// creator-only state across the writer build).
func (s *securedOutput) intoParts() (outputAttempt, *os.File) {
	return s.attempt, s.file
}

// outputAttempt is the exact identity of one secured output (Rust
// OutputAttempt).
type outputAttempt struct {
	destination *destination
	attemptID   [16]byte
	name        string
	identity    live.FileIdentity
}

// destination exposes the bound destination (Rust
// OutputAttempt::destination).
func (a *outputAttempt) destinationOf() *destination { return a.destination }

// attemptID exposes the random attempt id (Rust OutputAttempt::attempt_id).
func (a *outputAttempt) attemptIDOf() [16]byte { return a.attemptID }

// name exposes the private output name (Rust OutputAttempt::name).
func (a *outputAttempt) nameOf() string { return a.name }

// identity exposes the secured inode identity (Rust
// OutputAttempt::identity).
func (a *outputAttempt) identityOf() live.FileIdentity { return a.identity }

// facts reports the portable facts of one secured attempt (Rust
// OutputAttempt::facts).
func (a *outputAttempt) facts() PrivateOutputAttempt {
	identity := a.identity
	return outputFacts(a.destination, a.attemptID, a.name, &identity)
}

// unpreparedOutput is the attempt plus the finished output of the
// writer core before the one-pass preparation (Rust UnpreparedOutput).
type unpreparedOutput struct {
	attempt  outputAttempt
	finished FinishedOutput
}

// unpreparedFailure is one preparation failure carrying the still-owned
// attempt and finished output (Rust Failure<UnpreparedOutput>).
type unpreparedFailure struct {
	owner *unpreparedOutput
	cause error
}

func (f *unpreparedFailure) Error() string { return f.cause.Error() }
func (f *unpreparedFailure) Unwrap() error { return f.cause }

// prepareCancellable runs the one-pass preparation of the finished
// output and returns the prepared output (Rust
// OutputAttempt::prepare_cancellable). The policy starts as
// fail-if-exists; a replacement bind raises it later.
func (a *outputAttempt) prepareCancellable(finished FinishedOutput, check func() error) (*preparedOutput, *unpreparedFailure) {
	owner := &unpreparedOutput{attempt: *a, finished: finished}
	byteLength, sha512, err := prepareMachine(owner, check)
	if err != nil {
		return nil, &unpreparedFailure{owner: owner, cause: err}
	}
	return &preparedOutput{
		attempt:    owner.attempt,
		file:       finished.File,
		mapping:    finished.Mapping,
		meta:       finished.Meta,
		byteLength: byteLength,
		sha512:     sha512,
		policy:     reservationPolicyFailIfExists,
		previous:   nil,
	}, nil
}
