// High-offset mapped workspace inside one private final output inode
// (Rust immutable_output/unordered/workspace.rs): the workspace shares
// the attempt inode with the append-only output builder, owns the page
// range [first, limit) after the output pages, and allocates from a
// FREE_MAGIC free list or the tail. All views alias the mapping; no
// complete page ever exists in owned memory (the mmap-only contract).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Wire markers of one workspace free-list cell (Rust workspace.rs
// FREE_MAGIC / FREE_NEXT / FREE_TXN): the page head carries the magic,
// the next free page, and the workspace transaction.
const (
	workspaceFreeMagic = uint32(0x57465245)
	workspaceFreeNext  = 4
	workspaceFreeTxn   = 8
)

// immutableWorkspace is the mapped page provider of the normalized
// feed tree inside the private attempt inode (Rust Workspace): the
// store implements tree.Store plus the retiring and range surfaces so
// the generic tree core, the coverage-union input, and the ordered
// cursor drive it through the same code paths as every other store.
type immutableWorkspace struct {
	mapping *mapping.Mapping
	first   uint64
	limit   uint64
	next    uint64
	free    uint32
	txn     uint64
}

// newImmutableWorkspace binds one workspace to the read-write mapping
// of the attempt inode (Rust Workspace::new): first is the first
// workspace page (the output page budget), limit the total page count,
// and txn the output transaction (always 1).
func newImmutableWorkspace(m *mapping.Mapping, first, limit, txn uint64) (*immutableWorkspace, error) {
	if first > limit || limit*format.PageSize != m.Size() {
		return nil, invalid("immutable construction workspace is invalid")
	}
	return &immutableWorkspace{mapping: m, first: first, limit: limit, next: first, txn: txn}, nil
}

// pageCount returns the allocated high-water page count (Rust
// Workspace::page_count).
func (w *immutableWorkspace) pageCount() uint64 { return w.next }

func (w *immutableWorkspace) requireAllocated(page uint32) error {
	if uint64(page) < w.first || uint64(page) >= w.next {
		return corrupt("immutable workspace page is outside its allocation")
	}
	return nil
}

// popFree pops the free-list head cell (Rust Workspace::pop_free): the
// head must carry the free magic and this workspace's transaction, and
// the successor must be in-range when nonzero.
func (w *immutableWorkspace) popFree() (uint32, error) {
	page := w.free
	if err := w.requireAllocated(page); err != nil {
		return 0, err
	}
	next, err := w.mapping.Page(page)
	if err != nil {
		return 0, err
	}
	if format.U32(next[0:4]) != workspaceFreeMagic || format.U64(next[workspaceFreeTxn:]) != w.txn {
		return 0, corrupt("immutable workspace free link is invalid")
	}
	following := format.U32(next[workspaceFreeNext:])
	if following != 0 && (following == page || uint64(following) < w.first || uint64(following) >= w.next) {
		return 0, corrupt("immutable workspace free link is invalid")
	}
	w.free = following
	return page, nil
}

// TargetTxn returns the workspace transaction (Rust
// Store::target_txn).
func (w *immutableWorkspace) TargetTxn() uint64 { return w.txn }

// PageLimit returns the allocated high-water count (Rust
// Store::page_limit).
func (w *immutableWorkspace) PageLimit() uint64 { return w.next }

// Inspect returns one mapped workspace page view (Rust
// Store::inspect_page; the view is the mapping alias, never a copy).
func (w *immutableWorkspace) Inspect(pageNumber uint32) ([]byte, error) {
	if err := w.requireAllocated(pageNumber); err != nil {
		return nil, err
	}
	return w.mapping.Page(pageNumber)
}

// Allocate returns the free-list head or the next tail page (Rust
// Store::allocate; BudgetExceeded when the workspace limit is
// reached).
func (w *immutableWorkspace) Allocate() (uint32, error) {
	if w.free != 0 {
		return w.popFree()
	}
	if w.next == w.limit {
		return 0, budgetExceeded("immutable construction workspace pages")
	}
	page := uint32(w.next)
	w.next++
	work.PageCreated(1)
	return page, nil
}

// Update returns one mapped workspace page ready for mutation (Rust
// Store::update_page). The workspace has no dirty chain: the tag is
// always zero and RestoreDirty is the matching no-op.
func (w *immutableWorkspace) Update(pageNumber uint32) ([]byte, uint32, error) {
	if err := w.requireAllocated(pageNumber); err != nil {
		return nil, 0, err
	}
	page, err := w.mapping.Page(pageNumber)
	if err != nil {
		return nil, 0, err
	}
	return page, 0, nil
}

// RestoreDirty is the no-op tag restore of the tag-free workspace
// (Rust PageMut has no dirty-chain re-arm).
func (w *immutableWorkspace) FinishEdit(page []byte, tag uint32) error {
	if tag != 0 {
		return corrupt("immutable workspace tag restore is invalid")
	}
	return nil
}

// CopyPage returns the source and destination views of one COW copy
// (Rust Store::copy_page; both pages must already be allocated).
func (w *immutableWorkspace) CopyPage(source, destination uint32) ([]byte, []byte, uint32, error) {
	if err := w.requireAllocated(source); err != nil {
		return nil, nil, 0, err
	}
	if err := w.requireAllocated(destination); err != nil {
		return nil, nil, 0, err
	}
	src, err := w.mapping.Page(source)
	if err != nil {
		return nil, nil, 0, err
	}
	dst, err := w.mapping.Page(destination)
	if err != nil {
		return nil, nil, 0, err
	}
	work.PageCopied(1)
	return src, dst, 0, nil
}

// DiscardPrivate pushes one workspace page onto the free list (Rust
// Store::discard_private): the page must be allocated and born in this
// workspace's transaction, and a double discard is refused.
func (w *immutableWorkspace) DiscardPrivate(pageNumber uint32) error {
	if err := w.requireAllocated(pageNumber); err != nil {
		return err
	}
	if pageNumber == w.free {
		return corrupt("immutable workspace page was discarded twice")
	}
	page, err := w.mapping.Page(pageNumber)
	if err != nil {
		return err
	}
	if format.U64(page[format.HeaderBorn:]) != w.txn {
		return corrupt("immutable workspace discarded a foreign page")
	}
	format.PutU32(page[0:4], workspaceFreeMagic)
	format.PutU32(page[workspaceFreeNext:], w.free)
	format.PutU64(page[workspaceFreeTxn:], w.txn)
	work.BytesMoved(16) // Rust discard_private: magic + next + txn puts
	w.free = pageNumber
	return nil
}

// RetirePages discards every retired page (Rust
// RetiringStore::retire_pages).
func (w *immutableWorkspace) RetirePages(retired tree.RetiredPages) error {
	for _, page := range retired.Slice() {
		if err := w.DiscardPrivate(page); err != nil {
			return err
		}
	}
	return nil
}

// RangeRecordAdded is the untracked coverage accounting no-op (Rust
// range_mutation::RangeStore for the workspace).
func (w *immutableWorkspace) RangeRecordAdded(value uint32) error { return nil }

// RangeRecordRemoved is the untracked coverage accounting no-op (Rust
// range_mutation::RangeStore for the workspace).
func (w *immutableWorkspace) RangeRecordRemoved(value uint32) error { return nil }
