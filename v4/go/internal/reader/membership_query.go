package reader

// Named membership scopes and point matches (Rust membership_query.rs
// parity). Scopes resolve caller-selected feeds into a bounded reusable
// entry list; matching emits every feed whose membership bitmap contains
// one address, without scanning the catalog.

import (
	"math/bits"
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// checkpoint is the bounded-operation cancellation hook (the public
// CancellationToken.check; nil means no cancellation source).
type checkpoint func() error

// MembershipPointMatch emits one matching feed name; errors stop the scan
// and pass through unchanged.
type MembershipPointMatch func(name []byte) error

// ScopeData is the reusable SDK-owned feed list plus the budget that
// bounded its construction (Rust MembershipScopeState/ScopeData).
type ScopeData struct {
	entries  []FeedEntry
	maxHeap  uint64
	heapUsed uint64
}

// resolveAllFeeds reads every active catalog entry into a bounded scope
// (Rust ScopeData::all): flat-budget charge for the full entry vector
// first, forward feed cursor, cancellation checkpointed every 4096
// entries, then the feed-index map charge at finish.
func (r *ImmutableReader) ResolveAllFeeds(maxHeapBytes uint64, check checkpoint) (*ScopeData, error) {
	data := &ScopeData{maxHeap: maxHeapBytes}
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if maxHeapBytes == 0 {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership scope heap"}
	}
	if err := data.chargeEntries(r.meta.ActiveFeedCount); err != nil {
		return nil, err
	}
	cursor, err := r.NewFeedCursor()
	if err != nil {
		return nil, err
	}
	for {
		if len(data.entries)&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		entry, ok, err := cursor.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		data.entries = append(data.entries, entry)
	}
	if uint64(len(data.entries)) != r.meta.ActiveFeedCount {
		return nil, corrupt("feed catalog count changed during scope")
	}
	if err := data.chargeIndexMap(); err != nil {
		return nil, err
	}
	return data, nil
}

// resolveNamedFeeds resolves one nonempty name list into a bounded scope
// (Rust ScopeData::named): flat-budget charge for the full entry vector
// first, lookups, sort, duplicate check (cancellation checkpointed every
// 4096 steps), then the feed-index map charge at finish. Entries are
// returned in ascending index order; duplicated names are an
// InvalidArgument, unknown names NameNotFound. The empty-scope argument
// error precedes the budget refusal, exactly like Rust.
func (r *ImmutableReader) ResolveNamedFeeds(names []string, maxHeapBytes uint64, check checkpoint) (*ScopeData, error) {
	data := &ScopeData{maxHeap: maxHeapBytes}
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if len(names) == 0 {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "membership scope is empty"}
	}
	if maxHeapBytes == 0 {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership scope heap"}
	}
	if err := data.chargeEntries(uint64(len(names))); err != nil {
		return nil, err
	}
	for i, name := range names {
		if i&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		entry, found, err := r.LookupFeed(name)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, &format.Error{Code: format.CodeNameNotFound, Detail: "feed name not in the catalog"}
		}
		data.entries = append(data.entries, entry)
	}
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	data.sortEntries()
	for i := 1; i < len(data.entries); i++ {
		if i&4095 == 4095 && check != nil {
			if err := check(); err != nil {
				return nil, err
			}
		}
		if data.entries[i-1].FeedIndex == data.entries[i].FeedIndex {
			return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "membership scope feed names are not unique"}
		}
	}
	if err := data.chargeIndexMap(); err != nil {
		return nil, err
	}
	return data, nil
}

// Scope budget parity (Rust membership_query/scope.rs + heap.rs): the
// budget models the Rust owner's retained heap, not Go's allocator, so
// identical inputs admit identically. Charges are: the entries vector
// (count * size_of::<FeedEntry>(); the Rust FeedEntry is 24 bytes: a u32
// index plus a 16-byte borrowed name slice, so name bytes alias the
// mapping and are free) and the feed-index map (dense u32 positions when
// they fit, else sparse slots, whichever is smaller).
const rustFeedEntrySize = 24

// chargeEntries applies the entries-vector charge (Rust
// heap.vector::<FeedEntry>).
func (d *ScopeData) chargeEntries(count uint64) error {
	bytes, ok := mulChecked(count, rustFeedEntrySize)
	if !ok {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership scope heap"}
	}
	return d.chargeHeap(bytes, "membership scope heap")
}

// chargeIndexMap applies the feed-index map charge (Rust IndexMap::new):
// dense positions (last index + 1) * 4 when no larger than the sparse
// slots layout, sparse next_power_of_two(2 * count) * 8 otherwise; an
// entry count beyond u32 position values overflows the position encoding
// and is refused.
func (d *ScopeData) chargeIndexMap() error {
	if len(d.entries) == 0 {
		return nil // Rust IndexMap::Empty
	}
	if uint64(len(d.entries)) > uint64(^uint32(0)) {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership scope exceeds u32"}
	}
	last := d.entries[len(d.entries)-1].FeedIndex
	denseBytes := (uint64(last) + 1) * 4
	sparseLen, ok := nextPowerOfTwoChecked(uint64(len(d.entries)) * 2)
	if !ok {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership scope index heap"}
	}
	sparseBytes, ok := mulChecked(sparseLen, 8)
	if !ok {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "membership scope index heap"}
	}
	bytes := sparseBytes
	if denseBytes <= sparseBytes {
		bytes = denseBytes
	}
	return d.chargeHeap(bytes, "membership scope index heap")
}

// chargeHeap reserves bytes against the scope budget (Rust
// Heap::reserve_bytes).
func (d *ScopeData) chargeHeap(bytes uint64, label string) error {
	if d.heapUsed > d.maxHeap || bytes > d.maxHeap-d.heapUsed {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: label}
	}
	d.heapUsed += bytes
	return nil
}

// mulChecked multiplies two counts with overflow detection (Rust checked
// arithmetic on the heap model).
func mulChecked(a, b uint64) (uint64, bool) {
	if a != 0 && b > ^uint64(0)/a {
		return 0, false
	}
	return a * b, true
}

// nextPowerOfTwoChecked computes the next power of two at or above v,
// refusing values whose power of two would not fit (Rust
// usize::checked_next_power_of_two).
func nextPowerOfTwoChecked(v uint64) (uint64, bool) {
	if v > uint64(1)<<63 {
		return 0, false
	}
	if v == 0 {
		return 1, true
	}
	return uint64(1) << uint(64-bits.LeadingZeros64(v-1)), true
}

// matchingFeeds4 emits every feed matching one IPv4 address (Rust
// matching): resolve the membership bitmap, walk its words in bounded
// batches, and resolve each set feed index through the catalog.
func (r *ImmutableReader) MatchingFeeds4(addr uint32, emit MembershipPointMatch, check checkpoint) (uint64, error) {
	membership, found, err := r.LookupMembership4(addr)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	work.MembershipDecode(1)
	return r.matchingFromView(membership, emit, check)
}

// matchingFeeds6 emits every feed matching one IPv6 address.
func (r *ImmutableReader) MatchingFeeds6(addrHi, addrLo uint64, emit MembershipPointMatch, check checkpoint) (uint64, error) {
	membership, found, err := r.LookupMembership6(addrHi, addrLo)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	work.MembershipDecode(1)
	return r.matchingFromView(membership, emit, check)
}

const wordBatch = 64

func (r *ImmutableReader) matchingFromView(membership MembershipView, emit MembershipPointMatch, check checkpoint) (uint64, error) {
	wordCount := membership.WordCount()
	var words [wordBatch]uint64
	var start uint32
	var matching uint64
	for start < wordCount {
		if check != nil {
			if err := check(); err != nil {
				return 0, err
			}
		}
		expected := wordCount - start
		if expected > wordBatch {
			expected = wordBatch
		}
		read, err := membership.ReadWords(start, words[:expected])
		if err != nil {
			return 0, err
		}
		if uint32(read) != expected {
			return 0, corrupt("membership word read ended early")
		}
		for offset, word := range words[:read] {
			if err := emitWord(r, start+uint32(offset), word, emit, &matching); err != nil {
				return 0, err
			}
		}
		if start > ^uint32(0)-expected {
			return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "membership word index"}
		}
		start += expected
	}
	return matching, nil
}

func emitWord(r *ImmutableReader, wordIndex uint32, word uint64, emit MembershipPointMatch, count *uint64) error {
	for word != 0 {
		bit := uint32(bits.TrailingZeros64(word))
		if wordIndex > ^uint32(0)/64 {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "feed index"}
		}
		index := wordIndex*64 + bit
		entry, found, err := r.LookupFeedIndex(index)
		if err != nil {
			return err
		}
		if !found {
			return corrupt("membership names an inactive feed")
		}
		if err := emit(entry.Name); err != nil {
			return err
		}
		if *count == ^uint64(0) {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "matching feed count"}
		}
		*count++
		word &= word - 1
	}
	return nil
}

// FeedCount returns the number of feeds resolved into this scope.
func (d *ScopeData) FeedCount() int {
	return len(d.entries)
}

// Entries returns the resolved entries in ascending local index order.
// The resulting slice aliases the scope; the name slices alias the
// mapping and stay valid while the reader stays open.
func (d *ScopeData) Entries() []FeedEntry {
	return d.entries
}

// sortEntries orders the resolved entries by ascending feed index (Rust
// named ScopeData: entries.sort_unstable_by_key(|entry| entry.index)).
func (d *ScopeData) sortEntries() {
	sort.Slice(d.entries, func(i, j int) bool {
		return d.entries[i].FeedIndex < d.entries[j].FeedIndex
	})
}
