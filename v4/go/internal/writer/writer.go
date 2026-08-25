// Package writer implements the healthy-file writer core: one owner of the
// mapped committed generation and its mutations for the Go producer,
// mirroring the Rust writer_core. It composes the mapping owner and the
// shared bootstrap authority; persistent content is mmap-only and no
// complete page ever exists in owned memory. This chunk delivers the open
// surface (map_writer / select_committed / trim_committed_tail); the COW
// edit core, publication, and reclaim arrive in later chunks of this
// milestone.
package writer

import (
	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// PageBudget declares the draft resource limits, mirroring Rust
// draft_store::PageBudget plus the live-writer open-files bound:
// MaxHeapBytes bounds owned scratch, MaxPrivatePages bounds the COW
// draft extent, MaxGrowthPages bounds the file growth one transaction
// may claim, and MaxOpenFiles bounds the descriptors one operation may
// hold (the live writer validates it at open, Rust
// TransactionBudget::validate). The core keeps the declared budget from
// open so the edit core consumes one budget for the writer's lifetime.
type PageBudget struct {
	MaxHeapBytes    uint64
	MaxPrivatePages uint64
	MaxGrowthPages  uint64
	MaxOpenFiles    uint32
}

// WriterInfo is the logical generation facts of the selected committed
// meta, mirroring Rust WriterInfo.
type WriterInfo struct {
	AddressFamily    uint8
	ValueKind        uint8
	StructureKind    uint8
	ValueTag         [16]byte
	DatabaseID       [16]byte
	TransactionID    uint64
	CommitNonce      [16]byte
	PageCount        uint64
	RangeRecordCount uint64
	ActiveFeedCount  uint64
}

// Core is the opened writer core: the exclusive-locked read-write mapping
// of the committed generation plus the declared page budget (Rust
// WriterCore). At most one unpublished COW draft exists in later chunks.
type Core struct {
	m      *mapping.Mapping
	base   bootstrap.Result
	budget PageBudget
	// draft is the open COW transaction, or nil (Rust WriterCore::draft;
	// installed by the workflows and by reclamation).
	draft *Draft
	// unprovedTailEnd remembers a tail length observed before cleanup so
	// crash-recovery tooling can reason about abandoned tails (Rust
	// WriterCore::unproved_tail_end).
	unprovedTailEnd *uint64
	// unresolved records a publication failure after the alternate meta
	// write whose durability is unknown (Rust WriterCore's
	// State::OutcomeUnknown): every mutating entry point fails closed
	// with WrongState until Close, because a retried transaction would
	// reuse the same transaction id over an abandoned commit.
	unresolved error
}

// requireHealthy fails closed after an unresolved commit outcome (Rust
// require_healthy, live_writer.rs:311): only the close family remains
// legal, mirroring WrongMode("writer has an unresolved commit outcome").
func (c *Core) requireHealthy() error {
	if c.unresolved != nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "writer has an unresolved commit outcome"}
	}
	return nil
}

// MarkUnresolved brands the core unusable after a failed abandonment
// discard (Rust abort_after_source State::Unusable): the draft state is
// unknown, so every mutating entry point fails closed until Close.
func (c *Core) MarkUnresolved(err error) {
	c.draft = nil
	c.unprovedTailEnd = nil
	c.unresolved = err
}

// Healthy reports the fail-closed state of the core (Rust
// require_healthy): nil means the committed generation is selectable
// and mutating entry points may proceed. The public workflows check it
// before classifying a request so the unresolved-commit outcome keeps
// its WrongState class ahead of any request-specific error (Rust
// require_feed_workflow_ready ordering).
func (c *Core) Healthy() error { return c.requireHealthy() }

// FileIdentity returns the device and inode of the mapped file (Rust
// OpenedMain::identity over the held descriptor). The history
// projection compares it with the source reader identity so a database
// can never project onto itself (Rust require_compatible_source).
func (c *Core) FileIdentity() (device uint64, inode uint64, err error) {
	return c.m.FileIdentity()
}

// OpenWriter maps path read-write under the exclusive lifetime lock and
// selects the committed generation with the writer rule, mirroring Rust
// WriterCore::map_writer (database_file.rs map_writer): a read-write
// two-page bootstrap mapping, the Writer-mode meta selection, then Remap
// to the committed extent, so a huge corrupt or unpublished tail never
// costs VA and never becomes writable at open. The tail itself is trimmed
// by TrimCommittedTail and the path identity is re-verified after the
// remap, mirroring Rust live_writer.open_locked's terminal verify_pair
// (the sidecar coordination of open_locked is not implemented in the
// Go live surface; the mapping owner's exclusive lifetime lock
// substitutes for the sidecar writer claim, a recorded SOW-0025
// decision).
// check, when non-nil, runs under the lifetime lock exactly like the
// reader's namespace hook and arrives before the sidecar checks of M4. On
// failure the lock and descriptor are released.
func OpenWriter(path string, budget PageBudget, check func(clean string) error) (*Core, error) {
	return openWriter(path, budget, check, mapping.OpenMutable)
}

// OpenWriterLive maps path read-write under the shared lifetime lock for
// the live writer (Rust open_main: lock_file(MAIN_LIFETIME_LOCK, Shared)
// then WriterCore::map_writer): writer exclusivity comes from the sidecar
// writer claim taken by the live writer open, not from the exclusive
// lock. The mapping, bootstrap, remap, and terminal identity checks are
// identical to OpenWriter.
func OpenWriterLive(path string, budget PageBudget, check func(clean string) error) (*Core, error) {
	return openWriter(path, budget, check, mapping.OpenMutableShared)
}

// openWriter maps path read-write under one lifetime-lock mode and runs
// the shared open sequence: two-page bootstrap mapping, remap to the
// committed extent, and the terminal path-identity re-verification.
func openWriter(path string, budget PageBudget, check func(clean string) error, open func(string, func(string) error) (*mapping.Mapping, error)) (*Core, error) {
	m, err := open(path, check)
	if err != nil {
		return nil, err
	}
	c := &Core{m: m, budget: budget}
	if err := c.rebootstrap(); err != nil {
		m.Close()
		return nil, err
	}
	if err := m.Remap(c.base.CommittedBytes); err != nil {
		m.Close()
		return nil, err
	}
	// The path must still name the opened inode (reader.go parity and
	// Rust open_locked verify_pair): a replacement during the remap
	// window must not publish a writer bound to a detached inode.
	if err := m.VerifyIdentity(path); err != nil {
		m.Close()
		return nil, err
	}
	return c, nil
}

// Open performs the full writer open, mirroring Rust live_writer
// open_main + open_locked minus the sidecar coordination: map_writer,
// committed selection, tail trim, and the terminal path-identity
// re-verification (Rust open_locked ends with verify_pair after
// trim_committed_tail). On failure the exclusive lock and descriptor are
// released.
func Open(path string, budget PageBudget, check func(clean string) error) (*Core, error) {
	c, err := OpenWriter(path, budget, check)
	if err != nil {
		return nil, err
	}
	if err := c.SelectCommitted(); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.TrimCommittedTail(); err != nil {
		c.Close()
		return nil, err
	}
	if err := c.m.VerifyIdentity(path); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// SelectCommitted re-derives the committed generation from the current
// physical extent, mirroring Rust WriterCore::select_committed
// (writer_core/open.rs). The writer rule applies: only a provable
// current generation opens; a sole meta or a transaction-gapped pair is
// refused.
func (c *Core) SelectCommitted() error {
	return c.rebootstrap()
}

func (c *Core) rebootstrap() error {
	p0, err := c.m.Page(0)
	if err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	p1, err := c.m.Page(1)
	if err != nil {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: err.Error()}
	}
	// Rust select_committed re-stats the file at selection time
	// (writer_core/open.rs: mapping.file().metadata().len()): the live
	// writer holds the lifetime lock shared, so the lease holder may
	// legitimately extend the file between this open's lock acquisition
	// and this selection, and only the fresh extent proves the new
	// generation. The tracked PhysicalSize remains authoritative for
	// hot-path sizing and is kept in sync by Grow/Shrink.
	size, err := c.m.FileSize()
	if err != nil {
		return err
	}
	res, err := bootstrap.Open(p0, p1, size, bootstrap.ModeWriter)
	if err != nil {
		return err
	}
	c.base = *res
	return nil
}

// TrimCommittedTail removes any unpublished tail, mirroring Rust
// WriterCore::trim_committed_tail: when the physical extent exceeds the
// committed generation the file is shrunk to the committed bytes and
// synced to stable storage; a committed==physical core is a no-op. If the
// shrink succeeds but the sync fails, the tracked physical extent is
// already updated (Rust parity), so a retried trim is a no-op: the caller
// must abort on the error and let the next open re-trim.
func (c *Core) TrimCommittedTail() error {
	if c.base.PhysicalBytes == c.base.CommittedBytes {
		return nil
	}
	if err := c.m.Shrink(c.base.CommittedBytes); err != nil {
		return err
	}
	// Rust trim_committed_tail assigns the shrink_or_retain result: a
	// still-mapped view may retain the tail, and the tracked physical
	// extent must observe the retained length, not the committed
	// request.
	length, err := c.m.FileSize()
	if err != nil {
		return err
	}
	c.base.PhysicalBytes = length
	return c.m.SyncFile()
}

// BaseInfo returns the logical facts of the selected committed generation
// (Rust WriterCore::base_info).
func (c *Core) BaseInfo() WriterInfo {
	m := c.base.Meta
	return WriterInfo{
		AddressFamily:    m.AddressFamily,
		ValueKind:        m.ValueKind,
		StructureKind:    m.StructureKind,
		ValueTag:         m.ValueTag,
		DatabaseID:       m.DatabaseID,
		TransactionID:    m.TxnID,
		CommitNonce:      m.CommitNonce,
		PageCount:        m.PageCount,
		RangeRecordCount: m.RangeRecordCount,
		ActiveFeedCount:  m.ActiveFeedCount,
	}
}

// Budget returns the declared page budget (Rust WriterCore::max_heap_bytes
// family; the full budget is carried for the edit core).
func (c *Core) Budget() PageBudget { return c.budget }

// TailCleanupState is the unpublished-tail evidence of the committed
// generation (Rust WriterCore::TailCleanupState): the base identity plus
// the greatest observed tail length beyond the committed extent.
type TailCleanupState struct {
	DatabaseID               [16]byte
	TransactionID            uint64
	CommitNonce              [16]byte
	CommittedLength          uint64
	ObservedTailEndExclusive *uint64
}

// TailCleanupState reports the committed generation and the greatest
// observed physical length beyond it (Rust tail_cleanup_state): the
// current file extent and the unproved tail remembered by a failed trim,
// whichever is larger and exceeds the committed length. A metadata
// failure reports no current extent, exactly like the Rust
// metadata().ok().
func (c *Core) TailCleanupState() TailCleanupState {
	var current *uint64
	if length, err := c.m.FileSize(); err == nil {
		current = &length
	}
	var observed *uint64
	for _, candidate := range []*uint64{c.unprovedTailEnd, current} {
		if candidate == nil || *candidate <= c.base.CommittedBytes {
			continue
		}
		if observed == nil || *candidate > *observed {
			observed = candidate
		}
	}
	return TailCleanupState{
		DatabaseID:               c.base.Meta.DatabaseID,
		TransactionID:            c.base.Meta.TxnID,
		CommitNonce:              c.base.Meta.CommitNonce,
		CommittedLength:          c.base.CommittedBytes,
		ObservedTailEndExclusive: observed,
	}
}

// Close releases the mapping and the exclusive lifetime lock.
func (c *Core) Close() error { return c.m.Close() }
