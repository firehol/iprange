package recovery

// Recovery source guard (Rust recovery/source_guard.rs): the exact
// source-generation protection of one recovery operation. The
// immutable and quiescent (offline) arms open the database main under
// the lifetime lock, bind the exact recovery candidate or the proven
// current generation, and release on the terminal; the live arm (the
// registered reader-table machine) arrives with the recover_live api
// slice. A failed release retains the source inside the
// RecoverySourceCleanupGuard for an exact retry.

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// sourceMode selects the coordination binding of a recovery source
// (Rust SourceMode).
type sourceMode uint8

const (
	sourceModeImmutable sourceMode = iota
	sourceModeOffline
	sourceModeLive
)

// currentSourceMode selects the current-generation binding of a
// recovery source (Rust CurrentSourceMode).
type currentSourceMode uint8

const (
	currentSourceModeImmutable currentSourceMode = iota
	currentSourceModeLive
)

// recoverySource is one opened recovery source (Rust Source union:
// the basic arm now, the registered live arm with recover_live).
type recoverySource struct {
	basic *basicSource
	live  any
}

// basicSource is the immutable or quiescent recovery source (Rust
// BasicSource): the locked main descriptor, the mapped committed
// extent, the exact selection, and the retained generation.
type basicSource struct {
	file           *os.File
	mapping        *mapping.Mapping
	path           string
	sidecar        string
	hasSidecar     bool
	identity       live.FileIdentity
	selection      basicSelection
	meta           format.Meta
	lifetimeLocked bool
}

// basicSelection is the exact binding of one basic source (Rust
// BasicSelection): a recovery candidate token or the proven current
// generation.
type basicSelection struct {
	candidate *RecoveryCandidate
	current   bool
}

// sourceEnd is one source terminal (Rust SourceEnd): the primary
// cause and the cleanup guard when the release failed.
type sourceEnd struct {
	cause error
	guard *RecoverySourceCleanupGuard
}

// sourceOpenFailure is one failed source open (Rust
// SourceOpenFailure).
type sourceOpenFailure struct {
	cause error
	guard *RecoverySourceCleanupGuard
}

// openRecoverySource opens one recovery source for an exact candidate
// (Rust Source::open; the live arm arrives with the recover_live api
// slice and refuses honestly before that).
func openRecoverySource(path string, candidate *RecoveryCandidate, mode sourceMode, check func() error) (*recoverySource, *sourceOpenFailure) {
	if err := live.Checkpoint(check); err != nil {
		return nil, openProblem(err)
	}
	switch mode {
	case sourceModeImmutable:
		basic, err := openBasicSource(path, candidate, true, check)
		if err != nil {
			return nil, openProblem(err)
		}
		return &recoverySource{basic: basic}, nil
	case sourceModeOffline:
		basic, err := openBasicSource(path, candidate, false, check)
		if err != nil {
			return nil, openProblem(err)
		}
		return &recoverySource{basic: basic}, nil
	case sourceModeLive:
		return nil, openProblem(&format.Error{Code: format.CodePublicationUnsupported, Detail: "live recovery source arrives with the recover_live machine"})
	default:
		return nil, openProblem(&format.Error{Code: format.CodeInvalidEnum, Detail: "invalid recovery source mode"})
	}
}

// openRecoverySourceCurrent opens one recovery source for the proven
// current generation (Rust Source::open_current; the live arm arrives
// with the recover_live api slice).
func openRecoverySourceCurrent(path string, mode currentSourceMode, check func() error) (*recoverySource, *sourceOpenFailure) {
	if err := live.Checkpoint(check); err != nil {
		return nil, openProblem(err)
	}
	switch mode {
	case currentSourceModeImmutable:
		basic, err := openBasicSourceCurrent(path, check)
		if err != nil {
			return nil, openProblem(err)
		}
		return &recoverySource{basic: basic}, nil
	case currentSourceModeLive:
		return nil, openProblem(&format.Error{Code: format.CodePublicationUnsupported, Detail: "live recovery source arrives with the recover_live machine"})
	default:
		return nil, openProblem(&format.Error{Code: format.CodeInvalidEnum, Detail: "invalid recovery source mode"})
	}
}

// mapping returns the mapped committed extent of the source.
func (s *recoverySource) mapping() *mapping.Mapping {
	return s.basic.mapping
}

// meta returns the retained generation of the source.
func (s *recoverySource) meta() format.Meta {
	return s.basic.meta
}

// identity returns the portable identity of the source.
func (s *recoverySource) identity() publication.LocalFileIdentity {
	device, inode := live.IdentityDeviceInode(&s.basic.identity)
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}

// finishCurrent runs the terminal over the retained generation (Rust
// Source::finish_current).
func (s *recoverySource) finishCurrent(check func() error) sourceEnd {
	return s.finish(s.meta(), check)
}

// finish runs the checked terminal (Rust Source::finish): the final
// candidate proof, then the release; a failed release retains the
// source in the cleanup guard.
func (s *recoverySource) finish(used format.Meta, check func() error) sourceEnd {
	checked := s.finalCheck(used, check)
	released := s.release()
	return terminal(s, checked, released)
}

// abandon runs the failing terminal (Rust Source::abandon).
func (s *recoverySource) abandon(cause error) sourceEnd {
	released := s.release()
	return terminal(s, cause, released)
}

// releaseOnly runs the release terminal without any final proof (Rust
// Source::release_only).
func (s *recoverySource) releaseOnly() sourceEnd {
	released := s.release()
	return terminal(s, nil, released)
}

// finalCheck re-proves the source generation after the operation (Rust
// Source::final_check: the retained generation is unchanged and the
// exact selection still binds it; the bind errors propagate as-is).
func (s *recoverySource) finalCheck(used format.Meta, check func() error) error {
	basic := s.basic
	if basic.meta != used {
		return candidateChangedError()
	}
	var selected format.Meta
	var err error
	if basic.selection.current {
		selected, err = bindCurrent(basic, check)
	} else {
		selected, err = bindCandidate(basic, basic.selection.candidate, check)
	}
	if err != nil {
		return err
	}
	if selected != used {
		return candidateChangedError()
	}
	return nil
}

// release releases the lifetime lock of the source (Rust
// Source::release).
func (s *recoverySource) release() error {
	return s.basic.release()
}

// terminal folds the final proof and the release (Rust terminal): a
// failed release retains the source in the cleanup guard with the
// fixed recovery problem.
func terminal(source *recoverySource, checked error, released error) sourceEnd {
	if released == nil {
		return sourceEnd{cause: checked, guard: nil}
	}
	cause := checked
	if cause == nil {
		cause = cleanupForCause(released)
	}
	return sourceEnd{
		cause: cause,
		guard: &RecoverySourceCleanupGuard{
			source:      &guardSource{kind: guardSourceRecovery, recovery: source},
			lastProblem: problem(released),
		},
	}
}

// openProblem builds the plain open failure (Rust open_problem).
func openProblem(cause error) *sourceOpenFailure {
	return &sourceOpenFailure{cause: cause, guard: nil}
}

// cleanupForCause maps one failed release to the terminal cause (Rust
// cleanup_for_cause: ForkedHandle keeps its class, every other cause
// is the cleanup-conflict class).
func cleanupForCause(cause error) error {
	if errors.Is(cause, &format.Error{Code: format.CodeForkedHandle}) {
		return cause
	}
	return &format.Error{Code: format.CodeCleanupConflict, Detail: "source recovery protection was not released"}
}

// candidateChangedError is the fixed candidate-changed class (Rust
// candidate_changed).
func candidateChangedError() error {
	return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "recovery candidate changed"}
}

// problem builds the fixed recovery problem of one failed operation
// (Rust source_guard::problem over PublicationProblem::new; Go does
// not carry the os_code limb).
func problem(cause error) error {
	return &format.Error{Code: problemCode(cause), Detail: "recovery source operation failed"}
}

// openBasicSource opens the immutable or quiescent source for one
// candidate (Rust BasicSource::open: sidecar refusal for the
// immutable arm, the no-follow open, the identity capture, the
// lifetime lock, the candidate bind, and the mapped committed
// extent).
func openBasicSource(path string, candidate *RecoveryCandidate, immutable bool, check func() error) (*basicSource, error) {
	sidecar, hasSidecar, err := sidecarPath(path, immutable)
	if err != nil {
		return nil, err
	}
	if hasSidecar {
		if err := live.RequireSidecarAbsent(sidecar); err != nil {
			return nil, err
		}
	}
	file, err := openSourceFile(path, immutable)
	if err != nil {
		return nil, err
	}
	identity, err := live.IdentityAnyLink(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	mode := live.LockExclusive
	if immutable {
		mode = live.LockShared
	}
	if err := live.LockFileCancellable(file, live.MainLifetimeOffset, mode, check); err != nil {
		file.Close()
		return nil, err
	}
	return finishBasicOpen(file, path, sidecar, hasSidecar, identity, candidate, check)
}

// openBasicSourceCurrent opens the immutable current-generation source
// (Rust BasicSource::open_current: sidecar refused, read-only open,
// shared lock, the proven-current bind, and the mapped extent).
func openBasicSourceCurrent(path string, check func() error) (*basicSource, error) {
	sidecar, err := live.CanonicalSidecarPath(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	if err := live.RequireSidecarAbsent(sidecar); err != nil {
		return nil, err
	}
	file, err := openSourceFile(path, true)
	if err != nil {
		return nil, err
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
	source := &basicSource{file: file, path: filepath.Clean(path), sidecar: sidecar, hasSidecar: true, identity: identity, lifetimeLocked: true}
	meta, err := bindCurrent(source, check)
	if err != nil {
		unlockErr := source.release()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	source.meta = meta
	source.selection = basicSelection{current: true}
	m, err := mapAvailable(file, meta)
	if err != nil {
		unlockErr := source.release()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	source.mapping = m
	return source, nil
}

// sidecarPath derives the sidecar path of one basic source (Rust
// sidecar_path: only the immutable arm refuses a sidecar).
func sidecarPath(path string, immutable bool) (string, bool, error) {
	if !immutable {
		return "", false, nil
	}
	sidecar, err := live.CanonicalSidecarPath(filepath.Clean(path))
	if err != nil {
		return "", false, err
	}
	return sidecar, true, nil
}

// openSourceFile opens the main without following symlinks (Rust
// open_file: read-only for the immutable arm, read-write for the
// quiescent arm).
func openSourceFile(path string, immutable bool) (*os.File, error) {
	flags := os.O_RDWR
	if immutable {
		flags = os.O_RDONLY
	}
	file, err := os.OpenFile(filepath.Clean(path), flags|unixO_NOFOLLOW, 0)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	return file, nil
}

// finishBasicOpen binds and maps one opened basic source (Rust
// finish_open).
func finishBasicOpen(file *os.File, path, sidecar string, hasSidecar bool, identity live.FileIdentity, candidate *RecoveryCandidate, check func() error) (*basicSource, error) {
	source := &basicSource{file: file, path: filepath.Clean(path), sidecar: sidecar, hasSidecar: hasSidecar, identity: identity, lifetimeLocked: true}
	meta, err := bindCandidate(source, candidate, check)
	if err != nil {
		unlockErr := source.release()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	source.meta = meta
	source.selection = basicSelection{candidate: candidate}
	m, err := mapAvailable(file, meta)
	if err != nil {
		unlockErr := source.release()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	source.mapping = m
	return source, nil
}

// bindCandidate proves the candidate selection (Rust bind over
// verify_path + select + require_main_available + verify_path; the
// path proofs map to the candidate-changed class, the classification
// propagates its own errors).
func bindCandidate(source *basicSource, candidate *RecoveryCandidate, check func() error) (format.Meta, error) {
	if err := verifyBindPath(source); err != nil {
		return format.Meta{}, err
	}
	meta, err := selectCandidate(source, candidate, check)
	if err != nil {
		return format.Meta{}, err
	}
	// require_main_available is the recorded POSIX no-op.
	if err := verifyBindPath(source); err != nil {
		return format.Meta{}, err
	}
	return meta, nil
}

// bindCurrent proves the current-generation selection (Rust
// bind_current over verify_path + the immutable bootstrap +
// require_main_available + verify_path; the bootstrap and checkpoint
// errors propagate as-is).
func bindCurrent(source *basicSource, check func() error) (format.Meta, error) {
	if err := verifyBindPath(source); err != nil {
		return format.Meta{}, err
	}
	if err := live.Checkpoint(check); err != nil {
		return format.Meta{}, err
	}
	meta, err := bootstrapCurrent(source.file)
	if err != nil {
		return format.Meta{}, err
	}
	// require_main_available is the recorded POSIX no-op.
	if err := verifyBindPath(source); err != nil {
		return format.Meta{}, err
	}
	return meta, nil
}

// verifyBindPath re-proves the path identity and the sidecar absence
// (Rust verify_path with the candidate-changed mapping).
func verifyBindPath(source *basicSource) error {
	if err := live.VerifyPathAnyLink(source.path, source.identity); err != nil {
		return candidateChangedError()
	}
	if source.hasSidecar {
		if err := live.RequireSidecarAbsent(source.sidecar); err != nil {
			return candidateChangedError()
		}
	}
	return nil
}

// selectCandidate classifies the pair and selects the exact candidate
// (Rust select: the token identity and the selection refusal are the
// candidate-changed class, the classification propagates its errors).
func selectCandidate(source *basicSource, candidate *RecoveryCandidate, check func() error) (format.Meta, error) {
	if candidate == nil || source.identityPublication() != candidate.SourceIdentity {
		return format.Meta{}, candidateChangedError()
	}
	classified, err := readClassified(source.file, check)
	if err != nil {
		return format.Meta{}, err
	}
	meta, ok := classified.selectedMeta(candidate)
	if !ok {
		return format.Meta{}, candidateChangedError()
	}
	return meta, nil
}

// bootstrapCurrent bootstraps the proven current generation of the
// main (Rust database_file::bootstrap_file with the immutable reader
// mode: the geometry is proved before any mapping exists, exactly
// like the validation bootstrap).
func bootstrapCurrent(file *os.File) (format.Meta, error) {
	stat, err := file.Stat()
	if err != nil {
		return format.Meta{}, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	physical := uint64(stat.Size())
	if physical < 2*format.PageSize {
		return format.Meta{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "file smaller than two pages"}
	}
	if physical%format.PageSize != 0 {
		return format.Meta{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "file size not page-aligned"}
	}
	m, err := mapping.MapFile(file, 2*format.PageSize, false)
	if err != nil {
		return format.Meta{}, err
	}
	defer func() { _ = m.Close() }()
	p0, err := m.Page(0)
	if err != nil {
		return format.Meta{}, err
	}
	p1, err := m.Page(1)
	if err != nil {
		return format.Meta{}, err
	}
	res, err := bootstrap.Open(p0, p1, physical, bootstrap.ModeImmutableReader)
	if err != nil {
		return format.Meta{}, err
	}
	return res.Meta, nil
}

// mapAvailable maps the committed extent read-only (Rust
// map_available: the declared generation length, bounded by the
// available physical extent).
func mapAvailable(file *os.File, meta format.Meta) (*mapping.Mapping, error) {
	if meta.PageCount > ^uint64(0)/format.PageSize {
		return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery source mapping length"}
	}
	declared := meta.PageCount * format.PageSize
	available := declared
	if stat, err := file.Stat(); err == nil && uint64(stat.Size()) < available {
		available = uint64(stat.Size())
	}
	return mapping.MapFile(file, available, false)
}

// identityPublication is the portable identity of the retained source.
func (s *basicSource) identityPublication() publication.LocalFileIdentity {
	device, inode := live.IdentityDeviceInode(&s.identity)
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}

// release releases the lifetime lock (Rust BasicSource::release).
func (s *basicSource) release() error {
	if s.lifetimeLocked {
		s.lifetimeLocked = false
		return live.UnlockFile(s.file, live.MainLifetimeOffset)
	}
	return nil
}
