package recovery

// Recovery range build (Rust recovery/range_build.rs): one ordered or
// unordered range output is produced from the mapped source and the
// page-ownership set. The ordered arm streams the scan directly into
// the output policy; the unordered arm buffers the readable records,
// sorts them in memory inside the heap budget, and streams the sorted
// run. The authorized multi-pass scratch sort is the recorded
// chunk-4-10 follow-up, so the heap-exceeded unordered build refuses
// with the unordered-ranges class exactly like the Rust heap-only arm.

import (
	"errors"
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// maxInt is the widest platform int (the Rust usize clamp peer of the
// bounded resize refusals).
const maxInt = int(^uint(0) >> 1)

// rangeBuild is one range output request (Rust RangeBuild).
type rangeBuild struct {
	mapping           *mapping.Mapping
	meta              format.Meta
	budget            *RecoveryBudget
	check             func() error
	readableRecords   uint64
	ordered           bool
	retainedHeapBytes uint64
}

// rangeBuildFailure is one failed range build (Rust BuildFailure): the
// cause and the scratch cleanup authority of the retained pages (nil
// in the heap-only arm).
type rangeBuildFailure struct {
	cause   error
	scratch any
}

// rangeOutput consumes the produced record stream (Rust RangeOutput).
type rangeOutput interface {
	push(record rangeRecord) error
	finish() error
}

// buildRanges produces the range output (Rust build_ranges: the
// ordered scan streams directly, the unordered records are sorted in
// memory).
func buildRanges(codec rangeCodec, request rangeBuild, pages *pageSet, output rangeOutput) (any, *rangeBuildFailure) {
	if request.ordered {
		return buildOrdered(codec, request, pages, output)
	}
	return buildSorted(codec, request, pages, output)
}

// buildOrdered streams the scan into the output (Rust build_ordered:
// the page set reset, the ordered events, the readable-record proof,
// and the output finish fold through the page-set terminal).
func buildOrdered(codec rangeCodec, request rangeBuild, pages *pageSet, output rangeOutput) (any, *rangeBuildFailure) {
	scan := func() error {
		if err := pages.reset(); err != nil {
			return err
		}
		events := newBuildEvents(codec, true, func(record rangeRecord) error {
			return output.push(record)
		})
		if err := scanRanges(codec, request.mapping, request.meta, pages, request.check, events); err != nil {
			return err
		}
		if err := requireCount(events.readableRecords, request.readableRecords); err != nil {
			return err
		}
		return output.finish()
	}()
	return finishPages(pages, scan)
}

// buildSorted sorts the readable records inside the heap budget (Rust
// build_sorted: the retained heap proof, then the in-memory sort, or
// the unordered-ranges refusal when the heap cannot hold the buffer;
// the multi-pass scratch sort is the recorded follow-up).
func buildSorted(codec rangeCodec, request rangeBuild, pages *pageSet, output rangeOutput) (any, *rangeBuildFailure) {
	retained, ok := checkedAdd(request.retainedHeapBytes, pages.retainedBytes())
	if !ok {
		return finishPages(pages, overflowError("recovery retained heap"))
	}
	_, err := bufferFits(codec, request.readableRecords, retained, request.budget)
	switch {
	case err == nil:
		return buildInMemory(codec, request, retained, pages, output)
	case isBudgetExceeded(err):
		// The Rust authority routes the heap-exceeded build to the
		// authorized multi-pass scratch sort, which is the recorded
		// chunk-4-10 follow-up; the heap-only build refuses with the
		// exact unordered-ranges class of the Rust heap-only arm.
		return finishPages(pages, budgetError("recovery unordered ranges"))
	default:
		return finishPages(pages, err)
	}
}

// buildInMemory sorts the readable records in one bounded heap buffer
// and streams them into the output (Rust build_in_memory).
func buildInMemory(codec rangeCodec, request rangeBuild, retained uint64, pages *pageSet, output rangeOutput) (any, *rangeBuildFailure) {
	available, ok := checkedSub(request.budget.MaxHeapBytes, retained)
	if !ok {
		return finishPages(pages, budgetError("recovery unordered ranges"))
	}
	records, err := reserveRecords(codec, request.readableRecords, available)
	if err != nil {
		return finishPages(pages, err)
	}
	scan := func() error {
		if err := pages.reset(); err != nil {
			return err
		}
		events := newBuildEvents(codec, false, func(record rangeRecord) error {
			records = append(records, record)
			return nil
		})
		if err := scanRanges(codec, request.mapping, request.meta, pages, request.check, events); err != nil {
			return err
		}
		if err := requireCount(events.readableRecords, request.readableRecords); err != nil {
			return err
		}
		sort.Slice(records, func(i, j int) bool {
			return lessRecord(codec, records[i], records[j])
		})
		return nil
	}()
	if scan != nil {
		records = nil
		return finishPages(pages, scan)
	}
	scratch, failure := finishPages(pages, nil)
	if failure != nil {
		return nil, failure
	}
	for _, record := range records {
		if err := output.push(record); err != nil {
			return afterCleanup(err, scratch)
		}
	}
	if err := output.finish(); err != nil {
		return afterCleanup(err, scratch)
	}
	return scratch, nil
}

// bufferFits proves the unordered record buffer fits the heap (Rust
// buffer_fits: the record count times the family record size, plus the
// retained heap, inside the budget; arithmetic refusals keep the Rust
// classes).
func bufferFits(codec rangeCodec, records uint64, retained uint64, budget *RecoveryBudget) (uint64, error) {
	bytes, ok := checkedMul(records, uint64(codec.recordSize()))
	if !ok {
		return 0, overflowError("recovery range buffer")
	}
	total, ok := checkedAdd(bytes, retained)
	if !ok || total > budget.MaxHeapBytes {
		return 0, budgetError("recovery unordered ranges")
	}
	return bytes, nil
}

// reserveRecords allocates the exact unordered record buffer inside
// the retained bound (Rust reserve: the length conversion and the
// capacity-proof refusals keep the unordered-ranges class; Go make
// allocates the exact length, so the capacity recheck is the length
// bound itself).
func reserveRecords(codec rangeCodec, records uint64, maxRetainedBytes uint64) ([]rangeRecord, error) {
	if records > uint64(maxInt) {
		return nil, budgetError("recovery unordered ranges")
	}
	output := make([]rangeRecord, 0, int(records))
	retained := uint64(cap(output)) * uint64(codec.recordSize())
	if retained > maxRetainedBytes {
		return nil, budgetError("recovery unordered ranges")
	}
	return output, nil
}

// requireCount proves the scanned record count (Rust require_count:
// any difference is the candidate-changed class).
func requireCount(actual, expected uint64) error {
	if actual == expected {
		return nil
	}
	return candidateChangedError()
}

// analyzeRanges analyzes one range tree into the readable-records
// count and the order proof (Rust analyze_ranges: every page and
// envelope streams to the reporter; the order proof flips on the first
// from-regression of the readable stream).
func analyzeRanges(codec rangeCodec, m *mapping.Mapping, meta format.Meta, pages *pageSet, check func() error, rep *reporter) (uint64, bool, error) {
	events := &analysisEvents{rep: rep, codec: codec}
	if err := scanRanges(codec, m, meta, pages, check, events); err != nil {
		return 0, false, err
	}
	return events.readableRecords, events.ordered, nil
}

// analysisEvents wires one recovery range scan into the reporter (Rust
// AnalysisEvents: page and envelope events stream, the readable
// records count, and the examined ranges count with their no-bounds
// rejections).
type analysisEvents struct {
	rep             *reporter
	codec           rangeCodec
	previousFrom    *rangeKey
	readableRecords uint64
	ordered         bool
}

func (e *analysisEvents) pageAccepted() error {
	return e.rep.pageAccepted()
}

func (e *analysisEvents) pageRejected(ioUnreadable bool) error {
	return e.rep.pageRejected(ioUnreadable)
}

func (e *analysisEvents) unknown(reason validation.ValidationReason, page *uint32, unbounded bool) error {
	var interval *validation.PhysicalByteInterval
	if page != nil {
		value := pageInterval(*page)
		interval = &value
	}
	return e.rep.unknown(unknownEnvelope{
		reason:             reason,
		object:             validation.ObjectRangeTree,
		pageNumber:         page,
		physicalBytes:      interval,
		hasUnboundedExtent: unbounded,
	})
}

func (e *analysisEvents) rangeEvent(page uint32, record *rangeRecord) error {
	if err := e.rep.rangeExamined(); err != nil {
		return err
	}
	if record == nil {
		return e.rep.rangeRejectedWithoutBounds()
	}
	if e.previousFrom != nil && !e.codec.lessKey(record.from, *e.previousFrom) {
		e.ordered = false
	}
	previous := record.from
	e.previousFrom = &previous
	next := e.readableRecords + 1
	if next == 0 {
		return overflowError("recovery readable ranges")
	}
	e.readableRecords = next
	return nil
}

// buildEvents consumes the range scan of one range build (Rust
// Events): the page envelopes are no-ops, the readable stream feeds
// the emit function, and an order regression in the ordered arm is the
// candidate-changed class.
type buildEvents struct {
	codec           rangeCodec
	ordered         bool
	previousFrom    *rangeKey
	readableRecords uint64
	emit            func(rangeRecord) error
}

func newBuildEvents(codec rangeCodec, ordered bool, emit func(rangeRecord) error) *buildEvents {
	return &buildEvents{codec: codec, ordered: ordered, emit: emit}
}

func (e *buildEvents) pageAccepted() error {
	return nil
}

func (e *buildEvents) pageRejected(ioUnreadable bool) error {
	return nil
}

func (e *buildEvents) unknown(reason validation.ValidationReason, page *uint32, unbounded bool) error {
	return nil
}

func (e *buildEvents) rangeEvent(page uint32, record *rangeRecord) error {
	if record == nil {
		return nil
	}
	if e.ordered && e.previousFrom != nil && !e.codec.lessKey(record.from, *e.previousFrom) {
		return candidateChangedError()
	}
	previous := record.from
	e.previousFrom = &previous
	next := e.readableRecords + 1
	if next == 0 {
		return overflowError("recovery readable ranges")
	}
	e.readableRecords = next
	return e.emit(*record)
}

// retainedMetadataBytes is the heap retained by the analyzed metadata
// payload (Rust retained_metadata_bytes: the exact allocated length of
// the decompressed payload; absent metadata retains nothing).
func retainedMetadataBytes(metadata []byte) uint64 {
	if metadata == nil {
		return 0
	}
	return uint64(len(metadata))
}

// writeMetadata stages the analyzed metadata into the destination
// builder (Rust write_metadata: an absent payload stages nothing, the
// available heap is the budget minus the retained bytes, and the
// writer authority enforces the compression bound).
func writeMetadata(builder *writer.OutputBuilder, metadata []byte, maxHeapBytes, retainedHeapBytes uint64) error {
	if metadata == nil {
		return nil
	}
	available, ok := checkedSub(maxHeapBytes, retainedHeapBytes)
	if !ok {
		return budgetError("recovery metadata compression")
	}
	return builder.WriteMetadataWithBudget(metadata, available)
}

// finishPages folds the build result through the page-set terminal
// (Rust finish_pages: the failing terminal carries the cause and the
// scratch cleanup authority).
func finishPages(pages *pageSet, result error) (any, *rangeBuildFailure) {
	failure := pages.finish(result)
	if failure.cause != nil {
		return nil, &rangeBuildFailure{cause: failure.cause, scratch: failure.cleanup}
	}
	return failure.cleanup, nil
}

// afterCleanup wraps one post-scan failure with the page-set scratch
// (Rust after_cleanup).
func afterCleanup(cause error, scratch any) (any, *rangeBuildFailure) {
	return nil, &rangeBuildFailure{cause: cause, scratch: scratch}
}

// budgetError builds the fixed budget-exceeded class.
func budgetError(detail string) error {
	return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: detail}
}

// overflowError builds the fixed arithmetic-overflow class.
func overflowError(detail string) error {
	return &format.Error{Code: format.CodeArithmeticOverflow, Detail: detail}
}

// isBudgetExceeded reports whether one failure is the budget-exceeded
// class (the Rust Error::BudgetExceeded match arm).
func isBudgetExceeded(cause error) bool {
	var full *format.Error
	if !errors.As(cause, &full) {
		return false
	}
	return full.Code == format.CodeInsufficientResourceBudget
}

// checkedAdd folds one checked addition.
func checkedAdd(left, right uint64) (uint64, bool) {
	total := left + right
	return total, total >= left
}

// checkedSub folds one checked subtraction.
func checkedSub(left, right uint64) (uint64, bool) {
	if left < right {
		return 0, false
	}
	return left - right, true
}

// checkedMul folds one checked multiplication.
func checkedMul(left, right uint64) (uint64, bool) {
	if left != 0 && right > ^uint64(0)/left {
		return 0, false
	}
	return left * right, true
}
