package validation

// Bitmap word cache of the membership scan (Rust validation/bitmap/
// word_cache.rs): one cached leaf of word reads over the raw mapping,
// outside the graph claims. Every read re-verifies the walked headers
// (Rust require_query_header) and folds any defect into the Corrupt
// class, which the membership scan collapses into its own finding.

import (
	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// bitmapWordCache is the one-leaf cache (Rust WordCache).
type bitmapWordCache struct {
	root     uint32
	limit    uint64
	kind     bitmap.Kind
	leafBase uint64
	leafPage uint32
	cached   bool
	present  bool
}

func newBitmapWordCache(root uint32, limit uint64, kind bitmap.Kind) bitmapWordCache {
	return bitmapWordCache{root: root, limit: limit, kind: kind}
}

// word reads one global 64-bit word of the bitmap (Rust WordCache::word:
// out-of-range words and absent roots read empty).
func (w *bitmapWordCache) word(ctx *context, wordIndex uint32) (uint64, error) {
	bit := uint64(wordIndex) * 64
	if bit >= w.limit || w.root == 0 {
		return 0, nil
	}
	leafBase := bit / bitmap.LeafBits * bitmap.LeafBits
	if !w.cached || w.leafBase != leafBase {
		if err := w.loadLeaf(ctx, bit); err != nil {
			return 0, err
		}
	}
	if !w.present {
		return 0, nil
	}
	page, err := ctx.mapping.Page(w.leafPage)
	if err != nil {
		return 0, err
	}
	level := uint16(0)
	if _, err := checkedBitmapHeader(ctx, page, w.kind, &level); err != nil {
		return 0, err
	}
	return bitmap.LeafWord(page, int((bit-leafBase)/64))
}

// loadLeaf resolves and caches the leaf page covering bit (Rust
// WordCache::load_leaf: the descent re-proves every header and an absent
// child answers empty).
func (w *bitmapWordCache) loadLeaf(ctx *context, bit uint64) error {
	level, err := bitmap.RequiredLevel(w.limit)
	if err != nil {
		return formatError(err)
	}
	pageNumber := w.root
	base := uint64(0)
	for {
		page, err := ctx.mapping.Page(pageNumber)
		if err != nil {
			return err
		}
		header, err := checkedBitmapHeader(ctx, page, w.kind, &level)
		if err != nil {
			return err
		}
		if header.Level == 0 {
			w.leafBase = base
			w.leafPage = pageNumber
			w.cached = true
			w.present = true
			return nil
		}
		span, err := bitmap.Coverage(header.Level - 1)
		if err != nil {
			return formatError(err)
		}
		index := (bit - base) / span
		if index >= uint64(bitmap.BranchChildren) {
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "validated bitmap child is outside its branch"}
		}
		child, err := bitmap.BranchChild(page, int(index))
		if err != nil {
			return formatError(err)
		}
		childBase, err := bitmapWordBase(base, span, index)
		if err != nil {
			return err
		}
		if child == 0 {
			w.cached = true
			w.present = false
			return nil
		}
		base = childBase
		pageNumber = child
		level--
	}
}

// bitmapWordBase computes the child base bit of one branch slot (Rust
// WordCache::selected_child base arithmetic).
func bitmapWordBase(base, span uint64, index uint64) (uint64, error) {
	offset := span * index
	if span != 0 && offset/span != index {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation bitmap word"}
	}
	childBase := base + offset
	if childBase < base {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation bitmap word"}
	}
	return childBase, nil
}

// checkedBitmapHeader re-verifies one bitmap page header of a raw
// cross-check read (Rust require_query_header; any change is the Corrupt
// class folded into the caller's finding).
func checkedBitmapHeader(ctx *context, page []byte, kind bitmap.Kind, expectedLevel *uint16) (bitmap.Header, error) {
	header, problem := bitmap.CheckedHeader(page, ctx.meta.TxnID, kind, expectedLevel)
	if problem != bitmap.HeaderProblemNone {
		return bitmap.Header{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "validated bitmap changed during cross-check"}
	}
	return header, nil
}
