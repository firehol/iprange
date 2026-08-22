// Membership dictionary write state and interning (Rust
// membership_dictionary.rs): the ID namespace (used bitmap, id limit,
// entry count), the id/hash tree pair, word interning with SHA-256
// deduplication, and live refcount maintenance through the bounded
// reference batch.

package writer

import (
	"encoding/binary"
	"slices"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// membershipWords is one bounded membership bitmap source (Rust
// Words<S>). Implementations return each up-to-64-word chunk by value: a
// source never returns a mapped page view, so a dictionary read can never
// retain or alias page bytes, and the Go generic constraint method can
// never retain a caller stack buffer (a slice argument through the
// generic dispatch would escape). The type parameter is instantiated per
// concrete source (OutputWords, the combine operand, the history prefix),
// mirroring the Rust generic trait.
type membershipWords interface {
	WordCount() uint32
	// ReadChunk returns the words starting at start as a copy of at
	// most membershipChunkWords (Rust read_words bounded by
	// HASH_WORDS); count is the number of valid words.
	ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error)
}

// OutputWords is one membership bitmap source (Rust MembershipWords): the
// caller's own words in canonical order. The concrete type keeps every
// writer-side membership read on caller-owned words.
type OutputWords []uint64

// WordCount returns the canonical bitmap word count (Rust word_count).
func (w OutputWords) WordCount() uint32 { return uint32(len(w)) }

// ReadWords copies the sequential words starting at start into output
// (Rust read_words: bounded by len(output); the caller's chunked buffers
// are stack arrays or caller-owned slices).
func (w OutputWords) ReadWords(start uint32, output []uint64) error {
	startIndex := int(start)
	end := startIndex + len(output)
	if startIndex < 0 || end > len(w) {
		return corrupt("membership words are outside the source bounds")
	}
	copy(output, w[startIndex:end])
	return nil
}

// ReadChunk returns the words starting at start by value (the generic
// chunk read; Rust read_words with a HASH_WORDS chunk).
func (w OutputWords) ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error) {
	startIndex := int(start)
	if startIndex < 0 || startIndex > len(w) {
		return words, 0, corrupt("membership words are outside the source bounds")
	}
	count = membershipChunkWords
	if remaining := len(w) - startIndex; int(count) > remaining {
		count = uint32(remaining)
	}
	copy(words[:count], w[startIndex:startIndex+int(count)])
	return words, count, nil
}

// membershipState is the writable dictionary state (Rust State). The
// record and hash scratches are bounded encode targets owned by the
// draft store or output builder (never per record): a draft is
// single-threaded, every tree insert copies its record into the mapped
// page before the next encode reuses the scratch, and the slices are
// views over the owner's encode arrays, so writer allocations never
// scale with records.
type membershipState struct {
	idRoot        uint32
	hashRoot      uint32
	usedRoot      uint32
	entryCount    uint64
	idLimit       uint64
	recordScratch []byte
	hashScratch   []byte
}

// membershipInterned is the outcome of one intern (Rust Interned).
type membershipInterned struct {
	id        uint32
	wordCount uint32
	created   bool
}

// internMembership returns the dictionary ID for one membership bitmap,
// creating the record when the bitmap is new (Rust
// membership_dictionary::intern).
func internMembership[W membershipWords](store tree.RetiringStore, state *membershipState, words W) (membershipInterned, error) {
	work.MembershipIntern(1)
	wordCount := words.WordCount()
	if wordCount == 0 || wordCount > membershipMaxWordCount {
		return membershipInterned{}, invalid("membership word count is outside the v4 limit")
	}
	digest, err := digestWords(words)
	if err != nil {
		return membershipInterned{}, err
	}
	if id, found, err := findEqualMembership(store, state, words, digest); err != nil {
		return membershipInterned{}, err
	} else if found {
		return membershipInterned{id: id, wordCount: wordCount, created: false}, nil
	}
	return insertNewMembership(store, state, words, digest)
}

// insertNewMembership allocates the lowest free ID and inserts both tree
// records (Rust insert_new).
func insertNewMembership[W membershipWords](store tree.RetiringStore, state *membershipState, words W, digest [32]byte) (membershipInterned, error) {
	id, err := bitmap.AllocateLowestID(store, &state.usedRoot, &state.idLimit, state.entryCount,
		bitmap.KindMembership, membershipIDExhausted)
	if err != nil {
		return membershipInterned{}, err
	}
	record, err := encodeMembershipRecord(store, words, id, digest, state.recordScratch)
	if err != nil {
		return membershipInterned{}, err
	}
	if err := insertMembershipRecord(store, idCodec{}, &state.idRoot, record.Slice()); err != nil {
		return membershipInterned{}, err
	}
	writeHashKey(state.hashScratch, digest, words.WordCount(), id)
	if err := insertMembershipRecord(store, hashCodec{}, &state.hashRoot, state.hashScratch); err != nil {
		return membershipInterned{}, err
	}
	state.entryCount++
	return membershipInterned{id: id, wordCount: words.WordCount(), created: true}, nil
}

func membershipIDExhausted() error {
	return &format.Error{Code: format.CodeMembershipIdExhausted, Detail: "membership ID namespace exhausted"}
}

// findEqualMembership searches the hash tree for an equal bitmap (Rust
// find_equal): at_or_after over (digest, word_count, id), word-for-word
// comparison against the candidate record.
func findEqualMembership[W membershipWords](store tree.Store, state *membershipState, words W, digest [32]byte) (uint32, bool, error) {
	// Rust find_equal does not count membership_lookup; only record::find
	// and apply_delta do (membership_dictionary.rs + record.rs).
	if state.hashRoot == 0 {
		return 0, false, nil
	}
	key := hashProbe(digest, words.WordCount(), 1)
	for {
		value, ok, err := tree.AtOrAfter(hashCodec{}, store, state.hashRoot, tree.RawKey(key[:]))
		if err != nil {
			return 0, false, err
		}
		if !ok {
			return 0, false, nil
		}
		candidate := value
		if candidate.digest != digest || candidate.wordCount != words.WordCount() {
			return 0, false, nil
		}
		equal, err := equalMembershipWords(store, state.idRoot, candidate.id, words)
		if err != nil {
			return 0, false, err
		}
		if equal {
			return candidate.id, true, nil
		}
		if candidate.id == ^uint32(0) {
			return 0, false, nil
		}
		key = hashProbe(digest, words.WordCount(), candidate.id+1)
	}
}

// equalMembershipWords compares one stored record's bitmap with the
// source words in 64-word chunks (Rust equal_words): one located find,
// then chunked reads that reuse the located record.
func equalMembershipWords[W membershipWords](store tree.Store, idRoot uint32, id uint32, words W) (bool, error) {
	found, err := findMembership(store, idRoot, id)
	if err != nil {
		return false, err
	}
	if !found.located {
		return false, corrupt("membership hash points to a missing ID")
	}
	if found.record.wordCount != words.WordCount() {
		return false, nil
	}
	var actual [64]uint64
	var start uint32
	for start < found.record.wordCount {
		count := found.record.wordCount - start
		if count > 64 {
			count = 64
		}
		actualSlice := actual[:count]
		if err := readFoundMembershipWords(store, found, start, actualSlice); err != nil {
			return false, err
		}
		wanted, got, err := words.ReadChunk(start)
		if err != nil {
			return false, err
		}
		if got < count {
			return false, corrupt("membership words are outside the source bounds")
		}
		if !slices.Equal(actualSlice, wanted[:count]) {
			return false, nil
		}
		start += count
	}
	return true, nil
}

// encodeMembershipRecord builds the ID-tree record for one bitmap:
// inline when it fits the record limit, otherwise a blob tree, written
// into the caller's scratch (Rust record::encode; the scratch is the
// draft state's bounded record buffer).
func encodeMembershipRecord[W membershipWords](store tree.Store, words W, id uint32, digest [32]byte, scratch []byte) (membershipEncoded, error) {
	var blobRoot uint32
	if words.WordCount() > membershipInlineWords {
		root, err := buildMembershipBlob(store, words)
		if err != nil {
			return membershipEncoded{}, err
		}
		blobRoot = root
	}
	encoded, err := newMembershipEncoded(id, words.WordCount(), digest, blobRoot, scratch)
	if err != nil {
		return membershipEncoded{}, err
	}
	if blobRoot == 0 {
		// Read the words in bounded batches directly into the record
		// (Rust encode_inline WORD_BATCH=32).
		const batch = 32
		var start uint32
		for start < words.WordCount() {
			chunk, got, err := words.ReadChunk(start)
			if err != nil {
				return membershipEncoded{}, err
			}
			count := words.WordCount() - start
			if count > batch {
				count = batch
			}
			if got < count {
				return membershipEncoded{}, corrupt("membership words are outside the source bounds")
			}
			for offset, word := range chunk[:count] {
				if err := encoded.putInlineWord(int(start)+offset, word); err != nil {
					return membershipEncoded{}, err
				}
			}
			start += count
		}
	}
	return encoded, nil
}

// insertMembershipRecord inserts one record into one dictionary tree
// with its own codec, retiring the COW pages (Rust mutate_insert, which
// is generic over the tree codec: the ID tree and the hash tree each use
// their own).
func insertMembershipRecord[T any](store tree.RetiringStore, codec tree.Codec[T], root *uint32, record []byte) error {
	retired, changed, err := tree.Insert(codec, store, root, record, tree.RetiredPages{})
	if err != nil {
		return err
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	if !changed {
		return corrupt("membership dictionary key already exists")
	}
	return nil
}

// membershipFound is one located ID-tree record (Rust record::Found):
// the decoded record plus the leaf location, so repeated word reads reuse
// the located cell instead of re-descending the tree. The location is a
// value: a pointer to the returned local would escape to the heap on
// every dictionary lookup.
type membershipFound struct {
	record   membershipRecord
	location tree.LeafLocation
	located  bool
}

// findMembership locates one ID-tree record (Rust record::find). A
// missing record reports located=false without an error; each caller
// attaches its own missing-record corrupt detail, mirroring the Rust
// callers that map find's Option to their Corrupt strings.
func findMembership(store tree.Store, root uint32, id uint32) (membershipFound, error) {
	work.MembershipLookup(1)
	if id == 0 || root == 0 {
		return membershipFound{}, nil
	}
	value, location, ok, err := tree.PredecessorLocated(idCodec{}, store, root, tree.Key{Hi: uint64(id)})
	if err != nil {
		return membershipFound{}, err
	}
	if !ok {
		return membershipFound{}, nil
	}
	if value.id != id {
		return membershipFound{}, nil
	}
	return membershipFound{record: value, location: location, located: true}, nil
}

// readMembershipWords reads sequential words of one stored membership
// record (Rust read_words: find once, then the located read).
func readMembershipWords(store tree.Store, idRoot uint32, id uint32, start uint32, output []uint64) error {
	found, err := findMembership(store, idRoot, id)
	if err != nil {
		return err
	}
	if !found.located {
		return corrupt("membership ID is missing")
	}
	return readFoundMembershipWords(store, found, start, output)
}

// readFoundMembershipWords reads sequential words from one located record
// (Rust record::read_record_words): the inline bitmap is re-verified
// through the located cell with one inspect_leaf; blob bitmaps read
// through the blob tree. No dictionary lookup is counted per chunk.
func readFoundMembershipWords(store tree.Store, found membershipFound, start uint32, output []uint64) error {
	end := uint64(start) + uint64(len(output))
	if end > uint64(found.record.wordCount) {
		return corrupt("membership word range exceeds its bitmap")
	}
	for index := range output {
		output[index] = 0
	}
	switch found.record.storage {
	case membershipStorageInline:
		location := found.location
		return tree.InspectLeaf(idCodec{}, store, location.PageNumber, location.Header.ItemCount, location.Index, func(cell []byte) error {
			current, err := decodeMembershipRecord(cell)
			if err != nil {
				return err
			}
			if current.id != found.record.id || current.wordCount != found.record.wordCount ||
				current.storage != membershipStorageInline {
				return corrupt("membership record changed during read")
			}
			for index := range output {
				at := membershipIDBase + int(start+uint32(index))*8
				output[index] = u64LE(cell[at : at+8])
			}
			return nil
		})
	case membershipStorageBlob:
		return readMembershipBlobWords(store, found.record.blobRoot, found.record.wordCount, start, output)
	default:
		return corrupt("membership dictionary storage is malformed")
	}
}

// applyMembershipDelta applies one refcount change to one membership
// record, removing the record (and its blob, hash entry, and used bit)
// when the refcount reaches zero (Rust apply_delta +
// finish_record_removal).
func applyMembershipDelta(store tree.RetiringStore, state *membershipState, id uint32, change int64) error {
	work.MembershipLookup(1)
	var nextRefcount uint64
	retired, value, err := tree.MutateLeafU64(idCodec{}, store, &state.idRoot, tree.Key{Hi: uint64(id)},
		membershipRefcountOffset, tree.RetiredPages{}, func(record membershipRecord) (tree.LeafU64Mutation, error) {
			next, err := changedRefcount(record.refcount, change)
			if err != nil {
				return tree.LeafU64Mutation{}, err
			}
			nextRefcount = next
			if next == 0 {
				return tree.LeafU64Mutation{DoReplace: false}, nil
			}
			return tree.LeafU64Mutation{DoReplace: true, Replace: next}, nil
		})
	if err != nil {
		return err
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	if nextRefcount != 0 {
		return nil
	}
	return finishMembershipRemoval(store, state, value)
}

func changedRefcount(current uint64, change int64) (uint64, error) {
	if change >= 0 {
		next := current + uint64(change)
		if next < current {
			return 0, overflow("membership refcount")
		}
		return next, nil
	}
	amount := uint64(-change)
	if amount > current {
		return 0, overflow("membership refcount")
	}
	return current - amount, nil
}

// finishMembershipRemoval deletes the hash entry, releases the blob,
// clears the used bit, and shrinks the ID limit (Rust
// finish_record_removal).
func finishMembershipRemoval(store tree.RetiringStore, state *membershipState, record membershipRecord) error {
	probe := hashProbe(record.digest, record.wordCount, record.id)
	retired, err := tree.DeleteExisting(hashCodec{}, store, &state.hashRoot, tree.RawKey(probe[:]), tree.RetiredPages{})
	if err != nil {
		return err
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	if record.storage == membershipStorageBlob {
		if err := releaseMembershipBlob(store, record.blobRoot, record.wordCount); err != nil {
			return err
		}
	}
	cleared, err := bitmap.ClearUsed(store, &state.usedRoot, state.idLimit, bitmap.KindMembership, record.id, &retired)
	if err != nil {
		return err
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	if !cleared {
		return corrupt("membership used bit is missing")
	}
	if state.entryCount == 0 {
		return corrupt("membership entry count underflow")
	}
	state.entryCount--
	limit, err := bitmap.ShrinkMembership(store, &state.usedRoot, state.idLimit)
	if err != nil {
		return err
	}
	state.idLimit = limit
	return nil
}

// membershipReferenceBatch is the fixed-memory recurring-reference table
// (Rust immutable_output/reference_batch.rs): a power-of-two slot table
// (<=1024 entries) with linear probing; recurring ids accumulate a count
// and are applied as one delta on flush. When the batch is disabled the
// caller applies every reference directly.
type membershipReferenceBatch struct {
	slots   []referenceSlot
	entries int
	enabled bool
}

type referenceSlot struct {
	id    uint32
	count int64
}

// newMembershipReferenceBatch builds the batch with the given entry
// capacity (a power of two).
func newMembershipReferenceBatch(capacity int) membershipReferenceBatch {
	batch := membershipReferenceBatch{enabled: capacity > 0}
	if batch.enabled {
		batch.slots = make([]referenceSlot, capacity*2)
	}
	return batch
}

// ReferenceBatchEntryLimit is the Rust reference-batch entry cap
// (immutable_output/reference_batch.rs ENTRY_LIMIT: up to 1024
// recurring-reference entries, two 16-byte slots each).
const ReferenceBatchEntryLimit = 1024

// addReference records one reference to id (Rust add). The outcome
// selects the caller's action: added, direct, or full.
type referenceAdd uint8

const (
	referenceAdded referenceAdd = iota
	referenceDirect
	referenceFull
)

func (b *membershipReferenceBatch) addReference(id uint32) (referenceAdd, error) {
	if id == 0 {
		return 0, corrupt("dictionary reference ID is zero")
	}
	if !b.enabled {
		return referenceDirect, nil
	}
	entryLimit := len(b.slots) / 2
	index := int(id*0x9e3779b1) & (len(b.slots) - 1)
	for probe := 0; probe < len(b.slots); probe++ {
		slot := &b.slots[index]
		if slot.id == id {
			slot.count++
			return referenceAdded, nil
		}
		if slot.id == 0 {
			if b.entries == entryLimit {
				return referenceFull, nil
			}
			*slot = referenceSlot{id: id, count: 1}
			b.entries++
			return referenceAdded, nil
		}
		index = (index + 1) & (len(b.slots) - 1)
	}
	return referenceFull, nil
}

// takeReference removes one slot's delta (Rust take).
func (b *membershipReferenceBatch) takeReference(index int) (id uint32, count int64, ok bool) {
	slot := b.slots[index]
	if slot.id == 0 {
		return 0, 0, false
	}
	b.slots[index] = referenceSlot{}
	return slot.id, slot.count, true
}

// finishFlush empties the entry count (Rust finish_flush).
func (b *membershipReferenceBatch) finishFlush() { b.entries = 0 }

// isEmpty reports no pending entries (Rust is_empty).
func (b *membershipReferenceBatch) isEmpty() bool { return b.entries == 0 }

// capacity reports the slot table length (Rust len).
func (b *membershipReferenceBatch) capacity() int { return len(b.slots) }
func u64LE(b []byte) uint64                       { return binary.LittleEndian.Uint64(b) }

// addedBit is one membership bitmap source with a single extra feed bit
// set (Rust membership_dictionary::AddedBit): the base bitmap words
// followed by one bit at the feed index. The source is read directly
// from the dictionary, so no owned copy of the base words is ever
// materialized.
type addedBit struct {
	store     tree.Store
	idRoot    uint32
	baseID    uint32
	baseWords uint32
	bit       uint32
}

// WordCount returns the canonical bitmap length with the added bit (Rust
// AddedBit::word_count).
func (a *addedBit) WordCount() uint32 {
	if a.baseWords > a.bit/64+1 {
		return a.baseWords
	}
	return a.bit/64 + 1
}

// ReadChunk returns the words starting at start by value (Rust
// AddedBit::read_words): the base words are copied first, then the bit
// is folded in; words outside the base are zero.
func (a *addedBit) ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error) {
	count = membershipChunkWords
	if remaining := a.WordCount() - start; count > remaining {
		count = remaining
	}
	if err := readMembershipOperand(a.store, a.idRoot, a.baseID, a.baseWords, start, words[:count]); err != nil {
		return words, 0, err
	}
	word := a.bit / 64
	if word >= start && word-start < count {
		words[word-start] |= 1 << (a.bit % 64)
	}
	return words, count, nil
}

// internAddedBit returns the dictionary ID for one base bitmap plus one
// feed bit, creating the record when the bitmap is new (Rust
// membership_dictionary::intern_added_bit). A base bitmap that already
// contains the bit returns the base record unchanged.
func internAddedBit(store tree.RetiringStore, state *membershipState, baseID, baseWords, bit uint32) (membershipInterned, error) {
	work.MembershipIntern(1)
	if baseID != 0 {
		found, err := findMembership(store, state.idRoot, baseID)
		if err != nil {
			return membershipInterned{}, err
		}
		if !found.located {
			return membershipInterned{}, corrupt("membership reference ID is missing")
		}
		if found.record.wordCount != baseWords {
			return membershipInterned{}, corrupt("membership reference length changed")
		}
		wordIndex := bit / 64
		if wordIndex < baseWords {
			var word [1]uint64
			if err := readFoundMembershipWords(store, found, wordIndex, word[:]); err != nil {
				return membershipInterned{}, err
			}
			if word[0]&(1<<(bit%64)) != 0 {
				return membershipInterned{id: baseID, wordCount: baseWords}, nil
			}
		}
	}
	source := &addedBit{
		store:     store,
		idRoot:    state.idRoot,
		baseID:    baseID,
		baseWords: baseWords,
		bit:       bit,
	}
	return internMembership(store, state, source)
}
