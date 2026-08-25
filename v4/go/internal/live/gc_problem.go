//go:build windows

// Fixed problem folding of the Windows GC machine (Rust
// publication/problem.rs Problem::namespace + the fixed cleanup
// classes). The live GC machine cannot import the publication problem
// module, so the same Rust table is implemented here verbatim; the
// publication surface consumes these shapes without remapping.

package live

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// gcCleanupConflict builds the fixed cleanup-conflict class (Rust
// Problem::cleanup_conflict).
func gcCleanupConflict(detail string) *format.Error {
	return &format.Error{Code: format.CodeCleanupConflict, Detail: detail}
}

// gcCleanupInProgress builds the fixed cleanup-in-progress class (Rust
// Problem::cleanup_in_progress).
func gcCleanupInProgress(detail string) *format.Error {
	return &format.Error{Code: format.CodeCleanupInProgress, Detail: detail}
}

// gcNamespaceProblem maps one retained-directory namespace error to
// the fixed problem shape (Rust Problem::namespace, identical to the
// publication mapping: plain Io reports the fixed detail, IoAt reports
// its operation label, and a no-follow final symlink folds to
// Conflict).
func gcNamespaceProblem(err error) *format.Error {
	var fe *format.Error
	if errors.As(err, &fe) && fe.Code == format.CodeAccessPolicyUnsupported {
		return &format.Error{Code: format.CodeAccessPolicyUnsupported, Detail: "creator-only access policy is not proved"}
	}
	nerr, ok := AsNamespaceError(err)
	if !ok {
		return gcSdkProblem(err)
	}
	switch nerr.Kind {
	case NamespaceInvalidName:
		return &format.Error{Code: format.CodeNameInvalid, Detail: "invalid destination name"}
	case NamespaceNotDirectory:
		return &format.Error{Code: format.CodeConflict, Detail: "destination parent is not a directory"}
	case NamespaceNotRegular:
		return &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
	case NamespaceExists:
		return &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"}
	case NamespaceMissing:
		return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
	case NamespaceIdentityChanged:
		return &format.Error{Code: format.CodeConflict, Detail: "publication inode identity changed"}
	case NamespaceLinkCount:
		if nerr.Links == 0 {
			return &format.Error{Code: format.CodeConflict, Detail: "publication inode has no links"}
		}
		return &format.Error{Code: format.CodeConflict, Detail: "publication inode link count changed"}
	case NamespaceCrossFilesystem:
		return &format.Error{Code: format.CodePublicationUnsupported, Detail: "publication inode is on another filesystem"}
	case NamespaceAccessPolicy:
		return &format.Error{Code: format.CodeAccessPolicyUnsupported, Detail: "creator-only access policy is not proved"}
	case NamespaceUnsupported:
		return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "filesystem lacks required durable namespace operations"}
	case NamespaceForkedHandle:
		return &format.Error{Code: format.CodeForkedHandle, Detail: "publication handle crossed fork"}
	case NamespaceIo:
		return &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	case NamespaceIoAt:
		if IsNofollowSymlink(nerr.Err) {
			return &format.Error{Code: format.CodeConflict, Detail: "publication name is a symlink"}
		}
		return &format.Error{Code: format.CodeIO, Detail: nerr.Op}
	}
	panic("unreachable namespace error kind")
}

// gcProblemOf converts one machine error to the fixed problem value
// (the gc machine produces only format errors; a foreign class folds
// to the io class with its text, and the conversion sites never pass
// nil).
func gcProblemOf(err error) *format.Error {
	var fe *format.Error
	if errors.As(err, &fe) {
		return fe
	}
	return gcSdkProblem(err)
}

// gcSdkProblem wraps one non-namespace failure in the io class (Rust
// Problem::sdk; the Go sdk class carries the cause detail).
func gcSdkProblem(err error) *format.Error {
	return &format.Error{Code: format.CodeIO, Detail: err.Error()}
}
