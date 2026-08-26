// Exact two-meta commit-outcome resolution (Rust
// commit_resolution.rs): resolve_commit proves one exact attempted
// transaction and nonce against the two meta pages without validating
// either page graph, in Live or Immutable coordination mode, and
// trims only the unpublished tail of the selected generation. The
// open takes the shared main lifetime lock; the Live mode additionally
// gates the ready reader table of the attempted database, claims the
// writer lease, scans the reader slots against the selected
// generation, synchronizes the file, and proves the selection stable
// across the sync before trimming. Persistent bytes are read only
// through temporary read-only views of the two meta pages; no
// complete page ever exists in owned memory.

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// CommitResolutionMode is the coordination mode used while proving
// one commit attempt (Rust CommitResolutionMode).
type CommitResolutionMode uint8

const (
	CommitResolutionModeLive CommitResolutionMode = iota
	CommitResolutionModeImmutable
)

// LocalFileRelation is the relation between the attempted and
// inspected local files (Rust LocalFileRelation).
type LocalFileRelation uint8

const (
	LocalFileRelationSameLocalFile LocalFileRelation = iota
	LocalFileRelationDifferentLocalFile
)

// CommitResolution is the exact durability classification of one
// attempted transaction and nonce (Rust CommitResolution).
type CommitResolution uint8

const (
	CommitResolutionCommitted CommitResolution = iota
	CommitResolutionNotCommitted
	CommitResolutionSupersededUnknown
	CommitResolutionUnresolvable
)

// CommitResolutionResult is the factual identities and classification
// returned by commit resolution (Rust CommitResolutionResult).
type CommitResolutionResult struct {
	AttemptedDatabaseID     [16]byte
	AttemptedTransactionID  uint64
	AttemptedCommitNonce    [16]byte
	ActualDirectoryIdentity FileIdentity
	ActualMainIdentity      FileIdentity
	LocalFileRelation       LocalFileRelation
	Resolution              CommitResolution
	Cleanup                 CommitCleanupArtifacts
	CoordinationCleanup     CoordinationCleanup
	Cause                   error
}

// openedCommitFile is the resolved open facts of the inspected main
// file (Rust commit_resolution::Opened).
type openedCommitFile struct {
	file              *os.File
	identity          FileIdentity
	directoryIdentity FileIdentity
	mainIdentity      FileIdentity
	mainBasename      LocalBasename
}

// commitClassification is the selected generation plus the exact
// outcome of one attempted commit (Rust commit_resolution::
// Classification).
type commitClassification struct {
	resolution            CommitResolution
	selectedTransactionID uint64
	selectedCommitNonce   [16]byte
	selectedDatabaseID    [16]byte
	committedBytes        uint64
	physicalBytes         uint64
}

// ResolveCommit resolves one exact commit attempt without validating
// either page graph (Rust resolve_commit). Live mode requires the
// live coordination surface and the attempted database's ready
// reader table; Immutable mode requires the canonical sidecar to be
// absent. The main file is opened read-write, the shared main
// lifetime lock is held for the operation, and every release failure
// of the postcondition chain is folded into the returned result.
func ResolveCommit(path string, attempt *LiveCommitResult, mode CommitResolutionMode, check func() error) (*CommitResolutionResult, error) {
	if mode == CommitResolutionModeLive {
		if err := requireLiveSupported(); err != nil {
			return nil, err
		}
	}
	if err := validateCommitAttempt(attempt); err != nil {
		return nil, err
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	opened, err := openForCommitResolution(path, mode, check)
	if err != nil {
		return nil, err
	}
	relation := commitFileRelation(attempt, opened)
	switch mode {
	case CommitResolutionModeImmutable:
		sidecarPath, err := canonicalSidecarPath(path)
		if err != nil {
			opened.file.Close()
			return nil, err
		}
		if err := requireSidecarAbsent(sidecarPath); err != nil {
			opened.file.Close()
			return nil, err
		}
		result, err := resolveCommitLocked(path, attempt, opened, nil, check, relation)
		if err != nil {
			opened.file.Close()
			return nil, err
		}
		if err := UnlockFile(opened.file, MainLifetimeOffset); err != nil {
			recordPostconditionFailure(result, err)
		}
		opened.file.Close()
		return result, nil
	default: // CommitResolutionModeLive
		sidecar, err := open(path, attempt.AttemptedDatabaseID)
		if err != nil {
			opened.file.Close()
			return nil, err
		}
		if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
			sidecar.Close()
			opened.file.Close()
			return nil, err
		}
		if err := sidecar.claimWriter(); err != nil {
			// Rust: combine the claim cause with the gate release and
			// return; the sidecar descriptor close releases any lock the
			// release could not remove.
			combined := combineErrors(err, sidecar.unlockGate())
			sidecar.Close()
			opened.file.Close()
			return nil, combined
		}
		result, err := resolveCommitLocked(path, attempt, opened, sidecar, check, relation)
		if err != nil {
			// Rust: the sidecar and main descriptors drop, releasing the
			// gate, the writer lease, and the lifetime lock.
			sidecar.Close()
			opened.file.Close()
			return nil, err
		}
		for _, release := range []error{
			sidecar.releaseWriter(),
			sidecar.unlockGate(),
			UnlockFile(opened.file, MainLifetimeOffset),
		} {
			if release != nil {
				recordPostconditionFailure(result, release)
			}
		}
		sidecar.Close()
		opened.file.Close()
		return result, nil
	}
}

// validateCommitAttempt refuses an incomplete commit result (Rust
// validate_attempt: a zero database id, zero transaction id, or zero
// commit nonce is InvalidArgument).
func validateCommitAttempt(attempt *LiveCommitResult) error {
	if attempt.AttemptedDatabaseID == [16]byte{} ||
		attempt.AttemptedTransactionID == 0 ||
		attempt.AttemptedCommitNonce == [16]byte{} {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "commit result is incomplete"}
	}
	return nil
}

// openForCommitResolution opens the main file read-write, captures
// the retained identity with the mode's link rule, takes the shared
// main lifetime lock, verifies the path, and retains the directory
// identity and basename (Rust commit_resolution::open).
func openForCommitResolution(path string, mode CommitResolutionMode, check func() error) (*openedCommitFile, error) {
	file, _, err := openRw(path)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*openedCommitFile, error) {
		file.Close()
		return nil, err
	}
	var identity FileIdentity
	switch mode {
	case CommitResolutionModeLive:
		identity, err = identityOf(file)
	default:
		identity, err = IdentityAnyLink(file)
	}
	if err != nil {
		return fail(err)
	}
	if err := LockFileCancellable(file, MainLifetimeOffset, LockShared, check); err != nil {
		return fail(err)
	}
	if err := commitVerifyMain(path, identity, mode); err != nil {
		return fail(err)
	}
	directoryIdentity, err := parentIdentity(path)
	if err != nil {
		return fail(err)
	}
	basename, err := localBasenameFromPath(path)
	if err != nil {
		return fail(err)
	}
	return &openedCommitFile{
		file:              file,
		identity:          identity,
		directoryIdentity: directoryIdentity,
		mainIdentity:      identity,
		mainBasename:      basename,
	}, nil
}

// commitVerifyMain re-verifies that path names the retained identity
// with the mode's link rule (Rust commit_resolution::verify_main).
func commitVerifyMain(path string, identity FileIdentity, mode CommitResolutionMode) error {
	switch mode {
	case CommitResolutionModeLive:
		return verifyPath(path, identity)
	default:
		return VerifyPathAnyLink(path, identity)
	}
}

// commitFileRelation reports whether the attempted local file is the
// inspected one (Rust commit_resolution::relation).
func commitFileRelation(attempt *LiveCommitResult, opened *openedCommitFile) LocalFileRelation {
	if attempt.DirectoryIdentity == opened.directoryIdentity &&
		attempt.MainIdentity == opened.mainIdentity {
		return LocalFileRelationSameLocalFile
	}
	return LocalFileRelationDifferentLocalFile
}

// resolveCommitLocked runs the classify-sync-classify proof under the
// held locks (Rust commit_resolution::resolve_locked): the attempt is
// classified twice around a file sync, the selected generation must be
// stable, the main path and directory identity are re-proven, the
// sidecar (Live) is scanned and verified or its absence (Immutable) is
// re-proven, and only the unpublished tail is trimmed.
func resolveCommitLocked(path string, attempt *LiveCommitResult, opened *openedCommitFile, sidecar *Sidecar, check func() error, relation LocalFileRelation) (*CommitResolutionResult, error) {
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	first, err := classifyCommit(opened.file, attempt)
	if err != nil {
		return unresolvableCommit(attempt, opened, relation, err), nil
	}
	if err := RequireMainAvailable(path, opened.identity, first.selectedDatabaseID); err != nil {
		return nil, err
	}
	if sidecar != nil {
		if err := sidecar.scanAtMostCancellable(first.selectedTransactionID, check); err != nil {
			return nil, err
		}
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	if err := fileSync(opened.file); err != nil {
		return unresolvableCommit(attempt, opened, relation, err), nil
	}
	if err := checkpoint(check); err != nil {
		return nil, err
	}
	second, err := classifyCommit(opened.file, attempt)
	if err != nil {
		return unresolvableCommit(attempt, opened, relation, err), nil
	}
	if first != second {
		return unresolvableCommit(attempt, opened, relation, &format.Error{
			Code:   format.CodeWrongState,
			Detail: "selected generation changed during resolution",
		}), nil
	}
	mode := commitSidecarMode(sidecar)
	if err := commitVerifyMain(path, opened.identity, mode); err != nil {
		return nil, err
	}
	directoryIdentity, err := parentIdentity(path)
	if err != nil {
		return nil, err
	}
	if directoryIdentity != opened.directoryIdentity {
		return unresolvableCommit(attempt, opened, relation, &format.Error{
			Code: format.CodeDirectoryIdentityMismatch,
		}), nil
	}
	if sidecar != nil {
		if err := sidecar.verifyPath(); err != nil {
			return nil, err
		}
		if err := sidecar.verifyHeader(); err != nil {
			return nil, err
		}
	} else {
		sidecarPath, err := canonicalSidecarPath(path)
		if err != nil {
			return nil, err
		}
		if err := requireSidecarAbsent(sidecarPath); err != nil {
			return nil, err
		}
	}
	return resolveCommitTail(path, attempt, opened, sidecar, relation, first), nil
}

// classifyCommit classifies one attempted commit against a temporary
// read-only view of the two meta pages (Rust commit_resolution::
// classify): the writer-mode selected generation plus the exact
// commit-outcome classification.
func classifyCommit(file *os.File, attempt *LiveCommitResult) (commitClassification, error) {
	physical, err := filePhysical(file)
	if err != nil {
		return commitClassification{}, err
	}
	view, err := mapping.MapFile(file, 2*format.PageSize, false)
	if err != nil {
		return commitClassification{}, err
	}
	defer view.Close()
	p0, err := view.Page(0)
	if err != nil {
		return commitClassification{}, err
	}
	p1, err := view.Page(1)
	if err != nil {
		return commitClassification{}, err
	}
	selected, err := bootstrap.Open(p0, p1, physical, bootstrap.ModeWriter)
	if err != nil {
		return commitClassification{}, err
	}
	resolution, err := bootstrap.ResolveCommitAttempt(
		p0, p1, physical,
		attempt.AttemptedDatabaseID,
		attempt.AttemptedTransactionID,
		attempt.AttemptedCommitNonce,
	)
	if err != nil {
		return commitClassification{}, err
	}
	return commitClassificationFrom(CommitResolution(resolution), *selected), nil
}

// classifyCommitSelected re-derives the selected generation after the
// tail cleanup, keeping the already-proven outcome (Rust
// commit_resolution::classify_selected).
func classifyCommitSelected(file *os.File, resolution CommitResolution) (commitClassification, error) {
	physical, err := filePhysical(file)
	if err != nil {
		return commitClassification{}, err
	}
	view, err := mapping.MapFile(file, 2*format.PageSize, false)
	if err != nil {
		return commitClassification{}, err
	}
	defer view.Close()
	p0, err := view.Page(0)
	if err != nil {
		return commitClassification{}, err
	}
	p1, err := view.Page(1)
	if err != nil {
		return commitClassification{}, err
	}
	selected, err := bootstrap.Open(p0, p1, physical, bootstrap.ModeWriter)
	if err != nil {
		return commitClassification{}, err
	}
	return commitClassificationFrom(CommitResolution(resolution), *selected), nil
}

// commitClassificationFrom builds the classification facts of one
// selected generation (Rust commit_resolution::classification).
func commitClassificationFrom(resolution CommitResolution, selected bootstrap.Result) commitClassification {
	return commitClassification{
		resolution:            resolution,
		selectedTransactionID: selected.Meta.TxnID,
		selectedCommitNonce:   selected.Meta.CommitNonce,
		selectedDatabaseID:    selected.Meta.DatabaseID,
		committedBytes:        selected.CommittedBytes,
		physicalBytes:         selected.PhysicalBytes,
	}
}

// resolveCommitTail trims only the unpublished tail of the selected
// generation (Rust commit_resolution::resolve_tail): a file whose
// physical extent equals its committed extent is already clean; a
// longer file is shrunk and re-classified, and a cleanup failure is
// recorded as the exact tail artifact with its cause.
func resolveCommitTail(path string, attempt *LiveCommitResult, opened *openedCommitFile, sidecar *Sidecar, relation LocalFileRelation, selected commitClassification) *CommitResolutionResult {
	result := resolvedCommitResult(attempt, opened, relation, selected.resolution)
	if selected.physicalBytes == selected.committedBytes {
		return result
	}
	if err := trimCommitTail(path, opened, sidecar, selected); err != nil {
		result.Cleanup = tailArtifacts(commitTailArtifact(opened, selected, errorCodeOf(err)))
		result.Cause = err
	}
	return result
}

// trimCommitTail shrinks the file to the committed extent and proves
// the selected generation is stable (Rust commit_resolution::
// trim_tail): the shrink may retain the physical extent on Windows
// while another process maps the file, the file is synchronized, the
// classification is re-proven with the retained physical extent, and
// the main path, directory identity, and sidecar state are verified
// again.
func trimCommitTail(path string, opened *openedCommitFile, sidecar *Sidecar, selected commitClassification) error {
	physicalBytes, err := mapping.ShrinkFileOrRetain(opened.file, selected.committedBytes)
	if err != nil {
		return err
	}
	if err := fileSync(opened.file); err != nil {
		return err
	}
	current, err := classifyCommitSelected(opened.file, selected.resolution)
	if err != nil {
		return err
	}
	expected := selected
	expected.physicalBytes = physicalBytes
	if current != expected {
		return &format.Error{
			Code:   format.CodeUnresolvable,
			Detail: "selected generation changed during tail cleanup",
		}
	}
	mode := commitSidecarMode(sidecar)
	if err := commitVerifyMain(path, opened.identity, mode); err != nil {
		return err
	}
	directoryIdentity, err := parentIdentity(path)
	if err != nil {
		return err
	}
	if directoryIdentity != opened.directoryIdentity {
		return &format.Error{Code: format.CodeDirectoryIdentityMismatch}
	}
	if sidecar != nil {
		if err := sidecar.verifyPath(); err != nil {
			return err
		}
		return sidecar.verifyHeader()
	}
	sidecarPath, err := canonicalSidecarPath(path)
	if err != nil {
		return err
	}
	return requireSidecarAbsent(sidecarPath)
}

// commitTailArtifact builds the exact unresolved unpublished-main-tail
// ledger entry (Rust commit_resolution::tail_artifact).
func commitTailArtifact(opened *openedCommitFile, selected commitClassification, cleanupError format.ErrorCode) CommitCleanupArtifact {
	return CommitCleanupArtifact{
		DirectoryIdentity:        opened.directoryIdentity,
		MainBasename:             opened.mainBasename,
		MainIdentity:             opened.mainIdentity,
		ExpectedDatabaseID:       selected.selectedDatabaseID,
		TargetTransactionID:      selected.selectedTransactionID,
		TargetCommitNonce:        selected.selectedCommitNonce,
		CommittedTargetLength:    selected.committedBytes,
		ObservedTailEndExclusive: &selected.physicalBytes,
		CleanupError:             cleanupError,
	}
}

// recordPostconditionFailure folds one release failure into the
// result cause (Rust record_postcondition_failure: the chain nests as
// CleanupIncomplete around the prior cause).
func recordPostconditionFailure(result *CommitResolutionResult, cleanup error) {
	if result.Cause == nil {
		result.Cause = cleanup
		return
	}
	result.Cause = &format.Error{
		Code:   format.CodeCleanupInProgress,
		Detail: result.Cause.Error() + "; cleanup also failed: " + cleanup.Error(),
	}
}

// resolvedCommitResult builds the success-shaped resolution facts of
// one attempt (Rust commit_resolution::resolved).
func resolvedCommitResult(attempt *LiveCommitResult, opened *openedCommitFile, relation LocalFileRelation, resolution CommitResolution) *CommitResolutionResult {
	return &CommitResolutionResult{
		AttemptedDatabaseID:     attempt.AttemptedDatabaseID,
		AttemptedTransactionID:  attempt.AttemptedTransactionID,
		AttemptedCommitNonce:    attempt.AttemptedCommitNonce,
		ActualDirectoryIdentity: opened.directoryIdentity,
		ActualMainIdentity:      opened.mainIdentity,
		LocalFileRelation:       relation,
		Resolution:              resolution,
		Cleanup:                 cleanArtifacts(),
		CoordinationCleanup:     CoordinationCleanupNone,
		Cause:                   nil,
	}
}

// unresolvableCommit builds an Unresolvable classification carrying
// the cause (Rust commit_resolution::unresolvable).
func unresolvableCommit(attempt *LiveCommitResult, opened *openedCommitFile, relation LocalFileRelation, cause error) *CommitResolutionResult {
	result := resolvedCommitResult(attempt, opened, relation, CommitResolutionUnresolvable)
	result.Cause = cause
	return result
}

// commitSidecarMode derives the verification mode from the sidecar
// presence (Rust commit_resolution::sidecar_mode).
func commitSidecarMode(sidecar *Sidecar) CommitResolutionMode {
	if sidecar != nil {
		return CommitResolutionModeLive
	}
	return CommitResolutionModeImmutable
}

// fileSync forces one retained file's data to stable storage with the
// plain sync of the Rust std fs::File::sync_all (the resolution owner
// syncs the whole descriptor, not a mapped prefix).
func fileSync(f *os.File) error {
	if err := f.Sync(); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "sync: " + err.Error()}
	}
	return nil
}

// filePhysical reports the physical length of one retained file
// (Rust fs::metadata len).
func filePhysical(f *os.File) (uint64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, &format.Error{Code: format.CodeIO, Detail: "stat: " + err.Error()}
	}
	return uint64(st.Size()), nil
}
