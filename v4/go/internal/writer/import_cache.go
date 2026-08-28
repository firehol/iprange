// Mapped operation-private state for name-based membership import
// (Rust draft_store/import_cache.rs): the import cache keeps three
// fixed-tree namespaces in one private tree - the source-feed index
// map, the source-membership translations, and the translated-membership
// set - plus one last-translation slot for the sorted range sweep, and
// the import words are the same tree viewed as a bitmap word source.
// The cache lives only for the import operation: release discards every
// private page back to the draft allocator.

package writer

import (
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

const (
	importCacheBranch = format.PageType(240)
	importCacheLeaf   = format.PageType(241)
	importCacheAux    = 0x494d5043 // "IMPC" little-endian

	importCacheFeedKey       = 1
	importCacheMembershipKey = 2
	importCacheTranslatedKey = 3

	importCacheEntryKeyOffset   = 0
	importCacheEntryValueOffset = 8
	importCacheEntryWordsOffset = 12
	importCacheEntrySize        = 16
)

// importCacheEntry is one 16-byte cache record (Rust import_cache
// Entry): the namespaced u64 key plus the u32 value and word count.
type importCacheEntry struct {
	key   uint64
	value uint32
	words uint32
}

func splitImportCacheEntry(key, value uint64) importCacheEntry {
	return importCacheEntry{key: key, value: uint32(value), words: uint32(value >> 32)}
}

func (e importCacheEntry) joined() uint64 {
	return uint64(e.value) | (uint64(e.words) << 32)
}

func (e importCacheEntry) encode() [importCacheEntrySize]byte {
	var output [importCacheEntrySize]byte
	format.PutU64(output[importCacheEntryKeyOffset:], e.key)
	format.PutU32(output[importCacheEntryValueOffset:], e.value)
	format.PutU32(output[importCacheEntryWordsOffset:], e.words)
	return output
}

func decodeImportCacheEntry(input []byte) (importCacheEntry, error) {
	if len(input) != importCacheEntrySize {
		return importCacheEntry{}, corrupt("import cache record length is invalid")
	}
	return importCacheEntry{
		key:   format.U64(input[importCacheEntryKeyOffset:]),
		value: format.U32(input[importCacheEntryValueOffset:]),
		words: format.U32(input[importCacheEntryWordsOffset:]),
	}, nil
}

// importCacheCodec is the fixed-tree codec of the import cache (Rust
// CacheCodec: u64 keys, 16-byte leaf records, private page types).
type importCacheCodec struct{}

func (importCacheCodec) BranchType() format.PageType { return importCacheBranch }
func (importCacheCodec) LeafType() format.PageType   { return importCacheLeaf }
func (importCacheCodec) Aux() uint32                 { return importCacheAux }
func (importCacheCodec) KeySize() int                { return importCacheEntryValueOffset }
func (importCacheCodec) LeafSize() int               { return importCacheEntrySize }

func (importCacheCodec) ReadKey(cell []byte, _ uint16) (tree.Key, error) {
	if len(cell) < importCacheEntryValueOffset {
		return tree.Key{}, corrupt("import cache key truncated")
	}
	return tree.KeyOfU64(format.U64(cell[importCacheEntryKeyOffset:])), nil
}

// PrefixKeyProbe opts the codec into the inline prefix probe: fixed
// cells carry the little-endian u64 key as their prefix.
func (importCacheCodec) PrefixKeyProbe() {}

// CompareKey compares one cell key without materializing a Key (Rust
// u64 Ord; never called on the hot path, which uses the prefix probe).
func (importCacheCodec) CompareKey(cell []byte, _ uint16, target tree.Key) (int, error) {
	if len(cell) < importCacheEntryValueOffset {
		return 0, corrupt("import cache key truncated")
	}
	return cmpU64(format.U64(cell[importCacheEntryKeyOffset:]), target.U64()), nil
}

func (importCacheCodec) ReadLeaf(cell []byte) (importCacheEntry, error) {
	return decodeImportCacheEntry(cell)
}

func (importCacheCodec) WriteKey(key tree.Key, output []byte) {
	format.PutU64(output, key.U64())
}

// ImportCache is one import operation's private translation state
// (Rust ImportCache). The root facade creates one cache per import and
// feeds it through the WriterEdit import arms; the cache methods the
// facade needs directly (the last-translation fast path and the
// terminal counts) are exported, everything else stays private.
type ImportCache struct {
	root                  uint32
	sourceMemberships     uint64
	translatedMemberships uint64
	lastSource            uint32
	lastTranslated        translatedMembership
	hasLast               bool
}

// NewImportCache starts one empty import cache (Rust
// ImportCache::new).
func NewImportCache() *ImportCache { return &ImportCache{} }

// mapFeed records one source-feed-index to destination-feed-index
// mapping (Rust ImportCache::map_feed).
func (c *ImportCache) mapFeed(store *DraftStore, source, destination FeedEntry) error {
	return insertNewImportCacheEntry(store, &c.root, importCacheEntry{
		key:   namespacedImportKey(importCacheFeedKey, source.Index),
		value: destination.Index,
	})
}

// membership returns the cached translation of one source membership,
// consulting the last-translation slot first (Rust
// ImportCache::membership).
func (c *ImportCache) membership(store *DraftStore, source uint32) (translatedMembership, bool, error) {
	if translated, ok := c.lastTranslation(source); ok {
		return translated, true, nil
	}
	entry, ok, err := lookupImportCacheEntry(store, c.root, namespacedImportKey(importCacheMembershipKey, source))
	if err != nil || !ok {
		return translatedMembership{}, false, err
	}
	translated := newTranslatedMembership(entry.value, entry.words)
	c.lastSource = source
	c.lastTranslated = translated
	c.hasLast = true
	return translated, true, nil
}

// LastTranslation returns the stored translation of the previous
// source membership when it repeats (Rust ImportCache::last_translation
// over Option<TranslatedMembership>; the sorted range sweep consults it
// before any tree lookup).
func (c *ImportCache) LastTranslation(source uint32) (id, words uint32, ok bool) {
	translated, ok := c.lastTranslation(source)
	if !ok {
		return 0, 0, false
	}
	return translated.id, translated.words, true
}

// lastTranslation returns the cached translation of the previous
// source membership when it repeats (Rust ImportCache::
// last_translation).
func (c *ImportCache) lastTranslation(source uint32) (translatedMembership, bool) {
	if !c.hasLast || c.lastSource != source {
		return translatedMembership{}, false
	}
	return c.lastTranslated, true
}

// finishMembership interns the translated words and records the
// translation plus the translated-membership set (Rust
// ImportCache::finish_membership).
func (c *ImportCache) finishMembership(store *DraftStore, source uint32, words *ImportWords, check func() error) (translatedMembership, error) {
	interned, err := words.internAndRelease(store, check)
	if err != nil {
		return translatedMembership{}, err
	}
	return c.recordMembership(store, source, interned)
}

// recordMembership stores one translation and accounts the translated
// set (Rust ImportCache::record_membership).
func (c *ImportCache) recordMembership(store *DraftStore, source uint32, destination membershipInterned) (translatedMembership, error) {
	translated := newTranslatedMembership(destination.id, destination.wordCount)
	if err := insertNewImportCacheEntry(store, &c.root, importCacheEntry{
		key:   namespacedImportKey(importCacheMembershipKey, source),
		value: destination.id,
		words: destination.wordCount,
	}); err != nil {
		return translatedMembership{}, err
	}
	var err error
	c.sourceMemberships, err = checkedImportIncrement(c.sourceMemberships, "source distinct membership count")
	if err != nil {
		return translatedMembership{}, err
	}
	c.lastSource = source
	c.lastTranslated = translated
	c.hasLast = true

	translatedKey := namespacedImportKey(importCacheTranslatedKey, destination.id)
	_, present, err := lookupImportCacheEntry(store, c.root, translatedKey)
	if err != nil {
		return translatedMembership{}, err
	}
	if !present {
		if err := insertNewImportCacheEntry(store, &c.root, importCacheEntry{key: translatedKey}); err != nil {
			return translatedMembership{}, err
		}
		c.translatedMemberships, err = checkedImportIncrement(c.translatedMemberships, "translated membership count")
		if err != nil {
			return translatedMembership{}, err
		}
	}
	return translated, nil
}

// mapWordBatch maps one batch of source bitmap words through the feed
// index map into the translation words (Rust
// ImportCache::map_word_batch); missing reports a source bitmap naming
// an inactive feed index.
func (c *ImportCache) mapWordBatch(store *DraftStore, words *ImportWords, start uint32, sourceWords []uint64, check func() error) (bool, error) {
	words.store = store
	for offset, sourceWord := range sourceWords {
		wordIndex, err := checkedImportAdd(start, uint32(offset), "source membership word index")
		if err != nil {
			return false, err
		}
		if missing, err := c.mapSourceWord(store, words, wordIndex, sourceWord, check); err != nil || missing {
			return missing, err
		}
	}
	return false, nil
}

// mapSourceWord maps one bitmap word through the feed index tree (Rust
// ImportCache::map_source_word): every set bit names one source feed,
// which must exist in the feed map.
func (c *ImportCache) mapSourceWord(store *DraftStore, words *ImportWords, wordIndex uint32, sourceWord uint64, check func() error) (bool, error) {
	base := uint64(wordIndex) * 64
	for sourceWord != 0 {
		if err := check(); err != nil {
			return false, err
		}
		bit := uint32(bits.TrailingZeros64(sourceWord))
		sourceIndex64 := base + uint64(bit)
		if sourceIndex64 > uint64(^uint32(0)) {
			return false, overflow("source feed index")
		}
		entry, ok, err := lookupImportCacheEntry(store, c.root, namespacedImportKey(importCacheFeedKey, uint32(sourceIndex64)))
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
		if err := words.setBit(store, entry.value); err != nil {
			return false, err
		}
		sourceWord &= sourceWord - 1
	}
	return false, nil
}

// SourceCount reports the distinct source memberships translated (Rust
// ImportCache::source_memberships).
func (c *ImportCache) SourceCount() uint64 { return c.sourceMemberships }

// TranslatedCount reports the distinct destination memberships created
// (Rust ImportCache::translated_memberships).
func (c *ImportCache) TranslatedCount() uint64 { return c.translatedMemberships }

// release discards the cache tree and clears the translation slot
// (Rust ImportCache::release).
func (c *ImportCache) release(store *DraftStore, check func() error) error {
	if err := tree.DiscardPrivateTree(importCacheCodec{}, store, c.root, check); err != nil {
		return err
	}
	c.root = 0
	c.hasLast = false
	return nil
}

// ImportWords is one translated membership bitmap viewed as a word
// source over the cache tree (Rust ImportWords). The draft store is
// bound at the first batch: the words tree lives in the draft, and
// every word source is read through the same draft mapping during the
// intern. The root facade creates one words object per translated
// source membership and feeds it through the WriterEdit import arms.
type ImportWords struct {
	root      uint32
	wordCount uint32
	store     *DraftStore
}

// NewImportWords starts one empty translated bitmap (Rust
// ImportWords::new).
func NewImportWords() *ImportWords { return &ImportWords{} }

// isEmpty reports the zero-length bitmap (Rust ImportWords::is_empty).
func (w *ImportWords) isEmpty() bool { return w.wordCount == 0 }

// setBit sets one destination feed bit and grows the word length (Rust
// ImportWords::set_bit).
func (w *ImportWords) setBit(store *DraftStore, destinationIndex uint32) error {
	wordIndex := destinationIndex / 64
	entry, ok, err := lookupImportCacheEntry(store, w.root, uint64(wordIndex))
	if err != nil {
		return err
	}
	word := uint64(0)
	if ok {
		word = entry.joined()
	}
	word |= uint64(1) << (destinationIndex % 64)
	if err := insertImportCacheEntry(store, &w.root, splitImportCacheEntry(uint64(wordIndex), word)); err != nil {
		return err
	}
	next, err := checkedImportAdd(wordIndex, 1, "translated membership length")
	if err != nil {
		return err
	}
	if next > w.wordCount {
		w.wordCount = next
	}
	return nil
}

// WordCount returns the canonical bitmap word count (Rust
// ImportWords::word_count).
func (w *ImportWords) WordCount() uint32 { return w.wordCount }

// ReadChunk copies the bitmap words starting at start by value (Rust
// Words<DraftStore> read_words with the HASH_WORDS chunk; the cache
// tree stores only the set words, missing words are zero).
func (w *ImportWords) ReadChunk(start uint32) (words [membershipChunkWords]uint64, count uint32, err error) {
	if start > w.wordCount {
		return words, 0, corrupt("translated membership words are outside the source bounds")
	}
	count = membershipChunkWords
	if remaining := w.wordCount - start; count > remaining {
		count = remaining
	}
	store := w.store
	for index := uint32(0); index < count; index++ {
		var entry importCacheEntry
		var ok bool
		if w.root != 0 {
			entry, ok, err = lookupImportCacheEntry(store, w.root, uint64(start+index))
			if err != nil {
				return words, 0, err
			}
		}
		if ok {
			words[index] = entry.joined()
		}
	}
	return words, count, nil
}

// release discards the words tree (Rust ImportWords::release).
func (w *ImportWords) release(store *DraftStore, check func() error) error {
	if err := tree.DiscardPrivateTree(importCacheCodec{}, store, w.root, check); err != nil {
		return err
	}
	w.root = 0
	return nil
}

// internAndRelease interns the words into the draft membership
// dictionary and discards the words tree (Rust
// ImportWords::intern_and_release over DraftStore::intern_membership).
func (w *ImportWords) internAndRelease(store *DraftStore, check func() error) (membershipInterned, error) {
	w.store = store
	interned, err := draftInternMembership(store, w)
	if err != nil {
		return membershipInterned{}, err
	}
	if err := w.release(store, check); err != nil {
		return membershipInterned{}, err
	}
	return interned, nil
}

func namespacedImportKey(kind uint64, value uint32) uint64 {
	return (kind << 32) | uint64(value)
}

func lookupImportCacheEntry(store tree.Store, root uint32, key uint64) (importCacheEntry, bool, error) {
	if root == 0 {
		return importCacheEntry{}, false, nil
	}
	found, ok, err := tree.AtOrAfter(importCacheCodec{}, store, root, tree.KeyOfU64(key))
	if err != nil {
		return importCacheEntry{}, false, err
	}
	if !ok || found.key != key {
		return importCacheEntry{}, false, nil
	}
	return found, true, nil
}

func insertImportCacheEntry(store tree.RetiringStore, root *uint32, entry importCacheEntry) error {
	encoded := entry.encode()
	var retired tree.RetiredPages
	var err error
	retired, _, err = tree.Insert(importCacheCodec{}, store, root, encoded[:], retired)
	if err != nil {
		return err
	}
	return store.RetirePages(retired)
}

func insertNewImportCacheEntry(store tree.RetiringStore, root *uint32, entry importCacheEntry) error {
	if _, present, err := lookupImportCacheEntry(store, *root, entry.key); err != nil {
		return err
	} else if present {
		return corrupt("duplicate import cache key")
	}
	return insertImportCacheEntry(store, root, entry)
}

func checkedImportIncrement(value uint64, label string) (uint64, error) {
	if value == ^uint64(0) {
		return 0, overflow(label)
	}
	return value + 1, nil
}

func checkedImportAdd(left, right uint32, label string) (uint32, error) {
	sum := uint64(left) + uint64(right)
	if sum > uint64(^uint32(0)) {
		return 0, overflow(label)
	}
	return uint32(sum), nil
}
