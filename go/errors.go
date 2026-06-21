package iprangeformat

import "fmt"

// Error is a typed format error. Class mirrors the Rust Error variant names so the
// shared conformance corpus's reject_class values match across implementations
// (e.g. "InvalidInput", "Structural", "Invariant", "IntegrityFailed").
type Error struct {
	Class string
	Msg   string
}

func (e *Error) Error() string { return e.Class + ": " + e.Msg }

func errf(class, msg string) *Error { return &Error{Class: class, Msg: msg} }

// Constructors, one per Rust Error variant used by this package.
func errBadMagic() *Error { return errf("BadMagic", "not IPRANGE3") }
func errUnsupportedMajor(v uint16) *Error {
	return errf("UnsupportedMajor", fmt.Sprintf("version_major %d (expected 3)", v))
}
func errBadHeaderSize(s uint16) *Error {
	return errf("BadHeaderSize", fmt.Sprintf("header_size %d", s))
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
func errIntegrity(msg string) *Error         { return errf("IntegrityFailed", msg) }
func errInvalidInput(msg string) *Error      { return errf("InvalidInput", msg) }
