// Randomized structured-value property tests (Rust
// tests/structured_value_properties.rs parity): three xorshift seeds
// drive 24 rounds of profile assignments and clears over a 128-address
// network_enrichment_v1 domain; an independent per-address model
// derives the exact expected database after every round (per-address
// value plus threat mask, coalesced enrichment ranges, and per-feed
// boolean ranges). Profile 7 is the empty structure: the expected model
// is absent, so assigning it must behave exactly like a clear. The
// writer closes after every round before the live reader asserts
// the database; the Rust validate_clean checks are replaced by the
// public-reader cross-check (recorded decision).

package iprangedb

import (
	"fmt"
	"testing"
)

const structuredRounds = 24

var structuredSeeds = [...]uint64{
	0x0d492f18a73c65e1,
	0x8a1ed9305b74c26f,
	0xf67104ac39d8b52e,
}

var structuredFeeds = [...]string{"botnet", "scanner", "phishing", "proxy", "spam", "tor"}

// structuredExpected is one wanted per-address enrichment state: the
// value plus the threat mask (Rust Expected).
type structuredExpected struct {
	value NetworkEnrichmentV1
	mask  uint8
}

// structuredCell is one per-address model entry; present=false means
// absent (Rust Option<Expected>, None).
type structuredCell struct {
	value   NetworkEnrichmentV1
	mask    uint8
	present bool
}

// structuredModel is the independent per-address model (Rust
// [Option<Expected>; DOMAIN]).
type structuredModel [workflowDomain]structuredCell

// structuredRun is one coalesced wanted enrichment range.
type structuredRun struct {
	from, to int
	wanted   structuredExpected
}

// structuredSpan is one coalesced wanted boolean (per-feed) range.
type structuredSpan struct{ from, to int }

// structuredStructureCache is the per-round transaction structure cache
// (Rust [None; PROFILES.len() + 1]): profile 7 is the empty structure.
type structuredStructureCache [len(structuredProfiles) + 1]StructureRef

func structuredProfile(asn, countryID, stateID, cityID uint32, latitude, longitude int32, hasLocation bool, threatMask uint8) structuredExpected {
	return structuredExpected{
		value: NetworkEnrichmentV1{
			ASN:       asn,
			CountryID: countryID,
			StateID:   stateID,
			CityID:    cityID,
			Location: NetworkEnrichmentV1Location{
				LatitudeMicrodegrees:  latitude,
				LongitudeMicrodegrees: longitude,
			},
			HasLocation: hasLocation,
		},
		mask: threatMask,
	}
}

// structuredProfiles is the exact Rust PROFILES table.
var structuredProfiles = [7]structuredExpected{
	structuredProfile(64_512, 0, 0, 0, 0, 0, false, 0),
	structuredProfile(64_512, 0, 0, 0, 0, 0, false, 0b00_0001),
	structuredProfile(64_513, 1, 2, 3, 0, 0, true, 0b00_0011),
	structuredProfile(64_514, 8, 13, 21, -90_000_000, -180_000_000, true, 0b10_1010),
	structuredProfile(64_515, ^uint32(0), ^uint32(0)-1, ^uint32(0)-2, 90_000_000, 180_000_000, true, 0b11_1111),
	structuredProfile(0, 34, 55, 89, 0, 0, false, 0b01_0100),
	structuredProfile(0, 0, 0, 0, 0, 0, false, 0b00_0100),
}

// structuredContext names one database assertion for failure messages.
func structuredContext(seed uint64, round int) string {
	if round < 0 {
		return fmt.Sprintf("seed=%#018x round=start", seed)
	}
	return fmt.Sprintf("seed=%#018x round=%d", seed, round)
}

// structuredTransactionFeeds resolves the six named feeds in one
// transaction (Rust transaction_feeds).
func structuredTransactionFeeds(t *testing.T, tx *StructuredTransaction) []FeedRef {
	t.Helper()
	feeds := make([]FeedRef, 0, len(structuredFeeds))
	for _, name := range structuredFeeds {
		feed, found, err := tx.LookupFeed(feedName(t, name))
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("feed %s missing from the transaction catalog", name)
		}
		feeds = append(feeds, feed)
	}
	return feeds
}

// structuredMembership builds one transaction membership with the
// wanted mask bits set (Rust membership).
func structuredMembership(t *testing.T, tx *StructuredTransaction, feeds []FeedRef, mask uint8) MembershipRef {
	t.Helper()
	membership, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	for bit, feed := range feeds {
		if mask&(1<<bit) != 0 {
			membership, err = tx.AddFeed(membership, feed)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return membership
}

// structuredAssign interns profile (the empty structure when profile is
// the extra index past the profile table) and applies it to the
// inclusive range, filling the model exactly as the Rust assign does.
func structuredAssign(t *testing.T, tx *StructuredTransaction, feeds []FeedRef, structures *structuredStructureCache, from, to, profile int, model *structuredModel) {
	t.Helper()
	structure := structures[profile]
	if structure == (StructureRef{}) {
		var value NetworkEnrichmentV1
		var membership MembershipRef
		if profile < len(structuredProfiles) {
			wanted := structuredProfiles[profile]
			value = wanted.value
			if wanted.mask != 0 {
				membership = structuredMembership(t, tx, feeds, wanted.mask)
			}
		}
		interned, err := tx.InternNetworkEnrichmentV1(value, membership)
		if err != nil {
			t.Fatal(err)
		}
		structures[profile] = interned
		structure = interned
	}
	if _, err := tx.AssignV4(IPv4(from), IPv4(to), structure); err != nil {
		t.Fatal(err)
	}
	for index := from; index <= to; index++ {
		if profile < len(structuredProfiles) {
			wanted := structuredProfiles[profile]
			model[index] = structuredCell{value: wanted.value, mask: wanted.mask, present: true}
		} else {
			model[index] = structuredCell{}
		}
	}
}

// structuredClear clears the inclusive range in the transaction and the
// model (Rust clear).
func structuredClear(t *testing.T, tx *StructuredTransaction, from, to int, model *structuredModel) {
	t.Helper()
	if _, err := tx.ClearV4(IPv4(from), IPv4(to)); err != nil {
		t.Fatal(err)
	}
	for index := from; index <= to; index++ {
		model[index] = structuredCell{}
	}
}

// structuredRuns derives the coalesced enrichment runs from the model
// (Rust structured_runs): consecutive addresses with the same present
// value and mask merge into one run.
func structuredRuns(expected *structuredModel) []structuredRun {
	var runs []structuredRun
	index := 0
	for index < workflowDomain {
		if !expected[index].present {
			index++
			continue
		}
		from := index
		wanted := structuredExpected{value: expected[index].value, mask: expected[index].mask}
		for index+1 < workflowDomain && expected[index+1].present &&
			expected[index+1].value == wanted.value && expected[index+1].mask == wanted.mask {
			index++
		}
		runs = append(runs, structuredRun{from: from, to: index, wanted: wanted})
		index++
	}
	return runs
}

// structuredBooleanRuns derives the coalesced per-feed ranges from the
// model (Rust boolean_runs over one mask bit).
func structuredBooleanRuns(expected *structuredModel, bit int) []structuredSpan {
	present := func(index int) bool {
		return expected[index].present && expected[index].mask&(1<<bit) != 0
	}
	var runs []structuredSpan
	index := 0
	for index < workflowDomain {
		if !present(index) {
			index++
			continue
		}
		from := index
		for index+1 < workflowDomain && present(index+1) {
			index++
		}
		runs = append(runs, structuredSpan{from: from, to: index})
		index++
	}
	return runs
}

// assertStructuredMembership checks one view's threat membership
// against the wanted mask using the reader feed indexes (Rust
// assert_membership): mask zero wants no membership at all, otherwise
// every feed bit must match ContainsIndex.
func assertStructuredMembership(t *testing.T, view NetworkEnrichmentV1View, wanted uint8, feedIndexes []uint32, context string, address int) {
	t.Helper()
	actual, present, err := view.ThreatMembership()
	if err != nil {
		t.Fatal(err)
	}
	if wanted == 0 {
		if present {
			t.Fatalf("%s: address %d reports an unexpected threat membership", context, address)
		}
		return
	}
	if !present {
		t.Fatalf("%s: address %d reports no threat membership, want mask %#b", context, address, wanted)
	}
	for bit, index := range feedIndexes {
		contains, err := actual.ContainsIndex(index)
		if err != nil {
			t.Fatal(err)
		}
		if contains != (wanted&(1<<bit) != 0) {
			t.Fatalf("%s: address %d feed %d membership = %v, want %v", context, address, bit, contains, wanted&(1<<bit) != 0)
		}
	}
}

// assertStructuredRanges checks the enrichment range cursor against the
// coalesced model runs (Rust assert_structured_ranges).
func assertStructuredRanges(t *testing.T, r *LiveReader, expected *structuredModel, feedIndexes []uint32, context string) {
	t.Helper()
	wanted := structuredRuns(expected)
	cursor, err := r.NetworkEnrichmentV1CursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close()
	for _, run := range wanted {
		actual, ok, err := cursor.NextRange()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("%s: missing structured range %d..%d", context, run.from, run.to)
		}
		if actual.From != IPv4(run.from) || actual.To != IPv4(run.to) {
			t.Fatalf("%s: structured range = %d..%d, want %d..%d", context, actual.From, actual.To, run.from, run.to)
		}
		value, err := actual.Value.Value()
		if err != nil {
			t.Fatal(err)
		}
		if value != run.wanted.value {
			t.Fatalf("%s: structured range %d..%d value = %+v, want %+v", context, run.from, run.to, value, run.wanted.value)
		}
		assertStructuredMembership(t, actual.Value, run.wanted.mask, feedIndexes, context, run.from)
	}
	if _, ok, err := cursor.NextRange(); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatalf("%s: extra structured range", context)
	}
}

// assertStructuredFeedRanges checks every named feed projection against
// the model mask bits (Rust assert_feed_ranges).
func assertStructuredFeedRanges(t *testing.T, r *LiveReader, expected *structuredModel, context string) {
	t.Helper()
	for bit, name := range structuredFeeds {
		wanted := structuredBooleanRuns(expected, bit)
		cursor, err := r.FeedRangeCursorV4(name, RangeDirectionForward)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range wanted {
			actual, ok, err := cursor.NextRange()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatalf("%s: missing feed %s range %d..%d", context, name, run.from, run.to)
			}
			if actual.From != IPv4(run.from) || actual.To != IPv4(run.to) {
				t.Fatalf("%s: feed %s range = %d..%d, want %d..%d", context, name, actual.From, actual.To, run.from, run.to)
			}
		}
		if _, ok, err := cursor.NextRange(); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatalf("%s: extra feed %s range", context, name)
		}
	}
}

// assertStructuredDatabase checks every address, the enrichment range
// cursor, and every feed projection through the immutable reader
// against the model (Rust assert_database; the validate_clean call is
// replaced by this public-reader cross-check).
func assertStructuredDatabase(t *testing.T, path string, expected *structuredModel, context string) {
	t.Helper()
	r, err := OpenLiveReader(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	pin, err := r.Pin()
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	feedIndexes := make([]uint32, len(structuredFeeds))
	for index, name := range structuredFeeds {
		entry, found, err := r.LookupFeed(name)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("%s: feed %s missing from the reader catalog", context, name)
		}
		feedIndexes[index] = entry.Index
	}
	for address := 0; address < workflowDomain; address++ {
		view, found, err := pin.LookupNetworkEnrichmentV1V4(IPv4(address))
		if err != nil {
			t.Fatal(err)
		}
		wanted := expected[address]
		switch {
		case !wanted.present && !found:
		case wanted.present && found:
			actual, err := view.Value()
			if err != nil {
				t.Fatal(err)
			}
			if actual != wanted.value {
				t.Fatalf("%s: address %d value = %+v, want %+v", context, address, actual, wanted.value)
			}
			assertStructuredMembership(t, view, wanted.mask, feedIndexes, context, address)
		case wanted.present && !found:
			t.Fatalf("%s: address %d is missing, want %+v", context, address, wanted.value)
		default:
			t.Fatalf("%s: address %d is present but the model expects absence", context, address)
		}
	}
	assertStructuredRanges(t, r, expected, feedIndexes, context)
	assertStructuredFeedRanges(t, r, expected, context)
}

// TestRandomizedStructuredTransactionsMatchIndependentAddressModel
// mirrors Rust
// randomized_structured_transactions_match_independent_address_model:
// every seed runs the same 24-round schedule of broad assignment,
// narrow assignment, clear, and randomized extra operations with the
// abort/commit cadence (round+seedIndex)%4==3.
func TestRandomizedStructuredTransactionsMatchIndependentAddressModel(t *testing.T) {
	requireLiveCreation(t)
	for seedIndex, seed := range structuredSeeds {
		runStructuredSeed(t, seedIndex, seed)
	}
}

func runStructuredSeed(t *testing.T, seedIndex int, seed uint64) {
	t.Helper()
	path := structuredDB(t)

	// Create the six feeds in one structured transaction at the start
	// (Rust create_feeds), then assert the still-empty database.
	w, err := OpenLiveWriter(path, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginStructuredTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range structuredFeeds {
		if _, err := tx.EnsureFeed(feedName(t, name)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("seed=%#018x feed creation commit = %v, want committed", seed, result.Status)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var committed structuredModel
	assertStructuredDatabase(t, path, &committed, structuredContext(seed, -1))

	var random workflowRandom
	random.state = seed
	for round := 0; round < structuredRounds; round++ {
		draft := committed
		w, err := OpenLiveWriter(path, DefaultBudget(), nil)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := w.BeginStructuredTransaction(NewCancellationToken())
		if err != nil {
			t.Fatal(err)
		}
		feeds := structuredTransactionFeeds(t, tx)
		var structures structuredStructureCache

		// broad is the first profile differing from the committed
		// address 0 (Rust PROFILES.iter().position).
		broad := 0
		for index := range structuredProfiles {
			wanted := structuredCell{value: structuredProfiles[index].value, mask: structuredProfiles[index].mask, present: true}
			if committed[0] != wanted {
				broad = index
				break
			}
		}
		structuredAssign(t, tx, feeds, &structures, 0, workflowDomain-1, broad, &draft)
		structuredAssign(t, tx, feeds, &structures, workflowDomain/4, workflowDomain*3/4-1, (broad+1)%len(structuredProfiles), &draft)
		structuredClear(t, tx, workflowDomain/2-8, workflowDomain/2+7, &draft)

		extraOperations := 12 + int(random.below(21))
		for index := 0; index < extraOperations; index++ {
			from, to := random.span()
			switch random.below(5) {
			case 0:
				structuredClear(t, tx, from, to, &draft)
			case 1:
				structuredAssign(t, tx, feeds, &structures, from, to, len(structuredProfiles), &draft)
			default:
				profile := int(random.below(uint32(len(structuredProfiles))))
				structuredAssign(t, tx, feeds, &structures, from, to, profile, &draft)
			}
		}

		if (round+seedIndex)%4 == 3 {
			if err := tx.Abort(); err != nil {
				t.Fatal(err)
			}
		} else {
			result, err := tx.Commit()
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != CommitCommitted {
				t.Fatalf("%s: commit = %v (%v), want committed", structuredContext(seed, round), result.Status, result.Cause)
			}
			committed = draft
		}
		if _, err := w.Close(); err != nil {
			t.Fatal(err)
		}
		assertStructuredDatabase(t, path, &committed, structuredContext(seed, round))
	}
}
