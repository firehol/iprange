//go:build windows

// Atomic installation of one prepared live sidecar at its canonical
// name on Windows (Rust live_lifecycle/namespace.rs install over the
// retained Directory machine): no-replace first installation and the
// discarding replacement. The rollback-safe atomic exchange is
// unsupported on Windows (no atomic exchange primitive; the live reset
// gate refuses it before reaching this arm, exactly like Rust
// require_exchange_available). Every mutation runs against the
// retained Directory handle and is bracketed by identity proofs
// exactly like the POSIX arm.

package live

import (
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// bindPair binds one retained directory and the two final names of
// private and canonical, which must share one directory (Rust
// live_namespace::bind_pair).
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
	if err := validWindowsNameComponent(canonicalName); err != nil {
		dir.Close()
		return nil, "", "", nsMap(nsInvalidNameError())
	}
	return dir, name, canonicalName, nil
}

// install installs the prepared private sidecar at the canonical name
// with the selected namespace guarantee (Rust live_lifecycle/
// namespace.rs install): the discarding replacement under
// DiscardPrevious, the no-replace rename when no previous coordination
// exists, and the typed Unsupported class for the rollback-safe
// exchange (the reset gate refuses it earlier on Windows).
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
// rename without replacement, re-prove the directory, prove the
// private name absent, and re-verify the canonical identity.
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

// installExchange is unsupported on Windows (Rust
// live_namespace::install_exchange -> windows_mutation exchange ->
// Unsupported): no atomic name exchange exists.
func installExchange(private, canonical string, privateFile *os.File, expectedPrivate, expectedCanonical FileIdentity) error {
	return nsMap(nsUnsupportedError())
}
