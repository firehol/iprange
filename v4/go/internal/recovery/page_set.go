package recovery

// Recovery page-ownership set (Rust recovery/page_set.rs, heap-only
// arm): one sparse u64 page set which proves every recovered output
// page is written exactly once. The heap table is a power-of-two open
// hash table with the Rust load cap; the authorized-scratch migration
// is the recorded chunk-4-10 follow-up, so exceeding the heap load is
// the BudgetExceeded refusal exactly like the Rust non-posix arm.

import (
	"math"
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

const (
	pageSetEmpty              = uint64(0)
	pageSetMinSlots           = 8
	pageSetMaxLoadNumerator   = 3
	pageSetMaxLoadDenominator = 4
	slotBytes                 = 8
)

// pageSet is the sparse page-ownership table of one recovery output
// (Rust PageSet, heap-only Slots::Heap).
type pageSet struct {
	slots []uint64
	len   int
}

// newPageSet allocates the heap table for a recovery budget (Rust
// PageSet::allocate heap arm): the affordable slot count is the heap
// budget divided by the slot size, the wanted count derives from the
// expected page count at the load cap, and the table is the smaller
// power of two. A budget below the minimum slot count refuses.
func newPageSet(maxHeapBytes uint64, expectedPages uint64) (*pageSet, error) {
	affordable := maxHeapBytes / slotBytes
	if affordable > math.MaxInt {
		affordable = math.MaxInt
	}
	if affordable < pageSetMinSlots {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery page-ownership table"}
	}
	wanted := wantedSlots(expectedPages)
	slots := wanted
	if int(affordable) < wanted {
		slots = floorPowerOfTwo(int(affordable))
	}
	values, err := heapSlots(slots)
	if err != nil {
		return nil, err
	}
	return &pageSet{slots: values, len: 0}, nil
}

// forRecovery builds the page set of one recovery construction (Rust
// PageSet::for_recovery heap-only arm): the budget's scratch shape is
// accepted and validated, and the heap-only table refuses once the
// load cap is reached; the scratch migration is the recorded
// follow-up.
func forRecovery(maxHeapBytes uint64, expectedPages uint64, source format.Meta, budget *RecoveryBudget) (*pageSet, error) {
	if err := budget.validate(); err != nil {
		return nil, err
	}
	return newPageSet(maxHeapBytes, expectedPages)
}

// insert claims one page, reporting whether it is new (Rust
// PageSet::insert): the load cap is enforced before the probe, and the
// heap-only arm refuses with the BudgetExceeded class when the cap is
// reached.
func (p *pageSet) insert(page uint32) (bool, error) {
	if exceedsLoad(p.len+1, len(p.slots)) {
		return false, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery page-ownership table"}
	}
	encoded := uint64(page) + 1
	mask := uint64(len(p.slots) - 1)
	index := hashU32(page) & mask
	for {
		switch p.slots[index] {
		case pageSetEmpty:
			p.slots[index] = encoded
			p.len++
			return true, nil
		default:
			if p.slots[index] == encoded {
				return false, nil
			}
			index = (index + 1) & mask
		}
	}
}

// claim walks one graph edge through the ownership set (Rust
// PageSet::claim): depth and bounds refusals carry their deterministic
// reasons, an already-claimed page is the cycle or alias class from
// the walk path, and a successful claim records the page on the path.
func (p *pageSet) claim(page uint32, pageCount uint64, path []uint32, depth int) (bool, validation.ValidationReason, error) {
	if depth >= len(path) {
		return false, validation.ReasonTreeLevelInvalid, nil
	}
	if page < 2 || uint64(page) >= pageCount {
		return false, validation.ReasonPageOutOfBounds, nil
	}
	newClaim, err := p.insert(page)
	if err != nil {
		return false, 0, err
	}
	if !newClaim {
		reason := validation.ReasonPageAlias
		if containsPage(path[:depth], page) {
			reason = validation.ReasonTreeCycle
		}
		return false, reason, nil
	}
	path[depth] = page
	return true, 0, nil
}

// retainedBytes is the heap retained by the table (Rust
// PageSet::retained_bytes heap arm).
func (p *pageSet) retainedBytes() uint64 {
	return uint64(len(p.slots)) * slotBytes
}

// reset clears every claim (Rust PageSet::reset heap arm).
func (p *pageSet) reset() error {
	p.len = 0
	for i := range p.slots {
		p.slots[i] = pageSetEmpty
	}
	return nil
}

// pageSetFailure is the failing terminal of one page set (Rust
// PageSetFailure): the cause and the scratch cleanup authority of the
// attempted migration (nil in the heap-only arm).
type pageSetFailure struct {
	cause   error
	cleanup any
}

// finish folds the operation result through the page set terminal
// (Rust PageSet::finish heap-only arm): no scratch cleanup exists in
// the heap-only arm.
func (p *pageSet) finish(result error) *pageSetFailure {
	if result != nil {
		return &pageSetFailure{cause: result, cleanup: nil}
	}
	return &pageSetFailure{cause: nil, cleanup: nil}
}

// heapSlots allocates the zeroed table (Rust heap_slots; the
// allocation is bounded by the budget through the affordable slot
// count).
func heapSlots(slots int) ([]uint64, error) {
	if slots < pageSetMinSlots {
		return []uint64{}, nil
	}
	return make([]uint64, slots), nil
}

// wantedSlots sizes the table for the expected page count at the load
// cap (Rust wanted_slots: ceil(pages * 4 / 3), at least the minimum,
// rounded up to a power of two; the saturating clamp to the widest
// usize maps to the top bit of the platform int).
func wantedSlots(expectedPages uint64) int {
	wanted := (expectedPages*pageSetMaxLoadDenominator + pageSetMaxLoadNumerator - 1) / pageSetMaxLoadNumerator
	if wanted < pageSetMinSlots {
		wanted = pageSetMinSlots
	}
	if wanted > math.MaxInt {
		// The Rust saturating clamp lands on the unrepresentable top
		// bit of usize; the largest real capacity is the parity peer
		// here. The arm is unreachable for real sources (the wanted
		// count derives from a bounded meta page count).
		return math.MaxInt
	}
	return nextPowerOfTwo(int(wanted))
}

// exceedsLoad reports whether one more claim breaches the load cap
// (Rust exceeds_load: len * 4 > slots * 3).
func exceedsLoad(length, slots int) bool {
	return slots == 0 || uint64(length)*pageSetMaxLoadDenominator > uint64(slots)*pageSetMaxLoadNumerator
}

// floorPowerOfTwo is the largest power of two at most value (Rust
// floor_power_of_two).
func floorPowerOfTwo(value int) int {
	if value <= 0 {
		return 0
	}
	return 1 << (bits.Len(uint(value)) - 1)
}

// nextPowerOfTwo rounds one value up to a power of two (Rust
// checked_next_power_of_two).
func nextPowerOfTwo(value int) int {
	if value <= 1 {
		return 1
	}
	return 1 << bits.Len(uint(value-1))
}

// containsPage reports whether the walk path contains one page (Rust
// path[..depth].contains).
func containsPage(path []uint32, page uint32) bool {
	for _, entry := range path {
		if entry == page {
			return true
		}
	}
	return false
}

// hashU32 is the page hash of the recovery tables module (Rust
// tables::hash_u32, the splitmix-style finalizer over the raw page
// number).
func hashU32(value uint32) uint64 {
	v := uint64(value)
	v ^= v >> 30
	v *= 0xbf58476d1ce4e5b9
	v ^= v >> 27
	v *= 0x94d049bb133111eb
	return v ^ (v >> 31)
}
