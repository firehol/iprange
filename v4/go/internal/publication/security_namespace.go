//go:build !windows

// Creator-only security failures folded to the namespace classes
// (Rust security functions return NamespaceError directly: IoAt for
// the named operation failures, Io for the plain metadata class,
// AccessPolicy for the proof, Unsupported for missing ACL machinery).
// The Go security owner reports format classes; the fold happens at
// this machine boundary so publication callers see one error type,
// exactly like Rust.

package publication

import (
	"errors"
	"strings"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// securityNamespaceError maps one security failure to the namespace
// classes. The Go owner carries the Rust operation label as the
// leading "operation: cause" detail segment of its CodeIO class; the
// plain metadata failure (Rust Io) is the only CodeIO arm without a
// named operation class.
func securityNamespaceError(err error) error {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return err
	}
	switch fe.Code {
	case format.CodeAccessPolicyUnsupported:
		return &live.NamespaceError{Kind: live.NamespaceAccessPolicy}
	case format.CodeDurabilityUnsupported, format.CodeOSUnsupported:
		return &live.NamespaceError{Kind: live.NamespaceUnsupported}
	case format.CodeIO:
		op, _, _ := strings.Cut(fe.Detail, ": ")
		switch op {
		case "apply creator-only mode", "remove inherited access ACL", "verify absent access ACL":
			return &live.NamespaceError{Kind: live.NamespaceIoAt, Op: op, Err: fe}
		}
		return &live.NamespaceError{Kind: live.NamespaceIo, Err: fe}
	}
	return err
}
