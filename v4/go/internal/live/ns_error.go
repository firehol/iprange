// Namespace error classes of the retained-directory machine (Rust
// publication::namespace NamespaceError) and their public mapping
// (Rust live_namespace::namespace_error). The machine returns these
// classes; every caller maps them to the public SDK error exactly like
// the Rust live path, with the parent-identity open special case.

package live

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

type nsErrorKind uint8

const (
	nsInvalidName nsErrorKind = iota
	nsExists
	nsMissing
	nsIo
	nsUnsupported
	nsCrossFilesystem
	nsNotDirectory
	nsNotRegular
	nsIdentityChanged
	nsLinkCount
)

// nsError is one NamespaceError with its Rust public detail already
// attached (namespace.rs Error Display payloads; the io class keeps the
// operation and source for the CodeIO detail).
type nsError struct {
	kind nsErrorKind
	op   string
	err  error
}

func (e *nsError) Error() string {
	switch e.kind {
	case nsIo:
		return e.op + ": " + e.err.Error()
	case nsInvalidName:
		return "feed name is invalid"
	case nsExists:
		return "feed name already exists"
	case nsMissing:
		return "feed name does not exist"
	case nsUnsupported, nsCrossFilesystem:
		return "live file namespace lacks required local operations"
	default:
		return "live file ownership changed"
	}
}

func (e *nsError) Unwrap() error { return e.err }

func nsInvalidNameError() *nsError { return &nsError{kind: nsInvalidName} }
func nsExistsError() *nsError      { return &nsError{kind: nsExists} }
func nsMissingError() *nsError     { return &nsError{kind: nsMissing} }
func nsIoError(op string, err error) *nsError {
	return &nsError{kind: nsIo, op: op, err: err}
}
func nsUnsupportedError() *nsError     { return &nsError{kind: nsUnsupported} }
func nsCrossFilesystemError() *nsError { return &nsError{kind: nsCrossFilesystem} }
func nsNotDirectoryError() *nsError    { return &nsError{kind: nsNotDirectory} }
func nsNotRegularError() *nsError      { return &nsError{kind: nsNotRegular} }
func nsIdentityChangedError() *nsError { return &nsError{kind: nsIdentityChanged} }
func nsLinkCountError() *nsError       { return &nsError{kind: nsLinkCount} }

func nsErrorKindOf(err error) (nsErrorKind, bool) {
	var nerr *nsError
	if !errors.As(err, &nerr) {
		return 0, false
	}
	return nerr.kind, true
}

// nsMap maps one namespace error to the public SDK error (Rust
// live_namespace::namespace_error: Unsupported and CrossFilesystem
// fold to DurabilityUnsupported, the wrong-mode classes fold to
// WrongState with the single ownership-changed detail).
func nsMap(err error) error {
	kind, ok := nsErrorKindOf(err)
	if !ok {
		return err
	}
	switch kind {
	case nsInvalidName:
		return &format.Error{Code: format.CodeNameInvalid, Detail: "feed name is invalid"}
	case nsExists:
		return &format.Error{Code: format.CodeNameExists, Detail: "feed name already exists"}
	case nsMissing:
		return &format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"}
	case nsIo:
		return &format.Error{Code: format.CodeIO, Detail: err.Error()}
	case nsUnsupported, nsCrossFilesystem:
		return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "live file namespace lacks required local operations"}
	default:
		return &format.Error{Code: format.CodeWrongState, Detail: "live file ownership changed"}
	}
}

// nsMapParentIdentity maps one namespace error for the parent-identity
// surface (Rust live_namespace::parent_identity): a missing parent is
// the Io(NotFound) class, every other class maps through namespace_error.
func nsMapParentIdentity(err error) error {
	if kind, ok := nsErrorKindOf(err); ok && kind == nsMissing {
		return &format.Error{Code: format.CodeIO, Detail: "live parent directory does not exist"}
	}
	return nsMap(err)
}

// liveSecurityError maps one creator-only security failure to the live
// namespace classes (Rust create_private folds security errors through
// namespace_error: AccessPolicy becomes the WrongState ownership class,
// Unsupported becomes DurabilityUnsupported; the CodeAccessPolicyUnsupported
// problem class belongs to the publication resolver surface, chunk 4-8).
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
