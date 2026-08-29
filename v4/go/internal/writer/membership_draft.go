// Draft-owned membership interning and refcount finalization (Rust
// draft_store/membership.rs): the DraftStore-level membership wrappers
// over the shared dictionary core, the refcount delta state of the draft,
// and the workflow-finish drain.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// MembershipHandle is one interned membership bitmap plus its stored
// word count (Rust MembershipHandle). The zero handle is the empty
// bitmap.
type MembershipHandle struct {
	id        uint32
	wordCount uint32
}

func EmptyMembershipHandle() MembershipHandle { return MembershipHandle{} }

// isEmpty reports the empty bitmap handle (Rust is_empty).
func (h MembershipHandle) isEmpty() bool { return h.id == 0 }

// stored returns the wire id and word count (Rust stored).
func (h MembershipHandle) stored() (uint32, uint32) { return h.id, h.wordCount }

func handleFromInterned(interned membershipInterned) MembershipHandle {
	return MembershipHandle{id: interned.id, wordCount: interned.wordCount}
}

// membershipState returns the writable dictionary state of the draft
// (Rust DraftStore::membership_state).
func (s *DraftStore) membershipState() membershipState {
	return membershipState{
		idRoot:        s.draft.meta.MembershipIDRoot,
		hashRoot:      s.draft.meta.MembershipHashRoot,
		usedRoot:      s.draft.meta.MembershipUsedRoot,
		entryCount:    s.draft.meta.MembershipEntryCount,
		idLimit:       s.draft.meta.MembershipIDLimit,
		recordScratch: s.recordScratch[:],
		hashScratch:   s.hashScratch[:],
	}
}

// storeMembershipState writes the dictionary state back into the draft
// meta (Rust DraftStore::store_membership_state).
func (s *DraftStore) storeMembershipState(state membershipState) {
	s.draft.meta.MembershipIDRoot = state.idRoot
	s.draft.meta.MembershipHashRoot = state.hashRoot
	s.draft.meta.MembershipUsedRoot = state.usedRoot
	s.draft.meta.MembershipEntryCount = state.entryCount
	s.draft.meta.MembershipIDLimit = state.idLimit
}

// draftInternMembership interns one caller-owned bitmap into the draft
// dictionary, accounting a zero refcount delta for the new record (Rust
// DraftStore::intern_membership + track_new_membership). New records of
// every value kind get the owner-refcount delta exactly like Rust. The
// bitmap source stays concrete: the generic parameter instantiates the
// whole intern path per source type, so the word reads never cross an
// interface call (Rust generic Words on the hot path).
func draftInternMembership[W membershipWords](s *DraftStore, words W) (membershipInterned, error) {
	state := s.membershipState()
	interned, err := internMembership(s, &state, words)
	if err != nil {
		return membershipInterned{}, err
	}
	if err := s.trackNewMembership(interned); err != nil {
		return membershipInterned{}, err
	}
	s.storeMembershipState(state)
	return interned, nil
}

// combineMemberships combines one stored bitmap with a supplied bitmap
// and interns the result (Rust DraftStore::combine_memberships). The
// boolean is Rust's Option: an empty combination reports present=false,
// never a Some(0) id.
func (s *DraftStore) combineMemberships(current, supplied, suppliedWords uint32, operation MembershipOperation) (uint32, bool, error) {
	state := s.membershipState()
	interned, err := combineMembership(s, &state, &s.combineScratch, current, supplied, suppliedWords, operation)
	if err != nil {
		return 0, false, err
	}
	if err := s.trackNewMembership(interned); err != nil {
		return 0, false, err
	}
	s.storeMembershipState(state)
	if interned.id == 0 {
		return 0, false, nil
	}
	return interned.id, true, nil
}

// selectedMembershipBits probes one stored bitmap at the selected feed
// indexes, writing one presence byte per index into caller-owned output
// (Rust DraftStore::selected_membership_bits over contains_indexes).
func (s *DraftStore) selectedMembershipBits(id uint32, indexes []uint32, output []byte, check func() error) error {
	return containsMembershipIndexes(s, s.draft.meta.MembershipIDRoot, id, indexes, output, check)
}

// trackNewMembership accounts the zero refcount delta of one newly
// interned record (Rust track_new_membership).
func (s *DraftStore) trackNewMembership(interned membershipInterned) error {
	if !interned.created {
		return nil
	}
	return s.trackMembershipOwnerRefcount(interned.id, 0)
}

// trackMembershipRefcount accounts one refcount change of a membership
// value (Rust DraftStore::track_membership_refcount; a no-op for direct
// value kinds).
func (s *DraftStore) trackMembershipRefcount(id uint32, change int64) error {
	if s.draft.meta.ValueKind == format.ValueKindMembership {
		return s.trackMembershipOwnerRefcount(id, change)
	}
	return nil
}

// trackMembershipOwnerRefcount buffers one refcount change into the
// draft delta state unconditionally (Rust track_membership_owner_refcount).
func (s *DraftStore) trackMembershipOwnerRefcount(id uint32, change int64) error {
	work.MembershipRefcountBatch(boolToUint64(id != 0))
	root := s.draft.membershipDeltaRoot
	pending := &s.draft.membershipDeltaPending
	if err := trackBufferedDelta(s, &root, pending, id, change); err != nil {
		return err
	}
	s.draft.membershipDeltaRoot = root
	return nil
}

// finishMembershipDeltasWithCheckpoint flushes the pending slots, drains
// the delta tree in id order applying every change, and verifies the
// dictionary never exceeds its owner count (Rust
// DraftStore::finish_membership_deltas_with_checkpoint). Non-membership
// kinds require the delta state empty.
func (s *DraftStore) finishMembershipDeltasWithCheckpoint(checkpoint func() error) error {
	if s.draft.meta.ValueKind != format.ValueKindMembership && s.draft.meta.ValueKind != format.ValueKindStructured {
		return s.requireEmptyDelta()
	}
	root := s.draft.membershipDeltaRoot
	pending := &s.draft.membershipDeltaPending
	if err := flushMembershipDeltas(s, &root, pending); err != nil {
		return err
	}
	s.draft.membershipDeltaRoot = root
	if s.draft.membershipDeltaRoot == 0 {
		return nil
	}
	state := s.membershipState()
	drain, err := newMembershipDeltaDrain(s, s.draft.membershipDeltaRoot)
	if err != nil {
		return err
	}
	for {
		if err := checkpoint(); err != nil {
			return err
		}
		delta, ok, err := drain.next(s)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := applyMembershipDelta(s, &state, delta.id, delta.change); err != nil {
			return err
		}
	}
	s.draft.membershipDeltaRoot = 0
	s.draft.membershipDeltaPending = newDeltaPending()
	s.storeMembershipState(state)
	var ownerCount uint64
	switch s.draft.meta.ValueKind {
	case format.ValueKindMembership:
		ownerCount = s.draft.meta.RangeRecordCount
	case format.ValueKindStructured:
		ownerCount = s.draft.meta.StructureEntryCount
	}
	if s.draft.meta.MembershipEntryCount > ownerCount {
		return corrupt("membership dictionary exceeds its owner count")
	}
	return s.requireEmptyDelta()
}

// requireEmptyDelta rejects a draft carrying unexpected refcount delta
// state (Rust require_empty_delta).
func (s *DraftStore) requireEmptyDelta() error {
	if s.draft.membershipDeltaRoot == 0 && s.draft.membershipDeltaPending.isEmpty() {
		return nil
	}
	return corrupt("transaction contains unexpected membership refcount state")
}

func boolToUint64(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

// addFeedToMembership interns the member bitmap of one feed, optionally
// over a base bitmap (Rust DraftStore::add_feed_to_membership): the
// single-bit source is interned with the base words and the new record
// is tracked before the dictionary state is stored back.
func (s *DraftStore) addFeedToMembership(base MembershipHandle, feed FeedEntry) (MembershipHandle, error) {
	baseID, baseWords := base.stored()
	state := s.membershipState()
	interned, err := internAddedBit(s, &state, baseID, baseWords, feed.Index)
	if err != nil {
		return MembershipHandle{}, err
	}
	if err := s.trackNewMembership(interned); err != nil {
		return MembershipHandle{}, err
	}
	s.storeMembershipState(state)
	return handleFromInterned(interned), nil
}

// applyMembership applies one membership operation over the inclusive
// [from, to] interval of the draft range tree (Rust
// DraftStore::apply_membership): every present segment is combined with
// the supplied member bitmap through the authoritative transform walk,
// the membership dictionary state moves inside each combination, and the
// range root/count commit only after the walk succeeds. The checkpoint
// runs before every cell combination (Rust apply_membership calls the
// checkpoint inside the transform closure).
func (s *DraftStore) applyMembership4(from, to key4, member MembershipHandle, operation MembershipOperation, check func() error) (bool, error) {
	ctx := s.beginRangeEdit4(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	memberID, memberWords := member.stored()
	changed, err := rangeTransform(ctx, from, to, func(store RangeStore, value optionalValue) (optionalValue, error) {
		if err := check(); err != nil {
			return optionalValue{}, err
		}
		current := uint32(0)
		if value.present {
			current = value.value
		}
		id, present, err := s.combineMemberships(current, memberID, memberWords, operation)
		if err != nil {
			return optionalValue{}, err
		}
		return optionalValue{value: id, present: present}, nil
	})
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// applyMembership6 is the IPv6 form of applyMembership4.
func (s *DraftStore) applyMembership6(from, to key6, member MembershipHandle, operation MembershipOperation, check func() error) (bool, error) {
	ctx := s.beginRangeEdit6(s.draft.meta.RangeRoot, s.draft.meta.RangeRecordCount)
	memberID, memberWords := member.stored()
	changed, err := rangeTransform(ctx, from, to, func(store RangeStore, value optionalValue) (optionalValue, error) {
		if err := check(); err != nil {
			return optionalValue{}, err
		}
		current := uint32(0)
		if value.present {
			current = value.value
		}
		id, present, err := s.combineMemberships(current, memberID, memberWords, operation)
		if err != nil {
			return optionalValue{}, err
		}
		return optionalValue{value: id, present: present}, nil
	})
	if err != nil {
		return false, err
	}
	s.commitRangeEdit(&s.draft.meta.RangeRoot, &s.draft.meta.RangeRecordCount, changed)
	return changed, nil
}

// deleteFeedMembershipDifference subtracts one feed member from the
// whole family range of the draft (Rust delete_current_feed_membership):
// the transform runs over the full typed family interval.
func (s *DraftStore) deleteFeedMembershipDifference(member MembershipHandle, check func() error) (bool, error) {
	if s.draft.meta.AddressFamily == format.AddressFamilyIPv4 {
		return s.applyMembership4(key4(0), key4(0xFFFFFFFF), member, MembershipDifference, check)
	}
	max6 := key6{Hi: ^uint64(0), Lo: ^uint64(0)}
	return s.applyMembership6(key6{}, max6, member, MembershipDifference, check)
}

// applyMembershipV4 applies one membership operation over an inclusive
// IPv4 interval (Rust DraftStore::apply_membership_v4).
func (s *DraftStore) applyMembershipV4(from, to uint32, member MembershipHandle, operation MembershipOperation, check func() error) (bool, error) {
	return s.applyMembership4(key4(from), key4(to), member, operation, check)
}

// applyMembershipV6 applies one membership operation over an inclusive
// IPv6 interval (Rust DraftStore::apply_membership_v6).
func (s *DraftStore) applyMembershipV6(fromHi, fromLo, toHi, toLo uint64, member MembershipHandle, operation MembershipOperation, check func() error) (bool, error) {
	return s.applyMembership6(key6{Hi: fromHi, Lo: fromLo}, key6{Hi: toHi, Lo: toLo}, member, operation, check)
}

// deleteCurrentFeedMembership deletes one feed and clears its bit from
// every stored membership (Rust
// DraftStore::delete_current_feed_membership_cancellable): the member
// bitmap is interned, subtracted from the whole family range through the
// authoritative transform, and only then is the catalog entry removed.
func (s *DraftStore) deleteCurrentFeedMembership(feed FeedEntry, check func() error) error {
	// The fault point arms a mid-edit fatal corruption exactly where a
	// malformed draft cell would surface (v4work-only; no-op in
	// production builds), pinning the Rust abort_after branding
	// contract of the public transaction.
	if err := fault.Fail("membership.delete_feed_fatal"); err != nil {
		return corrupt("injected draft corruption: " + err.Error())
	}
	if err := check(); err != nil {
		return err
	}
	member, err := s.addFeedToMembership(EmptyMembershipHandle(), feed)
	if err != nil {
		return err
	}
	if _, err := s.deleteFeedMembershipDifference(member, check); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	return s.removeCurrentFeed(feed)
}
