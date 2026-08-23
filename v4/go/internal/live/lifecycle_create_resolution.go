// Exact completion or rollback of an interrupted CreateLive (Rust
// live_lifecycle/create_resolution.rs resolve_create_live): the main
// and the canonical sidecar are observed and classified (absent,
// exact, or malformed), a definitive result short-circuits, and the
// requested mode completes the pair (reserving the missing sidecar,
// writing the empty main, publishing ready) or removes both artifacts
// identity-guarded with the factual terminal state.

package live

import (
	"errors"
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// mainObservedKind classifies the creation main (Rust
// create_resolution::Main).
type mainObservedKind uint8

const (
	mainAbsent mainObservedKind = iota
	mainExact
	mainMalformed
)

// mainObserved is one observed creation main (Rust Main: the retained
// descriptor and identity when present).
type mainObserved struct {
	kind     mainObservedKind
	file     *os.File
	identity FileIdentity
}

// coordinationObservedKind classifies the creation sidecar (Rust
// create_resolution::Coordination).
type coordinationObservedKind uint8

const (
	coordinationAbsent coordinationObservedKind = iota
	coordinationExact
	coordinationMalformed
)

// coordinationObserved is one observed creation sidecar (Rust
// Coordination: the exact open sidecar with its state, or the retained
// descriptor of a malformed artifact).
type coordinationObserved struct {
	kind     coordinationObservedKind
	sidecar  *Sidecar
	state    sidecarState
	file     *os.File
	identity FileIdentity
}

// ResolveCreateLive resolves only the exact creation attempt
// identified by supplied (Rust resolve_create_live): the main and
// canonical sidecar are observed under the lifetime lock, a ready pair
// short-circuits to Created, and mode completes the missing artifacts
// or removes the attempt identity-guarded. check, when non-nil, is the
// cancellation checkpoint. Validation and open failures are hard
// errors; the resolution outcome is the factual CreateResult.
func ResolveCreateLive(path string, supplied *CreateResult, mode LiveTransitionResolutionMode, check func() error) (*CreateResult, error) {
	if err := requireLiveSupported(); err != nil {
		return nil, err
	}
	if err := requireSuppliedCreate(path, supplied); err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	main, err := observeMain(path, supplied, check)
	if err != nil {
		return nil, err
	}
	// The observed main descriptor is owned by this resolution (Rust
	// drops Main when resolve_create_live returns); close it on every
	// path, including the failure paths of the later observations.
	defer func() {
		if main.file != nil {
			main.file.Close()
		}
	}()
	sidecarPath, err := canonicalSidecarPath(path)
	if err != nil {
		return nil, err
	}
	coordination, err := observeCoordination(sidecarPath, supplied)
	if err != nil {
		return nil, err
	}
	// The observed coordination descriptor is owned by this resolution
	// (Rust drops Coordination when resolve_create_live returns);
	// close it after the resolution work on every path.
	defer func() {
		switch coordination.kind {
		case coordinationExact:
			coordination.sidecar.close()
		case coordinationMalformed:
			if coordination.file != nil {
				coordination.file.Close()
			}
		}
	}()
	if terminal, err := definitive(supplied, &main, &coordination); err != nil || terminal != nil {
		return terminal, err
	}
	switch mode {
	case LiveTransitionResolutionComplete:
		return completeCreate(path, supplied, &main, &coordination)
	case LiveTransitionResolutionRollback:
		return rollbackCreate(path, supplied, &main, &coordination)
	default:
		return nil, &format.Error{Code: format.CodeInvalidArgument, Detail: "creation result is incomplete"}
	}
}

// definitive short-circuits the resolution when the observed artifacts
// already prove the terminal state (Rust create_resolution::
// definitive): a ready pair is Created; a Created result without the
// ready pair is a conflict; a clean NotCreated result requires both
// names absent.
func definitive(supplied *CreateResult, main *mainObserved, coordination *coordinationObserved) (*CreateResult, error) {
	if main.kind == mainExact && coordination.kind == coordinationExact && coordination.state == stateReady {
		return created(supplied, mainIdentity(main), coordinationIdentity(coordination)), nil
	}
	switch supplied.State {
	case CreationStateCreated:
		return nil, &format.Error{Code: format.CodeConflict, Detail: "a completed creation result no longer names a ready pair"}
	case CreationStateNotCreated:
		if !supplied.ResiduePossible {
			if main.kind == mainAbsent && coordination.kind == coordinationAbsent {
				return notCreated(supplied, supplied.MainIdentity, supplied.SidecarIdentity), nil
			}
			return nil, &format.Error{Code: format.CodeConflict, Detail: "a clean not-created result has unexpected artifacts"}
		}
	}
	return nil, nil
}

// completeCreate finishes the interrupted creation (Rust
// create_resolution::complete): malformed artifacts are unresolvable,
// the missing sidecar is reserved and initialized creating, the missing
// main is created privately and written empty, and the pair is synced,
// published ready, and re-verified. Every failure reports the factual
// OutcomeUnknown result with the retained identities.
func completeCreate(path string, supplied *CreateResult, main *mainObserved, coordination *coordinationObserved) (*CreateResult, error) {
	if main.kind == mainMalformed {
		return nil, &format.Error{Code: format.CodeUnresolvable, Detail: "creation main exists but is not the exact empty generation"}
	}
	if coordination.kind == coordinationMalformed {
		return nil, &format.Error{Code: format.CodeUnresolvable, Detail: "creation sidecar exists but its header is malformed"}
	}

	var sidecar *Sidecar
	// The reserved sidecar (observed or freshly created) is owned by
	// this completion (Rust drops the Sidecar when complete_create
	// returns); close it on every return. Double closing an observed
	// sidecar is safe: Sidecar.close nil-checks its handles.
	defer func() {
		if sidecar != nil {
			sidecar.close()
		}
	}()
	state := stateCreating
	switch coordination.kind {
	case coordinationExact:
		sidecar = coordination.sidecar
		state = coordination.state
	case coordinationAbsent:
		var failure *privateCreationFailure
		sidecar, failure = reserve(path, supplied.DatabaseID, supplied.SidecarID, supplied.ReaderCapacity)
		if failure != nil {
			var sidecarIdentity *FileIdentity
			if failure.identity != nil {
				sidecarIdentity = failure.identity
			}
			return unknownAfterPrivateFailure(supplied, mainIdentity(main), sidecarIdentity, *failure), nil
		}
		identity := sidecar.localIdentity()
		if err := sidecar.initializeCreating(); err != nil {
			return unknown(supplied, mainIdentity(main), &identity, err), nil
		}
		if err := syncParent(sidecar.path); err != nil {
			return unknown(supplied, mainIdentity(main), &identity, err), nil
		}
	}
	sidecarIdentity := sidecar.localIdentity()

	var mainFile *os.File
	// The main descriptor (observed or freshly created) is owned by
	// this completion (Rust drops the main when complete_create
	// returns); close it on every return. Double closing the observed
	// main is safe: os.File.Close on a closed file returns ErrClosed.
	defer func() {
		if mainFile != nil {
			mainFile.Close()
		}
	}()
	var publicMain *FileIdentity
	switch main.kind {
	case mainExact:
		mainFile = main.file
		publicMain = mainIdentity(main)
	case mainAbsent:
		created, failure := createPrivate(path, cleanupAuthority{
			attemptID:     supplied.DatabaseID,
			ordinal:       0,
			kind:          cleanupKindOwnedMain,
			directoryRole: cleanupRoleMainFile,
		})
		if failure != nil {
			return unknownAfterPrivateFailure(supplied, failure.identity, &sidecarIdentity, *failure), nil
		}
		public := &created.identity
		if err := writeEmpty(created.file, expectedSpec(supplied)); err != nil {
			return unknown(supplied, public, &sidecarIdentity, err), nil
		}
		if err := syncParent(path); err != nil {
			return unknown(supplied, public, &sidecarIdentity, err), nil
		}
		mainFile = created.file
		publicMain = public
	}

	finished := mainFile.Sync()
	if finished == nil {
		finished = syncParent(path)
	}
	if finished == nil && state == stateCreating {
		finished = sidecar.publishReady()
	}
	if finished == nil {
		finished = syncParent(sidecar.path)
	}
	if finished == nil {
		finished = verifyCreated(path, mainFile, sidecar, supplied)
	}
	if finished == nil {
		return created(supplied, publicMain, &sidecarIdentity), nil
	}
	return unknown(supplied, publicMain, &sidecarIdentity, finished), nil
}

// rollbackCreate removes the creation artifacts identity-guarded, main
// first then sidecar (Rust create_resolution::rollback: the main
// cleanup failure short-circuits with the factual cleanup result; the
// sidecar path is recomputed for the removal).
func rollbackCreate(path string, supplied *CreateResult, main *mainObserved, coordination *coordinationObserved) (*CreateResult, error) {
	mainFacts := mainIdentity(main)
	sidecarFacts := coordinationIdentity(coordination)
	cleanup := cleanupOutcome{}
	if _, identity, ok := rawMain(main); ok {
		cleanup.absorb(removeExact(path, identity))
		if !cleanup.isClean() {
			return cleanupResult(supplied, mainFacts, sidecarFacts, cleanup), nil
		}
	}
	if _, identity, ok := rawCoordination(coordination); ok {
		sidecarPath, err := canonicalSidecarPath(path)
		if err != nil {
			return nil, err
		}
		cleanup.absorb(removeExact(sidecarPath, identity))
	}
	return cleanupResult(supplied, mainFacts, sidecarFacts, cleanup), nil
}

// observeMain opens and classifies the creation main under the
// exclusive lifetime lock (Rust create_resolution::observe_main: absent
// when the path is empty, exact when it carries the exact empty
// generation, malformed when a format failure can be attributed to
// this creation, and a conflict when the path carries another valid
// database).
func observeMain(path string, supplied *CreateResult, check func() error) (mainObserved, error) {
	identity, err := pathIdentity(path)
	if err != nil {
		return mainObserved{}, err
	}
	if identity == nil {
		return mainObserved{kind: mainAbsent}, nil
	}
	file, id, err := openRw(path)
	if err != nil {
		return mainObserved{}, err
	}
	// require_main_available is a POSIX no-op (Windows GC custody
	// verification only); the call keeps the Rust flow exact.
	if err := requireAvailable(path, id, cleanupAuthority{
		attemptID:     supplied.DatabaseID,
		ordinal:       0,
		kind:          cleanupKindOwnedMain,
		directoryRole: cleanupRoleMainFile,
	}); err != nil {
		file.Close()
		return mainObserved{}, err
	}
	if supplied.MainIdentity != nil && *supplied.MainIdentity != id {
		file.Close()
		return mainObserved{}, &format.Error{Code: format.CodeConflict, Detail: "creation main identity changed"}
	}
	if err := lockCancellable(file, mainLifetimeOffset, lockExclusive, check); err != nil {
		file.Close()
		return mainObserved{}, err
	}
	if err := verifyPath(path, id); err != nil {
		file.Close()
		return mainObserved{}, err
	}
	exact, err := isExactEmpty(file, expectedSpec(supplied))
	if err != nil {
		if isFormatClass(err) && supplied.MainIdentity != nil && *supplied.MainIdentity == id {
			return mainObserved{kind: mainMalformed, file: file, identity: id}, nil
		}
		file.Close()
		if isFormatClass(err) {
			return mainObserved{}, &format.Error{Code: format.CodeConflict, Detail: "malformed main cannot be attributed to this creation"}
		}
		return mainObserved{}, err
	}
	if !exact {
		file.Close()
		return mainObserved{}, &format.Error{Code: format.CodeConflict, Detail: "creation path contains another valid database"}
	}
	return mainObserved{kind: mainExact, file: file, identity: id}, nil
}

// observeCoordination opens and classifies the canonical creation
// sidecar (Rust create_resolution::observe_coordination: absent,
// exact when the header matches this creation, or malformed when a
// format failure can be attributed to the supplied sidecar identity).
func observeCoordination(path string, supplied *CreateResult) (coordinationObserved, error) {
	identity, err := existingIdentity(path)
	if err != nil {
		return coordinationObserved{}, err
	}
	if identity == nil {
		return coordinationObserved{kind: coordinationAbsent}, nil
	}
	if err := requireAvailable(path, *identity, cleanupAuthority{
		attemptID:     supplied.SidecarID,
		ordinal:       1,
		kind:          cleanupKindOwnedCoordination,
		directoryRole: cleanupRoleMainFile,
	}); err != nil {
		return coordinationObserved{}, err
	}
	if supplied.SidecarIdentity != nil && *supplied.SidecarIdentity != *identity {
		return coordinationObserved{}, &format.Error{Code: format.CodeConflict, Detail: "creation sidecar identity changed"}
	}
	sidecar, state, err := openAt(path, supplied.DatabaseID)
	if err == nil && sidecar.header.sidecarID == supplied.SidecarID && sidecar.header.capacity == supplied.ReaderCapacity {
		return coordinationObserved{kind: coordinationExact, sidecar: sidecar, state: state}, nil
	}
	if err == nil {
		sidecar.close()
		return coordinationObserved{}, &format.Error{Code: format.CodeConflict, Detail: "canonical sidecar belongs to another creation"}
	}
	if isMalformedClass(err) && supplied.SidecarIdentity != nil && *supplied.SidecarIdentity == *identity {
		file, reopened, openErr := openRw(path)
		if openErr != nil {
			return coordinationObserved{}, openErr
		}
		if reopened != *identity {
			file.Close()
			return coordinationObserved{}, &format.Error{Code: format.CodeConflict, Detail: "creation sidecar changed while it was reopened"}
		}
		if err := verifyPath(path, *identity); err != nil {
			file.Close()
			return coordinationObserved{}, err
		}
		return coordinationObserved{kind: coordinationMalformed, file: file, identity: *identity}, nil
	}
	if isMalformedClass(err) {
		return coordinationObserved{}, &format.Error{Code: format.CodeConflict, Detail: "malformed sidecar cannot be attributed to this creation"}
	}
	return coordinationObserved{}, err
}

// verifyCreated re-proves the completed pair (Rust
// create_resolution::verify_created: main identity at the path, sidecar
// path and header, and the exact empty main unchanged).
func verifyCreated(path string, main *os.File, sidecar *Sidecar, supplied *CreateResult) error {
	mainIdentity, err := identityOf(main)
	if err != nil {
		return err
	}
	if err := verifyPath(path, mainIdentity); err != nil {
		return err
	}
	if err := sidecar.verifyPath(); err != nil {
		return err
	}
	if err := sidecar.verifyHeader(); err != nil {
		return err
	}
	exact, err := isExactEmpty(main, expectedSpec(supplied))
	if err != nil {
		return err
	}
	if !exact {
		return &format.Error{Code: format.CodeConflict, Detail: "created main changed during resolution"}
	}
	return nil
}

// requireSuppliedCreate validates the retained creation facts (Rust
// create_resolution::require_supplied: nonzero identity draws, the
// destination basename, and the proven parent directory identity).
func requireSuppliedCreate(path string, supplied *CreateResult) error {
	if supplied.DatabaseID == [16]byte{} ||
		supplied.CommitNonce == [16]byte{} ||
		supplied.SidecarID == [16]byte{} ||
		supplied.ReaderCapacity == 0 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "creation result is incomplete"}
	}
	basename, err := localBasenameFromPath(path)
	if err != nil {
		return err
	}
	if basename != supplied.MainBasename {
		return &format.Error{Code: format.CodeConflict, Detail: "creation destination name changed"}
	}
	if supplied.DirectoryIdentity == nil {
		return &format.Error{Code: format.CodeUnresolvable, Detail: "creation never proved its parent directory identity"}
	}
	parent, err := parentIdentity(path)
	if err != nil {
		return err
	}
	if parent != *supplied.DirectoryIdentity {
		return &format.Error{Code: format.CodeDirectoryIdentityMismatch}
	}
	return nil
}

// expectedSpec rebuilds the exact empty main image of the creation
// attempt (Rust create_resolution::expected_spec).
func expectedSpec(supplied *CreateResult) emptySpec {
	return emptySpec{
		addressFamily: supplied.AddressFamily,
		valueKind:     supplied.ValueKind,
		structureKind: supplied.StructureKind,
		valueTag:      supplied.ValueTag,
		databaseID:    supplied.DatabaseID,
		commitNonce:   supplied.CommitNonce,
	}
}

// isExactEmpty reports whether the descriptor carries the exact empty
// generation with the exact committed length (Rust
// database_file::is_exact_empty through the Writer-mode bootstrap).
func isExactEmpty(f *os.File, spec emptySpec) (bool, error) {
	opened, err := bootstrapOf(f)
	if err != nil {
		return false, err
	}
	return opened.PhysicalBytes == opened.CommittedBytes && opened.Meta == emptyMeta(spec), nil
}

// isFormatClass reports the Rust Format|Corrupt family (Go folds both
// into CodeFormatInvalid; the UnsupportedStructure class is distinct
// and never attributed as malformed).
func isFormatClass(err error) bool {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return false
	}
	return fe.Code == format.CodeFormatInvalid
}

// isMalformedClass reports the Rust Format|Corrupt|WrongState family
// of the sidecar open (Go: CodeFormatInvalid and CodeWrongState).
func isMalformedClass(err error) bool {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return false
	}
	return fe.Code == format.CodeFormatInvalid || fe.Code == format.CodeWrongState
}

func rawMain(main *mainObserved) (*os.File, FileIdentity, bool) {
	switch main.kind {
	case mainAbsent:
		return nil, FileIdentity{}, false
	default:
		return main.file, main.identity, true
	}
}

func rawCoordination(coordination *coordinationObserved) (*os.File, FileIdentity, bool) {
	switch coordination.kind {
	case coordinationAbsent:
		return nil, FileIdentity{}, false
	case coordinationExact:
		return coordination.sidecar.file, coordination.sidecar.localIdentity(), true
	default:
		return coordination.file, coordination.identity, true
	}
}

func mainIdentity(main *mainObserved) *FileIdentity {
	if main.kind == mainAbsent {
		return nil
	}
	return &main.identity
}

func coordinationIdentity(coordination *coordinationObserved) *FileIdentity {
	switch coordination.kind {
	case coordinationAbsent:
		return nil
	case coordinationExact:
		identity := coordination.sidecar.localIdentity()
		return &identity
	default:
		return &coordination.identity
	}
}

func created(supplied *CreateResult, mainIdentity, sidecarIdentity *FileIdentity) *CreateResult {
	return createResult(supplied, CreationStateCreated, mainIdentity, sidecarIdentity, false, nil)
}

func notCreated(supplied *CreateResult, mainIdentity, sidecarIdentity *FileIdentity) *CreateResult {
	return createResult(supplied, CreationStateNotCreated, mainIdentity, sidecarIdentity, false, nil)
}

func unknown(supplied *CreateResult, mainIdentity, sidecarIdentity *FileIdentity, cause error) *CreateResult {
	return createResult(supplied, CreationStateOutcomeUnknown, mainIdentity, sidecarIdentity, true, cause)
}

// unknownAfterPrivateFailure folds one private-creation failure into
// the outcome-unknown result (Rust
// create_resolution::unknown_after_private_failure: the failure's
// cleanup housekeeping merges, and a failed cleanup nests the
// CleanupIncomplete class).
func unknownAfterPrivateFailure(supplied *CreateResult, mainIdentity, sidecarIdentity *FileIdentity, failure privateCreationFailure) *CreateResult {
	housekeeping := supplied.Housekeeping.merge(failure.cleanup.housekeeping)
	visible := append(append([]HousekeepingArtifact{}, supplied.VisibleHousekeeping...), failure.cleanup.visible...)
	cause := failure.cause
	if failure.cleanup.cause != nil {
		cause = combineErrors(failure.cause, failure.cleanup.cause)
	}
	result := createResult(supplied, CreationStateOutcomeUnknown, mainIdentity, sidecarIdentity, true, cause)
	result.Housekeeping = housekeeping
	result.VisibleHousekeeping = visible
	return result
}

// cleanupResult folds one cleanup outcome into the factual terminal
// state (Rust create_resolution::cleanup_result: a clean removal is
// NotCreated, a failed one OutcomeUnknown with residue possible).
func cleanupResult(supplied *CreateResult, mainIdentity, sidecarIdentity *FileIdentity, cleanup cleanupOutcome) *CreateResult {
	state := CreationStateNotCreated
	residue := false
	if !cleanup.isClean() {
		state = CreationStateOutcomeUnknown
		residue = true
	}
	result := createResult(supplied, state, mainIdentity, sidecarIdentity, residue, cleanup.cause)
	result.Housekeeping = supplied.Housekeeping.merge(cleanup.housekeeping)
	result.VisibleHousekeeping = append(append([]HousekeepingArtifact{}, supplied.VisibleHousekeeping...), cleanup.visible...)
	return result
}

// createResult assembles the full Rust CreateResult field surface.
func createResult(supplied *CreateResult, state CreationState, mainIdentity, sidecarIdentity *FileIdentity, residuePossible bool, cause error) *CreateResult {
	return &CreateResult{
		AddressFamily:       supplied.AddressFamily,
		ValueKind:           supplied.ValueKind,
		StructureKind:       supplied.StructureKind,
		ValueTag:            supplied.ValueTag,
		DatabaseID:          supplied.DatabaseID,
		CommitNonce:         supplied.CommitNonce,
		SidecarID:           supplied.SidecarID,
		DirectoryIdentity:   supplied.DirectoryIdentity,
		MainBasename:        supplied.MainBasename,
		MainIdentity:        mainIdentity,
		SidecarIdentity:     sidecarIdentity,
		ReaderCapacity:      supplied.ReaderCapacity,
		State:               state,
		ResiduePossible:     residuePossible,
		Housekeeping:        supplied.Housekeeping,
		VisibleHousekeeping: supplied.VisibleHousekeeping,
		Cause:               cause,
	}
}
