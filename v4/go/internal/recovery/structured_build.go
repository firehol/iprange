package recovery

// Canonical immutable output from one structured recovery analysis
// (Rust recovery/structured_build.rs): the structured mode proves the
// network-enrichment structure index and streams the structured-range
// policy over the shared source-ID lookups.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// structuredConstruct builds one canonical structured output from one
// recovery source (Rust structured_build::construct).
func structuredConstruct(m *mapping.Mapping, sourceMeta format.Meta, builder *writer.OutputBuilder, budget *RecoveryBudget, check func() error, sink RecoverySink) (*Construction, *constructionFailure) {
	return indirectConstruct(indirectMode{
		kind: format.ValueKindStructured,
		checkStructures: func(structures *structureIndex) error {
			if structures == nil {
				return corruptError("structured recovery has no structure index")
			}
			if structures.kind != format.StructureKindNetworkEnrichmentV1 {
				return &format.Error{Code: format.CodeUnsupportedStructure, Detail: "recovery structure kind is unsupported"}
			}
			return nil
		},
		output: func(context indirectOutputContext) (*scratchCleanup, *rangeBuildFailure) {
			codec, ok := indirectCodec(context.request.meta.AddressFamily)
			if !ok {
				return nil, &rangeBuildFailure{cause: &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery structured output family is invalid"}}
			}
			structures := context.structures
			if structures == nil {
				return nil, &rangeBuildFailure{cause: corruptError("structured recovery has no structure index")}
			}
			policy := &structuredOutput{
				mapping:     context.request.mapping,
				meta:        context.request.meta,
				memberships: context.memberships,
				structures:  structures,
				tables:      context.tables,
				builder:     context.builder,
				rep:         context.rep,
				family:      context.request.meta.AddressFamily,
			}
			return buildRanges(codec, context.request, context.pages, &components{check: context.request.check, codec: codec, policy: policy})
		},
	}, m, sourceMeta, builder, budget, check, sink)
}
