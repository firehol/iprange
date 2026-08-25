//go:build (freebsd || v4work) && !windows

// FreeBSD no-replace transition machine (Rust namespace_mutation.rs
// freebsd + `test` arms). Rust compiles this machine for freebsd and
// for test builds; the v4work test tag is the Go equivalent, so the
// same crash-safe transition is exercised on the linux/darwin test
// hosts exactly like the Rust suite does. Production freebsd reaches
// it through RenameNoReplace; the live sidecar still refuses freebsd,
// so the machine is publication-path-only there. The !windows guard
// keeps the v4work test tree cross-buildable on windows: the machine
// needs the POSIX Linkat and directory-identity primitives, which
// have no Windows counterpart (the Windows live surface is a tracked
// SOW-0026 stub).

package live

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/fault"
)

// linkState is the observed source/destination pair state of one
// no-replace transition (Rust LinkState).
type linkState uint8

const (
	linkStateSourceOnly linkState = iota
	linkStateLinked
	linkStateComplete
)

// linkNoReplace publishes the source identity at destination through
// the crash-safe linkat machine (Rust Directory::link_noreplace):
// the source file proves the any-link identity, the source name must
// still name it single-link, the linkat creates the destination, and
// the transition finishes or resumes through the EEXIST arm.
func (d *Directory) linkNoReplace(source string, sourceFile *os.File, destination string) error {
	expected, err := RegularIdentityAnyLink(sourceFile, d.id)
	if err != nil {
		return err
	}
	if err := d.requireSource(source, expected); err != nil {
		return err
	}
	err = unix.Linkat(int(d.file.Fd()), source, int(d.file.Fd()), destination, 0)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			state, serr := d.linkState(source, destination, expected)
			// Rust swallows a link_state error under EEXIST and reports
			// the plain Exists class; only the exact Linked pair resumes.
			if serr == nil && state == linkStateLinked {
				return d.FinishNoreplaceTransition(source, destination, expected)
			}
			return nsExistsError()
		}
		return nsIoError("link publication name without replacement", err)
	}
	fault.Crash("publication.freebsd.after_noreplace_link")
	return d.FinishNoreplaceTransition(source, destination, expected)
}

// FinishNoreplaceTransition completes or resumes a linkat transition
// (Rust Directory::finish_noreplace_transition): SourceOnly is the
// Missing class, Complete proves the pair, Linked syncs, unlinks the
// private alias, syncs again, and proves the final single-link name.
// The three named crash points sit between the durable steps.
func (d *Directory) FinishNoreplaceTransition(source, destination string, expected FileIdentity) error {
	state, err := d.linkState(source, destination, expected)
	if err != nil {
		return err
	}
	switch state {
	case linkStateSourceOnly:
		return nsMissingError()
	case linkStateComplete:
		return d.proveLinkComplete(source, destination, expected)
	}
	if err := d.Sync(); err != nil {
		return err
	}
	fault.Crash("publication.freebsd.after_noreplace_link_sync")
	if err := d.unlinkLinkAlias(source, destination, expected); err != nil {
		return err
	}
	fault.Crash("publication.freebsd.after_noreplace_alias_unlink")
	if err := d.Sync(); err != nil {
		return err
	}
	fault.Crash("publication.freebsd.after_noreplace_alias_sync")
	return d.proveLinkComplete(source, destination, expected)
}

// requireSource proves the source name still names the expected
// identity as one regular single-link file before the linkat (Rust
// Directory::require_source).
func (d *Directory) requireSource(source string, expected FileIdentity) error {
	found, present, err := d.Entry(source)
	if err != nil {
		return err
	}
	if !present {
		return nsMissingError()
	}
	return requireLinkEntry(found, expected, 1)
}

// linkState classifies the observed source/destination pair (Rust
// Directory::link_state): source-only, linked, or complete, with the
// exact link counts 1/2/2/1 on the identities.
func (d *Directory) linkState(source, destination string, expected FileIdentity) (linkState, error) {
	sourceEntry, sourcePresent, err := d.Entry(source)
	if err != nil {
		return 0, err
	}
	destinationEntry, destinationPresent, err := d.Entry(destination)
	if err != nil {
		return 0, err
	}
	switch {
	case sourcePresent && !destinationPresent:
		if err := requireLinkEntry(sourceEntry, expected, 1); err != nil {
			return 0, err
		}
		return linkStateSourceOnly, nil
	case sourcePresent && destinationPresent:
		if err := requireLinkEntry(sourceEntry, expected, 2); err != nil {
			return 0, err
		}
		if err := requireLinkEntry(destinationEntry, expected, 2); err != nil {
			return 0, err
		}
		return linkStateLinked, nil
	case !sourcePresent && destinationPresent:
		if err := requireLinkEntry(destinationEntry, expected, 1); err != nil {
			return 0, err
		}
		return linkStateComplete, nil
	default:
		return 0, nsMissingError()
	}
}

// unlinkLinkAlias removes the private source name only while the pair
// still names the expected identity twice (Rust
// Directory::unlink_link_alias).
func (d *Directory) unlinkLinkAlias(source, destination string, expected FileIdentity) error {
	state, err := d.linkState(source, destination, expected)
	if err != nil {
		return err
	}
	if state != linkStateLinked {
		return nsIdentityChangedError()
	}
	if err := unix.Unlinkat(int(d.file.Fd()), source, 0); err != nil {
		return nsIoError("unlink private publication alias", err)
	}
	return nil
}

// proveLinkComplete verifies the directory and proves the destination
// names the expected identity single-link with the source absent (Rust
// Directory::prove_link_complete).
func (d *Directory) proveLinkComplete(source, destination string, expected FileIdentity) error {
	if err := d.Verify(); err != nil {
		return err
	}
	state, err := d.linkState(source, destination, expected)
	if err != nil {
		return err
	}
	if state != linkStateComplete {
		return nsIdentityChangedError()
	}
	return nil
}

// requireLinkEntry proves one entry is the expected regular identity
// with the exact link count (Rust require_entry).
func requireLinkEntry(entry Entry, expected FileIdentity, links uint64) error {
	if !entry.Regular {
		return nsNotRegularError()
	}
	if entry.Identity != expected {
		return nsIdentityChangedError()
	}
	if entry.Links != links {
		return nsLinkCountError(entry.Links)
	}
	return nil
}

// OpenRegularAnyLink opens one name without following symlinks and
// proves only the regular-file and cross-filesystem rules, accepting
// any link count (Rust Directory::open_regular_any_link, compiled for
// freebsd and test builds; used by the no-replace transition and the
// canonical inspection arms). An absent name reports (nil, nil).
func (d *Directory) OpenRegularAnyLink(name string, writable bool) (*RegularFile, error) {
	return d.openRegularWithLinks(name, writable, false, "open retained transition file")
}
