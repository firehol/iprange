package bootstrap

// Typed bootstrap-refusal classification (Rust BootstrapError). Every
// error returned by this package is a *ProblemError: it carries the
// SDK format error (so all existing callers keep their class checks)
// plus the exact Rust variant that validation maps to findings and
// recovery maps to candidate states.

import (
	"bytes"
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// ProblemKind is the Rust BootstrapError discriminant.
type ProblemKind uint8

const (
	ProblemFileTooShort ProblemKind = iota
	ProblemFileUnaligned
	ProblemHostAddressability
	ProblemStaticIdentityMismatch
	ProblemNoBootstrapMeta
	ProblemTransactionGap
	ProblemPhysicalParity
	ProblemEqualTransactionDisagreement
	ProblemCurrentGenerationUnprovable
	ProblemImmutableLengthMismatch
	ProblemUnsupportedStructure
)

// ProblemError is one classified bootstrap refusal (Rust
// BootstrapError transported through the SDK format-error class).
type ProblemError struct {
	Format            *format.Error
	Kind              ProblemKind
	Meta0MagicInvalid bool
	Meta1MagicInvalid bool
	StructureKindCode uint8
}

// Error implements error (the SDK format detail).
func (e *ProblemError) Error() string { return e.Format.Error() }

// Unwrap exposes the SDK format error to errors.As/Is.
func (e *ProblemError) Unwrap() error { return e.Format }

// AsProblem classifies one bootstrap error; ok is false for errors the
// bootstrap authority did not produce.
func AsProblem(err error) (*ProblemError, bool) {
	var problem *ProblemError
	if !errors.As(err, &problem) {
		return nil, false
	}
	return problem, true
}

// problemErr builds one classified refusal (Rust
// BootstrapError::FileTooShort / FileUnaligned / ... as mapped by the
// caller).
func problemErr(kind ProblemKind, detail string) *ProblemError {
	return &ProblemError{Format: formatErr(detail), Kind: kind}
}

// metaMagicInvalid reports whether one meta page fails the magic check
// specifically (Rust bootstrap meta_magic_valid; the MetaProblem::Magic
// split that selects the MetaUnavailable finding reason).
func metaMagicInvalid(page []byte) bool {
	return len(page) < 8 || !bytes.Equal(page[0:8], format.MainMagic[:])
}
