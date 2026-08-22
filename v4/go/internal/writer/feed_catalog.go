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
	work.TreeLookup(1) // charged before the root shortcut (Rust fixed_tree::query)
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

// renameCurrentFeed renames one current feed entry, refusing a name that
// already exists (Rust DraftStore::rename_current_feed). The caller
// proves the entry is current; the existence probe only guards the new
// name.
func (s *DraftStore) renameCurrentFeed(entry feedEntry, newName string) (feedEntry, error) {
	if _, found, err := s.lookupFeed(newName); err != nil {
		return feedEntry{}, err
	} else if found {
		return feedEntry{}, &format.Error{Code: format.CodeNameExists, Detail: "feed name already exists"}
	}
	return s.renameCurrentFeedKnownAvailable(entry, newName)
}

// renameCurrentFeedKnownAvailable renames one current feed when the new
// name is already proven available (Rust
// DraftStore::rename_current_feed_known_available): the dual roots move
// only after the rename succeeds, and the draft is marked changed.
func (s *DraftStore) renameCurrentFeedKnownAvailable(entry feedEntry, newName string) (feedEntry, error) {
	nameRoot := s.draft.meta.CatalogNameRoot
	indexRoot := s.draft.meta.CatalogIndexRoot
	if err := renameCatalogEntry(s, s.catalogScratch[:], &nameRoot, &indexRoot, entry, newName); err != nil {
		return feedEntry{}, err
	}
	s.draft.meta.CatalogNameRoot = nameRoot
	s.draft.meta.CatalogIndexRoot = indexRoot
	s.draft.changed = true
	return feedEntry{name: newName, index: entry.index}, nil
}

// removeCurrentFeed deletes one current feed entry and clears its used
// bit (Rust DraftStore::remove_current_feed): the catalog roots move
// first, then the feed-index namespace bit, then the active count.
func (s *DraftStore) removeCurrentFeed(expected feedEntry) error {
	nameRoot := s.draft.meta.CatalogNameRoot
	indexRoot := s.draft.meta.CatalogIndexRoot
	if err := deleteCatalogEntry(s, &nameRoot, &indexRoot, expected); err != nil {
		return err
	}
	s.draft.meta.CatalogNameRoot = nameRoot
	s.draft.meta.CatalogIndexRoot = indexRoot

	usedRoot := s.draft.meta.FeedUsedRoot
	var retired tree.RetiredPages
	cleared, err := bitmap.ClearUsed(s, &usedRoot, s.draft.meta.FeedIndexLimit, bitmap.KindFeed, expected.index, &retired)
	if err != nil {
		return err
	}
	if !cleared {
		return corrupt("deleted feed used bit is missing")
	}
	s.draft.meta.FeedUsedRoot = usedRoot
	if err := s.RetirePages(retired); err != nil {
		return err
	}
	if s.draft.meta.ActiveFeedCount == 0 {
		return overflow("active feed count")
	}
	s.draft.meta.ActiveFeedCount--
	s.draft.changed = true
	return nil
}

// lookupCatalogFeed resolves one exact feed name in one pinned catalog
// generation (Rust feed_catalog::lookup over a MetaV4): the committed
// base for the workflow preconditions, or the draft generation for
// reference currency. The pinned view bounds every page read to the
// generation's page count and transaction.
func lookupCatalogFeed(store *DraftStore, meta format.Meta, name string) (feedEntry, bool, error) {
	work.CatalogLookup(1)
	work.TreeLookup(1) // charged before the root shortcut (Rust fixed_tree::query)
	if meta.CatalogNameRoot == 0 {
		return feedEntry{}, false, nil
	}
	value, ok, err := tree.AtOrAfter(nameCodec{}, selectedStore{store: store, meta: meta}, meta.CatalogNameRoot, tree.VarKey([]byte(name)))
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
