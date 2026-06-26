package iprangedb

import "fmt"

// Error is a typed format error. The spec mandates that on any "reader MUST reject"
// check a conforming reader discards all partial state, returns a typed error, and
// exposes nothing from the file (§ intro). Class mirrors the Rust Error variant names so
// behavior matches across implementations; Msg is a short location tag, never
// attacker-influenced bytes (scope, counts) echoed verbatim.
type Error struct {
	Class string
	Msg   string
}

func (e *Error) Error() string { return e.Class + ": " + e.Msg }

func errf(class, msg string) *Error { return &Error{Class: class, Msg: msg} }

// Constructors, one per Rust Error variant used by this package.
func errUnsupportedMajor(v uint16) *Error {
	return errf("UnsupportedMajor", fmt.Sprintf("version_major %d (expected 4)", v))
}
func errBadMetaSize(s uint16) *Error {
	return errf("BadMetaSize", fmt.Sprintf("meta_size %d", s))
}
func errFileTooShort(need, have uint64) *Error {
	return errf("FileTooShort", fmt.Sprintf("need %d bytes, have %d", need, have))
}
func errFileSizeMismatch(header, real uint64) *Error {
	return errf("FileSizeMismatch", fmt.Sprintf("header=%d real=%d", header, real))
}
func errNonZeroReserved(where string) *Error { return errf("NonZeroReserved", where) }
func errOverflow(where string) *Error        { return errf("Overflow", where) }
func errStructural(msg string) *Error        { return errf("Structural", msg) }
func errInvariant(msg string) *Error         { return errf("Invariant", msg) }
func errChecksumFailed(where string) *Error  { return errf("ChecksumFailed", where) }
func errIncompatible(where string) *Error    { return errf("Incompatible", where) }
func errInvalidInput(msg string) *Error      { return errf("InvalidInput", msg) }
func errState(msg string) *Error             { return errf("State", msg) }

// errorClass returns the Class of a *Error, or "unknown" for any other error.
func errorClass(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Class
	}
	return "unknown"
}
