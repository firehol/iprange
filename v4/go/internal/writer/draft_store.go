// DraftStore is the mapped page provider for one COW draft (Rust
// DraftStore: draft_store.rs + draft_store/storage.rs). It composes the
// mapping owner and the draft: allocations come from the private-page
// stack, the committed free bitmap, the allocator reserve, or the file
// tail; every privately claimed page is linked into one dirty chain whose
// tags live in the page checksum slot until prepare seals the data pages.
// All views alias the mapping; no complete page is ever owned here.
//
// The byte-level necessary-work counters (BytesMoved/BytesZeroed) mirror
// the Rust PageMut accounting at the store level: Go has no PageMut
// wrapper, so each draft mutation attributes the same byte counts the Rust
// primitives would.

package writer

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/retire"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Wire markers of the private-page stack and the dirty chain (Rust
// draft_store/storage.rs). The private marker reuses the common page
// header: bytes 0..4 the private magic, 4..8 the stack link, 8..16 the
// draft transaction, and the checksum slot (28) the dirty-chain tag.
const (
	dirtyEnd         = uint32(1)
	privateMagic     = uint32(0x50465245)
	privateMagicOff  = 0
	privateNextOff   = 4
	privateTxnOff    = 8
	privateHeaderLen = format.SlottedHeaderSize
)

// DraftStore is the mutable page provider of one draft.
type DraftStore struct {
	mapping            *mapping.Mapping
	committedPageCount uint64
	budget             PageBudget
	draft              *Draft
	// Encode targets for the dictionary and catalog records of one
	// draft. A draft is single-threaded and every tree insert copies its
	// record into the mapped page before the next encode reuses the
	// buffer, so these are allocated once per draft, never per record
	// (the Go generic tree interface makes stack encodes escape).
	recordScratch  [membershipRecordLimit]byte
	hashScratch    [membershipHashKeySize]byte
	catalogScratch [catalogMaxRecord]byte
	deltaScratch   [deltaRecordSize]byte
	// rangeScratch owns the fixed-size range-record encode targets of
	// this draft (Rust EncodedRange locals). The generic rangeFamily
	// interface makes stack targets escape, so the range context
	// borrows this draft-owned array instead of allocating per record.
	// Slot 0 serves the one-record paths; the up to three cells of one
	// leaf replacement use slots 0..2 (Rust replace_strictly_inside).
	rangeScratch [3][format.RangeRecordV6Size]byte
	// structureScratch owns the structure intern payload of one
	// operation (Rust intern_payload payload local). The shape-stenciled
	// generic internStructure leaks its payload argument, so the draft
	// copies the payload into this field before the call instead of
	// allocating per intern; the scratch is only live for one nested
	// intern call at a time.
	structureScratch structurePayload
	// combineScratch owns the combine operand of one membership combine
	// (Rust Combined local). The shape-stenciled generic membershipWords
	// dispatch leaks its pointer argument, so the draft reuses this
	// field instead of allocating per combine; the scratch is only live
	// for one nested combine at a time.
	combineScratch combinedWords
	// rangeCtx4 and rangeCtx6 are the mutable range-tree states of one
	// draft operation per address family (Rust range_mutation function
	// parameters: store, root, record_count). The range contexts carry
	// interface fields whose method calls escape a stack context, so the
	// draft owns one context per family and every entry point resets it
	// before the operation; root/count live in the rangeRoot/rangeCount
	// snapshot fields below, mirroring the Rust locals written back
	// after the edit succeeds.
	rangeCtx4 rangeCtx[key4]
	rangeCtx6 rangeCtx[key6]
	// rangeRoot and rangeCount are the operation-local root and record
	// count snapshots of one range edit (Rust draft_store.rs assign/
	// clear locals). The range context points at them; entry points
	// copy the draft meta in before the operation and out after it
	// succeeds.
	rangeRoot  uint32
	rangeCount uint64
}

// NewDraftStore binds one draft to the opened read-write mapping (Rust
// DraftStore::new). committedPageCount is the committed generation's page
// count: the free bitmap never hands out pages at or above it.
func NewDraftStore(m *mapping.Mapping, committedPageCount uint64, budget PageBudget, draft *Draft) *DraftStore {
	return &DraftStore{mapping: m, committedPageCount: committedPageCount, budget: budget, draft: draft}
}

// beginRangeEdit4 snaps the range root/count locals from one draft
// source and resets the draft-owned IPv4 range context for one range
// edit (Rust range_mutation function parameters). Every range entry
// point must pair beginRangeEdit with commitRangeEdit, so the untracked
// flag, the scratch target, and the root/count pointers are always reset
// before an operation: the missed-reset class that silently skipped
// membership accounting is impossible by construction.
func (s *DraftStore) beginRangeEdit4(root uint32, count uint64) *rangeCtx[key4] {
	s.rangeRoot = root
	s.rangeCount = count
	ctx := &s.rangeCtx4
	ctx.family = rangeCodec4{}
	ctx.store = s
	ctx.storeView = s
	ctx.untracked = false
	ctx.root = &s.rangeRoot
	ctx.count = &s.rangeCount
	ctx.scratch = &s.rangeScratch
	return ctx
}

// beginRangeEdit6 is the IPv6 form of beginRangeEdit4.
func (s *DraftStore) beginRangeEdit6(root uint32, count uint64) *rangeCtx[key6] {
	s.rangeRoot = root
	s.rangeCount = count
	ctx := &s.rangeCtx6
	ctx.family = rangeCodec6{}
	ctx.store = s
	ctx.storeView = s
	ctx.untracked = false
	ctx.root = &s.rangeRoot
	ctx.count = &s.rangeCount
	ctx.scratch = &s.rangeScratch
	return ctx
}

// commitRangeEdit writes the edited range root/count back to one draft
// destination and folds the changed flag into the draft (Rust
// draft_store.rs assign/clear locals written back after the edit).
func (s *DraftStore) commitRangeEdit(root *uint32, count *uint64, changed bool) {
	*root = s.rangeRoot
	*count = s.rangeCount
	s.draft.changed = s.draft.changed || changed
}

// Compile-time interface checks: the draft store serves the tree core, the
// free bitmap, and the retirement sink.
var (
	_ tree.Store         = (*DraftStore)(nil)
	_ bitmap.BitmapStore = (*DraftStore)(nil)
	_ tree.RetiringStore = (*DraftStore)(nil)
)

// TargetTxn returns the draft transaction (Rust Store::target_txn).
func (s *DraftStore) TargetTxn() uint64 { return s.draft.meta.TxnID }

// PageLimit returns the draft's current page count (Rust Store::page_limit).
func (s *DraftStore) PageLimit() uint64 { return s.draft.meta.PageCount }

// Inspect returns one mapped draft page view (Rust Store::inspect_page).
func (s *DraftStore) Inspect(pageNumber uint32) ([]byte, error) {
	if err := requirePage(pageNumber, s.draft.meta.PageCount); err != nil {
		return nil, err
	}
	return s.mapping.Page(pageNumber)
}

// Allocate returns one page for the draft: the private stack first, then
// the committed free bitmap (with its COW victims deferred to the
// allocator-retired backlog), then the file tail (Rust Store::allocate).
func (s *DraftStore) Allocate() (uint32, error) {
	if s.draft.privateHead != 0 {
		return s.popPrivate()
	}
	root := s.draft.meta.FreeBitmapRoot
	var retired tree.RetiredPages
	limit := s.committedPageCount
	page, ok, err := bitmap.TakeLowest(s, &root, limit, &retired)
	if err != nil {
		return 0, err
	}
	if ok {
		s.draft.meta.FreeBitmapRoot = root
		if err := s.draft.allocatorRetired.Extend(retired.Slice()); err != nil {
			return 0, err
		}
		if err := s.claimAllocated(page); err != nil {
			return 0, err
		}
		return page, nil
	}
	s.draft.meta.FreeBitmapRoot = root
	return s.allocateTail()
}

// Update returns one private mapped page view ready for mutation with the
// dirty-chain tag captured before the mutation (Rust Store::update_page).
// The caller must restore the tag with RestoreDirty after a successful
// mutation, because page-header writes clear the checksum slot that
// carries the tag until prepare seals the page. Committed pages are
// refused (Rust update_page).
func (s *DraftStore) Update(pageNumber uint32) ([]byte, uint32, error) {
	if err := requirePage(pageNumber, s.draft.meta.PageCount); err != nil {
		return nil, 0, err
	}
	page, err := s.mapping.Page(pageNumber)
	if err != nil {
		return nil, 0, err
	}
	tag, err := privateTag(page, s.draft.meta.TxnID)
	if err != nil {
		return nil, 0, err
	}
	return page, tag, nil
}

// FinishEdit stamps one page's dirty-chain tag after a successful
// mutation, writing into the same mapping view Update returned (Rust
// update_page/copy_page put the tag through the same PageMut, so the
// page fetch count stays one per edit).
func (s *DraftStore) FinishEdit(page []byte, tag uint32) error {
	format.PutU32(page[format.PageChecksumOffset:], tag)
	work.BytesMoved(4)
	return nil
}

// CopyPage returns the source and destination page views of one COW copy;
// the destination must already be private for the draft. The destination's
// dirty-chain tag is captured before the copy; the caller copies the bytes
// and then restores the tag with RestoreDirty (Rust Store::copy_page).
func (s *DraftStore) CopyPage(source, destination uint32) ([]byte, []byte, uint32, error) {
	if err := requirePage(source, s.draft.meta.PageCount); err != nil {
		return nil, nil, 0, err
	}
	if err := requirePage(destination, s.draft.meta.PageCount); err != nil {
		return nil, nil, 0, err
	}
	src, err := s.mapping.Page(source)
	if err != nil {
		return nil, nil, 0, err
	}
	dst, err := s.mapping.Page(destination)
	if err != nil {
		return nil, nil, 0, err
	}
	tag, err := privateTag(dst, s.draft.meta.TxnID)
	if err != nil {
		return nil, nil, 0, err
	}
	work.PageCopied(1)
	return src, dst, tag, nil
}

// DiscardPrivate pushes one private page onto the reuse stack (Rust
// Store::discard_private). The page must belong to the draft and must be
// linked in the dirty chain; the page content survives for reuse and the
// private marker occupies the page head.
func (s *DraftStore) DiscardPrivate(pageNumber uint32) error {
	if pageNumber == s.draft.privateHead {
		return corrupt("discarded private page is invalid")
	}
	txn := s.draft.meta.TxnID
	if err := requirePage(pageNumber, s.draft.meta.PageCount); err != nil {
		return err
	}
	next := s.draft.privateHead
	page, err := s.mapping.Page(pageNumber)
	if err != nil {
		return err
	}
	if format.U64(page[format.HeaderBorn:]) != txn {
		return corrupt("committed page cannot enter the private stack")
	}
	if format.U32(page[format.PageChecksumOffset:]) == 0 {
		return corrupt("private page is absent from the dirty chain")
	}
	format.PutU32(page[privateMagicOff:], privateMagic)
	format.PutU32(page[privateNextOff:], next)
	format.PutU64(page[privateTxnOff:], txn)
	work.BytesMoved(16)
	if _, err := privateTag(page, txn); err != nil {
		return err
	}
	s.draft.privateHead = pageNumber
	return nil
}

// AllocateBitmapPage returns one page for the free bitmap: the private
// stack, then the allocator reserve, then the file tail (Rust
// BitmapStore::allocate_bitmap_page).
func (s *DraftStore) AllocateBitmapPage() (uint32, error) {
	if s.draft.privateHead != 0 {
		return s.popPrivate()
	}
	for i := range s.draft.meta.AllocatorReserve {
		if s.draft.meta.AllocatorReserve[i] != 0 {
			page := s.draft.meta.AllocatorReserve[i]
			s.draft.meta.AllocatorReserve[i] = 0
			if err := s.claimAllocated(page); err != nil {
				return 0, err
			}
			return page, nil
		}
	}
	return s.allocateTail()
}

// AllocationForbidden reports whether pageNumber may never be handed out
// by the free bitmap: meta pages, the allocator reserve, and every
// persistent root (Rust BitmapStore::allocation_forbidden).
func (s *DraftStore) AllocationForbidden(pageNumber uint32) bool {
	if pageNumber < 2 {
		return true
	}
	for _, page := range s.draft.meta.AllocatorReserve {
		if page == pageNumber {
			return true
		}
	}
	for _, root := range s.draft.meta.Roots() {
		if root == pageNumber {
			return true
		}
	}
	return false
}

// RetirePages records every retired page in the retirement tree, draining
// the allocator-retired backlog in between (Rust RetiringStore::retire_pages).
func (s *DraftStore) RetirePages(retired tree.RetiredPages) error {
	for _, page := range retired.Slice() {
		if err := s.retireOne(page); err != nil {
			return err
		}
	}
	return s.drainAllocatorRetired()
}

// sealPrivatePages walks the dirty chain, sealing every data page's
// checksum, and clears the chain (Rust DraftStore::seal_private_pages).
// The checkpoint hook runs once per visited chain entry.
func (s *DraftStore) sealPrivatePages(checkpoint func() error) error {
	pageNumber := s.draft.dirtyHead
	remaining := s.draft.privatePages
	for pageNumber != 0 {
		if checkpoint != nil {
			if err := checkpoint(); err != nil {
				return err
			}
		}
		if remaining == 0 {
			return corrupt("draft dirty-page chain is cyclic")
		}
		remaining--
		txn := s.draft.meta.TxnID
		limit := s.draft.meta.PageCount
		var next uint32
		var dataPage bool
		page, err := s.Inspect(pageNumber)
		if err != nil {
			return err
		}
		next, err = dirtyNext(format.U32(page[format.PageChecksumOffset:]), pageNumber, limit)
		if err != nil {
			return err
		}
		if hasPageMagic(page) && format.U64(page[format.HeaderBorn:]) != txn {
			return corrupt("dirty page has the wrong transaction")
		}
		dataPage = hasPageMagic(page)
		if dataPage {
			page, err := s.mapping.Page(pageNumber)
			if err != nil {
				return err
			}
			if err := format.SealPageChecksum(page); err != nil {
				return err
			}
			work.BytesMoved(8) // Rust seal_mapped: put_u32(0) + put_u32(checksum)
		}
		pageNumber = next
	}
	s.draft.dirtyHead = 0
	return nil
}

// existingDirtyTag returns the dirty-chain tag when pageNumber is already
// part of this draft's dirty chain (Rust existing_dirty_tag).
func (s *DraftStore) existingDirtyTag(pageNumber uint32) (uint32, bool, error) {
	if err := requirePage(pageNumber, s.draft.meta.PageCount); err != nil {
		return 0, false, err
	}
	page, err := s.mapping.Page(pageNumber)
	if err != nil {
		return 0, false, err
	}
	txn := s.draft.meta.TxnID
	ownedData := format.U64(page[format.HeaderBorn:]) == txn
	privateStack := isPrivatePage(page, txn)
	tag := format.U32(page[format.PageChecksumOffset:])
	if !(ownedData || privateStack) || tag == 0 {
		return 0, false, nil
	}
	inChain, err := s.dirtyChainContains(pageNumber)
	if err != nil {
		return 0, false, err
	}
	if !inChain {
		return 0, false, nil
	}
	return tag, true, nil
}

// dirtyChainContains walks the dirty chain and reports whether expected is
// linked in it (Rust dirty_chain_contains).
func (s *DraftStore) dirtyChainContains(expected uint32) (bool, error) {
	txn := s.draft.meta.TxnID
	limit := s.draft.meta.PageCount
	pageNumber := s.draft.dirtyHead
	remaining := s.draft.privatePages
	for pageNumber != 0 {
		if remaining == 0 {
			return false, corrupt("draft dirty-page chain is cyclic")
		}
		remaining--
		if pageNumber == expected {
			return true, nil
		}
		if err := requirePage(pageNumber, limit); err != nil {
			return false, err
		}
		page, err := s.mapping.Page(pageNumber)
		if err != nil {
			return false, err
		}
		if format.U64(page[format.HeaderBorn:]) != txn && !isPrivatePage(page, txn) {
			return false, corrupt("dirty page has the wrong transaction")
		}
		next, err := dirtyNext(format.U32(page[format.PageChecksumOffset:]), pageNumber, limit)
		if err != nil {
			return false, err
		}
		pageNumber = next
	}
	return false, nil
}

// claimAllocated claims one page that may already belong to the draft,
// keeping one dirty-chain entry (Rust claim_allocated).
func (s *DraftStore) claimAllocated(pageNumber uint32) error {
	if err := requirePage(pageNumber, s.draft.meta.PageCount); err != nil {
		return err
	}
	tag, ok, err := s.existingDirtyTag(pageNumber)
	if err != nil {
		return err
	}
	return s.claimPage(pageNumber, tag, ok)
}

// claimNewTail claims one fresh tail page (Rust claim_new_tail).
func (s *DraftStore) claimNewTail(pageNumber uint32) error {
	if err := requirePage(pageNumber, s.draft.meta.PageCount); err != nil {
		return err
	}
	return s.claimPage(pageNumber, 0, false)
}

// claimPage privatizes pageNumber: it zeroes the page head, writes the
// private marker and draft transaction, links the page into the dirty
// chain through the checksum slot, and charges the private budget once
// (Rust claim_page).
func (s *DraftStore) claimPage(pageNumber uint32, existing uint32, hasExisting bool) error {
	var tag uint32
	if hasExisting {
		tag = existing
	} else if s.draft.dirtyHead == 0 {
		tag = dirtyEnd
	} else {
		tag = s.draft.dirtyHead
	}
	if !hasExisting {
		if err := s.chargePrivate(); err != nil {
			return err
		}
	}
	page, err := s.mapping.Page(pageNumber)
	if err != nil {
		return err
	}
	clear(page[0:privateHeaderLen])
	format.PutU32(page[privateMagicOff:], privateMagic)
	format.PutU64(page[privateTxnOff:], s.draft.meta.TxnID)
	format.PutU32(page[format.PageChecksumOffset:], tag)
	work.BytesZeroed(privateHeaderLen)
	work.BytesMoved(16)
	if !hasExisting {
		s.draft.dirtyHead = pageNumber
		work.PageCreated(1)
	}
	return nil
}

// popPrivate pops the private-page stack (Rust pop_private).
func (s *DraftStore) popPrivate() (uint32, error) {
	pageNumber := s.draft.privateHead
	if pageNumber == 0 {
		return 0, corrupt("private page stack is empty")
	}
	txn := s.draft.meta.TxnID
	page, err := s.Inspect(pageNumber)
	if err != nil {
		return 0, err
	}
	next, err := privateStackNext(page, txn)
	if err != nil {
		return 0, err
	}
	if next == pageNumber || (next != 0 && (next < 2 || uint64(next) >= s.draft.meta.PageCount)) {
		return 0, corrupt("private page stack points outside the draft")
	}
	s.draft.privateHead = next
	return pageNumber, nil
}

// chargePrivate charges one private page against the draft budget (Rust
// charge_private).
func (s *DraftStore) chargePrivate() error {
	if s.draft.privatePages >= s.budget.MaxPrivatePages {
		return budgetExceeded("private pages")
	}
	s.draft.privatePages++
	return nil
}

// allocateTail claims one new file-tail page (Rust allocate_tail).
func (s *DraftStore) allocateTail() (uint32, error) {
	if s.draft.meta.PageCount < s.committedPageCount {
		return 0, corrupt("draft page count moved backwards")
	}
	if s.draft.growthPages >= s.budget.MaxGrowthPages {
		return 0, budgetExceeded("file growth pages")
	}
	if s.draft.meta.PageCount >= format.MaxPageCount {
		return 0, &format.Error{Code: format.CodePageSpaceExhausted, Detail: "v4 page-number space is exhausted"}
	}
	if err := s.ensureTailCapacity(); err != nil {
		return 0, err
	}
	pageNumber := uint32(s.draft.meta.PageCount)
	s.draft.meta.PageCount++
	s.draft.growthPages++
	if err := s.claimNewTail(pageNumber); err != nil {
		return 0, err
	}
	return pageNumber, nil
}

// ensureTailCapacity grows the mapping so the next tail page is mapped
// (Rust ensure_tail_capacity): when the physical file already covers the
// transaction capacity the mapping is remapped only; otherwise both the
// file and the mapping grow to the capacity.
func (s *DraftStore) ensureTailCapacity() error {
	required := s.draft.meta.PageCount + 1
	mappedPages := s.mapping.Size() / format.PageSize
	if required <= mappedPages {
		return nil
	}
	capacity := s.committedPageCount + s.budget.MaxGrowthPages
	if capacity < s.committedPageCount || capacity > format.MaxPageCount {
		capacity = format.MaxPageCount
	}
	if required > capacity {
		return budgetExceeded("file growth pages")
	}
	bytes := capacity * format.PageSize
	if s.mapping.PhysicalSize() >= bytes {
		return s.mapping.Remap(bytes)
	}
	return s.mapping.Grow(bytes)
}

// retireOne records one retired page and every retirement-tree COW page it
// generated; the generated pages must become private on the second pass
// (Rust retire_one).
func (s *DraftStore) retireOne(pageNumber uint32) error {
	root := s.draft.meta.RetirementRoot
	count := s.draft.meta.RetiredExtentCount
	generated, err := retire.AddPage(s, &root, &count, s.draft.meta.TxnID, pageNumber)
	if err != nil {
		return err
	}
	s.draft.meta.RetirementRoot = root
	s.draft.meta.RetiredExtentCount = count

	for _, page := range generated.Slice() {
		root := s.draft.meta.RetirementRoot
		count := s.draft.meta.RetiredExtentCount
		nested, err := retire.AddPage(s, &root, &count, s.draft.meta.TxnID, page)
		if err != nil {
			return err
		}
		s.draft.meta.RetirementRoot = root
		s.draft.meta.RetiredExtentCount = count
		if nested.Len() != 0 {
			return corrupt("retirement COW path did not become private")
		}
		work.PageRetired(1)
	}
	work.PageRetired(1)
	return nil
}

// drainAllocatorRetired retires the allocator backlog until it is empty
// (Rust drain_allocator_retired).
func (s *DraftStore) drainAllocatorRetired() error {
	for s.draft.allocatorRetired.Len() > 0 {
		pages := s.draft.allocatorRetired.Slice()
		s.draft.allocatorRetired.Clear()
		for _, page := range pages {
			if err := s.retireOne(page); err != nil {
				return err
			}
		}
	}
	return nil
}

// freeOne marks one page free in the free bitmap, deferring the bitmap COW
// victims to the allocator-retired backlog (Rust free_one).
func (s *DraftStore) freeOne(pageNumber uint32) error {
	root := s.draft.meta.FreeBitmapRoot
	var retired tree.RetiredPages
	if err := bitmap.SetFree(s, &root, s.draft.meta.PageCount, pageNumber, &retired); err != nil {
		return err
	}
	s.draft.meta.FreeBitmapRoot = root
	if err := s.draft.allocatorRetired.Extend(retired.Slice()); err != nil {
		return err
	}
	return s.drainAllocatorRetired()
}

// privateTag returns the dirty-chain tag of one page that must already be
// private for the draft (Rust storage::private_tag).
func privateTag(page []byte, targetTxn uint64) (uint32, error) {
	tag := format.U32(page[format.PageChecksumOffset:])
	if (ownedBy(page, targetTxn) || isPrivatePage(page, targetTxn)) && tag != 0 {
		return tag, nil
	}
	return 0, corrupt("draft update page is not private")
}

// ownedBy reports whether the page was born in the transaction (Rust
// page_header::owned_by). The page head of a claimed-but-uninitialized
// private page also carries the transaction at the born offset, so both
// shapes pass.
func ownedBy(page []byte, txn uint64) bool {
	return format.U64(page[format.HeaderBorn:]) == txn
}

// privateStackNext returns the stack link of one private page (Rust
// storage::private_stack_next).
func privateStackNext(page []byte, targetTxn uint64) (uint32, error) {
	if !isPrivatePage(page, targetTxn) {
		return 0, corrupt("private page stack link is invalid")
	}
	return format.U32(page[privateNextOff:]), nil
}

// isPrivatePage reports whether the page head carries the private marker
// of the transaction (Rust storage::is_private_page).
func isPrivatePage(page []byte, targetTxn uint64) bool {
	return format.U32(page[privateMagicOff:]) == privateMagic &&
		format.U64(page[privateTxnOff:]) == targetTxn
}

// requirePage checks the draft page-number bounds: meta pages are never
// editable and the page must be inside the draft limit (Rust
// storage::require_page).
func requirePage(pageNumber uint32, pageLimit uint64) error {
	if pageNumber < 2 || uint64(pageNumber) >= pageLimit {
		return corrupt("page number is outside draft bounds")
	}
	return nil
}

// dirtyNext follows one dirty-chain link (Rust storage::dirty_next).
func dirtyNext(tag uint32, pageNumber uint32, pageLimit uint64) (uint32, error) {
	if tag == dirtyEnd {
		return 0, nil
	}
	if tag < 2 || tag == pageNumber || uint64(tag) >= pageLimit {
		return 0, corrupt("draft dirty-page link is invalid")
	}
	return tag, nil
}

// hasPageMagic reports whether the page head carries the ordinary page
// magic: data pages do, private-stack markers do not (Rust
// page_header::has_magic).
func hasPageMagic(page []byte) bool {
	return len(page) >= 4 && bytes.Equal(page[0:4], format.PageMagic[:])
}

func corrupt(detail string) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
}

func invalid(detail string) error {
	return &format.Error{Code: format.CodeInvalidArgument, Detail: detail}
}

func budgetExceeded(detail string) error {
	return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: detail}
}

func overflow(detail string) error {
	return &format.Error{Code: format.CodeArithmeticOverflow, Detail: detail}
}

func unsupported(detail string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: detail}
}
