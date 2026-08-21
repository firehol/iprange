// Package snapshot implements the compact unsigned snapshot of one pinned
// v4 generation (Rust iprange-livedb/src/snapshot/{api.rs,build.rs,
// source.rs,terminal.rs}). The machine opens the immutable source through
// the ordinary reader, preserves the source identity verbatim, copies all
// logical content into one private output under the caller's heap/page/open
// budget, proves the source generation survived the build, then publishes.
// Production code is mmap-only: the copy moves mapped views into the
// writer-owned mapping, and no complete page ever exists in owned memory.
package snapshot

import (
	"errors"
	"fmt"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// SourceMode is the snapshot source coordination (Rust SnapshotSourceMode).
type SourceMode uint8

const (
	// SourceImmutable snapshots the committed generation under a shared
	// lifetime lock, exactly like the immutable reader.
	SourceImmutable SourceMode = iota
	// SourceLive would pin a live database generation through the sidecar
	// coordination; the Go boundary refuses it until Milestone 4.
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
// attempt. The Rust live-only coordination fields (source guard,
// coordination cleanup, housekeeping) are always empty while the live
// source is refused and are documented, not carried.
type Failure struct {
	Cause   error
	Cleanup writer.CleanupState
}

// To runs one compact snapshot (Rust api::snapshot_to): the live refusal,
// budget validation, the source open before the destination create, the
// identity compare, the bounded logical copy, the source final check and
// release before the publish rename, and the publish mapping to the
// terminal shapes. check is the cancellation checkpoint (nil means
// uncancellable).
func To(sourcePath string, mode SourceMode, destinationPath string, policy writer.PublicationPolicy, budget *Budget, check func() error) (*writer.PublicationResult, *Failure) {
	// api.rs refuses the live source in the API layer, before the
	// platform machine and therefore before budget validation, with the
	// Unsupported class; the Go port has one platform and refuses at the
	// same position. reject_live_self is unreachable and not ported.
	if mode == SourceLive {
		return nil, &Failure{Cause: &format.Error{Code: format.CodeOSUnsupported, Detail: "live snapshot source requires the live sidecar coordination (Milestone 4)"}}
	}
	if err := budget.Validate(mode, policy); err != nil {
		return nil, &Failure{Cause: err}
	}
	// Source first (api.rs open_source before publication::workflow::
	// create): the opened generation pins its identity and holds the
	// shared lifetime lock before any destination artifact exists, so a
	// snapshot can never publish over a source it did not prove.
	source, err := reader.OpenImmutable(sourcePath)
	if err != nil {
		return nil, &Failure{Cause: err}
	}
	released := false
	fail := func(cause error, cleanup writer.CleanupState) (*writer.PublicationResult, *Failure) {
		// fail_source releases the reader; a close failure attaches as
		// the secondary cause (Rust release_only is infallible, Go's
		// mapping close is not).
		if !released {
			if closeErr := source.Close(); closeErr != nil {
				cause = attachClose(cause, closeErr)
			}
			released = true
		}
		return nil, &Failure{Cause: cause, Cleanup: cleanup}
	}
	attempt, err := writer.CreateAttempt(destinationPath, policy)
	if err != nil {
		return fail(err, writer.CleanupStateClean)
	}
	// The output identity is preserved from the source meta verbatim
	// (GenerationReader::output_spec): database id, transaction id, and
	// commit nonce survive the snapshot, unlike the fresh identity of
	// the algebra publish_set.
	meta := source.Meta()
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
		return fail(err, attempt.Discard())
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
		return fail(err, cleanup)
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
		return fail(cause, cleanup)
	}
	// api.rs compares the source identity with the private output
	// identity and refuses before any copy starts.
	srcDevice, srcInode, err := source.FileIdentity()
	if err != nil {
		return discarded(err)
	}
	if encodeIdentity(srcDevice, srcInode) == encodeIdentity(device, inode) {
		return discarded(&format.Error{Code: format.CodeInvalidArgument, Detail: "source and snapshot output identities match"})
	}
	if err := copyInto(source, builder, available, check); err != nil {
		return discarded(err)
	}
	if err := builder.Finish(); err != nil {
		return discarded(err)
	}
	// Source final check between builder finish and publish (Rust
	// finish_current): a replaced path or republished generation during
	// the build refuses publication with the changed-candidate class.
	if err := source.ConfirmUnchanged(sourcePath); err != nil {
		return discarded(err)
	}
	// Release the source before the publish rename (Rust finish_current
	// drops the guard after the final check).
	if err := source.Close(); err != nil {
		released = true
		return discarded(err)
	}
	released = true
	// Rust workflow::publish re-checks cancellation at the publication
	// gate: a token cancelled during the build must not proceed to the
	// rename.
	if check != nil {
		if err := check(); err != nil {
			return nil, &Failure{Cause: err, Cleanup: attempt.Discard()}
		}
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
			if merged := attachClose(result.Cause, closeErr); merged != nil {
				result.Cause = merged
			}
			return result, nil
		}
		return nil, &Failure{Cause: closeErr, Cleanup: writer.CleanupStateClean}
	}
	return result, nil
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
