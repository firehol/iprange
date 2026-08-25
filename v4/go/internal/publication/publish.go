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
	"github.com/firehol/iprange/v4/go/internal/live"
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
// content into. After a terminal (Close, Discard, or Finish) the
// exposure is nil: the attempt is consumed exactly like the residue
// handle of the same boundary.
func (a *PublishAttempt) File() *os.File {
	if a == nil {
		return nil
	}
	return a.file
}

// Close releases the attempt file and the bound destination
// directory without namespace work (Rust drop of OutputAttempt and
// File before any prepare step). The attempt is consumed.
func (a *PublishAttempt) Close() {
	if a == nil || a.file == nil {
		return
	}
	_ = a.file.Close()
	closeDestinationDirectory(a.attempt.destinationOf())
	a.file = nil
}

// CreatePublishAttempt creates and secures one private output (Rust
// workflow::create: the exchange-availability probe precedes the
// creation for the rollback-safe policy, the fail-if-exists policy
// proves the main and the twin absent, and every failure closes the
// bound destination directory like the Rust drop of the consumed
// owners). policy is the published policy of the caller surface; the
// attempt machine carries its reservation-wire peer.
func CreatePublishAttempt(destinationPath string, policy PublicationPolicy) (*PublishAttempt, *PublicationPreparationFailure) {
	reservation, ok := reservationPolicyOf(policy)
	if !ok {
		return nil, earlyPreparationFailure(
			problem(format.CodeInvalidArgument, "publication policy is invalid"), nil)
	}
	if reservation == reservationPolicyReplaceExisting && !mapping.ExchangeAvailable() {
		return nil, earlyPreparationFailure(
			problem(format.CodeDurabilityUnsupported, "rollback-safe replacement requires atomic name exchange"), nil)
	}
	var created *createdOutput
	var err error
	if reservation == reservationPolicyFailIfExists {
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
	return &PublishAttempt{attempt: attempt, file: file, policy: reservation}, nil
}

// FileIdentity returns the secured device+inode identity of the
// attempt file (Rust OutputAttempt::identity; the snapshot and
// publish_set identity-comparison probes use it before the build).
func (a *PublishAttempt) FileIdentity() (device uint64, inode uint64) {
	identity := a.attempt.identityOf()
	return live.IdentityDeviceInode(&identity)
}

// Facts reports the portable facts of the secured attempt (Rust
// OutputAttempt::facts).
func (a *PublishAttempt) Facts() PrivateOutputAttempt {
	if a == nil {
		return PrivateOutputAttempt{}
	}
	return a.attempt.facts()
}

// DiscardFacts removes the attempt like Discard and reports the exact
// ledger facts of the removal (Rust cleanup::discard_attempt over the
// EarlyDiscard output and artifact facts).
func (a *PublishAttempt) DiscardFacts() (PrivateOutputAttempt, *CleanupArtifact) {
	if a == nil || a.file == nil {
		return PrivateOutputAttempt{}, nil
	}
	facts := a.attempt.facts()
	discarded := discardAttempt(&a.attempt, a.file)
	_ = a.file.Close()
	closeDestinationDirectory(a.attempt.destinationOf())
	a.file = nil
	return facts, discarded.artifact
}

// Discard removes the not-yet-finished private attempt artifact
// (Rust cleanup::discard_attempt: identity-guarded unlink and the
// retained-directory sync) and releases the attempt file and the
// bound destination directory. The attempt is consumed: no Finish or
// Close may follow. The returned state classifies the removal exactly
// like the discard ledger of the Rust Failure::Early arms.
func (a *PublishAttempt) Discard() CleanupState {
	if a == nil || a.file == nil {
		return CleanupStateClean
	}
	discarded := discardAttempt(&a.attempt, a.file)
	_ = a.file.Close()
	closeDestinationDirectory(a.attempt.destinationOf())
	a.file = nil
	if discarded.artifact != nil {
		return CleanupStateResiduePossible
	}
	return CleanupStateClean
}

// Finish prepares, binds, and publishes the finished output built
// into the attempt file (Rust workflow::publish: the prepare and
// bind failures discard the attempt and fold their classes; the
// machine failures pass through; the attempt is consumed on every
// terminal).
func (a *PublishAttempt) Finish(finished FinishedOutput, check func() error) (PublicationResult, *PublicationPreparationFailure) {
	if a == nil || a.file == nil {
		return PublicationResult{}, earlyPreparationFailure(
			problem(format.CodeInvalidArgument, "publication attempt is already consumed"), nil)
	}
	prepared, prepareFailure := a.attempt.prepareCancellable(finished, check)
	if prepareFailure != nil {
		owner := prepareFailure.owner
		discarded := discardAttempt(&owner.attempt, owner.finished.File)
		closeFinishedOwner(owner.finished)
		closeDestinationDirectory(owner.attempt.destinationOf())
		a.file = nil
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
		a.file = nil
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
	a.file = nil
	return result, failure
}

// earlyPreparationFailure builds one pre-machine publication failure
// with the fixed cleanup ledger of the discard (Rust
// Failure::Early mapped onto the machine failure surface).
func earlyPreparationFailure(cause error, discarded *earlyDiscard) *PublicationPreparationFailure {
	failure := &PublicationPreparationFailure{Cause: cause}
	if discarded != nil && discarded.artifact != nil {
		ledger := newCleanupArtifacts()
		ledger.Push(*discarded.artifact)
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
