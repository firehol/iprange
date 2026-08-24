package validation

// Validation context: the 2-bit page-claim bitmap (GRAPH /
// ALLOCATION / UNCLAIMED), the membership and structure reverse
// tables, heap accounting, progress counters, cancellation, and the
// finding sink (Rust validation/context.rs). The validators of slices
// B-E compose this context; slice A ships the context core with the
// boundary tests.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

const (
	claimUnclaimed = 0
	claimGraph     = 1
	claimAlloc     = 2
)

// Claims is the 2-bit-per-page partition bitmap of the validated file
// (Rust validation::context Claims): the graph walk marks GRAPH, the
// allocation partitions mark ALLOCATION, and the final
// validate_partition sweep reports every page that is neither.
type Claims struct {
	bytes     []byte
	pageCount uint64
}

// newClaims builds the claim bitmap, bounded by the validation heap
// budget (Rust Claims::new: ceil(page_count/4) bytes, BudgetExceeded
// classes).
func newClaims(pageCount uint64, maxHeapBytes uint64) (*Claims, error) {
	byteCount := pageCount + 3
	if byteCount < pageCount {
		return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation claim bitmap"}
	}
	byteCount /= 4
	if byteCount > maxHeapBytes {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "validation page-claim bitmap"}
	}
	return &Claims{bytes: make([]byte, byteCount), pageCount: pageCount}, nil
}

// add marks one page with state and reports its previous 2-bit value
// (Rust Claims::add; an out-of-range page is the Corrupt class, which
// the SDK maps to FormatInvalid).
func (c *Claims) add(page uint32, state uint8) (uint8, error) {
	previous, err := c.get(page)
	if err != nil {
		return 0, err
	}
	index := int(page / 4)
	shift := (page % 4) * 2
	c.bytes[index] |= state << shift
	return previous, nil
}

// get reads the 2-bit value of one page (Rust Claims::get).
func (c *Claims) get(page uint32) (uint8, error) {
	if uint64(page) >= c.pageCount {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "validation claim is outside page bounds"}
	}
	index := int(page / 4)
	shift := (page % 4) * 2
	return (c.bytes[index] >> shift) & 3, nil
}

// retainedBytes is the heap retained by the claim bitmap (Rust
// Claims::retained_bytes).
func (c *Claims) retainedBytes() uint64 { return uint64(len(c.bytes)) }

// Context carries one validation sweep (Rust validation::context
// Context). mapping is the read-only page source, meta the selected
// generation, check the cancellation checkpoint (nil never cancels),
// and sink the finding consumer.
type Context struct {
	mapping     *mapping.Mapping
	meta        format.Meta
	claims      *Claims
	memberships *Table
	structures  *Table
	heapLimit   uint64
	heapUsed    uint64
	progress    ValidationProgress
	check       func() error
	sink        ValidationSink
}

// NewContext builds the validation context, charging the claim bitmap
// and the value-kind reverse tables against the budget (Rust
// Context::new).
func NewContext(m *mapping.Mapping, meta format.Meta, budget *ValidationBudget, check func() error, sink ValidationSink) (*Context, error) {
	claims, err := newClaims(meta.PageCount, budget.MaxHeapBytes)
	if err != nil {
		return nil, err
	}
	heapUsed := claims.retainedBytes()
	ctx := &Context{
		mapping:   m,
		meta:      meta,
		claims:    claims,
		heapLimit: budget.MaxHeapBytes,
		heapUsed:  heapUsed,
		check:     check,
		sink:      sink,
	}
	if meta.ValueKind == format.ValueKindMembership || meta.ValueKind == format.ValueKindStructured {
		table, err := newTable(meta.MembershipEntryCount, budget.MaxHeapBytes-heapUsed)
		if err != nil {
			return nil, err
		}
		heapUsed = heapUsed + table.retainedBytes()
		if heapUsed < table.retainedBytes() {
			return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation retained heap"}
		}
		ctx.memberships = table
		ctx.heapUsed = heapUsed
	}
	if meta.ValueKind == format.ValueKindStructured {
		table, err := newTable(meta.StructureEntryCount, budget.MaxHeapBytes-heapUsed)
		if err != nil {
			return nil, err
		}
		heapUsed = heapUsed + table.retainedBytes()
		if heapUsed < table.retainedBytes() {
			return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation retained heap"}
		}
		ctx.structures = table
		ctx.heapUsed = heapUsed
	}
	return ctx, nil
}

// finish returns the accumulated progress (Rust Context::finish).
func (c *Context) finish() ValidationProgress { return c.progress }

// checkpoint runs one cancellation checkpoint (Rust
// Context::checkpoint).
func (c *Context) checkpoint() error {
	if c.check == nil {
		return nil
	}
	return c.check()
}

// reserveHeap charges bytes against the heap budget (Rust
// Context::reserve_heap).
func (c *Context) reserveHeap(bytes uint64, purpose string) error {
	retained := c.heapUsed + bytes
	if retained < c.heapUsed {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation retained heap"}
	}
	if retained > c.heapLimit {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: purpose}
	}
	c.heapUsed = retained
	return nil
}

// releaseHeap returns bytes to the heap accounting (Rust
// Context::release_heap).
func (c *Context) releaseHeap(bytes uint64) {
	if bytes > c.heapUsed {
		c.heapUsed = 0
		return
	}
	c.heapUsed -= bytes
}

// markUntraversable counts one untraversable subgraph (Rust
// Context::mark_untraversable).
func (c *Context) markUntraversable(unbounded bool) error {
	return c.progress.markUntraversable(unbounded)
}

// reserveAllocatorPages records the meta allocator-reserve pages in
// the allocation partition (Rust Context::reserve_allocator_pages:
// out-of-bounds or double-claimed reserve pages are findings).
func (c *Context) reserveAllocatorPages() error {
	for _, page := range c.meta.AllocatorReserve {
		if page == 0 {
			continue
		}
		if page < 2 || uint64(page) >= c.meta.PageCount {
			if err := c.emit(ReasonPageOutOfBounds, ObjectFreeBitmap, &page, nil, nil); err != nil {
				return err
			}
			continue
		}
		previous, err := c.claims.add(page, claimAlloc)
		if err != nil {
			return err
		}
		if previous != claimUnclaimed {
			if err := c.emit(ReasonAllocationPartitionInvalid, ObjectFreeBitmap, &page, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// markAllocated records one allocated page in the allocation partition
// (Rust Context::mark_allocated).
func (c *Context) markAllocated(pageNumber uint32, object ValidationObject) error {
	if err := c.checkpoint(); err != nil {
		return err
	}
	if pageNumber < 2 || uint64(pageNumber) >= c.meta.PageCount {
		return c.emit(ReasonPageOutOfBounds, object, &pageNumber, nil, nil)
	}
	previous, err := c.claims.add(pageNumber, claimAlloc)
	if err != nil {
		return err
	}
	if previous != claimUnclaimed {
		return c.emit(ReasonAllocationPartitionInvalid, object, &pageNumber, nil, nil)
	}
	return nil
}

// readGraphPage reads one graph page through the claims partition
// (Rust Context::read_graph_page): bounds, alias/cycle detection, CRC,
// and the per-object page counters. A nil page with a nil error means
// the page was refused as a finding and its subgraph is
// untraversable.
func (c *Context) readGraphPage(pageNumber uint32, object ValidationObject, path []uint32) ([]byte, error) {
	if err := c.checkpoint(); err != nil {
		return nil, err
	}
	ok, err := c.requireGraphBounds(pageNumber, object)
	if err != nil || !ok {
		return nil, err
	}
	ok, err = c.claimGraphPage(pageNumber, object, path)
	if err != nil || !ok {
		return nil, err
	}
	return c.loadGraphPage(pageNumber, object)
}

func (c *Context) requireGraphBounds(pageNumber uint32, object ValidationObject) (bool, error) {
	if pageNumber >= 2 && uint64(pageNumber) < c.meta.PageCount {
		return true, nil
	}
	if err := c.emit(ReasonPageOutOfBounds, object, &pageNumber, nil, nil); err != nil {
		return false, err
	}
	unbounded := object == ObjectRangeTree
	if err := c.progress.markUntraversable(unbounded); err != nil {
		return false, err
	}
	return false, nil
}

func (c *Context) claimGraphPage(pageNumber uint32, object ValidationObject, path []uint32) (bool, error) {
	previous, err := c.claims.get(pageNumber)
	if err != nil {
		return false, err
	}
	if previous&claimGraph != 0 {
		reason := ReasonPageAlias
		for _, ancestor := range path {
			if ancestor == pageNumber {
				reason = ReasonTreeCycle
				break
			}
		}
		if err := c.emit(reason, object, &pageNumber, nil, nil); err != nil {
			return false, err
		}
		unbounded := object == ObjectRangeTree
		if err := c.progress.markUntraversable(unbounded); err != nil {
			return false, err
		}
		return false, nil
	}
	if _, err := c.claims.add(pageNumber, claimGraph); err != nil {
		return false, err
	}
	if previous&claimAlloc != 0 {
		if err := c.emit(ReasonAllocationPartitionInvalid, object, &pageNumber, nil, nil); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (c *Context) loadGraphPage(pageNumber uint32, object ValidationObject) ([]byte, error) {
	if err := c.progress.countPage(object); err != nil {
		return nil, err
	}
	page, err := c.mapping.Page(pageNumber)
	if err != nil {
		if err2 := c.emit(ReasonIoError, object, &pageNumber, nil, nil); err2 != nil {
			return nil, err2
		}
		unbounded := object == ObjectRangeTree
		if err2 := c.progress.markUntraversable(unbounded); err2 != nil {
			return nil, err2
		}
		return nil, nil
	}
	if !format.PageChecksumValid(page) {
		if err2 := c.emit(ReasonPageCrcMismatch, object, &pageNumber, nil, nil); err2 != nil {
			return nil, err2
		}
		unbounded := object == ObjectRangeTree
		if err2 := c.progress.markUntraversable(unbounded); err2 != nil {
			return nil, err2
		}
		return nil, nil
	}
	return page, nil
}

// validatePartition reports every page that neither the graph walk nor
// the allocation partitions claimed (Rust Context::validate_partition;
// the unclaimed runs are one finding each with their physical byte
// interval).
func (c *Context) validatePartition() error {
	page := uint64(2)
	for page < c.meta.PageCount {
		if err := c.checkpoint(); err != nil {
			return err
		}
		start, end, ok, err := c.nextUnclaimed(page)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		start32 := uint32(start)
		interval, err := partitionBytes(start, end)
		if err != nil {
			return err
		}
		if err := c.emit(ReasonAllocationPartitionInvalid, ObjectFileGeometry, &start32, &interval, nil); err != nil {
			return err
		}
		page = end
	}
	return nil
}

func (c *Context) nextUnclaimed(page uint64) (uint64, uint64, bool, error) {
	page, err := c.skipClaimed(page)
	if err != nil {
		return 0, 0, false, err
	}
	if page == c.meta.PageCount {
		return 0, 0, false, nil
	}
	end, err := c.skipUnclaimed(page)
	if err != nil {
		return 0, 0, false, err
	}
	return page, end, true, nil
}

func (c *Context) skipClaimed(page uint64) (uint64, error) {
	for page < c.meta.PageCount {
		value, err := c.claims.get(uint32(page))
		if err != nil {
			return 0, err
		}
		if value != claimUnclaimed {
			if page&63 == 0 {
				if err := c.checkpoint(); err != nil {
					return 0, err
				}
			}
			page++
			continue
		}
		break
	}
	return page, nil
}

func (c *Context) skipUnclaimed(page uint64) (uint64, error) {
	for page < c.meta.PageCount {
		value, err := c.claims.get(uint32(page))
		if err != nil {
			return 0, err
		}
		if value == claimUnclaimed {
			if page&63 == 0 {
				if err := c.checkpoint(); err != nil {
					return 0, err
				}
			}
			page++
			continue
		}
		break
	}
	return page, nil
}

// emit streams one finding through the sink (Rust Context::emit:
// counting first, Stop and sink failures surface as their exact
// classes).
func (c *Context) emit(reason ValidationReason, object ValidationObject, pageNumber *uint32, physicalBytes *PhysicalByteInterval, addressFence *ValidationAddressFence) error {
	if err := c.progress.countFinding(reason); err != nil {
		return err
	}
	finding := ValidationFinding{
		Sequence:      c.progress.FindingCount,
		Reason:        reason,
		Object:        object,
		PageNumber:    pageNumber,
		PhysicalBytes: physicalBytes,
		AddressFence:  addressFence,
	}
	if c.sink == nil {
		return nil
	}
	control, err := c.sink.Finding(&finding)
	if err != nil {
		return &format.Error{Code: format.CodeSinkFailed, Detail: err.Error()}
	}
	if control == SinkStop {
		return &format.Error{Code: format.CodeStoppedBySink, Detail: "validation stopped by sink"}
	}
	return nil
}

func objectHasAddresses(object ValidationObject) bool { return object == ObjectRangeTree }

func partitionBytes(start, end uint64) (PhysicalByteInterval, error) {
	startBytes := start * format.PageSize
	if start != 0 && startBytes/format.PageSize != start {
		return PhysicalByteInterval{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation partition byte offset"}
	}
	endBytes := end * format.PageSize
	if end != 0 && endBytes/format.PageSize != end {
		return PhysicalByteInterval{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation partition byte offset"}
	}
	return PhysicalByteInterval{Start: startBytes, EndExclusive: endBytes}, nil
}
