//go:build windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// PublishAttempt is the Windows refusal stub of the reservation-path
// publication composition (Rust namespace/windows.rs is a tracked SOW-0026
// surface): the package binds nothing and creates nothing until the
// native surface exists.
type PublishAttempt struct{}

// File satisfies the common surface; unreachable on Windows in
// milestone 1.
func (p *PublishAttempt) File() *os.File { return nil }

// Close satisfies the common surface; unreachable on Windows.
func (p *PublishAttempt) Close() {}

// FileIdentity satisfies the common surface; unreachable on Windows.
func (p *PublishAttempt) FileIdentity() (uint64, uint64) { return 0, 0 }

// Discard satisfies the common surface; unreachable on Windows.
func (p *PublishAttempt) Discard() CleanupState { return CleanupStateClean }

// Finish satisfies the common surface; unreachable on Windows (the
// composition refuses at create).
func (p *PublishAttempt) Finish(FinishedOutput, func() error) (PublicationResult, *PublicationPreparationFailure) {
	return PublicationResult{}, windowsCreateRefusal()
}

// Facts satisfies the common surface; unreachable on Windows.
func (p *PublishAttempt) Facts() PrivateOutputAttempt { return PrivateOutputAttempt{} }

// DiscardFacts satisfies the common surface; unreachable on Windows.
func (p *PublishAttempt) DiscardFacts() (PrivateOutputAttempt, *CleanupArtifact) {
	return PrivateOutputAttempt{}, nil
}

// CreatePublishAttempt refuses on Windows before any path access
// exactly like the destination bind of the POSIX arms (Rust
// namespace/windows.rs is a tracked SOW-0026 surface; Go keeps the
// honest-refusal stance).
func CreatePublishAttempt(string, PublicationPolicy) (*PublishAttempt, *PublicationPreparationFailure) {
	return nil, windowsCreateRefusal()
}

// windowsCreateRefusal builds the no-discard preparation failure of
// the Windows refusal (the POSIX earlyPreparationFailure with a nil
// discard: no artifact exists, the cleanup ledger stays empty).
func windowsCreateRefusal() *PublicationPreparationFailure {
	return &PublicationPreparationFailure{Cause: outputProblem(&live.NamespaceError{Kind: live.NamespaceUnsupported})}
}
