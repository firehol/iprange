//go:build linux && amd64

// Validation-mode wire codecs (Rust worker/wire_validation.rs): the
// validation request, the completed/operational-failure result with
// the optional retained publication problem, the cleanup result, the
// streamed finding envelope, and the validated-generation record.
// Every field order and corrupt detail mirrors the Rust authority. The
// decoded result uses the worker progress wire form because the Go
// validation package keeps its counter arrays private; the worker
// process composes the domain progress through the exported accessors.

package worker

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// ValidationRequest is the decoded validation request (Rust
// wire_validation.rs Request). Candidate carries the offline-candidate
// arm token; the other arms leave it nil.
type ValidationRequest struct {
	Path              string
	Mode              validation.ValidationMode
	Candidate         *recovery.RecoveryCandidate
	Budget            validation.ValidationBudget
	UnreadablePages   []uint32
	DeliveredFindings uint64
}

// WriteValidationRequest writes one validation request (Rust
// wire_validation::write_request: path, mode with the offline
// candidate arm, budget, unreadable-page list, delivered-findings
// count). The Go domain mode enum cannot carry the offline candidate,
// so the candidate travels as a parameter and is encoded inline inside
// the mode arm exactly like the Rust variant payload.
func WriteValidationRequest(control *Control, path string, mode validation.ValidationMode, candidate *recovery.RecoveryCandidate, budget *validation.ValidationBudget, unreadablePages []uint32, deliveredFindings uint64) error {
	w := NewWireWriter(control)
	if err := w.Path(path); err != nil {
		return err
	}
	switch mode {
	case validation.ValidationModeImmutableCurrent:
		if err := w.Byte(1); err != nil {
			return err
		}
	case validation.ValidationModeLiveCurrent:
		if err := w.Byte(2); err != nil {
			return err
		}
	case validation.ValidationModeOfflineCandidate:
		if candidate == nil {
			return &format.Error{Code: format.CodeInvalidArgument, Detail: "offline-candidate validation requires the candidate token (recovery.ValidateOfflineCandidate)"}
		}
		if err := w.Byte(3); err != nil {
			return err
		}
		if err := writeRecoveryCandidate(w, candidate); err != nil {
			return err
		}
	default:
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "worker validation mode is invalid"}
	}
	if err := writeValidationBudget(w, budget); err != nil {
		return err
	}
	if err := writeU32List(w, unreadablePages, &format.Error{Code: format.CodeInvalidArgument, Detail: "too many unreadable source pages"}); err != nil {
		return err
	}
	if err := w.U64(deliveredFindings); err != nil {
		return err
	}
	return w.Finish()
}

// ReadValidationRequest decodes one validation request (Rust
// wire_validation::read_request).
func ReadValidationRequest(control *Control) (*ValidationRequest, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	request := &ValidationRequest{}
	if request.Path, err = r.Path(); err != nil {
		return nil, err
	}
	modeTag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	switch modeTag {
	case 1:
		request.Mode = validation.ValidationModeImmutableCurrent
	case 2:
		request.Mode = validation.ValidationModeLiveCurrent
	case 3:
		// The offline-candidate arm carries a full recovery candidate;
		// the Go domain mode enum keeps no payload, so the request
		// struct retains the token for the worker process.
		candidate, err := readRecoveryCandidate(r)
		if err != nil {
			return nil, err
		}
		request.Candidate = candidate
		request.Mode = validation.ValidationModeOfflineCandidate
	default:
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker validation mode is invalid"}
	}
	if request.Budget, err = readValidationBudget(r); err != nil {
		return nil, err
	}
	if request.UnreadablePages, err = readU32List(r, &request.Budget.MaxHeapBytes); err != nil {
		return nil, err
	}
	if request.DeliveredFindings, err = r.U64(); err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return request, nil
}

// ValidationResultWire is the wire form of one completed validation
// result (Rust wire_validation.rs write_result ok-arm over
// validation::ValidationResult; the progress uses the worker wire
// form because the Go validation package keeps its counter arrays
// private). A domain result converts through wireValidationResultOf.
type ValidationResultWire struct {
	Valid        bool
	FileIdentity publication.LocalFileIdentity
	Generation   *validation.ValidatedGeneration
	Progress     ProgressWire
}

// wireValidationResultOf converts one domain validation result to its
// wire form (WriteValidationResult and the wire tests share the
// conversion).
func wireValidationResultOf(value *validation.ValidationResult) ValidationResultWire {
	return ValidationResultWire{
		Valid:        value.Valid,
		FileIdentity: value.FileIdentity,
		Generation:   value.Generation,
		Progress:     ProgressWireOf(&value.Progress),
	}
}

// ValidationFailureWire is the wire form of one operational validation
// failure (Rust wire_validation.rs err-arm over
// validation::ValidationFailure: the cause, the partial progress, the
// empty cleanup facts, and no source guard; the retained publication
// problem is transmitted separately).
type ValidationFailureWire struct {
	Cause               error
	Progress            ProgressWire
	Cleanup             publication.CleanupArtifacts
	CoordinationCleanup publication.CoordinationCleanup
	SourceCleanup       any
}

// failureWireOf builds the wire failure of one domain failure (Rust
// ValidationFailure; the progress arrays convert through the exported
// accessors, and a missing progress box is the zero counters).
func failureWireOf(value *validation.ValidationFailure) ValidationFailureWire {
	out := ValidationFailureWire{
		Cause:               value.Cause,
		Cleanup:             value.Cleanup,
		CoordinationCleanup: value.CoordinationCleanup,
		SourceCleanup:       value.SourceCleanup,
	}
	if value.Progress != nil {
		out.Progress = ProgressWireOf(value.Progress)
	}
	return out
}

// WriteValidationResult writes one validation result with the retained
// publication problem (Rust wire_validation::write_result: tag 0 the
// completed facts, tag 1 the encoded cause, partial progress, and the
// optional retained problem). Exactly one of result and failure is
// set, mirroring the Rust Result<ValidationResult, ValidationFailure>.
func WriteValidationResult(control *Control, result *validation.ValidationResult, failure *validation.ValidationFailure, retained *WireProblem) error {
	w := NewWireWriter(control)
	if result != nil {
		// The completion arm composes the one authoritative
		// domain-to-wire conversion (wireValidationResultOf) so the
		// writer and the wire tests share the same field mapping.
		wire := wireValidationResultOf(result)
		if err := w.Byte(0); err != nil {
			return err
		}
		if err := w.Bool(wire.Valid); err != nil {
			return err
		}
		if err := writeIdentity(w, wire.FileIdentity); err != nil {
			return err
		}
		if err := w.Bool(wire.Generation != nil); err != nil {
			return err
		}
		if wire.Generation != nil {
			if err := writeValidatedGeneration(w, wire.Generation); err != nil {
				return err
			}
		}
		if err := writeProgress(w, &wire.Progress); err != nil {
			return err
		}
		return w.Finish()
	}
	if err := w.Byte(1); err != nil {
		return err
	}
	if err := encodeWorkerError(w, failure.Cause); err != nil {
		return err
	}
	progress := ProgressWire{}
	if failure.Progress != nil {
		progress = ProgressWireOf(failure.Progress)
	}
	if err := writeProgress(w, &progress); err != nil {
		return err
	}
	if err := writeOptionalProblem(w, retained); err != nil {
		return err
	}
	return w.Finish()
}

// ReadValidationResult decodes one validation result (Rust
// wire_validation::read_result: a codec failure inside the envelope
// folds into an operational failure with the zero progress, exactly
// like the Rust unwrap_or_else wrapper).
func ReadValidationResult(control *Control) (result *ValidationResultWire, failure *ValidationFailureWire, retained *WireProblem) {
	result, failure, retained, err := readValidationResult(control)
	if err != nil {
		return nil, &ValidationFailureWire{Cause: err}, nil
	}
	return result, failure, retained
}

// readValidationResult is the fallible inner reader (Rust
// wire_validation::read_result_inner).
func readValidationResult(control *Control) (*ValidationResultWire, *ValidationFailureWire, *WireProblem, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, nil, nil, err
	}
	tag, err := r.Byte()
	if err != nil {
		return nil, nil, nil, err
	}
	var result *ValidationResultWire
	var failure *ValidationFailureWire
	var retained *WireProblem
	switch tag {
	case 0:
		value := &ValidationResultWire{}
		if value.Valid, err = r.Bool(); err != nil {
			return nil, nil, nil, err
		}
		if value.FileIdentity, err = readIdentity(r); err != nil {
			return nil, nil, nil, err
		}
		hasGeneration, err := r.Bool()
		if err != nil {
			return nil, nil, nil, err
		}
		if hasGeneration {
			generation, err := readValidatedGeneration(r)
			if err != nil {
				return nil, nil, nil, err
			}
			value.Generation = &generation
		}
		if value.Progress, err = readProgress(r); err != nil {
			return nil, nil, nil, err
		}
		result = value
	case 1:
		value := &ValidationFailureWire{}
		if value.Cause, err = readWorkerError(r); err != nil {
			return nil, nil, nil, err
		}
		if value.Progress, err = readProgress(r); err != nil {
			return nil, nil, nil, err
		}
		if retained, err = readOptionalProblem(r); err != nil {
			return nil, nil, nil, err
		}
		value.Cleanup = publication.NewCleanupArtifacts()
		failure = value
	default:
		return nil, nil, nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker validation result tag is invalid"}
	}
	if err := r.Finish(); err != nil {
		return nil, nil, nil, err
	}
	return result, failure, retained, nil
}

// WriteValidationCleanupResult writes one cleanup-worker result (Rust
// wire_validation::write_cleanup_result: the completion flag and the
// optional problem).
func WriteValidationCleanupResult(control *Control, complete bool, problem *WireProblem) error {
	w := NewWireWriter(control)
	if err := w.Bool(complete); err != nil {
		return err
	}
	if err := writeOptionalProblem(w, problem); err != nil {
		return err
	}
	return w.Finish()
}

// ReadValidationCleanupResult decodes one cleanup-worker result (Rust
// wire_validation::read_cleanup_result).
func ReadValidationCleanupResult(control *Control) (bool, *WireProblem, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return false, nil, err
	}
	complete, err := r.Bool()
	if err != nil {
		return false, nil, err
	}
	problem, err := readOptionalProblem(r)
	if err != nil {
		return false, nil, err
	}
	if err := r.Finish(); err != nil {
		return false, nil, err
	}
	return complete, problem, nil
}

// WriteValidationFinding writes one streamed finding envelope (Rust
// wire_validation::write_finding: sequence, reason and object classes,
// and the optional page/interval/page/fence facts).
func WriteValidationFinding(control *Control, finding *validation.ValidationFinding) error {
	w := NewWireWriter(control)
	if err := w.U64(finding.Sequence); err != nil {
		return err
	}
	if err := w.Byte(byte(finding.Reason)); err != nil {
		return err
	}
	if err := w.Byte(byte(finding.Object)); err != nil {
		return err
	}
	if err := writeOptionalU32(w, finding.PageNumber); err != nil {
		return err
	}
	if err := writeOptionalInterval(w, finding.PhysicalBytes); err != nil {
		return err
	}
	if err := writeOptionalU32(w, finding.RelatedPageNumber); err != nil {
		return err
	}
	if err := writeOptionalFence(w, finding.AddressFence); err != nil {
		return err
	}
	return w.Finish()
}

// ReadValidationFinding decodes one streamed finding envelope (Rust
// wire_validation::read_finding).
func ReadValidationFinding(control *Control) (*validation.ValidationFinding, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	finding := &validation.ValidationFinding{}
	if finding.Sequence, err = r.U64(); err != nil {
		return nil, err
	}
	reason, err := r.Byte()
	if err != nil {
		return nil, err
	}
	reasonValue, ok := validationReasonFromWire(reason)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker validation reason is invalid"}
	}
	finding.Reason = reasonValue
	object, err := r.Byte()
	if err != nil {
		return nil, err
	}
	objectValue, ok := validationObjectFromWire(object)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker validation object is invalid"}
	}
	finding.Object = objectValue
	if finding.PageNumber, err = readOptionalU32(r); err != nil {
		return nil, err
	}
	if finding.PhysicalBytes, err = readOptionalInterval(r); err != nil {
		return nil, err
	}
	if finding.RelatedPageNumber, err = readOptionalU32(r); err != nil {
		return nil, err
	}
	if finding.AddressFence, err = readOptionalFence(r, "worker validation fence is invalid"); err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return finding, nil
}

// writeValidatedGeneration writes one validated-generation record
// (Rust wire_validation::write_generation: the contract enums as raw
// bytes, the value tag, the database identity, and the 13 roots).
func writeValidatedGeneration(w *WireWriter, value *validation.ValidatedGeneration) error {
	if err := w.Byte(value.AddressFamily); err != nil {
		return err
	}
	if err := w.Byte(value.ValueKind); err != nil {
		return err
	}
	if err := w.Byte(value.StructureKind); err != nil {
		return err
	}
	if err := w.Bytes(value.ValueTag[:]); err != nil {
		return err
	}
	if err := w.Bytes(value.DatabaseID[:]); err != nil {
		return err
	}
	if err := w.U64(value.TransactionID); err != nil {
		return err
	}
	if err := w.Bytes(value.CommitNonce[:]); err != nil {
		return err
	}
	if err := w.U64(value.PageCount); err != nil {
		return err
	}
	for _, root := range value.Roots {
		if err := w.U32(root); err != nil {
			return err
		}
	}
	return nil
}

// readValidatedGeneration decodes one validated-generation record
// (Rust wire_validation::read_generation: every contract enum is
// validated against its from_wire table).
func readValidatedGeneration(r *WireReader) (validation.ValidatedGeneration, error) {
	value := validation.ValidatedGeneration{}
	var err error
	if value.AddressFamily, err = r.Byte(); err != nil {
		return value, err
	}
	if !addressFamilyFromWire(value.AddressFamily) {
		return value, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker address family is invalid"}
	}
	if value.ValueKind, err = r.Byte(); err != nil {
		return value, err
	}
	if !valueKindFromWire(value.ValueKind) {
		return value, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker value kind is invalid"}
	}
	if value.StructureKind, err = r.Byte(); err != nil {
		return value, err
	}
	if _, ok := format.StructureKindFromWire(value.StructureKind); !ok {
		return value, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker structure kind is invalid"}
	}
	tag, err := r.Array16()
	if err != nil {
		return value, err
	}
	if !valueTagFromWire(tag) {
		return value, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker value tag is invalid"}
	}
	value.ValueTag = tag
	if value.DatabaseID, err = r.Array16(); err != nil {
		return value, err
	}
	if value.TransactionID, err = r.U64(); err != nil {
		return value, err
	}
	if value.CommitNonce, err = r.Array16(); err != nil {
		return value, err
	}
	if value.PageCount, err = r.U64(); err != nil {
		return value, err
	}
	for index := range value.Roots {
		if value.Roots[index], err = r.U32(); err != nil {
			return value, err
		}
	}
	return value, nil
}

// addressFamilyFromWire checks one wire address-family code (Rust
// contract.rs AddressFamily::from_wire: Ipv4 4, Ipv6 6).
func addressFamilyFromWire(value byte) bool {
	return value == format.AddressFamilyIPv4 || value == format.AddressFamilyIPv6
}

// valueKindFromWire checks one wire value-kind code (Rust contract.rs
// ValueKind::from_wire: Direct 1, Membership 2, Structured 3).
func valueKindFromWire(value byte) bool {
	switch value {
	case format.ValueKindDirect, format.ValueKindMembership, format.ValueKindStructured:
		return true
	}
	return false
}

// valueTagFromWire checks one opaque 16-byte value tag (Rust
// contract.rs ValueTag::from_wire: the first zero byte starts a zero
// tail).
func valueTagFromWire(storage [16]byte) bool {
	nul := -1
	for index, byte := range storage {
		if byte == 0 {
			nul = index
			break
		}
	}
	if nul < 0 {
		return false
	}
	for _, byte := range storage[nul:] {
		if byte != 0 {
			return false
		}
	}
	return true
}

// ReadValidationProgress reads the sealed validation-progress callback
// payload (Rust client validation::read_validation_progress): nil with
// no error when the callback checkpoint is not a validation-progress
// seal.
func ReadValidationProgress(control *Control) (*ProgressWire, error) {
	if kind, ok := control.CallbackCheckpoint(); !ok || kind != CallbackValidationProgress {
		return nil, nil
	}
	r, err := NewWireCallbackReader(control)
	if err != nil {
		return nil, err
	}
	progress, err := readProgress(r)
	if err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return &progress, nil
}
