package recovery

// In-process recovery entry (Rust recovery/api.rs recover_precreated
// plus the worker client create position): the budget preflight, the
// destination attempt creation, the source open per mode with the
// attempt discard on failure, the source-identity proof, the
// kind-split build into the attempt file, the source finish, and the
// publication terminal. The worker-process boundary and its
// checkpoints are the recorded 4-11 scope; the in-process machine
// runs the same flow with the check hook in place of the worker
// probes.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// recoverPrecreated runs one recovery construction into a fresh
// fail-if-exists destination (Rust recovery/api.rs platform::
// recover_precreated; the attempt is created here at the position of
// the Rust worker client, before the source opens, so the source-open
// failure discards the attempt exactly like the worker arm). The
// in-process (non-worker) entries keep this client create position;
// the worker session consumes a parent-created attempt through the
// Recover*WithAttempt entries instead.
func recoverPrecreated(sourcePath string, candidate *RecoveryCandidate, destinationPath string, mode sourceMode, budget *RecoveryBudget, check func() error, sink RecoverySink) (*RecoveryResult, *RecoveryPreparationFailure) {
	// A nil check is the uncancellable convention everywhere in the
	// machine; the publication engine always calls its checkpoint, so
	// the seam is normalized once here.
	if check == nil {
		check = func() error { return nil }
	}
	effective, failure := validateRecoveryBudget(budget, mode)
	if failure != nil {
		return nil, failure
	}
	// A pre-cancelled recovery refuses before any destination artifact
	// exists (the Rust client cancellation check before the attempt
	// creation).
	if err := live.Checkpoint(check); err != nil {
		return nil, earlyRecoveryFailure(err)
	}
	attempt, attemptFailure := publication.CreatePublishAttempt(destinationPath, publication.PolicyFailIfExists)
	if attemptFailure != nil {
		// Rust recover_once create and secure arms: they run before
		// the source opens, so no source guard exists and the attempt
		// facts fold from the publication ledger.
		return nil, FromAttemptFailure(attemptFailure)
	}
	return recoverMachine(sourcePath, candidate, destinationPath, mode, effective, check, sink, attempt)
}

// recoverPrecreatedWithAttempt runs the recovery machine over a
// parent-created and secured output attempt (Rust worker.rs
// run_recovery over recovery/api.rs recover_precreated_local: the
// worker session consumes the attempt facts the request carried, so
// the machine never creates its own output). The worker client
// already ran the pre-cancel check and the preflight before the
// attempt existed, so this entry only re-proves the budget (the Rust
// machine step) and then runs the machine; every machine terminal
// consumes the provided attempt exactly like the created one.
func recoverPrecreatedWithAttempt(sourcePath string, candidate *RecoveryCandidate, destinationPath string, mode sourceMode, budget *RecoveryBudget, check func() error, sink RecoverySink, attempt *publication.PublishAttempt) (*RecoveryResult, *RecoveryPreparationFailure) {
	// A nil check is the uncancellable convention everywhere in the
	// machine; the publication engine always calls its checkpoint, so
	// the seam is normalized once here.
	if check == nil {
		check = func() error { return nil }
	}
	effective, failure := validateRecoveryBudget(budget, mode)
	if failure != nil {
		// The parent-created attempt is released without namespace
		// work (Rust drop of the consumed owners on the machine
		// refusal arm; the worker client pre-validates the same
		// budget, so this arm cannot fire inside a worker session).
		attempt.Close()
		return nil, failure
	}
	return recoverMachine(sourcePath, candidate, destinationPath, mode, effective, check, sink, attempt)
}

// recoverMachine runs the recovery construction over one existing
// secured attempt (Rust recovery/api.rs recover_precreated after the
// attempt parameter is in hand: open source, identity proof,
// kind-split build, source finish, publication terminal). Every
// terminal consumes the attempt exactly once.
func recoverMachine(sourcePath string, candidate *RecoveryCandidate, destinationPath string, mode sourceMode, effective *RecoveryBudget, check func() error, sink RecoverySink, attempt *publication.PublishAttempt) (*RecoveryResult, *RecoveryPreparationFailure) {
	source, openFailure := openRecoverySource(sourcePath, candidate, mode, check)
	if openFailure != nil {
		// Rust recover_precreated open_source arm: the created attempt
		// is discarded and its exact facts fold into the failure.
		facts, artifact := attempt.DiscardFacts()
		return nil, newRecoveryPreparationFailure(problem(openFailure.cause), RecoveryReport{}, &facts, artifact, openFailure.guard)
	}
	// failSource runs the failing terminal of an opened source (Rust
	// fail_source: the release failure lives only in the cleanup
	// guard, never in the folded cause).
	failSource := func(cause error, report RecoveryReport, output *publication.PrivateOutputAttempt, artifact *publication.CleanupArtifact) (*RecoveryResult, *RecoveryPreparationFailure) {
		end := source.releaseOnly()
		return nil, newRecoveryPreparationFailure(cause, report, output, artifact, end.guard)
	}
	// The source and the private output identity must differ (Rust
	// api.rs compare after the attempt exists).
	sourceDevice, sourceInode, _ := source.identity().DeviceInode()
	attemptDevice, attemptInode := attempt.FileIdentity()
	if encodeRecoveryIdentity(sourceDevice, sourceInode) == encodeRecoveryIdentity(attemptDevice, attemptInode) {
		facts, artifact := attempt.DiscardFacts()
		return failSource(problem(&format.Error{Code: format.CodeInvalidArgument, Detail: "source and recovery output identities match"}), RecoveryReport{}, &facts, artifact)
	}
	meta := source.meta()
	// The output-spec structure-kind gate runs before the builder
	// exists (Rust api.rs output_spec: from_wire on the raw code with
	// the UnsupportedStructure refusal and the default report, so an
	// unknown code never touches the destination file or the sink).
	structureKind, known := format.StructureKindFromWire(meta.StructureKind)
	if !known {
		facts, artifact := attempt.DiscardFacts()
		// The refusal folds through problem() like every sibling arm
		// (Rust fail_attempt: PublicationProblem over the cause).
		return failSource(problem(&format.Error{Code: format.CodeUnsupportedStructure, Detail: "recovery structure kind is unsupported"}), RecoveryReport{}, &facts, artifact)
	}
	spec, err := writer.FreshOutputSpec(meta.AddressFamily, meta.ValueKind, structureKind, meta.ValueTag, meta.FeedIndexLimit)
	if err != nil {
		facts, artifact := attempt.DiscardFacts()
		return failSource(problem(err), RecoveryReport{}, &facts, artifact)
	}
	// Recovery reserves its heap budget for salvage state, not output
	// acceleration: the reference batches charge nothing (the Rust
	// Heap::new(0) arm).
	builder, err := writer.NewStructuredOutputBuilderOverFile(attempt.File(), spec, writer.OutputBudget{MaxOutputPages: effective.MaxOutputPages}, 0, 0)
	if err != nil {
		facts, artifact := attempt.DiscardFacts()
		return failSource(problem(err), RecoveryReport{}, &facts, artifact)
	}
	discarded := func(cause error, report RecoveryReport) (*RecoveryResult, *RecoveryPreparationFailure) {
		_ = builder.Close()
		facts, artifact := attempt.DiscardFacts()
		return failSource(problem(cause), report, &facts, artifact)
	}
	// Rust api.rs:234 enter_source: the source probe is armed before
	// the machine build and dropped after it, so a real SIGBUS on any
	// source page during the salvable scan lands in the worker fault
	// record with the Source role (no hook in library processes: the
	// Probe runs the build directly).
	var construction *Construction
	var constructionFailure *constructionFailure
	probeErr := source.mapping().Probe(mapping.RoleSource, func() error {
		construction, constructionFailure = buildRecoveryKind(source.mapping(), meta, builder, effective, check, sink)
		return nil
	})
	if probeErr != nil {
		return discarded(probeErr, RecoveryReport{})
	}
	if constructionFailure != nil {
		return discarded(constructionFailure.cause, constructionFailure.report)
	}
	built := construction
	// The final source check runs before the publication prepare (Rust
	// api.rs finish: checkpoint, source probe, source.finish, then
	// prepare; the probe at api.rs:318 is armed across source.finish).
	var end sourceEnd
	finishProbeErr := source.mapping().Probe(mapping.RoleSource, func() error {
		end = source.finish(meta, check)
		return nil
	})
	if finishProbeErr != nil {
		_ = built.finished.Close()
		facts, artifact := attempt.DiscardFacts()
		return failSource(problem(finishProbeErr), built.report, &facts, artifact)
	}
	if end.cause != nil {
		_ = built.finished.Close()
		facts, artifact := attempt.DiscardFacts()
		return failSource(problem(end.cause), built.report, &facts, artifact)
	}
	result, finishFailure := attempt.Finish(publication.FinishedOutput{
		File:    attempt.File(),
		Mapping: built.finished.Mapping(),
		Meta:    built.finished.Meta(),
	}, check)
	if finishFailure != nil {
		return nil, fromPublicationFailure(finishFailure, built.report)
	}
	return completedRecoveryView(built.report, result), nil
}

// RecoverImmutable runs one immutable recovery (Rust api::
// recover_immutable over the worker immutable machine).
func RecoverImmutable(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, check func() error, sink RecoverySink) (*RecoveryResult, *RecoveryPreparationFailure) {
	return recoverPrecreated(sourcePath, candidate, destinationPath, sourceModeImmutable, budget, check, sink)
}

// RecoverOffline runs one quiescent recovery (Rust api::recover_offline
// over the worker offline machine; the certification is consumed by
// the caller boundary exactly like the Rust `let _ = certification`
// arm).
func RecoverOffline(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, check func() error, sink RecoverySink) (*RecoveryResult, *RecoveryPreparationFailure) {
	return recoverPrecreated(sourcePath, candidate, destinationPath, sourceModeOffline, budget, check, sink)
}

// RecoverLive runs one in-process live recovery (Rust api::recover_live
// plus the worker live machine): the platform support refusal runs
// before the budget, exactly like the Rust api arm.
func RecoverLive(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, check func() error, sink RecoverySink) (*RecoveryResult, *RecoveryPreparationFailure) {
	if err := live.RequireLiveSupported(); err != nil {
		return nil, earlyRecoveryFailure(err)
	}
	return recoverPrecreated(sourcePath, candidate, destinationPath, sourceModeLive, budget, check, sink)
}

// RecoverImmutableWithAttempt runs the immutable recovery machine
// over a parent-created and secured output attempt (Rust worker.rs
// run_recovery over recovery/api.rs recover_precreated_local). Only
// the worker binary calls this entry; in-process callers keep
// RecoverImmutable and its client create position.
func RecoverImmutableWithAttempt(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, check func() error, sink RecoverySink, attempt *publication.PublishAttempt) (*RecoveryResult, *RecoveryPreparationFailure) {
	return recoverPrecreatedWithAttempt(sourcePath, candidate, destinationPath, sourceModeImmutable, budget, check, sink, attempt)
}

// RecoverOfflineWithAttempt runs the quiescent recovery machine over
// a parent-created and secured output attempt (Rust worker.rs
// run_recovery over recover_precreated_local; the certification is
// consumed by the caller boundary exactly like the Rust `let _ =
// certification` arm).
func RecoverOfflineWithAttempt(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, check func() error, sink RecoverySink, attempt *publication.PublishAttempt) (*RecoveryResult, *RecoveryPreparationFailure) {
	return recoverPrecreatedWithAttempt(sourcePath, candidate, destinationPath, sourceModeOffline, budget, check, sink, attempt)
}

// RecoverLiveWithAttempt runs the live recovery machine over a
// parent-created and secured output attempt (Rust worker.rs
// run_recovery over recover_precreated_local). The platform support
// refusal runs before the machine, exactly like the in-process arm.
func RecoverLiveWithAttempt(sourcePath string, candidate *RecoveryCandidate, destinationPath string, budget *RecoveryBudget, check func() error, sink RecoverySink, attempt *publication.PublishAttempt) (*RecoveryResult, *RecoveryPreparationFailure) {
	if err := live.RequireLiveSupported(); err != nil {
		return nil, earlyRecoveryFailure(err)
	}
	return recoverPrecreatedWithAttempt(sourcePath, candidate, destinationPath, sourceModeLive, budget, check, sink, attempt)
}

// buildRecoveryKind constructs one recovery output by the source kind
// (Rust api.rs build: the direct, membership, and structured arms).
func buildRecoveryKind(m *mapping.Mapping, meta format.Meta, builder *writer.OutputBuilder, budget *RecoveryBudget, check func() error, sink RecoverySink) (*Construction, *constructionFailure) {
	switch meta.ValueKind {
	case format.ValueKindDirect:
		return directConstruct(m, meta, builder, budget, check, sink)
	case format.ValueKindMembership:
		return membershipConstruct(m, meta, builder, budget, check, sink)
	case format.ValueKindStructured:
		return structuredConstruct(m, meta, builder, budget, check, sink)
	default:
		return nil, &constructionFailure{cause: &format.Error{Code: format.CodeInvalidEnum, Detail: "recovery value kind is invalid"}}
	}
}

// completedRecoveryView folds the completed publication into the
// recovery terminal (Rust terminal::completed).
func completedRecoveryView(report RecoveryReport, publication publication.PublicationResult) *RecoveryResult {
	result := completedRecovery(report, publication)
	return &result
}

// validateRecoveryBudget proves the budget against the source mode
// (Rust api.rs validate_budget: the budget validation, the open-files
// minimum of two plus the live reserve, and the effective budget).
func validateRecoveryBudget(budget *RecoveryBudget, mode sourceMode) (*RecoveryBudget, *RecoveryPreparationFailure) {
	if err := budget.validate(); err != nil {
		return nil, earlyRecoveryFailure(err)
	}
	reserved := uint32(0)
	if mode == sourceModeLive {
		reserved = 1
	}
	minimum, ok := checkedAddU32(2, reserved)
	if !ok {
		return nil, earlyRecoveryFailure(overflowError("recovery open files"))
	}
	if budget.MaxOpenFiles < minimum {
		return nil, earlyRecoveryFailure(budgetError("recovery source and output files"))
	}
	effective := *budget
	effective.MaxOpenFiles -= reserved
	return &effective, nil
}

// FromAttemptFailure folds one publication attempt-creation failure
// into the recovery failure (Rust recover_once create and secure
// arms: the source never opened, so the source guard stays nil; the
// output facts appear only when the discard ledger retained them).
// The worker client arm builds its parent-side create/secure failure
// through this exported entry.
func FromAttemptFailure(failure *publication.PublicationPreparationFailure) *RecoveryPreparationFailure {
	out := fromPublicationFailure(failure, RecoveryReport{})
	if failure.Cleanup.Empty() {
		out.Output = nil
	}
	return out
}

// checkedAddU32 folds one checked 32-bit addition.
func checkedAddU32(left, right uint32) (uint32, bool) {
	total := left + right
	return total, total >= left
}

// encodeRecoveryIdentity encodes one device+inode pair like the
// source publication identities (the snapshot encodeIdentity peer).
func encodeRecoveryIdentity(device, inode uint64) [32]byte {
	var bytes [32]byte
	format.PutU64(bytes[0:8], device)
	format.PutU64(bytes[8:16], inode)
	return bytes
}
