package iprangeformat

import (
	"sort"

	"slices"
)

// MergeFeed is one input feed: a stable feed_id, its identity, and a value-less range
// list (a plain IP set — the membership set, not a per-range value, is assigned by the
// sweep, §13.3).
type MergeFeed[K ipKey[K]] struct {
	FeedID uint32
	Meta   FeedMeta
	Ranges [][2]K // inclusive [start, end] pairs
}

// MergeWriter builds a v3.1 merged file for key width K (§13.3). The target address
// family is K itself: an Ipv4Key writer is all-IPv4, an Ipv6Key writer all-IPv6 —
// fixed even when every feed is empty, so there is no vacuous-family ambiguity.
type MergeWriter[K ipKey[K]] struct {
	meta       FeedMeta // the merged artifact's own identity (§13.1)
	license    uint32
	generation uint64
	feeds      []MergeFeed[K]
}

func newMergeWriter[K ipKey[K]](meta FeedMeta, licenseFlags uint32, generationUnix uint64) *MergeWriter[K] {
	return &MergeWriter[K]{meta: meta, license: licenseFlags, generation: generationUnix}
}

// NewMergeWriterV4 starts an IPv4 merge with the merged-artifact identity metadata.
func NewMergeWriterV4(meta FeedMeta, licenseFlags uint32, generationUnix uint64) *MergeWriter[Ipv4Key] {
	return newMergeWriter[Ipv4Key](meta, licenseFlags, generationUnix)
}

// NewMergeWriterV6 starts an IPv6 merge.
func NewMergeWriterV6(meta FeedMeta, licenseFlags uint32, generationUnix uint64) *MergeWriter[Ipv6Key] {
	return newMergeWriter[Ipv6Key](meta, licenseFlags, generationUnix)
}

// AddFeed adds an input feed. Its ranges may overlap ranges of other feeds arbitrarily
// (that is the point); overlap within one feed is rejected at Build.
func (m *MergeWriter[K]) AddFeed(feedID uint32, meta FeedMeta, ranges [][2]K) error {
	for _, r := range ranges {
		if r[0].cmp(r[1]) > 0 {
			return errInvalidInput("merge feed range start > end")
		}
	}
	m.feeds = append(m.feeds, MergeFeed[K]{FeedID: feedID, Meta: meta, Ranges: ranges})
	return nil
}

// Build produces the complete v3.1 merged file bytes, or an error if not encodable
// (§13.3).
func (m *MergeWriter[K]) Build() ([]byte, error) {
	if len(m.feeds) == 0 {
		return nil, errInvalidInput("a merged file needs at least one feed")
	}

	// Catalog is sorted by feed_id (strictly ascending, §13.2); reject duplicates.
	feeds := slices.Clone(m.feeds)
	slices.SortFunc(feeds, func(a, b MergeFeed[K]) int {
		switch {
		case a.FeedID < b.FeedID:
			return -1
		case a.FeedID > b.FeedID:
			return 1
		default:
			return 0
		}
	})
	for i := 1; i < len(feeds); i++ {
		if feeds[i].FeedID == feeds[i-1].FeedID {
			return nil, errInvalidInput("duplicate feed_id in merge input")
		}
	}
	// each catalog identity field must be valid UTF-8 with a u32 length (§13.2).
	for i := range feeds {
		if err := feeds[i].Meta.validate(); err != nil {
			return nil, err
		}
	}

	// Per-feed: sort by start and reject within-feed overlap (§13.3). Contiguity within
	// a feed is left to the writer's final coalescing pass.
	for i := range feeds {
		rs := slices.Clone(feeds[i].Ranges)
		slices.SortFunc(rs, func(a, b [2]K) int { return a[0].cmp(b[0]) })
		for j := 1; j < len(rs); j++ {
			if rs[j-1][1].cmp(rs[j][0]) >= 0 {
				return nil, errInvalidInput("overlapping ranges within one feed")
			}
		}
		feeds[i].Ranges = rs
	}

	// Boundary sweep (§13.3): start (activate) and end+1 (deactivate) events; a range
	// reaching the family maximum has no end+1 (it stays active to the last address).
	type ev struct{ adds, removes []uint32 }
	events := map[K]*ev{}
	at := func(k K) *ev {
		e := events[k]
		if e == nil {
			e = &ev{}
			events[k] = e
		}
		return e
	}
	for i := range feeds {
		for _, r := range feeds[i].Ranges {
			at(r[0]).adds = append(at(r[0]).adds, feeds[i].FeedID)
			if e1, ok := r[1].checkedInc(); ok {
				at(e1).removes = append(at(e1).removes, feeds[i].FeedID)
			}
		}
	}
	boundaries := make([]K, 0, len(events))
	for k := range events {
		boundaries = append(boundaries, k)
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].cmp(boundaries[j]) < 0 })

	// Counts make the active set independent of intra-address event order (a feed that
	// ends at b-1 and resumes at b nets to "still active").
	active := map[uint32]int{}
	w := &Writer[K]{meta: m.meta, license: m.license, generation: m.generation}
	var famMax K
	famMax = famMax.maxKey()
	for i, b := range boundaries {
		e := events[b]
		for _, id := range e.removes {
			if c, ok := active[id]; ok {
				if c == 1 {
					delete(active, id)
				} else {
					active[id] = c - 1
				}
			}
		}
		for _, id := range e.adds {
			active[id]++
		}
		if len(active) == 0 {
			continue // a gap covered by no feed — emit nothing
		}
		var end K
		if i+1 < len(boundaries) {
			d, ok := boundaries[i+1].checkedDec()
			if !ok {
				return nil, errInvariant("merge boundary underflow")
			}
			end = d
		} else {
			end = famMax
		}
		if err := w.AddRange(b, end, membershipValue(active)); err != nil {
			return nil, err
		}
	}

	catalog := encodeCatalog(feeds)
	// version_minor = 1 + the catalog section mark the file merged (§13.1).
	return w.buildInner(versionMinorMerged, catalog)
}

// membershipValue builds a type_id==1 value from the active feed-ids in ascending
// order (each a little-endian uint32, §10).
func membershipValue(active map[uint32]int) *Value {
	ids := make([]uint32, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	b := make([]byte, 0, len(ids)*4)
	for _, id := range ids {
		b = le.AppendUint32(b, id)
	}
	return &Value{TypeID: 1, Bytes: b}
}

// encodeCatalog encodes the catalog section (§13.2): feed_count, field_count (6), then
// each feed in ascending feed_id order as feed_id + its six length-prefixed fields.
func encodeCatalog[K ipKey[K]](feeds []MergeFeed[K]) []byte {
	out := make([]byte, 0, 8+len(feeds)*8)
	out = le.AppendUint32(out, uint32(len(feeds)))
	out = le.AppendUint32(out, feedMetaFieldCount)
	for i := range feeds {
		out = le.AppendUint32(out, feeds[i].FeedID)
		out = feeds[i].Meta.appendFields(out)
	}
	return out
}
