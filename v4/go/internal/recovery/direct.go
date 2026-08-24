package recovery

// Direct recovery analysis and construction (Rust recovery/direct.rs
// and recovery/direct_build.rs): one analyze pass reads the readable
// ranges and the metadata through the page-ownership set, and one
// construct pass builds the canonical direct output through the shared
// preparation and the family-split range build.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// directAnalysis is the direct recovery analysis (Rust DirectAnalysis):
// the completed report, the readable-records count, the order proof,
// the decompressed metadata (nil when absent or damaged), and the
// page-ownership set.
type directAnalysis struct {
	report          RecoveryReport
	readableRecords uint64
	ordered         bool
	metadata        []byte
	pages           *pageSet
}

// directAnalyze runs the direct recovery analysis (Rust direct::analyze:
// the budget and cancellation preflight, the direct kind proof, the
// page set over the bounded expected pages, the range analysis, and
// the metadata read; every later failure carries the page-set
// terminal).
func directAnalyze(m *mapping.Mapping, meta format.Meta, budget *RecoveryBudget, check func() error, sink RecoverySink) (*directAnalysis, *analysisFailure) {
	if err := budget.validate(); err != nil {
		return nil, analysisFailureOf(err, RecoveryReport{}, nil)
	}
	if err := live.Checkpoint(check); err != nil {
		return nil, analysisFailureOf(err, RecoveryReport{}, nil)
	}
	if meta.ValueKind != format.ValueKindDirect {
		return nil, analysisFailureOf(&format.Error{Code: format.CodeWrongValueKind, Detail: "direct recovery requires direct values"}, RecoveryReport{}, nil)
	}
	physicalPages := m.Size() / format.PageSize
	expected := meta.PageCount
	if physicalPages < expected {
		expected = physicalPages
	}
	rep := newReporter(sink)
	pages, err := forRecovery(budget.MaxHeapBytes, expected, meta, budget)
	if err != nil {
		return nil, analysisFailureOf(err, rep.finish(), nil)
	}
	codec, ok := directCodec(meta.AddressFamily)
	if !ok {
		return nil, analysisFailureWithPages(pages, &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery direct family is invalid"}, rep.finish())
	}
	readable, ordered, err := analyzeRanges(codec, m, meta, pages, check, rep)
	if err != nil {
		return nil, analysisFailureWithPages(pages, err, rep.finish())
	}
	metadata, err := readMetadata(m, meta, pages, budget.MaxHeapBytes, check, rep)
	if err != nil {
		return nil, analysisFailureWithPages(pages, err, rep.finish())
	}
	return &directAnalysis{report: rep.finish(), readableRecords: readable, ordered: ordered, metadata: metadata, pages: pages}, nil
}

// directCodec returns the family codec of one direct source (Rust
// direct::analyze family split; the default arm is unreachable for a
// recovery-valid meta).
func directCodec(family uint8) (rangeCodec, bool) {
	switch family {
	case format.AddressFamilyIPv4:
		return rangeV4Codec{}, true
	case format.AddressFamilyIPv6:
		return rangeV6Codec{}, true
	default:
		return nil, false
	}
}

// directConstruct builds one canonical direct output from one recovery
// source (Rust direct_build::construct: the preparation proves the
// destination against the source generation, the analysis runs, and
// the family build folds the range stream and the finish).
func directConstruct(m *mapping.Mapping, sourceMeta format.Meta, builder *writer.OutputBuilder, budget *RecoveryBudget, check func() error, sink RecoverySink) (*Construction, *constructionFailure) {
	analysis, failure := prepareConstruction(builder, sourceMeta, format.ValueKindDirect, func() (any, *analysisFailure) {
		result, failed := directAnalyze(m, sourceMeta, budget, check, sink)
		if failed != nil {
			return nil, failed
		}
		return result, nil
	})
	if failure != nil {
		return nil, failure
	}
	direct := analysis.(*directAnalysis)
	codec, ok := directCodec(sourceMeta.AddressFamily)
	if !ok {
		return nil, constructionFailureOf(builder, &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery direct family is invalid"}, direct.report, nil)
	}
	return directBuild(codec, m, sourceMeta, builder, budget, check, sink, direct)
}

// directBuild runs the family build over one completed analysis (Rust
// direct_build::build: the retained metadata heap, the direct output
// policy over the overlap components, and the shared complete-ranges
// finish).
func directBuild(codec rangeCodec, m *mapping.Mapping, sourceMeta format.Meta, builder *writer.OutputBuilder, budget *RecoveryBudget, check func() error, sink RecoverySink, analysis *directAnalysis) (*Construction, *constructionFailure) {
	retained := retainedMetadataBytes(analysis.metadata)
	return completeRanges(builder, analysis.metadata, budget.MaxHeapBytes, retained, analysis.report, sink, func(builder *writer.OutputBuilder, rep *reporter) (any, *rangeBuildFailure) {
		policy := &directOutput{builder: builder, rep: rep, codec: codec}
		output := &components{check: check, codec: codec, policy: policy}
		return buildRanges(codec, rangeBuild{
			mapping:           m,
			meta:              sourceMeta,
			budget:            budget,
			check:             check,
			readableRecords:   analysis.readableRecords,
			ordered:           analysis.ordered,
			retainedHeapBytes: retained,
		}, analysis.pages, output)
	})
}
