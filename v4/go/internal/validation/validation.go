package validation

// Explicit bounded full-file validation (Rust validation.rs): the
// entry selects one source mode, preflights the budget and the
// platform, opens the locked source, bootstraps the meta pair, maps
// the committed extent read-only, and sweeps every page through the
// claims partition, streaming findings into the sink. Validation
// never modifies the source. The offline-candidate arm composes the
// shared sweep through SweepSelected; the recovery package owns the
// candidate selection and terminal (recovery/inspection.rs parity),
// because the Go mode enum cannot carry the candidate payload of the
// Rust ValidationMode::OfflineCandidate variant.

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// Validate runs one explicit validation over the selected source
// without changing it (Rust validation::validate). Exactly one of the
// result and the failure is non-nil; the failure carries the partial
// progress and the cleanup ledger.
func Validate(path string, mode ValidationMode, budget *ValidationBudget, check func() error, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	if err := preflight(mode, budget, check); err != nil {
		progress := NewProgress()
		return nil, failureOf(err, &progress)
	}
	switch mode {
	case ValidationModeImmutableCurrent:
		if budget.MaxOpenFiles < 1 {
			progress := NewProgress()
			return nil, failureOf(&format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "immutable validation open files"}, &progress)
		}
		return validateImmutable(path, budget, check, sink)
	case ValidationModeLiveCurrent:
		if budget.MaxOpenFiles < 2 {
			progress := NewProgress()
			return nil, failureOf(&format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "live validation open files"}, &progress)
		}
		return validateLive(path, budget, check, sink)
	case ValidationModeOfflineCandidate:
		// The Go mode enum cannot carry the candidate payload of the
		// Rust variant; the offline-candidate arm lives in the
		// recovery package (RecoveryCandidate token + source), which
		// composes the shared sweep below.
		progress := NewProgress()
		return nil, failureOf(&format.Error{Code: format.CodeInvalidArgument, Detail: "offline-candidate validation requires the candidate token (recovery.ValidateOfflineCandidate)"}, &progress)
	default:
		progress := NewProgress()
		return nil, failureOf(&format.Error{Code: format.CodeInvalidEnum, Detail: "invalid validation mode"}, &progress)
	}
}

// Preflight checks the platform, the budget, and the cancellation
// state before any path access (Rust validation::preflight; the
// facade routing reproduces the public entry's preflight before it
// routes to the worker client).
func Preflight(mode ValidationMode, budget *ValidationBudget, check func() error) error {
	return preflight(mode, budget, check)
}

// preflight checks the platform, the budget, and the cancellation
// state before any path access (Rust validation::preflight).
func preflight(mode ValidationMode, budget *ValidationBudget, check func() error) error {
	if mode == ValidationModeLiveCurrent {
		if err := live.CheckSupported(); err != nil {
			return err
		}
	}
	if budget == nil {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "validation budget is required"}
	}
	if err := budget.validate(); err != nil {
		return err
	}
	return live.Checkpoint(check)
}

// validateImmutable runs the immutable sweep (Rust
// validate_immutable): open the locked source, bootstrap the meta
// pair, probe the allocator reserve, sweep the graph and partition,
// then re-verify the source identity under the still-held lock.
func validateImmutable(path string, budget *ValidationBudget, check func() error, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	source, err := OpenImmutableSource(path, check)
	if err != nil {
		progress := NewProgress()
		return nil, failureOf(err, &progress)
	}
	result, failure := sweepImmutable(source, path, budget, check, sink)
	return result, failure
}

// sweepImmutable runs the mapped sweep over an opened immutable
// source (Rust validate_immutable bootstrap + validation_mapping +
// reserve_allocator_pages + validate_selected + source.verify).
func sweepImmutable(source *ImmutableSource, path string, budget *ValidationBudget, check func() error, sink ValidationSink) (*ValidationResult, *ValidationFailure) {
	// The geometry is proved before any mapping exists (Rust
	// require_geometry inside bootstrap_file_faultable): a short or
	// unaligned main reports the FileGeometryInvalid finding instead
	// of failing the bootstrap mapping.
	physical := sourceFileSize(source)
	if physical < 2*format.PageSize {
		progress := NewProgress()
		return bootstrapGeometryReport(source, sink, bootstrap.Problem(&format.Error{Code: format.CodeFormatInvalid, Detail: "file smaller than two pages"}, bootstrap.ProblemFileTooShort), &progress)
	}
	if physical%format.PageSize != 0 {
		progress := NewProgress()
		return bootstrapGeometryReport(source, sink, bootstrap.Problem(&format.Error{Code: format.CodeFormatInvalid, Detail: "file size not page-aligned"}, bootstrap.ProblemFileUnaligned), &progress)
	}
	m, err := mapping.MapFile(source.FileHandle(), 2*format.PageSize, false)
	if err != nil {
		progress := NewProgress()
		return nil, failureOf(sourceCloseFold(source, err), &progress)
	}
	defer func() {
		_ = m.Close()
	}()

	p0, err := m.Page(0)
	if err != nil {
		progress := NewProgress()
		return nil, failureOf(sourceCloseFold(source, err), &progress)
	}
	p1, err := m.Page(1)
	if err != nil {
		progress := NewProgress()
		return nil, failureOf(sourceCloseFold(source, err), &progress)
	}
	res, err := bootstrap.Open(p0, p1, physical, bootstrap.ModeImmutableReader)
	if err != nil {
		if problem, ok := bootstrap.AsProblem(err); ok {
			if problem.Kind == bootstrap.ProblemNoBootstrapMeta || sourceBootstrapReportable(problem) {
				progress := NewProgress()
				if reportErr := writeBootstrapFindings(problem, sink, &progress); reportErr != nil {
					return nil, failureOf(sourceCloseFold(source, reportErr), &progress)
				}
				if err2 := progress.markUntraversable(true); err2 != nil {
					return nil, failureOf(sourceCloseFold(source, err2), &progress)
				}
				if err2 := source.Verify(); err2 != nil {
					return nil, failureOf(sourceCloseFold(source, err2), &progress)
				}
				if closeErr := source.Close(); closeErr != nil {
					return nil, failureOf(closeErr, &progress)
				}
				return &ValidationResult{Valid: false, FileIdentity: source.PublicIdentity(), Generation: nil, Progress: progress}, nil
			}
		}
		progress := NewProgress()
		return nil, failureOf(sourceCloseFold(source, err), &progress)
	}

	// require_available is the recorded POSIX no-op (Rust
	// live_cleanup::require_main_available); the Windows GC custody
	// arrives with the M5 surface.

	whole, err := mapping.MapFile(source.FileHandle(), res.Meta.PageCount*format.PageSize, false)
	if err != nil {
		progress := NewProgress()
		return nil, failureOf(sourceCloseFold(source, err), &progress)
	}
	defer func() { _ = whole.Close() }()
	// Rust validation.rs:310 validation_mapping applies the worker
	// session unreadable-page list to the whole-source mapping: a
	// declared page refuses deterministically with the io-unreadable
	// class instead of faulting, so a restarted worker session never
	// SIGBUSes on the same page.
	if err := whole.SetUnreadablePages(mapping.SessionUnreadablePages()); err != nil {
		progress := NewProgress()
		return nil, failureOf(sourceCloseFold(source, err), &progress)
	}

	ctx, err := newContext(whole, res.Meta, budget, check, sink)
	if err != nil {
		progress := NewProgress()
		return nil, failureOf(sourceCloseFold(source, err), &progress)
	}
	// Rust validation.rs:283 probe_source: the whole-source mapping is
	// armed with the Source role for the reserve+selected sweep, so a
	// real mapped fault lands in the worker fault record instead of
	// chaining (no hook in library processes: the Probe runs the
	// sweep directly).
	scanErr := whole.Probe(mapping.RoleSource, func() error {
		if err := ctx.reserveAllocatorPages(); err != nil {
			return err
		}
		return validateSelected(ctx)
	})
	verification := source.Verify()
	progress := ctx.finish()
	if scanErr != nil {
		return nil, failureOf(sourceCloseFold(source, scanErr), &progress)
	}
	if verification != nil {
		return nil, failureOf(sourceCloseFold(source, verification), &progress)
	}
	if closeErr := source.Close(); closeErr != nil {
		return nil, failureOf(closeErr, &progress)
	}
	return &ValidationResult{
		Valid:        progress.FindingCount == 0,
		FileIdentity: source.PublicIdentity(),
		Generation:   generation(res.Meta),
		Progress:     progress,
	}, nil
}

// validateSelected runs the structure validators over the context in the
// Rust validate_selected order (validation.rs:460) and ends with the
// allocation-partition sweep. Slice B ships the metadata and retirement
// validators plus the tree/page authorities; the remaining validators
// are the slice C-E stubs. The sweep itself runs reserveAllocatorPages
// before validateSelected inside probe_source, exactly like Rust.
func validateSelected(ctx *context) error {
	if err := validateRange(ctx); err != nil {
		return err
	}
	if err := validateCatalog(ctx); err != nil {
		return err
	}
	if err := validateStructure(ctx); err != nil {
		return err
	}
	if err := validateMembership(ctx); err != nil {
		return err
	}
	if err := validateMetadata(ctx); err != nil {
		return err
	}
	if err := validateFreeBitmap(ctx); err != nil {
		return err
	}
	if err := validateRetirement(ctx); err != nil {
		return err
	}
	return ctx.validatePartition()
}

// SweepSelected runs the committed-extent sweep of one selected
// generation over an opened source file (Rust validate_offline's
// validation_mapping + reserve_allocator_pages + validate_selected +
// finish): the generation is mapped read-only, the context is built,
// the allocator reserve and the selected graph are swept, and the
// partial progress is returned with the first failure. The caller
// owns the file, the recovery identity checks, and the terminal
// verification; a mapping or context failure returns the empty
// progress exactly like the Rust failure arms.
func SweepSelected(file *os.File, meta format.Meta, budget *ValidationBudget, check func() error, sink ValidationSink) (ValidationProgress, error) {
	if meta.PageCount > ^uint64(0)/format.PageSize {
		return ValidationProgress{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation mapping length"}
	}
	whole, err := mapping.MapFile(file, meta.PageCount*format.PageSize, false)
	if err != nil {
		return ValidationProgress{}, err
	}
	defer func() { _ = whole.Close() }()
	// Rust validation.rs validation_mapping (validate_offline arm)
	// applies the worker session unreadable-page list to the
	// whole-source mapping before the sweep.
	if err := whole.SetUnreadablePages(mapping.SessionUnreadablePages()); err != nil {
		return ValidationProgress{}, err
	}
	ctx, err := newContext(whole, meta, budget, check, sink)
	if err != nil {
		return ValidationProgress{}, err
	}
	// Rust validation.rs:130 probe_source (validate_offline arm): the
	// sweep runs inside the armed Source probe of the whole-source
	// mapping.
	scanErr := whole.Probe(mapping.RoleSource, func() error {
		if err := ctx.reserveAllocatorPages(); err != nil {
			return err
		}
		return validateSelected(ctx)
	})
	if scanErr != nil {
		return ctx.finish(), scanErr
	}
	return ctx.finish(), nil
}

// sourceBootstrapReportable reports whether one classified bootstrap
// refusal produces the bootstrap-finding report (Rust
// validate_immutable's Error::Format arm: every Format-class
// problem).
func sourceBootstrapReportable(problem *bootstrap.ProblemError) bool {
	return problem.Format.Code == format.CodeFormatInvalid
}

// bootstrapGeometryReport streams the pre-mapping geometry finding
// report (the short and unaligned arms of the Rust bootstrap report):
// findings first, then the untraversable mark, then the source
// verification, in the Rust bootstrap_report order.
func bootstrapGeometryReport(source *ImmutableSource, sink ValidationSink, problem *bootstrap.ProblemError, progress *ValidationProgress) (*ValidationResult, *ValidationFailure) {
	if err := writeBootstrapFindings(problem, sink, progress); err != nil {
		return nil, failureOf(sourceCloseFold(source, err), progress)
	}
	if err := progress.markUntraversable(true); err != nil {
		return nil, failureOf(sourceCloseFold(source, err), progress)
	}
	if err := source.Verify(); err != nil {
		return nil, failureOf(sourceCloseFold(source, err), progress)
	}
	if closeErr := source.Close(); closeErr != nil {
		return nil, failureOf(closeErr, progress)
	}
	return &ValidationResult{Valid: false, FileIdentity: source.PublicIdentity(), Generation: nil, Progress: *progress}, nil
}

// writeBootstrapFindings streams the bootstrap-refusal findings (Rust
// write_bootstrap_findings + report_meta_problem + report_bootstrap_
// finding): the per-page problems split on the magic class, the
// geometry classes share the FileGeometry finding, and the
// selection-class failures map to the MetaInvalid finding.
func writeBootstrapFindings(problem *bootstrap.ProblemError, sink ValidationSink, progress *ValidationProgress) error {
	switch problem.Kind {
	case bootstrap.ProblemNoBootstrapMeta:
		if err := reportMetaPage(problem.Meta0MagicInvalid, 0, sink, progress); err != nil {
			return err
		}
		return reportMetaPage(problem.Meta1MagicInvalid, 1, sink, progress)
	case bootstrap.ProblemStaticIdentityMismatch:
		return reportBootstrapFinding(ReasonMetaStaticMismatch, ObjectMeta, nil, sink, progress)
	case bootstrap.ProblemFileTooShort, bootstrap.ProblemFileUnaligned, bootstrap.ProblemHostAddressability, bootstrap.ProblemImmutableLengthMismatch:
		return reportBootstrapFinding(ReasonFileGeometryInvalid, ObjectFileGeometry, nil, sink, progress)
	default:
		return reportBootstrapFinding(ReasonMetaInvalid, ObjectMeta, nil, sink, progress)
	}
}

// reportMetaPage reports one per-page meta problem (Rust
// report_meta_problem): the magic class selects the MetaUnavailable
// reason, everything else is MetaInvalid, both on the Meta object
// with the page number and its physical byte interval.
func reportMetaPage(magicInvalid bool, pageNumber uint32, sink ValidationSink, progress *ValidationProgress) error {
	reason := ReasonMetaInvalid
	if magicInvalid {
		reason = ReasonMetaUnavailable
	}
	return reportBootstrapFinding(reason, ObjectMeta, &pageNumber, sink, progress)
}

// reportBootstrapFinding counts and streams one bootstrap finding
// (Rust report_bootstrap_finding: the sequence is the post-count
// finding count, and a page-carrying finding always includes its
// physical byte interval).
func reportBootstrapFinding(reason ValidationReason, object ValidationObject, pageNumber *uint32, sink ValidationSink, progress *ValidationProgress) error {
	var interval *PhysicalByteInterval
	if pageNumber != nil {
		value, err := partitionBytes(uint64(*pageNumber), uint64(*pageNumber)+1)
		if err != nil {
			return err
		}
		interval = &value
	}
	return emitFinding(progress, sink, reason, object, pageNumber, interval, nil)
}

// sourceFileSize is the physical extent of the opened source (Rust
// open_read_only fstat; the bootstrap selection requires the exact
// committed length for the immutable reader).
func sourceFileSize(source *ImmutableSource) uint64 {
	size, err := source.FileHandle().Stat()
	if err != nil {
		return 0
	}
	return uint64(size.Size())
}

// sourceCloseFold closes the source and returns the primary error
// (the close error surfaces only when the primary is nil, like the
// Rust combine_errors arms).
func sourceCloseFold(source *ImmutableSource, primary error) error {
	closeErr := source.Close()
	if primary != nil {
		return primary
	}
	return closeErr
}

// failureOf builds one operational failure with the partial progress
// and the clean ledger (Rust validation::failure).
func failureOf(cause error, progress *ValidationProgress) *ValidationFailure {
	return &ValidationFailure{
		Cause:               cause,
		Progress:            progress,
		CoordinationCleanup: publication.CoordinationCleanupNone,
	}
}
