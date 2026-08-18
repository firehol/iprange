// Draft is one unpublished COW transaction over the opened committed
// generation (Rust draft_store.rs Draft). It owns the evolving meta and the
// private-page bookkeeping: the private-page stack, the dirty-page chain
// head and charge, the allocator-retired backlog, and the growth charge for
// one transaction, plus the range-tree privacy flag. The membership and
// structure delta state machines of the Rust Draft arrive with their edit
// cores.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// Draft is one unpublished COW transaction over the committed generation.
type Draft struct {
	base format.Meta
	meta format.Meta

	// privateHead is the LIFO of claimed pages discarded by the edit
	// core for reuse (Rust private_head).
	privateHead uint32
	// dirtyHead is the head of the dirty-page chain: every privately
	// claimed page is linked through its checksum slot until prepare
	// seals the data pages (Rust dirty_head).
	dirtyHead uint32
	// allocatorRetired is the backlog of bitmap COW victims that must be
	// retired after the current bitmap edit (Rust allocator_retired).
	allocatorRetired tree.RetiredPages
	// privatePages is the number of dirty-chain entries (the private
	// page budget charge; Rust private_pages).
	privatePages uint64
	// growthPages is the number of file-tail pages claimed this
	// transaction (Rust growth_pages).
	growthPages uint64
	// changed reports whether the draft mutated persistent content
	// (Rust Draft::changed; set by the edit workflows and prepare).
	changed bool
	// rangeTreePrivate reports the range tree is draft-private (Rust
	// Draft::range_tree_private; true when the committed base has no
	// range tree). Public range edits on a private tree take the gap
	// path; edits over a committed tree COW it.
	rangeTreePrivate bool
}

// NewDraft starts one draft over base with the next transaction ID and the
// caller's commit nonce (Rust Draft::new). The transaction ID is checked
// for exhaustion exactly like the Rust checked_add.
func NewDraft(base format.Meta, nonce [16]byte) (*Draft, error) {
	meta := base
	if meta.TxnID == ^uint64(0) {
		return nil, overflow("transaction ID")
	}
	meta.TxnID++
	meta.CommitNonce = nonce
	return &Draft{base: base, meta: meta, rangeTreePrivate: base.RangeRoot == 0}, nil
}

// Base returns the committed base generation the draft edits.
func (d *Draft) Base() format.Meta { return d.base }

// Meta returns the evolving draft meta; the caller publishes it when the
// draft commits.
func (d *Draft) Meta() format.Meta { return d.meta }

// Changed reports whether the draft mutated persistent content.
func (d *Draft) Changed() bool { return d.changed }
