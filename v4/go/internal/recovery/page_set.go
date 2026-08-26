package recovery

// Recovery page-ownership set (Rust recovery/page_set.rs): one sparse
// u64 page set which proves every recovered output page is written
// exactly once. The heap table is a power-of-two open hash table with
// the Rust load cap; when the authorized scratch budget is supplied,
// the load cap migrates the table into fixed-slot file storage inside
// the scratch directory (table at offset 128, one 8-byte claim per
// slot), and the scratch attempt travels with the set into the
// external sort and the cleanup terminal.

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
// (Rust PageSet with the Heap or File slot storage and the optional
// scratch fallback).
type pageSet struct {
	heap     []uint64
	file     *fileSlots
	len      int
	fallback *pageFallback
	scratch  *scratch
}

// pageFallback is the authorized-scratch migration description of one
// page set (Rust Fallback).
type pageFallback struct {
	directory    string
	source       format.Meta
	maxBytes     uint64
	maxFiles     uint32
	maxOpenFiles uint32
	wantedSlots  int
}

// fileSlots is the fixed-slot file table of one migrated page set
// (Rust FileSlots: the detached scratch file and the slot count).
type fileSlots struct {
	file  scratchFile
	slots int
}

// newPageSet allocates the heap table for a recovery budget (Rust
// PageSet::allocate heap arm): the affordable slot count is the heap
// budget divided by the slot size, the wanted count derives from the
// expected page count at the load cap, and the table is the smaller
// power of two. Without a scratch fallback a budget below the minimum
// slot count refuses; with one, the tiny table migrates on load.
func newPageSet(maxHeapBytes uint64, expectedPages uint64, fallback *pageFallback) (*pageSet, error) {
	affordable := maxHeapBytes / slotBytes
	if affordable > math.MaxInt {
		affordable = math.MaxInt
	}
	if affordable < pageSetMinSlots && fallback == nil {
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
	return &pageSet{heap: values, fallback: fallback}, nil
}

// forRecovery builds the page set of one recovery construction (Rust
// PageSet::for_recovery): the budget's scratch shape is accepted and
// validated, the fallback captures the scratch directory facts, and
// the heap table is the smaller power of two.
func forRecovery(maxHeapBytes uint64, expectedPages uint64, source format.Meta, budget *RecoveryBudget) (*pageSet, error) {
	if err := budget.validate(); err != nil {
		return nil, err
	}
	var fallback *pageFallback
	if budget.ScratchDirectory != "" {
		fallback = &pageFallback{
			directory:    budget.ScratchDirectory,
			source:       source,
			maxBytes:     budget.MaxScratchBytes,
			maxFiles:     budget.MaxScratchFiles,
			maxOpenFiles: budget.MaxOpenFiles,
			wantedSlots:  wantedSlots(expectedPages),
		}
	}
	return newPageSet(maxHeapBytes, expectedPages, fallback)
}

// insert claims one page, reporting whether it is new (Rust
// PageSet::insert): the load cap is enforced before the probe, the
// cap triggers the scratch migration when a fallback exists, and a
// still-exceeding table refuses with the BudgetExceeded class.
func (p *pageSet) insert(page uint32) (bool, error) {
	if exceedsLoad(p.len+1, p.slotCount()) {
		if err := p.migrate(); err != nil {
			return false, err
		}
		if exceedsLoad(p.len+1, p.slotCount()) {
			return false, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery page-ownership table"}
		}
	}
	encoded := uint64(page) + 1
	mask := uint64(p.slotCount() - 1)
	index := hashU32(page) & mask
	for {
		value, err := p.readSlot(int(index))
		if err != nil {
			return false, err
		}
		switch {
		case value == pageSetEmpty:
			if err := p.writeSlot(int(index), encoded); err != nil {
				return false, err
			}
			p.len++
			return true, nil
		case value == encoded:
			return false, nil
		default:
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

// slotCount is the current table capacity (Rust PageSet::slot_count).
func (p *pageSet) slotCount() int {
	if p.file != nil {
		return p.file.slots
	}
	return len(p.heap)
}

// readSlot reads one claim (Rust PageSet::read: heap or fixed-slot
// file).
func (p *pageSet) readSlot(index int) (uint64, error) {
	if p.file != nil {
		return p.file.read(index)
	}
	return p.heap[index], nil
}

// writeSlot stores one claim (Rust PageSet::write).
func (p *pageSet) writeSlot(index int, value uint64) error {
	if p.file != nil {
		return p.file.write(index, value)
	}
	p.heap[index] = value
	return nil
}

// retainedBytes is the heap retained by the table (Rust
// PageSet::retained_bytes: the file table retains no heap).
func (p *pageSet) retainedBytes() uint64 {
	if p.file != nil {
		return 0
	}
	return uint64(len(p.heap)) * slotBytes
}

// reset clears every claim (Rust PageSet::reset).
func (p *pageSet) reset() error {
	p.len = 0
	if p.file != nil {
		return p.resetFile()
	}
	for i := range p.heap {
		p.heap[i] = pageSetEmpty
	}
	return nil
}

// pageSetFailure is the failing terminal of one page set (Rust
// PageSetFailure): the cause and the scratch cleanup of the attempted
// migration.
type pageSetFailure struct {
	cause   error
	cleanup *scratchCleanup
}

// finish folds the operation result through the page-set terminal
// (Rust PageSet::finish over finish_cleanup): the retained scratch
// attempt cleans before the result is delivered, and an unclean
// cleanup folds into the CleanupIncomplete class with the cleanup
// ledger attached.
func (p *pageSet) finish(result error) *pageSetFailure {
	var cleanup *scratchCleanup
	if p.scratch != nil {
		scratch := p.scratch
		p.scratch = nil
		scratch.attachSlotIfFile(p)
		cleanup = scratch.cleanup()
	}
	if cleanup == nil {
		return &pageSetFailure{cause: result, cleanup: nil}
	}
	if cleanup.clean() {
		return &pageSetFailure{cause: result, cleanup: cleanup}
	}
	return &pageSetFailure{cause: cleanupIncompleteError(result, cleanup), cleanup: cleanup}
}

// attachSlotIfFile returns the detached file slot of the page set to
// its scratch owner (Rust release arm during cleanup).
func (s *scratch) attachSlotIfFile(p *pageSet) {
	if p.file != nil {
		s.attach(p.file.file)
	}
}

// takeScratch detaches the scratch attempt for the external sort
// (Rust PageSet::take_scratch: the fallback is consumed and the
// attempt moves to the caller).
func (p *pageSet) takeScratch() *scratch {
	p.fallback = nil
	scratch := p.scratch
	p.scratch = nil
	return scratch
}

// release returns the page set's file storage to its scratch attempt
// (Rust PageSet::release): the detached file slot is re-attached and
// reported, the storage is reset to an empty heap table.
func (p *pageSet) release(scratch *scratch) *scratchSlot {
	var slot *scratchSlot
	if p.file != nil {
		value := scratch.attach(p.file.file)
		slot = &value
		p.file = nil
	}
	p.heap = nil
	return slot
}

// createScratchFile establishes one detached scratch file of the
// requested length (Rust PageSet::create_scratch_file: the table
// arm of Tables::allocate).
func (p *pageSet) createScratchFile(length uint64) (scratchFile, error) {
	scratch, err := p.ensureScratch()
	if err != nil {
		return scratchFile{}, err
	}
	slot, err := scratch.create()
	if err != nil {
		return scratchFile{}, err
	}
	if err := scratch.resize(slot, length); err != nil {
		return scratchFile{}, err
	}
	return scratch.detach(slot), nil
}

// migrate moves the heap claims into fixed-slot file storage (Rust
// PageSet::migrate); without a fallback the BudgetExceeded class is
// the Rust non-posix arm.
func (p *pageSet) migrate() error {
	if p.file != nil {
		return nil
	}
	if p.fallback == nil {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery page-ownership table"}
	}
	fallback := *p.fallback
	slots, err := p.fileSlotsOf(&fallback)
	if err != nil {
		return err
	}
	if exceedsLoad(p.len+1, slots) {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery page-ownership table"}
	}
	output, err := p.createFileSlots(slots)
	if err != nil {
		return err
	}
	p.file = output
	return nil
}

// fileSlotsOf computes the affordable fixed-slot count (Rust
// PageSet::file_slots over Fallback::file_slots).
func (p *pageSet) fileSlotsOf(fallback *pageFallback) (int, error) {
	var available uint64
	if p.scratch != nil {
		available = p.scratch.remainingBytes()
	} else {
		reserve := uint64(0)
		if fallback.maxFiles >= 2 {
			reserve = scratchHeaderSize
		}
		available = fallback.maxBytes - reserve
		if available > fallback.maxBytes {
			return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery page-ownership scratch"}
		}
	}
	return fallback.fileSlots(available)
}

// createFileSlots establishes the detached fixed-slot table (Rust
// PageSet::create_file_slots: the table extent, the scratch file, and
// the copy of every existing claim).
func (p *pageSet) createFileSlots(slots int) (*fileSlots, error) {
	length, err := tableLength(slots)
	if err != nil {
		return nil, err
	}
	scratch, err := p.ensureScratch()
	if err != nil {
		return nil, err
	}
	slot, err := scratch.create()
	if err != nil {
		return nil, err
	}
	if err := scratch.resize(slot, length); err != nil {
		return nil, err
	}
	file := scratch.detach(slot)
	output := &fileSlots{file: file, slots: slots}
	for _, value := range p.heap {
		if value == pageSetEmpty {
			continue
		}
		if err := output.insert(value); err != nil {
			return nil, err
		}
	}
	return output, nil
}

// ensureScratch starts the scratch attempt on first use (Rust
// PageSet::ensure_scratch).
func (p *pageSet) ensureScratch() (*scratch, error) {
	if p.scratch == nil {
		if p.fallback == nil {
			return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery scratch"}
		}
		scratch, err := scratchStart(p.fallback.directory, p.fallback.source, p.fallback.maxBytes, p.fallback.maxFiles, p.fallback.maxOpenFiles)
		if err != nil {
			return nil, err
		}
		p.scratch = scratch
	}
	return p.scratch, nil
}

// resetFile clears the fixed-slot table (Rust PageSet::reset_file:
// reset then re-establish the table extent).
func (p *pageSet) resetFile() error {
	if p.file == nil {
		panic("file reset requires file slots")
	}
	scratch, err := p.ensureScratch()
	if err != nil {
		return err
	}
	slot := p.file.file.slot()
	if err := scratch.reset(slot); err != nil {
		return err
	}
	length, err := tableLength(p.file.slots)
	if err != nil {
		return err
	}
	return scratch.resize(slot, length)
}

// pageFallback.fileSlots computes the affordable slot count from the
// available bytes (Rust Fallback::file_slots: the header first, the
// slot count at the floor power of two, the load cap, and the minimum
// slot count).
func (f *pageFallback) fileSlots(available uint64) (int, error) {
	tableBytes := available - scratchHeaderSize
	if tableBytes > available {
		return 0, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery page-ownership scratch"}
	}
	affordable := tableBytes / slotBytes
	if affordable > math.MaxInt {
		affordable = math.MaxInt
	}
	slots := f.wantedSlots
	if int(affordable) < slots {
		slots = floorPowerOfTwo(int(affordable))
	}
	if slots < pageSetMinSlots {
		return 0, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery page-ownership scratch"}
	}
	return slots, nil
}

// tableLength is the complete fixed-slot table extent (Rust
// table_length: the header plus the slot bytes).
func tableLength(slots int) (uint64, error) {
	bytes, ok := checkedMul(uint64(slots), slotBytes)
	if !ok {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery page-ownership scratch length"}
	}
	total, ok := checkedAdd(bytes, scratchHeaderSize)
	if !ok {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery page-ownership scratch length"}
	}
	return total, nil
}

// slotOffset is the byte offset of one fixed-slot claim (Rust
// slot_offset: the header plus the slot index).
func slotOffset(index int) (uint64, error) {
	bytes, ok := checkedMul(uint64(index), slotBytes)
	if !ok {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery page-ownership scratch offset"}
	}
	total, ok := checkedAdd(bytes, scratchHeaderSize)
	if !ok {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery page-ownership scratch offset"}
	}
	return total, nil
}

// fileSlots.read reads one claim (Rust FileSlots::read).
func (s *fileSlots) read(index int) (uint64, error) {
	var bytes [8]byte
	offset, err := slotOffset(index)
	if err != nil {
		return 0, err
	}
	if err := s.file.read(offset, bytes[:]); err != nil {
		return 0, err
	}
	return format.U64(bytes[:]), nil
}

// fileSlots.write stores one claim (Rust FileSlots::write).
func (s *fileSlots) write(index int, value uint64) error {
	var bytes [8]byte
	format.PutU64(bytes[:], value)
	offset, err := slotOffset(index)
	if err != nil {
		return err
	}
	return s.file.write(offset, bytes[:])
}

// fileSlots.insert claims one page probing from its hash (Rust
// FileSlots::insert; the migration copy arm).
func (s *fileSlots) insert(encoded uint64) error {
	page := uint32(encoded - 1)
	if encoded == 0 || uint64(page)+1 != encoded {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery page claim is invalid"}
	}
	mask := uint64(s.slots - 1)
	index := hashU32(page) & mask
	for {
		value, err := s.read(int(index))
		if err != nil {
			return err
		}
		if value == pageSetEmpty {
			return s.write(int(index), encoded)
		}
		index = (index + 1) & mask
	}
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
// cap (Rust wanted_slots: saturating ceil(pages * 4 / 3), at least
// the minimum, rounded up to a power of two; the saturating clamp to
// the widest usize maps to the top bit of the platform int).
func wantedSlots(expectedPages uint64) int {
	wanted := uint64(pageSetMinSlots)
	if expectedPages <= math.MaxUint64/pageSetMaxLoadDenominator {
		product := expectedPages * pageSetMaxLoadDenominator
		wanted = (product + pageSetMaxLoadNumerator - 1) / pageSetMaxLoadNumerator
		if wanted < pageSetMinSlots {
			wanted = pageSetMinSlots
		}
	}
	if wanted > uint64(maxInt) {
		return maxInt
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
