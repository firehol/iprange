// Physical commit preparation and alternate-meta publication (Rust
// writer_core/publication.rs). Publish is the exact physical sequence:
// shrink-or-retain to the committed extent, flush the data pages, sync,
// write the alternate meta page into the mapping, flush it, sync again,
// then adopt the new generation as the proven-current base. The crash
// points of the Rust sequence are preserved test-only via internal/fault.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// CommitAttempt is the identity of one prepared commit (Rust
// writer_core::CommitAttempt).
type CommitAttempt struct {
	DatabaseID    [16]byte
	TransactionID uint64
	CommitNonce   [16]byte
}

// PublishStatus classifies the outcome of one publish attempt (Rust
// PublishOutcome): the error arose before any publication step (the
// committed generation is untouched), after the outcome became unknown
// (the file may have advanced), or the commit landed.
type PublishStatus uint8

const (
	PublishCommitted PublishStatus = iota
	PublishBeforePublication
	PublishOutcomeUnknown
)

// PublishResult is one publish outcome (Rust PublishOutcome).
type PublishResult struct {
	Status PublishStatus
	Err    error
}

// StartDraft begins one COW draft over the committed generation with the
// caller's nonce (Rust workflow draft installation: Draft::new over
// base.meta). The all-zero nonce is refused exactly like Rust
// random::nonzero_128 refuses an all-zero draw: the commit nonce is the
// crash-recovery operation identity and a constant zero generation is a
// class Rust's API cannot produce. The public workflows draw the nonce
// from randomNonce when they arrive.
func (c *Core) StartDraft(nonce [16]byte) error {
	if err := c.requireHealthy(); err != nil {
		return err
	}
	if c.draft != nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "a draft is already open"}
	}
	if nonce == [16]byte{} {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "commit nonce must be nonzero"}
	}
	draft, err := NewDraft(c.base.Meta, nonce)
	if err != nil {
		return err
	}
	c.draft = draft
	return nil
}

// BeginDraft starts one COW draft over the committed generation and
// draws the commit nonce (Rust WriterCore::begin_transaction: nonzero_128
// then Draft::new). The public workflows use this instead of accepting a
// caller nonce; the all-zero draw refusal of StartDraft stays in force
// (Rust random::nonzero_128 parity).
func (c *Core) BeginDraft() error {
	if err := c.requireHealthy(); err != nil {
		return err
	}
	nonce, err := randomNonce()
	if err != nil {
		return err
	}
	return c.StartDraft(nonce)
}

// Draft returns the open draft, or nil (Rust WriterCore::draft).
func (c *Core) Draft() *Draft { return c.draft }

// CommitAttempt names the pending commit while the workflow input is
// closed (Rust WriterCore::commit_attempt). The workflow-input-open gate
// is structurally closed: editor workflow states arrive with their public
// workflows. Exported for the public facade's commit orchestration.
func (c *Core) CommitAttempt() (CommitAttempt, error) {
	if err := c.requireHealthy(); err != nil {
		return CommitAttempt{}, err
	}
	if c.draft == nil || !c.draft.changed {
		return CommitAttempt{}, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	return CommitAttempt{
		DatabaseID:    c.draft.meta.DatabaseID,
		TransactionID: c.draft.meta.TxnID,
		CommitNonce:   c.draft.meta.CommitNonce,
	}, nil
}

// Prepare stages the draft for publication (Rust WriterCore::prepare):
// the draft store runs the full prepare-with-checkpoint sequence (private
// page release, bitmap shape, checksum sealing).
func (c *Core) Prepare(checkpoint func() error) error {
	if err := c.requireHealthy(); err != nil {
		return err
	}
	if err := fault.Fail("commit.prepare"); err != nil {
		return err
	}
	if c.draft == nil {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	return store.PrepareWithCheckpoint(checkpoint)
}

// RequireDraftLength verifies the locked file and the mapping both cover
// the draft's committed length (Rust WriterCore::require_draft_length:
// page_count * PAGE_SIZE, checked).
func (c *Core) RequireDraftLength() error {
	if c.draft == nil {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}
	}
	expected, err := checkedMul(c.draft.meta.PageCount, format.PageSize, "committed file length")
	if err != nil {
		return err
	}
	fileLen, err := c.m.FileSize()
	if err != nil {
		return err
	}
	if fileLen < expected || c.m.Size() < expected {
		return corrupt("draft file length is inconsistent")
	}
	return nil
}

// Publish commits the prepared draft by publishing the alternate meta
// page (Rust WriterCore::publish). On success the base becomes the new
// generation with ProvenCurrent selection and the draft is cleared; a
// failure before the meta write leaves the committed generation
// untouched, a failure after it reports OutcomeUnknown with the draft
// abandoned (no further use of this Core is safe).
func (c *Core) Publish(checkpoint func() error) PublishResult {
	if err := c.requireHealthy(); err != nil {
		return PublishResult{Status: PublishBeforePublication, Err: err}
	}
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return PublishResult{Status: PublishBeforePublication, Err: err}
		}
	}
	fault.Crash("commit.before_private_sync")
	if c.draft == nil {
		return PublishResult{Status: PublishBeforePublication, Err: &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no pending transaction"}}
	}
	committedBytes := c.draft.meta.PageCount * format.PageSize
	if err := c.m.Shrink(committedBytes); err != nil {
		return PublishResult{Status: PublishBeforePublication, Err: err}
	}
	dataOffset := uint64(2 * format.PageSize)
	if committedBytes > dataOffset {
		if err := c.m.FlushRange(dataOffset, committedBytes-dataOffset); err != nil {
			return PublishResult{Status: PublishBeforePublication, Err: err}
		}
	}
	if err := c.m.SyncFile(); err != nil {
		return PublishResult{Status: PublishBeforePublication, Err: err}
	}
	fault.Crash("commit.after_private_sync")
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return PublishResult{Status: PublishBeforePublication, Err: err}
		}
	}

	meta := c.draft.meta
	target := uint8(1) - c.base.SelectedMetaPage
	page, err := c.m.Page(uint32(target))
	if err != nil {
		return c.outcomeUnknown(err)
	}
	if err := meta.EncodeMapped(page); err != nil {
		return c.outcomeUnknown(err)
	}
	fault.Crash("commit.after_meta_write")
	if err := fault.Fail("commit.after_meta_write"); err != nil {
		return c.outcomeUnknown(err)
	}
	if err := c.m.FlushPage(uint32(target)); err != nil {
		return c.outcomeUnknown(err)
	}
	if err := c.m.SyncFile(); err != nil {
		return c.outcomeUnknown(err)
	}
	fault.Crash("commit.after_meta_sync")
	physical, err := c.m.FileSize()
	if err != nil {
		return c.outcomeUnknown(err)
	}
	c.base = bootstrap.Result{
		Meta:             meta,
		Selection:        bootstrap.SelectionProvenCurrent,
		SelectedMetaPage: target,
		CommittedBytes:   committedBytes,
		PhysicalBytes:    physical,
	}
	c.draft = nil
	c.unprovedTailEnd = nil
	return PublishResult{Status: PublishCommitted}
}

// outcomeUnknown abandons the draft after a publication step whose effect
// on the file is unknown (Rust WriterCore::outcome_unknown) and brands
// the core unusable: the durability state of the file is unknown, so
// every mutating entry point fails closed until Close.
func (c *Core) outcomeUnknown(err error) PublishResult {
	c.draft = nil
	c.unprovedTailEnd = nil
	c.unresolved = err
	return PublishResult{Status: PublishOutcomeUnknown, Err: err}
}

// checkedMul multiplies with the Rust ArithmeticOverflow class.
func checkedMul(a, b uint64, what string) (uint64, error) {
	if a != 0 && b > ^uint64(0)/a {
		return 0, overflow(what)
	}
	return a * b, nil
}
