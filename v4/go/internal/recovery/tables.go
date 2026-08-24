package recovery

// One bounded backing store for the recovery catalog and membership
// tables (Rust recovery/tables.rs): the heap-only arm lays out every
// fixed-size region (catalog records/names/indexes, membership records/
// ids, structure records/ids) in one owned byte slice sized from the
// counted records at the load cap. The authorized-scratch migration is
// the recorded chunk-4-10 follow-up, so a layout that does not fit the
// heap refuses with the Rust non-posix tables class.

import "github.com/firehol/iprange/v4/go/internal/format"

const (
	catalogRecordSize    = uint64(264)
	catalogNameSlotSize  = uint64(24)
	catalogIndexSlotSize = uint64(16)
	membershipRecordSize = uint64(56)
	structureRecordSize  = uint64(48)
)

// tableCounts is the counted record bound of one recovery table layout
// (Rust tables::Counts).
type tableCounts struct {
	catalog     uint64
	memberships uint64
	structures  uint64
}

// tableRegion is one fixed-width region of the recovery table layout
// (Rust tables::Region).
type tableRegion struct {
	start uint64
	slots uint64
	width uint64
}

// tableLayout is the exact region layout of one recovery table store
// (Rust tables::Layout).
type tableLayout struct {
	catalogRecords    tableRegion
	catalogNames      tableRegion
	catalogIndexes    tableRegion
	membershipRecords tableRegion
	membershipIDs     tableRegion
	structureRecords  tableRegion
	structureIDs      tableRegion
	bytes             uint64
}

// newTableLayout lays out the regions from the counted records (Rust
// Layout::new: one slot table per record class at the hash load cap,
// every region in the fixed order).
func newTableLayout(counts tableCounts) (tableLayout, error) {
	catalogSlots, err := hashTableSlots(counts.catalog)
	if err != nil {
		return tableLayout{}, err
	}
	membershipSlots, err := hashTableSlots(counts.memberships)
	if err != nil {
		return tableLayout{}, err
	}
	structureSlots, err := hashTableSlots(counts.structures)
	if err != nil {
		return tableLayout{}, err
	}
	var next uint64
	var layout tableLayout
	if layout.catalogRecords, err = tableRegionOf(&next, counts.catalog, catalogRecordSize); err != nil {
		return tableLayout{}, err
	}
	if layout.catalogNames, err = tableRegionOf(&next, catalogSlots, catalogNameSlotSize); err != nil {
		return tableLayout{}, err
	}
	if layout.catalogIndexes, err = tableRegionOf(&next, catalogSlots, catalogIndexSlotSize); err != nil {
		return tableLayout{}, err
	}
	if layout.membershipRecords, err = tableRegionOf(&next, counts.memberships, membershipRecordSize); err != nil {
		return tableLayout{}, err
	}
	if layout.membershipIDs, err = tableRegionOf(&next, membershipSlots, idSlotSize); err != nil {
		return tableLayout{}, err
	}
	if layout.structureRecords, err = tableRegionOf(&next, counts.structures, structureRecordSize); err != nil {
		return tableLayout{}, err
	}
	if layout.structureIDs, err = tableRegionOf(&next, structureSlots, idSlotSize); err != nil {
		return tableLayout{}, err
	}
	layout.bytes = next
	return layout, nil
}

// tableRegionOf appends one region to the layout (Rust region).
func tableRegionOf(next *uint64, slots, width uint64) (tableRegion, error) {
	start := *next
	bytes, ok := checkedMul(slots, width)
	if !ok {
		return tableRegion{}, overflowError("recovery table layout")
	}
	*next, ok = checkedAdd(start, bytes)
	if !ok {
		return tableRegion{}, overflowError("recovery table layout")
	}
	return tableRegion{start: start, slots: slots, width: width}, nil
}

// hashTableSlots sizes one slot table at the hash load cap (Rust
// hash_slots: ceil(records*4/3) plus the 8-slot minimum, rounded up to
// a power of two; zero records keep zero slots).
func hashTableSlots(records uint64) (uint64, error) {
	if records == 0 {
		return 0, nil
	}
	slots, ok := checkedMul(records, 4)
	if !ok {
		return 0, overflowError("recovery table slots")
	}
	slots, ok = checkedAdd(slots, 2)
	if !ok {
		return 0, overflowError("recovery table slots")
	}
	slots = slots / 3
	if slots < 8 {
		slots = 8
	}
	if slots > 1<<63 {
		return 0, overflowError("recovery table slots")
	}
	slots--
	slots |= slots >> 1
	slots |= slots >> 2
	slots |= slots >> 4
	slots |= slots >> 8
	slots |= slots >> 16
	slots |= slots >> 32
	return slots + 1, nil
}

// tableStore is the heap-only recovery table backing store (Rust
// Tables with Storage::Heap).
type tableStore struct {
	layout tableLayout
	bytes  []byte
}

// allocateTables builds the recovery table store for the counted
// records inside the remaining heap (Rust Tables::allocate heap-only
// arm: the layout must fit the budget minus the page set and the
// reserved heap, else the tables class refuses).
func allocateTables(counts tableCounts, pages *pageSet, budget *RecoveryBudget, reservedHeapBytes uint64) (*tableStore, error) {
	layout, err := newTableLayout(counts)
	if err != nil {
		return nil, err
	}
	available, ok := checkedSub(budget.MaxHeapBytes, pages.retainedBytes())
	if ok {
		available, ok = checkedSub(available, reservedHeapBytes)
	}
	if !ok {
		available = 0
	}
	if layout.bytes > available || layout.bytes > uint64(maxInt) {
		return nil, budgetError("recovery tables")
	}
	return &tableStore{layout: layout, bytes: make([]byte, int(layout.bytes))}, nil
}

// retainedBytes is the heap retained by the store (Rust
// Tables::retained_bytes heap arm).
func (t *tableStore) retainedBytes() uint64 {
	return uint64(len(t.bytes))
}

// read copies one region entry into output (Rust Tables::read: the
// exact width and slot proof, then the heap copy).
func (t *tableStore) read(region tableRegion, index uint64, output []byte) error {
	offset, err := t.offset(region, index, len(output))
	if err != nil {
		return err
	}
	copy(output, t.bytes[offset:offset+uint64(len(output))])
	return nil
}

// write stores one region entry (Rust Tables::write).
func (t *tableStore) write(region tableRegion, index uint64, input []byte) error {
	offset, err := t.offset(region, index, len(input))
	if err != nil {
		return err
	}
	copy(t.bytes[offset:offset+uint64(len(input))], input)
	return nil
}

// offset proves one region access (Rust Tables::offset: the exact
// width, the slot bound, and the checked arithmetic; the layout bounds
// are the Corrupt class).
func (t *tableStore) offset(region tableRegion, index uint64, width int) (uint64, error) {
	if uint64(width) != region.width || index >= region.slots {
		return 0, corruptError("recovery table access is outside its region")
	}
	offset, ok := checkedMul(index, region.width)
	if !ok {
		return 0, overflowError("recovery table offset")
	}
	offset, ok = checkedAdd(region.start, offset)
	if !ok {
		return 0, overflowError("recovery table offset")
	}
	end, ok := checkedAdd(offset, region.width)
	if !ok || end > t.layout.bytes {
		return 0, corruptError("recovery table region exceeds its backing")
	}
	return offset, nil
}

// corruptError builds the fixed format-invalid class.
func corruptError(detail string) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
}
