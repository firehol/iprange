package recovery

// Offline-candidate validation (Rust validation.rs validate_offline +
// verify_offline_candidate): one retained recovery-candidate state of
// a quiescent database path is validated under the exclusive lifetime
// lock, without changing the source. The arm lives here - not in the
// validation package - because the Go mode enum cannot carry the
// candidate payload of the Rust ValidationMode::OfflineCandidate
// variant; the shared sweep is composed from the validation package.

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// ValidateOfflineCandidate validates one retained recovery candidate
// of a quiescent database path (the Go entry of the Rust
// validate_offline arm): the exclusive-lifetime-locked source is
// opened, the token identity and the selected meta are re-proved, the
// candidate state is swept through the shared validation machine, and
// the terminal re-verifies the source and the token before the result
// is reported. Exactly one of the result and the failure is non-nil;
// the failure carries the partial progress.
func ValidateOfflineCandidate(path string, candidate *RecoveryCandidate, budget *validation.ValidationBudget, check func() error, sink validation.ValidationSink) (*validation.ValidationResult, *validation.ValidationFailure) {
	if candidate == nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(&format.Error{Code: format.CodeInvalidArgument, Detail: "offline-candidate validation requires a candidate token"}, &progress)
	}
	source, err := openOfflineSource(path, check)
	if err != nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(err, &progress)
	}
	identity := source.publicIdentity()
	classified, err := readClassified(source.file, check)
	if err != nil {
		progress := validation.NewProgress()
		return nil, validation.Failure(closeOffline(source, err), &progress)
	}
	progress := validation.NewProgress()
	if identity != candidate.SourceIdentity {
		return nil, validation.Failure(closeOffline(source, &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "recovery candidate source identity changed"}), &progress)
	}
	meta, ok := classified.selectedMeta(candidate)
	if !ok {
		return nil, validation.Failure(closeOffline(source, &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "recovery candidate no longer selected"}), &progress)
	}
	if err := source.requireAvailable(meta.DatabaseID); err != nil {
		return nil, validation.Failure(closeOffline(source, err), &progress)
	}
	swept, err := validation.SweepSelected(source.file, meta, budget, check, sink)
	verification := verifyOfflineCandidate(source, candidate, check)
	if err != nil {
		return nil, validation.Failure(closeOffline(source, err), &swept)
	}
	if verification != nil {
		return nil, validation.Failure(closeOffline(source, verification), &swept)
	}
	if err := source.close(); err != nil {
		return nil, validation.Failure(err, &swept)
	}
	return &validation.ValidationResult{
		Valid:        swept.FindingCount == 0,
		FileIdentity: identity,
		Generation:   validation.Generation(meta),
		Progress:     swept,
	}, nil
}

// verifyOfflineCandidate re-proves the token after the sweep (Rust
// verify_offline_candidate): the source path identity, the re-read
// classification, the still-selected token, and the source path
// identity again, all under the still-held exclusive lock.
func verifyOfflineCandidate(source *offlineSource, candidate *RecoveryCandidate, check func() error) error {
	if err := source.verify(); err != nil {
		return candidateIdentityError(err)
	}
	classified, err := readClassified(source.file, check)
	if err != nil {
		return err
	}
	if _, ok := classified.selectedMeta(candidate); !ok {
		return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "recovery candidate no longer selected"}
	}
	if err := source.verify(); err != nil {
		return candidateIdentityError(err)
	}
	return nil
}

// candidateIdentityError maps one terminal source identity failure to
// the candidate-changed class (Rust candidate_identity_error: the
// wrong-mode class is the candidate-changed class; the Rust Io
// NotFound arm has no Go counterpart because the Go namespace proofs
// surface the missing path as NameNotFound, which Rust also leaves
// unchanged).
func candidateIdentityError(cause error) error {
	var fe *format.Error
	if errors.As(cause, &fe) && fe.Code == format.CodeWrongState {
		return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: cause.Error()}
	}
	return cause
}
