package recovery

// Canonical immutable output from one membership recovery analysis
// (Rust recovery/membership_build.rs): the membership mode proves the
// absence of a structure index and streams the membership-range
// policy over the shared source-ID lookup.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// membershipConstruct builds one canonical membership output from one
// recovery source (Rust membership_build::construct).
func membershipConstruct(m *mapping.Mapping, sourceMeta format.Meta, builder *writer.OutputBuilder, budget *RecoveryBudget, check func() error, sink RecoverySink) (*Construction, *constructionFailure) {
	return indirectConstruct(indirectMode{
		kind: format.ValueKindMembership,
		checkStructures: func(structures *structureIndex) error {
			if structures != nil {
				return corruptError("membership recovery unexpectedly has a structure index")
			}
			return nil
		},
		output: func(context indirectOutputContext) (*scratchCleanup, *rangeBuildFailure) {
			codec, ok := indirectCodec(context.request.meta.AddressFamily)
			if !ok {
				return nil, &rangeBuildFailure{cause: &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery membership output family is invalid"}}
			}
			policy := &membershipOutput{
				mapping:     context.request.mapping,
				meta:        context.request.meta,
				memberships: context.memberships,
				tables:      context.tables,
				builder:     context.builder,
				rep:         context.rep,
				family:      context.request.meta.AddressFamily,
			}
			return buildRanges(codec, context.request, context.pages, &components{check: context.request.check, codec: codec, policy: policy})
		},
	}, m, sourceMeta, builder, budget, check, sink)
}
