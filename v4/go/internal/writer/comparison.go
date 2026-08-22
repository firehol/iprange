// Workflow comparison types (Rust workflow.rs Comparison +
// workflow/compare.rs ScannedComparison): allocation-free exact
// before/after statistics of one logical range-map change, shared by the
// feed merge and the workflow classification. The value-free report
// facade copies these values onto its public report types.

package writer

import "github.com/firehol/iprange/v4/go/internal/format"

// comparison is the exact six-way before/after classification of one
// change (Rust Comparison).
type comparison struct {
	before    format.Cardinality129
	after     format.Cardinality129
	unchanged format.Cardinality129
	changed   format.Cardinality129
	added     format.Cardinality129
	removed   format.Cardinality129
}

func (c comparison) equal(other comparison) bool {
	return c.before.Compare(other.before) == 0 &&
		c.after.Compare(other.after) == 0 &&
		c.unchanged.Compare(other.unchanged) == 0 &&
		c.changed.Compare(other.changed) == 0 &&
		c.added.Compare(other.added) == 0 &&
		c.removed.Compare(other.removed) == 0
}

// isZero reports the all-zero classification (Rust
// Cardinality129::ZERO equality).
func (c comparison) isZero() bool { return c.equal(comparison{}) }

// scannedComparison is the comparison plus the counted interval totals
// of the scanned before and after maps (Rust ScannedComparison).
type scannedComparison struct {
	comparison      comparison
	beforeIntervals uint64
	afterIntervals  uint64
}
