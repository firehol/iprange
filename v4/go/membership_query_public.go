// Named membership scopes and point matches over one opened immutable or
// live database (Rust membership_query.rs parity). Scopes resolve caller
// feed selections into bounded reusable entry lists; matching emits every
// feed whose membership bitmap contains one address.

package iprangedb

import "github.com/firehol/iprange/v4/go/internal/reader"

// MembershipQueryBudget bounds the heap retained by one reusable scope.
type MembershipQueryBudget struct {
	MaxHeapBytes uint64
}

// FeedPair is one caller-selected pair of feed names (join inputs; the
// canonical definition lives in internal/reader).
type FeedPair = reader.FeedPair

// MatchingFeedsReport is the exact point-match outcome after all matching
// names were emitted.
type MatchingFeedsReport struct {
	MatchingFeedCount uint64
}

// MembershipQuery is the format-facing query capability over one pinned
// membership generation.
type MembershipQuery struct {
	r cursorHost
}

// MembershipQuery opens the membership query surface. The database must
// be a membership database (structured databases are refused; Rust
// membership_query::Query::new parity).
func (r *ImmutableReader) MembershipQuery() (*MembershipQuery, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if err := r.requireMembershipQuery(); err != nil {
		return nil, err
	}
	return &MembershipQuery{r: r}, nil
}

// AllFeeds resolves every active feed into one reusable scope.
func (q *MembershipQuery) AllFeeds(budget MembershipQueryBudget, cancellation *CancellationToken) (*MembershipScope, error) {
	if err := q.r.checkOpen(); err != nil {
		return nil, err
	}
	data, err := q.r.core().ResolveAllFeeds(budget.MaxHeapBytes, cancellation.check)
	if err != nil {
		return nil, publicError(err)
	}
	return &MembershipScope{r: q.r, data: data}, nil
}

// NamedFeeds resolves a nonempty unique name list into one reusable
// scope. Unknown names report ErrorNameNotFound.
func (q *MembershipQuery) NamedFeeds(names []string, budget MembershipQueryBudget, cancellation *CancellationToken) (*MembershipScope, error) {
	if err := q.r.checkOpen(); err != nil {
		return nil, err
	}
	data, err := q.r.core().ResolveNamedFeeds(names, budget.MaxHeapBytes, cancellation.check)
	if err != nil {
		return nil, publicError(err)
	}
	return &MembershipScope{r: q.r, data: data}, nil
}

// MatchingFeedsV4 emits every feed matching one IPv4 address without
// scanning the catalog. Each yielded name is an owned string copy
// (allocated once per match); a nil yield is a sink that discards names
// without allocating; a nil cancellation runs uncancellable.
func (q *MembershipQuery) MatchingFeedsV4(address IPv4, yield func(name string) error, cancellation *CancellationToken) (MatchingFeedsReport, error) {
	if err := q.r.checkOpen(); err != nil {
		return MatchingFeedsReport{}, err
	}
	count, err := q.r.core().MatchingFeeds4(uint32(address), func(name []byte) error {
		if yield == nil {
			return nil
		}
		return yield(string(name))
	}, cancellation.check)
	if err != nil {
		return MatchingFeedsReport{}, publicError(err)
	}
	return MatchingFeedsReport{MatchingFeedCount: count}, nil
}

// MatchingFeedsV6 emits every feed matching one IPv6 address. Each
// yielded name is an owned string copy (allocated once per match).
func (q *MembershipQuery) MatchingFeedsV6(address IPv6, yield func(name string) error, cancellation *CancellationToken) (MatchingFeedsReport, error) {
	if err := q.r.checkOpen(); err != nil {
		return MatchingFeedsReport{}, err
	}
	count, err := q.r.core().MatchingFeeds6(address.Hi, address.Lo, func(name []byte) error {
		if yield == nil {
			return nil
		}
		return yield(string(name))
	}, cancellation.check)
	if err != nil {
		return MatchingFeedsReport{}, publicError(err)
	}
	return MatchingFeedsReport{MatchingFeedCount: count}, nil
}

// MembershipScope is the reusable resolution of one feed selection.
// The scope keeps the reader pinned for its lifetime; Feeds returns
// copied entry names (strings owned by the caller, like LookupFeed).
type MembershipScope struct {
	r    cursorHost
	data *reader.ScopeData
}

// FeedCount returns the number of feeds resolved into this scope.
func (s *MembershipScope) FeedCount() int {
	return s.data.FeedCount()
}

// Feeds returns the scope's resolved entries in ascending local index
// order. The returned slice is owned by the caller; each name string is
// a copy of the catalog entry name (the root boundary never hands out
// views that alias the mapping), so Feeds allocates once per entry.
func (s *MembershipScope) Feeds() []FeedEntry {
	entries := s.data.Entries()
	output := make([]FeedEntry, len(entries))
	for i, entry := range entries {
		output[i] = FeedEntry{Index: entry.FeedIndex, Name: string(entry.Name)}
	}
	return output
}

// family returns the address family of the scope's pinned reader.
func (s *MembershipScope) family() uint8 {
	return s.r.core().Meta().AddressFamily
}
