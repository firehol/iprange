// Membership combine allocation pin (SOW-0025 M3 milestone
// confirmation): a real non-shortcut combination must allocate nothing
// per call, like the Rust Combined stack value
// (membership_dictionary/algebra.rs). The identity shortcut returns
// before any allocation; the miss path (two distinct non-empty
// bitmaps) previously escaped one &combinedWords per call through the
// shape-stenciled generic dispatch. The draft now owns the combine
// operand (draft_store.go combineScratch), so every measured call
// reuses it.

package writer

import "testing"

// TestMembershipCombineZeroAlloc pins the miss path of
// combineMembership (union of two distinct non-empty stored bitmaps):
// the seeded records exist before the loop, the warmup run inserts the
// union record, and the measured runs take the combine -> canonical
// count -> intern-dedup path with zero allocations per call.
func TestMembershipCombineZeroAlloc(t *testing.T) {
	m, state := newAlgebraState()
	a := internAlgebraWords(t, m, state, 0, 64)
	b := internAlgebraWords(t, m, state, 1, 65)
	rightWords := storedWordsOf(t, m, state, b)
	// The union of two distinct non-empty bitmaps never hits the
	// identity shortcut, so every measured call exercises the scratch
	// path.
	allocs := testing.AllocsPerRun(200, func() {
		if _, err := combineMembership(m, state, &combineScratch, a, b, rightWords, MembershipUnion); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("membership combine miss-path allocations per call: %.0f", allocs)
	if allocs != 0 {
		t.Fatalf("membership combine miss path allocates %.0f objects per call, contract is exactly zero", allocs)
	}
}
