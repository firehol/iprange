// Stable ownership of the destination replaced by one publication
// (Rust publication/replacement.rs). The replacement bind opens the
// canonical main, proves it is not the attempt itself, locks it for
// the artifact lifetime, digests it, and re-proves it before and after
// the digest; the previous main is then retired by the main-file
// machine under the same lock.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// previousMain is the replaced canonical main (Rust PreviousMain).
type previousMain struct {
	file       *os.File
	mapping    *mapping.Mapping
	identity   live.FileIdentity
	byteLength uint64
	sha512     [64]byte
}

// Close releases the previous-main mapping and descriptor (Rust drop
// of PreviousMain).
func (p *previousMain) Close() error {
	var first error
	if p.mapping != nil {
		if err := p.mapping.Close(); err != nil && first == nil {
			first = err
		}
	}
	if p.file != nil {
		if err := p.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// replacementFailure is one replacement bind failure carrying the
// still-owned prepared output (Rust Failure<PreparedOutput> in
// replacement.rs).
type replacementFailure struct {
	output *preparedOutput
	cause  error
}

func (f *replacementFailure) Error() string { return f.cause.Error() }
func (f *replacementFailure) Unwrap() error { return f.cause }

// bindPrevious opens the replaced canonical main and attaches it to
// the prepared output under the replace-existing policy (Rust
// replacement::bind).
func bindPrevious(output *preparedOutput, check func() error) (*preparedOutput, *replacementFailure) {
	return bindPreviousWith(output, reservationPolicyReplaceExisting, check)
}

// bindPreviousNoRollback attaches the replaced canonical main under
// the replace-existing-no-rollback policy (Rust
// replacement::bind_no_rollback).
func bindPreviousNoRollback(output *preparedOutput, check func() error) (*preparedOutput, *replacementFailure) {
	return bindPreviousWith(output, reservationPolicyReplaceExistingNoRollback, check)
}

func bindPreviousWith(output *preparedOutput, policy reservationPolicy, check func() error) (*preparedOutput, *replacementFailure) {
	// rust: debug_assert!(policy.is_replacement())
	previous, err := openPrevious(output, check)
	if err != nil {
		return nil, &replacementFailure{output: output, cause: err}
	}
	output.policy = policy
	output.previous = previous
	return output, nil
}

// openPrevious opens and proves the replaced canonical main (Rust
// replacement.rs open).
func openPrevious(output *preparedOutput, check func() error) (*previousMain, error) {
	if err := live.Checkpoint(check); err != nil {
		return nil, err
	}
	destination := output.attempt.destination
	if err := destination.directory().RequireAbsent(destination.coordinationName()); err != nil {
		return nil, err
	}
	regular, err := destination.directory().OpenRegular(destination.mainName(), true)
	if err != nil {
		return nil, err
	}
	if regular == nil {
		return nil, &live.NamespaceError{Kind: live.NamespaceMissing}
	}
	if regular.Identity == output.attempt.identity {
		regular.File.Close()
		return nil, sameIdentityError()
	}
	if err := live.LockFileCancellable(regular.File, live.MainLifetimeOffset, live.LockExclusive, check); err != nil {
		regular.File.Close()
		return nil, err
	}
	if err := verifyCanonicalDestination(destination, regular.File, regular.Identity, 0, false); err != nil {
		regular.File.Close()
		return nil, err
	}
	if err := live.SyncFile(regular.File); err != nil {
		regular.File.Close()
		return nil, err
	}
	if err := live.Checkpoint(check); err != nil {
		regular.File.Close()
		return nil, err
	}
	st, err := regular.File.Stat()
	if err != nil {
		regular.File.Close()
		return nil, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	byteLength := uint64(st.Size())
	mapped, err := mapping.MapFile(regular.File, byteLength, false)
	if err != nil {
		regular.File.Close()
		return nil, err
	}
	sha512, err := digestCancellable(mapped, byteLength, check)
	if err != nil {
		mapped.Close()
		regular.File.Close()
		return nil, err
	}
	if err := verifyCanonicalDestination(destination, regular.File, regular.Identity, byteLength, true); err != nil {
		mapped.Close()
		regular.File.Close()
		return nil, err
	}
	return &previousMain{
		file:       regular.File,
		mapping:    mapped,
		identity:   regular.Identity,
		byteLength: byteLength,
		sha512:     sha512,
	}, nil
}

// sameIdentityError is the fixed same-identity class (Rust
// replacement::Error::SameIdentity, mapped to Problem::replacement by
// problem.go).
func sameIdentityError() error {
	return &format.Error{Code: format.CodeConflict, Detail: "replacement source and destination identities match"}
}

// contentChangedError is the fixed content-changed class (Rust
// replacement::Error::ContentChanged, mapped to Problem::replacement
// by problem.go).
func contentChangedError() error {
	return &format.Error{Code: format.CodeConflict, Detail: "replacement destination content changed"}
}

// verifyCanonicalNamespace proves the previous main is still the
// canonical main of the destination (Rust
// PreviousMain::verify_canonical_namespace).
func (p *previousMain) verifyCanonicalNamespace(destination *destination) error {
	return verifyCanonicalDestination(destination, p.file, p.identity, p.byteLength, true)
}

// verifyPrivateOrRetired proves the previous main is now at the
// private output name (one link) or fully unlinked (zero links) after
// the main rename (Rust PreviousMain::verify_private_or_retired).
func (p *previousMain) verifyPrivateOrRetired(destination *destination, privateName string) error {
	if err := destination.directory().Verify(); err != nil {
		return err
	}
	identity, err := live.RegularIdentityAnyLink(p.file, destination.directory().Identity())
	if err != nil {
		return err
	}
	size, err := fstatSize(p.file)
	if err != nil {
		return &live.NamespaceError{Kind: live.NamespaceIo, Op: "inspect retained file", Err: err}
	}
	if identity != p.identity || size != p.byteLength {
		return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	links, err := live.RegularLinkCount(p.file)
	if err != nil {
		return err
	}
	switch links {
	case 1:
		return destination.directory().VerifyName(privateName, p.identity)
	case 0:
		return destination.directory().RequireAbsent(privateName)
	default:
		return &live.NamespaceError{Kind: live.NamespaceLinkCount, Links: links}
	}
}

// verifyRetired proves the previous main is fully unlinked and gone
// from the destination (Rust PreviousMain::verify_retired).
func (p *previousMain) verifyRetired(destination *destination, privateName string) error {
	if err := destination.directory().Verify(); err != nil {
		return err
	}
	if err := destination.directory().RequireAbsent(privateName); err != nil {
		return err
	}
	identity, err := live.RegularIdentityAnyLink(p.file, destination.directory().Identity())
	if err != nil {
		return err
	}
	if identity != p.identity {
		return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	size, err := fstatSize(p.file)
	if err != nil {
		return &live.NamespaceError{Kind: live.NamespaceIo, Op: "inspect retained file", Err: err}
	}
	if size != p.byteLength {
		return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	links, err := live.RegularLinkCount(p.file)
	if err != nil {
		return err
	}
	if links != 0 {
		return &live.NamespaceError{Kind: live.NamespaceLinkCount, Links: links}
	}
	return nil
}

// verifyContent digests the retained previous main and compares it to
// the bind-time digest (Rust PreviousMain::verify_content). The
// check function is optional (Rust Option<CancellationToken>).
func (p *previousMain) verifyContent(destination *destination, check func() error) error {
	if err := p.verifyCanonicalNamespace(destination); err != nil {
		return err
	}
	var (
		digested [64]byte
		err      error
	)
	if check != nil {
		digested, err = digestCancellable(p.mapping, p.byteLength, check)
	} else {
		digested, err = digest(p.mapping, p.byteLength)
	}
	if err != nil {
		return err
	}
	if err := p.verifyCanonicalNamespace(destination); err != nil {
		return err
	}
	if digested != p.sha512 {
		return contentChangedError()
	}
	return nil
}

// verifyCanonicalDestination proves one open file is the canonical
// main of the destination with the expected identity and optionally
// the expected length (Rust replacement.rs verify_canonical).
func verifyCanonicalDestination(destination *destination, file *os.File, expected live.FileIdentity, expectedLength uint64, requireLength bool) error {
	if err := destination.directory().Verify(); err != nil {
		return err
	}
	if err := destination.directory().VerifyName(destination.mainName(), expected); err != nil {
		return err
	}
	identity, err := live.RegularIdentity(file, destination.directory().Identity())
	if err != nil {
		return err
	}
	if identity != expected {
		return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
	}
	if requireLength {
		st, err := file.Stat()
		if err != nil {
			return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
		}
		if uint64(st.Size()) != expectedLength {
			return &live.NamespaceError{Kind: live.NamespaceIdentityChanged}
		}
	}
	return nil
}
