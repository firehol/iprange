// Canonical offline immutable-to-live transition (Rust
// live_lifecycle/transition.rs initialize_live): the main file is
// opened read-write under the exclusive lifetime lock, its committed
// generation is proven through the Writer-mode bootstrap with the
// exact-committed-length rule, the canonical sidecar must be absent,
// and the sidecar is reserved, initialized creating, parent-synced,
// verified against the locked main, and published ready. Every step is
// crash- and cancellation-checkpointed at the exact Rust points; any
// failure removes the created sidecar identity-guarded and reports the
// factual state.

package live

import (
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// LiveTransitionOperation is one offline live-coordination operation
// (Rust live_lifecycle::LiveTransitionOperation).
type LiveTransitionOperation uint8

const (
	LiveTransitionInitialize LiveTransitionOperation = iota
	LiveTransitionReset
)

// LiveResetPolicy is the namespace guarantee selected for replacing
// existing live coordination (Rust LiveResetPolicy). Reset is out of
// the current 4-3 slice (scheduled for chunk 4-6); the enum exists
// because LiveTransitionResult carries it.
type LiveResetPolicy uint8

const (
	LiveResetRollbackSafe LiveResetPolicy = iota
	LiveResetDiscardPrevious
)

// LiveTransitionStatus is the factual state after one offline
// transition attempt (Rust LiveTransitionStatus).
type LiveTransitionStatus uint8

const (
	LiveTransitionStatusUnchanged LiveTransitionStatus = iota
	LiveTransitionStatusInitialized
	LiveTransitionStatusOutcomeUnknown
)

// LiveCoordinationLocation is the last proven location of the new
// coordination inode (Rust LiveCoordinationLocation).
type LiveCoordinationLocation uint8

const (
	LiveCoordinationLocationAbsent LiveCoordinationLocation = iota
	LiveCoordinationLocationCanonical
	LiveCoordinationLocationPrivate
	LiveCoordinationLocationUnclassified
)

// LiveTransitionResult is the exact facts retained for one transition
// attempt (Rust live_lifecycle::LiveTransitionResult). The directory
// and main identities are always present; a sidecar identity is nil
// when that sidecar was never created or no longer exists.
type LiveTransitionResult struct {
	Operation               LiveTransitionOperation
	ResetPolicy             *LiveResetPolicy
	Status                  LiveTransitionStatus
	DatabaseID              [16]byte
	TransactionID           uint64
	CommitNonce             [16]byte
	DirectoryIdentity       *FileIdentity
	MainIdentity            *FileIdentity
	MainBasename            LocalBasename
	ReaderCapacity          uint32
	SidecarID               [16]byte
	PreviousSidecarIdentity *FileIdentity
	NewSidecarIdentity      *FileIdentity
	NewSidecarLocation      LiveCoordinationLocation
	ResiduePossible         bool
	Housekeeping            housekeeping
	VisibleHousekeeping     []HousekeepingArtifact
	Cause                   error
}

// lockedMain is the exclusive-lifetime-locked read-write handle of one
// quiescent main file (Rust transition::LockedMain): the retained
// identity, the parent identity, the portable basename, and the proven
// Writer-mode bootstrap.
type lockedMain struct {
	path              string
	file              *os.File
	identity          FileIdentity
	directoryIdentity FileIdentity
	basename          LocalBasename
	bootstrap         *bootstrap.Result
}

// liveTransitionAttempt is the immutable identity of one transition
// attempt (Rust transition::Attempt).
type liveTransitionAttempt struct {
	operation         LiveTransitionOperation
	resetPolicy       *LiveResetPolicy
	databaseID        [16]byte
	transactionID     uint64
	commitNonce       [16]byte
	directoryIdentity *FileIdentity
	mainIdentity      *FileIdentity
	mainBasename      LocalBasename
	readerCapacity    uint32
	sidecarID         [16]byte
}

// InitializeLive converts one quiescent immutable database into a live
// database (Rust live_lifecycle::initialize_live / transition::
// initialize_live): the main file is opened read-write and locked for
// its lifetime, its committed generation is proven with the exact
// committed length, the canonical .readers sidecar must be absent, and
// the sidecar is reserved and initialized to the ready state. check,
// when non-nil, is the cancellation checkpoint (Rust
// CancellationToken). Capacity-zero and every open/proof failure are
// hard errors; later failures return a LiveTransitionResult with the
// factual state.
func InitializeLive(path string, readerCapacity uint32, check func() error) (*LiveTransitionResult, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	if err := requireCapacity(readerCapacity); err != nil {
		return nil, err
	}
	main, err := openLockedMain(path, check)
	if err != nil {
		return nil, err
	}
	defer main.file.Close()
	canonical, err := canonicalSidecarPath(main.path)
	if err != nil {
		return nil, err
	}
	if err := requireSidecarAbsent(canonical); err != nil {
		return nil, err
	}
	attempt, err := newTransitionAttempt(LiveTransitionInitialize, main, canonical, readerCapacity)
	if err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	sidecar, failure := reserve(main.path, attempt.databaseID, attempt.sidecarID, readerCapacity)
	if failure != nil {
		return attempt.reservationFailure(*failure), nil
	}
	identity := sidecar.localIdentity()
	if err := checkpoint(check); err != nil {
		return attempt.cleanupCreated(sidecar, err, LiveCoordinationLocationCanonical), nil
	}
	if err := initializeSidecar(main, sidecar, check); err != nil {
		return attempt.cleanupCreated(sidecar, err, LiveCoordinationLocationCanonical), nil
	}
	return attempt.initialized(&identity), nil
}

// openLockedMain opens path read-write without following symlinks,
// proves the retained identity and parent identity, takes the
// exclusive lifetime byte-range lock, re-verifies the path, and proves
// the committed generation through the Writer-mode bootstrap with the
// exact-committed-length rule (Rust transition::LockedMain::open).
func openLockedMain(path string, check func() error) (*lockedMain, error) {
	file, identity, err := openRw(path)
	if err != nil {
		return nil, err
	}
	directory, err := parentIdentity(path)
	if err != nil {
		file.Close()
		return nil, err
	}
	basename, err := localBasenameFromPath(path)
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := lockCancellable(file, mainLifetimeOffset, lockExclusive, check); err != nil {
		file.Close()
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		file.Close()
		return nil, err
	}
	if err := verifyPath(path, identity); err != nil {
		file.Close()
		return nil, err
	}
	result, err := bootstrapOf(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	// require_main_available is a POSIX no-op (Windows GC custody
	// verification only); the call keeps the Rust flow exact.
	if err := requireAvailable(filepath.Clean(path), identity, cleanupAuthority{
		attemptID:     result.Meta.DatabaseID,
		ordinal:       0,
		kind:          cleanupKindOwnedMain,
		directoryRole: cleanupRoleMainFile,
	}); err != nil {
		file.Close()
		return nil, err
	}
	if result.PhysicalBytes != result.CommittedBytes {
		file.Close()
		return nil, &format.Error{Code: format.CodeWrongState, Detail: "offline transition requires exact committed length"}
	}
	return &lockedMain{
		path:              filepath.Clean(path),
		file:              file,
		identity:          identity,
		directoryIdentity: directory,
		basename:          basename,
		bootstrap:         result,
	}, nil
}

// verify re-proves the path identity and the unchanged committed
// generation (Rust transition::LockedMain::verify: any meta or length
// change is a CleanupConflict).
func (m *lockedMain) verify() error {
	if err := verifyPath(m.path, m.identity); err != nil {
		return err
	}
	current, err := bootstrapOf(m.file)
	if err != nil {
		return err
	}
	if current.Meta != m.bootstrap.Meta ||
		current.CommittedBytes != m.bootstrap.CommittedBytes ||
		current.PhysicalBytes != m.bootstrap.PhysicalBytes {
		return &format.Error{Code: format.CodeCleanupConflict, Detail: "main generation changed during live transition"}
	}
	return nil
}

// bootstrapOf proves the committed generation of an open descriptor
// through the two mapped meta pages in Writer mode (Rust
// database_file::bootstrap_file + bootstrap_mapping).
func bootstrapOf(f *os.File) (*bootstrap.Result, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, &format.Error{Code: format.CodeIO, Detail: "main stat: " + err.Error()}
	}
	physical := uint64(st.Size())
	m, err := mapping.MapFile(f, 2*format.PageSize, false)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	p0, err := m.Page(0)
	if err != nil {
		return nil, err
	}
	p1, err := m.Page(1)
	if err != nil {
		return nil, err
	}
	return bootstrap.Open(p0, p1, physical, bootstrap.ModeWriter)
}

// newTransitionAttempt captures the locked main's identity facts and
// draws the cleanup sidecar identity at the canonical path ordinal 1
// (Rust transition::Attempt::new).
func newTransitionAttempt(operation LiveTransitionOperation, main *lockedMain, cleanupSource string, readerCapacity uint32) (liveTransitionAttempt, error) {
	sidecarID, err := uniqueAttemptID(cleanupSource, 1)
	if err != nil {
		return liveTransitionAttempt{}, err
	}
	return liveTransitionAttempt{
		operation:         operation,
		databaseID:        main.bootstrap.Meta.DatabaseID,
		transactionID:     main.bootstrap.Meta.TxnID,
		commitNonce:       main.bootstrap.Meta.CommitNonce,
		directoryIdentity: &main.directoryIdentity,
		mainIdentity:      &main.identity,
		mainBasename:      main.basename,
		readerCapacity:    readerCapacity,
		sidecarID:         sidecarID,
	}, nil
}

// reservationFailure folds a sidecar private-creation failure (Rust
// transition::Attempt::reservation_failure): the failure's identity
// and cleanup outcome feed the terminal facts; residue makes the
// outcome unknown at an unclassified location.
func (a liveTransitionAttempt) reservationFailure(failure privateCreationFailure) *LiveTransitionResult {
	facts := failedFacts(failure.cause, failure.cleanup)
	if facts.residuePossible {
		return a.result(LiveTransitionStatusOutcomeUnknown, failure.identity, LiveCoordinationLocationUnclassified, facts)
	}
	return a.result(LiveTransitionStatusUnchanged, failure.identity, LiveCoordinationLocationAbsent, facts)
}

// initialized reports the completed transition (Rust
// Attempt::initialized): the sidecar identity at the canonical location
// with clean facts.
func (a liveTransitionAttempt) initialized(identity *FileIdentity) *LiveTransitionResult {
	return a.result(LiveTransitionStatusInitialized, identity, LiveCoordinationLocationCanonical, cleanFacts())
}

// cleanupCreated removes the created sidecar identity-guarded and
// reports the factual state (Rust Attempt::cleanup_created: the sidecar
// identity is retained in both outcomes; a clean removal reports
// Unchanged at Absent, a failed one OutcomeUnknown at the caller
// location).
func (a liveTransitionAttempt) cleanupCreated(sidecar *Sidecar, cause error, location LiveCoordinationLocation) *LiveTransitionResult {
	cleanup := removeExact(sidecar.path, sidecar.localIdentity())
	facts := failedFacts(cause, cleanup)
	if facts.residuePossible {
		return a.result(LiveTransitionStatusOutcomeUnknown, &sidecar.identity, location, facts)
	}
	return a.result(LiveTransitionStatusUnchanged, &sidecar.identity, LiveCoordinationLocationAbsent, facts)
}

// result assembles the full Rust LiveTransitionResult field surface.
func (a liveTransitionAttempt) result(status LiveTransitionStatus, newSidecarIdentity *FileIdentity, location LiveCoordinationLocation, facts terminalFacts) *LiveTransitionResult {
	return &LiveTransitionResult{
		Operation:           a.operation,
		ResetPolicy:         a.resetPolicy,
		Status:              status,
		DatabaseID:          a.databaseID,
		TransactionID:       a.transactionID,
		CommitNonce:         a.commitNonce,
		DirectoryIdentity:   a.directoryIdentity,
		MainIdentity:        a.mainIdentity,
		MainBasename:        a.mainBasename,
		ReaderCapacity:      a.readerCapacity,
		SidecarID:           a.sidecarID,
		NewSidecarIdentity:  newSidecarIdentity,
		NewSidecarLocation:  location,
		ResiduePossible:     facts.residuePossible,
		Housekeeping:        facts.housekeeping,
		VisibleHousekeeping: facts.visibleHousekeep,
		Cause:               facts.cause,
	}
}

// initializeSidecar runs the transition sidecar steps and crash points
// between each durability step (Rust transition::initialize_sidecar).
func initializeSidecar(main *lockedMain, sidecar *Sidecar, check func() error) error {
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := sidecar.initializeCreating(); err != nil {
		return err
	}
	fault.Crash("live_initialize.after_creating_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := syncParent(sidecar.path); err != nil {
		return err
	}
	fault.Crash("live_initialize.after_creating_parent_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := main.verify(); err != nil {
		return err
	}
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := sidecar.publishReady(); err != nil {
		return err
	}
	fault.Crash("live_initialize.after_ready_sync")
	if err := syncParent(sidecar.path); err != nil {
		return err
	}
	fault.Crash("live_initialize.after_ready_parent_sync")
	return nil
}

// requireCapacity rejects a zero reader capacity (Rust
// transition::require_capacity).
func requireCapacity(capacity uint32) error {
	if capacity == 0 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "reader capacity must be greater than zero"}
	}
	return nil
}

// requireSidecarAbsent refuses any canonical sidecar entry, whatever
// its shape (Rust database_file::require_sidecar_absent: a plain
// symlink_metadata presence check).
func requireSidecarAbsent(path string) error {
	_, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &format.Error{Code: format.CodeIO, Detail: "sidecar stat: " + err.Error()}
	}
	return &format.Error{Code: format.CodeWrongState, Detail: "immutable open requires the canonical .readers sidecar to be absent"}
}
