// Membership algebra tests (Rust membership_dictionary/algebra.rs +
// contains_indexes semantics): identity shortcuts, real combinations with
// canonical trailing-word counts, and the selected-index presence probe
// over one stored bitmap.

package writer

import (
	"errors"
	"slices"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// algebraWords builds one caller-owned bitmap from set bit positions.
func algebraWords(bits ...uint32) OutputWords {
	max := uint32(0)
	for _, bit := range bits {
		if word := bit/64 + 1; word > max {
			max = word
		}
	}
	words := make(OutputWords, max)
	for _, bit := range bits {
		words[bit/64] |= 1 << (bit % 64)
	}
	return words
}

// internAlgebraWords interns one bitmap and returns its ID and length.
func internAlgebraWords(t *testing.T, m *rangeMemoryStore, state *membershipState, bits ...uint32) uint32 {
	t.Helper()
	interned, err := internMembership(m, state, algebraWords(bits...))
	if err != nil {
		t.Fatal(err)
	}
	if interned.id == 0 {
		t.Fatal("interned bitmap got the empty ID")
	}
	return interned.id
}

// storedAlgebraWords reads one stored bitmap's words back through the
// located record read.
func storedAlgebraWords(t *testing.T, m *rangeMemoryStore, state *membershipState, id uint32) []uint64 {
	t.Helper()
	found, err := findMembership(m, state.idRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	words := make([]uint64, found.record.wordCount)
	if err := readFoundMembershipWords(m, found, 0, words); err != nil {
		t.Fatal(err)
	}
	return words
}

func newAlgebraState() (*rangeMemoryStore, *membershipState) {
	m := newRangeMemoryStore()
	return m, &membershipState{idRoot: 0, hashRoot: 0, usedRoot: 0, entryCount: 0, idLimit: 1}
}

// TestMembershipCombineIdentityShortcuts pins every Rust identity rule:
// replace picks the right operand, union/difference/intersection/xor
// collapse when one side is empty or both sides are the same ID, and no
// identity shortcut ever creates a record.
func TestMembershipCombineIdentityShortcuts(t *testing.T) {
	m, state := newAlgebraState()
	a := internAlgebraWords(t, m, state, 0, 64)
	b := internAlgebraWords(t, m, state, 1, 65)
	entryCount := state.entryCount

	cases := []struct {
		name        string
		op          membershipOperation
		left, right uint32
		wantID      uint32
	}{}
	_ = cases

	check := func(t *testing.T, op membershipOperation, left, right uint32, want uint32) {
		t.Helper()
		interned, err := combineMembership(m, state, left, right, storedWordsOf(t, m, state, right), op)
		if err != nil {
			t.Fatal(err)
		}
		if interned.id != want {
			t.Fatalf("op %v left %d right %d: id = %d, want %d", op, left, right, interned.id, want)
		}
		if interned.created {
			t.Fatalf("op %v created a record", op)
		}
	}

	// Replace picks the right operand unconditionally.
	check(t, membershipReplace, a, b, b)
	check(t, membershipReplace, 0, b, b)
	check(t, membershipReplace, a, 0, 0)
	// Union: empty picks the other side; equal IDs collapse.
	check(t, membershipUnion, 0, b, b)
	check(t, membershipUnion, a, 0, a)
	check(t, membershipUnion, a, a, a)
	// Difference: equal IDs and empty-left give the empty bitmap.
	check(t, membershipDifference, a, a, 0)
	check(t, membershipDifference, 0, b, 0)
	check(t, membershipDifference, a, 0, a)
	// Intersection: either empty gives the empty bitmap.
	check(t, membershipIntersection, a, 0, 0)
	check(t, membershipIntersection, 0, b, 0)
	check(t, membershipIntersection, a, a, a)
	// Xor: equal IDs and empty sides collapse.
	check(t, membershipXor, a, a, 0)
	check(t, membershipXor, 0, b, b)
	check(t, membershipXor, a, 0, a)

	if state.entryCount != entryCount {
		t.Fatalf("identity shortcuts changed the entry count: %d -> %d", entryCount, state.entryCount)
	}
}

func storedWordsOf(t *testing.T, m *rangeMemoryStore, state *membershipState, id uint32) uint32 {
	t.Helper()
	if id == 0 {
		return 0
	}
	found, err := findMembership(m, state.idRoot, id)
	if err != nil {
		t.Fatal(err)
	}
	return found.record.wordCount
}

// TestMembershipCombineRealCombinations interns real word combinations
// and pins the canonical trailing-word counts (Rust combine: raw count,
// canonical_count, then intern).
func TestMembershipCombineRealCombinations(t *testing.T) {
	m, state := newAlgebraState()
	// word0 bits {0,1} and {0,2}: every pairwise operation is a new
	// distinct record except the intersections that keep only bit 0.
	a := internAlgebraWords(t, m, state, 0, 1)
	b := internAlgebraWords(t, m, state, 0, 2)

	union, err := combineMembership(m, state, a, b, storedWordsOf(t, m, state, b), membershipUnion)
	if err != nil {
		t.Fatal(err)
	}
	if !union.created {
		t.Fatal("union was not created")
	}
	if got := storedAlgebraWords(t, m, state, union.id); !slices.Equal(got, []uint64{0b111}) {
		t.Fatalf("union words = %v, want [7]", got)
	}

	difference, err := combineMembership(m, state, a, b, storedWordsOf(t, m, state, b), membershipDifference)
	if err != nil {
		t.Fatal(err)
	}
	if got := storedAlgebraWords(t, m, state, difference.id); !slices.Equal(got, []uint64{0b010}) {
		t.Fatalf("difference words = %v, want [2]", got)
	}

	intersection, err := combineMembership(m, state, a, b, storedWordsOf(t, m, state, b), membershipIntersection)
	if err != nil {
		t.Fatal(err)
	}
	if got := storedAlgebraWords(t, m, state, intersection.id); !slices.Equal(got, []uint64{0b001}) {
		t.Fatalf("intersection words = %v, want [1]", got)
	}

	xor, err := combineMembership(m, state, a, b, storedWordsOf(t, m, state, b), membershipXor)
	if err != nil {
		t.Fatal(err)
	}
	if got := storedAlgebraWords(t, m, state, xor.id); !slices.Equal(got, []uint64{0b110}) {
		t.Fatalf("xor words = %v, want [6]", got)
	}

	// Canonical trailing zeros: the union with a bit at word 2 keeps
	// exactly three words even though the raw count is two.
	sparse := internAlgebraWords(t, m, state, 130)
	combined, err := combineMembership(m, state, sparse, a, storedWordsOf(t, m, state, a), membershipUnion)
	if err != nil {
		t.Fatal(err)
	}
	if combined.wordCount != 3 {
		t.Fatalf("canonical word count = %d, want 3", combined.wordCount)
	}
	// The union folds the one-word operand into the sparse three-word
	// bitmap at word 0 and drops the copied tail.
	words := storedAlgebraWords(t, m, state, combined.id)
	if len(words) != 3 || words[0] != 0b011 {
		t.Fatalf("canonical union words = %v, want [3 0 0]", words)
	}
}

// TestMembershipCombineStaleReference pins the require_words contract: a
// right operand whose advertised length does not match its stored record
// fails with the stale-reference class.
func TestMembershipCombineStaleReference(t *testing.T) {
	m, state := newAlgebraState()
	a := internAlgebraWords(t, m, state, 0, 1)
	if _, err := combineMembership(m, state, a, a, 5, membershipUnion); err == nil {
		t.Fatal("stale right operand accepted")
	} else if code := errCode(err); code != format.CodeStaleReference {
		t.Fatalf("code = %d, want StaleReference", code)
	}
}

// TestContainsMembershipIndexesProbesSelectedBits pins contains_indexes:
// the presence flags, the canonical-selection guards, the empty bitmap
// short-circuit, and the 4096-unit cancellation checkpoint.
func TestContainsMembershipIndexesProbesSelectedBits(t *testing.T) {
	m, state := newAlgebraState()
	id := internAlgebraWords(t, m, state, 0, 1, 65, 130)

	indexes := []uint32{0, 1, 2, 65, 130}
	output := make([]uint8, len(indexes))
	if err := containsMembershipIndexes(m, state.idRoot, id, indexes, output, nil); err != nil {
		t.Fatal(err)
	}
	want := []uint8{1, 1, 0, 1, 1}
	if !slices.Equal(output, want) {
		t.Fatalf("presence = %v, want %v", output, want)
	}

	// The empty bitmap short-circuits to all zeros.
	output = make([]uint8, 3)
	if err := containsMembershipIndexes(m, state.idRoot, 0, []uint32{0, 1, 2}, output, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(output, []uint8{0, 0, 0}) {
		t.Fatalf("empty presence = %v, want zeros", output)
	}

	// Non-canonical selections are refused before any read.
	if err := containsMembershipIndexes(m, state.idRoot, id, []uint32{1, 1}, make([]uint8, 2), nil); err == nil {
		t.Fatal("duplicate selection accepted")
	} else if code := errCode(err); code != format.CodeInvalidArgument {
		t.Fatalf("duplicate code = %d, want InvalidArgument", code)
	}
	if err := containsMembershipIndexes(m, state.idRoot, id, []uint32{2, 1}, make([]uint8, 2), nil); err == nil {
		t.Fatal("descending selection accepted")
	} else if code := errCode(err); code != format.CodeInvalidArgument {
		t.Fatalf("descending code = %d, want InvalidArgument", code)
	}
	if err := containsMembershipIndexes(m, state.idRoot, id, []uint32{0}, make([]uint8, 2), nil); err == nil {
		t.Fatal("length mismatch accepted")
	} else if code := errCode(err); code != format.CodeInvalidArgument {
		t.Fatalf("mismatch code = %d, want InvalidArgument", code)
	}

	// The 4096-unit cancellation cadence fires the caller checkpoint.
	cancelled := errors.New("cancelled")
	var large []uint32
	for index := uint32(0); index < 5000; index++ {
		large = append(large, index)
	}
	output = make([]uint8, len(large))
	if err := containsMembershipIndexes(m, state.idRoot, id, large, output, func() error { return cancelled }); !errors.Is(err, cancelled) {
		t.Fatalf("cancellation not surfaced: %v", err)
	}
}
