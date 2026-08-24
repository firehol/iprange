//go:build !windows

// Reservation-path publication of one finished immutable output (Rust
// publication/workflow.rs create + publish): CreatePublishAttempt
// creates and secures the private output (the fail-if-exists policy
// also proves the main and the coordination twin absent), the caller
// builds the finished content into the attempt file, and Finish
// prepares it (custody, lifetime lock, finished proof, digest, finish
// sync), binds it to the replaced main under replacement policies,
// and publishes it through the attempt machine. Every pre-machine
// failure carries the folded problem class and the discard evidence
// exactly like the Rust Failure::Early arms; the composition closes
// every descriptor it opened (Rust drops the consumed owners),
// including the bound destination directory that the machine keeps
// open for its caller. finished is consumed on every terminal.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// PublishAttempt is one created and secured private output awaiting
// its finished content (Rust workflow::create result). The attempt
// file is the only legal target for the finished output; calling
// Close without Finish abandons the attempt under no namespace work
// (Rust drop of the created owners).
type PublishAttempt struct {
	attempt outputAttempt
	file    *os.File
	policy  reservationPolicy
}

// File exposes the attempt file the caller builds the finished
// content into.
func (a *PublishAttempt) File() *os.File { return a.file }

// Close releases the attempt file and the bound destination
// directory without namespace work (Rust drop of OutputAttempt and
// File before any prepare step).
func (a *PublishAttempt) Close() {
	_ = a.file.Close()
	closeDestinationDirectory(a.attempt.destinationOf())
}

// CreatePublishAttempt creates and secures one private output (Rust
// workflow::create: the exchange-availability probe precedes the
// creation for the rollback-safe policy, the fail-if-exists policy
// proves the main and the twin absent, and every failure closes the
// bound destination directory like the Rust drop of the consumed
// owners).
func CreatePublishAttempt(destinationPath string, policy reservationPolicy, check func() error) (*PublishAttempt, *PublicationPreparationFailure) {
	if policy == reservationPolicyReplaceExisting && !mapping.ExchangeAvailable() {
		return nil, earlyPreparationFailure(
			problem(format.CodeDurabilityUnsupported, "rollback-safe replacement requires atomic name exchange"), nil)
	}
	var created *createdOutput
	var err error
	if policy == reservationPolicyFailIfExists {
		created, err = createOutputAbsent(destinationPath)
	} else {
		created, err = createOutput(destinationPath)
	}
	if err != nil {
		// Rust create: Problem::output, no discard (nothing exists).
		return nil, earlyPreparationFailure(outputProblem(err), nil)
	}
	secured, failure := created.secure()
	if failure != nil {
		discarded := discardCreated(created)
		closeCreatedOwner(created)
		return nil, earlyPreparationFailure(outputProblem(failure.cause), &discarded)
	}
	attempt, file := secured.intoParts()
	return &PublishAttempt{attempt: attempt, file: file, policy: policy}, nil
}

// Finish prepares, binds, and publishes the finished output built
// into the attempt file (Rust workflow::publish: the prepare and
// bind failures discard the attempt and fold their classes; the
// machine failures pass through; the attempt is consumed on every
// terminal).
func (a *PublishAttempt) Finish(finished FinishedOutput, check func() error) (PublicationResult, *PublicationPreparationFailure) {
	prepared, prepareFailure := a.attempt.prepareCancellable(finished, check)
	if prepareFailure != nil {
		owner := prepareFailure.owner
		discarded := discardAttempt(&owner.attempt, owner.finished.File)
		closeFinishedOwner(owner.finished)
		closeDestinationDirectory(owner.attempt.destinationOf())
		return PublicationResult{}, earlyPreparationFailure(outputProblem(prepareFailure.cause), &discarded)
	}
	var bound *preparedOutput
	var bindFailure *replacementFailure
	switch a.policy {
	case reservationPolicyFailIfExists:
		bound = prepared
	case reservationPolicyReplaceExisting:
		bound, bindFailure = bindPrevious(prepared, check)
	case reservationPolicyReplaceExistingNoRollback:
		bound, bindFailure = bindPreviousNoRollback(prepared, check)
	}
	if bindFailure != nil {
		discarded := discardAttempt(&prepared.attempt, prepared.file)
		_ = prepared.Close()
		closeDestinationDirectory(prepared.attempt.destinationOf())
		return PublicationResult{}, earlyPreparationFailure(replacementProblem(bindFailure), &discarded)
	}
	var result PublicationResult
	var failure *PublicationPreparationFailure
	switch a.policy {
	case reservationPolicyFailIfExists:
		result, failure = failIfExistsCancellable(bound, check)
	default:
		result, failure = replaceExistingCancellable(bound, check)
	}
	_ = bound.Close()
	closeDestinationDirectory(bound.attempt.destinationOf())
	return result, failure
}

// earlyPreparationFailure builds one pre-machine publication failure
// with the fixed cleanup ledger of the discard (Rust
// Failure::Early mapped onto the machine failure surface).
func earlyPreparationFailure(cause error, discarded *earlyDiscard) *PublicationPreparationFailure {
	failure := &PublicationPreparationFailure{Cause: cause}
	if discarded != nil && discarded.artifact != nil {
		ledger := newCleanupArtifacts()
		ledger.push(*discarded.artifact)
		failure.Cleanup = ledger
		failure.PublicationAttemptID = discarded.output.PublicationAttemptID
		failure.DirectoryIdentity = discarded.output.DirectoryIdentity
		failure.PrivateOutputBasenameEncoding = discarded.output.BasenameEncoding
		failure.PrivateOutputBasename = discarded.output.Basename
		failure.OutputIdentity = discarded.output.Identity
		failure.CreationSecurity = discarded.output.CreationSecurity
	}
	return failure
}

// closeCreatedOwner releases the file and the bound destination
// directory of one created output (Rust drop of the CreatedOutput).
func closeCreatedOwner(created *createdOutput) {
	_ = created.fileHandle().Close()
	closeDestinationDirectory(created.destinationOf())
}

// closeFinishedOwner releases the file and the mapping of one
// finished output that never reached a prepared owner (Rust drop of
// the Finished).
func closeFinishedOwner(finished FinishedOutput) {
	_ = finished.File.Close()
	if finished.Mapping != nil {
		_ = finished.Mapping.Close()
	}
}

// closeDestinationDirectory releases the bound destination
// directory of one output owner (the machine keeps it open for its
// caller; the composition closes it on its terminals like the Rust
// drop).
func closeDestinationDirectory(d *destination) {
	d.directory().Close()
}
