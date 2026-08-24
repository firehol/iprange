package recovery

// Canonical output for ranges backed by recovered typed structures
// (Rust recovery/structured_output.rs): one accepted component pushes
// the decoded network-enrichment value with its optional membership
// bitmap, and one unresolved structure streams the missing-structure
// or invalid-membership fence.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// structuredResolved is one resolved structure record with its
// optional membership (Rust Resolved).
type structuredResolved struct {
	structure  structureLocator
	membership *membershipLocator
}

// structuredOutput is the structured-range policy of the
// overlap-component pass (Rust NetworkEnrichmentV1Output).
type structuredOutput struct {
	mapping     *mapping.Mapping
	meta        format.Meta
	memberships *membershipIndex
	structures  *structureIndex
	tables      *tableStore
	builder     *writer.OutputBuilder
	rep         *reporter
	family      uint8
}

// resolve proves one record's structure and membership (Rust
// resolve_record: the structure lookup, the membership dependency, and
// their fence envelopes).
func (o *structuredOutput) resolve(record rangeRecord) (any, error) {
	structure, found, err := o.structures.get(o.tables, record.value)
	if err != nil {
		return nil, err
	}
	if !found || structure.rejected {
		fence := o.codec().fence(record.from, record.to)
		if err := o.rep.unknown(unknownEnvelope{
			reason:       validation.ReasonStructureMissing,
			object:       validation.ObjectStructureDictionary,
			addressFence: &fence,
		}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var membership *membershipLocator
	if structure.membershipID != 0 {
		locator, found, err := o.memberships.get(o.tables, structure.membershipID)
		if err != nil {
			return nil, err
		}
		if !found || locator.rejected {
			fence := o.codec().fence(record.from, record.to)
			page := structure.leafPage
			if err := o.rep.unknown(unknownEnvelope{
				reason:       validation.ReasonStructureMembershipInvalid,
				object:       validation.ObjectStructureDictionary,
				pageNumber:   &page,
				addressFence: &fence,
			}); err != nil {
				return nil, err
			}
			return nil, nil
		}
		membership = &locator
	}
	return structuredResolved{structure: structure, membership: membership}, nil
}

// accept counts one accepted component and pushes it (Rust
// NetworkEnrichmentV1Output::accept: the missing structure reports the
// rejected range, the resolved structure streams the range).
func (o *structuredOutput) accept(record rangeRecord, resolved any) error {
	value, ok := resolved.(structuredResolved)
	if !ok {
		if err := o.codec().reportRejected(o.rep, 1, record.from, record.to); err != nil {
			return err
		}
		return nil
	}
	payload := value.structure.payloadBytes()
	decoded, err := format.DecodeNetworkEnrichmentV1(payload)
	if err != nil {
		return err
	}
	if err := o.push(record, decoded, value.membership); err != nil {
		return err
	}
	return o.codec().reportAccepted(o.rep, record)
}

// rejectOverlap streams one whole overlap component (Rust
// NetworkEnrichmentV1Output::reject_overlap).
func (o *structuredOutput) rejectOverlap(count uint64, from, to rangeKey) error {
	return reportOverlap(o.rep, o.codec(), count, from, to)
}

// finish proves nothing for the structured policy (Rust
// NetworkEnrichmentV1Output::finish: no pending range).
func (o *structuredOutput) finish() error { return nil }

// push streams one structured range to the destination builder (Rust
// StructuredKey::push over the optional membership words).
func (o *structuredOutput) push(record rangeRecord, value format.NetworkEnrichmentV1, membership *membershipLocator) error {
	switch o.family {
	case format.AddressFamilyIPv4:
		if membership == nil {
			return o.builder.PushNetworkEnrichmentV1V4Words(uint32(record.from.hi), uint32(record.to.hi), value, nil)
		}
		return o.builder.PushNetworkEnrichmentV1V4Words(uint32(record.from.hi), uint32(record.to.hi), value, locatorWords{reader: membershipWordReader{m: o.mapping, meta: o.meta, locator: *membership}})
	case format.AddressFamilyIPv6:
		if membership == nil {
			return o.builder.PushNetworkEnrichmentV1V6Words(record.from.hi, record.from.lo, record.to.hi, record.to.lo, value, nil)
		}
		return o.builder.PushNetworkEnrichmentV1V6Words(record.from.hi, record.from.lo, record.to.hi, record.to.lo, value, locatorWords{reader: membershipWordReader{m: o.mapping, meta: o.meta, locator: *membership}})
	default:
		return corruptError("recovery structured output family is invalid")
	}
}

// codec returns the family codec of one structured output.
func (o *structuredOutput) codec() rangeCodec {
	switch o.family {
	case format.AddressFamilyIPv4:
		return rangeV4Codec{}
	case format.AddressFamilyIPv6:
		return rangeV6Codec{}
	default:
		return nil
	}
}
