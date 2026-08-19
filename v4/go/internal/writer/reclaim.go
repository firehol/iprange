// Bounded reclamation draft preparation (Rust writer_core/reclaim.rs).
// Reclamation selects the oldest reader-safe retired transactions within
// the work limits, installs a reclamation draft, applies the selection
// physically, and prepares it for publication exactly like a normal
// commit.

package writer

import (
	"crypto/rand"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// PreparedReclamation is the installed, prepared reclamation draft (Rust
// writer_core::PreparedReclamation).
type PreparedReclamation struct {
	TransactionCount uint64
	PageCount        uint64
	Attempt          CommitAttempt
}

// PrepareReclamation selects bounded reclamation work and leaves a
// prepared draft ready for Publish (Rust WriterCore::prepare_reclamation).
// oldestReader, when non-nil, is the oldest active reader transaction;
// extents retired by that transaction or later are never reclaimed.
func (c *Core) PrepareReclamation(oldestReader *uint64, maxTransactions, maxPages uint64, checkpoint func() error) (*PreparedReclamation, error) {
	if err := c.requireHealthy(); err != nil {
		return nil, err
	}
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}
	draft, err := NewDraft(c.base.Meta, nonce)
	if err != nil {
		return nil, err
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, draft)
	selection, err := store.SelectReclamation(oldestReader, maxTransactions, maxPages, checkpoint)
	if err != nil {
		return nil, err
	}
	if selection == nil {
		return nil, nil
	}
	attempt := CommitAttempt{
		DatabaseID:    draft.meta.DatabaseID,
		TransactionID: draft.meta.TxnID,
		CommitNonce:   draft.meta.CommitNonce,
	}
	c.draft = draft
	transactionCount := selection.Transactions
	pageCount := selection.Pages
	if err := store.ApplyReclamation(selection, checkpoint); err != nil {
		return nil, err
	}
	if err := c.Prepare(checkpoint); err != nil {
		return nil, err
	}
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return nil, err
		}
	}
	return &PreparedReclamation{
		TransactionCount: transactionCount,
		PageCount:        pageCount,
		Attempt:          attempt,
	}, nil
}

// randomNonce draws one nonzero 128-bit commit nonce (Rust
// random::nonzero_128: one fill, all-zero is a hard error).
func randomNonce() ([16]byte, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nonce, &format.Error{Code: format.CodeIO, Detail: "commit nonce: " + err.Error()}
	}
	if nonce == [16]byte{} {
		return nonce, &format.Error{Code: format.CodeFormatInvalid, Detail: "operating-system randomness returned an all-zero identity"}
	}
	return nonce, nil
}
