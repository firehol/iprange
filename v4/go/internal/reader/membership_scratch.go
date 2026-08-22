package reader

// Authoritative selected-membership decoder for scoped scans and joins
// (Rust membership_query/decode.rs and cache.rs parity). A Scratch holds
// the selected feeds (position list plus set flags) of one membership
// bitmap and a bounded exact cache keyed by membership ID, so recurring
// sequences are decoded once. The heap model keeps the exact Rust
// size_of accounting; Go allocations are separate and bounded by the
// same charges.

import (
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Rust size parity constants (Rust size_of on the same logical types):
// Cardinality129 is 24 bytes (u8 + padding + two u64), a u32 is 4, a
// usize is 8, a cache Slot is 24 (u64 + three u32 with alignment), and
// the pair/join cells are 40 (four u32 + Cardinality129).
const (
	rustCardSize = 24
	rustU32Size  = 4
	rustUsize    = 8
	rustSlotSize = 24
	rustCellSize = 40
)

// operationHeap is the modeled per-operation heap (Rust heap.rs). Every
// charge uses the exact Rust byte accounting; a charge above the
// remaining budget fails with CodeInsufficientResourceBudget and the
// given label.
type operationHeap struct {
	remaining uint64
}

func newOperationHeap(remaining uint64) *operationHeap {
	return &operationHeap{remaining: remaining}
}

func (h *operationHeap) charge(bytes uint64, label string) error {
	if bytes > h.remaining {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: label}
	}
	h.remaining -= bytes
	return nil
}

// filled charges capacity * size exactly like Rust heap.filled (the
// charge is the allocation, not the fill).
func (h *operationHeap) filled(capacity, size uint64, label string) error {
	bytes, ok := mulChecked(capacity, size)
	if !ok {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: label}
	}
	return h.charge(bytes, label)
}

// remaining reports the uncharged budget.
func (h *operationHeap) remainingBytes() uint64 {
	return h.remaining
}

// seqCacheSlot is one open-addressed cache entry (Rust cache::Slot).
type seqCacheSlot struct {
	hash          uint64
	membershipID  uint32
	offset        uint32
	lengthPlusOne uint32
}

// sequenceCache is a bounded exact cache of selected-feed sequences,
// keyed by membership ID (Rust SequenceCache keyed surface; the
// sequence-keyed surface belongs to the chunk-3 algebra machinery).
type sequenceCache struct {
	slots       []seqCacheSlot
	positions   []uint32
	positionCap uint32
	mask        uint64
	entries     uint32
	entryLimit  uint32
}

func (c *sequenceCache) empty() {
	c.slots = nil
	c.positions = nil
}

// enable sizes the cache from the given byte budget (Rust
// SequenceCache::enable): entry limit is floor power of two of the
// affordable slot pairs, capped at 1024 entries and 65536 positions; a
// cache that cannot fit its minimum shape stays disabled.
func (c *sequenceCache) enable(heap *operationHeap, maxBytes uint64) error {
	if len(c.slots) != 0 {
		return nil
	}
	available := heap.remainingBytes()
	if available > maxBytes {
		available = maxBytes
	}
	minimum := uint64(rustSlotSize*2) + rustU32Size
	possible := int(available / minimum)
	if possible > 1024 {
		possible = 1024
	}
	entryLimit := floorPowerOfTwo(possible)
	if entryLimit == 0 {
		return nil
	}
	slotCount := entryLimit * 2
	slotBytes, ok := mulChecked(uint64(slotCount), rustSlotSize)
	if !ok {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership sequence cache"}
	}
	remaining := uint64(0)
	if available > slotBytes {
		remaining = available - slotBytes
	}
	positionCap := int(remaining / rustU32Size)
	if positionCap > 65536 {
		positionCap = 65536
	}
	if positionCap == 0 {
		return nil
	}
	if err := heap.filled(uint64(slotCount), rustSlotSize, "membership sequence cache"); err != nil {
		return err
	}
	if err := heap.filled(uint64(positionCap), rustU32Size, "membership sequence cache"); err != nil {
		return err
	}
	c.slots = make([]seqCacheSlot, slotCount)
	c.positions = make([]uint32, 0, positionCap)
	c.positionCap = uint32(positionCap)
	c.mask = uint64(slotCount - 1)
	c.entryLimit = uint32(entryLimit)
	return nil
}

// keyed returns the cached sequence for one membership ID, or nil.
func (c *sequenceCache) keyed(membershipID uint32) []uint32 {
	if membershipID == 0 || len(c.slots) == 0 {
		return nil
	}
	hash := mix(uint64(membershipID))
	index := hash & c.mask
	for range c.slots {
		slot := c.slots[index]
		if slot.lengthPlusOne == 0 {
			return nil
		}
		if slot.hash == hash && slot.membershipID == membershipID {
			start := int(slot.offset)
			length := int(slot.lengthPlusOne) - 1
			return c.positions[start : start+length]
		}
		index = (index + 1) & c.mask
	}
	return nil
}

// insertKeyed stores one membership sequence, dropping it silently when
// the cache is full (Rust SequenceCache::insert).
func (c *sequenceCache) insertKeyed(membershipID uint32, positions []uint32) error {
	if membershipID == 0 {
		return corrupt("membership cache key is zero")
	}
	if len(c.slots) == 0 || c.entries == c.entryLimit ||
		len(positions) > int(c.positionCap)-len(c.positions) {
		return nil
	}
	length := uint32(len(positions))
	offset := uint32(len(c.positions))
	hash := mix(uint64(membershipID))
	index := hash & c.mask
	for range c.slots {
		if c.slots[index].lengthPlusOne == 0 {
			c.positions = append(c.positions, positions...)
			c.slots[index] = seqCacheSlot{
				hash:          hash,
				membershipID:  membershipID,
				offset:        offset,
				lengthPlusOne: length + 1,
			}
			c.entries++
			return nil
		}
		index = (index + 1) & c.mask
	}
	return nil
}

// floorPowerOfTwo returns the largest power of two at or below value.
func floorPowerOfTwo(value int) int {
	if value <= 0 {
		return 0
	}
	return 1 << (bitsLen(uint64(value)) - 1)
}

func bitsLen(v uint64) int {
	n := 0
	for v > 0 {
		n++
		v >>= 1
	}
	return n
}

// mix is the Rust cache hash mixer (cache.rs mix).
func mix(value uint64) uint64 {
	value ^= value >> 30
	value = value * 0xbf58_476d_1ce4_e5b9
	value ^= value >> 27
	value = value * 0x94d0_49bb_1331_11eb
	return value ^ (value >> 31)
}

// scratch is one reusable selected-membership decoder (Rust Scratch).
type scratch struct {
	present      []uint32
	flags        []byte
	membershipID uint32
	loaded       bool
	cache        sequenceCache
}

func newScratch(feeds int, heap *operationHeap) (*scratch, error) {
	if err := heap.filled(uint64(feeds), rustU32Size, "membership aggregation heap"); err != nil {
		return nil, err
	}
	if err := heap.filled(uint64(feeds), 1, "membership aggregation heap"); err != nil {
		return nil, err
	}
	return &scratch{
		present: make([]uint32, 0, feeds),
		flags:   make([]byte, feeds),
	}, nil
}

// clear drops the current membership and resets the selected set.
func (s *scratch) clear(check checkpoint) error {
	s.membershipID = 0
	s.loaded = false
	for i, position := range s.present {
		if err := checkEvery(i, check); err != nil {
			return err
		}
		s.flags[position] = 0
	}
	s.present = s.present[:0]
	return nil
}

// load decodes one membership bitmap into the selected set, serving
// recurring IDs from the cache (Rust Scratch::load; a zero ID is refused
// as corruption by the read path). The loaded marker mirrors Rust's
// Option<MembershipToken>: an ID equal to the zero marker is only a cache
// hit after a real load, so a corrupt range naming membership 0 still
// reaches lookupMembershipID and fails Corrupt exactly like Rust.
func (s *scratch) load(r *ImmutableReader, membershipID uint32, scope *ScopeData, check checkpoint) error {
	if s.loaded && s.membershipID == membershipID {
		return nil
	}
	if err := s.clear(check); err != nil {
		return err
	}
	if cached := s.cache.keyed(membershipID); cached != nil {
		for i, position := range cached {
			if err := checkEvery(i, check); err != nil {
				return err
			}
			s.flags[position] = 1
			s.present = append(s.present, position)
		}
		s.membershipID = membershipID
		s.loaded = true
		work.MembershipDecodeCacheHit(1)
		return nil
	}
	view, err := r.LookupMembershipID(membershipID)
	if err != nil {
		return err
	}
	if err := s.decode(&view, scope, check); err != nil {
		return err
	}
	if err := s.cache.insertKeyed(membershipID, s.present); err != nil {
		return err
	}
	s.membershipID = membershipID
	s.loaded = true
	return nil
}

// decode selects the feeding words of one membership view (Rust
// Scratch::decode): the selected-word walk when the scope touches fewer
// words, the full bitmap walk otherwise.
func (s *scratch) decode(view *MembershipView, scope *ScopeData, check checkpoint) error {
	work.MembershipDecode(1)
	wordCount := view.WordCount()
	if uint64(scope.selectedWordCount)*4 < uint64(wordCount) {
		return s.decodeSelected(view, scope, wordCount, check)
	}
	return s.decodeAll(view, scope, wordCount, check)
}

func (s *scratch) decodeSelected(view *MembershipView, scope *ScopeData, wordCount uint32, check checkpoint) error {
	position := 0
	for position < len(scope.entries) {
		wordIndex := scope.entries[position].FeedIndex / 64
		if wordIndex >= wordCount {
			break
		}
		word, found, err := view.Word(wordIndex)
		if err != nil {
			return err
		}
		if !found {
			return corrupt("membership word disappeared")
		}
		work.MembershipWordRead(1)
		for position < len(scope.entries) && scope.entries[position].FeedIndex/64 == wordIndex {
			if err := checkEvery(position, check); err != nil {
				return err
			}
			entry := scope.entries[position]
			if word&(uint64(1)<<(entry.FeedIndex%64)) != 0 {
				if err := s.push(position); err != nil {
					return err
				}
			}
			position++
		}
	}
	return nil
}

func (s *scratch) decodeAll(view *MembershipView, scope *ScopeData, wordCount uint32, check checkpoint) error {
	const wordBatch = 64
	var words [wordBatch]uint64
	var start uint32
	for start < wordCount {
		if start != 0 && check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		expected := wordCount - start
		if expected > wordBatch {
			expected = wordBatch
		}
		read, err := view.ReadWords(start, words[:expected])
		if err != nil {
			return err
		}
		if uint32(read) != expected {
			return corrupt("membership word read ended early")
		}
		work.MembershipWordRead(uint64(read))
		for offset, word := range words[:read] {
			wordIndex := start + uint32(offset)
			for word != 0 {
				bit := uint32(bits.TrailingZeros64(word))
				if wordIndex > ^uint32(0)/64 {
					return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "feed index"}
				}
				feedIndex := wordIndex*64 + bit
				if position, found := scope.position(feedIndex); found {
					if err := s.push(position); err != nil {
						return err
					}
				} else if scope.allCatalog {
					return corrupt("membership names an inactive feed")
				}
				word &= word - 1
			}
		}
		next, ok := addCheckedU32(start, uint32(read))
		if !ok {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership word index"}
		}
		start = next
	}
	return nil
}

// push records one selected scope position (Rust Scratch::push).
func (s *scratch) push(position int) error {
	if uint64(position) > uint64(^uint32(0)) {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership scope exceeds u32"}
	}
	if s.flags[position] != 0 {
		return corrupt("membership feed bit was decoded twice")
	}
	s.flags[position] = 1
	s.present = append(s.present, uint32(position))
	return nil
}

// presentList returns the current selected scope positions.
func (s *scratch) presentList() []uint32 {
	return s.present
}

func addCheckedU32(a, b uint32) (uint32, bool) {
	c := a + b
	if c < a {
		return 0, false
	}
	return c, true
}

// sequenceOf returns one cached entry's position span.
func (c *sequenceCache) sequenceOf(slot seqCacheSlot) []uint32 {
	start := int(slot.offset)
	length := int(slot.lengthPlusOne) - 1
	return c.positions[start : start+length]
}

// sequenceValue returns the cached membership id for one exact position
// sequence (Rust SequenceCache::sequence_value): the hash is a 4096-
// checkpoint fold over the sequence, and a match requires the exact
// stored sequence.
func (c *sequenceCache) sequenceValue(positions []uint32, check checkpoint) (uint32, bool, error) {
	if len(c.slots) == 0 {
		return 0, false, nil
	}
	hash, err := hashSequence(positions, check)
	if err != nil {
		return 0, false, err
	}
	index := hash & c.mask
	for range c.slots {
		slot := c.slots[index]
		if slot.lengthPlusOne == 0 {
			return 0, false, nil
		}
		if slot.hash == hash {
			equal, err := sequencesEqual(c.sequenceOf(slot), positions, check)
			if err != nil {
				return 0, false, err
			}
			if equal {
				return slot.membershipID, true, nil
			}
		}
		index = (index + 1) & c.mask
	}
	return 0, false, nil
}

// insertSequence stores one position sequence with its membership id
// (Rust SequenceCache::insert_sequence), dropping it silently when the
// cache is full.
func (c *sequenceCache) insertSequence(positions []uint32, value uint32, check checkpoint) error {
	if value == 0 {
		return corrupt("membership cache value is zero")
	}
	if len(c.slots) == 0 || c.entries == c.entryLimit ||
		len(positions) > int(c.positionCap)-len(c.positions) {
		return nil
	}
	hash, err := hashSequence(positions, check)
	if err != nil {
		return err
	}
	length := uint32(len(positions))
	offset := uint32(len(c.positions))
	index := hash & c.mask
	for range c.slots {
		if c.slots[index].lengthPlusOne == 0 {
			c.positions = append(c.positions, positions...)
			c.slots[index] = seqCacheSlot{
				hash:          hash,
				membershipID:  value,
				offset:        offset,
				lengthPlusOne: length + 1,
			}
			c.entries++
			return nil
		}
		index = (index + 1) & c.mask
	}
	return nil
}

// hashSequence folds one position sequence into the cache hash with the
// 4096-unit checkpoint cadence (Rust cache.rs hash_sequence).
func hashSequence(positions []uint32, check checkpoint) (uint64, error) {
	hash := mix(uint64(len(positions)))
	for work, position := range positions {
		if err := checkEvery(work, check); err != nil {
			return 0, err
		}
		hash = mix(hash ^ uint64(position))
	}
	return hash, nil
}

// sequencesEqual compares two position sequences exactly with the
// 4096-unit checkpoint cadence (Rust cache.rs sequences_equal).
func sequencesEqual(left, right []uint32, check checkpoint) (bool, error) {
	if len(left) != len(right) {
		return false, nil
	}
	for work := range left {
		if err := checkEvery(work, check); err != nil {
			return false, err
		}
		if left[work] != right[work] {
			return false, nil
		}
	}
	return true, nil
}
