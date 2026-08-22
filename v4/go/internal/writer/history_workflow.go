// One prepared history projection binding: the plan and the running
// merge owned together for the public lifecycle (Rust
// project_history_state: prepare then begin then per-range push then
// finish). The binding holds the unexported plan and merge across the
// public facade calls, and the push/finish entry points bind the
// stateless DraftStore inline like the Core edit entry points
// (ops.go), so the per-record drive loop never allocates a closure.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// HistoryProjection is one running projection over the open membership
// workflow draft (Rust HistoryPlan + HistoryMerge bound for a
// projection). It is not safe to use after Finish, and the surrounding
// writer owns the draft until the public workflow discards it or
// finishes the membership workflow. The draft store is bound once at
// begin and reused by every push and the finish: it reads the draft meta
// live, so one instance serves the whole projection without per-record
// allocation (Rust DraftStore is one handle per projection).
type HistoryProjection struct {
	core  *Core
	store *DraftStore
	merge *historyMerge
}

// BeginHistoryProjection prepares the destination feeds and starts the
// projection merge on the open membership workflow draft (Rust
// WriterEdit::prepare_history_from + begin_history over the draft that
// start_feed_workflow_draft created). The draft must already exist; the
// public facade checks that before calling (Rust mutate never starts a
// plain transaction here because the workflow draft is open).
func (c *Core) BeginHistoryProjection(windows []HistoryWindow, check func() error) (*HistoryProjection, error) {
	if err := c.requireHealthy(); err != nil {
		return nil, err
	}
	if c.draft == nil {
		return nil, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	plan, err := prepareHistoryPlan(store, windows, check)
	if err != nil {
		return nil, err
	}
	merge, err := plan.begin(store, c.base.Meta, check)
	if err != nil {
		return nil, err
	}
	return &HistoryProjection{core: c, store: store, merge: merge}, nil
}

// Push4 feeds one inclusive IPv4 source range into the projection merge
// (Rust WriterEdit::push_history over Ipv4Key; the last-seen value is
// the range value).
func (p *HistoryProjection) Push4(from, to uint32, lastSeen uint32, check func() error) error {
	if err := p.core.requireHealthy(); err != nil {
		return err
	}
	if p.core.draft == nil {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	return p.merge.push(p.store, tree.Key{Hi: uint64(from)}, tree.Key{Hi: uint64(to)}, lastSeen, check)
}

// Push6 feeds one inclusive IPv6 source range into the projection merge
// (Rust WriterEdit::push_history over Ipv6Key; the last-seen value is
// the range value).
func (p *HistoryProjection) Push6(fromHi, fromLo, toHi, toLo uint64, lastSeen uint32, check func() error) error {
	if err := p.core.requireHealthy(); err != nil {
		return err
	}
	if p.core.draft == nil {
		return &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	return p.merge.push(p.store, tree.Key{Hi: fromHi, Lo: fromLo}, tree.Key{Hi: toHi, Lo: toLo}, lastSeen, check)
}

// Finish ends the projection merge and assembles the projection report
// (Rust WriterEdit::finish_history). The merge is consumed; the caller
// then discards the draft for a no-change report or finishes the
// membership workflow for a changed report.
func (p *HistoryProjection) Finish(sourceRangeCount uint64, sourceAddresses format.Cardinality129, check func() error) (*HistoryProjectionReport, error) {
	if err := p.core.requireHealthy(); err != nil {
		return nil, err
	}
	if p.core.draft == nil {
		return nil, &format.Error{Code: format.CodeNoPendingTransaction, Detail: "no changed transaction is pending"}
	}
	return p.merge.finish(p.store, check, sourceRangeCount, sourceAddresses)
}
