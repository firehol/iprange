package recovery

// Recovery candidate inspection (Rust recovery/inspection.rs
// inspect_recovery_candidates): the retained meta pair of one source is
// opened under the mode's coordination binding and classified without
// scanning any page graph. The inspection returns the exact candidate
// tokens and the classification progress; the live arm exposes only
// the proven newest candidate, exactly like the Rust live_inspection.

import (
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// RecoveryInspectionMode selects the coordination binding of one
// recovery candidate inspection (Rust RecoveryInspectionMode). Offline
// certifies exclusive quiescence for the complete operation.
type RecoveryInspectionMode uint8

const (
	RecoveryInspectionImmutable RecoveryInspectionMode = iota
	RecoveryInspectionLive
	RecoveryInspectionOffline
)

// RecoveryCandidateInspection is the bounded recovery-candidate
// inspection result (Rust RecoveryCandidateInspection): the source
// identity, the classification progress, and the projected candidate
// tokens in the Rust candidate order (newest, previous, unordered
// pages; the live inspection exposes only the newest).
type RecoveryCandidateInspection struct {
	SourceIdentity publication.LocalFileIdentity
	Progress       validation.ValidationProgress
	candidates     [2]*RecoveryCandidate
}

// CandidateCount reports the number of present candidate tokens.
func (i *RecoveryCandidateInspection) CandidateCount() int {
	count := 0
	for _, candidate := range i.candidates {
		if candidate != nil {
			count++
		}
	}
	return count
}

// Candidate returns the present candidate at one index, or nil.
func (i *RecoveryCandidateInspection) Candidate(index int) *RecoveryCandidate {
	if index < 0 || index >= len(i.candidates) {
		return nil
	}
	return i.candidates[index]
}

// Candidates returns the present candidate tokens in order.
func (i *RecoveryCandidateInspection) Candidates() []*RecoveryCandidate {
	out := make([]*RecoveryCandidate, 0, 2)
	for _, candidate := range i.candidates {
		if candidate != nil {
			out = append(out, candidate)
		}
	}
	return out
}

// newRecoveryCandidateInspection builds one inspection result (Rust
// RecoveryCandidateInspection::new).
func newRecoveryCandidateInspection(identity publication.LocalFileIdentity, progress validation.ValidationProgress, candidates [2]*RecoveryCandidate) *RecoveryCandidateInspection {
	return &RecoveryCandidateInspection{SourceIdentity: identity, Progress: progress, candidates: candidates}
}

// InspectRecoveryCandidates classifies the retained recovery
// candidates of one database path under the selected mode (Rust
// inspect_recovery_candidates): the platform, budget, and cancellation
// preflights run before any path access, exactly like the Rust entry;
// the public facade composes this entry.
func InspectRecoveryCandidates(path string, mode RecoveryInspectionMode, budget *validation.ValidationBudget, check func() error) (*RecoveryCandidateInspection, error) {
	if mode == RecoveryInspectionLive {
		if err := live.CheckSupported(); err != nil {
			return nil, err
		}
	}
	if err := requireInspectionBudget(budget, mode); err != nil {
		return nil, err
	}
	if err := live.Checkpoint(check); err != nil {
		return nil, err
	}
	switch mode {
	case RecoveryInspectionImmutable:
		return inspectImmutable(path, check)
	case RecoveryInspectionLive:
		return inspectLive(path, check)
	case RecoveryInspectionOffline:
		return inspectOffline(path, check)
	default:
		return nil, &format.Error{Code: format.CodeInvalidEnum, Detail: "invalid recovery inspection mode"}
	}
}

// requireInspectionBudget validates the shared budget and the
// open-file bound of one mode (Rust require_budget: the live binding
// holds two files, every other binding one).
func requireInspectionBudget(budget *validation.ValidationBudget, mode RecoveryInspectionMode) error {
	if budget == nil {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "validation budget is required"}
	}
	if err := budget.Validate(); err != nil {
		return err
	}
	required := uint32(1)
	if mode == RecoveryInspectionLive {
		required = 2
	}
	if budget.MaxOpenFiles < required {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery inspection open-file budget"}
	}
	return nil
}

// inspectImmutable classifies the meta pair of one immutable source
// (Rust inspect_immutable): the shared-lifetime-locked read-only
// source, the raw classification, per-candidate availability, the
// post-classification source verification, and the candidate
// projection.
func inspectImmutable(path string, check func() error) (*RecoveryCandidateInspection, error) {
	source, err := validation.OpenImmutableSource(path, check)
	if err != nil {
		return nil, err
	}
	classified, err := readClassified(source.FileHandle(), check)
	if err != nil {
		return nil, closeImmutable(source, err)
	}
	if err := requireImmutableAvailable(source, &classified); err != nil {
		return nil, closeImmutable(source, err)
	}
	if err := source.Verify(); err != nil {
		return nil, closeImmutable(source, err)
	}
	result, err := inspection(source.PublicIdentity(), &classified)
	if err != nil {
		return nil, closeImmutable(source, err)
	}
	if err := source.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

// inspectOffline classifies the meta pair of one quiescent source
// (Rust inspect_offline): the read-write exclusive-lifetime-locked
// source, the raw classification, per-candidate availability, the
// post-classification verification, and the candidate projection.
func inspectOffline(path string, check func() error) (*RecoveryCandidateInspection, error) {
	source, err := openOfflineSource(path, check)
	if err != nil {
		return nil, err
	}
	classified, err := readClassified(source.file, check)
	if err != nil {
		return nil, closeOffline(source, err)
	}
	if err := requireOfflineAvailable(source, &classified); err != nil {
		return nil, closeOffline(source, err)
	}
	if err := source.verify(); err != nil {
		return nil, closeOffline(source, err)
	}
	result, err := inspection(source.publicIdentity(), &classified)
	if err != nil {
		return nil, closeOffline(source, err)
	}
	if err := source.close(); err != nil {
		return nil, err
	}
	return result, nil
}

// inspectLive classifies the meta pair of one live database under the
// reader-table gate (Rust inspect_live): the shared-lifetime read-only
// open, the proven-current requirement, the exclusive gate over the
// ready reader table, the re-proved pair under the gate, and the
// newest-only projection. The open composes the Go live-reader
// authority (mapping.OpenLiveReader): its SameFile identity checks
// stand for the Rust single-link namespace rule and its two-page
// geometry refusal surfaces before classification, both established
// Go-live deviations reviewed at the reader gate.
func inspectLive(path string, check func() error) (*RecoveryCandidateInspection, error) {
	m, err := mapping.OpenLiveReader(path, func(string) error { return live.Checkpoint(check) })
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*RecoveryCandidateInspection, error) {
		return nil, combineErrors(err, m.Close())
	}
	initial, err := classifyMapping(m)
	if err != nil {
		return fail(err)
	}
	current, err := requireLiveCurrent(&initial)
	if err != nil {
		return fail(err)
	}
	// require_main_available is the recorded POSIX no-op (Rust
	// live_cleanup::require_main_available; the Windows GC custody
	// arrives with the M5 surface).
	gate, err := live.OpenLiveRecoveryGate(path, current.DatabaseID, check)
	if err != nil {
		return fail(live.CoordinationCause(err))
	}
	release := func(primary error) error { return gate.Release(primary) }
	result, err := inspectLiveLocked(path, m, gate, check)
	if err != nil {
		return nil, combineErrors(m.Close(), release(err))
	}
	if err := release(nil); err != nil {
		return nil, combineErrors(err, m.Close())
	}
	if err := m.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

// inspectLiveLocked runs the inspection under the held gate (Rust
// inspect_live_locked): the pair is re-classified and re-proven, the
// reader table identity is compared, the slots are inspected at most
// to the proven transaction, the pair is verified again, and the
// newest-only projection is built.
func inspectLiveLocked(path string, m *mapping.Mapping, gate *live.LiveRecoveryGate, check func() error) (*RecoveryCandidateInspection, error) {
	verifyLive := func() error {
		if err := m.VerifyIdentity(path); err != nil {
			return live.CoordinationCause(err)
		}
		return live.CoordinationCause(gate.Verify())
	}
	if err := verifyLive(); err != nil {
		return nil, err
	}
	if err := live.Checkpoint(check); err != nil {
		return nil, err
	}
	classified, err := classifyMapping(m)
	if err != nil {
		return nil, err
	}
	current, err := requireLiveCurrent(&classified)
	if err != nil {
		return nil, err
	}
	if current.DatabaseID != gate.DatabaseID() {
		return nil, live.CoordinationCause(&format.Error{Code: format.CodeWrongState, Detail: "reader table belongs to a different database"})
	}
	if err := gate.InspectAtMost(current.TxnID, check); err != nil {
		return nil, live.CoordinationCause(err)
	}
	if err := verifyLive(); err != nil {
		return nil, err
	}
	return liveInspection(publicationIdentityOf(m), &classified)
}

// classifyMapping classifies the two meta pages of an opened live-reader
// mapping (the Rust read_classified over the already-locked file; the
// live-reader mapping is exactly the two meta pages).
func classifyMapping(m *mapping.Mapping) (classifiedMetas, error) {
	var states [2]bootstrap.RecoveryMetaState
	var has [2]bool
	if err := classifyMapped(m, m.Size(), nil, &states, &has); err != nil {
		return classifiedMetas{}, err
	}
	return classifyMetas(states, has), nil
}

// publicationIdentityOf builds the portable identity of one opened
// live-reader mapping (Rust public_identity over the live identity).
func publicationIdentityOf(m *mapping.Mapping) publication.LocalFileIdentity {
	device, inode, err := m.FileIdentity()
	if err != nil {
		return publication.LocalFileIdentity{}
	}
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}

// requireLiveCurrent proves the current generation of a classified
// pair (Rust require_live_current): an unproven order is the
// unprovable class, a recovery-invalid proven current is the
// unreadable class.
func requireLiveCurrent(classified *classifiedMetas) (format.Meta, error) {
	if !classified.pair.Proven() {
		return format.Meta{}, &format.Error{Code: format.CodeLiveRecoveryCurrentGenerationUnprovable, Detail: "live recovery current generation unprovable"}
	}
	meta, ok := classified.currentRecoveryMeta()
	if !ok {
		return format.Meta{}, &format.Error{Code: format.CodeLiveRecoveryCurrentGenerationUnreadable, Detail: "live recovery current generation unreadable"}
	}
	return meta, nil
}

// requireImmutableAvailable verifies every projected candidate is
// available in the retained source (Rust require_immutable_available;
// require_available is the recorded POSIX no-op).
func requireImmutableAvailable(source *validation.ImmutableSource, classified *classifiedMetas) error {
	for _, candidate := range classified.candidates(source.PublicIdentity()) {
		if candidate != nil {
			if err := source.RequireAvailable(candidate.DatabaseID); err != nil {
				return err
			}
		}
	}
	return nil
}

// requireOfflineAvailable verifies every projected candidate is
// available in the retained source (Rust require_offline_available;
// require_available is the recorded POSIX no-op).
func requireOfflineAvailable(source *offlineSource, classified *classifiedMetas) error {
	for _, candidate := range classified.candidates(source.publicIdentity()) {
		if candidate != nil {
			if err := source.requireAvailable(candidate.DatabaseID); err != nil {
				return err
			}
		}
	}
	return nil
}

// readClassified classes the meta pair of one opened source (Rust
// read_classified): the physical extent is sampled, at most the two
// meta pages are mapped read-only, and every present page is
// classified; absent pages stay absent for the progress accounting.
func readClassified(file *os.File, check func() error) (classifiedMetas, error) {
	stat, err := file.Stat()
	if err != nil {
		return classifiedMetas{}, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	physical := uint64(stat.Size())
	mappedBytes := physical
	if mappedBytes > 2*format.PageSize {
		mappedBytes = 2 * format.PageSize
	}
	var states [2]bootstrap.RecoveryMetaState
	var has [2]bool
	if mappedBytes >= format.PageSize {
		m, err := mapping.MapFile(file, mappedBytes, false)
		if err != nil {
			return classifiedMetas{}, err
		}
		defer func() { _ = m.Close() }()
		if err := classifyMapped(m, mappedBytes, check, &states, &has); err != nil {
			return classifiedMetas{}, err
		}
	}
	return classifyMetas(states, has), nil
}

// classifyMapped classifies every mapped meta page (Rust
// classify_mapped): a page is classified only when its complete page
// extent is inside the mapped bytes.
func classifyMapped(m *mapping.Mapping, mappedBytes uint64, check func() error, states *[2]bootstrap.RecoveryMetaState, has *[2]bool) error {
	for index := uint64(0); index < 2; index++ {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		if (index+1)*format.PageSize > mappedBytes {
			continue
		}
		page, err := m.Page(uint32(index))
		if err != nil {
			return err
		}
		states[index] = bootstrap.ClassifyRecoveryMeta(page)
		has[index] = true
	}
	return nil
}

// inspection projects one classification result (Rust inspection):
// the classification progress and the candidate tokens in order.
func inspection(identity publication.LocalFileIdentity, classified *classifiedMetas) (*RecoveryCandidateInspection, error) {
	progress, err := classified.progress()
	if err != nil {
		return nil, err
	}
	return newRecoveryCandidateInspection(identity, progress, classified.candidates(identity)), nil
}

// liveInspection projects the newest-only live result (Rust
// live_inspection): the proven newest candidate token, or the
// unreadable class when the proven current is not recoverable.
func liveInspection(identity publication.LocalFileIdentity, classified *classifiedMetas) (*RecoveryCandidateInspection, error) {
	var newest *RecoveryCandidate
	for _, candidate := range classified.candidates(identity) {
		if candidate != nil && candidate.Label == CandidateNewest {
			newest = candidate
			break
		}
	}
	if newest == nil {
		return nil, &format.Error{Code: format.CodeLiveRecoveryCurrentGenerationUnreadable, Detail: "live recovery current generation unreadable"}
	}
	progress, err := classified.progress()
	if err != nil {
		return nil, err
	}
	return newRecoveryCandidateInspection(identity, progress, [2]*RecoveryCandidate{newest, nil}), nil
}

// offlineSource is one exclusive-lifetime-locked read-write database
// main of a quiescent recovery operation (Rust
// recovery/inspection.rs OfflineSource): the main is opened without
// following symlinks, the retained identity is captured any-link, the
// exclusive lifetime lock certifies quiescence, and the path identity
// is re-proven under the lock.
type offlineSource struct {
	file     *os.File
	path     string
	identity live.FileIdentity
	locked   bool
}

// openOfflineSource opens the quiescent source (Rust
// OfflineSource::open: open_rw, identity_any_link, the exclusive
// lifetime lock, then the path identity re-check).
func openOfflineSource(path string, check func() error) (*offlineSource, error) {
	clean := filepath.Clean(path)
	file, err := os.OpenFile(clean, os.O_RDWR|unixO_NOFOLLOW, 0)
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "open: " + err.Error()}
	}
	identity, err := live.IdentityAnyLink(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := live.LockFileCancellable(file, live.MainLifetimeOffset, live.LockExclusive, check); err != nil {
		file.Close()
		return nil, err
	}
	source := &offlineSource{file: file, path: clean, identity: identity, locked: true}
	if err := live.VerifyPathAnyLink(source.path, source.identity); err != nil {
		unlockErr := source.unlock()
		file.Close()
		return nil, combineErrors(err, unlockErr)
	}
	return source, nil
}

// verify re-proves the path identity under the still-held exclusive
// lock (Rust OfflineSource::verify).
func (s *offlineSource) verify() error {
	return live.VerifyPathAnyLink(s.path, s.identity)
}

// publicIdentity returns the portable local identity (Rust
// OfflineSource::public_identity).
func (s *offlineSource) publicIdentity() publication.LocalFileIdentity {
	device, inode := live.IdentityDeviceInode(&s.identity)
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}

// requireAvailable verifies the retained source is still available for
// the database identity (Rust OfflineSource::require_available over
// live_cleanup::require_main_available; the recorded POSIX no-op).
func (s *offlineSource) requireAvailable(databaseID [16]byte) error { return nil }

// close releases the exclusive lifetime lock and the descriptor (the
// unlock error folds first).
func (s *offlineSource) close() error {
	unlockErr := s.unlock()
	closeErr := s.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func (s *offlineSource) unlock() error {
	if s.locked {
		s.locked = false
		return live.UnlockFile(s.file, live.MainLifetimeOffset)
	}
	return nil
}

// closeImmutable closes an immutable source under a primary error
// (Rust combine_errors arms on the drop).
func closeImmutable(source *validation.ImmutableSource, primary error) error {
	return combineErrors(primary, source.Close())
}

// closeOffline closes a quiescent source under a primary error.
func closeOffline(source *offlineSource, primary error) error {
	return combineErrors(primary, source.close())
}

// combineErrors joins two failures with the primary first (the Rust
// combine_errors arms of the recovery and validation sources).
func combineErrors(primary, secondary error) error {
	if primary != nil {
		return primary
	}
	return secondary
}
