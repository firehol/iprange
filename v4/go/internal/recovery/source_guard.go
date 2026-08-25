package recovery

// Recovery source guard (Rust recovery/source_guard.rs): the exact
// source-generation protection of one recovery operation. The
// immutable and quiescent (offline) arms open the database main under
// the lifetime lock, bind the exact recovery candidate or the proven
// current generation, and release on the terminal; the live arm
// (source_guard candidate open over the registered reader-table
// machine) binds the newest recovery candidate through the sidecar. A
// failed release retains the source inside the
// RecoverySourceCleanupGuard for an exact retry.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

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
// the basic arm for the immutable and quiescent modes, the registered
// live arm for recover_live).
type recoverySource struct {
	basic *basicSource
	live  *live.LiveSource
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
// (Rust Source::open).
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
		// Rust LiveSource::open: only the newest candidate names the
		// live current generation; every other label refuses before
		// any path access.
		if candidate == nil || candidate.Label != CandidateNewest {
			return nil, openProblem(&format.Error{Code: format.CodeInvalidArgument, Detail: "live recovery requires the newest candidate"})
		}
		token, ok := candidateLiveToken(candidate)
		if !ok {
			return nil, openProblem(candidateChangedError())
		}
		source, err := live.OpenLiveSourceCandidate(path, token, check)
		if err != nil {
			return nil, openProblemLive(err)
		}
		// Rust source_guard/live.rs:298 claim_prepared applies the
		// worker session unreadable-page list to the claimed live
		// mapping before the source returns; the Go live open lives in
		// internal/live (which cannot import the worker session
		// state), so recovery applies the list on the returned borrow
		// before any recovery scan probes it.
		if err := source.Mapping().SetUnreadablePages(mapping.SessionUnreadablePages()); err != nil {
			end := source.Abandon(err)
			return nil, openProblemLive(combineErrors(err, end.Cause))
		}
		return &recoverySource{live: source}, nil
	default:
		return nil, openProblem(&format.Error{Code: format.CodeInvalidEnum, Detail: "invalid recovery source mode"})
	}
}

// openRecoverySourceCurrent opens one recovery source for the proven
// current generation (Rust Source::open_current; the live current arm
// remains refused until its consumer appears).
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
		return nil, openProblem(&format.Error{Code: format.CodePublicationUnsupported, Detail: "live current recovery source is not composed yet"})
	default:
		return nil, openProblem(&format.Error{Code: format.CodeInvalidEnum, Detail: "invalid recovery source mode"})
	}
}

// mapping returns the mapped committed extent of the source.
func (s *recoverySource) mapping() *mapping.Mapping {
	if s.live != nil {
		return s.live.Mapping()
	}
	return s.basic.mapping
}

// meta returns the retained generation of the source.
func (s *recoverySource) meta() format.Meta {
	if s.live != nil {
		return s.live.Meta()
	}
	return s.basic.meta
}

// identity returns the portable identity of the source.
func (s *recoverySource) identity() publication.LocalFileIdentity {
	if s.live != nil {
		device, inode, _ := s.live.FileIdentity()
		return publication.LocalFileIdentityFromDeviceInode(device, inode)
	}
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
	if s.live != nil {
		return liveEnd(s.live.FinishCandidate(used, check), s)
	}
	checked := s.finalCheck(used, check)
	released := s.release()
	return terminal(s, checked, released)
}

// abandon runs the failing terminal (Rust Source::abandon).
func (s *recoverySource) abandon(cause error) sourceEnd {
	if s.live != nil {
		return liveEnd(s.live.Abandon(cause), s)
	}
	released := s.release()
	return terminal(s, cause, released)
}

// releaseOnly runs the release terminal without any final proof (Rust
// Source::release_only).
func (s *recoverySource) releaseOnly() sourceEnd {
	if s.live != nil {
		return liveEnd(s.live.ReleaseOnly(), s)
	}
	released := s.release()
	return terminal(s, nil, released)
}

// close releases the retained source exactly where the Rust machine
// drops it (RAII): the cleanup guard retry and the success terminal
// both call it after the release. The baseline call sites are
// terminal() and RetryCleanup; a discarded guard closes through its
// finalizer (the Go analog of the Rust guard drop).
func (s *recoverySource) close() {
	if s.basic != nil {
		s.basic.close()
	}
}

// liveEnd folds one live source terminal into the recovery terminal
// (Rust terminal over the LiveSource arm: the release failure lives
// in the retained cleanup guard, never in the folded cause).
func liveEnd(end live.LiveSourceEnd, s *recoverySource) sourceEnd {
	if !end.Residue {
		return sourceEnd{cause: end.Cause, guard: nil}
	}
	return sourceEnd{
		cause: end.Cause,
		guard: &RecoverySourceCleanupGuard{
			source:      &guardSource{kind: guardSourceRecovery, recovery: s},
			lastProblem: problem(end.Cause),
		},
	}
}

// candidateLiveToken converts one recovery candidate to the live
// selection token (Rust LiveSource::open receives the recovery token
// directly; the Go token crosses the live boundary without the label
// and portable-identity limbs, which the recovery token keeps).
func candidateLiveToken(candidate *RecoveryCandidate) (bootstrap.RecoveryCandidateToken, bool) {
	device, inode, ok := candidate.SourceIdentity.DeviceInode()
	if !ok {
		return bootstrap.RecoveryCandidateToken{}, false
	}
	return bootstrap.RecoveryCandidateToken{
		MetaPage:      candidate.MetaPage,
		Device:        device,
		Inode:         inode,
		DatabaseID:    candidate.DatabaseID,
		TransactionID: candidate.TransactionID,
		CommitNonce:   candidate.CommitNonce,
	}, true
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

// release releases the source registration and lifetime (Rust
// Source::release; the cleanup-guard retry seam).
func (s *recoverySource) release() error {
	if s.live != nil {
		return s.live.Release()
	}
	return s.basic.release()
}

// terminal folds the final proof and the release (Rust terminal): a
// failed release retains the source in the cleanup guard with the
// fixed recovery problem.
func terminal(source *recoverySource, checked error, released error) sourceEnd {
	if released == nil {
		// Rust drops the source at the terminal; the Go peer closes
		// the mapping and the descriptor here (the Windows deletion
		// rule makes a retained mapped view observable).
		source.close()
		return sourceEnd{cause: checked, guard: nil}
	}
	cause := checked
	if cause == nil {
		cause = cleanupForCause(released)
	}
	guard := &RecoverySourceCleanupGuard{
		source:      &guardSource{kind: guardSourceRecovery, recovery: source},
		lastProblem: problem(released),
	}
	// The guard is the sole owner of the retained source: a discarded
	// guard must close the source like the Rust guard drop, so the
	// finalizer is the Drop analog (cleared by RetryCleanup when the
	// retry completes).
	runtime.SetFinalizer(guard, (*RecoverySourceCleanupGuard).closeRetained)
	return sourceEnd{
		cause: cause,
		guard: guard,
	}
}

// openProblem builds the plain open failure (Rust open_problem).
func openProblem(cause error) *sourceOpenFailure {
	return &sourceOpenFailure{cause: cause, guard: nil}
}

// openProblemLive folds one failed live open exactly like the Rust
// finish_open Claimed arm: a claimed-open unwind with coordination
// residue retains the half-released source in a retryable cleanup
// guard; every other failure is the plain open problem.
func openProblemLive(cause error) *sourceOpenFailure {
	var open *live.OpenFailure
	if errors.As(cause, &open) && open.Residue {
		return &sourceOpenFailure{
			cause: open.Cause,
			guard: &RecoverySourceCleanupGuard{
				source:      &guardSource{kind: guardSourceRecovery, recovery: &recoverySource{live: open.Retained}},
				lastProblem: problem(open.Released),
			},
		}
	}
	return openProblem(cause)
}

// cleanupForCause maps one failed release to the terminal cause (Rust
// cleanup_for_cause: ForkedHandle keeps its class, every other cause
// is the cleanup-conflict class).
func cleanupForCause(cause error) error {
	var fe *format.Error
	if errors.As(cause, &fe) && fe.Code == format.CodeForkedHandle {
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
	file, err := openSourceFilePlatform(filepath.Clean(path), flags)
	if err != nil {
		var fe *format.Error
		if errors.As(err, &fe) {
			return nil, fe
		}
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
	// Rust bind runs require_main_available between the selection and
	// the second path proof (source_guard/basic.rs bind); the custody
	// proof propagates its own errors, the path proofs stay
	// candidate-changed.
	if err := live.RequireMainAvailable(source.path, source.identity, meta.DatabaseID); err != nil {
		return format.Meta{}, err
	}
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
	// Rust bind_current runs require_main_available between the
	// bootstrap and the second path proof (source_guard/basic.rs
	// bind_current); the custody proof propagates its own errors.
	if err := live.RequireMainAvailable(source.path, source.identity, meta.DatabaseID); err != nil {
		return format.Meta{}, err
	}
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
	m, err := mapping.MapFile(file, available, false)
	if err != nil {
		return nil, err
	}
	// Rust source_guard/basic.rs:165 map_available applies the worker
	// session unreadable-page list to the basic (immutable/offline)
	// guard mapping before the recovery scan probes it.
	if err := m.SetUnreadablePages(mapping.SessionUnreadablePages()); err != nil {
		_ = m.Close()
		return nil, err
	}
	return m, nil
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

// close releases the mapped extent and the descriptor in the Rust
// drop order (BasicSource field order: mapping before file; the
// lifetime lock is already released by the terminal). Close errors
// are dropped exactly like the Rust drop, which cannot report them.
func (s *basicSource) close() {
	if s.mapping != nil {
		_ = s.mapping.Close()
		s.mapping = nil
	}
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}
