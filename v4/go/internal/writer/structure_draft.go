// Draft-owned structured-value edit core (Rust draft_store/structured.rs
// and structured_value/manager.rs): the network_enrichment_v1 intern with
// optional threat membership, the structure assign/clear through the
// range edit core, the feed-removal transform that re-interns payloads,
// and the structure refcount delta state machine drained at prepare.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// StructureHandle is one draft-owned structure reference (Rust
// StructureHandle): the zero handle is the empty structure that clears
// ranges. Values are opaque to callers; the internal id round-trips
// through the edit bindings.
type StructureHandle struct {
	id uint32
}

// EmptyStructureHandle returns the empty structure handle (Rust
// StructureHandle::empty), the canonical operand of a structured clear.
func EmptyStructureHandle() StructureHandle { return StructureHandle{} }

// stored returns the structure dictionary id (Rust StructureHandle::id).
func (h StructureHandle) stored() uint32 { return h.id }

// requireNetworkEnrichmentV1 mirrors Rust
// DraftStore::require_network_enrichment_v1: the draft must be a
// network_enrichment_v1 structured database.
func (s *DraftStore) requireNetworkEnrichmentV1() error {
	if s.draft.meta.ValueKind != format.ValueKindStructured || s.draft.meta.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return &format.Error{Code: format.CodeWrongStructureKind, Detail: "operation requires a network_enrichment_v1 database"}
	}
	return nil
}

// structureState snapshots the draft structure dictionary state (Rust
// structured_value::State::from_meta).
func (s *DraftStore) structureState() structureState {
	return structureState{
		idRoot:     s.draft.meta.StructureIDRoot,
		hashRoot:   s.draft.meta.StructureHashRoot,
		usedRoot:   s.draft.meta.StructureUsedRoot,
		entryCount: s.draft.meta.StructureEntryCount,
		idLimit:    s.draft.meta.StructureIDLimit,
	}
}

// storeStructureState writes the dictionary state back to the draft meta
// (Rust structured_value::State::write_to).
func (s *DraftStore) storeStructureState(state structureState) {
	s.draft.meta.StructureIDRoot = state.idRoot
	s.draft.meta.StructureHashRoot = state.hashRoot
	s.draft.meta.StructureUsedRoot = state.usedRoot
	s.draft.meta.StructureEntryCount = state.entryCount
	s.draft.meta.StructureIDLimit = state.idLimit
}

// trackStructureRefcount buffers one structure refcount change into the
// draft structure delta state (Rust
// DraftStore::track_structure_refcount over membership_delta
// track_buffered).
func (s *DraftStore) trackStructureRefcount(id uint32, change int64) error {
	root := s.draft.structureDeltaRoot
	if err := trackBufferedDelta(s, &root, &s.draft.structureDeltaPending, id, change); err != nil {
		return err
	}
	s.draft.structureDeltaRoot = root
	return nil
}

// internNetworkEnrichmentV1 encodes one value with the optional threat
// membership and interns it in the draft structure dictionary (Rust
// DraftStore::intern_network_enrichment_v1): a created record buffers a
// zero structure refcount delta and a +1 owner refcount for the
// membership it links.
func (s *DraftStore) internNetworkEnrichmentV1(value format.NetworkEnrichmentV1, membership MembershipHandle) (StructureHandle, error) {
	if err := s.requireNetworkEnrichmentV1(); err != nil {
		return StructureHandle{}, err
	}
	membershipID, _ := membership.stored()
	payload, err := encodeNetworkEnrichmentV1(value, membershipID)
	if err != nil {
		return StructureHandle{}, err
	}
	return s.internStructurePayload(payload)
}

// internStructurePayload interns one canonical payload in the draft
// structure dictionary (Rust DraftStore::intern_payload): a created
// record buffers the structure refcount delta marker and the membership
// owner refcount before the dictionary state is stored back.
func (s *DraftStore) internStructurePayload(payload structurePayload) (StructureHandle, error) {
	state := s.structureState()
	// The generic intern's shape instantiation leaks its payload
	// argument, so the draft-owned scratch carries the payload instead
	// of a stack local (draft_store.go structureScratch).
	s.structureScratch = payload
	interned, err := internStructure(structureNetworkEnrichmentV1{}, s, &state, &s.structureScratch)
	if err != nil {
		return StructureHandle{}, err
	}
	if interned.created {
		if err := s.trackStructureRefcount(interned.id, 0); err != nil {
			return StructureHandle{}, err
		}
		if err := s.trackMembershipOwnerRefcount(interned.membershipID, 1); err != nil {
			return StructureHandle{}, err
		}
	}
	s.storeStructureState(state)
	return StructureHandle{id: interned.id}, nil
}

// assignStructureInputV4 assigns one structured IPv4 range (Rust
// DraftStore::assign_structure_input_v4): the empty structure clears,
// a private range tree assigns through the leaf-locator input, and a
// shared committed tree assigns directly.
func (s *DraftStore) assignStructureInputV4(from, to uint32, structure StructureHandle, input *privateInput) (bool, error) {
	if structure.id == 0 {
		return s.ClearV4(from, to)
	}
	if s.draft.rangeTreePrivate {
		return s.assignInput(tree.KeyOfU32(from), tree.KeyOfU32(to), structure.id, input)
	}
	return s.assign(tree.KeyOfU32(from), tree.KeyOfU32(to), structure.id)
}

// assignStructureInputV6 assigns one structured IPv6 range (Rust
// DraftStore::assign_structure_input_v6).
func (s *DraftStore) assignStructureInputV6(fromHi, fromLo, toHi, toLo uint64, structure StructureHandle, input *privateInput) (bool, error) {
	if structure.id == 0 {
		return s.ClearV6(fromHi, fromLo, toHi, toLo)
	}
	if s.draft.rangeTreePrivate {
		return s.assignInput(tree.KeyOfU128(fromHi, fromLo), tree.KeyOfU128(toHi, toLo), structure.id, input)
	}
	return s.assign(tree.KeyOfU128(fromHi, fromLo), tree.KeyOfU128(toHi, toLo), structure.id)
}

// deleteCurrentStructuredFeed deletes one feed and removes it from every
// stored structure payload (Rust
// DraftStore::delete_current_structured_feed): the member bitmap is
// interned, subtracted from every range cell through the authoritative
// transform, and only then is the catalog entry removed.
func (s *DraftStore) deleteCurrentStructuredFeed(feed FeedEntry, check func() error) error {
	if err := s.requireNetworkEnrichmentV1(); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	member, err := s.addFeedToMembership(EmptyMembershipHandle(), feed)
	if err != nil {
		return err
	}
	family, err := s.rangeFamily()
	if err != nil {
		return err
	}
	ctx := s.beginRangeEdit(family, s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	var minimum, maximum tree.Key
	if s.draft.meta.AddressFamily == format.AddressFamilyIPv4 {
		minimum = tree.KeyOfU32(uint32(0))
		maximum = tree.KeyOfU32(uint32(1<<32 - 1))
	} else {
		minimum = tree.KeyOfU128(uint64(0), uint64(0))
		maximum = tree.KeyOfU128(^uint64(0), ^uint64(0))
	}
	changed, err := rangeTransform(ctx, minimum, maximum, func(store RangeStore, value optionalValue) (optionalValue, error) {
		if !value.present {
			return optionalValue{}, nil
		}
		return s.removeFeedFromStructure(store, value.value, member, check)
	})
	if err != nil {
		return err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	if err := check(); err != nil {
		return err
	}
	return s.removeCurrentFeed(feed)
}

// removeFeedFromStructure removes one feed from one stored structure
// payload, re-interning the replacement (Rust
// DraftStore::remove_feed_from_structure): an unchanged combination
// keeps the current structure, and an emptied membership re-interns the
// payload with membership zero.
func (s *DraftStore) removeFeedFromStructure(store RangeStore, structureID uint32, removed MembershipHandle, check func() error) (optionalValue, error) {
	if err := check(); err != nil {
		return optionalValue{}, err
	}
	record, found, err := structureTableFind(structureNetworkEnrichmentV1{}, store, s.draft.meta.StructureIDRoot, s.draft.meta.StructureIDLimit, structureID)
	if err != nil {
		return optionalValue{}, err
	}
	if !found {
		return optionalValue{}, corrupt("range names a missing structure")
	}
	removedID, removedWords := removed.stored()
	membershipID := structureNetworkEnrichmentV1{}.membershipID(&record.payload)
	replacement, present, err := s.combineMemberships(membershipID, removedID, removedWords, MembershipDifference)
	if err != nil {
		return optionalValue{}, err
	}
	if (present && replacement == membershipID) || (!present && membershipID == 0) {
		return optionalValue{value: structureID, present: true}, nil
	}
	nextMembership := uint32(0)
	if present {
		nextMembership = replacement
	}
	payload, err := structureNetworkEnrichmentV1{}.withMembership(record.payload, nextMembership)
	if err != nil {
		return optionalValue{}, err
	}
	structure, err := s.internStructurePayload(payload)
	if err != nil {
		return optionalValue{}, err
	}
	return optionalValue{value: structure.id, present: structure.id != 0}, nil
}

// finishStructureDeltasWithCheckpoint flushes the pending structure
// deltas, drains the delta tree in id order applying every change, and
// releases the membership of each deleted structure (Rust
// DraftStore::finish_structure_deltas_with_checkpoint). Non-structured
// kinds require the delta state empty.
func (s *DraftStore) finishStructureDeltasWithCheckpoint(checkpoint func() error) error {
	if s.draft.meta.ValueKind != format.ValueKindStructured {
		return s.requireEmptyStructureDelta()
	}
	root := s.draft.structureDeltaRoot
	if err := flushMembershipDeltas(s, &root, &s.draft.structureDeltaPending); err != nil {
		return err
	}
	s.draft.structureDeltaRoot = root
	if root == 0 {
		return nil
	}
	state := s.structureState()
	drain, err := newMembershipDeltaDrain(s, root)
	if err != nil {
		return err
	}
	for {
		delta, ok, err := drain.next(s)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := checkpoint(); err != nil {
			return err
		}
		released, err := applyStructureDelta(structureNetworkEnrichmentV1{}, s, &state, delta.id, delta.change)
		if err != nil {
			return err
		}
		if released != 0 {
			if err := s.trackMembershipOwnerRefcount(released, -1); err != nil {
				return err
			}
		}
	}
	s.draft.structureDeltaRoot = 0
	s.draft.structureDeltaPending = newDeltaPending()
	s.storeStructureState(state)
	if s.draft.meta.StructureEntryCount > s.draft.meta.RangeRecordCount {
		return corrupt("structure dictionary exceeds the range-record count")
	}
	return s.requireEmptyStructureDelta()
}

// requireEmptyStructureDelta rejects a draft carrying unexpected
// structure refcount delta state (Rust require_empty_delta over the
// structure delta fields).
func (s *DraftStore) requireEmptyStructureDelta() error {
	if s.draft.structureDeltaRoot == 0 && s.draft.structureDeltaPending.isEmpty() {
		return nil
	}
	return corrupt("transaction contains unexpected structure refcount state")
}
