// Membership bitmap algebra over interned memberships (Rust
// membership_dictionary/algebra.rs + contains_indexes): the operation
// enum, the combine of two stored bitmaps with canonical word counts and
// identity shortcuts, and the selected-word presence probe over one
// stored bitmap. Every operand read targets caller-owned word buffers;
// no mapped page view ever reaches the dictionary read.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// MembershipOperation is one per-address membership operation (Rust
// contract::MembershipOperation).
type MembershipOperation uint8

const (
	MembershipReplace MembershipOperation = iota
	MembershipUnion
	MembershipDifference
	MembershipIntersection
	MembershipXor
)

// combinedWords is the on-the-fly combination of two stored membership
// bitmaps (Rust Combined): the left operand is read directly, the right
// operand is read in HASH_WORDS-sized chunks and folded per word, so the
// combination never materializes a bitmap in owned memory.
type combinedWords struct {
	store      tree.Store
	idRoot     uint32
	leftID     uint32
	leftWords  uint32
	rightID    uint32
	rightWords uint32
	operation  MembershipOperation
	wordCount  uint32
}

func (c *combinedWords) WordCount() uint32 { return c.wordCount }

func (c *combinedWords) ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error) {
	count = membershipChunkWords
	if remaining := c.wordCount - start; count > remaining {
		count = remaining
	}
	if err := readMembershipOperand(c.store, c.idRoot, c.leftID, c.leftWords, start, words[:]); err != nil {
		return words, 0, err
	}
	var right [membershipChunkWords]uint64
	if err := readMembershipOperand(c.store, c.idRoot, c.rightID, c.rightWords, start, right[:]); err != nil {
		return words, 0, err
	}
	applyMembershipWords(words[:], right[:], c.operation)
	return words, count, nil
}

// combineMembership returns the dictionary ID for the combination of two
// stored bitmaps, creating the record when the result is new (Rust
// membership_dictionary::combine): identity shortcuts first, then the
// canonical trailing-word count, then the intern.
func combineMembership(store tree.RetiringStore, state *membershipState, scratch *combinedWords, leftID, rightID, rightWords uint32, operation MembershipOperation) (membershipInterned, error) {
	work.MembershipCombination(1)
	leftWords, err := storedMembershipWordCount(store, state.idRoot, leftID)
	if err != nil {
		return membershipInterned{}, err
	}
	if err := requireMembershipWords(store, state.idRoot, rightID, rightWords); err != nil {
		return membershipInterned{}, err
	}
	if result, ok := membershipIdentity(leftID, leftWords, rightID, rightWords, operation); ok {
		return result, nil
	}
	*scratch = combinedWords{
		store:      store,
		idRoot:     state.idRoot,
		leftID:     leftID,
		leftWords:  leftWords,
		rightID:    rightID,
		rightWords: rightWords,
		operation:  operation,
		wordCount:  rawMembershipWordCount(leftWords, rightWords, operation),
	}
	source := scratch
	source.wordCount, err = canonicalMembershipCount(store, source)
	if err != nil {
		return membershipInterned{}, err
	}
	if source.wordCount == 0 {
		return membershipInterned{wordCount: 0}, nil
	}
	return internMembership(store, state, source)
}

// storedMembershipWordCount returns the stored bitmap length of one ID
// (Rust stored_word_count: id 0 is the empty bitmap).
func storedMembershipWordCount(store tree.Store, root uint32, id uint32) (uint32, error) {
	if id == 0 {
		return 0, nil
	}
	found, err := findMembership(store, root, id)
	if err != nil {
		return 0, err
	}
	if !found.located {
		return 0, corrupt("range membership ID is missing")
	}
	return found.record.wordCount, nil
}

// requireMembershipWords proves one operand still has its advertised
// length (Rust require_words: StaleReference when the stored length moved
// or a nonzero empty ID is supplied).
func requireMembershipWords(store tree.Store, root uint32, id uint32, expected uint32) error {
	stored, err := storedMembershipWordCount(store, root, id)
	if err != nil {
		return err
	}
	if stored == expected && (id != 0 || expected == 0) {
		return nil
	}
	return &format.Error{Code: format.CodeStaleReference, Detail: "operation reference is stale"}
}

// membershipIdentity returns the shortcut result when one combine operand
// already determines the outcome (Rust identity: the selected operand,
// the union/difference/intersection/xor identities, or none).
func membershipIdentity(leftID, leftWords, rightID, rightWords uint32, operation MembershipOperation) (membershipInterned, bool) {
	left := [2]uint32{leftID, leftWords}
	right := [2]uint32{rightID, rightWords}
	selected, ok := selectedMembershipIdentity(left, right, operation)
	if !ok {
		return membershipInterned{}, false
	}
	return membershipInterned{id: selected[0], wordCount: selected[1], created: false}, true
}

func selectedMembershipIdentity(left, right [2]uint32, operation MembershipOperation) ([2]uint32, bool) {
	switch operation {
	case MembershipReplace:
		return right, true
	case MembershipUnion:
		return unionMembershipIdentity(left, right)
	case MembershipDifference:
		return differenceMembershipIdentity(left, right)
	case MembershipIntersection:
		return intersectionMembershipIdentity(left, right)
	case MembershipXor:
		return xorMembershipIdentity(left, right)
	default:
		return [2]uint32{}, false
	}
}

func unionMembershipIdentity(left, right [2]uint32) ([2]uint32, bool) {
	switch {
	case left[0] == 0:
		return right, true
	case right[0] == 0:
		return left, true
	case left[0] == right[0]:
		return left, true
	default:
		return [2]uint32{}, false
	}
}

func differenceMembershipIdentity(left, right [2]uint32) ([2]uint32, bool) {
	switch {
	case left[0] == 0:
		return [2]uint32{}, true
	case left[0] == right[0]:
		return [2]uint32{}, true
	case right[0] == 0:
		return left, true
	default:
		return [2]uint32{}, false
	}
}

func intersectionMembershipIdentity(left, right [2]uint32) ([2]uint32, bool) {
	switch {
	case left[0] == 0 || right[0] == 0:
		return [2]uint32{}, true
	case left[0] == right[0]:
		return left, true
	default:
		return [2]uint32{}, false
	}
}

func xorMembershipIdentity(left, right [2]uint32) ([2]uint32, bool) {
	switch {
	case left[0] == 0:
		return right, true
	case right[0] == 0:
		return left, true
	case left[0] == right[0]:
		return [2]uint32{}, true
	default:
		return [2]uint32{}, false
	}
}

// rawMembershipWordCount is the uncanonicalized combination length (Rust
// raw_word_count).
func rawMembershipWordCount(left, right uint32, operation MembershipOperation) uint32 {
	switch operation {
	case MembershipReplace:
		return right
	case MembershipUnion, MembershipXor:
		return maxU32(left, right)
	case MembershipDifference:
		return left
	case MembershipIntersection:
		return minU32(left, right)
	default:
		return 0
	}
}

// canonicalMembershipCount trims the trailing zero words of one combined
// source (Rust canonical_count: reads the tail in HASH_WORDS chunks and
// finds the last nonzero word).
func canonicalMembershipCount[W membershipWords](store tree.Store, source W) (uint32, error) {
	end := source.WordCount()
	for end != 0 {
		start := end - minU32(end, membershipChunkWords)
		count := end - start
		chunk, got, err := source.ReadChunk(start)
		if err != nil {
			return 0, err
		}
		if got < count {
			return 0, corrupt("membership words are outside the source bounds")
		}
		for index := int(count) - 1; index >= 0; index-- {
			if chunk[index] != 0 {
				return start + uint32(index) + 1, nil
			}
		}
		end = start
	}
	return 0, nil
}

// readMembershipOperand reads one stored bitmap's sequential words into
// the caller's buffer (Rust read_operand: zero fill, then the available
// span through one located record read). The empty ID reads as all zeros.
func readMembershipOperand(store tree.Store, idRoot, id, wordCount, start uint32, output []uint64) error {
	for index := range output {
		output[index] = 0
	}
	if id == 0 || start >= wordCount {
		return nil
	}
	count := uint32(len(output))
	if remaining := wordCount - start; count > remaining {
		count = remaining
	}
	found, err := findMembership(store, idRoot, id)
	if err != nil {
		return err
	}
	if !found.located {
		return corrupt("membership ID is missing")
	}
	return readFoundMembershipWords(store, found, start, output[:count])
}

// applyMembershipWords folds one right operand word into the left buffer
// per operation (Rust apply_words).
func applyMembershipWords(left []uint64, right []uint64, operation MembershipOperation) {
	for index := range left {
		switch operation {
		case MembershipReplace:
			left[index] = right[index]
		case MembershipUnion:
			left[index] |= right[index]
		case MembershipDifference:
			left[index] &^= right[index]
		case MembershipIntersection:
			left[index] &= right[index]
		case MembershipXor:
			left[index] ^= right[index]
		}
	}
}

// containsMembershipIndexes probes one stored bitmap at selected feed
// indexes (Rust contains_indexes): canonical increasing selection, one
// located record, one cached word between consecutive probes, and the
// 4096-unit cancellation cadence.
func containsMembershipIndexes(store tree.Store, idRoot uint32, id uint32, indexes []uint32, output []uint8, check func() error) error {
	if len(indexes) != len(output) {
		return invalid("membership index selection is not canonical")
	}
	for work := 0; work+1 < len(indexes); work++ {
		if err := checkEvery(work, check); err != nil {
			return err
		}
		if indexes[work] >= indexes[work+1] {
			return invalid("membership index selection is not canonical")
		}
	}
	for index := range output {
		output[index] = 0
	}
	if id == 0 || len(indexes) == 0 {
		return nil
	}
	found, err := findMembership(store, idRoot, id)
	if err != nil {
		return err
	}
	if !found.located {
		return corrupt("membership ID is missing")
	}
	var cachedWord uint64
	cachedIndex := ^uint32(0)
	for work, index := range indexes {
		if err := checkEvery(work, check); err != nil {
			return err
		}
		wordIndex := index / 64
		if wordIndex >= found.record.wordCount {
			break
		}
		word := cachedWord
		if cachedIndex != wordIndex {
			var value [1]uint64
			if err := readFoundMembershipWords(store, found, wordIndex, value[:]); err != nil {
				return err
			}
			word = value[0]
			cachedWord = word
			cachedIndex = wordIndex
		}
		if word&(uint64(1)<<(index%64)) != 0 {
			output[work] = 1
		}
	}
	return nil
}

// checkpoint runs one cancellation checkpoint; a nil checkpoint never
// cancels (Rust CancellationToken::check; the writer passes the public
// cancellation function, internal callers pass nil).
func checkpoint(check func() error) error {
	if check == nil {
		return nil
	}
	return check()
}

// integer is the set of integer counters used by checkpoint cadences.
type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// checkEvery runs one cancellation checkpoint every 4096 work items (Rust
// CancellationToken::check cadence); a nil check never cancels.
func checkEvery[T integer](work T, check func() error) error {
	if uint64(work)&4095 != 4095 || check == nil {
		return nil
	}
	return check()
}

func minU32(left, right uint32) uint32 {
	if left < right {
		return left
	}
	return right
}

func maxU32(left, right uint32) uint32 {
	if left > right {
		return left
	}
	return right
}
