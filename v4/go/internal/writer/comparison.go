// Workflow Comparison types (Rust workflow.rs Comparison +
// workflow/compare.rs ScannedComparison): allocation-free exact
// before/after statistics of one logical range-map change, shared by the
// feed merge and the workflow classification. The value-free report
// facade copies these values onto its public report types.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// Comparison is the exact six-way before/after classification of one
// change (Rust Comparison). The fields carry the inclusive address
// totals of each class.
type Comparison struct {
	Before    format.Cardinality129
	After     format.Cardinality129
	Unchanged format.Cardinality129
	Changed   format.Cardinality129
	Added     format.Cardinality129
	Removed   format.Cardinality129
}

func (c Comparison) equal(other Comparison) bool {
	return c.Before.Compare(other.Before) == 0 &&
		c.After.Compare(other.After) == 0 &&
		c.Unchanged.Compare(other.Unchanged) == 0 &&
		c.Changed.Compare(other.Changed) == 0 &&
		c.Added.Compare(other.Added) == 0 &&
		c.Removed.Compare(other.Removed) == 0
}

// isZero reports the all-zero classification (Rust
// Cardinality129::ZERO equality).
func (c Comparison) isZero() bool { return c.equal(Comparison{}) }

// ScannedComparison is the Comparison plus the counted interval totals
// of the scanned before and after maps (Rust ScannedComparison).
type ScannedComparison struct {
	Comparison      Comparison
	BeforeIntervals uint64
	AfterIntervals  uint64
}

// compareMaps sweeps two canonical range maps of one store and returns
// the exact six-way address classification (Rust workflow/compare.rs
// maps): the base generation versus the draft generation. Both streams
// charge their fixed-tree range consumption like every other map scan
// (Rust Cursor::next charges range_consumed), and the checkpoint runs
// between every sweep step and inside every cell pair.
func compareMaps(store *DraftStore, base format.Meta, check func() error) (Comparison, error) {
	rfamily, err := store.rangeFamily()
	if err != nil {
		return Comparison{}, err
	}
	oldCursor, err := newRangeCursor(store, base, false)
	if err != nil {
		return Comparison{}, err
	}
	newCursor, err := newRangeCursor(store, store.draft.meta, false)
	if err != nil {
		return Comparison{}, err
	}
	old, oldOK, err := oldCursor.next()
	if err != nil {
		return Comparison{}, err
	}
	newValue, newOK, err := newCursor.next()
	if err != nil {
		return Comparison{}, err
	}
	result := Comparison{}
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
			return Comparison{}, err
		}
		switch {
		case oldOK && newOK:
			step, err := compareRangePair(rfamily, family, old, newValue, &result, check)
			if err != nil {
				return Comparison{}, err
			}
			switch step.left {
			case compareConsume:
				if err := advanceOld(); err != nil {
					return Comparison{}, err
				}
			case compareAfter:
				next, ok := rfamily.Next(step.leftEnd)
				if !ok {
					return Comparison{}, overflow("range comparison cursor")
				}
				old.from = next
			}
			switch step.right {
			case compareConsume:
				if err := advanceNew(); err != nil {
					return Comparison{}, err
				}
			case compareAfter:
				next, ok := rfamily.Next(step.rightEnd)
				if !ok {
					return Comparison{}, overflow("range comparison cursor")
				}
				newValue.from = next
			}
		case oldOK:
			count, err := familyInclusiveCardinality(family, old.from, old.to)
			if err != nil {
				return Comparison{}, err
			}
			result.Before, err = addComparisonCount(result.Before, count)
			if err != nil {
				return Comparison{}, err
			}
			result.Removed, err = addComparisonCount(result.Removed, count)
			if err != nil {
				return Comparison{}, err
			}
			if err := advanceOld(); err != nil {
				return Comparison{}, err
			}
		default:
			count, err := familyInclusiveCardinality(family, newValue.from, newValue.to)
			if err != nil {
				return Comparison{}, err
			}
			result.After, err = addComparisonCount(result.After, count)
			if err != nil {
				return Comparison{}, err
			}
			result.Added, err = addComparisonCount(result.Added, count)
			if err != nil {
				return Comparison{}, err
			}
			if err := advanceNew(); err != nil {
				return Comparison{}, err
			}
		}
	}
	if err := verifyComparison(result); err != nil {
		return Comparison{}, err
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
func compareRangePair(rfamily rangeFamily, family uint8, left, right rangeRecord, result *Comparison, check func() error) (compareStep, error) {
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
		result.Before, err = addComparisonCount(result.Before, count)
		if err != nil {
			return compareStep{}, err
		}
		result.Removed, err = addComparisonCount(result.Removed, count)
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
		result.After, err = addComparisonCount(result.After, count)
		if err != nil {
			return compareStep{}, err
		}
		result.Added, err = addComparisonCount(result.Added, count)
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
	result.Before, err = addComparisonCount(result.Before, count)
	if err != nil {
		return compareStep{}, err
	}
	result.After, err = addComparisonCount(result.After, count)
	if err != nil {
		return compareStep{}, err
	}
	if leftValue.value == rightValue.value {
		result.Unchanged, err = addComparisonCount(result.Unchanged, count)
	} else {
		result.Changed, err = addComparisonCount(result.Changed, count)
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
func stepLeftOnly(family uint8, left rangeRecord, result *Comparison) (compareStep, error) {
	count, err := familyInclusiveCardinality(family, left.from, left.to)
	if err != nil {
		return compareStep{}, err
	}
	result.Before, err = addComparisonCount(result.Before, count)
	if err != nil {
		return compareStep{}, err
	}
	result.Removed, err = addComparisonCount(result.Removed, count)
	if err != nil {
		return compareStep{}, err
	}
	return compareStep{left: compareConsume, right: compareKeep}, nil
}

// stepRightOnly classifies one right-only record (Rust
// right_before_left: added).
func stepRightOnly(family uint8, right rangeRecord, result *Comparison) (compareStep, error) {
	count, err := familyInclusiveCardinality(family, right.from, right.to)
	if err != nil {
		return compareStep{}, err
	}
	result.After, err = addComparisonCount(result.After, count)
	if err != nil {
		return compareStep{}, err
	}
	result.Added, err = addComparisonCount(result.Added, count)
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
func verifyComparison(result Comparison) error {
	unchangedChanged, err := addComparisonCount(result.Unchanged, result.Changed)
	if err != nil {
		return err
	}
	before, err := addComparisonCount(unchangedChanged, result.Removed)
	if err != nil {
		return err
	}
	after, err := addComparisonCount(unchangedChanged, result.Added)
	if err != nil {
		return err
	}
	if before.Compare(result.Before) != 0 || after.Compare(result.After) != 0 {
		return corrupt("workflow address classes do not balance")
	}
	return nil
}
