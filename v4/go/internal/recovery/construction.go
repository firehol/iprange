package recovery

// Shared recovery output envelope (Rust recovery/construction.rs): the
// destination builder is proven against the source generation before
// the analysis runs, and every failing arm carries the builder, the
// partial report, and the scratch facts for the terminal. The Go peer
// composes the writer draft as the Rust immutable_output Builder.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// constructionFailure is one failed construction (Rust
// construction::Failure): the retained builder, the cause, the
// partial report, and the scratch facts.
type constructionFailure struct {
	builder *writer.Draft
	cause   error
	report  RecoveryReport
	scratch any
}

// analysisFailure is one failed analysis (Rust
// construction::AnalysisFailure).
type analysisFailure struct {
	cause   error
	report  RecoveryReport
	scratch any
}

// prepareConstruction proves the builder against the source and runs
// the analysis (Rust construction::prepare: the builder refusal is the
// invalid-argument class of the source kind).
func prepareConstruction(builder *writer.Draft, source format.Meta, kind uint8, analyze func() (any, *analysisFailure)) (any, *constructionFailure) {
	if err := requireBuilder(builder, source, kind); err != nil {
		return nil, constructionFailureOf(builder, err, RecoveryReport{}, nil)
	}
	analysis, err := analyze()
	if err != nil {
		return nil, constructionFailureOf(builder, err.cause, err.report, err.scratch)
	}
	return analysis, nil
}

// requireBuilder proves the destination builder matches the source
// generation and the recovery kind (Rust construction::require_builder:
// the family, the tag, the feed index limit, and the recovery
// starting transaction).
func requireBuilder(builder *writer.Draft, source format.Meta, kind uint8) error {
	output := builder.Meta()
	purpose := "recovery output does not match its source"
	switch kind {
	case format.ValueKindDirect:
		purpose = "recovery output does not match its direct source"
	case format.ValueKindMembership:
		purpose = "recovery output does not match its membership source"
	case format.ValueKindStructured:
		purpose = "recovery output does not match its structured source"
	}
	feedIndexLimit := uint64(0)
	if kind != format.ValueKindDirect {
		feedIndexLimit = source.FeedIndexLimit
	}
	if output.AddressFamily != source.AddressFamily ||
		output.ValueKind != kind ||
		output.StructureKind != source.StructureKind ||
		output.ValueTag != source.ValueTag ||
		output.FeedIndexLimit != feedIndexLimit ||
		output.TxnID != 1 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: purpose}
	}
	return nil
}

// constructionFailureOf builds one failed construction (Rust
// construction::failure).
func constructionFailureOf(builder *writer.Draft, cause error, report RecoveryReport, scratch any) *constructionFailure {
	return &constructionFailure{builder: builder, cause: cause, report: report, scratch: scratch}
}

// analysisFailureOf builds one failed analysis (Rust
// construction::analysis_failure).
func analysisFailureOf(cause error, report RecoveryReport, scratch any) *analysisFailure {
	return &analysisFailure{cause: cause, report: report, scratch: scratch}
}

// analysisFailureWithPages folds the page set terminal into one failed
// analysis (Rust construction::analysis_failure_with_pages).
func analysisFailureWithPages(pages *pageSet, cause error, report RecoveryReport) *analysisFailure {
	failure := pages.finish(cause)
	return analysisFailureOf(failure.cause, report, failure.cleanup)
}
