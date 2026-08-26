package recovery

// Bounded two-file merge sort for recovery-readable direct ranges
// (Rust recovery/external_sort.rs): the readable records stream into
// sorted runs inside the heap record buffer, runs live in the mapped
// scratch files, and fixed passes merge down to one run which streams
// into the output. Every budget refusal and corruption class is the
// Rust class; the scratch cleanup folds through the same terminal.

import (
	"slices"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// externalSortFailure is the failing terminal of one external sort
// (Rust ExternalSortFailure: the cause and the attached scratch
// cleanup).
type externalSortFailure struct {
	cause   error
	cleanup *scratchCleanup
}

// sortRequest is the bounded external-sort request (Rust SortRequest).
type sortRequest struct {
	meta              format.Meta
	budget            *RecoveryBudget
	retainedHeapBytes uint64
	readableRecords   uint64
	check             func() error
	initialArea       *sortArea
}

// sortArea is one scratch region usable as a sort area (Rust
// SortArea).
type sortArea struct {
	slot scratchSlot
	base uint64
}

// newSortArea builds one sort area at the payload base (Rust
// SortArea::new).
func newSortArea(slot scratchSlot, base uint64) sortArea {
	return sortArea{slot: slot, base: base}
}

// sortAndEmit runs the full external sort and streams the sorted
// records into the emit function (Rust sort_and_emit): the bounded
// record buffer, the reset page set, the scratch attempt (reused from
// the page set or started fresh), the run scan, and the merge pass
// chain fold through the scratch cleanup terminal.
func sortAndEmit(codec rangeCodec, m *mapping.Mapping, request sortRequest, pages *pageSet, emit func(rangeRecord) error) (*scratchCleanup, *externalSortFailure) {
	records, err := prepareRecords(codec, request.budget, request.retainedHeapBytes, pages)
	if err != nil {
		return nil, pageFailure(pages, err)
	}
	if err := pages.reset(); err != nil {
		return nil, pageFailure(pages, err)
	}
	scratch := pages.takeScratch()
	if scratch == nil {
		scratch, err = startSortScratch(request.meta, request.budget)
		if err != nil {
			return nil, pageFailure(pages, err)
		}
	}
	if err := scratch.requireExternalSort(); err != nil {
		pages.release(scratch)
		return finishSort(scratch, err)
	}
	first, err := firstSortArea(scratch, request.initialArea)
	if err != nil {
		pages.release(scratch)
		return finishSort(scratch, err)
	}
	workspace := newSortWorkspace()
	var runs runs
	scanErr := scanSortRuns(workspace, codec, m, request.meta, pages, &records, first, request.readableRecords, request.check, scratch, &runs)
	records = nil
	reusable := pages.release(scratch)
	var result error
	if scanErr == nil {
		result = sortRunsAndEmit(workspace, codec, scratch, runs, reusable, request.readableRecords, request.check, emit)
	} else {
		result = scanErr
	}
	return finishSort(scratch, result)
}

// firstSortArea selects the initial sort area (Rust first_area: the
// reuse area from the tables storage or a fresh scratch file, sized
// to the payload base).
func firstSortArea(scratch *scratch, initial *sortArea) (sortArea, error) {
	area := sortArea{}
	if initial != nil {
		area = *initial
	} else {
		slot, err := scratch.create()
		if err != nil {
			return sortArea{}, err
		}
		area = sortArea{slot: slot, base: scratchHeaderSize}
	}
	if err := scratch.resize(area.slot, area.base); err != nil {
		return sortArea{}, err
	}
	return area, nil
}

// prepareRecords allocates the exact bounded record buffer (Rust
// prepare_records: the heap minus the retained facts, at least one
// record, and the retained capacity proof).
func prepareRecords(codec rangeCodec, budget *RecoveryBudget, retainedHeapBytes uint64, pages *pageSet) ([]rangeRecord, error) {
	recordSize := uint64(codec.recordSize())
	available, ok := checkedSub(budget.MaxHeapBytes, retainedHeapBytes)
	if !ok {
		return nil, budgetError("recovery unordered range buffer")
	}
	available, ok = checkedSub(available, pages.retainedBytes())
	if !ok {
		return nil, budgetError("recovery unordered range buffer")
	}
	available, ok = checkedSub(available, recordSize)
	if !ok {
		return nil, budgetError("recovery unordered range buffer")
	}
	capacity := available / recordSize
	capacity++
	if capacity > uint64(maxInt) {
		capacity = uint64(maxInt)
	}
	records := make([]rangeRecord, 0, int(capacity))
	retained := uint64(cap(records)) * recordSize
	if retained > available+recordSize {
		return nil, budgetError("recovery unordered range buffer")
	}
	return records, nil
}

// scanSortRuns scans the mapped source into sorted runs (Rust
// scan_runs): every readable record joins the record buffer, the
// buffer flushes as a sorted run at capacity, and the final flush
// plus the readable-record proof close the scan.
func scanSortRuns(workspace *sortWorkspace, codec rangeCodec, m *mapping.Mapping, meta format.Meta, pages *pageSet, records *[]rangeRecord, first sortArea, readableRecords uint64, check func() error, scratch *scratch, out *runs) error {
	*out = runs{area: first, end: first.base}
	capacity := cap(*records)
	events := &scanSortEvents{
		codec:     codec,
		workspace: workspace,
		records:   records,
		capacity:  capacity,
		scratch:   scratch,
		runs:      out,
		check:     check,
	}
	if err := scanRanges(codec, m, meta, pages, check, events); err != nil {
		return err
	}
	if err := events.flush(); err != nil {
		return err
	}
	if events.seen != readableRecords {
		return candidateChangedError()
	}
	return nil
}

// sortRunsAndEmit merges the scanned runs and streams the final run
// (Rust sort_runs_and_emit: a zero-record source emits nothing).
func sortRunsAndEmit(workspace *sortWorkspace, codec rangeCodec, scratch *scratch, input runs, reusable *scratchSlot, readableRecords uint64, check func() error, emit func(rangeRecord) error) error {
	if readableRecords == 0 {
		return nil
	}
	sorted, err := mergeAllSortRuns(workspace, codec, scratch, input, reusable, check)
	if err != nil {
		return err
	}
	return emitSorted(workspace, codec, scratch, sorted, readableRecords, check, emit)
}

// runs is the run-set state of one merge pass (Rust Runs).
type runs struct {
	area  sortArea
	end   uint64
	count uint64
}

// appendRun sorts the buffered records and frames them as one run
// (Rust Runs::append).
func appendRun(r *runs, workspace *sortWorkspace, codec rangeCodec, scratch *scratch, records []rangeRecord) error {
	if len(records) == 0 {
		return nil
	}
	slices.SortFunc(records, func(left, right rangeRecord) int {
		switch {
		case lessRecord(codec, left, right):
			return -1
		case lessRecord(codec, right, left):
			return 1
		default:
			return 0
		}
	})
	end, err := writeRun(workspace, scratch, codec, r.area.slot, r.end, records)
	if err != nil {
		return err
	}
	r.end = end
	next := r.count + 1
	if next == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch runs"}
	}
	r.count = next
	return nil
}

// scanSortEvents consumes the range scan of one external sort (Rust
// ScanEvents).
type scanSortEvents struct {
	codec     rangeCodec
	workspace *sortWorkspace
	records   *[]rangeRecord
	capacity  int
	scratch   *scratch
	runs      *runs
	check     func() error
	seen      uint64
}

// flush sorts and frames the buffer as one run (Rust ScanEvents::flush).
func (e *scanSortEvents) flush() error {
	if err := appendRun(e.runs, e.workspace, e.codec, e.scratch, *e.records); err != nil {
		return err
	}
	*e.records = (*e.records)[:0]
	return nil
}

func (e *scanSortEvents) pageAccepted() error { return nil }
func (e *scanSortEvents) pageRejected(bool) error {
	return nil
}
func (e *scanSortEvents) unknown(reason validation.ValidationReason, page *uint32, unbounded bool) error {
	return nil
}
func (e *scanSortEvents) rangeEvent(page uint32, record rangeRecordOption) error {
	if !record.ok {
		return nil
	}
	if err := live.Checkpoint(e.check); err != nil {
		return err
	}
	*e.records = append(*e.records, record.value)
	next := e.seen + 1
	if next == 0 {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery readable ranges"}
	}
	e.seen = next
	if len(*e.records) == e.capacity {
		return e.flush()
	}
	return nil
}

// mergeAllSortRuns reduces the run set to one run (Rust merge_all):
// the reusable file slot or a fresh scratch file becomes the output
// area, and every pass swaps the areas until one run remains.
func mergeAllSortRuns(workspace *sortWorkspace, codec rangeCodec, scratch *scratch, input runs, reusable *scratchSlot, check func() error) (sortArea, error) {
	if input.count <= 1 {
		return input.area, nil
	}
	output := sortArea{base: scratchHeaderSize}
	if reusable != nil {
		output.slot = *reusable
	} else {
		slot, err := scratch.create()
		if err != nil {
			return sortArea{}, err
		}
		output.slot = slot
	}
	for input.count > 1 {
		if err := scratch.resize(output.slot, output.base); err != nil {
			return sortArea{}, err
		}
		merged, err := mergeSortPass(workspace, codec, scratch, input, output, check)
		if err != nil {
			return sortArea{}, err
		}
		output = input.area
		input = merged
	}
	return input.area, nil
}

// mergeSortPass merges run pairs of the input area into the output
// area (Rust merge_pass): each merge consumes one or two runs, and
// the pass proves the input was consumed exactly.
func mergeSortPass(workspace *sortWorkspace, codec rangeCodec, scratch *scratch, input runs, output sortArea, check func() error) (runs, error) {
	sourceAt := input.area.base
	destinationAt := output.base
	remainingRuns := input.count
	outputRuns := uint64(0)
	for remainingRuns != 0 {
		if err := live.Checkpoint(check); err != nil {
			return runs{}, err
		}
		left, err := readRun(scratch, codec, input.area.slot, sourceAt)
		if err != nil {
			return runs{}, err
		}
		sourceAt = left.end
		var right *run
		if remainingRuns > 1 {
			nextRun, err := readRun(scratch, codec, input.area.slot, sourceAt)
			if err != nil {
				return runs{}, err
			}
			sourceAt = nextRun.end
			right = &nextRun
		}
		destinationAt, err = mergeRuns(workspace, scratch, codec, input.area.slot, output.slot, destinationAt, left, right, check)
		if err != nil {
			return runs{}, err
		}
		if right != nil {
			remainingRuns -= 2
		} else {
			remainingRuns--
		}
		outputRuns++
	}
	if sourceAt != scratch.length(input.area.slot) {
		return runs{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch run framing has trailing bytes"}
	}
	return runs{area: output, end: destinationAt, count: outputRuns}, nil
}

// emitSorted streams the final sorted run into the output (Rust
// emit_sorted): the run must carry exactly the readable records and
// end at the file length.
func emitSorted(workspace *sortWorkspace, codec rangeCodec, scratch *scratch, area sortArea, expected uint64, check func() error, emit func(rangeRecord) error) error {
	run, err := readRun(scratch, codec, area.slot, area.base)
	if err != nil {
		return err
	}
	if run.count != expected || run.end != scratch.length(area.slot) {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "final recovery scratch run is incomplete"}
	}
	reader := newRunReader(codec, area.slot, run, workspace.left[:])
	for {
		record, err := reader.next(scratch)
		if err != nil {
			return err
		}
		if !record.ok {
			return nil
		}
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		if err := emit(record.value); err != nil {
			return err
		}
	}
}

// finishSort folds the sort result through the scratch cleanup
// terminal (Rust finish: a clean cleanup with a successful result
// returns the cleanup, any failure attaches it, and an unclean
// cleanup becomes the CleanupIncomplete class).
func finishSort(scratch *scratch, result error) (*scratchCleanup, *externalSortFailure) {
	cleanup := scratch.cleanup()
	clean := cleanup.clean()
	switch {
	case result == nil && clean:
		return cleanup, nil
	case !clean:
		cause := cleanupIncompleteError(result, cleanup)
		return nil, &externalSortFailure{cause: cause, cleanup: cleanup}
	default:
		return nil, &externalSortFailure{cause: result, cleanup: cleanup}
	}
}

// startSortScratch starts the scratch attempt of the external sort
// (Rust start_scratch).
func startSortScratch(meta format.Meta, budget *RecoveryBudget) (*scratch, error) {
	if budget.ScratchDirectory == "" {
		return nil, budgetError("recovery unordered ranges")
	}
	return scratchStart(budget.ScratchDirectory, meta, budget.MaxScratchBytes, budget.MaxScratchFiles, budget.MaxOpenFiles)
}

// pageFailure folds one pre-sort failure through the page-set
// terminal (Rust page_failure).
func pageFailure(pages *pageSet, cause error) *externalSortFailure {
	failure := pages.finish(cause)
	return &externalSortFailure{cause: failure.cause, cleanup: failure.cleanup}
}
