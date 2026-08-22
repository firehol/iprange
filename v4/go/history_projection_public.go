// Public history projection surface (Rust
// live_writer/history_projection.rs parity): one last-seen direct
// source is scanned once in canonical order and merged into named
// destination feeds of a membership database, producing exact
// before/after window statistics. The writer workflow mirrors Rust
// project_history_state: the feed-workflow preconditions, the source
// compatibility checks, the exact-workflow draft, the one-pass source
// drive with corruption gating, and the changed/no-change terminal.
// The changed terminal is one prepared handle (DirectTransaction
// precedent) that owns the draft until Commit, Abort, or Writer.Close,
// with the cancellation token captured at prepare and checked through
// commit (Rust PreparedState). The live source mode is refused until
// Milestone 4 exactly like the snapshot live refusal.

package iprangedb

import (
	"errors"
	"math"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/work"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// HistoryProjectionSourceKind is the source coordination of one history
// projection (Rust HistoryProjectionSource). The live mode is refused
// until Milestone 4 with ErrorOSUnsupported.
type HistoryProjectionSourceKind uint8

const (
	// HistoryProjectionSourceImmutable projects the committed
	// generation of one immutable database path.
	HistoryProjectionSourceImmutable HistoryProjectionSourceKind = iota
	// HistoryProjectionSourceLive would project one live database
	// generation through the sidecar coordination; the Go SDK refuses
	// it for now (Rust Unsupported, milestone-4 scope).
	HistoryProjectionSourceLive
)

// HistoryProjectionSource is the source of one history projection
// (Rust HistoryProjectionSource). Only the immutable mode is accepted;
// Live reports ErrorOSUnsupported.
type HistoryProjectionSource struct {
	Kind   HistoryProjectionSourceKind
	Reader *ImmutableReader
}

// ProjectHistory projects one last-seen direct source into named
// destination feeds of this membership writer (Rust
// LiveWriter::project_history): every source range is consumed exactly
// once in ascending order, every requested window feed is ensured on
// the destination, and the report carries the exact aggregate and
// per-window statistics. The changed handle records the cancellation
// token for the prepared commit; cancel the token to stop the
// projection between bounded units of work.
func (w *Writer) ProjectHistory(source HistoryProjectionSource, windows []HistoryWindow, cancellation *CancellationToken) (*FinishedHistoryProjection, error) {
	if w.core == nil {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	// require_feed_workflow_ready (Rust feed_workflow.rs): healthy, a
	// membership destination, no pending transaction.
	if err := w.core.Healthy(); err != nil {
		return nil, err
	}
	if w.core.BaseInfo().ValueKind != format.ValueKindMembership {
		return nil, &format.Error{Code: format.CodeWrongValueKind, Detail: "named-feed workflow requires a membership database"}
	}
	if w.core.HasDraft() {
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "a writer transaction is already pending"}
	}
	if len(windows) == 0 || uint64(len(windows)) > math.MaxUint32 {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "history window count is invalid"}
	}
	// Source::new + require_compatible_source (Rust history_projection.rs).
	if source.Kind == HistoryProjectionSourceLive {
		return nil, &Error{Code: ErrorOSUnsupported, Detail: "live history source requires the live sidecar coordination (Milestone 4)"}
	}
	if source.Kind != HistoryProjectionSourceImmutable || source.Reader == nil {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "history projection source is invalid"}
	}
	info, err := source.Reader.Info()
	if err != nil {
		return nil, err
	}
	if info.ValueKind != ValueKindDirect {
		return nil, &format.Error{Code: format.CodeWrongValueKind, Detail: "history projection requires a direct source"}
	}
	if semantic, ok := info.DirectSemantic(); !ok || semantic != DirectSemanticLastSeen {
		return nil, &format.Error{Code: format.CodeWrongValueTag, Detail: "history projection requires a last_seen direct source"}
	}
	if uint8(info.Family) != w.core.BaseInfo().AddressFamily {
		return nil, &format.Error{Code: format.CodeWrongAddressFamily, Detail: "history projection source family differs"}
	}
	sourceDevice, sourceInode, err := source.Reader.FileIdentity()
	if err != nil {
		return nil, err
	}
	writerDevice, writerInode, err := w.core.FileIdentity()
	if err != nil {
		return nil, err
	}
	if sourceDevice == writerDevice && sourceInode == writerInode {
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "history projection source and destination are the same file"}
	}
	if err := cancellation.check(); err != nil {
		return nil, err
	}
	if err := w.core.BeginMembershipWorkflow(); err != nil {
		return nil, err
	}
	internalWindows := make([]writer.HistoryWindow, len(windows))
	for index, window := range windows {
		internalWindows[index] = writer.HistoryWindow{FeedName: string(window.FeedName), Cutoff: window.Cutoff}
	}
	projection, err := w.core.BeginHistoryProjection(internalWindows, cancellation.check)
	if err != nil {
		return nil, w.abortAfter(err)
	}
	report, err := w.driveHistoryProjection(source.Reader, info, projection, cancellation)
	if err != nil {
		return nil, err
	}
	return w.finishHistoryProjection(report, cancellation)
}

// driveHistoryProjection scans the source direct ranges once in
// ascending order, feeding each canonical range into the projection
// merge and charging the exact Rust work counters: one input source
// pass and one source pass per projection (Rust project_history), the
// fixed-tree cursor open charges its own source pass, and each
// consumed range is charged by the reader cursor itself (Rust
// range_cursor::next; the drive does not charge ranges again).
func (w *Writer) driveHistoryProjection(src *ImmutableReader, info DatabaseInfo, projection *writer.HistoryProjection, cancellation *CancellationToken) (*writer.HistoryProjectionReport, error) {
	sourceRangeCount := uint64(0)
	sourceAddresses := format.CardinalityZero()
	work.InputSourcePass(1)
	if info.Family == AddressFamilyIPv4 {
		cursor, err := src.inner.NewDirectCursor4(reader.RangeForward)
		if err != nil {
			return nil, w.abortAfterSource(err)
		}
		work.SourcePass(1)
		var previous histPrevious4
		for {
			current, ok, err := cursor.Next()
			if err != nil {
				return nil, w.abortAfterSource(err)
			}
			if !ok {
				break
			}
			if !canonicalSource4(previous, current) {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source last_seen ranges are not canonical"})
			}
			if err := projection.Push4(current.From, current.To, current.Value, cancellation.check); err != nil {
				return nil, w.abortAfter(err)
			}
			if sourceRangeCount == math.MaxUint64 {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "source range count"})
			}
			sourceRangeCount++
			size, err := format.IPv4Inclusive(current.From, current.To)
			if err != nil {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "source address count"})
			}
			sourceAddresses, err = sourceAddresses.Add(size)
			if err != nil {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "source address count"})
			}
			previous = histPrevious4{set: true, from: current.From, to: current.To, value: current.Value}
		}
	} else {
		cursor, err := src.inner.NewDirectCursor6(reader.RangeForward)
		if err != nil {
			return nil, w.abortAfterSource(err)
		}
		work.SourcePass(1)
		var previous histPrevious6
		for {
			current, ok, err := cursor.Next()
			if err != nil {
				return nil, w.abortAfterSource(err)
			}
			if !ok {
				break
			}
			if !canonicalSource6(previous, current) {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source last_seen ranges are not canonical"})
			}
			if err := projection.Push6(current.FromHi, current.FromLo, current.ToHi, current.ToLo, current.Value, cancellation.check); err != nil {
				return nil, w.abortAfter(err)
			}
			if sourceRangeCount == math.MaxUint64 {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "source range count"})
			}
			sourceRangeCount++
			size, err := format.IPv6Inclusive(current.FromHi, current.FromLo, current.ToHi, current.ToLo)
			if err != nil {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "source address count"})
			}
			sourceAddresses, err = sourceAddresses.Add(size)
			if err != nil {
				return nil, w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "source address count"})
			}
			previous = histPrevious6{set: true, fromHi: current.FromHi, fromLo: current.FromLo, toHi: current.ToHi, toLo: current.ToLo, value: current.Value}
		}
	}
	if sourceRangeCount != info.RangeRecordCount {
		return nil, w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source last_seen range count disagrees"})
	}
	report, err := projection.Finish(sourceRangeCount, sourceAddresses, cancellation.check)
	if err != nil {
		return nil, w.abortAfter(err)
	}
	return report, nil
}

// canonicalSource4 reports one IPv4 source range is strictly canonical
// after the previous one (Rust require_canonical_source): ascending
// ordered, non-overlapping, and same-value adjacent ranges merge.
func canonicalSource4(previous histPrevious4, current reader.DirectRange4) bool {
	if current.From > current.To {
		return false
	}
	if previous.set {
		if previous.from >= current.From || previous.to >= current.From {
			return false
		}
		if previous.value == current.Value && previous.to != math.MaxUint32 && previous.to+1 == current.From {
			return false
		}
	}
	return true
}

// canonicalSource6 reports one IPv6 source range is strictly canonical
// after the previous one (Rust require_canonical_source): ascending
// ordered, non-overlapping, and same-value adjacent ranges merge.
func canonicalSource6(previous histPrevious6, current reader.DirectRange6) bool {
	if current.FromHi > current.ToHi || (current.FromHi == current.ToHi && current.FromLo > current.ToLo) {
		return false
	}
	if previous.set {
		if previous.fromHi > current.FromHi || (previous.fromHi == current.FromHi && previous.fromLo >= current.FromLo) ||
			previous.toHi > current.FromHi || (previous.toHi == current.FromHi && previous.toLo >= current.FromLo) {
			return false
		}
		if previous.value == current.Value {
			next := previous.toLo + 1
			carry := next == 0
			nextHi := previous.toHi
			if carry {
				nextHi = previous.toHi + 1
			}
			if previous.toHi != math.MaxUint64 || previous.toLo != math.MaxUint64 {
				if nextHi == current.FromHi && next == current.FromLo {
					return false
				}
			}
		}
	}
	return true
}

// histPrevious4 is the previous canonical source range of the IPv4
// drive (Rust Option<DirectRange>).
type histPrevious4 struct {
	set   bool
	from  uint32
	to    uint32
	value uint32
}

// histPrevious6 is the previous canonical source range of the IPv6
// drive (Rust Option<DirectRange>).
type histPrevious6 struct {
	set    bool
	fromHi uint64
	fromLo uint64
	toHi   uint64
	toLo   uint64
	value  uint32
}

// finishHistoryProjection assembles the terminal handle (Rust
// finish_state): a no-change report discards the draft and returns the
// clean handle; a changed report finishes the membership workflow and
// returns the prepared handle with the cancellation captured.
func (w *Writer) finishHistoryProjection(report *writer.HistoryProjectionReport, cancellation *CancellationToken) (*FinishedHistoryProjection, error) {
	public := publicHistoryReport(report)
	if report.LogicalChange == writer.LogicalNoChange {
		if err := w.core.DiscardUnpublished(); err != nil {
			return nil, err
		}
		return &FinishedHistoryProjection{w: w, report: public, cancellation: cancellation}, nil
	}
	if err := w.core.Mutate(func(edit *writer.WriterEdit) error {
		return edit.FinishMembershipWorkflow(cancellation.check)
	}); err != nil {
		return nil, w.abortAfter(err)
	}
	return &FinishedHistoryProjection{w: w, report: public, changed: true, cancellation: cancellation}, nil
}

// publicHistoryReport copies the internal projection report onto the
// value-free public report type.
func publicHistoryReport(report *writer.HistoryProjectionReport) HistoryProjectionReport {
	out := HistoryProjectionReport{
		LogicalChange:       LogicalChange(report.LogicalChange),
		SourceRangeCount:    report.SourceRangeCount,
		SourceAddresses:     report.SourceAddresses,
		CreatedFeedCount:    report.CreatedFeedCount,
		BeforeIntervalCount: report.BeforeIntervalCount,
		AfterIntervalCount:  report.AfterIntervalCount,
		BeforeAddresses:     report.BeforeAddresses,
		AfterAddresses:      report.AfterAddresses,
		UnchangedAddresses:  report.UnchangedAddresses,
		AddedAddresses:      report.AddedAddresses,
		RemovedAddresses:    report.RemovedAddresses,
	}
	for _, window := range report.Windows {
		out.Windows = append(out.Windows, HistoryWindowReport{
			FeedName:            []byte(window.FeedName),
			Cutoff:              window.Cutoff,
			Created:             window.Created,
			BeforeIntervalCount: window.BeforeIntervalCount,
			AfterIntervalCount:  window.AfterIntervalCount,
			BeforeAddresses:     window.BeforeAddresses,
			AfterAddresses:      window.AfterAddresses,
			UnchangedAddresses:  window.UnchangedAddresses,
			AddedAddresses:      window.AddedAddresses,
			RemovedAddresses:    window.RemovedAddresses,
		})
	}
	return out
}

// abortAfterSource mirrors Rust LiveWriter::abort_after_source: discard
// the draft and report the cause wrapped in the TransactionAborted
// class; when the discard itself fails, brand the writer unresolved and
// nest the CleanupInProgress class (Rust CleanupIncomplete) around the
// cause.
func (w *Writer) abortAfterSource(cause error) error {
	discardErr := w.core.DiscardUnpublished()
	inner := cause
	if discardErr != nil {
		w.core.MarkUnresolved(discardErr)
		inner = &abortError{
			class: &format.Error{Code: format.CodeCleanupInProgress, Detail: "history projection discard failed"},
			cause: cause,
		}
	}
	return &abortError{
		class: &format.Error{Code: format.CodeTransactionAborted, Detail: "history projection aborted"},
		cause: inner,
	}
}

// abortAfter mirrors Rust LiveWriter::abort_after: abort_after_source,
// then brand the writer unusable when the cause is a fatal class (Rust
// Io/Format/Corrupt).
func (w *Writer) abortAfter(cause error) error {
	result := w.abortAfterSource(cause)
	var typed *format.Error
	if errors.As(cause, &typed) && (typed.Code == format.CodeIO || typed.Code == format.CodeFormatInvalid) {
		w.core.MarkUnresolved(typed)
	}
	return result
}

// FinishedHistoryProjection is the terminal of one history projection
// (Rust FinishedHistoryProjection collapsed to one Go handle, the
// DirectTransaction precedent). Every operation works on both variants
// except Commit, SetMetadataJSON, and ClearMetadataJSON, which require
// the changed variant; Abort on a no-change result reports
// ErrorNoPendingTransaction (Rust FinishedHistoryProjection::abort
// parity). The changed handle owns the draft until Commit, Abort, or
// Writer.Close.
type FinishedHistoryProjection struct {
	w            *Writer
	report       HistoryProjectionReport
	changed      bool
	spent        bool
	cancellation *CancellationToken
}

// IsChanged reports whether the projection produced a logical change
// (Rust FinishedHistoryProjection::Changed vs NoChange).
func (h *FinishedHistoryProjection) IsChanged() bool {
	return h.changed
}

// Report returns the exact projection report for both variants (Rust
// FinishedHistoryProjection::report).
func (h *FinishedHistoryProjection) Report() HistoryProjectionReport {
	return h.report
}

// requireChangedActive gates the changed-variant-only operations: the
// handle must be the changed variant, not spent, and the writer must
// still own the draft.
func (h *FinishedHistoryProjection) requireChangedActive() error {
	if !h.changed {
		return &format.Error{Code: format.CodeWrongState, Detail: "history projection did not change"}
	}
	if h.spent || h.w.core == nil || h.w.core.Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "history projection is no longer active"}
	}
	return nil
}

// SetMetadataJSON stages one exact metadata replacement in the changed
// projection (Rust PreparedHistoryProjection::set_metadata_json): the
// same 20 MiB cap and single-stage rule as a direct transaction, with
// the captured cancellation checked before and after the stage.
func (h *FinishedHistoryProjection) SetMetadataJSON(input []byte) (bool, error) {
	if err := h.requireChangedActive(); err != nil {
		return false, err
	}
	if err := h.cancellation.check(); err != nil {
		h.spent = true
		return false, h.w.abortAfter(err)
	}
	changed, err := h.w.core.SetMetadata(input)
	if err != nil {
		h.spent = true
		return false, h.w.abortAfter(err)
	}
	if err := h.cancellation.check(); err != nil {
		h.spent = true
		return false, h.w.abortAfter(err)
	}
	return changed, nil
}

// ClearMetadataJSON stages metadata absence in the changed projection
// (Rust PreparedHistoryProjection::clear_metadata_json); an
// already-absent database reports false.
func (h *FinishedHistoryProjection) ClearMetadataJSON() (bool, error) {
	if err := h.requireChangedActive(); err != nil {
		return false, err
	}
	if err := h.cancellation.check(); err != nil {
		h.spent = true
		return false, h.w.abortAfter(err)
	}
	changed, err := h.w.core.ClearMetadata()
	if err != nil {
		h.spent = true
		return false, h.w.abortAfter(err)
	}
	if err := h.cancellation.check(); err != nil {
		h.spent = true
		return false, h.w.abortAfter(err)
	}
	return changed, nil
}

// Commit publishes the changed projection (Rust
// PreparedHistoryProjection::commit): the DirectTransaction commit
// sequence with the captured cancellation checked at prepare and during
// publication (Rust commit_with). An unchanged draft is discarded and
// reports ErrorNoPendingTransaction.
func (h *FinishedHistoryProjection) Commit() (CommitResult, error) {
	if err := h.requireChangedActive(); err != nil {
		return CommitResult{}, err
	}
	draft := h.w.core.Draft()
	if !draft.Changed() {
		if err := h.w.core.DiscardUnpublished(); err != nil {
			h.spent = true
			return CommitResult{}, err
		}
		h.spent = true
		return CommitResult{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	attempt, err := h.w.core.CommitAttempt()
	if err != nil {
		h.spent = true
		return CommitResult{}, err
	}
	// Rust commit_with prepare_and_lock: check, prepare, check, then
	// the sidecar lock (Go noop).
	if err := h.cancellation.check(); err != nil {
		return h.abortAfter(attempt, err), nil
	}
	if err := h.w.core.Prepare(h.cancellation.check); err != nil {
		return h.abortAfter(attempt, err), nil
	}
	if err := h.cancellation.check(); err != nil {
		return h.abortAfter(attempt, err), nil
	}
	if err := h.w.core.RequireDraftLength(); err != nil {
		return h.abortAfter(attempt, err), nil
	}
	if err := h.w.core.RequireUnchangedBase(); err != nil {
		return h.abortAfter(attempt, err), nil
	}
	res := h.w.core.Publish(h.cancellation.check)
	h.spent = true
	result := CommitResult{DatabaseID: attempt.DatabaseID, TransactionID: attempt.TransactionID, CommitNonce: attempt.CommitNonce, Err: res.Err}
	switch res.Status {
	case writer.PublishCommitted:
		result.Status = CommitCommitted
	case writer.PublishBeforePublication:
		result.Status = CommitNotCommitted
	default:
		result.Status = CommitOutcomeUnknown
	}
	return result, nil
}

// abortAfter reports an aborted prepared commit exactly like the direct
// transaction commit abort (Rust commit_with abort_after): the result
// error class is TransactionAborted, and a failed abandonment discard
// nests the CleanupInProgress class.
func (h *FinishedHistoryProjection) abortAfter(attempt writer.CommitAttempt, cause error) CommitResult {
	discardErr := h.w.core.DiscardUnpublished()
	h.spent = true
	inner := cause
	if discardErr != nil {
		h.w.core.MarkUnresolved(discardErr)
		inner = &abortError{
			class: &format.Error{Code: format.CodeCleanupInProgress, Detail: "history projection commit discard failed"},
			cause: cause,
		}
	}
	return CommitResult{
		Status:        CommitNotCommitted,
		DatabaseID:    attempt.DatabaseID,
		TransactionID: attempt.TransactionID,
		CommitNonce:   attempt.CommitNonce,
		Err: &abortError{
			class: &format.Error{Code: format.CodeTransactionAborted, Detail: "history projection commit aborted after a preparation failure"},
			cause: inner,
		},
	}
}

// Abort discards the changed projection draft; the writer stays open
// and healthy (Rust PreparedHistoryProjection::abort). A no-change
// result is already clean and reports ErrorNoPendingTransaction (Rust
// FinishedHistoryProjection::abort parity).
func (h *FinishedHistoryProjection) Abort() error {
	if h.spent {
		return &format.Error{Code: format.CodeWrongState, Detail: "history projection is no longer active"}
	}
	h.spent = true
	if !h.changed {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	if h.w.core == nil || h.w.core.Draft() == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "history projection is no longer active"}
	}
	return h.w.core.DiscardUnpublished()
}
