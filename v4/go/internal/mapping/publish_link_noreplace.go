//go:build !windows

package mapping

import (
	"errors"
	"runtime"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// linkNoReplace implements the FreeBSD crash-safe no-replace publication
// (Rust publication/namespace_mutation.rs link_noreplace): FreeBSD has no
// atomic no-replace rename, so the destination is linked to the attempt
// inode, the directory is synced, and the private attempt alias is
// unlinked with the identity proved at every step. The machine is
// compiled on every non-Windows target so the exact syscall sequence
// runs in the unit tests on the build host; the FreeBSD build-tagged
// entry point is its only production caller (Rust gates link_noreplace
// to target_os = "freebsd").
//
// expectedDevice/expectedInode is the attempt-file identity captured
// from the builder descriptor at creation (Rust link_noreplace stats the
// open source File: regular_identity_any_link). Every path probe in the
// machine must name that identity or the operation fails with a
// conflict, so a path swap between custody verification and the rename
// cannot redirect the publication.
func linkNoReplace(dir, source, destination string, expectedDevice, expectedInode uint64) error {
	if err := requireLinkSource(source, expectedDevice, expectedInode); err != nil {
		return err
	}
	switch err := unix.Linkat(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, 0); {
	case err == nil:
		// The destination now holds the second link to the attempt inode.
	case errors.Is(err, unix.EEXIST):
		// A destination that already names the same inode is the
		// crash-recovery case (a prior attempt died between the link
		// and the alias unlink) and finishes the transition; every
		// other EEXIST - foreign destination, extra links, identity
		// drift - is the plain Exists refusal (Rust maps any non-Linked
		// link_state outcome under EEXIST to NamespaceError::Exists).
		state, stateErr := linkState(source, destination, expectedDevice, expectedInode)
		if stateErr == nil && state == linkStateLinked {
			return finishNoReplaceTransition(dir, source, destination, expectedDevice, expectedInode)
		}
		return &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"}
	default:
		return &format.Error{Code: format.CodeIO, Detail: "link publication name without replacement"}
	}
	fault.Crash("publication.freebsd.after_noreplace_link")
	return finishNoReplaceTransition(dir, source, destination, expectedDevice, expectedInode)
}

// finishNoReplaceTransition completes or verifies a no-replace
// publication from the current link state (Rust
// finish_noreplace_transition): SourceOnly means the link never
// happened, Complete means the transition already finished, and Linked
// is the live two-link state that needs the directory sync, the
// identity-proved alias unlink, and the final sync before the proof.
// The recovery branches are reachable by crash-resume machinery (Rust
// resolver/residue) and by the crash tests; the exact-link checks make
// foreign or extra links fail closed.
func finishNoReplaceTransition(dir, source, destination string, expectedDevice, expectedInode uint64) error {
	switch state, stateErr := linkState(source, destination, expectedDevice, expectedInode); {
	case stateErr != nil:
		return stateErr
	case state == linkStateSourceOnly:
		return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
	case state == linkStateComplete:
		return proveLinkComplete(destination, expectedDevice, expectedInode)
	}
	// Linked: persist the new destination link, then remove the private
	// alias so only the destination names the output (Rust sync, crash,
	// unlink_link_alias, crash, sync, crash).
	if err := SyncDirectory(dir); err != nil {
		return err
	}
	fault.Crash("publication.freebsd.after_noreplace_link_sync")
	if err := unlinkLinkAlias(source, destination, expectedDevice, expectedInode); err != nil {
		return err
	}
	fault.Crash("publication.freebsd.after_noreplace_alias_unlink")
	if err := SyncDirectory(dir); err != nil {
		return err
	}
	fault.Crash("publication.freebsd.after_noreplace_alias_sync")
	return proveLinkComplete(destination, expectedDevice, expectedInode)
}

// linkMachineState classifies the two publication names (Rust
// LinkState).
type linkMachineState int

const (
	linkStateSourceOnly linkMachineState = iota + 1
	linkStateLinked
	linkStateComplete
)

// requireLinkSource is Rust require_source: the source entry exists, is
// a regular file, has the expected identity, and has exactly one link.
func requireLinkSource(source string, expectedDevice, expectedInode uint64) error {
	entry, err := entryIdentity(source)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
		}
		return err
	}
	return requireEntry(entry, expectedDevice, expectedInode, 1)
}

// linkState is the two-name classification used by every machine step
// (Rust link_state): both names absent is Missing; the source alone is
// SourceOnly; both naming the expected inode with exactly two links each
// is Linked; the destination alone is Complete. Every probed entry must
// be a regular file with the expected identity and the exact link count.
func linkState(source, destination string, expectedDevice, expectedInode uint64) (linkMachineState, error) {
	sourceEntry, sourceErr := entryIdentity(source)
	destinationEntry, destinationErr := entryIdentity(destination)
	sourceAbsent := errors.Is(sourceErr, unix.ENOENT)
	destinationAbsent := errors.Is(destinationErr, unix.ENOENT)
	if sourceErr != nil && !sourceAbsent {
		return 0, sourceErr
	}
	if destinationErr != nil && !destinationAbsent {
		return 0, destinationErr
	}
	switch {
	case !sourceAbsent && destinationAbsent:
		if err := requireEntry(sourceEntry, expectedDevice, expectedInode, 1); err != nil {
			return 0, err
		}
		return linkStateSourceOnly, nil
	case sourceAbsent && !destinationAbsent:
		if err := requireEntry(destinationEntry, expectedDevice, expectedInode, 1); err != nil {
			return 0, err
		}
		return linkStateComplete, nil
	case !sourceAbsent && !destinationAbsent:
		if err := requireEntry(sourceEntry, expectedDevice, expectedInode, 2); err != nil {
			return 0, err
		}
		if err := requireEntry(destinationEntry, expectedDevice, expectedInode, 2); err != nil {
			return 0, err
		}
		return linkStateLinked, nil
	default:
		return 0, &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
	}
}

// unlinkLinkAlias is Rust unlink_link_alias: the exact two-link state
// must still hold, then the private source alias is unlinked.
func unlinkLinkAlias(source, destination string, expectedDevice, expectedInode uint64) error {
	state, err := linkState(source, destination, expectedDevice, expectedInode)
	if err != nil {
		return err
	}
	if state != linkStateLinked {
		return &format.Error{Code: format.CodeConflict, Detail: "publication inode identity changed"}
	}
	if err := Unlink(source); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "unlink private publication alias"}
	}
	return nil
}

// proveLinkComplete is Rust prove_link_complete: only the destination
// may remain, still naming the expected inode with one link.
func proveLinkComplete(destination string, expectedDevice, expectedInode uint64) error {
	entry, err := entryIdentity(destination)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return &format.Error{Code: format.CodeNameNotFound, Detail: "publication name is missing"}
		}
		return err
	}
	if err := requireEntry(entry, expectedDevice, expectedInode, 1); err != nil {
		return &format.Error{Code: format.CodeConflict, Detail: "publication inode identity changed"}
	}
	return nil
}

// linkEntry is one lstat probe of a publication name (Rust
// Directory::entry).
type linkEntry struct {
	device, inode, nlink uint64
	regular              bool
}

// entryIdentity probes one publication name. ENOENT is returned raw so
// callers can classify absence; every other failure is the typed
// problem error (Rust Directory::entry: ENOENT is Ok(None), and any
// other fstatat errno becomes IoAt; the problem boundary classifies
// the nofollow-symlink errno family as the symlink conflict and
// everything else as Io with the retained-name operation detail).
func entryIdentity(path string) (linkEntry, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return linkEntry{}, err
		}
		if isNofollowSymlink(err) {
			return linkEntry{}, &format.Error{Code: format.CodeConflict, Detail: "publication name is a symlink"}
		}
		return linkEntry{}, &format.Error{Code: format.CodeIO, Detail: "inspect retained name"}
	}
	return linkEntry{
		device:  uint64(st.Dev),
		inode:   uint64(st.Ino),
		nlink:   uint64(st.Nlink),
		regular: st.Mode&unix.S_IFMT == unix.S_IFREG,
	}, nil
}

// isNofollowSymlink is the Rust problem-boundary nofollow-symlink
// errno family (publication problem namespace mapping): ELOOP on every
// unix target, and EMLINK on FreeBSD (the historical no-follow errno
// there).
func isNofollowSymlink(err error) bool {
	if errors.Is(err, unix.ELOOP) {
		return true
	}
	return runtime.GOOS == "freebsd" && errors.Is(err, unix.EMLINK)
}

// requireEntry is Rust require_entry: regular, expected identity, exact
// link count.
func requireEntry(entry linkEntry, expectedDevice, expectedInode, links uint64) error {
	if !entry.regular {
		return &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
	}
	if entry.device != expectedDevice || entry.inode != expectedInode {
		return &format.Error{Code: format.CodeConflict, Detail: "publication inode identity changed"}
	}
	if entry.nlink != links {
		return &format.Error{Code: format.CodeConflict, Detail: "publication inode link count changed"}
	}
	return nil
}
