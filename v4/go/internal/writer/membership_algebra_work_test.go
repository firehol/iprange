//go:build v4work

// Necessary-work pins for the membership algebra (Rust work::measure
// parity for membership_combination, membership_lookup on combinable
// operands, and the single locate of contains_indexes).

package writer

import (
	"slices"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestMembershipCombineWorkPins pins the Rust counter contract: every
// combine counts one membership_combination, identity shortcuts intern
// nothing, and a real combination interns once after two operand
// lookups.
func TestMembershipCombineWorkPins(t *testing.T) {
	m, state := newAlgebraState()
	a := internAlgebraWords(t, m, state, 0, 64)
	b := internAlgebraWords(t, m, state, 1, 65)

	work.Reset()
	if _, err := combineMembership(m, state, a, a, storedWordsOf(t, m, state, a), membershipUnion); err != nil {
		t.Fatal(err)
	}
	if work.Read().MembershipCombinations != 1 {
		t.Fatalf("identity combine combinations = %d, want 1", work.Read().MembershipCombinations)
	}
	if work.Read().MembershipInterns != 0 {
		t.Fatalf("identity combine interned: %d", work.Read().MembershipInterns)
	}

	work.Reset()
	if _, err := combineMembership(m, state, a, b, storedWordsOf(t, m, state, b), membershipUnion); err != nil {
		t.Fatal(err)
	}
	snapshot := work.Read()
	if snapshot.MembershipCombinations != 1 {
		t.Fatalf("real combine combinations = %d, want 1", snapshot.MembershipCombinations)
	}
	if snapshot.MembershipInterns != 1 {
		t.Fatalf("real combine interns = %d, want 1", snapshot.MembershipInterns)
	}
	if snapshot.MembershipLookups < 2 {
		t.Fatalf("real combine lookups = %d, want >= 2 (both operands)", snapshot.MembershipLookups)
	}
}

// TestContainsMembershipIndexesWorkPin pins the single locate: the probe
// finds one record and serves every selected word from it (Rust
// contains_indexes find_record parity).
func TestContainsMembershipIndexesWorkPin(t *testing.T) {
	m, state := newAlgebraState()
	id := internAlgebraWords(t, m, state, 0, 65)
	indexes := []uint32{0, 1, 2, 65}
	output := make([]uint8, len(indexes))
	work.Reset()
	if err := containsMembershipIndexes(m, state.idRoot, id, indexes, output, nil); err != nil {
		t.Fatal(err)
	}
	snapshot := work.Read()
	if snapshot.MembershipLookups != 1 {
		t.Fatalf("probe lookups = %d, want 1", snapshot.MembershipLookups)
	}
	if !slices.Equal(output, []uint8{1, 0, 0, 1}) {
		t.Fatalf("probe output = %v", output)
	}
}
