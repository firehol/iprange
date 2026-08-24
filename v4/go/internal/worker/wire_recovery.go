//go:build linux && amd64

// Recovery-mode wire codecs (Rust worker/wire_recovery.rs): the
// recovery request (source/destination paths, candidate, worker mode,
// budget, output attempt, unreadable pages, delivered unknowns), the
// completed/failed outcome envelope with the retained publication
// problem, the streamed unknown-damage envelope, the recovery report,
// and the optional scratch attempt. Every field order and corrupt
// detail mirrors the Rust authority. The Go recovery terminal types
// are fully exported, so the codecs compose them directly.

package worker

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// WorkerMode selects the coordination binding of one worker recovery
// (Rust recovery::WorkerMode; the wire tags are Immutable 1, Offline 2,
// Live 3).
type WorkerMode uint8

const (
	WorkerModeImmutable WorkerMode = 1
	WorkerModeOffline   WorkerMode = 2
	WorkerModeLive      WorkerMode = 3
)

// RecoveryRequest is the decoded recovery request (Rust
// wire_recovery.rs Request).
type RecoveryRequest struct {
	SourcePath        string
	DestinationPath   string
	Candidate         recovery.RecoveryCandidate
	Mode              WorkerMode
	Budget            recovery.RecoveryBudget
	Output            publication.PrivateOutputAttempt
	UnreadablePages   []uint32
	DeliveredUnknowns uint64
}

// WriteRecoveryRequest writes one recovery request (Rust
// wire_recovery::write_request: paths, candidate, mode, budget, output
// attempt, unreadable-page list, delivered envelope count).
func WriteRecoveryRequest(control *Control, sourcePath, destinationPath string, candidate *recovery.RecoveryCandidate, mode WorkerMode, budget *recovery.RecoveryBudget, output *publication.PrivateOutputAttempt, unreadablePages []uint32, deliveredUnknowns uint64) error {
	tag, ok := workerModeTag(mode)
	if !ok {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "worker recovery mode is invalid"}
	}
	w := NewWireWriter(control)
	if err := w.Path(sourcePath); err != nil {
		return err
	}
	if err := w.Path(destinationPath); err != nil {
		return err
	}
	if err := writeRecoveryCandidate(w, candidate); err != nil {
		return err
	}
	if err := w.Byte(tag); err != nil {
		return err
	}
	if err := writeRecoveryBudget(w, budget); err != nil {
		return err
	}
	if err := writePrivateOutput(w, output); err != nil {
		return err
	}
	if err := writeU32List(w, unreadablePages, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "unreadable source-page list"}); err != nil {
		return err
	}
	if err := w.U64(deliveredUnknowns); err != nil {
		return err
	}
	return w.Finish()
}

// ReadRecoveryRequest decodes one recovery request (Rust
// wire_recovery::read_request; the unreadable-page list charges its
// byte footprint against the budget's heap allowance).
func ReadRecoveryRequest(control *Control) (*RecoveryRequest, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	request := &RecoveryRequest{}
	if request.SourcePath, err = r.Path(); err != nil {
		return nil, err
	}
	if request.DestinationPath, err = r.Path(); err != nil {
		return nil, err
	}
	if request.Candidate, err = readRecoveryCandidateValue(r); err != nil {
		return nil, err
	}
	modeTag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	mode, ok := workerModeFromTag(modeTag)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker recovery mode is invalid"}
	}
	request.Mode = mode
	if request.Budget, err = readRecoveryBudget(r); err != nil {
		return nil, err
	}
	if request.Output, err = readPrivateOutput(r); err != nil {
		return nil, err
	}
	if request.UnreadablePages, err = readU32List(r, &request.Budget.MaxHeapBytes); err != nil {
		return nil, err
	}
	if request.DeliveredUnknowns, err = r.U64(); err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return request, nil
}

// RecoveryOutcome is the Go peer of the Rust recovery outcome: exactly
// one of Result or Failure is set (Rust
// recovery::RecoveryOutcome = Result<RecoveryResult,
// Box<RecoveryPreparationFailure>>).
type RecoveryOutcome struct {
	Result  *recovery.RecoveryResult
	Failure *recovery.RecoveryPreparationFailure
}

// WriteRecoveryOutcome writes one recovery outcome with the retained
// publication problem of a guard-pending terminal (Rust
// wire_recovery::write_outcome: tag 0 carries the completed result,
// tag 1 the preparation failure plus the optional retained problem).
func WriteRecoveryOutcome(control *Control, outcome *RecoveryOutcome, retained *WireProblem) error {
	w := NewWireWriter(control)
	if outcome.Result != nil {
		if err := w.Byte(0); err != nil {
			return err
		}
		return w.finishRecoveryResult(outcome.Result)
	}
	if err := w.Byte(1); err != nil {
		return err
	}
	if err := writeRecoveryFailure(w, outcome.Failure); err != nil {
		return err
	}
	if err := writeOptionalProblem(w, retained); err != nil {
		return err
	}
	return w.Finish()
}

// finishRecoveryResult writes the completed-result arm and seals the
// payload.
func (w *WireWriter) finishRecoveryResult(result *recovery.RecoveryResult) error {
	if err := writeRecoveryReport(w, &result.Report); err != nil {
		return err
	}
	if err := writeOptionalScratch(w, result.Scratch); err != nil {
		return err
	}
	if err := writePublicationResult(w, &result.Publication); err != nil {
		return err
	}
	return w.Finish()
}

// ReadRecoveryOutcome decodes one recovery outcome (Rust
// wire_recovery::read_outcome).
func ReadRecoveryOutcome(control *Control) (*RecoveryOutcome, *WireProblem, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, nil, err
	}
	tag, err := r.Byte()
	if err != nil {
		return nil, nil, err
	}
	outcome := &RecoveryOutcome{}
	var retained *WireProblem
	switch tag {
	case 0:
		result := &recovery.RecoveryResult{}
		if result.Report, err = readRecoveryReport(r); err != nil {
			return nil, nil, err
		}
		if result.Scratch, err = readOptionalScratch(r); err != nil {
			return nil, nil, err
		}
		if result.Publication, err = readPublicationResult(r); err != nil {
			return nil, nil, err
		}
		outcome.Result = result
	case 1:
		failure, err := readRecoveryFailure(r)
		if err != nil {
			return nil, nil, err
		}
		outcome.Failure = failure
		if retained, err = readOptionalProblem(r); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker recovery result tag is invalid"}
	}
	if err := r.Finish(); err != nil {
		return nil, nil, err
	}
	return outcome, retained, nil
}

// WriteRecoveryUnknown writes one streamed unknown-damage envelope to a
// fresh session payload (Rust wire_recovery::write_unknown: sequence,
// reason and object classes, the optional page/interval/fence, and the
// span flags).
func WriteRecoveryUnknown(control *Control, envelope *recovery.RecoveryUnknownEnvelope) error {
	w := NewWireWriter(control)
	if err := w.U64(envelope.Sequence); err != nil {
		return err
	}
	if err := w.Byte(byte(envelope.Reason)); err != nil {
		return err
	}
	if err := w.Byte(byte(envelope.Object)); err != nil {
		return err
	}
	if err := writeOptionalU32(w, envelope.PageNumber); err != nil {
		return err
	}
	if err := writeOptionalInterval(w, envelope.PhysicalBytes); err != nil {
		return err
	}
	if err := writeOptionalFence(w, envelope.AddressFence); err != nil {
		return err
	}
	if err := w.Bool(envelope.ContributesToPossibleSpan); err != nil {
		return err
	}
	if err := w.Bool(envelope.HasUnboundedExtent); err != nil {
		return err
	}
	return w.Finish()
}

// ReadRecoveryUnknown decodes one streamed unknown-damage envelope
// (Rust wire_recovery::read_unknown).
func ReadRecoveryUnknown(control *Control) (*recovery.RecoveryUnknownEnvelope, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	envelope := &recovery.RecoveryUnknownEnvelope{}
	if envelope.Sequence, err = r.U64(); err != nil {
		return nil, err
	}
	reason, err := r.Byte()
	if err != nil {
		return nil, err
	}
	reasonValue, ok := validationReasonFromWire(reason)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker recovery reason is invalid"}
	}
	envelope.Reason = reasonValue
	object, err := r.Byte()
	if err != nil {
		return nil, err
	}
	objectValue, ok := validationObjectFromWire(object)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker recovery object is invalid"}
	}
	envelope.Object = objectValue
	if envelope.PageNumber, err = readOptionalU32(r); err != nil {
		return nil, err
	}
	if envelope.PhysicalBytes, err = readOptionalInterval(r); err != nil {
		return nil, err
	}
	if envelope.AddressFence, err = readOptionalFence(r, "worker recovery fence is invalid"); err != nil {
		return nil, err
	}
	if envelope.ContributesToPossibleSpan, err = r.Bool(); err != nil {
		return nil, err
	}
	if envelope.HasUnboundedExtent, err = r.Bool(); err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return envelope, nil
}

// writeRecoveryFailure writes one preparation-failure arm (Rust
// wire_recovery::write_failure): the partial report, the optional
// scratch attempt, the optional output attempt, the cleanup ledger, and
// the coordination/housekeeping classes plus the fixed cause problem.
func writeRecoveryFailure(w *WireWriter, value *recovery.RecoveryPreparationFailure) error {
	if err := writeRecoveryReport(w, &value.Report); err != nil {
		return err
	}
	if err := writeOptionalScratch(w, value.Scratch); err != nil {
		return err
	}
	if err := w.Bool(value.Output != nil); err != nil {
		return err
	}
	if value.Output != nil {
		if err := writePrivateOutput(w, value.Output); err != nil {
			return err
		}
	}
	if err := writeCleanupArtifacts(w, &value.Cleanup); err != nil {
		return err
	}
	if err := writeCoordinationCleanup(w, value.CoordinationCleanup); err != nil {
		return err
	}
	if err := writeHousekeepingValue(w, value.Housekeeping); err != nil {
		return err
	}
	if err := writeHousekeepingList(w, value.VisibleHousekeeping); err != nil {
		return err
	}
	problem := wireProblemOf(value.Cause)
	return writeProblem(w, &problem)
}

// readRecoveryFailure decodes one preparation-failure arm (Rust
// wire_recovery::read_failure; the source cleanup guard is never
// transmitted, exactly like Rust).
func readRecoveryFailure(r *WireReader) (*recovery.RecoveryPreparationFailure, error) {
	failure := &recovery.RecoveryPreparationFailure{}
	var err error
	if failure.Report, err = readRecoveryReport(r); err != nil {
		return nil, err
	}
	if failure.Scratch, err = readOptionalScratch(r); err != nil {
		return nil, err
	}
	hasOutput, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if hasOutput {
		output, err := readPrivateOutput(r)
		if err != nil {
			return nil, err
		}
		failure.Output = &output
	}
	if failure.Cleanup, err = readCleanupArtifacts(r); err != nil {
		return nil, err
	}
	coordinationTag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if failure.CoordinationCleanup, err = readCoordinationCleanupByte(coordinationTag); err != nil {
		return nil, err
	}
	housekeepingTag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if failure.Housekeeping, err = readHousekeepingValueByte(housekeepingTag); err != nil {
		return nil, err
	}
	if failure.VisibleHousekeeping, err = readHousekeepingArtifacts(r); err != nil {
		return nil, err
	}
	cause, err := readProblem(r)
	if err != nil {
		return nil, err
	}
	failure.Cause = cause.Err()
	return failure, nil
}

// writeRecoveryReport writes one recovery report (Rust
// wire_recovery::report: the physical and logical counters and the
// three exact cardinalities; used both inline and as the callback
// checkpoint payload).
func writeRecoveryReport(w *WireWriter, value *recovery.RecoveryReport) error {
	if err := writeRecoveryPageCounts(w, &value.Pages); err != nil {
		return err
	}
	if err := writeRecoveryLogicalCounts(w, &value.Ranges); err != nil {
		return err
	}
	if err := writeRecoveryLogicalCounts(w, &value.CatalogEntries); err != nil {
		return err
	}
	if err := writeRecoveryLogicalCounts(w, &value.MembershipEntries); err != nil {
		return err
	}
	if err := writeRecoveryLogicalCounts(w, &value.StructureEntries); err != nil {
		return err
	}
	if err := writeRecoveryLogicalCounts(w, &value.MetadataChunks); err != nil {
		return err
	}
	if err := writeRecoveryLogicalCounts(w, &value.RetirementRecords); err != nil {
		return err
	}
	if err := writeCardinality(w, value.VerifiedAddresses); err != nil {
		return err
	}
	if err := writeCardinality(w, value.RejectedAddresses); err != nil {
		return err
	}
	if err := writeCardinality(w, value.BoundedPossibleSpanAddresses); err != nil {
		return err
	}
	if err := w.Bool(value.HasUnboundedUnknown); err != nil {
		return err
	}
	return w.U64(value.UnknownEnvelopes)
}

// readRecoveryReport decodes one recovery report (Rust
// wire_recovery::read_report).
func readRecoveryReport(r *WireReader) (recovery.RecoveryReport, error) {
	value := recovery.RecoveryReport{}
	var err error
	if value.Pages, err = readRecoveryPageCounts(r); err != nil {
		return value, err
	}
	if value.Ranges, err = readRecoveryLogicalCounts(r); err != nil {
		return value, err
	}
	if value.CatalogEntries, err = readRecoveryLogicalCounts(r); err != nil {
		return value, err
	}
	if value.MembershipEntries, err = readRecoveryLogicalCounts(r); err != nil {
		return value, err
	}
	if value.StructureEntries, err = readRecoveryLogicalCounts(r); err != nil {
		return value, err
	}
	if value.MetadataChunks, err = readRecoveryLogicalCounts(r); err != nil {
		return value, err
	}
	if value.RetirementRecords, err = readRecoveryLogicalCounts(r); err != nil {
		return value, err
	}
	if value.VerifiedAddresses, err = readCardinality(r); err != nil {
		return value, err
	}
	if value.RejectedAddresses, err = readCardinality(r); err != nil {
		return value, err
	}
	if value.BoundedPossibleSpanAddresses, err = readCardinality(r); err != nil {
		return value, err
	}
	if value.HasUnboundedUnknown, err = r.Bool(); err != nil {
		return value, err
	}
	if value.UnknownEnvelopes, err = r.U64(); err != nil {
		return value, err
	}
	return value, nil
}

// writeRecoveryPageCounts writes one physical-page facts record (Rust
// wire_recovery::page_counts).
func writeRecoveryPageCounts(w *WireWriter, value *recovery.RecoveryPageCounts) error {
	if err := w.U64(value.Examined); err != nil {
		return err
	}
	if err := w.U64(value.Accepted); err != nil {
		return err
	}
	if err := w.U64(value.Rejected); err != nil {
		return err
	}
	return w.U64(value.IOUnreadable)
}

// readRecoveryPageCounts decodes one physical-page facts record (Rust
// wire_recovery::read_page_counts).
func readRecoveryPageCounts(r *WireReader) (recovery.RecoveryPageCounts, error) {
	value := recovery.RecoveryPageCounts{}
	var err error
	if value.Examined, err = r.U64(); err != nil {
		return value, err
	}
	if value.Accepted, err = r.U64(); err != nil {
		return value, err
	}
	if value.Rejected, err = r.U64(); err != nil {
		return value, err
	}
	if value.IOUnreadable, err = r.U64(); err != nil {
		return value, err
	}
	return value, nil
}

// writeRecoveryLogicalCounts writes one logical-object facts record
// (Rust wire_recovery::logical_counts).
func writeRecoveryLogicalCounts(w *WireWriter, value *recovery.RecoveryLogicalCounts) error {
	if err := w.U64(value.Examined); err != nil {
		return err
	}
	if err := w.U64(value.Accepted); err != nil {
		return err
	}
	return w.U64(value.Rejected)
}

// readRecoveryLogicalCounts decodes one logical-object facts record
// (Rust wire_recovery::read_logical_counts).
func readRecoveryLogicalCounts(r *WireReader) (recovery.RecoveryLogicalCounts, error) {
	value := recovery.RecoveryLogicalCounts{}
	var err error
	if value.Examined, err = r.U64(); err != nil {
		return value, err
	}
	if value.Accepted, err = r.U64(); err != nil {
		return value, err
	}
	if value.Rejected, err = r.U64(); err != nil {
		return value, err
	}
	return value, nil
}

// writeOptionalScratch writes one optional scratch attempt (Rust
// wire_recovery::optional_scratch).
func writeOptionalScratch(w *WireWriter, value *recovery.RecoveryScratchAttempt) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	if err := w.Bytes(value.AttemptID[:]); err != nil {
		return err
	}
	if err := writeIdentity(w, value.DirectoryIdentity); err != nil {
		return err
	}
	return writeCreationSecurity(w, &value.CreationSecurity)
}

// readOptionalScratch decodes one optional scratch attempt (Rust
// wire_recovery::read_optional_scratch).
func readOptionalScratch(r *WireReader) (*recovery.RecoveryScratchAttempt, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value := &recovery.RecoveryScratchAttempt{}
	if value.AttemptID, err = r.Array16(); err != nil {
		return nil, err
	}
	if value.DirectoryIdentity, err = readIdentity(r); err != nil {
		return nil, err
	}
	if value.CreationSecurity, err = readCreationSecurity(r); err != nil {
		return nil, err
	}
	return value, nil
}

// writeRecoveryBudget writes one recovery budget (Rust
// wire_recovery::write_budget: heap bytes, output pages, open files,
// scratch bytes and files, and the optional scratch directory).
func writeRecoveryBudget(w *WireWriter, value *recovery.RecoveryBudget) error {
	if err := w.U64(value.MaxHeapBytes); err != nil {
		return err
	}
	if err := w.U64(value.MaxOutputPages); err != nil {
		return err
	}
	if err := w.U32(value.MaxOpenFiles); err != nil {
		return err
	}
	if err := w.U64(value.MaxScratchBytes); err != nil {
		return err
	}
	if err := w.U32(value.MaxScratchFiles); err != nil {
		return err
	}
	var directory *string
	if value.ScratchDirectory != "" {
		directory = &value.ScratchDirectory
	}
	return w.OptionalPath(directory)
}

// readRecoveryBudget decodes one recovery budget (Rust
// wire_recovery::read_budget).
func readRecoveryBudget(r *WireReader) (recovery.RecoveryBudget, error) {
	budget := recovery.RecoveryBudget{}
	var err error
	if budget.MaxHeapBytes, err = r.U64(); err != nil {
		return budget, err
	}
	if budget.MaxOutputPages, err = r.U64(); err != nil {
		return budget, err
	}
	if budget.MaxOpenFiles, err = r.U32(); err != nil {
		return budget, err
	}
	if budget.MaxScratchBytes, err = r.U64(); err != nil {
		return budget, err
	}
	if budget.MaxScratchFiles, err = r.U32(); err != nil {
		return budget, err
	}
	var directory *string
	if directory, err = r.OptionalPath(); err != nil {
		return budget, err
	}
	if directory != nil {
		budget.ScratchDirectory = *directory
	}
	return budget, nil
}

// workerModeTag maps one worker mode to its wire tag (Rust
// wire_recovery::mode_tag).
func workerModeTag(mode WorkerMode) (byte, bool) {
	switch mode {
	case WorkerModeImmutable:
		return 1, true
	case WorkerModeOffline:
		return 2, true
	case WorkerModeLive:
		return 3, true
	}
	return 0, false
}

// workerModeFromTag maps one wire tag back to a worker mode (Rust
// wire_recovery::read_mode).
func workerModeFromTag(tag byte) (WorkerMode, bool) {
	switch tag {
	case 1:
		return WorkerModeImmutable, true
	case 2:
		return WorkerModeOffline, true
	case 3:
		return WorkerModeLive, true
	}
	return 0, false
}

// validationReasonFromWire maps one wire reason byte (Rust
// ValidationReason::from_wire: the 47 classes in declaration order).
func validationReasonFromWire(value byte) (validation.ValidationReason, bool) {
	if int(value) < validation.ValidationReasonCount {
		return validation.ValidationReason(value), true
	}
	return 0, false
}

// validationObjectFromWire maps one wire object byte (Rust
// ValidationObject::from_wire: the 17 classes in declaration order).
func validationObjectFromWire(value byte) (validation.ValidationObject, bool) {
	if int(value) < validation.ValidationObjectCount {
		return validation.ValidationObject(value), true
	}
	return 0, false
}

// readRecoveryCandidateValue decodes one recovery candidate token as a
// value (Rust wire::read_recovery_candidate; the request codec keeps
// the candidate inline).
func readRecoveryCandidateValue(r *WireReader) (recovery.RecoveryCandidate, error) {
	candidate, err := readRecoveryCandidate(r)
	if err != nil {
		return recovery.RecoveryCandidate{}, err
	}
	return *candidate, nil
}

// ReadRecoveryCallbackReport reads the sealed recovery-report callback
// payload (Rust client recovery::read_recovery_callback_report): the
// callback checkpoint must be sealed as RecoveryReport, otherwise the
// Conflict class reports the missing checkpoint.
func ReadRecoveryCallbackReport(control *Control) (*recovery.RecoveryReport, error) {
	if kind, ok := control.CallbackCheckpoint(); !ok || kind != CallbackRecoveryReport {
		return nil, &format.Error{Code: format.CodeConflict, Detail: "worker recovery callback checkpoint is missing"}
	}
	r, err := NewWireCallbackReader(control)
	if err != nil {
		return nil, err
	}
	report, err := readRecoveryReport(r)
	if err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return &report, nil
}
