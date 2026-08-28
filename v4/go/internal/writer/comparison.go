// Workflow Comparison types (Rust workflow.rs Comparison +
// workflow/compare.rs ScannedComparison): allocation-free exact
// before/after statistics of one logical range-map change, shared by the
// feed merge and the workflow classification. The value-free report
// facade copies these values onto its public report types.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
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

// compareMaps4 sweeps two canonical IPv4 range maps of one store and
// returns the exact six-way address classification (Rust workflow/
// compare.rs maps over Ipv4Key).
func compareMaps4(store *DraftStore, base format.Meta, check func() error) (Comparison, error) {
	return compareMapsTyped(store, base, rangeCodec4{}, check)
}

// compareMaps6 sweeps two canonical IPv6 range maps of one store and
// returns the exact six-way address classification (Rust workflow/
// compare.rs maps over Ipv6Key).
func compareMaps6(store *DraftStore, base format.Meta, check func() error) (Comparison, error) {
	return compareMapsTyped(store, base, rangeCodec6{}, check)
}

// compareMapsTyped sweeps two canonical range maps of one store and
// returns the exact six-way address classification (Rust workflow/
// compare.rs maps): the base generation versus the draft generation.
// Both streams charge their fixed-tree range consumption like every
// other map scan (Rust Cursor::next charges range_consumed), and the
// checkpoint runs between every sweep step and inside every cell pair.
func compareMapsTyped[K any](store *DraftStore, base format.Meta, codec rangeFamily[K], check func() error) (Comparison, error) {
	oldCursor, err := newRangeCursor(store, base, codec, false)
	if err != nil {
		return Comparison{}, err
	}
	newCursor, err := newRangeCursor(store, store.draft.meta, codec, false)
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
			step, err := compareRangePair(codec, family, old, newValue, &result, check)
			if err != nil {
				return Comparison{}, err
			}
			switch step.left {
			case compareConsume:
				if err := advanceOld(); err != nil {
					return Comparison{}, err
				}
			case compareAfter:
				next, ok := codec.Next(step.leftEnd)
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
				next, ok := codec.Next(step.rightEnd)
				if !ok {
					return Comparison{}, overflow("range comparison cursor")
				}
				newValue.from = next
			}
		case oldOK:
			count, err := familyInclusiveCardinalityOf(codec, old.from, old.to)
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
			count, err := familyInclusiveCardinalityOf(codec, newValue.from, newValue.to)
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
type compareStep[K any] struct {
	left     compareStepKind
	right    compareStepKind
	leftEnd  K
	rightEnd K
}

// compareRangePair classifies one overlapping or separated pair of
// sweep records (Rust compare_pair / compare_overlap): the classification
// runs on inclusive key intervals, and the returned step describes how
// each side advances.
func compareRangePair[K any](rfamily rangeFamily[K], family uint8, left, right rangeRecord[K], result *Comparison, check func() error) (compareStep[K], error) {
	if rfamily.Less(left.to, right.from) {
		return stepLeftOnly(rfamily, family, left, result)
	}
	if rfamily.Less(right.to, left.from) {
		return stepRightOnly(rfamily, family, right, result)
	}
	leftValue, rightValue := left, right
	step := compareStep[K]{left: compareKeep, right: compareKeep}
	if rfamily.Less(leftValue.from, rightValue.from) {
		end, ok := rfamily.Previous(rightValue.from)
		if !ok {
			return compareStep[K]{}, overflow("range comparison prefix")
		}
		count, err := familyInclusiveCardinalityOf(rfamily, leftValue.from, end)
		if err != nil {
			return compareStep[K]{}, err
		}
		result.Before, err = addComparisonCount(result.Before, count)
		if err != nil {
			return compareStep[K]{}, err
		}
		result.Removed, err = addComparisonCount(result.Removed, count)
		if err != nil {
			return compareStep[K]{}, err
		}
		leftValue.from = rightValue.from
	} else if rfamily.Less(rightValue.from, leftValue.from) {
		end, ok := rfamily.Previous(leftValue.from)
		if !ok {
			return compareStep[K]{}, overflow("range comparison prefix")
		}
		count, err := familyInclusiveCardinalityOf(rfamily, rightValue.from, end)
		if err != nil {
			return compareStep[K]{}, err
		}
		result.After, err = addComparisonCount(result.After, count)
		if err != nil {
			return compareStep[K]{}, err
		}
		result.Added, err = addComparisonCount(result.Added, count)
		if err != nil {
			return compareStep[K]{}, err
		}
		rightValue.from = leftValue.from
	}
	end := leftValue.to
	if rfamily.Less(rightValue.to, end) {
		end = rightValue.to
	}
	count, err := familyInclusiveCardinalityOf(rfamily, leftValue.from, end)
	if err != nil {
		return compareStep[K]{}, err
	}
	result.Before, err = addComparisonCount(result.Before, count)
	if err != nil {
		return compareStep[K]{}, err
	}
	result.After, err = addComparisonCount(result.After, count)
	if err != nil {
		return compareStep[K]{}, err
	}
	if leftValue.value == rightValue.value {
		result.Unchanged, err = addComparisonCount(result.Unchanged, count)
	} else {
		result.Changed, err = addComparisonCount(result.Changed, count)
	}
	if err != nil {
		return compareStep[K]{}, err
	}
	if err := check(); err != nil {
		return compareStep[K]{}, err
	}
	if rfamily.Equal(leftValue.to, end) {
		step.left = compareConsume
	} else {
		step.left = compareAfter
		step.leftEnd = end
	}
	if rfamily.Equal(rightValue.to, end) {
		step.right = compareConsume
	} else {
		step.right = compareAfter
		step.rightEnd = end
	}
	return step, nil
}

// stepLeftOnly classifies one left-only record (Rust
// left_before_right: removed).
func stepLeftOnly[K any](rfamily rangeFamily[K], family uint8, left rangeRecord[K], result *Comparison) (compareStep[K], error) {
	count, err := familyInclusiveCardinalityOf(rfamily, left.from, left.to)
	if err != nil {
		return compareStep[K]{}, err
	}
	result.Before, err = addComparisonCount(result.Before, count)
	if err != nil {
		return compareStep[K]{}, err
	}
	result.Removed, err = addComparisonCount(result.Removed, count)
	if err != nil {
		return compareStep[K]{}, err
	}
	return compareStep[K]{left: compareConsume, right: compareKeep}, nil
}

// stepRightOnly classifies one right-only record (Rust
// right_before_left: added).
func stepRightOnly[K any](rfamily rangeFamily[K], family uint8, right rangeRecord[K], result *Comparison) (compareStep[K], error) {
	count, err := familyInclusiveCardinalityOf(rfamily, right.from, right.to)
	if err != nil {
		return compareStep[K]{}, err
	}
	result.After, err = addComparisonCount(result.After, count)
	if err != nil {
		return compareStep[K]{}, err
	}
	result.Added, err = addComparisonCount(result.Added, count)
	if err != nil {
		return compareStep[K]{}, err
	}
	return compareStep[K]{left: compareKeep, right: compareConsume}, nil
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
