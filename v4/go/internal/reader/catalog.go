package reader

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Feed catalog access (binary-format-v4.md section 8). Lookup allocates no
// heap bytes; the returned name aliases the mapping and must not escape the
// operation.

// LookupFeed returns the feed entry for one exact name. The returned Name
// aliases the mapping and is valid only for the current operation. The
// string input is compared without any heap allocation.
func (r *ImmutableReader) LookupFeed(name string) (FeedEntry, bool, error) {
	if !format.FeedNameValidString(name) {
		return FeedEntry{}, false, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid feed name"}
	}
	work.CatalogLookup(1)
	work.TreeLookup(1) // charged before the root shortcut (Rust fixed_tree::query)
	if r.meta.CatalogNameRoot == 0 {
		return FeedEntry{}, false, nil
	}
	cur := r.meta.CatalogNameRoot
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			return FeedEntry{}, false, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return FeedEntry{}, false, err
		}
		if first {
			level = h.Level // the root's own level starts the descent
			first = false
		} else if h.Level != level {
			return FeedEntry{}, false, corrupt("catalog level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, 0, format.SlotItemsPerPage)
		if err != nil {
			return FeedEntry{}, false, err
		}
		switch h.PageType {
		case format.PageTypeCatalogNameBranch:
			if level == 0 {
				return FeedEntry{}, false, corrupt("zero-level name branch")
			}
			child, err := nameBranchChild(sl, name, r.meta.PageCount)
			if err != nil {
				return FeedEntry{}, false, err
			}
			if child == 0 {
				return FeedEntry{}, false, nil // no entry qualifies: absent
			}
			work.TreeDescent(1)
			cur, level = child, level-1
		case format.PageTypeCatalogNameLeaf:
			entry, found, err := nameLeafLookup(sl, name, r.meta.FeedIndexLimit)
			if err != nil || !found {
				return FeedEntry{}, false, err
			}
			return entry, true, nil
		default:
			return FeedEntry{}, false, corrupt("unexpected name page type %d", h.PageType)
		}
	}
}

// FeedEntry is one catalog entry.
type FeedEntry struct {
	FeedIndex uint32
	Name      []byte
}

// cmpName compares one mapped name slice with the caller string without
// allocating; byte comparison over the v4 lowercase-ASCII name domain is
// identical to unsigned-byte comparison.
func cmpName(mapped []byte, name string) int {
	n := len(mapped)
	if len(name) < n {
		n = len(name)
	}
	for i := 0; i < n; i++ {
		if mapped[i] != name[i] {
			if mapped[i] < name[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(mapped) < len(name):
		return -1
	case len(mapped) > len(name):
		return 1
	default:
		return 0
	}
}

// nameBranchChild finds the greatest branch entry whose first name is
// lexicographically <= target, comparing unsigned name bytes. Probes
// validate the record shape and name grammar (the name is the key) but do
// not touch the child; the selected entry is decoded once and its child
// validated. The child field of a non-selected entry is never read.
func nameBranchChild(sl format.SlottedPage, target string, pageCount uint64) (uint32, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		name, err := format.CatalogNameBranchKey(b)
		if err != nil {
			return 0, err
		}
		return cmpName(name, target), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, err
	}
	rec, err := format.DecodeCatalogNameBranch(b)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("name child out of range")
	}
	return rec.Child, nil
}

// nameLeafLookup finds the exact name in one name leaf. Probes decode the
// record shape and compare the name; only the selected record is checked
// against feedIndexLimit (mirroring feed_catalog.rs read_key + decode_leaf:
// the limit is validated on the served entry, never on non-selected
// records).
func nameLeafLookup(sl format.SlottedPage, target string, feedIndexLimit uint64) (FeedEntry, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		rec, err := format.DecodeCatalogNameRecord(b)
		if err != nil {
			return 0, err
		}
		return cmpName(rec.Name, target), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return FeedEntry{}, false, err
	}
	work.LeafValidation(1)
	b, err := sl.Record(best)
	if err != nil {
		return FeedEntry{}, false, err
	}
	rec, err := format.DecodeCatalogNameRecord(b)
	if err != nil {
		return FeedEntry{}, false, err
	}
	if cmpName(rec.Name, target) != 0 {
		return FeedEntry{}, false, nil // exact match required
	}
	if uint64(rec.FeedIndex) >= feedIndexLimit {
		return FeedEntry{}, false, corrupt("catalog feed index %d beyond limit %d", rec.FeedIndex, feedIndexLimit)
	}
	return FeedEntry{FeedIndex: rec.FeedIndex, Name: rec.Name}, true, nil
}

// LookupFeedBytes returns the feed entry for one exact mapped name
// (Rust feed_catalog::lookup takes the FeedName value): the validation
// cross-check feeds the mapped name view directly, with no allocation
// (the string variant of LookupFeed serves the caller-string hot path).
func (r *ImmutableReader) LookupFeedBytes(name []byte) (FeedEntry, bool, error) {
	if !format.FeedNameValid(name) {
		return FeedEntry{}, false, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid feed name"}
	}
	work.CatalogLookup(1)
	work.TreeLookup(1) // charged before the root shortcut (Rust fixed_tree::query)
	if r.meta.CatalogNameRoot == 0 {
		return FeedEntry{}, false, nil
	}
	cur := r.meta.CatalogNameRoot
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			return FeedEntry{}, false, err
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			return FeedEntry{}, false, err
		}
		if first {
			level = h.Level // the root's own level starts the descent
			first = false
		} else if h.Level != level {
			return FeedEntry{}, false, corrupt("catalog level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, 0, format.SlotItemsPerPage)
		if err != nil {
			return FeedEntry{}, false, err
		}
		switch h.PageType {
		case format.PageTypeCatalogNameBranch:
			if level == 0 {
				return FeedEntry{}, false, corrupt("zero-level name branch")
			}
			child, err := nameBranchChildBytes(sl, name, r.meta.PageCount)
			if err != nil {
				return FeedEntry{}, false, err
			}
			if child == 0 {
				return FeedEntry{}, false, nil // no entry qualifies: absent
			}
			work.TreeDescent(1)
			cur, level = child, level-1
		case format.PageTypeCatalogNameLeaf:
			entry, found, err := nameLeafLookupBytes(sl, name, r.meta.FeedIndexLimit)
			if err != nil || !found {
				return FeedEntry{}, false, err
			}
			return entry, true, nil
		default:
			return FeedEntry{}, false, corrupt("unexpected name page type %d", h.PageType)
		}
	}
}

// nameBranchChildBytes is the mapped-name variant of nameBranchChild.
func nameBranchChildBytes(sl format.SlottedPage, target []byte, pageCount uint64) (uint32, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		name, err := format.CatalogNameBranchKey(b)
		if err != nil {
			return 0, err
		}
		return cmpBytes(name, target), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return 0, err
	}
	b, err := sl.Record(best)
	if err != nil {
		return 0, err
	}
	rec, err := format.DecodeCatalogNameBranch(b)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("name child out of range")
	}
	return rec.Child, nil
}

// nameLeafLookupBytes is the mapped-name variant of nameLeafLookup.
func nameLeafLookupBytes(sl format.SlottedPage, target []byte, feedIndexLimit uint64) (FeedEntry, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		rec, err := format.DecodeCatalogNameRecord(b)
		if err != nil {
			return 0, err
		}
		return cmpBytes(rec.Name, target), nil
	}
	best, err := greatestLE(int(sl.Header.ItemCount), cmp)
	if err != nil || best < 0 {
		return FeedEntry{}, false, err
	}
	work.LeafValidation(1)
	b, err := sl.Record(best)
	if err != nil {
		return FeedEntry{}, false, err
	}
	rec, err := format.DecodeCatalogNameRecord(b)
	if err != nil {
		return FeedEntry{}, false, err
	}
	if cmpBytes(rec.Name, target) != 0 {
		return FeedEntry{}, false, nil // exact match required
	}
	if uint64(rec.FeedIndex) >= feedIndexLimit {
		return FeedEntry{}, false, corrupt("catalog feed index %d beyond limit %d", rec.FeedIndex, feedIndexLimit)
	}
	return FeedEntry{FeedIndex: rec.FeedIndex, Name: rec.Name}, true, nil
}

// cmpBytes compares two mapped name views byte by byte (the v4
// lowercase-ASCII name domain makes the unsigned-byte order exact).
func cmpBytes(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
