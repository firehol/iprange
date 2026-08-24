package recovery

// Direct recovery output policy (Rust recovery/direct_output.rs): one
// accepted component is coalesced with an adjacent equal-value record
// and pushed to the destination builder, and one rejected overlap
// component streams the ranges-rejected count and its fence envelope.

import (
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// directOutput is the direct-range policy of the overlap-component
// pass (Rust DirectOutput): the pending record coalesces adjacent
// equal-value records before the push.
type directOutput struct {
	builder  *writer.OutputBuilder
	rep      *reporter
	codec    rangeCodec
	previous *rangeRecord
}

// resolve proves nothing for the direct policy (Rust
// DirectOutput::resolve: no resolved token).
func (o *directOutput) resolve(record rangeRecord) (any, error) {
	return nil, nil
}

// accept counts one accepted component and coalesces it (Rust
// DirectOutput::accept).
func (o *directOutput) accept(record rangeRecord, resolved any) error {
	if err := o.codec.reportAccepted(o.rep, record); err != nil {
		return err
	}
	return o.coalesce(record)
}

// rejectOverlap streams one whole overlap component (Rust
// DirectOutput::reject_overlap).
func (o *directOutput) rejectOverlap(count uint64, from, to rangeKey) error {
	return reportOverlap(o.rep, o.codec, count, from, to)
}

// finish pushes the pending record (Rust DirectOutput::finish over
// finish_output).
func (o *directOutput) finish() error {
	if o.previous == nil {
		return nil
	}
	record := *o.previous
	o.previous = nil
	return o.codec.pushRecord(o.builder, record)
}

// coalesce merges one record with an adjacent equal-value previous
// record or pushes the previous (Rust DirectOutput::coalesce).
func (o *directOutput) coalesce(record rangeRecord) error {
	if o.previous == nil {
		o.previous = &record
		return nil
	}
	previous := *o.previous
	next, ok := o.codec.nextKey(previous.to)
	if previous.value == record.value && ok && next == record.from {
		previous.to = record.to
		o.previous = &previous
		return nil
	}
	if err := o.codec.pushRecord(o.builder, previous); err != nil {
		return err
	}
	o.previous = &record
	return nil
}

// reportOverlap streams one rejected overlap component (Rust
// report_overlap: the ranges-rejected count then the RangeOverlap
// fence envelope).
func reportOverlap(rep *reporter, codec rangeCodec, count uint64, from, to rangeKey) error {
	if err := codec.reportRejected(rep, count, from, to); err != nil {
		return err
	}
	fence := codec.fence(from, to)
	return rep.unknown(unknownEnvelope{
		reason:       validation.ReasonRangeOverlap,
		object:       validation.ObjectRangeTree,
		addressFence: &fence,
	})
}
