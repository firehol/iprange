// Randomized scalar-model property test of the membership algebra over
// randomized public sources (Rust
// iprange-livedb/tests/membership_algebra_properties.rs parity): 5
// randomized source databases of 6 feeds over 128 addresses build one
// global per-address model; the algebra Count and Compare run over
// AllFeeds scopes, and the four PublishSet outputs (union,
// intersection, exclusion, flat) are each reopened and verified per
// address through MatchingFeedsV4.

package iprangedb

import (
	"fmt"
	"path/filepath"
	"testing"
)

// algebraPropertySources/Feeds/Domain mirror the Rust property test
// constants.
const (
	algebraPropertySources = 5
	algebraPropertyFeeds   = 6
	algebraPropertyDomain  = 128
)

// algebraPropertyRandom is the Rust xorshift64 generator used by the
// property test (random() in the Rust test): the caller state is the
// full u64 after the shift chain, and the present test is
// state % 7 < 2.
func algebraPropertyRandom(state *uint64) uint64 {
	*state ^= *state << 13
	*state ^= *state >> 7
	*state ^= *state << 17
	return *state
}

// algebraPropertyTransactionBudget mirrors the Rust transaction_budget()
// values, including the live-writer open-files bound.
func algebraPropertyTransactionBudget() PageBudget {
	return PageBudget{
		MaxHeapBytes:    2 * 1024 * 1024,
		MaxPrivatePages: 20_000,
		MaxGrowthPages:  20_000,
		MaxOpenFiles:    2,
	}
}

// algebraPropertyOutputBudget mirrors the Rust output_budget().
func algebraPropertyOutputBudget() AlgebraOutputBudget {
	return AlgebraOutputBudget{MaxOutputPages: 20_000, MaxOpenFiles: 3}
}

// algebraPropertyBooleanRanges converts one per-address boolean model
// slice into the minimal inclusive IPv4 range list (Rust boolean_ranges:
// one trailing false sentinel closes the final run).
func algebraPropertyBooleanRanges(values []bool) []AddressRange4 {
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

// algebraPropertyNames renders one index list as feed names f<index>
// (Rust names()).
func algebraPropertyNames(indexes []int) []string {
	names := make([]string, len(indexes))
	for i, index := range indexes {
		names[i] = fmt.Sprintf("f%d", index)
	}
	return names
}

// algebraPropertyAddFeed creates one named feed from one per-address
// boolean model on the writer (Rust add_feed()).
func algebraPropertyAddFeed(t *testing.T, writer *Writer, name string, values []bool) {
	t.Helper()
	feed, err := NewFeedName(name)
	if err != nil {
		t.Fatal(err)
	}
	create, err := writer.BeginCreateFeed(feed, NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4(algebraPropertyBooleanRanges(values)); err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !finished.IsChanged() {
		t.Fatalf("feed %s: expected catalog change", name)
	}
	if _, err := finished.Commit(); err != nil {
		t.Fatal(err)
	}
}

// algebraPropertyAddressSet folds one per-address predicate over the
// global model into one address boolean set (Rust address_set()).
func algebraPropertyAddressSet(global *[algebraPropertyFeeds][algebraPropertyDomain]bool, wanted []int, predicate func(present []bool, wanted []int) bool) [algebraPropertyDomain]bool {
	var output [algebraPropertyDomain]bool
	for address := 0; address < algebraPropertyDomain; address++ {
		present := make([]bool, algebraPropertyFeeds)
		for feed := 0; feed < algebraPropertyFeeds; feed++ {
			present[feed] = global[feed][address]
		}
		output[address] = predicate(present, wanted)
	}
	return output
}

// algebraPropertyMatching returns the matching feed names of one IPv4
// address in catalog order (Rust matching()).
func algebraPropertyMatching(t *testing.T, reader *ImmutableReader, address int) []string {
	t.Helper()
	query, err := reader.MembershipQuery()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0)
	if _, err := query.MatchingFeedsV4(IPv4(address), func(name string) error {
		names = append(names, name)
		return nil
	}, NewCancellationToken()); err != nil {
		t.Fatal(err)
	}
	return names
}

// algebraPropertyAssertEqualNames compares two feed-name lists in
// order, treating nil and empty as equal.
func algebraPropertyAssertEqualNames(t *testing.T, address int, actual, expected []string) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("address %d: matching feeds %v, want %v", address, actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("address %d: matching feeds %v, want %v", address, actual, expected)
		}
	}
}

// algebraPropertyAssertPreserved verifies one published preserve-feeds
// output per address: the predicate decides membership, and the
// expected names are the wanted feeds present at the address (Rust
// assert_preserved()).
func algebraPropertyAssertPreserved(t *testing.T, path string, global *[algebraPropertyFeeds][algebraPropertyDomain]bool, wanted []int, predicate func(present []bool, wanted []int) bool) {
	t.Helper()
	reader, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, reader)
	for address := 0; address < algebraPropertyDomain; address++ {
		present := make([]bool, algebraPropertyFeeds)
		for feed := 0; feed < algebraPropertyFeeds; feed++ {
			present[feed] = global[feed][address]
		}
		expected := make([]string, 0)
		if predicate(present, wanted) {
			for _, feed := range wanted {
				if global[feed][address] {
					expected = append(expected, fmt.Sprintf("f%d", feed))
				}
			}
		}
		algebraPropertyAssertEqualNames(t, address, algebraPropertyMatching(t, reader, address), expected)
	}
}

// TestRandomizedGlobalAlgebraMatchesAScalarAddressModel runs the Rust
// randomized_global_algebra_matches_a_scalar_address_model property: 5
// randomized source databases of 6 feeds over 128 addresses build one
// global per-address model; the algebra Count and Compare are checked
// against the model, and the four PublishSet outputs (union,
// intersection, exclusion, flat) are each reopened and verified per
// address.
func TestRandomizedGlobalAlgebraMatchesAScalarAddressModel(t *testing.T) {
	requirePublicationSecurity(t)
	requireLiveCreation(t)
	state := uint64(0xa183f9de36b47021)
	var sourceModel [algebraPropertySources][algebraPropertyFeeds][algebraPropertyDomain]bool
	for source := 0; source < algebraPropertySources; source++ {
		for feed := 0; feed < algebraPropertyFeeds; feed++ {
			for address := 0; address < algebraPropertyDomain; address++ {
				sourceModel[source][feed][address] = algebraPropertyRandom(&state)%7 < 2
			}
		}
	}
	var global [algebraPropertyFeeds][algebraPropertyDomain]bool
	for feed := 0; feed < algebraPropertyFeeds; feed++ {
		for address := 0; address < algebraPropertyDomain; address++ {
			for source := 0; source < algebraPropertySources; source++ {
				if sourceModel[source][feed][address] {
					global[feed][address] = true
					break
				}
			}
		}
	}

	cancellation := NewCancellationToken()
	sourcePaths := make([]string, algebraPropertySources)
	for source := 0; source < algebraPropertySources; source++ {
		sourcePaths[source] = filepath.Join(t.TempDir(), fmt.Sprintf("source-%d", source))
		tag, err := NewValueTag([]byte("feeds"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Create(sourcePaths[source], AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
			t.Fatal(err)
		}
		writer, err := OpenWriter(sourcePaths[source], algebraPropertyTransactionBudget())
		if err != nil {
			t.Fatal(err)
		}
		for feed := 0; feed < algebraPropertyFeeds; feed++ {
			algebraPropertyAddFeed(t, writer, fmt.Sprintf("f%d", feed), sourceModel[source][feed][:])
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	readers := make([]*ImmutableReader, algebraPropertySources)
	for source := 0; source < algebraPropertySources; source++ {
		reader, err := OpenImmutable(sourcePaths[source])
		if err != nil {
			t.Fatal(err)
		}
		readers[source] = reader
	}
	scopes := make([]*MembershipScope, algebraPropertySources)
	for source := 0; source < algebraPropertySources; source++ {
		query, err := readers[source].MembershipQuery()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := query.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 2 * 1024 * 1024}, cancellation)
		if err != nil {
			t.Fatal(err)
		}
		scopes[source] = scope
	}
	algebra, err := NewMembershipAlgebra(scopes, MembershipAlgebraBudget{
		MaxHeapBytes: 8 * 1024 * 1024,
		MaxSources:   algebraPropertySources,
	}, cancellation)
	if err != nil {
		t.Fatal(err)
	}

	unionIndexes := []int{0, 2, 4}
	intersectionIndexes := []int{1, 3, 5}
	excludeIndexes := []int{0, 5}
	unionNames := algebraPropertyNames(unionIndexes)
	intersectionNames := algebraPropertyNames(intersectionIndexes)
	excludeNames := algebraPropertyNames(excludeIndexes)
	expectedUnion := algebraPropertyAddressSet(&global, unionIndexes, func(present []bool, wanted []int) bool {
		for _, feed := range wanted {
			if present[feed] {
				return true
			}
		}
		return false
	})
	expectedRight := algebraPropertyAddressSet(&global, intersectionIndexes, func(present []bool, wanted []int) bool {
		for _, feed := range wanted {
			if present[feed] {
				return true
			}
		}
		return false
	})
	var expectedExclusion [algebraPropertyDomain]bool
	for address := 0; address < algebraPropertyDomain; address++ {
		anyUnion := false
		for _, feed := range unionIndexes {
			if global[feed][address] {
				anyUnion = true
				break
			}
		}
		anyExcluded := false
		for _, feed := range excludeIndexes {
			if global[feed][address] {
				anyExcluded = true
				break
			}
		}
		expectedExclusion[address] = anyUnion && !anyExcluded
	}

	count, err := algebra.Count(AlgebraFeedSelectionNamed(unionNames), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	expectedUnionCount := uint64(0)
	for _, present := range expectedUnion {
		if present {
			expectedUnionCount++
		}
	}
	if actual := count.Addresses.Lo(); actual != expectedUnionCount {
		t.Fatalf("count: addresses %d, want %d", actual, expectedUnionCount)
	}

	comparison, err := algebra.Compare(AlgebraFeedSelectionNamed(unionNames), AlgebraFeedSelectionNamed(intersectionNames), cancellation)
	if err != nil {
		t.Fatal(err)
	}
	expectedOverlap := uint64(0)
	expectedLeftOnly := uint64(0)
	expectedRightOnly := uint64(0)
	for address := 0; address < algebraPropertyDomain; address++ {
		switch {
		case expectedUnion[address] && expectedRight[address]:
			expectedOverlap++
		case expectedUnion[address]:
			expectedLeftOnly++
		case expectedRight[address]:
			expectedRightOnly++
		}
	}
	if actual := comparison.OverlapAddresses.Lo(); actual != expectedOverlap {
		t.Fatalf("overlap: addresses %d, want %d", actual, expectedOverlap)
	}
	if actual := comparison.LeftOnlyAddresses.Lo(); actual != expectedLeftOnly {
		t.Fatalf("left only: addresses %d, want %d", actual, expectedLeftOnly)
	}
	if actual := comparison.RightOnlyAddresses.Lo(); actual != expectedRightOnly {
		t.Fatalf("right only: addresses %d, want %d", actual, expectedRightOnly)
	}

	publishDir := t.TempDir()

	unionPath := filepath.Join(publishDir, "union")
	unionTag, err := NewValueTag([]byte("union"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := algebra.PublishSet(unionPath, unionTag, AlgebraSetUnion(AlgebraFeedSelectionNamed(unionNames)), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, algebraPropertyOutputBudget(), cancellation); err != nil {
		t.Fatal(err)
	}
	algebraPropertyAssertPreserved(t, unionPath, &global, unionIndexes, func(present []bool, wanted []int) bool {
		for _, feed := range wanted {
			if present[feed] {
				return true
			}
		}
		return false
	})

	intersectionPath := filepath.Join(publishDir, "intersection")
	intersectionTag, err := NewValueTag([]byte("intersection"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := algebra.PublishSet(intersectionPath, intersectionTag, AlgebraSetIntersection(AlgebraFeedSelectionNamed(intersectionNames)), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, algebraPropertyOutputBudget(), cancellation); err != nil {
		t.Fatal(err)
	}
	algebraPropertyAssertPreserved(t, intersectionPath, &global, intersectionIndexes, func(present []bool, wanted []int) bool {
		for _, feed := range wanted {
			if !present[feed] {
				return false
			}
		}
		return true
	})

	exclusionPath := filepath.Join(publishDir, "exclusion")
	exclusionTag, err := NewValueTag([]byte("exclusion"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := algebra.PublishSet(exclusionPath, exclusionTag, AlgebraSetExclusion(AlgebraFeedSelectionNamed(unionNames), AlgebraFeedSelectionNamed(excludeNames)), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, algebraPropertyOutputBudget(), cancellation); err != nil {
		t.Fatal(err)
	}
	exclusionReader, err := OpenImmutable(exclusionPath)
	if err != nil {
		t.Fatal(err)
	}
	for address := 0; address < algebraPropertyDomain; address++ {
		expected := make([]string, 0)
		if expectedExclusion[address] {
			for _, feed := range unionIndexes {
				if global[feed][address] {
					expected = append(expected, fmt.Sprintf("f%d", feed))
				}
			}
		}
		algebraPropertyAssertEqualNames(t, address, algebraPropertyMatching(t, exclusionReader, address), expected)
	}
	mustClose(t, exclusionReader)

	flatPath := filepath.Join(publishDir, "flat")
	flatTag, err := NewValueTag([]byte("flat"))
	if err != nil {
		t.Fatal(err)
	}
	flatMode, err := AlgebraOutputModeFlat("all")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := algebra.PublishSet(flatPath, flatTag, AlgebraSetUnion(AlgebraFeedSelectionAll()), flatMode, nil, PolicyFailIfExists, algebraPropertyOutputBudget(), cancellation); err != nil {
		t.Fatal(err)
	}
	flatReader, err := OpenImmutable(flatPath)
	if err != nil {
		t.Fatal(err)
	}
	for address := 0; address < algebraPropertyDomain; address++ {
		any := false
		for feed := 0; feed < algebraPropertyFeeds; feed++ {
			if global[feed][address] {
				any = true
				break
			}
		}
		expected := make([]string, 0)
		if any {
			expected = append(expected, "all")
		}
		algebraPropertyAssertEqualNames(t, address, algebraPropertyMatching(t, flatReader, address), expected)
	}
	mustClose(t, flatReader)

	// Mirror the Rust drop order: the algebra and its scopes are
	// released before the source readers close.
	algebra = nil
	scopes = nil
	for _, reader := range readers {
		mustClose(t, reader)
	}
}
