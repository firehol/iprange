// Shared ownership rules for private publication artifacts (Rust
// publication/maintenance/common.rs): the exact-pattern scan in
// constant memory, the stable-entry proof, the owned-open with the
// artifact lock, the unix retirement with the retained link-count
// proof, and the artifact-specific problem classes.

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// maintenanceArtifact is one private artifact family (Rust
// maintenance::common::Artifact): the name prefix and the fixed
// problem details of the family.
type maintenanceArtifact struct {
	prefix              string
	invalidName         string
	unsupportedIdentity string
	invalidIdentity     string
	ownershipMismatch   string
	ownershipChanged    string
	lostName            string
	remainedLinked      string
}

// encodeName builds one exact private artifact name (Rust
// Artifact::encode_name: the zero attempt id is the fixed
// InvalidArgument class).
func (a *maintenanceArtifact) encodeName(attempt [16]byte) (string, error) {
	if attempt == [16]byte{} {
		return "", problem(format.CodeInvalidArgument, "publication attempt id must be nonzero")
	}
	name, err := privateName(a.prefix, attempt)
	if err != nil {
		return "", a.namespaceError(err)
	}
	return name, nil
}

// decodeName decodes one scanned name back to its attempt identity
// (Rust Artifact::decode_name; anything that is not the exact
// pattern is skipped).
func (a *maintenanceArtifact) decodeName(bytes []byte) ([16]byte, bool) {
	return privateAttempt(a.prefix, bytes)
}

// scan visits every stable exact-pattern name of one directory in
// constant memory (Rust Artifact::scan): the cancellation token
// gates the open and every entry, the count uses the checked-add
// overflow class, namespace stream failures map through
// namespaceError, and visitor failures pass through unchanged.
func (a *maintenanceArtifact) scan(path string, check func() error, overflow string, visit func(dir *live.Directory, directoryIdentity LocalFileIdentity, bytes []byte, attempt [16]byte) (bool, error)) (LocalFileIdentity, uint64, error) {
	if err := live.Checkpoint(check); err != nil {
		return LocalFileIdentity{}, 0, sdkProblem(err)
	}
	dir, err := live.OpenDirectory(path)
	if err != nil {
		return LocalFileIdentity{}, 0, a.namespaceError(err)
	}
	defer dir.Close()
	directoryIdentity := maintenanceDirectoryIdentity(dir)
	var entries uint64
	scanErr := dir.Scan(func(bytes []byte) error {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		attempt, ok := a.decodeName(bytes)
		if !ok {
			return nil
		}
		counted, err := visit(dir, directoryIdentity, bytes, attempt)
		if err != nil {
			return err
		}
		if counted {
			if entries == ^uint64(0) {
				return problem(format.CodeArithmeticOverflow, overflow)
			}
			entries++
		}
		return nil
	})
	if scanErr != nil {
		if _, ok := live.AsNamespaceError(scanErr); ok {
			return LocalFileIdentity{}, 0, a.namespaceError(scanErr)
		}
		return LocalFileIdentity{}, 0, scanErr
	}
	return directoryIdentity, entries, nil
}

// inspectStable inspects one exact-pattern name only when it is a
// single-link regular file both before and after the inspection, and
// returns the retained identity with the value (Rust
// Artifact::inspect_stable). The opened regular closes on every
// path that does not transfer it; here no path transfers it.
func inspectStable[T any](a *maintenanceArtifact, dir *live.Directory, bytes []byte, inspect func(file *os.File, identity live.FileIdentity) (T, error)) (identity live.FileIdentity, value T, ok bool, err error) {
	name := string(bytes)
	found, present, entryErr := dir.Entry(name)
	if entryErr != nil {
		return identity, value, false, a.namespaceError(entryErr)
	}
	if !present || !found.Regular || found.Links != 1 {
		return identity, value, false, nil
	}
	regular, regularErr := dir.OpenRegular(name, false)
	if regularErr != nil {
		return identity, value, false, a.namespaceError(regularErr)
	}
	if regular == nil {
		return identity, value, false, nil
	}
	defer regular.File.Close()
	value, err = inspect(regular.File, regular.Identity)
	if err != nil {
		return identity, value, false, err
	}
	current, present, entryErr := dir.Entry(name)
	if entryErr != nil {
		return identity, value, false, a.namespaceError(entryErr)
	}
	if !present || !current.Regular || current.Links != 1 || current.Identity != regular.Identity {
		return identity, value, false, nil
	}
	return regular.Identity, value, true, nil
}

// identity converts one portable identity to the retained pair with
// the artifact-specific invalid classes (Rust Artifact::identity).
func (a *maintenanceArtifact) identity(value LocalFileIdentity) (live.FileIdentity, error) {
	if value.Kind != identityKind {
		return live.FileIdentity{}, problem(format.CodeInvalidArgument, a.unsupportedIdentity)
	}
	device, inode, ok := value.deviceInode()
	if !ok {
		return live.FileIdentity{}, problem(format.CodeInvalidArgument, a.invalidIdentity)
	}
	return live.IdentityFromDeviceInode(device, inode), nil
}

// requireOwned proves one entry is the exact single-link expected
// inode (Rust Artifact::require_owned).
func (a *maintenanceArtifact) requireOwned(entry live.Entry, expected live.FileIdentity) error {
	if !entry.Regular || entry.Links != 1 || entry.Identity != expected {
		return problem(format.CodeCleanupConflict, a.ownershipMismatch)
	}
	return nil
}

// openOwned opens one exact artifact under its single-link proof and
// artifact lock (Rust Artifact::open_owned: an absent name is the
// durable absence, a lost exact name is the ownership class, and the
// post-lock verify is the ownership-changed class).
func (a *maintenanceArtifact) openOwned(dir *live.Directory, name string, found live.Entry, present bool, expected live.FileIdentity, lockOffset uint64, check func() error) (*live.RegularFile, error) {
	if !present {
		if err := a.durableAbsence(dir, name); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := a.requireOwned(found, expected); err != nil {
		return nil, err
	}
	regular, err := dir.OpenRegular(name, true)
	if err != nil {
		return nil, a.namespaceError(err)
	}
	if regular == nil {
		return nil, problem(format.CodeCleanupConflict, a.lostName)
	}
	if err := live.LockFileCancellable(regular.File, lockOffset, live.LockExclusive, check); err != nil {
		_ = regular.File.Close()
		return nil, err
	}
	if err := dir.VerifyName(name, expected); err != nil {
		_ = regular.File.Close()
		return nil, a.cleanupError(err)
	}
	return regular, nil
}

// durableAbsence proves one exact name durably absent (Rust
// Artifact::durable_absence: sync, verify, then the exact absence
// proof under the cleanup classes).
func (a *maintenanceArtifact) durableAbsence(dir *live.Directory, name string) error {
	if err := dir.Sync(); err != nil {
		return a.namespaceError(err)
	}
	if err := dir.Verify(); err != nil {
		return a.namespaceError(err)
	}
	if err := dir.RequireAbsent(name); err != nil {
		return a.cleanupError(err)
	}
	return nil
}

// remove runs one exact artifact removal (Rust Artifact::remove: the
// directory identity proof, the owned open or durable absence, the
// content verification, the cancellation checkpoint, and the unix
// retirement).
func (a *maintenanceArtifact) remove(path string, expectedDirectory LocalFileIdentity, attempt [16]byte, expectedArtifact LocalFileIdentity, lockOffset uint64, check func() error, verifyContent func(file *os.File, identity live.FileIdentity) error) (AbandonedArtifactRemoval, error) {
	expectedDirectoryIdentity, err := a.identity(expectedDirectory)
	if err != nil {
		return AbandonedArtifactRemoval{}, err
	}
	expectedArtifactIdentity, err := a.identity(expectedArtifact)
	if err != nil {
		return AbandonedArtifactRemoval{}, err
	}
	dir, err := live.OpenDirectory(path)
	if err != nil {
		return AbandonedArtifactRemoval{}, a.namespaceError(err)
	}
	defer dir.Close()
	if dir.Identity() != expectedDirectoryIdentity {
		return AbandonedArtifactRemoval{}, directoryIdentityMismatchProblem()
	}
	name, err := a.encodeName(attempt)
	if err != nil {
		return AbandonedArtifactRemoval{}, err
	}
	found, present, err := dir.Entry(name)
	if err != nil {
		return AbandonedArtifactRemoval{}, a.namespaceError(err)
	}
	regular, err := a.openOwned(dir, name, found, present, expectedArtifactIdentity, lockOffset, check)
	if err != nil {
		return AbandonedArtifactRemoval{}, err
	}
	if regular == nil {
		return removalResult(false, nil), nil
	}
	defer regular.File.Close()
	if err := verifyContent(regular.File, expectedArtifactIdentity); err != nil {
		return AbandonedArtifactRemoval{}, err
	}
	if err := live.Checkpoint(check); err != nil {
		return AbandonedArtifactRemoval{}, err
	}
	return a.retire(dir, name, regular, expectedArtifactIdentity)
}

// retire unlinks the exact inode and proves the retained link count
// and the durable absence (Rust Artifact::retire_unix: every unlink
// error is the cleanup class, a missing exact name is the lost
// class, a linked inode is the remained-linked cause, and the
// post-removal absence failure folds into the sdk class).
func (a *maintenanceArtifact) retire(dir *live.Directory, name string, regular *live.RegularFile, expected live.FileIdentity) (AbandonedArtifactRemoval, error) {
	unlinked, err := dir.UnlinkExact(name, expected)
	if err != nil {
		return AbandonedArtifactRemoval{}, a.cleanupError(err)
	}
	if !unlinked {
		return AbandonedArtifactRemoval{}, problem(format.CodeCleanupConflict, a.lostName)
	}
	count, err := live.RegularLinkCount(regular.File)
	var cause error
	switch {
	case err != nil:
		cause = namespaceProblem(err)
	case count != 0:
		cause = problem(format.CodeCleanupConflict, a.remainedLinked)
	}
	if cause != nil {
		return removalResult(true, cause), nil
	}
	if err := a.durableAbsence(dir, name); err != nil {
		return removalResult(true, sdkProblem(err)), nil
	}
	return removalResult(true, nil), nil
}

// namespaceError maps one namespace failure of the scan/remove
// surface (Rust Artifact::namespace_error: the artifact-specific
// invalid-name class, raw IO, the fixed unsupported class for
// directories without the local operations, and the cleanup class
// for anything else).
func (a *maintenanceArtifact) namespaceError(err error) *format.Error {
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		return sdkProblem(err)
	}
	switch nerr.Kind {
	case live.NamespaceInvalidName:
		return problem(format.CodeInvalidArgument, a.invalidName)
	case live.NamespaceForkedHandle:
		return problem(format.CodeForkedHandle, "publication handle crossed fork")
	case live.NamespaceIo, live.NamespaceIoAt:
		return problem(format.CodeIO, "publication filesystem operation failed")
	case live.NamespaceUnsupported, live.NamespaceCrossFilesystem:
		return problem(format.CodeOSUnsupported, "publication directory lacks required local operations")
	default:
		return problem(format.CodeCleanupConflict, "publication directory entry changed")
	}
}

// cleanupError maps one namespace failure of an exact-name mutation
// (Rust Artifact::cleanup_error: raw IO and the fork class pass
// through, everything else is the ownership-changed class).
func (a *maintenanceArtifact) cleanupError(err error) *format.Error {
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		return sdkProblem(err)
	}
	switch nerr.Kind {
	case live.NamespaceIo, live.NamespaceIoAt:
		return problem(format.CodeIO, "publication filesystem operation failed")
	case live.NamespaceForkedHandle:
		return problem(format.CodeForkedHandle, "publication handle crossed fork")
	default:
		return problem(format.CodeCleanupConflict, a.ownershipChanged)
	}
}

// maintenanceDirectoryIdentity is the portable identity of one
// scanned directory (Rust namespace::local_identity over
// Directory::identity).
func maintenanceDirectoryIdentity(dir *live.Directory) LocalFileIdentity {
	identity := dir.Identity()
	return residueLocalIdentity(&identity)
}

// directoryIdentityMismatchProblem is the fixed directory identity
// class (Rust Error::DirectoryIdentityMismatch).
func directoryIdentityMismatchProblem() *format.Error {
	return problem(format.CodeDirectoryIdentityMismatch, "publication directory identity mismatch")
}

// removalResult builds one factual removal outcome (Rust
// maintenance::removal: the cleanup state derives from the cause).
func removalResult(sourcePresent bool, cause error) AbandonedArtifactRemoval {
	state := CleanupStateClean
	if cause != nil {
		state = CleanupStateResiduePossible
	}
	return AbandonedArtifactRemoval{SourcePresent: sourcePresent, CleanupState: state, Cause: cause}
}
