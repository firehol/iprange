package recovery

// Fixed-width buffered run framing, reading, writing, and merging
// (Rust recovery/external_sort/streams.rs): every run is "IPR4RUN1"
// plus a little-endian u64 count, followed by the fixed-width scratch
// records; reads and writes travel 4096-byte buffers through the
// mapped scratch file.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

const (
	runHeaderSize = 16
	runBufferSize = 4096
)

// runMagic is the exact run header magic (Rust RUN_MAGIC).
var runMagic = [8]byte{'I', 'P', 'R', '4', 'R', 'U', 'N', '1'}

// sortWorkspace is the reusable buffered I/O arena of one external
// sort (Rust keeps the run reader and writer buffers on its stack;
// Go cannot prove stack residence through the codec interface, so one
// bounded workspace is allocated once per sort instead of per run).
type sortWorkspace struct {
	writer [runBufferSize]byte
	left   [runBufferSize]byte
	right  [runBufferSize]byte
}

// newSortWorkspace builds the fixed 12 KiB I/O arena of one sort.
func newSortWorkspace() *sortWorkspace {
	return &sortWorkspace{}
}

// run is one framed run inside a scratch file (Rust Run).
type run struct {
	recordsAt uint64
	end       uint64
	count     uint64
}

// writeRun frames and writes one sorted run (Rust write_run).
func writeRun(workspace *sortWorkspace, scratch *scratch, codec rangeCodec, slot scratchSlot, at uint64, records []rangeRecord) (uint64, error) {
	if err := writeRunHeader(scratch, slot, at, uint64(len(records))); err != nil {
		return 0, err
	}
	writer := newRecordWriter(workspace, codec, slot, at+runHeaderSize)
	for _, record := range records {
		if err := writer.push(scratch, record); err != nil {
			return 0, err
		}
	}
	return writer.finish(scratch)
}

// mergeRuns merges one or two input runs into one output run (Rust
// merge_runs).
func mergeRuns(workspace *sortWorkspace, scratch *scratch, codec rangeCodec, input, output scratchSlot, at uint64, left run, right *run, check func() error) (uint64, error) {
	count := left.count
	if right != nil {
		total, ok := checkedAdd(left.count, right.count)
		if !ok {
			return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "merged recovery scratch run"}
		}
		count = total
	}
	if err := writeRunHeader(scratch, output, at, count); err != nil {
		return 0, err
	}
	writer := newRecordWriter(workspace, codec, output, at+runHeaderSize)
	leftReader := newRunReader(codec, input, left, workspace.left[:])
	leftRecord, err := leftReader.next(scratch)
	if err != nil {
		return 0, err
	}
	if right != nil {
		if err := mergePair(workspace, scratch, codec, input, *right, check, &writer, &leftReader, &leftRecord); err != nil {
			return 0, err
		}
	} else if err := copyLeftRun(scratch, codec, check, &writer, &leftReader, leftRecord); err != nil {
		return 0, err
	}
	return writer.finish(scratch)
}

// mergePair streams the smaller leading record of two run readers
// (Rust merge_pair).
func mergePair(workspace *sortWorkspace, scratch *scratch, codec rangeCodec, input scratchSlot, right run, check func() error, writer *recordWriter, left *runReader, leftRecord *rangeRecordOption) error {
	rightReader := newRunReader(codec, input, right, workspace.right[:])
	rightRecord, err := rightReader.next(scratch)
	if err != nil {
		return err
	}
	leftOption := *leftRecord
	for {
		takeLeft := chooseRunRecord(codec, leftOption, rightRecord)
		if !takeLeft.ok {
			break
		}
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		if takeLeft.left {
			if err := writer.push(scratch, leftOption.value); err != nil {
				return err
			}
			leftOption, err = left.next(scratch)
			if err != nil {
				return err
			}
		} else {
			if err := writer.push(scratch, rightRecord.value); err != nil {
				return err
			}
			rightRecord, err = rightReader.next(scratch)
			if err != nil {
				return err
			}
		}
	}
	*leftRecord = leftOption
	return nil
}

// runChoice selects the smaller of two optional records (Rust
// choose: the ok flag carries the None separation, the left flag
// selects the left run).
type runChoice struct {
	ok   bool
	left bool
}

// chooseRunRecord selects the smaller of two optional records (Rust
// choose over record_order: the left run wins when its record orders
// at most the right record).
func chooseRunRecord(codec rangeCodec, left, right rangeRecordOption) runChoice {
	switch {
	case left.ok && right.ok:
		return runChoice{ok: true, left: !lessRecord(codec, right.value, left.value)}
	case left.ok:
		return runChoice{ok: true, left: true}
	case right.ok:
		return runChoice{ok: true, left: false}
	default:
		return runChoice{ok: false}
	}
}

// copyLeftRun streams the remainder of the left run (Rust copy_left).
func copyLeftRun(scratch *scratch, codec rangeCodec, check func() error, writer *recordWriter, left *runReader, record rangeRecordOption) error {
	for record.ok {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		if err := writer.push(scratch, record.value); err != nil {
			return err
		}
		next, err := left.next(scratch)
		if err != nil {
			return err
		}
		record = next
	}
	return nil
}

// writeRunHeader frames one run header (Rust write_header).
func writeRunHeader(scratch *scratch, slot scratchSlot, at uint64, count uint64) error {
	var header [runHeaderSize]byte
	copy(header[0:8], runMagic[:])
	format.PutU64(header[8:16], count)
	return scratch.write(slot, at, header[:])
}

// readRun validates and decodes one run header (Rust read_run).
func readRun(scratch *scratch, codec rangeCodec, slot scratchSlot, at uint64) (run, error) {
	var header [runHeaderSize]byte
	if err := scratch.read(slot, at, header[:]); err != nil {
		return run{}, err
	}
	if string(header[0:8]) != string(runMagic[:]) {
		return run{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch run header is malformed"}
	}
	count := format.U64(header[8:16])
	if count == 0 {
		return run{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch run is empty"}
	}
	recordsAt, ok := checkedAdd(at, runHeaderSize)
	if !ok {
		return run{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch run"}
	}
	bytes, ok := checkedMul(count, uint64(codec.scratchRecordSize()))
	if !ok {
		return run{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch run"}
	}
	end, ok := checkedAdd(recordsAt, bytes)
	if !ok {
		return run{}, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch run"}
	}
	if end > scratch.length(slot) {
		return run{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch run exceeds its file"}
	}
	return run{recordsAt: recordsAt, end: end, count: count}, nil
}

// runReader streams the fixed-width records of one run through one
// 4096-byte buffer of the sort workspace (Rust RunReader: the Rust
// buffers are stack values; Go keeps one bounded workspace per sort
// instead of per-run buffers).
type runReader struct {
	codec     rangeCodec
	size      int
	slot      scratchSlot
	nextAt    uint64
	remaining uint64
	buffer    []byte
	buffered  int
	index     int
}

// newRunReader opens one run reader (Rust RunReader::new): the
// caller supplies the workspace buffer, so the two readers of one
// merge never alias each other, and the value stays on the caller
// stack (no per-merge-op allocation).
func newRunReader(codec rangeCodec, slot scratchSlot, run run, buffer []byte) runReader {
	return runReader{
		codec:     codec,
		size:      codec.scratchRecordSize(),
		slot:      slot,
		nextAt:    run.recordsAt,
		remaining: run.count,
		buffer:    buffer,
	}
}

// next decodes the next record of the run (Rust RunReader::next).
func (r *runReader) next(scratch *scratch) (rangeRecordOption, error) {
	if r.remaining == 0 {
		return rangeRecordOption{}, nil
	}
	if r.index == r.buffered {
		if err := r.fill(scratch); err != nil {
			return rangeRecordOption{}, err
		}
	}
	size := r.size
	start := r.index * size
	r.index++
	r.remaining--
	return rangeRecordOption{value: r.codec.decodeScratch(r.buffer[start : start+size]), ok: true}, nil
}

// fill loads the next bounded batch (Rust RunReader::fill).
func (r *runReader) fill(scratch *scratch) error {
	size := uint64(r.size)
	capacity := uint64(runBufferSize) / size
	count := r.remaining
	if count > capacity {
		count = capacity
	}
	bytes := count * size
	if err := scratch.read(r.slot, r.nextAt, r.buffer[:bytes]); err != nil {
		return err
	}
	next, ok := checkedAdd(r.nextAt, bytes)
	if !ok {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch read"}
	}
	r.nextAt = next
	r.buffered = int(count)
	r.index = 0
	return nil
}

// recordWriter frames the fixed-width records of one run through one
// 4096-byte buffer of the sort workspace (Rust RecordWriter).
type recordWriter struct {
	codec   rangeCodec
	size    int
	slot    scratchSlot
	nextAt  uint64
	buffer  []byte
	records int
}

// newRecordWriter opens one run writer (Rust RecordWriter::new).
func newRecordWriter(workspace *sortWorkspace, codec rangeCodec, slot scratchSlot, nextAt uint64) recordWriter {
	return recordWriter{
		codec:  codec,
		size:   codec.scratchRecordSize(),
		slot:   slot,
		nextAt: nextAt,
		buffer: workspace.writer[:],
	}
}

// push encodes one record into the buffer, flushing when full (Rust
// RecordWriter::push).
func (w *recordWriter) push(scratch *scratch, record rangeRecord) error {
	size := w.size
	if (w.records+1)*size > runBufferSize {
		if err := w.flush(scratch); err != nil {
			return err
		}
	}
	start := w.records * size
	w.codec.encodeScratch(record, w.buffer[start:start+size])
	w.records++
	return nil
}

// finish flushes and reports the next write position (Rust
// RecordWriter::finish).
func (w *recordWriter) finish(scratch *scratch) (uint64, error) {
	if err := w.flush(scratch); err != nil {
		return 0, err
	}
	return w.nextAt, nil
}

// flush writes the buffered records (Rust RecordWriter::flush).
func (w *recordWriter) flush(scratch *scratch) error {
	size := w.size
	bytes := w.records * size
	if bytes == 0 {
		return nil
	}
	if err := scratch.write(w.slot, w.nextAt, w.buffer[:bytes]); err != nil {
		return err
	}
	next, ok := checkedAdd(w.nextAt, uint64(bytes))
	if !ok {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch write"}
	}
	w.nextAt = next
	w.records = 0
	return nil
}
