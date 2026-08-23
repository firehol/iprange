// Package snapshot implements the compact unsigned snapshot of one pinned
// v4 generation (Rust iprange-livedb/src/snapshot/{api.rs,build.rs,
// source.rs,terminal.rs}). The machine opens the source (the immutable
// reader for the immutable mode, the registered live source guard for the
// live mode), preserves the source identity verbatim, copies all logical
// content into one private output under the caller's heap/page/open
// budget, proves the source generation survived the build, then publishes.
// Production code is mmap-only: the copy moves mapped views into the
// writer-owned mapping, and no complete page ever exists in owned memory.
package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// SourceMode is the snapshot source coordination (Rust SnapshotSourceMode).
type SourceMode uint8

const (
	// SourceImmutable snapshots the committed generation under a shared
	// lifetime lock, exactly like the immutable reader.
	SourceImmutable SourceMode = iota
	// SourceLive pins a live database generation through the sidecar
	// coordination (one claimed reader slot, released in the Rust order).
	SourceLive
)

// Budget bounds one snapshot construction (Rust SnapshotBudget).
type Budget struct {
	MaxHeapBytes   uint64
	MaxOutputPages uint64
	MaxOpenFiles   uint32
}

// Validate mirrors Rust SnapshotBudget::validate: at least two output
// pages, and the open-file budget must cover the source plus the private
// attempt (two files) with a third file for the coordination artifact of
// the replace policies and live mode. Every refusal carries the
// Rust-verbatim InsufficientResourceBudget detail.
func (b *Budget) Validate(mode SourceMode, policy writer.PublicationPolicy) error {
	if b.MaxOutputPages < 2 {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "snapshot output pages"}
	}
	required := uint32(2)
	if mode == SourceLive || policy == writer.PolicyReplaceExisting || policy == writer.PolicyReplaceExistingNoRollback {
		required = 3
	}
	if b.MaxOpenFiles < required {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "snapshot open files"}
	}
	return nil
}

// Failure is one preparation failure (Rust SnapshotPreparationFailure
// collapsed to the Go-visible terminal, the AlgebraPreparationFailure
// precedent): the primary cause plus the cleanup state of the private
// attempt. A failed source release maps to CleanupStateResiduePossible,
// the Go projection of the Rust coordination_cleanup CleanupGuard state
// (the retryable source guard itself is not carried; recovery ports it
// when its surface needs it).
type Failure struct {
	Cause   error
	Cleanup writer.CleanupState
}

// source is one opened snapshot source (Rust recovery/source_guard.rs
// Source over the Basic/Live variants): the opened generation pins its
// identity and holds its coordination until the terminal release.
type source interface {
	Meta() format.Meta
	FileIdentity() (device uint64, inode uint64, err error)
	Core() *reader.ImmutableReader
	// finishCurrent runs the Rust Source::finish_current fold: the
	// final check, the unconditional single release, and the terminal
	// (cause plus residue). The caller must not release again; every
	// pre-finish failure uses the openSource release function (Rust
	// release_only through fail_source).
	finishCurrent(check func() error) sourceEnd
}

// sourceEnd is the factual terminal of one source release (Rust
// SourceEnd): the folded cause and whether coordination residue is
// still possible.
type sourceEnd struct {
	cause   error
	residue bool
}

// To runs one compact snapshot (Rust api::snapshot_to): the live
// coordination refusal, budget validation, the source open before the
// destination create, the live self-replacement refusal, the identity
// compare, the bounded logical copy, the source final check and release
// before the publish rename, and the publish mapping to the terminal
// shapes. check is the cancellation checkpoint (nil means
// uncancellable).
func To(sourcePath string, mode SourceMode, destinationPath string, policy writer.PublicationPolicy, budget *Budget, check func() error) (*writer.PublicationResult, *Failure) {
	// api.rs refuses the live source before the platform machine, hence
	// before budget validation, on platforms without proven
	// coordination (require_live_supported). openSource repeats the
	// check before any path access for direct machine callers.
	if mode == SourceLive {
		if err := live.CheckSupported(); err != nil {
			return nil, &Failure{Cause: err}
		}
	}
	if err := budget.Validate(mode, policy); err != nil {
		return nil, &Failure{Cause: err}
	}
	// Source first (api.rs open_source before publication::workflow::
	// create): the opened generation pins its identity and holds its
	// coordination before any destination artifact exists.
	src, fail, release := openSource(sourcePath, mode, check)
	if fail != nil {
		return nil, fail
	}
	// failSource folds one failing pre-finish step (Rust fail_source):
	// the source releases without the final check, and a failed release
	// reports residue possible. After finishCurrent the source is never
	// released again (Rust finish_current already released it; the
	// carried guard projects to the cleanup classification).
	failSource := func(cause error, cleanup writer.CleanupState) (*writer.PublicationResult, *Failure) {
		// Rust fail_source keeps the primary cause pure and surfaces a
		// failed release only through the cleanup guard; the residue
		// classification is the Go projection of that guard.
		end := release()
		if end.cause != nil && cause == nil {
			cause = end.cause
		}
		if end.residue {
			cleanup = writer.CleanupStateResiduePossible
		}
		return nil, &Failure{Cause: cause, Cleanup: cleanup}
	}
	// api.rs rejects a live snapshot that would replace its own source
	// path, after the source open and before the destination create.
	if err := rejectLiveSelf(src, mode, destinationPath, policy); err != nil {
		return failSource(err, writer.CleanupStateClean)
	}
	// A pre-cancelled snapshot refuses before any destination artifact
	// exists (Rust source_guard lock_file_cancellable refuses at the
	// source-open cancellation lock): the attempt is never created, so
	// there is nothing to discard.
	if err := checkCancellation(check); err != nil {
		return failSource(err, writer.CleanupStateClean)
	}
	attempt, err := writer.CreateAttempt(destinationPath, policy)
	if err != nil {
		return failSource(err, writer.CleanupStateClean)
	}
	// The output identity is preserved from the source meta verbatim
	// (GenerationReader::output_spec): database id, transaction id, and
	// commit nonce survive the snapshot, unlike the fresh identity of
	// the algebra publish_set.
	meta := src.Meta()
	spec := writer.OutputSpec{
		AddressFamily:  meta.AddressFamily,
		ValueKind:      meta.ValueKind,
		StructureKind:  meta.StructureKind,
		ValueTag:       meta.ValueTag,
		DatabaseID:     meta.DatabaseID,
		TxnID:          meta.TxnID,
		CommitNonce:    meta.CommitNonce,
		FeedIndexLimit: meta.FeedIndexLimit,
	}
	// Reference batches charge the snapshot heap exactly like Rust
	// Builder::new_owned_with_heap (immutable_output.rs): the membership
	// batch first for membership/structured kinds, the structure batch
	// second from the remaining heap for structured kinds. The remaining
	// heap becomes the copy budget (build.rs copy()).
	heap := budget.MaxHeapBytes
	membershipEntries := 0
	if spec.ValueKind == format.ValueKindMembership || spec.ValueKind == format.ValueKindStructured {
		membershipEntries = chargeReferenceBatch(&heap)
	}
	structureEntries := 0
	if spec.ValueKind == format.ValueKindStructured {
		structureEntries = chargeReferenceBatch(&heap)
	}
	available := &Budget{MaxHeapBytes: heap, MaxOutputPages: budget.MaxOutputPages, MaxOpenFiles: budget.MaxOpenFiles}
	builder, err := writer.NewStructuredOutputBuilder(attempt.AttemptPath(), spec, writer.OutputBudget{MaxOutputPages: budget.MaxOutputPages}, membershipEntries, structureEntries, nil)
	if err != nil {
		return failSource(err, attempt.Discard())
	}
	// Capture the attempt-file identity from the builder's own descriptor
	// (Rust CreatedOutput::create_with + attempt.identity()): every later
	// Discard is identity-guarded, and the source/private-output compare
	// has its probe.
	device, inode, err := builder.FileIdentity()
	if err != nil {
		closeErr := builder.Close()
		cleanup := attempt.Discard()
		if closeErr != nil {
			err = attachClose(err, closeErr)
		}
		return failSource(err, cleanup)
	}
	attempt.SetFileIdentity(device, inode)
	discarded := func(cause error) (*writer.PublicationResult, *Failure) {
		// Rust drops the mapped writer in every failing path; Go must
		// release the builder before the attempt discard.
		closeErr := builder.Close()
		cleanup := attempt.Discard()
		if closeErr != nil {
			cause = attachClose(cause, closeErr)
		}
		return failSource(cause, cleanup)
	}
	// abortAfterFinish folds one post-finish failure: the builder
	// closes, the attempt discards, the close error attaches, and the
	// finish residue folds to the cleanup classification. The source
	// has already been released by finishCurrent; nothing here
	// releases it again (Rust finish_current released, and the carried
	// guard projects to the cleanup state).
	abortAfterFinish := func(cause error, residue bool) (*writer.PublicationResult, *Failure) {
		closeErr := builder.Close()
		cleanup := attempt.Discard()
		if closeErr != nil {
			cause = attachClose(cause, closeErr)
		}
		if residue {
			cleanup = writer.CleanupStateResiduePossible
		}
		return nil, &Failure{Cause: cause, Cleanup: cleanup}
	}
	// api.rs compares the source identity with the private output
	// identity and refuses before any copy starts.
	srcDevice, srcInode, err := src.FileIdentity()
	if err != nil {
		return discarded(err)
	}
	if encodeIdentity(srcDevice, srcInode) == encodeIdentity(device, inode) {
		return discarded(&format.Error{Code: format.CodeInvalidArgument, Detail: "source and snapshot output identities match"})
	}
	if err := copyInto(src.Core(), builder, available, check); err != nil {
		return discarded(err)
	}
	if err := builder.Finish(); err != nil {
		return discarded(err)
	}
	// Rust finish_current: the final check and the single source release
	// run as one fold (a failing check still releases; a failing release
	// reports residue possible). A replaced path or republished
	// generation during the build refuses publication with the
	// changed-candidate class; the source is not released again.
	end := src.finishCurrent(check)
	if end.cause != nil {
		return abortAfterFinish(end.cause, end.residue)
	}
	// Rust workflow::publish re-checks cancellation at the publication
	// gate: a token cancelled during the build must not proceed to the
	// rename.
	if err := checkCancellation(check); err != nil {
		return abortAfterFinish(err, false)
	}
	result, err := writer.Publish(attempt, builder, policy)
	closeErr := builder.Close()
	if err != nil {
		var failure *writer.PublicationPreparationFailure
		if errors.As(err, &failure) {
			return nil, &Failure{Cause: attachClose(failure.Cause, closeErr), Cleanup: failure.Cleanup}
		}
		return nil, &Failure{Cause: attachClose(err, closeErr), Cleanup: writer.CleanupStateClean}
	}
	if closeErr != nil {
		// A refused or outcome-unknown publish keeps its Rust Ok
		// classification; the close failure attaches as the secondary
		// cause (publish_set precedent).
		if result.Cause != nil {
			result.Cause = attachClose(result.Cause, closeErr)
			return result, nil
		}
		return nil, &Failure{Cause: closeErr, Cleanup: writer.CleanupStateClean}
	}
	return result, nil
}

// openSource opens the snapshot source per mode (Rust api.rs
// open_source). The live mode repeats the require_live_supported
// refusal before path access (the machine ran it before budget
// validation, the Rust api.rs position); the immutable mode opens the
// ordinary reader.
func openSource(path string, mode SourceMode, check func() error) (source, *Failure, func() sourceEnd) {
	switch mode {
	case SourceImmutable:
		r, err := reader.OpenImmutable(path)
		if err != nil {
			return nil, &Failure{Cause: err}, nil
		}
		return immutableSource{reader: r, path: path}, nil, func() sourceEnd {
			if closeErr := r.Close(); closeErr != nil {
				// Rust release_only: a failed release maps through
				// cleanup_for_cause and reports residue possible.
				return sourceEnd{cause: live.CleanupForCause(closeErr), residue: true}
			}
			return sourceEnd{}
		}
	case SourceLive:
		ls, err := live.OpenLiveSourceCurrent(path, check)
		if err != nil {
			fail := &Failure{Cause: err}
			var open *live.OpenFailure
			if errors.As(err, &open) && open.Residue {
				fail.Cleanup = writer.CleanupStateResiduePossible
			}
			return nil, fail, nil
		}
		return liveSource{ls}, nil, func() sourceEnd {
			end := ls.ReleaseOnly()
			return sourceEnd{cause: end.Cause, residue: end.Residue}
		}
	default:
		return nil, &Failure{Cause: &format.Error{Code: format.CodeInvalidArgument, Detail: "snapshot source mode is invalid"}}, nil
	}
}

// liveSource adapts the registered live source guard to the snapshot
// source surface (Rust recovery/source_guard/live.rs LiveSource).
type liveSource struct {
	source *live.LiveSource
}

func (s liveSource) Meta() format.Meta { return s.source.Meta() }

func (s liveSource) FileIdentity() (device uint64, inode uint64, err error) {
	return s.source.FileIdentity()
}

func (s liveSource) Core() *reader.ImmutableReader { return s.source.Core() }

// finishCurrent proves the pinned generation survived the build and
// releases the guard (Rust LiveSource::finish_current: final_check with
// the owner, gate, paths, and slot proofs, then the slot-gate-lifetime
// release, folded to the terminal).
func (s liveSource) finishCurrent(check func() error) sourceEnd {
	end := s.source.FinishCurrent(check)
	return sourceEnd{cause: end.Cause, residue: end.Residue}
}

// immutableSource adapts the immutable reader to the snapshot source
// surface (Rust source_guard/basic.rs BasicSource).
type immutableSource struct {
	reader *reader.ImmutableReader
	path   string
}

func (s immutableSource) Meta() format.Meta { return s.reader.Meta() }

func (s immutableSource) FileIdentity() (device uint64, inode uint64, err error) {
	return s.reader.FileIdentity()
}

func (s immutableSource) Core() *reader.ImmutableReader { return s.reader }

// finishCurrent proves the immutable generation survived the build and
// releases the reader (Rust BasicSource::finish_current: the
// bind_current re-run through ConfirmUnchanged, then the mapping
// close). A close failure folds to the residue terminal like the
// source terminal() fold.
func (s immutableSource) finishCurrent(check func() error) sourceEnd {
	checked := s.reader.ConfirmUnchanged(s.path, check)
	closeErr := s.reader.Close()
	if closeErr == nil {
		return sourceEnd{cause: checked}
	}
	// Rust terminal(): a failed release keeps the check failure, or maps
	// the release error through cleanup_for_cause when the check was
	// clean, and reports residue possible.
	cause := checked
	if cause == nil {
		cause = live.CleanupForCause(closeErr)
	}
	return sourceEnd{cause: cause, residue: true}
}

// rejectLiveSelf refuses a live snapshot that would replace its own
// source path (Rust api.rs reject_live_self): the check runs only for
// the live mode with a replace policy, binds the destination main name,
// and compares the current destination inode with the source identity.
// A missing or non-regular destination is not a rejection; the
// destination creation reports it with the exact publication class.
func rejectLiveSelf(src source, mode SourceMode, destinationPath string, policy writer.PublicationPolicy) error {
	if mode != SourceLive ||
		(policy != writer.PolicyReplaceExisting && policy != writer.PolicyReplaceExistingNoRollback) {
		return nil
	}
	// Rust Destination::bind validates the destination main name before
	// any filesystem access (path::validate_main_name plus
	// require_name_lengths); the writer's CreateAttempt applies the
	// same rules at the attempt creation.
	if !writer.ValidDestinationName(destinationPath) {
		return &format.Error{Code: format.CodeNameInvalid, Detail: "invalid destination name"}
	}
	clean := filepath.Clean(destinationPath)
	dir := filepath.Dir(clean)
	// Rust Destination::bind -> Directory::open proves the parent is a
	// plain directory before any namespace operation; the class mapping
	// is platform-split (writer.CheckPublicationParent): POSIX folds
	// ELOOP and ENOTDIR into the IO class, Windows keeps the
	// NotDirectory Conflict arm for non-directory and reparse-point
	// parents.
	if err := writer.CheckPublicationParent(dir); err != nil {
		return err
	}
	parentDevice, _, err := directoryIdentityOf(dir)
	if err != nil {
		return err
	}
	// The destination main name is opened without following symlinks
	// (Rust Directory::open_regular, read-only). An absent name is not
	// a rejection; the attempt creation reports it with the exact
	// publication class.
	dst, err := os.Lstat(clean)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	if !dst.Mode().IsRegular() {
		return &format.Error{Code: format.CodeConflict, Detail: "publication name is not a regular file"}
	}
	file, err := openDestinationNoFollow(clean)
	if err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	device, inode, err := fileIdentityOf(file)
	if err != nil {
		file.Close()
		return err
	}
	links, err := fileLinksOf(file)
	file.Close()
	if err != nil {
		return err
	}
	// Rust regular_identity: the destination must live on the parent
	// filesystem and carry exactly one link (a hard-linked destination
	// is a namespace mutation the exchange retirement would strand).
	if device != parentDevice {
		return &format.Error{Code: format.CodePublicationUnsupported, Detail: "publication inode is on another filesystem"}
	}
	if links != 1 {
		return &format.Error{Code: format.CodeConflict, Detail: "publication inode link count changed"}
	}
	srcDevice, srcInode, err := src.FileIdentity()
	if err != nil {
		return err
	}
	if device == srcDevice && inode == srcInode {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "a live snapshot cannot replace its own source path"}
	}
	return nil
}

// encodeIdentity renders the 32-byte Rust identity encoding of one
// (device, inode) pair (publication/namespace_identity.rs): little-endian
// device, little-endian inode, sixteen zero padding bytes.
func encodeIdentity(device, inode uint64) [32]byte {
	var bytes [32]byte
	format.PutU64(bytes[0:8], device)
	format.PutU64(bytes[8:16], inode)
	return bytes
}

// attachClose attaches a cleanup-side close error to the primary cause
// (the publish_set mergeErrors shape: the primary stays the
// errors.As/Is/Unwrap target with the secondary present in the message
// only).
func attachClose(primary error, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	if primary == nil {
		return closeErr
	}
	return fmt.Errorf("%w; %v", primary, closeErr)
}
