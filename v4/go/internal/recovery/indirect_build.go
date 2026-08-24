package recovery

// Shared immutable construction for membership-backed recovery modes
// (Rust recovery/indirect_build.rs): one analysis is prepared against
// the destination builder, the feeds are re-pushed from the recovered
// catalog, and the family build folds the mode output through the
// shared range build and the complete-ranges finish.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// indirectMode is one membership-backed recovery output mode (Rust
// Mode): the value-kind guard, the structure proof, and the range
// output policy over the recovery tables.
type indirectMode struct {
	kind            uint8
	checkStructures func(structures *structureIndex) error
	output          func(context indirectOutputContext) (any, *rangeBuildFailure)
}

// indirectOutputContext carries one mode output request (Rust
// OutputContext).
type indirectOutputContext struct {
	request     rangeBuild
	pages       *pageSet
	memberships *membershipIndex
	structures  *structureIndex
	tables      *tableStore
	builder     *writer.OutputBuilder
	rep         *reporter
}

// indirectConstruct runs one membership-backed recovery construction
// (Rust indirect_build::construct): the preparation proves the
// destination against the source generation and runs the analysis,
// then the family build streams the mode output.
func indirectConstruct(mode indirectMode, m *mapping.Mapping, sourceMeta format.Meta, builder *writer.OutputBuilder, budget *RecoveryBudget, check func() error, sink RecoverySink) (*Construction, *constructionFailure) {
	analysis, failure := prepareConstruction(builder, sourceMeta, mode.kind, func() (any, *analysisFailure) {
		result, failed := indirectAnalyze(m, sourceMeta, budget, check, sink, mode.kind)
		if failed != nil {
			return nil, failed
		}
		return result, nil
	})
	if failure != nil {
		return nil, failure
	}
	indirect := analysis.(*indirectAnalysis)
	if err := mode.checkStructures(indirect.structures); err != nil {
		return nil, constructionFailureOf(builder, err, indirect.report, nil)
	}
	codec, ok := indirectCodec(sourceMeta.AddressFamily)
	if !ok {
		return nil, constructionFailureOf(builder, &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery indirect family is invalid"}, indirect.report, nil)
	}
	return indirectBuild(mode, codec, m, sourceMeta, builder, budget, check, sink, indirect)
}

// indirectBuild runs the family build over one completed analysis
// (Rust indirect_build::build: the structure proof, the recovered
// catalog feeds, the retained tables heap, and the complete-ranges
// finish over the mode output).
func indirectBuild(mode indirectMode, codec rangeCodec, m *mapping.Mapping, sourceMeta format.Meta, builder *writer.OutputBuilder, budget *RecoveryBudget, check func() error, sink RecoverySink, analysis *indirectAnalysis) (*Construction, *constructionFailure) {
	if err := analysis.catalog.forEach(analysis.tables, func(entry catalogFeed) error {
		return builder.PushFeed(string(entry.name), entry.index)
	}); err != nil {
		return nil, constructionFailureOf(builder, err, analysis.report, nil)
	}
	retained, err := retainedIndirectBytes(analysis.tables, analysis.metadata)
	if err != nil {
		return nil, constructionFailureOf(builder, err, analysis.report, nil)
	}
	// The metadata compression window charges only the retained
	// metadata bytes (Rust indirect_build::build: complete_ranges over
	// retained_metadata_bytes); the full retained tables heap travels
	// only in the range-build context below.
	return completeRanges(builder, analysis.metadata, budget.MaxHeapBytes, retainedMetadataBytes(analysis.metadata), analysis.report, sink, func(builder *writer.OutputBuilder, rep *reporter) (any, *rangeBuildFailure) {
		context := indirectOutputContext{
			request: rangeBuild{
				mapping:           m,
				meta:              sourceMeta,
				budget:            budget,
				check:             check,
				readableRecords:   analysis.readableRecords,
				ordered:           analysis.ordered,
				retainedHeapBytes: retained,
			},
			pages:       analysis.pages,
			memberships: analysis.memberships,
			structures:  analysis.structures,
			tables:      analysis.tables,
			builder:     builder,
			rep:         rep,
		}
		return mode.output(context)
	})
}

// retainedIndirectBytes is the heap retained by the tables and the
// analyzed metadata (Rust indirect_build::retained_bytes).
func retainedIndirectBytes(tables *tableStore, metadata []byte) (uint64, error) {
	retained, ok := checkedAdd(tables.retainedBytes(), retainedMetadataBytes(metadata))
	if !ok {
		return 0, overflowError("recovery retained heap")
	}
	return retained, nil
}

// indirectCodec returns the family codec of one indirect source.
func indirectCodec(family uint8) (rangeCodec, bool) {
	switch family {
	case format.AddressFamilyIPv4:
		return rangeV4Codec{}, true
	case format.AddressFamilyIPv6:
		return rangeV6Codec{}, true
	default:
		return nil, false
	}
}
