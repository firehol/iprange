package recovery

// Source cleanup guard (Rust recovery/source_guard.rs
// RecoverySourceCleanupGuard and GuardSource): the retryable cleanup
// authority retained when a recovery source release fails. The guard
// keeps the exact source and the fixed problem of the failed release;
// a retried cleanup either completes and empties the guard, or
// updates the last problem and retains the source for another retry.

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// guardSourceKind selects the cleanup source class of one guard (Rust
// GuardSource). The validation and worker arms arrive with their
// cleanup surfaces.
type guardSourceKind uint8

const (
	guardSourceRecovery guardSourceKind = iota
	guardSourceValidation
	guardSourceWorker
)

// workerCleanup is the retryable release authority of the worker
// client (Rust worker::WorkerCleanup). Recovery cannot import the
// worker client package - its client arms import recovery - so the
// guard reaches the retained cleanup through this minimal surface.
type workerCleanup interface {
	Release() error
	LastProblemError() error
}

// guardSource is the cleanup payload of one guard (Rust GuardSource).
type guardSource struct {
	kind       guardSourceKind
	recovery   *recoverySource
	validation any
	worker     workerCleanup
}

// release retries the cleanup of the retained source.
func (g *guardSource) release() error {
	switch g.kind {
	case guardSourceRecovery:
		if g.recovery != nil {
			return g.recovery.release()
		}
	case guardSourceWorker:
		if g.worker != nil {
			return g.worker.Release()
		}
	}
	return nil
}

// problem reports the fixed problem class of one failed cleanup; the
// worker arm reports the retained worker problem instead (Rust
// GuardSource::problem for the Worker arm returns the cleanup's last
// problem).
func (g *guardSource) problem(cause error) error {
	if g.kind == guardSourceWorker && g.worker != nil {
		if last := g.worker.LastProblemError(); last != nil {
			return last
		}
	}
	return problem(cause)
}

// RecoverySourceCleanupGuard is the public retryable cleanup authority
// of one failed recovery source release (Rust
// RecoverySourceCleanupGuard). A failed release retains the exact
// source and its last problem for a retried cleanup; the guard never
// allocates.
type RecoverySourceCleanupGuard struct {
	source      *guardSource
	lastProblem error
}

// FromWorkerCleanup builds the retryable cleanup authority of one
// retained worker cleanup (Rust RecoverySourceCleanupGuard::from_worker):
// the release retry runs the isolated cleanup worker and the last
// problem folds from the retained cleanup. The recovery package
// cannot import the worker package, so the cleanup arrives through
// the workerCleanup surface.
func FromWorkerCleanup(cleanup workerCleanup, problem error) *RecoverySourceCleanupGuard {
	return &RecoverySourceCleanupGuard{
		source:      &guardSource{kind: guardSourceWorker, worker: cleanup},
		lastProblem: problem,
	}
}

// LastProblem returns the fixed problem of the last failed cleanup.
func (g *RecoverySourceCleanupGuard) LastProblem() error {
	return g.lastProblem
}

// CleanupPending reports whether the guard still retains the source.
func (g *RecoverySourceCleanupGuard) CleanupPending() bool {
	return g.source != nil
}

// RetryCleanup retries the retained source release (Rust
// RecoverySourceCleanupGuard::retry_cleanup): true when the cleanup
// completed, false with the last problem when the source is retained
// for another retry.
func (g *RecoverySourceCleanupGuard) RetryCleanup() (bool, error) {
	if g.source == nil {
		return false, nil
	}
	if err := g.source.release(); err != nil {
		g.lastProblem = g.source.problem(err)
		return false, g.lastProblem
	}
	g.source = nil
	return true, nil
}

// problemCode extracts the typed code of one internal failure.
func problemCode(cause error) format.ErrorCode {
	var fe *format.Error
	if errors.As(cause, &fe) {
		return fe.Code
	}
	return format.CodePanic
}
