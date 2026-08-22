// Draft-owned membership interning and refcount finalization (Rust
// draft_store/membership.rs): the DraftStore-level membership wrappers
// over the shared dictionary core, the refcount delta state of the draft,
// and the workflow-finish drain.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// membershipHandle is one interned membership bitmap plus its stored
// word count (Rust MembershipHandle). The zero handle is the empty
// bitmap.
type membershipHandle struct {
	id        uint32
	wordCount uint32
}

func emptyMembershipHandle() membershipHandle { return membershipHandle{} }

// isEmpty reports the empty bitmap handle (Rust is_empty).
func (h membershipHandle) isEmpty() bool { return h.id == 0 }

// stored returns the wire id and word count (Rust stored).
func (h membershipHandle) stored() (uint32, uint32) { return h.id, h.wordCount }

func handleFromInterned(interned membershipInterned) membershipHandle {
	return membershipHandle{id: interned.id, wordCount: interned.wordCount}
}

// membershipState returns the writable dictionary state of the draft
// (Rust DraftStore::membership_state).
func (s *DraftStore) membershipState() membershipState {
	return membershipState{
		idRoot:     s.draft.meta.MembershipIDRoot,
		hashRoot:   s.draft.meta.MembershipHashRoot,
		usedRoot:   s.draft.meta.MembershipUsedRoot,
		entryCount: s.draft.meta.MembershipEntryCount,
		idLimit:    s.draft.meta.MembershipIDLimit,
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

// internMembership interns one caller-owned bitmap into the draft
// dictionary, accounting a zero refcount delta for the new record (Rust
// DraftStore::intern_membership + track_new_membership). New records of
// every value kind get the owner-refcount delta exactly like Rust.
func (s *DraftStore) internMembership(words membershipWords) (membershipInterned, error) {
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
func (s *DraftStore) combineMemberships(current, supplied, suppliedWords uint32, operation membershipOperation) (uint32, bool, error) {
	state := s.membershipState()
	interned, err := combineMembership(s, &state, current, supplied, suppliedWords, operation)
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
		delta, err := drain.next(s)
		if err != nil {
			return err
		}
		if delta == nil {
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
