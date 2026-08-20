package reader

// Ordered direct-range cursors with seek (Rust range_cursor.rs parity).
// The cursor walks the range tree through the shared treeCursor frames;
// seek repositions to the containing range or the nearest range in the
// selected direction (RangeSeek), and next returns ranges in ascending
// (forward) or descending (backward) key order.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// RangeDirection selects the cursor movement direction.
type RangeDirection uint8

const (
	RangeForward  RangeDirection = 0
	RangeBackward RangeDirection = 1
)

// DirectRange4 is one inclusive direct-value interval.
type DirectRange4 struct {
	From, To, Value uint32
}

// DirectRange6 is one inclusive direct-value interval.
type DirectRange6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	Value                      uint32
}

func (r *ImmutableReader) NewDirectCursor4(direction RangeDirection) (*DirectCursor4, error) {
	state, err := r.newTreeCursor(r.meta.RangeRoot, cursorDir(direction), format.PageTypeRangeBranch, format.PageTypeRangeLeaf, uint32(r.meta.AddressFamily))
	if err != nil {
		return nil, err
	}
	return &DirectCursor4{state: state}, nil
}

func (r *ImmutableReader) NewDirectCursor6(direction RangeDirection) (*DirectCursor6, error) {
	state, err := r.newTreeCursor(r.meta.RangeRoot, cursorDir(direction), format.PageTypeRangeBranch, format.PageTypeRangeLeaf, uint32(r.meta.AddressFamily))
	if err != nil {
		return nil, err
	}
	return &DirectCursor6{state: state}, nil
}

func decodeRangeBranch4(b []byte) (uint32, error) {
	_, child, err := format.DecodeRangeEntryV4(b)
	return child, err
}

func decodeRangeBranch6(b []byte) (uint32, error) {
	_, _, child, err := format.DecodeRangeEntryV6(b)
	return child, err
}

// rangeBranchSeek4 selects the branch child for one seek step, mirroring
// Rust fixed_tree::Cursor::seek_inner: greatest first-key <= target, with
// a forward seek below the first key descending into the first child and
// a backward seek below the first key finishing. The selected child is
// validated (Rust branch_child/require_child: 2 <= child < page_limit).
func rangeBranchSeek4(sl format.SlottedPage, target uint32, dir cursorDir, pageLimit uint64) (int, uint32, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		if len(b) < 4 {
			return 0, corrupt("short range branch record %d", len(b))
		}
		return cmpU32(format.U32(b[0:4]), target), nil
	}
	position, exact, err := lowerBoundPosition(int(sl.Header.ItemCount), cmp)
	if err != nil {
		return 0, 0, false, err
	}
	index := position
	if !exact {
		index--
	}
	if index < 0 {
		if dir == cursorBackward {
			return 0, 0, true, nil // nothing at or below the target
		}
		index = 0 // forward seek below the first key: first child
	}
	b, err := sl.Record(index)
	if err != nil {
		return 0, 0, false, err
	}
	child, err := decodeRangeBranch4(b)
	if err != nil {
		return 0, 0, false, err
	}
	if !format.PageNumberValid(child, pageLimit) {
		return 0, 0, false, corrupt("range tree branch child page %d is invalid", child)
	}
	return index, child, false, nil
}

// rangeBranchSeek6 is the IPv6 twin of rangeBranchSeek4.
func rangeBranchSeek6(sl format.SlottedPage, targetHi, targetLo uint64, dir cursorDir, pageLimit uint64) (int, uint32, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		if len(b) < 16 {
			return 0, corrupt("short range branch record %d", len(b))
		}
		hi, lo := format.U128(b[0:16])
		return cmpU128(hi, lo, targetHi, targetLo), nil
	}
	position, exact, err := lowerBoundPosition(int(sl.Header.ItemCount), cmp)
	if err != nil {
		return 0, 0, false, err
	}
	index := position
	if !exact {
		index--
	}
	if index < 0 {
		if dir == cursorBackward {
			return 0, 0, true, nil
		}
		index = 0
	}
	b, err := sl.Record(index)
	if err != nil {
		return 0, 0, false, err
	}
	child, err := decodeRangeBranch6(b)
	if err != nil {
		return 0, 0, false, err
	}
	if !format.PageNumberValid(child, pageLimit) {
		return 0, 0, false, corrupt("range tree branch child page %d is invalid", child)
	}
	return index, child, false, nil
}

// rangeLeafSeek4 implements the Rust RangeSeek leaf policy for IPv4:
// forward returns the containing range or the next range at or after the
// target (or crosses to the next leaf), backward the containing range or
// the greatest range strictly below the target.
func rangeLeafSeek4(sl format.SlottedPage, target uint32, dir cursorDir) (int, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		from, err := format.RangeRecordKeyV4(b)
		if err != nil {
			return 0, err
		}
		return cmpU32(from, target), nil
	}
	position, exact, err := lowerBoundPosition(int(sl.Header.ItemCount), cmp)
	if err != nil {
		return 0, false, err
	}
	if dir == cursorBackward {
		if exact {
			return position, false, nil
		}
		if position == 0 {
			return 0, true, nil // finished: nothing strictly below
		}
		return position - 1, false, nil
	}
	if exact {
		return position, false, nil
	}
	if position == 0 {
		return 0, false, nil // key below the first record: start at first
	}
	rec, err := rangeRecordAt4(sl, position-1)
	if err != nil {
		return 0, false, err
	}
	if target <= rec.To {
		return position - 1, false, nil
	}
	// No further record in this leaf: the traversal crosses to the next
	// leaf (NextLeaf). Report position == itemCount so the seek caller
	// advances into the sibling leaf.
	return position, false, nil
}

// rangeLeafSeek6 is the IPv6 twin of rangeLeafSeek4.
func rangeLeafSeek6(sl format.SlottedPage, targetHi, targetLo uint64, dir cursorDir) (int, bool, error) {
	cmp := func(i int) (int, error) {
		b, err := sl.Record(i)
		if err != nil {
			return 0, err
		}
		hi, lo, err := format.RangeRecordKeyV6(b)
		if err != nil {
			return 0, err
		}
		return cmpU128(hi, lo, targetHi, targetLo), nil
	}
	position, exact, err := lowerBoundPosition(int(sl.Header.ItemCount), cmp)
	if err != nil {
		return 0, false, err
	}
	if dir == cursorBackward {
		if exact {
			return position, false, nil
		}
		if position == 0 {
			return 0, true, nil
		}
		return position - 1, false, nil
	}
	if exact {
		return position, false, nil
	}
	if position == 0 {
		return 0, false, nil
	}
	rec, err := rangeRecordAt6(sl, position-1)
	if err != nil {
		return 0, false, err
	}
	if cmpU128(targetHi, targetLo, rec.ToHi, rec.ToLo) <= 0 {
		return position - 1, false, nil
	}
	return position, false, nil
}

// rangeRecordAt4 decodes one leaf record at position (exact cell view).
func rangeRecordAt4(sl format.SlottedPage, index int) (format.RangeRecordV4, error) {
	b, err := sl.Record(index)
	if err != nil {
		return format.RangeRecordV4{}, err
	}
	b, err = exactRecordView(b, format.RangeRecordV4Size)
	if err != nil {
		return format.RangeRecordV4{}, err
	}
	work.LeafValidation(1)
	return format.DecodeRangeRecordV4(b)
}

// rangeRecordAt6 decodes one leaf record at position (exact cell view).
func rangeRecordAt6(sl format.SlottedPage, index int) (format.RangeRecordV6, error) {
	b, err := sl.Record(index)
	if err != nil {
		return format.RangeRecordV6{}, err
	}
	b, err = exactRecordView(b, format.RangeRecordV6Size)
	if err != nil {
		return format.RangeRecordV6{}, err
	}
	work.LeafValidation(1)
	return format.DecodeRangeRecordV6(b)
}

// lowerBoundPosition finds the first record whose key is >= target
// (position) and whether its key equals the target, mirroring Rust
// fixed_tree::lower_bound. Records before position have keys < target.
func lowerBoundPosition(itemCount int, cmp func(i int) (int, error)) (int, bool, error) {
	lo, hi := 0, itemCount
	for lo < hi {
		mid := (lo + hi) / 2
		c, err := cmp(mid)
		if err != nil {
			return 0, false, err
		}
		if c < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= itemCount {
		return lo, false, nil
	}
	c, err := cmp(lo)
	if err != nil {
		return 0, false, err
	}
	return lo, c == 0, nil
}

// DirectCursor4 is one ordered IPv4 range cursor.
type DirectCursor4 struct {
	state *treeCursor
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor (Rust
// CursorState.seek parity).
func (c *DirectCursor4) Seek(target uint32) error {
	c.state.seek4 = target
	if err := c.state.seekPosition(); err != nil {
		return err
	}
	return c.afterSeek()
}

// afterSeek resolves a NextLeaf policy result: when the leaf policy
// selected a position past the leaf end, advance into the sibling leaf.
func (c *DirectCursor4) afterSeek() error {
	if c.state.finished {
		return nil
	}
	if c.state.index >= int(c.state.itemCount) {
		// Move one past the leaf end; advance crosses to the sibling.
		c.state.index = int(c.state.itemCount) - 1
		if _, _, err := c.state.advance(); err != nil {
			return err
		}
	}
	return nil
}

// Next returns the next range in the cursor's direction; ok reports
// whether a range was produced.
func (c *DirectCursor4) Next() (DirectRange4, bool, error) {
	if c.state.finished {
		return DirectRange4{}, false, nil
	}
	sl, _, err := c.state.openLeaf()
	if err != nil {
		return DirectRange4{}, false, err
	}
	rec, err := rangeRecordAt4(sl, c.state.index)
	if err != nil {
		return DirectRange4{}, false, err
	}
	work.RangeConsumed(1)
	if _, _, err := c.state.advance(); err != nil {
		return DirectRange4{}, false, err
	}
	return DirectRange4{From: rec.From, To: rec.To, Value: rec.Value}, true, nil
}

// DirectCursor6 is one ordered IPv6 range cursor.
type DirectCursor6 struct {
	state *treeCursor
}

// Seek repositions to the containing range or the nearest range in the
// cursor's direction. Seeks are repeatable on an exhausted cursor (Rust
// CursorState.seek parity).
func (c *DirectCursor6) Seek(targetHi, targetLo uint64) error {
	c.state.seekHi = targetHi
	c.state.seekLo = targetLo
	if err := c.state.seekPosition(); err != nil {
		return err
	}
	if c.state.finished {
		return nil
	}
	if c.state.index >= int(c.state.itemCount) {
		c.state.index = int(c.state.itemCount) - 1
		if _, _, err := c.state.advance(); err != nil {
			return err
		}
	}
	return nil
}

// Next returns the next range in the cursor's direction; ok reports
// whether a range was produced.
func (c *DirectCursor6) Next() (DirectRange6, bool, error) {
	if c.state.finished {
		return DirectRange6{}, false, nil
	}
	sl, _, err := c.state.openLeaf()
	if err != nil {
		return DirectRange6{}, false, err
	}
	rec, err := rangeRecordAt6(sl, c.state.index)
	if err != nil {
		return DirectRange6{}, false, err
	}
	work.RangeConsumed(1)
	if _, _, err := c.state.advance(); err != nil {
		return DirectRange6{}, false, err
	}
	return DirectRange6{FromHi: rec.FromHi, FromLo: rec.FromLo, ToHi: rec.ToHi, ToLo: rec.ToLo, Value: rec.Value}, true, nil
}
