// Structured-value construction for immutable outputs (Rust
// immutable_output/structured.rs): the structure-mode guards, the
// network_enrichment_v1 payload intern with optional membership words, the
// per-range push through the shared range bulk machinery, and the bounded
// structure reference batch that aggregates recurring structure refcounts.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// structureReferenceBatch is the fixed-memory recurring-reference table of
// structure IDs (Rust immutable_output/reference_batch.rs used by
// structured.rs). The Rust authority uses one ReferenceBatch type for both
// the membership and the structure batches; the Go peer shares the exact
// same slot machinery, so this is an alias of the membership batch.
type structureReferenceBatch = membershipReferenceBatch

// PushNetworkEnrichmentV1V4 interns one network_enrichment_v1 value and
// appends one IPv4 structured range (Rust
// push_network_enrichment_v1_v4): the structure kind and family guards,
// the optional membership words interned through the membership
// dictionary, the payload intern, the range push, and the structure
// reference.
func (b *OutputBuilder) PushNetworkEnrichmentV1V4(from, to uint32, value format.NetworkEnrichmentV1, membership OutputWords) error {
	return b.mutate(func() error {
		if err := b.requireStructureMode(format.StructureKindNetworkEnrichmentV1, format.AddressFamilyIPv4); err != nil {
			return err
		}
		structure, err := b.internNetworkEnrichmentV1(value, membership)
		if err != nil {
			return err
		}
		if err := b.ranges.push(b, rangeRecord{
			from:  tree.Key{Hi: uint64(from)},
			to:    tree.Key{Hi: uint64(to)},
			value: structure,
		}); err != nil {
			return err
		}
		return b.addStructureReference(structure)
	})
}

// PushNetworkEnrichmentV1V6 interns one network_enrichment_v1 value and
// appends one IPv6 structured range (Rust
// push_network_enrichment_v1_v6).
func (b *OutputBuilder) PushNetworkEnrichmentV1V6(fromHi, fromLo, toHi, toLo uint64, value format.NetworkEnrichmentV1, membership OutputWords) error {
	return b.mutate(func() error {
		if err := b.requireStructureMode(format.StructureKindNetworkEnrichmentV1, format.AddressFamilyIPv6); err != nil {
			return err
		}
		structure, err := b.internNetworkEnrichmentV1(value, membership)
		if err != nil {
			return err
		}
		if err := b.ranges.push(b, rangeRecord{
			from:  tree.Key{Hi: fromHi, Lo: fromLo},
			to:    tree.Key{Hi: toHi, Lo: toLo},
			value: structure,
		}); err != nil {
			return err
		}
		return b.addStructureReference(structure)
	})
}

// internNetworkEnrichmentV1 encodes the payload with the optional interned
// membership id and interns it into the structure dictionary (Rust
// intern_network_enrichment_v1): a newly created structure that references
// a membership adds the membership reference, and an absent payload (an
// all-zero structure) can never back a range.
func (b *OutputBuilder) internNetworkEnrichmentV1(value format.NetworkEnrichmentV1, membership OutputWords) (uint32, error) {
	membershipID := uint32(0)
	if membership != nil {
		var err error
		membershipID, err = b.internMembership(membership)
		if err != nil {
			return 0, err
		}
	}
	payload, err := encodeNetworkEnrichmentV1(value, membershipID)
	if err != nil {
		return 0, err
	}
	state := b.structureState()
	interned, err := internStructure(structureNetworkEnrichmentV1{}, b, &state, payload)
	if err != nil {
		return 0, err
	}
	b.storeStructureState(state)
	if interned.id == 0 {
		return 0, invalid("an absent structure cannot create a range")
	}
	if interned.created && interned.membershipID != 0 {
		if err := b.addMembershipReference(interned.membershipID); err != nil {
			return 0, err
		}
	}
	return interned.id, nil
}

// addStructureReference records one reference and applies it directly when
// the batch is disabled, or flushes a full batch and retries (Rust
// add_structure_reference).
func (b *OutputBuilder) addStructureReference(value uint32) error {
	switch outcome, err := b.structureRefs.addReference(value); {
	case err != nil:
		return err
	case outcome == referenceAdded:
		return nil
	case outcome == referenceDirect:
		return b.applyStructureReference(value)
	case outcome == referenceFull:
	}
	if err := b.flushStructureReferences(); err != nil {
		return err
	}
	switch outcome, err := b.structureRefs.addReference(value); {
	case err != nil:
		return err
	case outcome == referenceAdded:
		return nil
	case outcome == referenceFull:
		return corrupt("empty structure reference batch stayed full")
	case outcome == referenceDirect:
		return b.applyStructureReference(value)
	}
	return nil
}

// applyStructureReference applies one refcount delta immediately (Rust
// apply_structure_reference).
func (b *OutputBuilder) applyStructureReference(value uint32) error {
	state := b.structureState()
	if _, err := applyStructureDelta(structureNetworkEnrichmentV1{}, b, &state, value, 1); err != nil {
		return err
	}
	b.storeStructureState(state)
	return nil
}

// flushStructureReferences applies every pending structure delta (Rust
// flush_structure_references).
func (b *OutputBuilder) flushStructureReferences() error {
	if b.structureRefs.isEmpty() {
		return nil
	}
	state := b.structureState()
	for index := 0; index < b.structureRefs.capacity(); index++ {
		id, count, ok := b.structureRefs.takeReference(index)
		if !ok {
			continue
		}
		if _, err := applyStructureDelta(structureNetworkEnrichmentV1{}, b, &state, id, count); err != nil {
			return err
		}
	}
	b.structureRefs.finishFlush()
	b.storeStructureState(state)
	return nil
}

// requireStructureMode checks the structure kind and address family of one
// structured operation (Rust require_structure_mode): a non-structured
// output or a different structure kind is WrongStructureKind, a family
// mismatch is WrongAddressFamily.
func (b *OutputBuilder) requireStructureMode(kind, family uint8) error {
	if b.meta.ValueKind != format.ValueKindStructured || b.meta.StructureKind != kind {
		return wrongStructureKind("immutable output operation does not match its structure kind")
	}
	if b.meta.AddressFamily != family {
		return &format.Error{Code: format.CodeWrongAddressFamily, Detail: "immutable output operation does not match its address family"}
	}
	return nil
}

// structureState captures the writable dictionary state (Rust
// State::from_meta).
func (b *OutputBuilder) structureState() structureState {
	return structureState{
		idRoot:     b.meta.StructureIDRoot,
		hashRoot:   b.meta.StructureHashRoot,
		usedRoot:   b.meta.StructureUsedRoot,
		entryCount: b.meta.StructureEntryCount,
		idLimit:    b.meta.StructureIDLimit,
	}
}

// storeStructureState persists the dictionary state into the meta (Rust
// State::write_to).
func (b *OutputBuilder) storeStructureState(state structureState) {
	b.meta.StructureIDRoot = state.idRoot
	b.meta.StructureHashRoot = state.hashRoot
	b.meta.StructureUsedRoot = state.usedRoot
	b.meta.StructureEntryCount = state.entryCount
	b.meta.StructureIDLimit = state.idLimit
}
