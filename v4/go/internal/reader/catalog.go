package reader

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Feed catalog access (binary-format-v4.md section 8). Lookup allocates no
// heap bytes; the returned name aliases the mapping and must not escape the
// operation.

// LookupFeed returns the feed entry for one exact name. The returned Name
// aliases the mapping and is valid only for the current operation. The
// string input is compared without any heap allocation.
func (r *ImmutableReader) LookupFeed(name string) (FeedEntry, bool, error) {
	if r.meta.CatalogNameRoot == 0 {
		return FeedEntry{}, false, nil
	}
	if !format.FeedNameValidString(name) {
		return FeedEntry{}, false, &format.Error{Code: format.CodeNameInvalid, Detail: "invalid feed name"}
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
// lexicographically <= target, comparing unsigned name bytes.
func nameBranchChild(sl format.SlottedPage, target string, pageCount uint64) (uint32, error) {
	probe := func(i int) (format.CatalogNameBranchRecord, error) {
		b, err := sl.Record(i)
		if err != nil {
			return format.CatalogNameBranchRecord{}, err
		}
		return format.DecodeCatalogNameBranch(b)
	}
	lo, hi := 0, int(sl.Header.ItemCount)
	best := -1
	for lo < hi {
		mid := lo + (hi-lo)/2
		rec, err := probe(mid)
		if err != nil {
			return 0, err
		}
		if cmpName(rec.FirstName, target) <= 0 {
			best = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if best < 0 {
		return 0, nil // no entry qualifies: the name is absent
	}
	rec, err := probe(best)
	if err != nil || !format.PageNumberValid(rec.Child, pageCount) {
		return 0, corrupt("name child out of range")
	}
	return rec.Child, nil
}

// nameLeafLookup finds the exact name in one name leaf. Every probed record
// is validated against feedIndexLimit: a record whose index is at or above
// the committed limit is corruption (feed_catalog.rs decode_leaf).
func nameLeafLookup(sl format.SlottedPage, target string, feedIndexLimit uint64) (FeedEntry, bool, error) {
	probe := func(i int) (format.CatalogNameRecord, error) {
		b, err := sl.Record(i)
		if err != nil {
			return format.CatalogNameRecord{}, err
		}
		rec, err := format.DecodeCatalogNameRecord(b)
		if err != nil {
			return format.CatalogNameRecord{}, err
		}
		if uint64(rec.FeedIndex) >= feedIndexLimit {
			return format.CatalogNameRecord{}, corrupt("catalog feed index %d beyond limit %d", rec.FeedIndex, feedIndexLimit)
		}
		return rec, nil
	}
	lo, hi := 0, int(sl.Header.ItemCount)
	for lo < hi {
		mid := lo + (hi-lo)/2
		rec, err := probe(mid)
		if err != nil {
			return FeedEntry{}, false, err
		}
		switch cmpName(rec.Name, target) {
		case 0:
			return FeedEntry{FeedIndex: rec.FeedIndex, Name: rec.Name}, true, nil
		case -1:
			lo = mid + 1
		case 1:
			hi = mid
		}
	}
	return FeedEntry{}, false, nil
}
