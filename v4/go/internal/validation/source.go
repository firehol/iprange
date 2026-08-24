package validation

// Validation source (Rust validation/source.rs ImmutableSource): the
// database main is opened read-only without following symlinks, the
// shared lifetime lock is taken, the path identity and sidecar
// absence are proved under the lock, and the source is re-verified
// after the sweep. The live sources land with the LiveCurrent slice
// (F); require_main_available is the recorded POSIX no-op.

import (
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// ImmutableSource is the read-only locked database main of one
// immutable validation (Rust validation::source ImmutableSource).
type immutableSource struct {
	file     *os.File
	path     string
	sidecar  string
	identity live.FileIdentity
	locked   bool
}

// openImmutableSource opens the database main with the Rust
// ImmutableSource::open ordering: canonical sidecar, pre-open sidecar
// refusal, read-only no-follow open, identity capture, the shared
// lifetime lock, and the post-lock path identity + sidecar presence
// re-check. Every failure combines the unlock error exactly like the
// Rust combine_errors arms.
func openImmutableSource(path string, check func() error) (*immutableSource, error) {
	clean := filepath.Clean(path)
	sidecar, err := live.CanonicalSidecarPath(clean)
	if err != nil {
		return nil, err
	}
	if err := live.RequireSidecarAbsent(sidecar); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(clean, os.O_RDONLY|unixO_NOFOLLOW, 0)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	identity, err := live.IdentityAnyLink(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := live.LockFileCancellable(file, live.MainLifetimeOffset, live.LockShared, check); err != nil {
		file.Close()
		return nil, err
	}
	source := &immutableSource{file: file, path: clean, sidecar: sidecar, identity: identity, locked: true}
	// The open verifies twice exactly like the Rust open: the inline
	// path+sidecar check combined with the unlock error, then the
	// source verify again before the open returns.
	if err := live.VerifyPathAnyLink(source.path, source.identity); err != nil {
		unlockErr := source.unlock()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	if err := live.RequireSidecarAbsent(source.sidecar); err != nil {
		unlockErr := source.unlock()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	if err := source.verify(); err != nil {
		unlockErr := source.unlock()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	return source, nil
}

// verify re-proves the path identity and the sidecar absence (Rust
// ImmutableSource::verify).
func (s *immutableSource) verify() error {
	if err := live.VerifyPathAnyLink(s.path, s.identity); err != nil {
		return err
	}
	return live.RequireSidecarAbsent(s.sidecar)
}

// publicIdentity returns the portable local identity (Rust
// immutableSource::public_identity over the publication namespace
// encoding).
func (s *immutableSource) publicIdentity() LocalFileIdentity {
	device, inode := live.IdentityDeviceInode(&s.identity)
	return publicationIdentity(device, inode)
}

// file returns the held descriptor for the validation mapping and the
// bootstrap probe.
func (s *immutableSource) fileHandle() *os.File { return s.file }

// close releases the lifetime lock and the descriptor (Rust drops the
// immutableSource; the lock release runs before the fd close, and the
// unlock error folds like the Rust combine_errors arms).
func (s *immutableSource) close() error {
	unlockErr := s.unlock()
	closeErr := s.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func (s *immutableSource) unlock() error {
	if s.locked {
		s.locked = false
		return live.UnlockFile(s.file, live.MainLifetimeOffset)
	}
	return nil
}

// combineErrors joins two failures with the primary first (Rust
// combine_errors: the primary is the As/Is target, the secondary
// appears when the primary is nil).
func combineErrors(primary, secondary error) error {
	if primary != nil {
		return primary
	}
	if secondary != nil {
		return secondary
	}
	return nil
}

// publicationIdentity builds the portable identity of one
// device+inode pair through the publication encoding authority (Rust
// namespace::local_identity).
func publicationIdentity(device, inode uint64) LocalFileIdentity {
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}
