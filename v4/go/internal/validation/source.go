package validation

// Validation source (Rust validation/source.rs ImmutableSource): the
// database main is opened read-only without following symlinks, the
// shared lifetime lock is taken, the path identity and sidecar
// absence are proved under the lock, and the source is re-verified
// after the sweep. The live sources live in live.go (the LiveCurrent
// arm composes the internal/live registration machine); the
// retained-source custody proof (require_main_available) is a
// platform seam: the Windows arm proves the exact GC envelope
// absence, every other platform keeps the recorded no-op.

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// ImmutableSource is the read-only locked database main of one
// immutable validation (Rust validation::source ImmutableSource). The
// recovery inspection composes the same authority for its immutable
// arm instead of reimplementing the open.
type ImmutableSource struct {
	file     *os.File
	path     string
	sidecar  string
	identity live.FileIdentity
	locked   bool
}

// OpenImmutableSource opens the database main with the Rust
// ImmutableSource::open ordering: canonical sidecar, pre-open sidecar
// refusal, read-only no-follow open, identity capture, the shared
// lifetime lock, and the post-lock path identity + sidecar presence
// re-check. Every failure combines the unlock error exactly like the
// Rust combine_errors arms.
func OpenImmutableSource(path string, check func() error) (*ImmutableSource, error) {
	clean := filepath.Clean(path)
	sidecar, err := live.CanonicalSidecarPath(clean)
	if err != nil {
		return nil, err
	}
	if err := live.RequireSidecarAbsent(sidecar); err != nil {
		return nil, err
	}
	file, err := openReadOnlyNoFollow(clean)
	if err != nil {
		var fe *format.Error
		if errors.As(err, &fe) {
			return nil, fe
		}
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
	source := &ImmutableSource{file: file, path: clean, sidecar: sidecar, identity: identity, locked: true}
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
	if err := source.Verify(); err != nil {
		unlockErr := source.unlock()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	return source, nil
}

// Verify re-proves the path identity and the sidecar absence (Rust
// ImmutableSource::verify).
func (s *ImmutableSource) Verify() error {
	if err := live.VerifyPathAnyLink(s.path, s.identity); err != nil {
		return err
	}
	return live.RequireSidecarAbsent(s.sidecar)
}

// PublicIdentity returns the portable local identity (Rust
// ImmutableSource::public_identity over the publication namespace
// encoding).
func (s *ImmutableSource) PublicIdentity() LocalFileIdentity {
	device, inode := live.IdentityDeviceInode(&s.identity)
	return publicationIdentity(device, inode)
}

// FileHandle returns the held descriptor for the validation mapping
// and the recovery classification (Rust ImmutableSource::file).
func (s *ImmutableSource) FileHandle() *os.File { return s.file }

// RequireAvailable verifies the retained source is still available for
// the database identity (Rust live_cleanup::require_main_available:
// the Windows arm proves the exact GC envelope absence, every other
// platform keeps the recorded no-op).
func (s *ImmutableSource) RequireAvailable(databaseID [16]byte) error {
	return live.RequireMainAvailable(s.path, s.identity, databaseID)
}

// Close releases the lifetime lock and the descriptor (Rust drops the
// ImmutableSource; the lock release runs before the fd close, and the
// unlock error folds like the Rust combine_errors arms).
func (s *ImmutableSource) Close() error {
	unlockErr := s.unlock()
	closeErr := s.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func (s *ImmutableSource) unlock() error {
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
