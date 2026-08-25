// Randomized scalar-model property tests of the membership query and
// aggregation surfaces (Rust
// iprange-livedb/tests/membership_query_properties.rs parity): random
// boolean models drive real feed creations, then MatchingFeedsV4,
// AllFeeds, and the AllPairs aggregation are checked against the model
// point by point and pair by pair.

package iprangedb

import (
	"fmt"
	"path/filepath"
	"testing"
)

// queryPropertyRounds/Addresses/Feeds mirror the Rust property test
// constants.
const (
	queryPropertyRounds    = 24
	queryPropertyAddresses = 96
	queryPropertyFeeds     = 7
)

// queryPropertyRandom is the Rust xorshift64 generator used by the
// property test (random() in the Rust test): the caller state is the
// full u64 after the shift chain, and the present test is
// state % 7 < 2.
func queryPropertyRandom(state *uint64) uint64 {
	*state ^= *state << 13
	*state ^= *state >> 7
	*state ^= *state << 17
	return *state
}

// queryPropertyBudget mirrors the Rust transaction_budget() values,
// including the live-writer open-files bound.
func queryPropertyBudget() PageBudget {
	return PageBudget{
		MaxHeapBytes:    2 * 1024 * 1024,
		MaxPrivatePages: 20_000,
		MaxGrowthPages:  20_000,
		MaxOpenFiles:    2,
	}
}

// queryPropertyBooleanRanges converts one per-address boolean model
// slice into the minimal inclusive IPv4 range list (Rust boolean_ranges:
// one trailing false sentinel closes the final run).
func queryPropertyBooleanRanges(values []bool) []AddressRange4 {
	ranges := make([]AddressRange4, 0)
	start := -1
	for index := 0; index <= len(values); index++ {
		present := index < len(values) && values[index]
		switch {
		case start == -1 && present:
			start = index
		case start != -1 && !present:
			ranges = append(ranges, AddressRange4{From: IPv4(start), To: IPv4(index - 1)})
			start = -1
		}
	}
	return ranges
}

// queryPropertyOutput collects the aggregate sink batches (Rust Output
// MembershipAggregateSink).
type queryPropertyOutput struct {
	feeds []FeedCardinality
	pairs []FeedOverlap
}

// TestRandomizedPointAndPairQueriesMatchAScalarModel runs the Rust
// randomized_point_and_pair_queries_match_a_scalar_model property: 24
// rounds, each creating 7 feeds from a random [7][96]bool model, then
// verifying every point match, feed cardinality, and unordered pair
// overlap against the model.
func TestRandomizedPointAndPairQueriesMatchAScalarModel(t *testing.T) {
	requireFileCreation(t)
	cancellation := NewCancellationToken()
	state := uint64(0x51d209baa36ec47f)
	for round := 0; round < queryPropertyRounds; round++ {
		var model [queryPropertyFeeds][queryPropertyAddresses]bool
		for feed := range model {
			for address := range model[feed] {
				model[feed][address] = queryPropertyRandom(&state)%7 < 2
			}
		}

		path := filepath.Join(t.TempDir(), "query-property.iprdb")
		tag, err := NewValueTag([]byte("feeds"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
			t.Fatal(err)
		}
		writer, err := OpenWriter(path, queryPropertyBudget())
		if err != nil {
			t.Fatal(err)
		}
		for feedIndex := 0; feedIndex < queryPropertyFeeds; feedIndex++ {
			create, err := writer.BeginCreateFeed(feedName(t, fmt.Sprintf("f%d", feedIndex)), cancellation)
			if err != nil {
				t.Fatal(err)
			}
			if err := create.AddRangesV4(queryPropertyBooleanRanges(model[feedIndex][:])); err != nil {
				t.Fatal(err)
			}
			finished, err := create.FinishInput()
			if err != nil {
				t.Fatal(err)
			}
			if !finished.IsChanged() {
				t.Fatalf("round %d: new feed f%d did not change catalog", round, feedIndex)
			}
			result, err := finished.Commit()
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != CommitCommitted {
				t.Fatalf("round %d: feed f%d commit = %v, want committed", round, feedIndex, result.Status)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}

		reader, err := OpenImmutable(path)
		if err != nil {
			t.Fatal(err)
		}
		query, err := reader.MembershipQuery()
		if err != nil {
			t.Fatal(err)
		}
		for address := 0; address < queryPropertyAddresses; address++ {
			actual := make([]string, 0)
			if _, err := query.MatchingFeedsV4(IPv4(address), func(name string) error {
				actual = append(actual, name)
				return nil
			}, cancellation); err != nil {
				t.Fatal(err)
			}
			expected := make([]string, 0)
			for feedIndex := 0; feedIndex < queryPropertyFeeds; feedIndex++ {
				if model[feedIndex][address] {
					expected = append(expected, fmt.Sprintf("f%d", feedIndex))
				}
			}
			if len(actual) != len(expected) {
				t.Fatalf("round %d, address %d: matching feeds %v, want %v", round, address, actual, expected)
			}
			for index := range expected {
				if actual[index] != expected[index] {
					t.Fatalf("round %d, address %d: matching feeds %v, want %v", round, address, actual, expected)
				}
			}
		}

		scope, err := query.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 2 * 1024 * 1024}, cancellation)
		if err != nil {
			t.Fatal(err)
		}
		output := queryPropertyOutput{}
		if _, err := scope.Aggregate(MembershipAggregationAllPairs(),
			func(batch []FeedCardinality) error {
				output.feeds = append(output.feeds, batch...)
				return nil
			},
			func(batch []FeedOverlap) error {
				output.pairs = append(output.pairs, batch...)
				return nil
			},
			cancellation); err != nil {
			t.Fatal(err)
		}

		for feedIndex := 0; feedIndex < queryPropertyFeeds; feedIndex++ {
			expected := uint64(0)
			for _, present := range model[feedIndex] {
				if present {
					expected++
				}
			}
			name := fmt.Sprintf("f%d", feedIndex)
			found := false
			for _, cell := range output.feeds {
				if cell.Feed == name {
					found = true
					if actual := cell.Addresses.Lo(); actual != expected {
						t.Fatalf("round %d, feed %d: cardinality %d, want %d", round, feedIndex, actual, expected)
					}
				}
			}
			if !found {
				t.Fatalf("round %d: feed %s missing from cardinalities", round, name)
			}
		}
		for left := 0; left < queryPropertyFeeds; left++ {
			for right := left + 1; right < queryPropertyFeeds; right++ {
				expected := uint64(0)
				for address := 0; address < queryPropertyAddresses; address++ {
					if model[left][address] && model[right][address] {
						expected++
					}
				}
				leftName := fmt.Sprintf("f%d", left)
				rightName := fmt.Sprintf("f%d", right)
				found := false
				for _, cell := range output.pairs {
					if cell.Left == leftName && cell.Right == rightName {
						found = true
						if actual := cell.Addresses.Lo(); actual != expected {
							t.Fatalf("round %d, pair %d/%d: overlap %d, want %d", round, left, right, actual, expected)
						}
					}
				}
				if !found {
					t.Fatalf("round %d: pair %s/%s missing from overlaps", round, leftName, rightName)
				}
			}
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
