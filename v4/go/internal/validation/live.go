package validation

// LiveCurrent validation (Rust validation.rs validate_live +
// validate_live_selected + validate_live_bootstrap): the selected
// sweep runs over the registered live source (one claimed reader slot
// of the committed generation) with the same graph and partition
// walks as the immutable sweep, and the terminal folds the scan and
// release results exactly like the Rust finish(scan). The bootstrap
// arm reports the refused committed-generation selection as findings
// and releases the gate-held registration.

import (
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// validateLive runs the LiveCurrent mode (Rust validate_live): the
// open union selects the registered source or the bootstrap source,
// and every failing open retains its coordination residue.
func validateLive(path string, budget *ValidationBudget, check func() error, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	opened, openFailure := live.OpenLiveValidationSource(path, check)
	if openFailure != nil {
		progress := NewProgress()
		return nil, liveOpenFailureOf(openFailure, &progress)
	}
	if opened.Bootstrap != nil {
		return sweepLiveBootstrap(opened.Bootstrap, sink)
	}
	return sweepLiveSelected(opened.Selected, budget, check, sink)
}

// sweepLiveSelected runs the sweep over one registered live source
// (Rust validate_live_selected): the mapping and context failures
// release the source with an empty progress, the scan reuses the
// shared reserve+selected composition, and the terminal folds the
// scan into the source release.
func sweepLiveSelected(source *live.LiveSource, budget *ValidationBudget, check func() error, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	meta := source.Meta()
	device, inode, err := source.FileIdentity()
	if err != nil {
		progress := NewProgress()
		end := source.FinishCurrent(func() error { return err })
		return nil, liveEndFailureOf(end, &progress)
	}
	identity := publicationIdentity(device, inode)
	ctx, err := newContext(source.Mapping(), meta, budget, check, sink)
	if err != nil {
		progress := NewProgress()
		end := source.FinishCurrent(func() error { return err })
		return nil, liveEndFailureOf(end, &progress)
	}
	scanErr := ctx.reserveAllocatorPages()
	if scanErr == nil {
		scanErr = validateSelected(ctx)
	}
	progress := ctx.finish()
	end := source.FinishCurrent(func() error { return scanErr })
	if end.Cause != nil {
		return nil, liveEndFailureOf(end, &progress)
	}
	return &ValidationResult{
		Valid:        progress.FindingCount == 0,
		FileIdentity: identity,
		Generation:   generation(meta),
		Progress:     progress,
	}, nil
}

// sweepLiveBootstrap reports the refused committed-generation
// selection (Rust validate_live_bootstrap): the bootstrap findings and
// the untraversable mark, then the gate release; the generation stays
// unproven.
func sweepLiveBootstrap(source *live.LiveBootstrapValidationSource, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	progress := NewProgress()
	device, inode, err := source.FileIdentity()
	if err != nil {
		end := source.Finish(func() error { return err })
		return nil, liveEndFailureOf(end, &progress)
	}
	identity := publicationIdentity(device, inode)
	report := writeBootstrapFindings(source.Problem(), sink, &progress)
	if report == nil {
		report = progress.markUntraversable(true)
	}
	end := source.Finish(func() error { return report })
	if end.Cause != nil {
		return nil, liveEndFailureOf(end, &progress)
	}
	return &ValidationResult{
		Valid:        false,
		FileIdentity: identity,
		Generation:   nil,
		Progress:     progress,
	}, nil
}

// liveOpenFailureOf builds one failure from a failed live open (Rust
// failure_with_guard over LiveOpenFailure): a claimed-open unwind
// keeps the coordination residue.
func liveOpenFailureOf(openFailure *live.LiveValidationOpenFailure, progress *ValidationProgress) *ValidationFailure {
	failure := failureOf(openFailure.Cause, progress)
	if openFailure.Residue {
		failure.CoordinationCleanup = publication.CoordinationCleanupCleanupGuard
	}
	return failure
}

// liveEndFailureOf builds one failure from a live source terminal
// (Rust failure_with_guard over LiveSourceEnd): a failed release keeps
// the retryable source handle and the coordination residue.
func liveEndFailureOf(end live.LiveSourceEnd, progress *ValidationProgress) *ValidationFailure {
	failure := failureOf(end.Cause, progress)
	if end.Residue {
		failure.CoordinationCleanup = publication.CoordinationCleanupCleanupGuard
	}
	return failure
}
