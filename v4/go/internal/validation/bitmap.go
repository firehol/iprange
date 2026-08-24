package validation

// Bitmap validation (Rust validation/bitmap.rs): the hierarchical bitmap
// walk with the exact reason mapping (header problems, out-of-range
// summary bits, summary-bit and item-count disagreements), the bit
// accounting, and the free-kind allocation marking. The catalog
// used-bitmap arm composes this walk; the free/membership/structure
// arms arrive with their slices.

import (
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// bitmapObject maps one bitmap kind to its validation object (Rust
// ValidationKind::object).
func bitmapObject(kind bitmap.Kind) ValidationObject {
	switch kind {
	case bitmap.KindFree:
		return ObjectFreeBitmap
	case bitmap.KindFeed:
		return ObjectFeedUsedBitmap
	case bitmap.KindMembership:
		return ObjectMembershipUsedBitmap
	default:
		return ObjectStructureUsedBitmap
	}
}

// validateBitmap walks one bitmap root and returns the count of set
// bits (Rust bitmap::validate): a zero root is empty, a zero limit with
// a root is the BitmapSummaryInvalid class, and the walk follows the
// graph claims of the kind's object.
func validateBitmap(ctx *context, root uint32, limit uint64, kind bitmap.Kind) (uint64, error) {
	if root == 0 {
		return 0, nil
	}
	if limit == 0 {
		if err := ctx.emit(ReasonBitmapSummaryInvalid, bitmapObject(kind), &root, nil, nil); err != nil {
			return 0, err
		}
		return 0, nil
	}
	required, err := bitmap.RequiredLevel(limit)
	if err != nil {
		return 0, formatError(err)
	}
	var path [bitmap.MaxLevel + 1]uint32
	result, _, err := validateBitmapNode(ctx, root, required, 0, limit, kind, &path, 0)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, nil
	}
	return result.setBits, nil
}

// bitmapNodeResult mirrors Rust NodeResult: the counted set bits and
// the has-one/has-candidate summary contributions.
type bitmapNodeResult struct {
	setBits      uint64
	hasOne       bool
	hasCandidate bool
}

// validateBitmapNode visits one bitmap node (Rust validate_node: the
// path slot, the graph claim, the classified header, and the
// leaf/branch split).
func validateBitmapNode(ctx *context, pageNumber uint32, expectedLevel uint16, base, limit uint64, kind bitmap.Kind, path *[bitmap.MaxLevel + 1]uint32, depth int) (*bitmapNodeResult, uint16, error) {
	if depth >= len(path) {
		if err := ctx.emit(ReasonTreeLevelInvalid, bitmapObject(kind), &pageNumber, nil, nil); err != nil {
			return nil, 0, err
		}
		return nil, 0, nil
	}
	path[depth] = pageNumber
	page, err := ctx.readGraphPage(pageNumber, bitmapObject(kind), path[:depth])
	if err != nil || page == nil {
		return nil, 0, err
	}
	header, problem := bitmap.CheckedHeader(page, ctx.meta.TxnID, kind, &expectedLevel)
	if problem != bitmap.HeaderProblemNone {
		if err := ctx.emit(bitmapHeaderProblemReason(problem), bitmapObject(kind), &pageNumber, nil, nil); err != nil {
			return nil, 0, err
		}
		return nil, 0, nil
	}
	if !bitmap.ReservedZero(page, header.Level) {
		if err := ctx.emit(ReasonPageReservedNonzero, bitmapObject(kind), &pageNumber, nil, nil); err != nil {
			return nil, 0, err
		}
	}
	if header.Level == 0 {
		result, err := validateBitmapLeaf(ctx, pageNumber, page, base, limit, kind, header)
		return result, header.Level, err
	}
	result, err := validateBitmapBranch(ctx, pageNumber, page, base, limit, kind, header, path, depth)
	return result, header.Level, err
}

// bitmapHeaderProblemReason maps one bitmap header problem to its reason
// class (Rust emit_header_problem).
func bitmapHeaderProblemReason(problem bitmap.HeaderProblem) ValidationReason {
	switch problem {
	case bitmap.HeaderProblemBorn:
		return ReasonPageBornTxnInvalid
	case bitmap.HeaderProblemLevel:
		return ReasonTreeLevelInvalid
	case bitmap.HeaderProblemType:
		return ReasonPageTypeMismatch
	default:
		return ReasonPageHeaderInvalid
	}
}

// validateBitmapLeaf validates the 500 words of one bitmap leaf (Rust
// validate_leaf): every out-of-range bit is the BitmapSummaryInvalid
// class, the nonzero-word count must equal the item count, and the
// free kind marks every set bit in the allocation partition.
func validateBitmapLeaf(ctx *context, pageNumber uint32, page []byte, base, limit uint64, kind bitmap.Kind, header bitmap.Header) (*bitmapNodeResult, error) {
	totals := bitmapLeafTotals{}
	for index := 0; index < bitmap.LeafWords; index++ {
		word, err := bitmap.LeafWord(page, index)
		if err != nil {
			return nil, formatError(err)
		}
		wordBase, valid, validMask, err := validateBitmapWord(ctx, pageNumber, base, limit, kind, index, word)
		if err != nil {
			return nil, err
		}
		if err := totals.add(ctx, wordBase, word, valid, validMask, kind); err != nil {
			return nil, err
		}
	}
	if totals.nonzeroWords != uint64(header.ItemCount) {
		if err := ctx.emit(ReasonPageHeaderInvalid, bitmapObject(kind), &pageNumber, nil, nil); err != nil {
			return nil, err
		}
	}
	result := totals.result()
	return result, nil
}

// bitmapLeafTotals mirrors Rust LeafTotals.
type bitmapLeafTotals struct {
	nonzeroWords uint64
	setBits      uint64
	hasCandidate bool
}

func (t *bitmapLeafTotals) add(ctx *context, wordBase uint64, word, valid, validMask uint64, kind bitmap.Kind) error {
	if word != 0 {
		t.nonzeroWords++
	}
	ones := uint64(bits.OnesCount64(valid))
	if t.setBits > ^uint64(0)-ones {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation bitmap bit count"}
	}
	t.setBits += ones
	switch kind {
	case bitmap.KindFree:
		return markFreeBits(ctx, wordBase, valid)
	case bitmap.KindFeed, bitmap.KindMembership, bitmap.KindStructure:
		if (^valid)&validMask != 0 {
			t.hasCandidate = true
		}
	}
	return nil
}

func (t *bitmapLeafTotals) result() *bitmapNodeResult {
	return &bitmapNodeResult{setBits: t.setBits, hasOne: t.setBits != 0, hasCandidate: t.hasCandidate}
}

// validateBitmapWord validates one 64-bit word against the limit window
// (Rust validate_leaf_word): bits outside the valid range are the
// BitmapSummaryInvalid class and the returned valid word is bit-masked.
func validateBitmapWord(ctx *context, pageNumber uint32, base, limit uint64, kind bitmap.Kind, index int, word uint64) (uint64, uint64, uint64, error) {
	wordBase := base + uint64(index*64)
	if wordBase < base {
		return 0, 0, 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation bitmap word offset"}
	}
	validMask := inRangeMask(wordBase, limit, kind)
	if word&^validMask != 0 {
		if err := ctx.emit(ReasonBitmapSummaryInvalid, bitmapObject(kind), &pageNumber, nil, nil); err != nil {
			return 0, 0, 0, err
		}
	}
	return wordBase, word & validMask, validMask, nil
}

// inRangeMask mirrors Rust in_range_mask: the valid bit window of one
// word (limit, the kind's first candidate, and the free bitmap's
// reserved page-0 pair).
func inRangeMask(base, limit uint64, kind bitmap.Kind) uint64 {
	if base >= limit {
		return 0
	}
	valid := limit - base
	if valid > 64 {
		valid = 64
	}
	var mask uint64
	if valid == 64 {
		mask = ^uint64(0)
	} else {
		mask = (1 << valid) - 1
	}
	first := kind.FirstCandidate()
	if base < first {
		excluded := first - base
		if excluded > 64 {
			excluded = 64
		}
		mask &= ^uint64(0) << excluded
	}
	if kind == bitmap.KindFree && base == 0 {
		mask &^= uint64(3)
	}
	return mask
}

// markFreeBits marks every set bit of one free word in the allocation
// partition (Rust mark_free_bits).
func markFreeBits(ctx *context, wordBase uint64, w uint64) error {
	for w != 0 {
		bit := uint64(bits.TrailingZeros64(w))
		page := wordBase + bit
		if page < wordBase {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation free-page number"}
		}
		if err := ctx.markAllocated(uint32(page), ObjectFreeBitmap); err != nil {
			return err
		}
		w &= w - 1
	}
	return nil
}

// validateBitmapBranch validates the 256 children of one bitmap branch
// (Rust validate_branch): the nonzero-child count must equal the item
// count, every present child's summary bit must equal the expected
// contribution of its subtree, and absent children of a non-free kind
// still contribute their candidate span.
func validateBitmapBranch(ctx *context, pageNumber uint32, page []byte, base, limit uint64, kind bitmap.Kind, header bitmap.Header, path *[bitmap.MaxLevel + 1]uint32, depth int) (*bitmapNodeResult, error) {
	childSpan, err := bitmap.Coverage(header.Level - 1)
	if err != nil {
		return nil, formatError(err)
	}
	totals := bitmapBranchTotals{}
	for index := 0; index < bitmap.BranchChildren; index++ {
		child, err := bitmap.BranchChild(page, index)
		if err != nil {
			return nil, formatError(err)
		}
		if child != 0 {
			totals.childCount++
		}
		var result *bitmapNodeResult
		if child == 0 {
			childBase, err := bitmapChildBase(base, childSpan, index)
			if err != nil {
				return nil, err
			}
			result = absentBitmapResult(childBase, childSpan, limit, kind)
		} else {
			childBase, err := bitmapChildBase(base, childSpan, index)
			if err != nil {
				return nil, err
			}
			result, _, err = validateBitmapNode(ctx, child, header.Level-1, childBase, limit, kind, path, depth+1)
			if err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}
		}
		if err := totals.add(ctx, pageNumber, page, index, kind, result); err != nil {
			return nil, err
		}
	}
	if totals.childCount != uint64(header.ItemCount) {
		if err := ctx.emit(ReasonPageHeaderInvalid, bitmapObject(kind), &pageNumber, nil, nil); err != nil {
			return nil, err
		}
	}
	result := totals.result()
	return result, nil
}

// bitmapBranchTotals mirrors Rust BranchTotals.
type bitmapBranchTotals struct {
	childCount   uint64
	setBits      uint64
	hasOne       bool
	hasCandidate bool
}

func (t *bitmapBranchTotals) add(ctx *context, pageNumber uint32, page []byte, index int, kind bitmap.Kind, result *bitmapNodeResult) error {
	expected := result.hasOne
	if kind != bitmap.KindFree {
		expected = result.hasCandidate
	}
	summary, err := bitmap.SummaryBit(page, index)
	if err != nil {
		return formatError(err)
	}
	if summary != expected {
		if err := ctx.emit(ReasonBitmapSummaryInvalid, bitmapObject(kind), &pageNumber, nil, nil); err != nil {
			return err
		}
	}
	if t.setBits > ^uint64(0)-result.setBits {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation bitmap bit count"}
	}
	t.setBits += result.setBits
	t.hasOne = t.hasOne || result.hasOne
	t.hasCandidate = t.hasCandidate || result.hasCandidate
	return nil
}

func (t *bitmapBranchTotals) result() *bitmapNodeResult {
	return &bitmapNodeResult{setBits: t.setBits, hasOne: t.hasOne, hasCandidate: t.hasCandidate}
}

// bitmapChildBase computes the base bit of one child span (Rust
// bitmap_child_base).
func bitmapChildBase(base, span uint64, index int) (uint64, error) {
	offset := span * uint64(index)
	if span != 0 && offset/span != uint64(index) {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation bitmap child offset"}
	}
	childBase := base + offset
	if childBase < base {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation bitmap child offset"}
	}
	return childBase, nil
}

// absentBitmapResult mirrors Rust absent_result: an absent child of a
// non-free kind still contributes its candidate span.
func absentBitmapResult(base, span, limit uint64, kind bitmap.Kind) *bitmapNodeResult {
	end := base + span
	if end < base || end > limit {
		end = limit
	}
	candidate := false
	if kind != bitmap.KindFree {
		first := kind.FirstCandidate()
		low := base
		if low < first {
			low = first
		}
		candidate = low < end
	}
	return &bitmapNodeResult{setBits: 0, hasOne: false, hasCandidate: candidate}
}

// validateFreeBitmap runs the free bitmap validator over the whole
// committed generation (Rust validate_selected's bitmap::validate with
// Kind::Free and the page count as the bit limit).
func validateFreeBitmap(ctx *context) error {
	_, err := validateBitmap(ctx, ctx.meta.FreeBitmapRoot, ctx.meta.PageCount, bitmap.KindFree)
	return err
}
