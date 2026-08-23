// Complete direct replacement and timestamp refresh workflows (Rust
// live_writer/direct_workflow.rs parity): the exact unordered direct-map
// replacement and the first-seen/last-seen full-snapshot refreshes over
// the internal range workflow, ending in the shared FinishedWorkflow
// terminal with the replacement report.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// DirectReplacement is one complete unordered direct-map replacement
// (Rust DirectReplacement): the input ranges carry direct values and
// overwrite the whole map at finish.
type DirectReplacement struct {
	w     *Writer
	state *directWorkflowState
}

// FirstSeenRefresh is one complete unordered first-seen refresh (Rust
// FirstSeenRefresh): covered addresses keep their old value, newly
// covered addresses stamp the refresh value.
type FirstSeenRefresh struct {
	w            *Writer
	state        *directWorkflowState
	refreshValue uint32
}

// LastSeenRefresh is one complete unordered last-seen refresh (Rust
// LastSeenRefresh): covered addresses refresh to at least the refresh
// value, recent absence survives, and absence at or below the cutoff
// expires.
type LastSeenRefresh struct {
	w            *Writer
	state        *directWorkflowState
	refreshValue uint32
	cutoff       uint32
}

// directWorkflowState is the borrow-free exact-direct workflow state
// (Rust ExactDirectState): the workflow kind, the record counter, and
// the per-family input. The input kind is fixed at begin: replacement
// workflows own the assignment input, timestamp workflows own the
// coverage union input.
type directWorkflowState struct {
	w            *Writer
	cancellation *CancellationToken
	workflow     WorkflowKind
	inputRecords uint64
	assignment   *writer.AssignmentInput
	coverage     *writer.UnionInput
	edit         *writer.WriterEdit
}

// BeginDirectReplacement begins a complete direct-map replacement on a
// clean direct writer (Rust begin_direct_replacement).
func (w *Writer) BeginDirectReplacement(cancellation *CancellationToken) (*DirectReplacement, error) {
	state, err := w.beginExactDirectState(WorkflowDirectReplacement, cancellation)
	if err != nil {
		return nil, err
	}
	return &DirectReplacement{w: w, state: state}, nil
}

// BeginFirstSeenRefresh begins a full-snapshot refresh on an exact
// first_seen database (Rust begin_first_seen_refresh).
func (w *Writer) BeginFirstSeenRefresh(refreshValue uint32, cancellation *CancellationToken) (*FirstSeenRefresh, error) {
	state, err := w.beginTimestampState(DirectSemanticFirstSeen, WorkflowFirstSeenRefresh, cancellation)
	if err != nil {
		return nil, err
	}
	return &FirstSeenRefresh{w: w, state: state, refreshValue: refreshValue}, nil
}

// BeginLastSeenRefresh begins a full-snapshot refresh on an exact
// last_seen database (Rust begin_last_seen_refresh).
func (w *Writer) BeginLastSeenRefresh(refreshValue, cutoff uint32, cancellation *CancellationToken) (*LastSeenRefresh, error) {
	state, err := w.beginTimestampState(DirectSemanticLastSeen, WorkflowLastSeenRefresh, cancellation)
	if err != nil {
		return nil, err
	}
	return &LastSeenRefresh{w: w, state: state, refreshValue: refreshValue, cutoff: cutoff}, nil
}

// beginTimestampState runs the timestamp preconditions then the shared
// exact-direct begin (Rust begin_timestamp_state): the database must be
// direct and carry exactly the requested semantic tag.
func (w *Writer) beginTimestampState(semantic DirectSemantic, workflow WorkflowKind, cancellation *CancellationToken) (*directWorkflowState, error) {
	if w.core == nil {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if w.core.BaseInfo().ValueKind != format.ValueKindDirect {
		return nil, &format.Error{Code: format.CodeWrongValueKind, Detail: "timestamp refresh requires a direct database"}
	}
	info, err := w.Info()
	if err != nil {
		return nil, err
	}
	actual, ok := info.DirectSemantic()
	if !ok || actual != semantic {
		return nil, &format.Error{Code: format.CodeWrongValueTag, Detail: "timestamp refresh requires its exact value tag"}
	}
	return w.beginExactDirectState(workflow, cancellation)
}

// requireDirectReady is the direct-workflow precondition shared by every
// begin: a healthy writer with no pending draft (Rust
// require_healthy inside begin_exact_direct_state; the draft check runs
// in Core.BeginRangeWorkflow through BeginTransaction).
func (w *Writer) requireDirectReady() error {
	if w.core == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if err := w.core.Healthy(); err != nil {
		return err
	}
	if w.core.BaseInfo().ValueKind != format.ValueKindDirect {
		return &format.Error{Code: format.CodeWrongValueKind, Detail: "exact direct workflow requires a direct database"}
	}
	return nil
}

// beginExactDirectState mirrors Rust begin_exact_direct_state: the
// direct-kind precondition, the cancellation checkpoint, the range
// workflow draft, and the input state for the workflow kind.
func (w *Writer) beginExactDirectState(workflow WorkflowKind, cancellation *CancellationToken) (*directWorkflowState, error) {
	if err := w.requireDirectReady(); err != nil {
		return nil, err
	}
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	if err := w.core.BeginRangeWorkflow(); err != nil {
		return nil, err
	}
	edit, err := w.core.BindEdit()
	if err != nil {
		return nil, err
	}
	family := w.core.BaseInfo().AddressFamily
	state := &directWorkflowState{
		w:            w,
		cancellation: cancellation,
		workflow:     workflow,
		edit:         edit,
	}
	switch workflow {
	case WorkflowFirstSeenRefresh, WorkflowLastSeenRefresh:
		coverage := writer.NewUnionInput(family, format.ValueKindDirect, w.core.Budget().MaxHeapBytes)
		state.coverage = &coverage
	default:
		assignment := writer.NewAssignmentInput(family, w.core.Budget().MaxHeapBytes)
		state.assignment = &assignment
	}
	return state, nil
}

// AddRangesV4 streams one IPv4 direct-replacement batch (Rust
// DirectReplacement::add_ranges_v4_slice).
func (r *DirectReplacement) AddRangesV4(ranges []DirectRangeV4) error {
	return r.state.addDirectV4(ranges)
}

// AddRangesV6 streams one IPv6 direct-replacement batch (Rust
// DirectReplacement::add_ranges_v6_slice).
func (r *DirectReplacement) AddRangesV6(ranges []DirectRangeV6) error {
	return r.state.addDirectV6(ranges)
}

// FinishInput finishes normalization, comparison, and changed-root
// preparation (Rust DirectReplacement::finish_input).
func (r *DirectReplacement) FinishInput() (*FinishedWorkflow, error) {
	if err := r.state.requireReplacement(); err != nil {
		return nil, err
	}
	r.state.releaseInput()
	return r.state.finishReplacement()
}

// AddRangesV4 streams one IPv4 refresh batch (Rust
// FirstSeenRefresh::add_ranges_v4_slice).
func (f *FirstSeenRefresh) AddRangesV4(ranges []AddressRange4) error {
	return f.state.addTimestampV4(ranges, f.refreshValue)
}

// AddRangesV6 streams one IPv6 refresh batch (Rust
// FirstSeenRefresh::add_ranges_v6_slice).
func (f *FirstSeenRefresh) AddRangesV6(ranges []AddressRange6) error {
	return f.state.addTimestampV6(ranges, f.refreshValue)
}

// FinishInput preserves old values on current coverage and finishes the
// exact refresh (Rust FirstSeenRefresh::finish_input).
func (f *FirstSeenRefresh) FinishInput() (*FinishedWorkflow, error) {
	return f.state.finishFirstSeen(f.refreshValue)
}

// FinishInputWithRemovalsV4 finishes the exact refresh while streaming
// bounded batches of removed IPv4 first-seen intervals (Rust
// FirstSeenRefresh::finish_input_with_removals_v4). The batch slice is
// borrowed for each synchronous sink call and must not be retained;
// sink errors abort the workflow unchanged (Rust sink error contract).
func (f *FirstSeenRefresh) FinishInputWithRemovalsV4(sink FirstSeenRemoval4Sink) (*FinishedWorkflow, error) {
	var scratch []FirstSeenRemoval4
	internal := writer.FirstSeenRemoval4Sink(func(removals []writer.FirstSeenRemoval4) error {
		scratch = scratch[:0]
		for _, r := range removals {
			scratch = append(scratch, FirstSeenRemoval4{From: IPv4(r.From), To: IPv4(r.To), FirstSeen: r.FirstSeen, Addresses: r.Addresses})
		}
		return sink(scratch)
	})
	return f.state.finishFirstSeenWithRemovals4(f.refreshValue, internal)
}

// FinishInputWithRemovalsV6 finishes the exact refresh while streaming
// bounded batches of removed IPv6 first-seen intervals (Rust
// FirstSeenRefresh::finish_input_with_removals_v6). The batch slice is
// borrowed for each synchronous sink call and must not be retained;
// sink errors abort the workflow unchanged (Rust sink error contract).
func (f *FirstSeenRefresh) FinishInputWithRemovalsV6(sink FirstSeenRemoval6Sink) (*FinishedWorkflow, error) {
	var scratch []FirstSeenRemoval6
	internal := writer.FirstSeenRemoval6Sink(func(removals []writer.FirstSeenRemoval6) error {
		scratch = scratch[:0]
		for _, r := range removals {
			scratch = append(scratch, FirstSeenRemoval6{FromHi: r.FromHi, FromLo: r.FromLo, ToHi: r.ToHi, ToLo: r.ToLo, FirstSeen: r.FirstSeen, Addresses: r.Addresses})
		}
		return sink(scratch)
	})
	return f.state.finishFirstSeenWithRemovals6(f.refreshValue, internal)
}

// AddRangesV4 streams one IPv4 refresh batch (Rust
// LastSeenRefresh::add_ranges_v4_slice).
func (l *LastSeenRefresh) AddRangesV4(ranges []AddressRange4) error {
	return l.state.addTimestampV4(ranges, l.refreshValue)
}

// AddRangesV6 streams one IPv6 refresh batch (Rust
// LastSeenRefresh::add_ranges_v6_slice).
func (l *LastSeenRefresh) AddRangesV6(ranges []AddressRange6) error {
	return l.state.addTimestampV6(ranges, l.refreshValue)
}

// FinishInput refreshes current coverage, retains recent absence, and
// expires old absence (Rust LastSeenRefresh::finish_input).
func (l *LastSeenRefresh) FinishInput() (*FinishedWorkflow, error) {
	return l.state.finishLastSeen(l.refreshValue, l.cutoff)
}

// addDirectV4 mirrors Rust ExactDirectState::add_direct_v4: the input
// family gate, the replacement-input kind gate, and the assignment-input
// drain with the per-record ordering check.
func (s *directWorkflowState) addDirectV4(ranges []DirectRangeV4) error {
	if err := s.requireInputFamily(format.AddressFamilyIPv4); err != nil {
		return err
	}
	if s.assignment == nil {
		return s.w.abortAfter(&format.Error{Code: format.CodeWrongState, Detail: "direct replacement address family changed"})
	}
	work.SourcePass(1)
	work.InputSourcePass(1)
	if err := s.cancellation.check(); err != nil {
		return s.w.abortAfter(err)
	}
	for chunkStart := 0; chunkStart < len(ranges); chunkStart += 4096 {
		if chunkStart != 0 {
			if err := s.cancellation.check(); err != nil {
				return s.w.abortAfter(err)
			}
		}
		chunkEnd := chunkStart + 4096
		if chunkEnd > len(ranges) {
			chunkEnd = len(ranges)
		}
		for record := chunkStart; record < chunkEnd; record++ {
			r := ranges[record]
			next, err := s.nextInputRecord()
			if err != nil {
				return s.w.abortAfter(err)
			}
			if r.From > r.To {
				return s.w.abortAfter(&format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"})
			}
			if _, err := s.edit.AssignInputV4(r.From, r.To, r.Value, s.assignment); err != nil {
				return s.w.abortAfter(err)
			}
			work.RangeConsumed(1)
			s.inputRecords = next
		}
	}
	if len(ranges) != 0 {
		if err := s.cancellation.check(); err != nil {
			return s.w.abortAfter(err)
		}
	}
	return nil
}

// addDirectV6 mirrors Rust ExactDirectState::add_direct_v6 (IPv6).
func (s *directWorkflowState) addDirectV6(ranges []DirectRangeV6) error {
	if err := s.requireInputFamily(format.AddressFamilyIPv6); err != nil {
		return err
	}
	if s.assignment == nil {
		return s.w.abortAfter(&format.Error{Code: format.CodeWrongState, Detail: "direct replacement address family changed"})
	}
	work.SourcePass(1)
	work.InputSourcePass(1)
	if err := s.cancellation.check(); err != nil {
		return s.w.abortAfter(err)
	}
	for chunkStart := 0; chunkStart < len(ranges); chunkStart += 4096 {
		if chunkStart != 0 {
			if err := s.cancellation.check(); err != nil {
				return s.w.abortAfter(err)
			}
		}
		chunkEnd := chunkStart + 4096
		if chunkEnd > len(ranges) {
			chunkEnd = len(ranges)
		}
		for record := chunkStart; record < chunkEnd; record++ {
			r := ranges[record]
			next, err := s.nextInputRecord()
			if err != nil {
				return s.w.abortAfter(err)
			}
			if r.FromHi > r.ToHi || (r.FromHi == r.ToHi && r.FromLo > r.ToLo) {
				return s.w.abortAfter(&format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"})
			}
			if _, err := s.edit.AssignInputV6(r.FromHi, r.FromLo, r.ToHi, r.ToLo, r.Value, s.assignment); err != nil {
				return s.w.abortAfter(err)
			}
			work.RangeConsumed(1)
			s.inputRecords = next
		}
	}
	if len(ranges) != 0 {
		if err := s.cancellation.check(); err != nil {
			return s.w.abortAfter(err)
		}
	}
	return nil
}

// addTimestampV4 mirrors Rust ExactDirectState::add_timestamp_v4: the
// general input takes the ordinary assignment path, otherwise the
// constant range pushes into the private tree.
func (s *directWorkflowState) addTimestampV4(ranges []AddressRange4, value uint32) error {
	if err := s.requireInputFamily(format.AddressFamilyIPv4); err != nil {
		return err
	}
	if s.coverage == nil {
		return s.w.abortAfter(&format.Error{Code: format.CodeWrongState, Detail: "timestamp workflow address family changed"})
	}
	work.SourcePass(1)
	work.InputSourcePass(1)
	if err := s.cancellation.check(); err != nil {
		return s.w.abortAfter(err)
	}
	for chunkStart := 0; chunkStart < len(ranges); chunkStart += 4096 {
		if chunkStart != 0 {
			if err := s.cancellation.check(); err != nil {
				return s.w.abortAfter(err)
			}
		}
		chunkEnd := chunkStart + 4096
		if chunkEnd > len(ranges) {
			chunkEnd = len(ranges)
		}
		for record := chunkStart; record < chunkEnd; record++ {
			r := ranges[record]
			next, err := s.nextInputRecord()
			if err != nil {
				return s.w.abortAfter(err)
			}
			if r.From > r.To {
				return s.w.abortAfter(&format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"})
			}
			if s.coverage.IsGeneral() {
				if _, err := s.edit.AssignV4(uint32(r.From), uint32(r.To), value); err != nil {
					return s.w.abortAfter(err)
				}
			} else if err := s.edit.AddPrivateConstantRangeV4(uint32(r.From), uint32(r.To), value, s.coverage); err != nil {
				return s.w.abortAfter(err)
			}
			work.RangeConsumed(1)
			s.inputRecords = next
		}
	}
	if len(ranges) != 0 {
		if err := s.cancellation.check(); err != nil {
			return s.w.abortAfter(err)
		}
	}
	return nil
}

// addTimestampV6 mirrors Rust ExactDirectState::add_timestamp_v6 (IPv6).
func (s *directWorkflowState) addTimestampV6(ranges []AddressRange6, value uint32) error {
	if err := s.requireInputFamily(format.AddressFamilyIPv6); err != nil {
		return err
	}
	if s.coverage == nil {
		return s.w.abortAfter(&format.Error{Code: format.CodeWrongState, Detail: "timestamp workflow address family changed"})
	}
	work.SourcePass(1)
	work.InputSourcePass(1)
	if err := s.cancellation.check(); err != nil {
		return s.w.abortAfter(err)
	}
	for chunkStart := 0; chunkStart < len(ranges); chunkStart += 4096 {
		if chunkStart != 0 {
			if err := s.cancellation.check(); err != nil {
				return s.w.abortAfter(err)
			}
		}
		chunkEnd := chunkStart + 4096
		if chunkEnd > len(ranges) {
			chunkEnd = len(ranges)
		}
		for record := chunkStart; record < chunkEnd; record++ {
			r := ranges[record]
			next, err := s.nextInputRecord()
			if err != nil {
				return s.w.abortAfter(err)
			}
			if r.FromHi > r.ToHi || (r.FromHi == r.ToHi && r.FromLo > r.ToLo) {
				return s.w.abortAfter(&format.Error{Code: format.CodeInvalidArgument, Detail: "range start exceeds range end"})
			}
			if s.coverage.IsGeneral() {
				if _, err := s.edit.AssignV6(r.FromHi, r.FromLo, r.ToHi, r.ToLo, value); err != nil {
					return s.w.abortAfter(err)
				}
			} else if err := s.edit.AddPrivateConstantRangeV6(r.FromHi, r.FromLo, r.ToHi, r.ToLo, value, s.coverage); err != nil {
				return s.w.abortAfter(err)
			}
			work.RangeConsumed(1)
			s.inputRecords = next
		}
	}
	if len(ranges) != 0 {
		if err := s.cancellation.check(); err != nil {
			return s.w.abortAfter(err)
		}
	}
	return nil
}

// requireInputFamily mirrors Rust require_input_family: the input must
// be active and the database family must match; a mismatch aborts the
// workflow through the writer.
func (s *directWorkflowState) requireInputFamily(family uint8) error {
	if err := s.requireActive(); err != nil {
		return err
	}
	if s.w.core.BaseInfo().AddressFamily != family {
		return s.w.abortAfter(&format.Error{Code: format.CodeWrongAddressFamily, Detail: "range family does not match the database"})
	}
	return nil
}

// requireActive mirrors Rust require_input_active: healthy writer with
// an open workflow input.
func (s *directWorkflowState) requireActive() error {
	if s.w.core == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if err := s.w.core.Healthy(); err != nil {
		return err
	}
	if !s.w.core.WorkflowInputOpen() {
		return &format.Error{Code: format.CodeWrongState, Detail: "workflow input is not active"}
	}
	return nil
}

// requireReplacement is the Rust finish_replacement_state workflow-kind
// gate: only replacement workflows finish through the replacement path.
func (s *directWorkflowState) requireReplacement() error {
	if err := s.requireActive(); err != nil {
		return err
	}
	if s.workflow == WorkflowFirstSeenRefresh || s.workflow == WorkflowLastSeenRefresh {
		return s.w.abortAfter(&format.Error{Code: format.CodeWrongState, Detail: "timestamp refresh requires its refresh parameters"})
	}
	return nil
}

// releaseInput mirrors Rust AssignmentInput::release: the eager
// assignment input releases its pending locator pages.
func (s *directWorkflowState) releaseInput() {
	if s.assignment != nil {
		s.assignment.Release()
	}
}

// finishReplacement mirrors Rust finish_replacement_state: the
// replacement report from the full map comparison, then the shared
// workflow completion.
func (s *directWorkflowState) finishReplacement() (*FinishedWorkflow, error) {
	comparison, err := s.w.core.CompareMaps(s.cancellation.check)
	if err != nil {
		return nil, s.w.abortAfter(err)
	}
	afterIntervals := s.w.core.Draft().Meta().RangeRecordCount
	report := s.replacementReport(comparison, afterIntervals, afterIntervals, comparison.After)
	return completeDirectWorkflow(s.w, report, s.cancellation)
}

// finishFirstSeen mirrors Rust finish_first_seen_state: seal the
// constant ranges, merge with the first-seen policy, and complete.
func (s *directWorkflowState) finishFirstSeen(refreshValue uint32) (*FinishedWorkflow, error) {
	if err := s.requireFirstSeen(); err != nil {
		return nil, err
	}
	return s.finishTimestamp(func(edit *writer.WriterEdit) (writer.TimestampMerge, error) {
		return edit.MergeFirstSeen(refreshValue, s.cancellation.check)
	})
}

// finishFirstSeenWithRemovals4 mirrors Rust
// finish_first_seen_with_removals_v4_state: the first-seen preconditions
// and the IPv4 family gate, then the timestamp finish streaming every
// removed interval through the sink.
func (s *directWorkflowState) finishFirstSeenWithRemovals4(refreshValue uint32, sink writer.FirstSeenRemoval4Sink) (*FinishedWorkflow, error) {
	if err := s.requireFirstSeen(); err != nil {
		return nil, err
	}
	if err := s.requireInputFamily(format.AddressFamilyIPv4); err != nil {
		return nil, err
	}
	return s.finishTimestamp(func(edit *writer.WriterEdit) (writer.TimestampMerge, error) {
		return edit.MergeFirstSeenWithRemovals4(refreshValue, sink, s.cancellation.check)
	})
}

// finishFirstSeenWithRemovals6 mirrors Rust
// finish_first_seen_with_removals_v6_state (IPv6).
func (s *directWorkflowState) finishFirstSeenWithRemovals6(refreshValue uint32, sink writer.FirstSeenRemoval6Sink) (*FinishedWorkflow, error) {
	if err := s.requireFirstSeen(); err != nil {
		return nil, err
	}
	if err := s.requireInputFamily(format.AddressFamilyIPv6); err != nil {
		return nil, err
	}
	return s.finishTimestamp(func(edit *writer.WriterEdit) (writer.TimestampMerge, error) {
		return edit.MergeFirstSeenWithRemovals6(refreshValue, sink, s.cancellation.check)
	})
}

// finishLastSeen mirrors Rust finish_last_seen_state: seal the constant
// ranges, merge with the last-seen policy, and complete.
func (s *directWorkflowState) finishLastSeen(refreshValue, cutoff uint32) (*FinishedWorkflow, error) {
	if err := s.requireActive(); err != nil {
		return nil, err
	}
	if s.workflow != WorkflowLastSeenRefresh {
		return nil, s.w.abortAfter(&format.Error{Code: format.CodeWrongState, Detail: "workflow is not a last-seen refresh"})
	}
	return s.finishTimestamp(func(edit *writer.WriterEdit) (writer.TimestampMerge, error) {
		return edit.MergeLastSeen(refreshValue, cutoff, s.cancellation.check)
	})
}

// requireFirstSeen is the Rust finish_first_seen_state precondition.
func (s *directWorkflowState) requireFirstSeen() error {
	if err := s.requireActive(); err != nil {
		return err
	}
	if s.workflow != WorkflowFirstSeenRefresh {
		return s.w.abortAfter(&format.Error{Code: format.CodeWrongState, Detail: "workflow is not a first-seen refresh"})
	}
	return nil
}

// finishTimestamp mirrors Rust ExactDirectState::finish_timestamp: seal
// the private constant ranges, run the timestamp merge supplied by the
// caller, and complete with the timestamp report. Every merge error
// aborts the workflow through the writer (Rust mutate -> abort_after),
// discarding the draft so no partially merged tree can be committed.
func (s *directWorkflowState) finishTimestamp(merge func(edit *writer.WriterEdit) (writer.TimestampMerge, error)) (*FinishedWorkflow, error) {
	if err := s.w.core.Mutate(func(edit *writer.WriterEdit) error {
		if s.coverage == nil {
			return &format.Error{Code: format.CodeWrongState, Detail: "timestamp workflow has direct replacement input"}
		}
		return edit.FinishPrivateConstantRanges(s.coverage)
	}); err != nil {
		return nil, s.w.abortAfter(err)
	}
	var merged writer.TimestampMerge
	err := s.w.core.Mutate(func(edit *writer.WriterEdit) error {
		var err error
		merged, err = merge(edit)
		return err
	})
	if err != nil {
		return nil, s.w.abortAfter(err)
	}
	afterIntervals := s.w.core.Draft().Meta().RangeRecordCount
	report := s.replacementReport(merged.Comparison, merged.InputIntervals, afterIntervals, merged.InputAddresses)
	return completeDirectWorkflow(s.w, report, s.cancellation)
}

// replacementReport builds the exact replacement report (Rust
// WorkflowReport::replacement): the before/after interval counts come
// from the caller (the scanned comparison or the merge), and the
// address classes come from the comparison.
func (s *directWorkflowState) replacementReport(comparison writer.Comparison, inputIntervals, afterIntervals uint64, inputAddresses Cardinality129) *WorkflowReport {
	beforeIntervals := s.w.core.BaseInfo().RangeRecordCount
	logical := classifyComparison(comparison)
	return &WorkflowReport{
		Workflow:                     s.workflow,
		LogicalChange:                logical,
		InputRecordCount:             s.inputRecords,
		InputNormalizedIntervalCount: inputIntervals,
		BeforeRangeRecordCount:       beforeIntervals,
		AfterRangeRecordCount:        afterIntervals,
		InputAddresses:               inputAddresses,
		BeforeAddresses:              comparison.Before,
		AfterAddresses:               comparison.After,
		UnchangedValueAddresses:      comparison.Unchanged,
		ChangedValueAddresses:        comparison.Changed,
		AddedAddresses:               comparison.Added,
		RemovedAddresses:             comparison.Removed,
	}
}

// nextInputRecord reserves the next input record counter before the
// record is applied and the caller charges RangeConsumed and stores the
// counter after the apply, mirroring Rust drain_source per-record order.
func (s *directWorkflowState) nextInputRecord() (uint64, error) {
	next := s.inputRecords + 1
	if next == 0 {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "workflow input record count"}
	}
	return next, nil
}

// completeDirectWorkflow mirrors Rust complete_workflow for direct
// workflows: a no-change report discards the draft and returns the clean
// terminal; a changed report retires the base through
// finish_direct_workflow and returns the prepared terminal.
func completeDirectWorkflow(w *Writer, report *WorkflowReport, cancellation *CancellationToken) (*FinishedWorkflow, error) {
	if report.LogicalChange == LogicalNoChange {
		if err := w.core.DiscardUnpublished(); err != nil {
			w.core.MarkUnresolved(err)
			return nil, err
		}
		return &FinishedWorkflow{w: w, report: *report, changed: false}, nil
	}
	if err := w.core.Mutate(func(edit *writer.WriterEdit) error {
		return edit.FinishDirectWorkflow(cancellation.check)
	}); err != nil {
		return nil, w.abortAfter(err)
	}
	return &FinishedWorkflow{w: w, report: *report, changed: true, cancellation: cancellation}, nil
}
