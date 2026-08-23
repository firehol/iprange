//go:build !windows

// Atomic installation of one prepared live sidecar at its canonical
// name (Rust live_lifecycle/namespace.rs install over the retained
// Directory machine): no-replace first installation, discarding
// replacement, and the rollback-safe atomic exchange with its
// verification-failure restore. Every mutation runs against the
// retained Directory descriptor (namespace_install_linux.go /
// _darwin.go / _other.go) and is bracketed by identity proofs exactly
// like Rust live_namespace.rs.

package live

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// bindPair binds one retained directory and the two final names of
// private and canonical, which must share one directory (Rust
// live_namespace::bind_pair: the parents must be equal, both names
// must be valid Name components).
func bindPair(private, canonical string) (*Directory, string, string, error) {
	private = filepath.Clean(private)
	canonical = filepath.Clean(canonical)
	if filepath.Dir(private) != filepath.Dir(canonical) {
		return nil, "", "", &format.Error{Code: format.CodeInvalidArgument, Detail: "live transition names must share one directory"}
	}
	dir, name, err := bindPath(private)
	if err != nil {
		return nil, "", "", nsMap(err)
	}
	canonicalName := filepath.Base(canonical)
	if err := validNameComponent(canonicalName); err != nil {
		dir.Close()
		return nil, "", "", nsMap(nsInvalidNameError())
	}
	return dir, name, canonicalName, nil
}

// install installs the prepared private sidecar at the canonical name
// with the selected namespace guarantee (Rust live_lifecycle/
// namespace.rs install): the rollback-safe atomic exchange when a
// previous sidecar exists under RollbackSafe, the discarding
// replacement under DiscardPrevious, and the no-replace rename when no
// previous coordination exists.
func install(private, canonical string, privateFile *os.File, privateIdentity FileIdentity, previous *FileIdentity, policy LiveResetPolicy) error {
	switch {
	case previous != nil && policy == LiveResetRollbackSafe:
		return installExchange(private, canonical, privateFile, privateIdentity, *previous)
	case previous != nil:
		return installReplaceDiscarding(private, canonical, privateFile, privateIdentity, *previous)
	default:
		return installNoreplace(private, canonical, privateFile, privateIdentity)
	}
}

// installNoreplace renames the prepared private sidecar to the
// canonical name only when the canonical name is absent (Rust
// live_namespace::install_noreplace): verify the private identity,
// rename without replacement, sync the Directory, prove the private
// name absent, and re-verify the canonical identity.
func installNoreplace(private, canonical string, privateFile *os.File, expected FileIdentity) error {
	dir, privateName, canonicalName, err := bindPair(private, canonical)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.VerifyName(privateName, expected); err != nil {
		return nsMap(err)
	}
	if err := dir.RenameNoReplace(privateName, privateFile, canonicalName); err != nil {
		return nsMap(err)
	}
	if err := dir.Sync(); err != nil {
		return nsMap(err)
	}
	if err := dir.RequireAbsent(privateName); err != nil {
		return nsMap(err)
	}
	if err := dir.VerifyName(canonicalName, expected); err != nil {
		return nsMap(err)
	}
	return nil
}

// installReplaceDiscarding renames the prepared private sidecar over
// the canonical name, discarding the previous coordination (Rust
// live_namespace::install_replace_discarding): both identities must
// prove before the plain rename; a changed canonical is the
// CleanupConflict class exactly like Rust.
func installReplaceDiscarding(private, canonical string, privateFile *os.File, expectedPrivate, expectedCanonical FileIdentity) error {
	dir, privateName, canonicalName, err := bindPair(private, canonical)
	if err != nil {
		return err
	}
	defer dir.Close()
	// Both identities prove before the rename; a failure of either is
	// the CleanupConflict class exactly like Rust's folded
	// verify_name().and_then(verify_name).map_err(|_| CleanupConflict).
	if err := dir.VerifyName(privateName, expectedPrivate); err != nil {
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "canonical coordination changed during discarding reset"}
	}
	if err := dir.VerifyName(canonicalName, expectedCanonical); err != nil {
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "canonical coordination changed during discarding reset"}
	}
	if err := dir.RenamePlain(privateName, canonicalName); err != nil {
		return nsMap(err)
	}
	if err := dir.Sync(); err != nil {
		return nsMap(err)
	}
	if err := dir.RequireAbsent(privateName); err != nil {
		return nsMap(err)
	}
	if err := dir.VerifyName(canonicalName, expectedPrivate); err != nil {
		return nsMap(err)
	}
	return nil
}

// installExchange atomically exchanges the prepared private sidecar
// with the canonical name, keeping the previous coordination reachable
// at the private name for rollback (Rust live_namespace::
// install_exchange). Both identities must prove before the exchange;
// after the exchange both swapped names must prove, and a failure
// restores the original layout with the CleanupConflict class (or
// CleanupIncomplete when the restore itself fails).
func installExchange(private, canonical string, privateFile *os.File, expectedPrivate, expectedCanonical FileIdentity) error {
	dir, privateName, canonicalName, err := bindPair(private, canonical)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.VerifyName(privateName, expectedPrivate); err != nil {
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "canonical coordination changed during reset"}
	}
	if err := dir.VerifyName(canonicalName, expectedCanonical); err != nil {
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "canonical coordination changed during reset"}
	}
	if err := dir.RenameExchange(privateName, canonicalName); err != nil {
		return nsMap(err)
	}
	if dir.VerifyName(canonicalName, expectedPrivate) == nil && dir.VerifyName(privateName, expectedCanonical) == nil {
		return nil
	}
	cause := &format.Error{Code: format.CodeCleanupConflict, Detail: "canonical coordination changed during reset"}
	if err := dir.RenameExchange(canonicalName, privateName); err != nil {
		return &format.Error{Code: format.CodeCleanupInProgress, Detail: cause.Error() + "; cleanup also failed: " + nsMap(err).Error()}
	}
	return cause
}

// renameNamespaceResult classifies one rename errno like Rust
// rename_result: the conflict errno maps to the caller's conflict
// namespace class (EEXIST is Exists, ENOENT is Missing), the
// no-primitive family is Unsupported, every other failure is the
// operation's Io class.
func renameNamespaceResult(err error, conflictErrno error, conflict error, operation string) error {
	if err == nil {
		return nil
	}
	switch {
	case conflictErrno != nil && errors.Is(err, conflictErrno):
		return conflict
	case errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
		return nsUnsupportedError()
	default:
		return nsIoError(operation, err)
	}
}
