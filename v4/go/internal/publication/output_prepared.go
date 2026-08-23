//go:build !windows

// One-pass preparation and verification of an immutable output (Rust
// output.rs PreparedOutput / prepare_cancellable / inspect_exact /
// verify_custody). Preparation proves custody, takes the artifact
// lifetime lock, digests the exact mapped bytes, and re-proves the
// finished length after the durability sync; it never validates the
// whole file beyond the meta pair.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// preparedOutput is one fully prepared publication output (Rust
// PreparedOutput): the attempt, the file and its read-only mapping,
// the meta page, the proved byte length and SHA-512, the policy, and
// the optional replaced previous main.
type preparedOutput struct {
	attempt    outputAttempt
	file       *os.File
	mapping    *mapping.Mapping
	meta       format.Meta
	byteLength uint64
	sha512     [64]byte
	policy     reservationPolicy
	previous   *previousMain
}

// Close releases the prepared mapping and its file descriptor (Rust
// drop of PreparedOutput; the mapping owner unmaps and closes its
// duplicated descriptor, and the attempt file closes here).
func (p *preparedOutput) Close() error {
	var first error
	if p.mapping != nil {
		if err := p.mapping.Close(); err != nil && first == nil {
			first = err
		}
	}
	if p.file != nil {
		if err := p.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	// The previous main stays alive with the prepared output; its own
	// Close releases it when the caller is done.
	return first
}

// outputLocation is the custody position of one verification (Rust
// output.rs Location).
type outputLocation uint8

const (
	outputLocationPrivate outputLocation = iota
	outputLocationMain
)

// verifyPrivate proves the output is still the single-linked private
// artifact of the attempt (Rust PreparedOutput::verify_private).
func (p *preparedOutput) verifyPrivate() error {
	return p.verify(outputLocationPrivate)
}

// verifyMain proves the output is the canonical main of the
// destination, or that the replaced previous still holds the private
// name in the replacement flow (Rust PreparedOutput::verify_main).
func (p *preparedOutput) verifyMain() error {
	return p.verify(outputLocationMain)
}

// verifyDestinationBeforeMain proves the destination state expected
// before the main rename: the previous main is still canonical under a
// replacement policy, or the main name is absent otherwise (Rust
// PreparedOutput::verify_destination_before_main).
func (p *preparedOutput) verifyDestinationBeforeMain() error {
	if p.previous != nil {
		return p.previous.verifyCanonicalNamespace(p.attempt.destination)
	}
	return p.attempt.destination.directory().RequireAbsent(p.attempt.destination.mainName())
}

func (p *preparedOutput) verify(location outputLocation) error {
	// Rust worker::enter_output: test-only probe, recorded with the
	// 4-10/4-11 observed-checkpoint chunks, absent here by design.
	length, err := inspectExact(&p.attempt, p.file, p.mapping, p.meta, location)
	if err != nil {
		return err
	}
	if length != p.byteLength {
		return finishedLengthChanged()
	}
	switch {
	case p.previous == nil && location == outputLocationMain:
		return p.attempt.destination.directory().RequireAbsent(p.attempt.name)
	case p.previous != nil && location == outputLocationMain:
		return p.previous.verifyPrivateOrRetired(p.attempt.destination, p.attempt.name)
	default:
		return nil
	}
}

// prepareMachine runs the one-pass preparation (Rust
// output.rs prepare_cancellable): custody proof, lifetime lock,
// finished inspection, digest, and finish re-proof.
func prepareMachine(owner *unpreparedOutput, check func() error) (uint64, [64]byte, error) {
	if err := live.Checkpoint(check); err != nil {
		return 0, [64]byte{}, err
	}
	// Rust worker::enter_output probe deferred with the observed
	// checkpoints (4-10/4-11); no observable semantics.
	if err := verifyCustody(&owner.attempt, owner.finished.File, outputLocationPrivate); err != nil {
		return 0, [64]byte{}, err
	}
	if err := live.LockFileCancellable(owner.finished.File, live.MainLifetimeOffset, live.LockExclusive, check); err != nil {
		return 0, [64]byte{}, err
	}
	byteLength, err := inspectFinished(owner)
	if err != nil {
		return 0, [64]byte{}, err
	}
	sha512, err := digestCancellable(owner.finished.Mapping, byteLength, check)
	if err != nil {
		return 0, [64]byte{}, err
	}
	if err := finishMachine(owner, byteLength, check); err != nil {
		return 0, [64]byte{}, err
	}
	return byteLength, sha512, nil
}

// finishMachine syncs the finished file, checks cancellation, and
// re-proves the finished length (Rust output.rs finish_cancellable).
func finishMachine(owner *unpreparedOutput, byteLength uint64, check func() error) error {
	if err := live.SyncFile(owner.finished.File); err != nil {
		return err
	}
	if err := live.Checkpoint(check); err != nil {
		return err
	}
	finalLength, err := inspectFinished(owner)
	if err != nil {
		return err
	}
	if finalLength != byteLength {
		return finishedLengthChanged()
	}
	return nil
}

// inspectFinished inspects the unfinished output at its private
// position (Rust output.rs inspect_finished).
func inspectFinished(owner *unpreparedOutput) (uint64, error) {
	return inspectExact(
		&owner.attempt,
		owner.finished.File,
		owner.finished.Mapping,
		owner.finished.Meta,
		outputLocationPrivate,
	)
}

// inspectExact proves custody, reads the physical length, selects the
// committed meta pair, and compares it to the expected meta (Rust
// output.rs inspect_exact). Only the two meta pages are read; the rest
// of the file is proven by the digest pass, never validated.
func inspectExact(attempt *outputAttempt, file *os.File, mapping *mapping.Mapping, expected format.Meta, location outputLocation) (uint64, error) {
	if err := verifyCustody(attempt, file, location); err != nil {
		return 0, err
	}
	st, err := file.Stat()
	if err != nil {
		return 0, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	byteLength := uint64(st.Size())
	page0, err := mapping.Page(0)
	if err != nil {
		return 0, err
	}
	page1, err := mapping.Page(1)
	if err != nil {
		return 0, err
	}
	opened, err := bootstrap.Open(page0, page1, byteLength, bootstrap.ModeImmutableReader)
	if err != nil {
		return 0, outputBootstrapError()
	}
	if opened.Meta != expected {
		return 0, finishedMetaChanged()
	}
	return byteLength, nil
}

// verifyCustody proves the file still is the attempt's single-linked
// retained inode at the requested position and carries the creator
// commitment (Rust output.rs verify_custody). The Rust gc-barrier
// availability call is #[cfg(windows)] and compiles to nothing on
// POSIX; Go publication refuses Windows opens before this point, so
// the barrier is intentionally absent (Phase-2 GC surface).
func verifyCustody(attempt *outputAttempt, file *os.File, location outputLocation) error {
	directory := attempt.destination.directory()
	identity, err := live.RegularIdentity(file, directory.Identity())
	if err != nil {
		return err
	}
	if identity != attempt.identity {
		return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	switch location {
	case outputLocationPrivate:
		if err := directory.VerifyName(attempt.name, identity); err != nil {
			return err
		}
	case outputLocationMain:
		if err := directory.VerifyName(attempt.destination.mainName(), identity); err != nil {
			return err
		}
	}
	return attempt.destination.verifyCreated(file)
}
