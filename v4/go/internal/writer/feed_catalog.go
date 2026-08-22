// Draft-owned feed catalog operations (Rust draft_store/catalog.rs): the
// name lookup, the ensure/insert over the dual name and index trees
// through the shared atomic insert, and the feed-index allocation over
// the feed used bitmap. Names stay caller-owned strings; no mapped bytes
// are ever copied into owned memory.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// feedEntry is one draft catalog entry (Rust FeedEntry): the validated
// feed name and its numeric index. The name is the caller's owned string,
// never a mapped view.
type feedEntry struct {
	name  string
	index uint32
}

// lookupFeed resolves one exact feed name in the draft catalog (Rust
// DraftStore::lookup_feed, which takes the already-validated FeedName
// type). Callers must validate the name at their boundary first: the
// encoder's corrupt-class re-check below guards the stored format, it
// never converts user input errors.
func (s *DraftStore) lookupFeed(name string) (feedEntry, bool, error) {
	work.CatalogLookup(1)
	if s.draft.meta.CatalogNameRoot == 0 {
		return feedEntry{}, false, nil
	}
	value, ok, err := tree.AtOrAfter(nameCodec{}, s, s.draft.meta.CatalogNameRoot, tree.VarKey([]byte(name)))
	if err != nil {
		return feedEntry{}, false, err
	}
	if !ok {
		return feedEntry{}, false, nil
	}
	if !nameBytesEqual(name, value.Name) {
		return feedEntry{}, false, nil
	}
	return feedEntry{name: name, index: value.FeedIndex}, true, nil
}

// ensureFeed returns the existing entry or creates the feed (Rust
// DraftStore::ensure_feed: the created flag distinguishes the two).
func (s *DraftStore) ensureFeed(name string) (feedEntry, bool, error) {
	if entry, found, err := s.lookupFeed(name); err != nil {
		return feedEntry{}, false, err
	} else if found {
		return entry, false, nil
	}
	entry, err := s.insertFeed(name)
	if err != nil {
		return feedEntry{}, false, err
	}
	return entry, true, nil
}

// insertFeed allocates a feed index and inserts the dual catalog records
// (Rust DraftStore::insert_feed): the roots and the active feed count move
// only after both inserts succeed, and the draft is marked changed.
func (s *DraftStore) insertFeed(name string) (feedEntry, error) {
	index, err := s.allocateFeedIndex()
	if err != nil {
		return feedEntry{}, err
	}
	nameRoot := s.draft.meta.CatalogNameRoot
	indexRoot := s.draft.meta.CatalogIndexRoot
	if err := insertCatalogEntry(s, s.catalogScratch[:], &nameRoot, &indexRoot, name, index); err != nil {
		return feedEntry{}, err
	}
	s.draft.meta.CatalogNameRoot = nameRoot
	s.draft.meta.CatalogIndexRoot = indexRoot
	if s.draft.meta.ActiveFeedCount == ^uint64(0) {
		return feedEntry{}, overflow("active feed count")
	}
	s.draft.meta.ActiveFeedCount++
	s.draft.changed = true
	return feedEntry{name: name, index: index}, nil
}

// allocateFeedIndex hands out the lowest free feed index: a reused hole
// from the used bitmap, or the new index at the limit when the namespace
// is dense (Rust DraftStore::allocate_feed_index; the 2^32 limit is the
// feed-index exhaustion class).
func (s *DraftStore) allocateFeedIndex() (uint32, error) {
	root := s.draft.meta.FeedUsedRoot
	limit := s.draft.meta.FeedIndexLimit
	var retired tree.RetiredPages
	reused, ok, err := bitmap.TakeLowestUsed(s, &root, limit, bitmap.KindFeed, &retired)
	if err != nil {
		return 0, err
	}
	var index uint32
	if ok {
		index = reused
	} else {
		if limit == 1<<32 {
			return 0, &format.Error{Code: format.CodeFeedIndexExhausted, Detail: "feed-index space is exhausted"}
		}
		index = uint32(limit)
		nextLimit := limit + 1
		if err := bitmap.SetUsed(s, &root, nextLimit, bitmap.KindFeed, index, &retired); err != nil {
			return 0, err
		}
		s.draft.meta.FeedIndexLimit = nextLimit
	}
	s.draft.meta.FeedUsedRoot = root
	if err := s.RetirePages(retired); err != nil {
		return 0, err
	}
	return index, nil
}

// nameBytesEqual compares one caller string with one stored name without
// copying the stored bytes (the stored name aliases the mapping).
func nameBytesEqual(name string, stored []byte) bool {
	if len(name) != len(stored) {
		return false
	}
	for index := 0; index < len(name); index++ {
		if name[index] != stored[index] {
			return false
		}
	}
	return true
}
