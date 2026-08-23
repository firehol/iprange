// Canonical empty live-pair creation and failure cleanup (Rust
// live_lifecycle/creation.rs create_live): the sidecar is reserved and
// published before the main file is written, every step is crash- and
// cancellation-checkpointed at the exact Rust points, and any failure
// removes the created artifacts identity-guarded, ordered by cleanup
// ordinal. The result reports the factual terminal state (Created,
// NotCreated, or OutcomeUnknown) with the retained identities.

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/random"
)

// CreationState is the factual terminal state of one creation attempt
// (Rust live_lifecycle::creation::CreationState).
type CreationState uint8

const (
	// CreationStateNotCreated: nothing observable remains.
	CreationStateNotCreated CreationState = iota
	// CreationStateCreated: the pair is complete and committed.
	CreationStateCreated
	// CreationStateOutcomeUnknown: a cleanup could not prove absence.
	CreationStateOutcomeUnknown
)

// CreateResult is the identity and terminal state of one creation
// attempt (Rust live_lifecycle::creation::CreateResult). Identities are
// nil when the corresponding artifact was never created or no longer
// exists.
type CreateResult struct {
	AddressFamily       uint8
	ValueKind           uint8
	StructureKind       uint8
	ValueTag            [16]byte
	DatabaseID          [16]byte
	CommitNonce         [16]byte
	SidecarID           [16]byte
	DirectoryIdentity   *FileIdentity
	MainBasename        LocalBasename
	MainIdentity        *FileIdentity
	SidecarIdentity     *FileIdentity
	ReaderCapacity      uint32
	State               CreationState
	ResiduePossible     bool
	Housekeeping        housekeeping
	VisibleHousekeeping []HousekeepingArtifact
	Cause               error
}

// createAttempt is the immutable identity of one creation attempt
// (Rust creation::Attempt).
type createAttempt struct {
	addressFamily  uint8
	valueKind      uint8
	structureKind  uint8
	valueTag       [16]byte
	databaseID     [16]byte
	commitNonce    [16]byte
	sidecarID      [16]byte
	directoryID    *FileIdentity
	mainBasename   LocalBasename
	readerCapacity uint32
}

// CreateLive creates one empty transaction-1 live database and reader
// table at path (Rust live_writer::create_live / creation::create_live),
// with the sidecar-first ordering: the canonical .readers sidecar is
// reserved, initialized as creating, parent-synced, and the destination
// re-verified absent before the main file is created privately. check,
// when non-nil, is the cancellation checkpoint (Rust
// CancellationToken). Capacity-zero, invalid-kind, and invalid
// destination arguments are hard errors; every later failure returns a
// CreateResult with the factual state.
func CreateLive(path string, addressFamily, valueKind, structureKind uint8, valueTag [16]byte, readerCapacity uint32, check func() error) (*CreateResult, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	if err := validateDestination(path, readerCapacity); err != nil {
		return nil, err
	}
	if err := validateKinds(valueKind, structureKind); err != nil {
		return nil, err
	}
	attempt, err := newCreateAttempt(path, addressFamily, valueKind, structureKind, valueTag, readerCapacity)
	if err != nil {
		return nil, err
	}
	dirID, err := parentIdentity(path)
	if err != nil {
		return attempt.notCreated(err), nil
	}
	attempt.directoryID = &dirID
	if err := attempt.bindCleanupIDs(path); err != nil {
		return attempt.notCreated(err), nil
	}
	if err := checkpoint(check); err != nil {
		return attempt.notCreated(err), nil
	}
	sidecar, failure := reserve(path, attempt.databaseID, attempt.sidecarID, readerCapacity)
	if failure != nil {
		return attempt.reservationFailure(*failure), nil
	}
	if err := checkpoint(check); err != nil {
		return attempt.failed(path, sidecar, nil, err), nil
	}
	if err := sidecar.initializeCreating(); err != nil {
		return attempt.failed(path, sidecar, nil, err), nil
	}
	if err := prepareSidecar(path, sidecar, check); err != nil {
		return attempt.failed(path, sidecar, nil, err), nil
	}

	created, failure := createPrivate(path, cleanupAuthority{
		attemptID:     attempt.databaseID,
		ordinal:       0,
		kind:          cleanupKindOwnedMain,
		directoryRole: cleanupRoleMainFile,
	})
	if failure != nil {
		return attempt.privateFailure(sidecar, *failure), nil
	}
	main := created.file
	mainIdentity := created.identity
	spec := emptySpec{
		addressFamily: attempt.addressFamily,
		valueKind:     attempt.valueKind,
		structureKind: attempt.structureKind,
		valueTag:      attempt.valueTag,
		databaseID:    attempt.databaseID,
		commitNonce:   attempt.commitNonce,
	}
	if err := initializePair(path, main, sidecar, spec, check); err != nil {
		main.Close()
		return attempt.failed(path, sidecar, &mainIdentity, err), nil
	}
	return attempt.created(mainIdentity, sidecar.localIdentity()), nil
}

func newCreateAttempt(path string, addressFamily, valueKind, structureKind uint8, valueTag [16]byte, readerCapacity uint32) (createAttempt, error) {
	databaseID, err := random.Nonzero128()
	if err != nil {
		return createAttempt{}, err
	}
	commitNonce, err := random.Nonzero128()
	if err != nil {
		return createAttempt{}, err
	}
	sidecarID, err := random.Nonzero128()
	if err != nil {
		return createAttempt{}, err
	}
	basename, err := localBasenameFromPath(path)
	if err != nil {
		return createAttempt{}, err
	}
	return createAttempt{
		addressFamily:  addressFamily,
		valueKind:      valueKind,
		structureKind:  structureKind,
		valueTag:       valueTag,
		databaseID:     databaseID,
		commitNonce:    commitNonce,
		sidecarID:      sidecarID,
		mainBasename:   basename,
		readerCapacity: readerCapacity,
	}, nil
}

// bindCleanupIDs re-binds the attempt ids through the cleanup identity
// draw (Rust creation::Attempt::bind_cleanup_ids: unique_attempt_id at
// the main path ordinal 0 and at the canonical sidecar path ordinal 1).
func (a *createAttempt) bindCleanupIDs(path string) error {
	databaseID, err := uniqueAttemptID(path, 0)
	if err != nil {
		return err
	}
	sidecarPath, err := canonicalSidecarPath(path)
	if err != nil {
		return err
	}
	sidecarID, err := uniqueAttemptID(sidecarPath, 1)
	if err != nil {
		return err
	}
	a.databaseID = databaseID
	a.sidecarID = sidecarID
	return nil
}

func (a createAttempt) created(main FileIdentity, sidecar FileIdentity) *CreateResult {
	return a.result(CreationStateCreated, &main, &sidecar, cleanFacts())
}

func (a createAttempt) notCreated(cause error) *CreateResult {
	return a.result(CreationStateNotCreated, nil, nil, causeFacts(cause))
}

// reservationFailure folds a private-creation failure of the sidecar
// (Rust Attempt::reservation_failure): the failure's identity and
// cleanup outcome feed the terminal facts unchanged.
func (a createAttempt) reservationFailure(failure privateCreationFailure) *CreateResult {
	return a.failureResult(nil, failure.identity, failure.cause, failure.cleanup)
}

// privateFailure folds a private-creation failure of the main file and
// removes the reserved sidecar only when the main cleanup was clean
// (Rust Attempt::private_failure). The failure's main identity and
// main cleanup outcome are retained unchanged; a failed main cleanup
// stops the cascade so the resolver can sort the residue.
func (a createAttempt) privateFailure(sidecar *Sidecar, failure privateCreationFailure) *CreateResult {
	cleanup := failure.cleanup
	if cleanup.isClean() {
		cleanup.absorb(removeExact(sidecar.path, sidecar.localIdentity()))
	}
	return a.failureResult(failure.identity, &sidecar.identity, failure.cause, cleanup)
}

// failed cleans up both created artifacts, main first, then the
// sidecar (Rust Attempt::failed + creation::cleanup: removal stops at
// the first failed cleanup, ordered by ordinal).
func (a createAttempt) failed(path string, sidecar *Sidecar, main *FileIdentity, cause error) *CreateResult {
	var cleanup cleanupOutcome
	if main != nil {
		cleanup.absorb(removeExact(path, *main))
		if !cleanup.isClean() {
			return a.failureResult(main, &sidecar.identity, cause, cleanup)
		}
	}
	cleanup.absorb(removeExact(sidecar.path, sidecar.localIdentity()))
	return a.failureResult(main, &sidecar.identity, cause, cleanup)
}

func (a createAttempt) failureResult(main *FileIdentity, sidecar *FileIdentity, cause error, cleanup cleanupOutcome) *CreateResult {
	facts := failedFacts(cause, cleanup)
	state := CreationStateNotCreated
	if facts.residuePossible {
		state = CreationStateOutcomeUnknown
	}
	return a.result(state, main, sidecar, facts)
}

func (a createAttempt) result(state CreationState, main *FileIdentity, sidecar *FileIdentity, facts terminalFacts) *CreateResult {
	return &CreateResult{
		AddressFamily:       a.addressFamily,
		ValueKind:           a.valueKind,
		StructureKind:       a.structureKind,
		ValueTag:            a.valueTag,
		DatabaseID:          a.databaseID,
		CommitNonce:         a.commitNonce,
		SidecarID:           a.sidecarID,
		DirectoryIdentity:   a.directoryID,
		MainBasename:        a.mainBasename,
		MainIdentity:        main,
		SidecarIdentity:     sidecar,
		ReaderCapacity:      a.readerCapacity,
		State:               state,
		ResiduePossible:     facts.residuePossible,
		Housekeeping:        facts.housekeeping,
		VisibleHousekeeping: facts.visibleHousekeep,
		Cause:               facts.cause,
	}
}

func validateDestination(path string, readerCapacity uint32) error {
	if readerCapacity == 0 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "reader capacity must be greater than zero"}
	}
	if err := requireAbsent(path); err != nil {
		return err
	}
	sidecarPath, err := canonicalSidecarPath(path)
	if err != nil {
		return err
	}
	return requireAbsent(sidecarPath)
}

func validateKinds(valueKind, structureKind uint8) error {
	valid := false
	switch valueKind {
	case format.ValueKindDirect, format.ValueKindMembership:
		valid = structureKind == format.StructureKindNone
	case format.ValueKindStructured:
		valid = structureKind != format.StructureKindNone
	}
	if !valid {
		return &format.Error{Code: format.CodeWrongStructureKind, Detail: "value kind and structure kind do not form a valid database"}
	}
	return nil
}

// prepareSidecar runs the crash points between the creating state and
// the main-file creation (Rust creation::prepare_sidecar).
func prepareSidecar(path string, sidecar *Sidecar, check func() error) error {
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := syncParent(sidecar.path); err != nil {
		return err
	}
	fault.Crash("create.after_sidecar_parent_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	return requireAbsent(path)
}

// initializePair writes the empty main image, publishes the ready
// sidecar state, and runs the crash points between each durability step
// (Rust creation::initialize_pair).
func initializePair(path string, main *os.File, sidecar *Sidecar, spec emptySpec, check func() error) error {
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := writeEmpty(main, spec); err != nil {
		return err
	}
	fault.Crash("create.after_main_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := syncParent(path); err != nil {
		return err
	}
	fault.Crash("create.after_main_parent_sync")
	if err := checkpoint(check); err != nil {
		return err
	}
	if err := sidecar.publishReady(); err != nil {
		return err
	}
	fault.Crash("create.after_ready_sync")
	if err := syncParent(sidecar.path); err != nil {
		return err
	}
	fault.Crash("create.after_ready_parent_sync")
	return nil
}

// requireAbsent refuses a destination that already exists (Rust
// creation::require_absent: path_identity present -> InvalidArgument).
func requireAbsent(path string) error {
	identity, err := pathIdentity(path)
	if err != nil {
		return err
	}
	if identity != nil {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "destination already exists"}
	}
	return nil
}
