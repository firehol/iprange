// Namespace error classes of the retained-directory machine (Rust
// publication::namespace NamespaceError). The machine returns these
// errors; every caller maps them to its public surface exactly like
// the Rust paths: the live path folds through namespace_error, the
// publication resolver folds through Problem::namespace.

package live

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// NamespaceErrorKind is the discriminant of one retained-directory
// namespace error (Rust NamespaceError). Io is the plain metadata/open
// class (Rust NamespaceError::Io); IoAt is a named operation failure
// (Rust IoAt{operation, source}) whose operation label is its detail;
// LinkCount carries the observed link count (Rust LinkCount(u64)).
type NamespaceErrorKind uint8

// The kind order mirrors the Rust NamespaceError enum declaration
// (namespace.rs), so the two tables stay 1:1 readable.
const (
	NamespaceInvalidName NamespaceErrorKind = iota
	NamespaceNotDirectory
	NamespaceNotRegular
	NamespaceExists
	NamespaceMissing
	NamespaceIdentityChanged
	NamespaceLinkCount
	NamespaceCrossFilesystem
	NamespaceAccessPolicy
	NamespaceUnsupported
	NamespaceForkedHandle
	NamespaceIo
	NamespaceIoAt
)

// NamespaceError is one retained-directory namespace error (Rust
// publication::namespace NamespaceError; the Go peer of the Rust
// enum, with os.PathError-style exported facts). Kind carries the
// class; Op is the operation label of the Io/IoAt classes; Links is
// the observed link count of the LinkCount class; Err is the wrapped
// syscall cause of the Io/IoAt classes.
type NamespaceError struct {
	Kind  NamespaceErrorKind
	Op    string
	Links uint64
	Err   error
}

// AsNamespaceError classifies one error as a namespace error; ok is
// false when err is not one (Rust namespace_error receives the typed
// NamespaceError, so callers map other error types separately).
func AsNamespaceError(err error) (*NamespaceError, bool) {
	var nerr *NamespaceError
	if !errors.As(err, &nerr) {
		return nil, false
	}
	return nerr, true
}

func (e *NamespaceError) Error() string {
	switch e.Kind {
	case NamespaceIo, NamespaceIoAt:
		return e.Op + ": " + e.Err.Error()
	case NamespaceInvalidName:
		return "feed name is invalid"
	case NamespaceExists:
		return "feed name already exists"
	case NamespaceMissing:
		return "feed name does not exist"
	case NamespaceUnsupported, NamespaceCrossFilesystem:
		return "live file namespace lacks required local operations"
	default:
		return "live file ownership changed"
	}
}

func (e *NamespaceError) Unwrap() error { return e.Err }

// nsInvalidNameError builds the invalid-name class (Rust
// NamespaceError::InvalidName).
func nsInvalidNameError() *NamespaceError { return &NamespaceError{Kind: NamespaceInvalidName} }

// nsExistsError builds the exists class (Rust NamespaceError::Exists).
func nsExistsError() *NamespaceError { return &NamespaceError{Kind: NamespaceExists} }

// nsMissingError builds the missing class (Rust NamespaceError::Missing).
func nsMissingError() *NamespaceError { return &NamespaceError{Kind: NamespaceMissing} }

// nsIoError builds one named operation failure (Rust
// NamespaceError::IoAt{operation, source}; the operation label is the
// public io detail).
func nsIoError(op string, err error) *NamespaceError {
	return &NamespaceError{Kind: NamespaceIoAt, Op: op, Err: err}
}

// nsPlainIoError builds one plain metadata/open failure (Rust
// NamespaceError::Io(source)); its public detail is the fixed
// publication filesystem-operation string, so the operation label is
// diagnostic only.
func nsPlainIoError(op string, err error) *NamespaceError {
	return &NamespaceError{Kind: NamespaceIo, Op: op, Err: err}
}

func nsUnsupportedError() *NamespaceError     { return &NamespaceError{Kind: NamespaceUnsupported} }
func nsCrossFilesystemError() *NamespaceError { return &NamespaceError{Kind: NamespaceCrossFilesystem} }
func nsNotDirectoryError() *NamespaceError    { return &NamespaceError{Kind: NamespaceNotDirectory} }
func nsNotRegularError() *NamespaceError      { return &NamespaceError{Kind: NamespaceNotRegular} }
func nsIdentityChangedError() *NamespaceError { return &NamespaceError{Kind: NamespaceIdentityChanged} }

// nsLinkCountError builds the link-count class with the observed count
// (Rust NamespaceError::LinkCount(links)).
func nsLinkCountError(links uint64) *NamespaceError {
	return &NamespaceError{Kind: NamespaceLinkCount, Links: links}
}

// nsAccessPolicyError builds the creator-only security class (Rust
// NamespaceError::AccessPolicy).
func nsAccessPolicyError() *NamespaceError { return &NamespaceError{Kind: NamespaceAccessPolicy} }

// nsForkedHandleError builds the cross-fork class (Rust
// NamespaceError::ForkedHandle; Go cannot fork, so owners produce it
// only through identity ownership checks).
func nsForkedHandleError() *NamespaceError { return &NamespaceError{Kind: NamespaceForkedHandle} }

// nsMap maps one namespace error to the public SDK error (Rust
// live_namespace::namespace_error: Unsupported and CrossFilesystem
// fold to DurabilityUnsupported, the wrong-mode classes fold to
// WrongState with the single ownership-changed detail, ForkedHandle
// keeps its class).
func nsMap(err error) error {
	nerr, ok := AsNamespaceError(err)
	if !ok {
		return err
	}
	switch nerr.Kind {
	case NamespaceInvalidName:
		return &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	case NamespaceExists:
		return &format.Error{Code: format.CodeNameExists, Detail: "feed name already exists"}
	case NamespaceMissing:
		return &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	case NamespaceIo, NamespaceIoAt:
		return &format.Error{Code: format.CodeIO, Detail: err.Error()}
	case NamespaceUnsupported, NamespaceCrossFilesystem:
		return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "live file namespace lacks required local operations"}
	case NamespaceForkedHandle:
		return &format.Error{Code: format.CodeForkedHandle}
	default:
		return &format.Error{Code: format.CodeWrongState, Detail: "live file ownership changed"}
	}
}

// nsMapParentIdentity maps one namespace error for the parent-identity
// surface (Rust live_namespace::parent_identity): a missing parent is
// the Io(NotFound) class, every other class maps through namespace_error.
func nsMapParentIdentity(err error) error {
	if nerr, ok := AsNamespaceError(err); ok && nerr.Kind == NamespaceMissing {
		return &format.Error{Code: format.CodeIO, Detail: "live parent directory does not exist"}
	}
	return nsMap(err)
}

// liveSecurityError maps one creator-only security failure to the live
// namespace classes (Rust create_private folds security errors through
// namespace_error: AccessPolicy becomes the WrongState ownership class,
// Unsupported becomes DurabilityUnsupported; the CodeAccessPolicyUnsupported
// problem class belongs to the publication resolver surface).
func liveSecurityError(err error) error {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return err
	}
	switch fe.Code {
	case format.CodeAccessPolicyUnsupported:
		return &format.Error{Code: format.CodeWrongState, Detail: "live file ownership changed"}
	case format.CodeDurabilityUnsupported:
		return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "live file namespace lacks required local operations"}
	}
	return err
}
