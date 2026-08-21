// Writer close and unpublished-tail cleanup (Rust writer_core/close.rs +
// writer_core.rs discard_unpublished). Close re-selects the committed
// generation from the locked file, trims any unpublished tail, and
// verifies the discard geometry before releasing the draft.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// ClosePlan is the re-selected committed generation a close must retain
// (Rust writer_core::ClosePlan).
type ClosePlan struct {
	selected bootstrap.Result
}

// TransactionID returns the planned generation's transaction (Rust
// ClosePlan::transaction_id).
func (p ClosePlan) TransactionID() uint64 { return p.selected.Meta.TxnID }

// PrepareClose re-selects the committed generation from the locked file
// and verifies identity and generation stability (Rust
// WriterCore::prepare_close): a changed database identity is WrongMode
// (Go class CodeWrongState, the established WrongMode mapping),
// and a draft with a committed generation different from the base is
// WrongMode (the generation advanced before abort cleanup).
func (c *Core) PrepareClose() (ClosePlan, error) {
	physical, err := c.m.FileSize()
	if err != nil {
		return ClosePlan{}, err
	}
	p0, err := c.m.Page(0)
	if err != nil {
		return ClosePlan{}, err
	}
	p1, err := c.m.Page(1)
	if err != nil {
		return ClosePlan{}, err
	}
	selected, err := bootstrap.Open(p0, p1, physical, bootstrap.ModeWriter)
	if err != nil {
		return ClosePlan{}, err
	}
	if selected.Meta.DatabaseID != c.base.Meta.DatabaseID {
		return ClosePlan{}, &format.Error{Code: format.CodeWrongState, Detail: "live database identity changed"}
	}
	if c.draft != nil && selected.Meta != c.base.Meta {
		return ClosePlan{}, &format.Error{Code: format.CodeWrongState, Detail: "committed generation changed before abort cleanup"}
	}
	return ClosePlan{selected: *selected}, nil
}

// FinishClose trims to the planned committed generation and verifies the
// discard geometry (Rust WriterCore::finish_close).
func (c *Core) FinishClose(plan ClosePlan) error {
	physical, err := c.trimTo(plan.selected.CommittedBytes)
	if err != nil {
		return err
	}
	if c.draft != nil {
		if err := c.verifyDiscardResult(physical); err != nil {
			return err
		}
	} else if fileLen, err := c.m.FileSize(); err != nil {
		return err
	} else if fileLen != physical {
		return corrupt("writer close changed the retained physical length")
	}
	c.base.PhysicalBytes = physical
	c.draft = nil
	c.unprovedTailEnd = nil
	return nil
}

// DiscardUnpublished aborts the open draft: trim any unpublished tail and
// verify the committed generation is intact (Rust
// WriterCore::discard_unpublished).
func (c *Core) DiscardUnpublished() error {
	if err := fault.Fail("commit.discard_unpublished"); err != nil {
		return err
	}
	if err := c.requireHealthy(); err != nil {
		return err
	}
	if err := c.RequireUnchangedBase(); err != nil {
		return err
	}
	physical, err := c.trimUnpublishedTail()
	if err != nil {
		return err
	}
	if err := c.verifyDiscardResult(physical); err != nil {
		return err
	}
	c.base.PhysicalBytes = physical
	c.draft = nil
	c.unprovedTailEnd = nil
	return nil
}

func (c *Core) trimTo(committedBytes uint64) (uint64, error) {
	length, err := c.m.FileSize()
	if err != nil {
		return 0, err
	}
	if length < committedBytes {
		return 0, corrupt("main file is shorter than its committed generation")
	}
	if length > committedBytes {
		c.unprovedTailEnd = &length
		if err := c.m.Shrink(committedBytes); err != nil {
			return 0, err
		}
		if err := c.m.SyncFile(); err != nil {
			return 0, err
		}
		return committedBytes, nil
	}
	if c.m.Size() != committedBytes {
		if err := c.m.Remap(committedBytes); err != nil {
			return 0, err
		}
	}
	if c.draft != nil {
		if err := c.m.SyncFile(); err != nil {
			return 0, err
		}
	}
	return length, nil
}

func (c *Core) trimUnpublishedTail() (uint64, error) {
	length, err := c.m.FileSize()
	if err != nil {
		return 0, err
	}
	if length < c.base.CommittedBytes {
		return 0, corrupt("main file is shorter than its committed generation")
	}
	if length > c.base.CommittedBytes {
		c.unprovedTailEnd = &length
		if err := c.m.Shrink(c.base.CommittedBytes); err != nil {
			return 0, err
		}
		if err := c.m.SyncFile(); err != nil {
			return 0, err
		}
		return c.base.CommittedBytes, nil
	}
	if c.m.Size() != c.base.CommittedBytes {
		if err := c.m.Remap(c.base.CommittedBytes); err != nil {
			return 0, err
		}
	}
	return length, nil
}

// verifyDiscardResult checks the post-cleanup geometry (Rust
// WriterCore::verify_discard_result): the committed generation must be
// unchanged, the retained physical length must cover it and stay
// page-aligned, the locked file must match it, and the mapping must sit
// exactly at the committed extent.
func (c *Core) verifyDiscardResult(physicalBytes uint64) error {
	if err := c.RequireUnchangedBase(); err != nil {
		return err
	}
	if physicalBytes < c.base.CommittedBytes || physicalBytes%format.PageSize != 0 {
		return corrupt("unpublished tail cleanup left inconsistent geometry")
	}
	fileLen, err := c.m.FileSize()
	if err != nil {
		return err
	}
	if fileLen != physicalBytes || c.m.Size() != c.base.CommittedBytes {
		return corrupt("unpublished tail cleanup left inconsistent geometry")
	}
	return nil
}

// requireUnchangedBase re-selects the committed generation and verifies
// it still equals the base (Rust WriterCore::require_unchanged_base).
func (c *Core) RequireUnchangedBase() error {
	physical, err := c.m.FileSize()
	if err != nil {
		return err
	}
	p0, err := c.m.Page(0)
	if err != nil {
		return err
	}
	p1, err := c.m.Page(1)
	if err != nil {
		return err
	}
	selected, err := bootstrap.Open(p0, p1, physical, bootstrap.ModeWriter)
	if err != nil {
		return err
	}
	if selected.Meta != c.base.Meta {
		return &format.Error{Code: format.CodeWrongState, Detail: "committed generation changed under the writer"}
	}
	return nil
}
