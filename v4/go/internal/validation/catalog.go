package validation

// Catalog validation (Rust validation/catalog.rs): the name and index
// tree walks with the record index-limit proof, the used-bitmap arm,
// and the feed bijection cross-check over the catalog authority. The
// cross-check re-reads the trees and the used bitmap through the reader
// and the raw mapping (Rust feed_catalog + bitmap contains over the
// generation), outside the graph claims.

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// validateCatalog runs the catalog validators (Rust catalog::validate):
// only membership and structured databases carry a catalog; every other
// value kind is empty.
func validateCatalog(ctx *context) error {
	if ctx.meta.ValueKind != format.ValueKindMembership && ctx.meta.ValueKind != format.ValueKindStructured {
		return nil
	}
	if err := validateCatalogTrees(ctx); err != nil {
		return err
	}
	if err := validateCatalogUsedBitmap(ctx); err != nil {
		return err
	}
	return crossCheckCatalog(ctx)
}

// catalogNameCodec is the Codec of the name tree (Rust NameCodec: the
// variable name records on both levels; an undecodable record is the
// CatalogNameInvalid class).
func catalogNameCodec() treeCodec {
	return treeCodec{
		branchType:    byte(format.PageTypeCatalogNameBranch),
		leafType:      byte(format.PageTypeCatalogNameLeaf),
		branchLayout:  format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord),
		leafLayout:    format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord),
		branchInvalid: ReasonCatalogNameInvalid,
		leafInvalid:   ReasonCatalogNameInvalid,
		branchKey: func(cell []byte) (tree.Key, bool) {
			_, name, err := format.DecodeCatalogEntry(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return tree.VarKey(name), true
		},
		branchChild: func(cell []byte) (uint32, bool) {
			index, _, err := format.DecodeCatalogEntry(cell)
			if err != nil {
				return 0, false
			}
			return index, true
		},
		leafKey: func(cell []byte) (tree.Key, bool) {
			_, name, err := format.DecodeCatalogEntry(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return tree.VarKey(name), true
		},
	}
}

// catalogIndexCodec is the Codec of the numeric index tree (Rust
// IndexCodec: fixed 8-byte branch entries, variable name records on the
// leaves, and the leaf-invalid class only; the branch shape is
// guaranteed by the fixed layout so branch decodes never fail).
func catalogIndexCodec() treeCodec {
	return treeCodec{
		branchType:    byte(format.PageTypeCatalogIndexBranch),
		leafType:      byte(format.PageTypeCatalogIndexLeaf),
		branchLayout:  format.FixedLayout(format.CatalogIndexBranchSize),
		leafLayout:    format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord),
		branchInvalid: ReasonTreeOrderInvalid,
		leafInvalid:   ReasonCatalogNameInvalid,
		branchKey: func(cell []byte) (tree.Key, bool) {
			index, _, err := format.DecodeCatalogIndexBranchFields(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return tree.Key{Lo: uint64(index)}, true
		},
		branchChild: func(cell []byte) (uint32, bool) {
			_, child, err := format.DecodeCatalogIndexBranchFields(cell)
			if err != nil {
				return 0, false
			}
			return child, true
		},
		leafKey: func(cell []byte) (tree.Key, bool) {
			index, _, err := format.DecodeCatalogEntry(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return tree.Key{Lo: uint64(index)}, true
		},
	}
}

// validateCatalogTrees walks the name and index trees and proves the
// record counts against the declared active-feed count (Rust
// validate_trees + validate_record: every served entry index must sit
// inside the declared feed-index limit).
func validateCatalogTrees(ctx *context) error {
	nameRecords, err := walkTree(ctx, ctx.meta.CatalogNameRoot, ObjectCatalogNameTree, catalogNameCodec(), catalogValidateRecord)
	if err != nil {
		return err
	}
	indexRecords, err := walkTree(ctx, ctx.meta.CatalogIndexRoot, ObjectCatalogIndexTree, catalogIndexCodec(), catalogValidateRecord)
	if err != nil {
		return err
	}
	if nameRecords.records != ctx.meta.ActiveFeedCount || indexRecords.records != ctx.meta.ActiveFeedCount {
		return catalogCountMismatch(ctx)
	}
	return nil
}

// catalogValidateRecord proves one served catalog record's index inside
// the declared limit (Rust validate_record; an undecodable record is
// already reported by the leaf-invalid class and skipped here).
func catalogValidateRecord(ctx *context, pageNumber uint32, cell []byte) error {
	index, _, err := format.DecodeCatalogEntry(cell)
	if err != nil {
		return nil
	}
	if uint64(index) >= ctx.meta.FeedIndexLimit {
		pageCopy := pageNumber
		return ctx.emit(ReasonCatalogBijectionInvalid, ObjectCatalogIndexTree, &pageCopy, nil, nil)
	}
	return nil
}

func catalogCountMismatch(ctx *context) error {
	return ctx.emit(ReasonRootCountInvalid, ObjectCatalogIndexTree, nil, nil, nil)
}

func catalogBijectionFinding(ctx *context) error {
	return ctx.emit(ReasonCatalogBijectionInvalid, ObjectCatalogIndexTree, nil, nil, nil)
}

// validateCatalogUsedBitmap walks the feed used bitmap and proves its
// set-bit count against the active-feed count (Rust
// validate_used_bitmap).
func validateCatalogUsedBitmap(ctx *context) error {
	used, err := validateBitmap(ctx, ctx.meta.FeedUsedRoot, ctx.meta.FeedIndexLimit, bitmap.KindFeed)
	if err != nil {
		return err
	}
	if used != ctx.meta.ActiveFeedCount {
		return ctx.emit(ReasonCatalogBitmapInvalid, ObjectFeedUsedBitmap, nil, nil, nil)
	}
	return nil
}

// crossCheckCatalog proves the name/index bijection feed by feed (Rust
// catalog::cross_check over the FeedCursor): every index-tree entry must
// resolve through its name and must be set in the used bitmap, and any
// lookup or cursor defect is one bijection finding.
func crossCheckCatalog(ctx *context) error {
	catalog := reader.NewImmutable(ctx.mapping, ctx.meta)
	cursor, err := catalog.NewFeedCursor()
	if err != nil {
		return catalogBijectionFinding(ctx)
	}
	for {
		if err := ctx.checkpoint(); err != nil {
			return err
		}
		entry, ok, err := cursor.Next()
		if err != nil {
			return catalogBijectionFinding(ctx)
		}
		if !ok {
			return nil
		}
		matches, err := catalogPairMatches(ctx, catalog, entry)
		if err != nil {
			return err
		}
		if !matches {
			if err := catalogBijectionFinding(ctx); err != nil {
				return err
			}
		}
	}
}

// catalogPairMatches mirrors Rust pair_matches: the name lookup must
// return the exact same entry, and the used bitmap must contain its
// index; every lookup defect is a mismatch, and the bitmap query folds
// its own defects the same way.
func catalogPairMatches(ctx *context, catalog *reader.ImmutableReader, entry reader.FeedEntry) (bool, error) {
	name, found, err := catalog.LookupFeedBytes(entry.Name)
	if err != nil || !found {
		return false, nil
	}
	if name.FeedIndex != entry.FeedIndex || !bytes.Equal(name.Name, entry.Name) {
		return false, nil
	}
	set, err := bitmapContains(ctx, ctx.meta.FeedUsedRoot, ctx.meta.FeedIndexLimit, bitmap.KindFeed, entry.FeedIndex)
	if err != nil {
		return false, nil
	}
	return set, nil
}
