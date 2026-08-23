// Fixed, allocation-free publication failure details (Rust
// publication/problem.rs). Every code and detail string is verbatim
// Rust; Go does not carry os_code (design decision 6), and the
// Windows-only gc/checkpoint/in-progress arms are recorded with their
// Phase-2 and 4-10/4-11 chunks, never stubbed here.

package publication

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// problem builds one fixed problem (Rust PublicationProblem::new).
func problem(code format.ErrorCode, detail string) *format.Error {
	return &format.Error{Code: code, Detail: detail}
}

// cleanupConflictProblem builds the fixed cleanup-conflict class (Rust
// Problem::cleanup_conflict).
func cleanupConflictProblem(detail string) *format.Error {
	return problem(format.CodeCleanupConflict, detail)
}

// checkpointProblem is the Go peer of the Rust Error::Checkpoint
// (reservation_file::Error / main_file::Error) and Result::Checkpoint
// arms: one observer or machine checkpoint problem that is already in
// its final fixed shape and must pass every composition fold
// unchanged, exactly like the Rust problem clone.
type checkpointProblem struct {
	problem *format.Error
}

func (e *checkpointProblem) Error() string { return e.problem.Error() }
func (e *checkpointProblem) Unwrap() error { return e.problem }

// asCheckpointProblem unwraps one checkpoint problem, or nil.
func asCheckpointProblem(err error) *format.Error {
	var checkpoint *checkpointProblem
	if errors.As(err, &checkpoint) {
		return checkpoint.problem
	}
	return nil
}

// namespaceProblem maps one retained-directory namespace error to the
// fixed publication problem (Rust Problem::namespace). The plain Io
// class always reports the fixed filesystem-operation detail; the
// IoAt class reports its operation label, and a no-follow final
// symlink folds to the Conflict class.
func namespaceProblem(err error) *format.Error {
	// The creator-only security owner reports the AccessPolicy class
	// directly (Rust folds it into NamespaceError::AccessPolicy at the
	// machine boundary; both fold to the same problem here).
	var fe *format.Error
	if errors.As(err, &fe) && fe.Code == format.CodeAccessPolicyUnsupported {
		return problem(format.CodeAccessPolicyUnsupported, "creator-only access policy is not proved")
	}
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		return sdkProblem(err)
	}
	switch nerr.Kind {
	case live.NamespaceInvalidName:
		return problem(format.CodeNameInvalid, "invalid destination name")
	case live.NamespaceNotDirectory:
		return problem(format.CodeConflict, "destination parent is not a directory")
	case live.NamespaceNotRegular:
		return problem(format.CodeConflict, "publication name is not a regular file")
	case live.NamespaceExists:
		return problem(format.CodeNameExists, "publication name already exists")
	case live.NamespaceMissing:
		return problem(format.CodeNameNotFound, "publication name is missing")
	case live.NamespaceIdentityChanged:
		return problem(format.CodeConflict, "publication inode identity changed")
	case live.NamespaceLinkCount:
		if nerr.Links == 0 {
			return problem(format.CodeConflict, "publication inode has no links")
		}
		return problem(format.CodeConflict, "publication inode link count changed")
	case live.NamespaceCrossFilesystem:
		return problem(format.CodePublicationUnsupported, "publication inode is on another filesystem")
	case live.NamespaceAccessPolicy:
		return problem(format.CodeAccessPolicyUnsupported, "creator-only access policy is not proved")
	case live.NamespaceUnsupported:
		return problem(format.CodeDurabilityUnsupported, "filesystem lacks required durable namespace operations")
	case live.NamespaceForkedHandle:
		return problem(format.CodeForkedHandle, "publication handle crossed fork")
	case live.NamespaceIo:
		return problem(format.CodeIO, "publication filesystem operation failed")
	case live.NamespaceIoAt:
		if live.IsNofollowSymlink(nerr.Err) {
			return problem(format.CodeConflict, "publication name is a symlink")
		}
		return problem(format.CodeIO, nerr.Op)
	}
	panic("unreachable namespace error kind")
}

// outputProblem maps one output-machine failure (Rust Problem::output:
// Namespace, Sdk, Bootstrap, Gc, FinishedMetaChanged,
// FinishedLengthChanged). The Gc arm is Windows-only and recorded with
// Phase-2, never stubbed here.
func outputProblem(err error) *format.Error {
	if _, ok := live.AsNamespaceError(err); ok {
		return namespaceProblem(err)
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		return sdkProblem(err)
	}
	switch fe.Code {
	case format.CodeAccessPolicyUnsupported:
		return problem(format.CodeAccessPolicyUnsupported, "creator-only access policy is not proved")
	case format.CodeFormatInvalid:
		if fe.Detail == "" {
			return problem(format.CodeFormatInvalid, "output metadata is malformed")
		}
		return fe
	case format.CodeConflict:
		// The finished-output arms carry Rust-verbatim fixed details
		// from the producer ("finished output metadata changed" /
		// "finished output length changed"); preserve them.
		return fe
	default:
		return sdkProblem(err)
	}
}

// reservationProblem maps one reservation-file failure (Rust
// Problem::reservation: Namespace, Sdk, Output, Gc, Checkpoint, Codec,
// HeaderChanged, HeaderInvariant, LengthChanged). The Gc and
// Checkpoint arms are Windows-only / 4-10-4-11 and recorded there,
// never stubbed here.
func reservationProblem(err error) *format.Error {
	if _, ok := live.AsNamespaceError(err); ok {
		return namespaceProblem(err)
	}
	if checkpoint := asCheckpointProblem(err); checkpoint != nil {
		return checkpoint
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		return sdkProblem(err)
	}
	switch fe.Code {
	case format.CodeAccessPolicyUnsupported:
		return problem(format.CodeAccessPolicyUnsupported, "creator-only access policy is not proved")
	case format.CodeFormatInvalid:
		// The codec and header-invariant arms carry Rust-verbatim
		// fixed details from the producer ("reservation record is
		// malformed" / "reservation state is inconsistent"); preserve
		// them. A bare FormatInvalid class is the output bootstrap
		// fold (Rust reservation_file::Error::Output).
		if fe.Detail == "" {
			return outputProblem(err)
		}
		return fe
	case format.CodeConflict:
		// The header-changed and length-changed arms carry
		// Rust-verbatim fixed details ("reservation record changed" /
		// "reservation length changed"); preserve them.
		return fe
	default:
		return sdkProblem(err)
	}
}

// replacementProblem maps one replacement failure (Rust
// Problem::replacement: Namespace, Sdk, Output, SameIdentity,
// ContentChanged).
func replacementProblem(err error) *format.Error {
	if _, ok := live.AsNamespaceError(err); ok {
		return namespaceProblem(err)
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		return sdkProblem(err)
	}
	switch fe.Code {
	case format.CodeAccessPolicyUnsupported:
		return problem(format.CodeAccessPolicyUnsupported, "creator-only access policy is not proved")
	case format.CodeFormatInvalid:
		return outputProblem(err)
	case format.CodeConflict:
		// The same-identity and content-changed arms carry
		// Rust-verbatim fixed details from the producer ("replacement
		// source and destination identities match" / "replacement
		// destination content changed"); preserve them.
		return fe
	default:
		return sdkProblem(err)
	}
}

// mainProblem maps one main-file failure (Rust Problem::main:
// Namespace, Sdk, Output, Reservation, Checkpoint, PreviousLinkCount,
// ReservationLinkCount). The Checkpoint arm is recorded with
// 4-10/4-11, the Gc arm is Windows-only Phase 2, and the Injected arm
// is test-only (the Go crash harness maps it at the fault boundary);
// none are stubbed here.
func mainProblem(err error) *format.Error {
	if _, ok := live.AsNamespaceError(err); ok {
		return namespaceProblem(err)
	}
	if checkpoint := asCheckpointProblem(err); checkpoint != nil {
		return checkpoint
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		return sdkProblem(err)
	}
	switch fe.Code {
	case format.CodeAccessPolicyUnsupported:
		return problem(format.CodeAccessPolicyUnsupported, "creator-only access policy is not proved")
	case format.CodeFormatInvalid:
		return outputProblem(err)
	case format.CodeConflict:
		// The reservation arms carry Rust-verbatim fixed details;
		// preserve them.
		return fe
	case format.CodeCleanupConflict:
		// The retired-link arms carry Rust-verbatim fixed details
		// ("retired previous destination still has a link" /
		// "retired reservation still has a link"); preserve them.
		return fe
	default:
		return sdkProblem(err)
	}
}

// sdkProblem maps one SDK operation failure (Rust Problem::sdk): the
// error code is preserved and the detail is the fixed Rust string; Go
// carries no os_code. Errors that are neither namespace errors nor
// typed SDK errors are unreachable in the ported machine; they fold to
// the Io class like an unclassified syscall failure.
func sdkProblem(err error) *format.Error {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return problem(format.CodeIO, "publication SDK operation failed")
	}
	return problem(fe.Code, "publication SDK operation failed")
}
