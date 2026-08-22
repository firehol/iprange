// Draft prepare and reclamation (Rust draft_store.rs
// prepare_with_checkpoint / select_reclamation / apply_reclamation /
// reclaim_extent). Prepare is the physical staging before publication:
// release private pages back to the allocator machinery, finish the free
// bitmap shape, then seal every dirty data page. The structure and
// membership delta finishing stages of the Rust sequence are structural
// no-ops here until those edit cores exist (direct drafts carry no
// deltas).

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/retire"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// PrepareWithCheckpoint stages the draft for publication (Rust
// DraftStore::prepare_with_checkpoint). The workflow-input-open gate of
// the Rust sequence is structurally closed: editor workflow states arrive
// with their public workflows, and every current Draft is past input.
// A draft with no changes publishes nothing (Rust early return).
func (s *DraftStore) PrepareWithCheckpoint(checkpoint func() error) error {
	if !s.draft.changed {
		return nil
	}
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	// finish_structure_deltas: no-op until the structure edit core
	// (direct drafts carry no structure deltas).
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	// finish_membership_deltas: no-op until the membership edit core
	// (direct drafts carry no membership deltas).
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	if err := s.releasePrivatePages(checkpoint); err != nil {
		return err
	}
	if err := s.finishBitmapShape(checkpoint); err != nil {
		return err
	}
	if checkpoint != nil {
		if err := checkpoint(); err != nil {
			return err
		}
	}
	return s.sealPrivatePages(checkpoint)
}

// releasePrivatePages returns every private page to the allocator
// machinery until the allocator-retired backlog is empty (Rust
// DraftStore::release_private_pages: replenish the reserve, drain the
// backlog, drain the private stack, repeat while the backlog grew).
func (s *DraftStore) releasePrivatePages(checkpoint func() error) error {
	for {
		if checkpoint != nil {
			if err := checkpoint(); err != nil {
				return err
			}
		}
		if err := s.replenishReserve(); err != nil {
			return err
		}
		if err := s.drainAllocatorRetired(); err != nil {
			return err
		}
		if err := s.drainPrivateStack(checkpoint); err != nil {
			return err
		}
		if s.draft.allocatorRetired.Len() == 0 {
			return nil
		}
	}
}

// drainPrivateStack frees every page of the private stack into the free
// bitmap, draining the allocator-retired backlog after each page (Rust
// DraftStore::drain_private_stack).
func (s *DraftStore) drainPrivateStack(checkpoint func() error) error {
	for s.draft.privateHead != 0 {
		if checkpoint != nil {
			if err := checkpoint(); err != nil {
				return err
			}
		}
		pageNumber, err := s.popPrivate()
		if err != nil {
			return err
		}
		if err := s.freeOne(pageNumber); err != nil {
			return err
		}
	}
	return nil
}

// replenishReserve fills every empty allocator-reserve slot from the
// private stack or the file tail, zeroing fresh tail pages (Rust
// DraftStore::replenish_reserve).
func (s *DraftStore) replenishReserve() error {
	for i := 0; i < len(s.draft.meta.AllocatorReserve); i++ {
		if s.draft.meta.AllocatorReserve[i] != 0 {
			continue
		}
		if s.draft.privateHead == 0 {
			pageNumber, err := s.allocateTail()
			if err != nil {
				return err
			}
			page, tag, err := s.Update(pageNumber)
			if err != nil {
				return err
			}
			clear(page)
			if err := s.RestoreDirty(pageNumber, tag); err != nil {
				return err
			}
			s.draft.meta.AllocatorReserve[i] = pageNumber
			continue
		}
		pageNumber, err := s.popPrivate()
		if err != nil {
			return err
		}
		s.draft.meta.AllocatorReserve[i] = pageNumber
	}
	return nil
}

// finishBitmapShape grows the free bitmap hierarchy until the allocator
// reserve is full (Rust DraftStore::finish_bitmap_shape: replenish, ensure
// the bitmap level, repeat while reserve slots emptied).
func (s *DraftStore) finishBitmapShape(checkpoint func() error) error {
	for {
		if checkpoint != nil {
			if err := checkpoint(); err != nil {
				return err
			}
		}
		if err := s.replenishReserve(); err != nil {
			return err
		}
		root := s.draft.meta.FreeBitmapRoot
		if err := bitmap.EnsureLevel(s, &root, s.draft.meta.PageCount); err != nil {
			return err
		}
		s.draft.meta.FreeBitmapRoot = root
		full := true
		for _, page := range s.draft.meta.AllocatorReserve {
			if page == 0 {
				full = false
				break
			}
		}
		if full {
			return nil
		}
	}
}

// SelectReclamation selects the bounded oldest-safe retirement work (Rust
// DraftStore::select_reclamation over retirement.rs
// select_reclamation_with_checkpoint; the selected transaction is the one
// before this draft's).
func (s *DraftStore) SelectReclamation(oldestReader *uint64, maxTransactions, maxPages uint64, checkpoint func() error) (*retire.Reclamation, error) {
	return retire.SelectReclamation(s, s.draft.meta.RetirementRoot, s.draft.meta.TxnID-1, oldestReader, maxTransactions, maxPages, checkpoint)
}

// ApplyReclamation replays the selected reclamation against the current
// retirement tree and verifies the selection still matches exactly (Rust
// DraftStore::apply_reclamation: retire every extent through the selected
// transaction, count transactions and pages, fail closed on change).
func (s *DraftStore) ApplyReclamation(selection *retire.Reclamation, checkpoint func() error) error {
	var transactions uint64
	var pages uint64
	var previousTxn uint64
	for {
		if checkpoint != nil {
			if err := checkpoint(); err != nil {
				return err
			}
		}
		extent, hasExtent, err := retire.First(s, s.draft.meta.RetirementRoot)
		if err != nil {
			return err
		}
		if !hasExtent {
			break
		}
		if extent.Transaction() > selection.ThroughTxn {
			break
		}
		if extent.Transaction() != previousTxn {
			transactions++
			previousTxn = extent.Transaction()
		}
		pages, err = checkedAdd(pages, extent.PageCount(), "reclaimed page count")
		if err != nil {
			return err
		}
		if err := s.reclaimExtent(extent, checkpoint); err != nil {
			return err
		}
	}
	if transactions != selection.Transactions || pages != selection.Pages {
		return corrupt("reclamation selection changed")
	}
	s.draft.changed = true
	return nil
}

// reclaimExtent frees every page of one retirement extent and removes the
// extent from the retirement tree, retiring any COW victims the removal
// produced (Rust DraftStore::reclaim_extent).
func (s *DraftStore) reclaimExtent(extent retire.Extent, checkpoint func() error) error {
	first, end := extent.Pages()
	for page := first; page < end; page++ {
		if checkpoint != nil {
			if err := checkpoint(); err != nil {
				return err
			}
		}
		// The page-number space is u32 (Rust u32::try_from Corrupt guard);
		// a retirement extent crossing 2^32 must fail closed, never
		// truncate into the live allocator.
		if page > uint64(^uint32(0)) {
			return corrupt("reclaimed page exceeds page-number space")
		}
		if err := s.freeOne(uint32(page)); err != nil {
			return err
		}
		work.PageReclaimed(1)
	}
	root := s.draft.meta.RetirementRoot
	count := s.draft.meta.RetiredExtentCount
	generated, err := retire.RemoveExtent(s, &root, &count, extent)
	if err != nil {
		return err
	}
	s.draft.meta.RetirementRoot = root
	s.draft.meta.RetiredExtentCount = count
	for _, page := range generated.Slice() {
		if err := s.retireOne(page); err != nil {
			return err
		}
	}
	return s.drainAllocatorRetired()
}

// checkedAdd adds two page counts with the Rust ArithmeticOverflow class.
func checkedAdd(a, b uint64, what string) (uint64, error) {
	if ^uint64(0)-a < b {
		return 0, overflow(what)
	}
	return a + b, nil
}
