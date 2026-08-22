// Workflow comparison types (Rust workflow.rs Comparison +
// workflow/compare.rs ScannedComparison): allocation-free exact
// before/after statistics of one logical range-map change, shared by the
// feed merge and the workflow classification. The value-free report
// facade copies these values onto its public report types.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

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

// compareMaps sweeps two canonical range maps of one store and returns
// the exact six-way address classification (Rust workflow/compare.rs
// maps): the base generation versus the draft generation. Both streams
// charge their fixed-tree range consumption like every other map scan
// (Rust Cursor::next charges range_consumed), and the checkpoint runs
// between every sweep step and inside every cell pair.
func compareMaps(store *DraftStore, base format.Meta, check func() error) (comparison, error) {
	rfamily, err := store.rangeFamily()
	if err != nil {
		return comparison{}, err
	}
	oldCursor, err := newRangeCursor(store, base, false)
	if err != nil {
		return comparison{}, err
	}
	newCursor, err := newRangeCursor(store, store.draft.meta, false)
	if err != nil {
		return comparison{}, err
	}
	old, oldOK, err := oldCursor.next()
	if err != nil {
		return comparison{}, err
	}
	newValue, newOK, err := newCursor.next()
	if err != nil {
		return comparison{}, err
	}
	result := comparison{}
	family := base.AddressFamily
	advanceOld := func() error {
		old, oldOK, err = oldCursor.next()
		return err
	}
	advanceNew := func() error {
		newValue, newOK, err = newCursor.next()
		return err
	}
	for oldOK || newOK {
		if err := check(); err != nil {
			return comparison{}, err
		}
		switch {
		case oldOK && newOK:
			step, err := compareRangePair(rfamily, family, old, newValue, &result, check)
			if err != nil {
				return comparison{}, err
			}
			switch step.left {
			case compareConsume:
				if err := advanceOld(); err != nil {
					return comparison{}, err
				}
			case compareAfter:
				next, ok := rfamily.Next(step.leftEnd)
				if !ok {
					return comparison{}, overflow("range comparison cursor")
				}
				old.from = next
			}
			switch step.right {
			case compareConsume:
				if err := advanceNew(); err != nil {
					return comparison{}, err
				}
			case compareAfter:
				next, ok := rfamily.Next(step.rightEnd)
				if !ok {
					return comparison{}, overflow("range comparison cursor")
				}
				newValue.from = next
			}
		case oldOK:
			count, err := familyInclusiveCardinality(family, old.from, old.to)
			if err != nil {
				return comparison{}, err
			}
			result.before, err = addComparisonCount(result.before, count)
			if err != nil {
				return comparison{}, err
			}
			result.removed, err = addComparisonCount(result.removed, count)
			if err != nil {
				return comparison{}, err
			}
			if err := advanceOld(); err != nil {
				return comparison{}, err
			}
		default:
			count, err := familyInclusiveCardinality(family, newValue.from, newValue.to)
			if err != nil {
				return comparison{}, err
			}
			result.after, err = addComparisonCount(result.after, count)
			if err != nil {
				return comparison{}, err
			}
			result.added, err = addComparisonCount(result.added, count)
			if err != nil {
				return comparison{}, err
			}
			if err := advanceNew(); err != nil {
				return comparison{}, err
			}
		}
	}
	if err := verifyComparison(result); err != nil {
		return comparison{}, err
	}
	return result, nil
}

// compareStepKind is the left/right cursor action of one sweep step
// (Rust Advance: Keep / Consume / After).
type compareStepKind uint8

const (
	compareKeep compareStepKind = iota
	compareConsume
	compareAfter
)

// compareStep is the disposition of one overlap step (Rust Step).
type compareStep struct {
	left     compareStepKind
	right    compareStepKind
	leftEnd  tree.Key
	rightEnd tree.Key
}

// compareRangePair classifies one overlapping or separated pair of
// sweep records (Rust compare_pair / compare_overlap): the classification
// runs on inclusive key intervals, and the returned step describes how
// each side advances.
func compareRangePair(rfamily rangeFamily, family uint8, left, right rangeRecord, result *comparison, check func() error) (compareStep, error) {
	if left.to.Less(right.from) {
		return stepLeftOnly(family, left, result)
	}
	if right.to.Less(left.from) {
		return stepRightOnly(family, right, result)
	}
	leftValue, rightValue := left, right
	step := compareStep{left: compareKeep, right: compareKeep}
	if leftValue.from.Less(rightValue.from) {
		end, ok := rfamily.Previous(rightValue.from)
		if !ok {
			return compareStep{}, overflow("range comparison prefix")
		}
		count, err := familyInclusiveCardinality(family, leftValue.from, end)
		if err != nil {
			return compareStep{}, err
		}
		result.before, err = addComparisonCount(result.before, count)
		if err != nil {
			return compareStep{}, err
		}
		result.removed, err = addComparisonCount(result.removed, count)
		if err != nil {
			return compareStep{}, err
		}
		leftValue.from = rightValue.from
	} else if rightValue.from.Less(leftValue.from) {
		end, ok := rfamily.Previous(leftValue.from)
		if !ok {
			return compareStep{}, overflow("range comparison prefix")
		}
		count, err := familyInclusiveCardinality(family, rightValue.from, end)
		if err != nil {
			return compareStep{}, err
		}
		result.after, err = addComparisonCount(result.after, count)
		if err != nil {
			return compareStep{}, err
		}
		result.added, err = addComparisonCount(result.added, count)
		if err != nil {
			return compareStep{}, err
		}
		rightValue.from = leftValue.from
	}
	end := leftValue.to
	if rightValue.to.Less(end) {
		end = rightValue.to
	}
	count, err := familyInclusiveCardinality(family, leftValue.from, end)
	if err != nil {
		return compareStep{}, err
	}
	result.before, err = addComparisonCount(result.before, count)
	if err != nil {
		return compareStep{}, err
	}
	result.after, err = addComparisonCount(result.after, count)
	if err != nil {
		return compareStep{}, err
	}
	if leftValue.value == rightValue.value {
		result.unchanged, err = addComparisonCount(result.unchanged, count)
	} else {
		result.changed, err = addComparisonCount(result.changed, count)
	}
	if err != nil {
		return compareStep{}, err
	}
	if err := check(); err != nil {
		return compareStep{}, err
	}
	if leftValue.to.Equal(end) {
		step.left = compareConsume
	} else {
		step.left = compareAfter
		step.leftEnd = end
	}
	if rightValue.to.Equal(end) {
		step.right = compareConsume
	} else {
		step.right = compareAfter
		step.rightEnd = end
	}
	return step, nil
}

// stepLeftOnly classifies one left-only record (Rust
// left_before_right: removed).
func stepLeftOnly(family uint8, left rangeRecord, result *comparison) (compareStep, error) {
	count, err := familyInclusiveCardinality(family, left.from, left.to)
	if err != nil {
		return compareStep{}, err
	}
	result.before, err = addComparisonCount(result.before, count)
	if err != nil {
		return compareStep{}, err
	}
	result.removed, err = addComparisonCount(result.removed, count)
	if err != nil {
		return compareStep{}, err
	}
	return compareStep{left: compareConsume, right: compareKeep}, nil
}

// stepRightOnly classifies one right-only record (Rust
// right_before_left: added).
func stepRightOnly(family uint8, right rangeRecord, result *comparison) (compareStep, error) {
	count, err := familyInclusiveCardinality(family, right.from, right.to)
	if err != nil {
		return compareStep{}, err
	}
	result.after, err = addComparisonCount(result.after, count)
	if err != nil {
		return compareStep{}, err
	}
	result.added, err = addComparisonCount(result.added, count)
	if err != nil {
		return compareStep{}, err
	}
	return compareStep{left: compareKeep, right: compareConsume}, nil
}

// addComparisonCount adds one address count with overflow check (Rust
// add: checked_add with the workflow address count class).
func addComparisonCount(left, right format.Cardinality129) (format.Cardinality129, error) {
	value, err := left.Add(right)
	if err != nil {
		return format.Cardinality129{}, overflow("workflow address count")
	}
	return value, nil
}

// verifyComparison checks the six classes balance (Rust verify).
func verifyComparison(result comparison) error {
	unchangedChanged, err := addComparisonCount(result.unchanged, result.changed)
	if err != nil {
		return err
	}
	before, err := addComparisonCount(unchangedChanged, result.removed)
	if err != nil {
		return err
	}
	after, err := addComparisonCount(unchangedChanged, result.added)
	if err != nil {
		return err
	}
	if before.Compare(result.before) != 0 || after.Compare(result.after) != 0 {
		return corrupt("workflow address classes do not balance")
	}
	return nil
}
