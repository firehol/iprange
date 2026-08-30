// Per-family routing seam for the gap/replace layer (SOW-0027
// regression slice I-1b): the generic writer machinery dispatches each
// tree-core gap call to the emitted family-specialized entry of
// internal/tree (range_gap_v4.go / range_gap_v6.go) when the context
// carries a concrete codec, exactly like Rust monomorphizes
// fixed_tree/gap.rs per RangeCodec<Ipv4Key>/<Ipv6Key>. The dynamic
// type switch runs once per record mutation and replaces the interface
// dispatch on the codec, the gap probe, and the selector of the whole
// call chain; the compiler cannot prove the switch cases for a generic
// type parameter, so the switch operates on the erased interface and
// the typed results convert back through one dynamic assertion. The
// generic tree entries remain the reference implementation for any
// future family, so the default branch keeps calling them.

package writer

import (
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// gapLocalInsert routes InsertIfLocalGap to the emitted family entry.
// The rejection proof is written through the caller-owned reject slot
// (the private input's slot on the locator path, the context slot
// otherwise) and the emitted entry returns only the compact
// inserted/page outcome: the hot overwrite path never moves the
// ~340-byte LocalInsert by value across the seam.
func gapLocalInsert[K any](ctx *rangeCtx[K], r rangeRecord[K], cell []byte, retired tree.RetiredPages, reject *tree.LocalReject[rangeRecord[K]]) (tree.RetiredPages, tree.LocalGapOutcome, error) {
	switch family := any(ctx.family).(type) {
	case tree.RangeCodec4:
		retired, outcome, err := tree.InsertIfLocalGap4(family, ctx.storeView, ctx.root, cell, retired, any(r).(tree.RangeRecord[tree.RangeKey4]), any(reject).(*tree.LocalReject[tree.RangeRecord[tree.RangeKey4]]))
		if err != nil {
			return tree.RetiredPages{}, tree.LocalGapOutcome{}, err
		}
		return retired, outcome, nil
	case tree.RangeCodec6:
		retired, outcome, err := tree.InsertIfLocalGap6(family, ctx.storeView, ctx.root, cell, retired, any(r).(tree.RangeRecord[tree.RangeKey6]), any(reject).(*tree.LocalReject[tree.RangeRecord[tree.RangeKey6]]))
		if err != nil {
			return tree.RetiredPages{}, tree.LocalGapOutcome{}, err
		}
		return retired, outcome, nil
	default:
		retired, result, err := tree.InsertIfLocalGap(ctx.family, ctx.storeView, ctx.root, cell, retired, privateGap[K]{Family: ctx.family, R: r})
		if err != nil {
			return tree.RetiredPages{}, tree.LocalGapOutcome{}, err
		}
		outcome := tree.LocalGapOutcome{Inserted: result.Inserted, PageNumber: result.PageNumber}
		if result.Rejected {
			*reject = result.Reject
		}
		return retired, outcome, nil
	}
}

// gapCachedInterior routes InsertIfCachedInteriorGap to the emitted
// family entry.
func gapCachedInterior[K any](ctx *rangeCtx[K], r rangeRecord[K], cell []byte, pageNumber uint32) (tree.CachedInsert, error) {
	switch family := any(ctx.family).(type) {
	case tree.RangeCodec4:
		return tree.InsertIfCachedInteriorGap4(family, ctx.store, pageNumber, cell, any(r).(tree.RangeRecord[tree.RangeKey4]))
	case tree.RangeCodec6:
		return tree.InsertIfCachedInteriorGap6(family, ctx.store, pageNumber, cell, any(r).(tree.RangeRecord[tree.RangeKey6]))
	default:
		return tree.InsertIfCachedInteriorGap(ctx.family, ctx.store, pageNumber, cell, privateGap[K]{Family: ctx.family, R: r})
	}
}

// gapEdgeInsert routes InsertIfEdgeGap to the emitted family entry.
func gapEdgeInsert[K any](ctx *rangeCtx[K], incoming rangeRecord[K], cell []byte, position *tree.PrivateEdge, edge tree.Edge, knownGap bool) (tree.EdgeInsert[rangeRecord[K]], error) {
	switch family := any(ctx.family).(type) {
	case tree.RangeCodec4:
		result, err := tree.InsertIfEdgeGap4(family, ctx.storeView, ctx.root, cell, position, edge, knownGap, any(incoming).(tree.RangeRecord[tree.RangeKey4]))
		if err != nil {
			return tree.EdgeInsert[rangeRecord[K]]{}, err
		}
		return any(result).(tree.EdgeInsert[rangeRecord[K]]), nil
	case tree.RangeCodec6:
		result, err := tree.InsertIfEdgeGap6(family, ctx.storeView, ctx.root, cell, position, edge, knownGap, any(incoming).(tree.RangeRecord[tree.RangeKey6]))
		if err != nil {
			return tree.EdgeInsert[rangeRecord[K]]{}, err
		}
		return any(result).(tree.EdgeInsert[rangeRecord[K]]), nil
	default:
		return tree.InsertIfEdgeGap(ctx.family, ctx.storeView, ctx.root, cell, position, edge, knownGap, privateGap[K]{Family: ctx.family, R: incoming})
	}
}

// gapRejectedInsert routes InsertRejectedGap to the emitted family
// entry.
func gapRejectedInsert[K any](ctx *rangeCtx[K], cell []byte, rejected tree.LocalReject[rangeRecord[K]]) (tree.PrivatePosition, bool, error) {
	switch family := any(ctx.family).(type) {
	case tree.RangeCodec4:
		return tree.InsertRejectedGap4(family, ctx.storeView, ctx.root, cell, any(rejected).(tree.LocalReject[tree.RangeRecord[tree.RangeKey4]]))
	case tree.RangeCodec6:
		return tree.InsertRejectedGap6(family, ctx.storeView, ctx.root, cell, any(rejected).(tree.LocalReject[tree.RangeRecord[tree.RangeKey6]]))
	default:
		return tree.InsertRejectedGap(ctx.family, ctx.storeView, ctx.root, cell, rejected)
	}
}

// predecessorReplace routes ReplaceLocalPredecessorWith to the emitted
// family entry. The rejection proof travels by pointer end to end: the
// emitted entries dereference the caller-owned slot, so the seam never
// copies the ~340-byte record into a type assertion.
func predecessorReplace[K any](ctx *rangeCtx[K], rejected *tree.LocalReject[rangeRecord[K]], key K, cells [][]byte) error {
	switch family := any(ctx.family).(type) {
	case tree.RangeCodec4:
		return tree.ReplaceLocalPredecessorWith4(family, ctx.storeView, ctx.root, any(rejected).(*tree.LocalReject[tree.RangeRecord[tree.RangeKey4]]), ctx.family.KeyOf(key), cells)
	case tree.RangeCodec6:
		return tree.ReplaceLocalPredecessorWith6(family, ctx.storeView, ctx.root, any(rejected).(*tree.LocalReject[tree.RangeRecord[tree.RangeKey6]]), ctx.family.KeyOf(key), cells)
	default:
		return tree.ReplaceLocalPredecessorWith(ctx.family, ctx.storeView, ctx.root, *rejected, ctx.family.KeyOf(key), cells)
	}
}

// localRunReplace routes ReplaceLocalRun to the emitted family entry.
func localRunReplace[K any](ctx *rangeCtx[K], rejected *tree.LocalReject[rangeRecord[K]], run tree.LocalRun, replacement []byte) error {
	switch family := any(ctx.family).(type) {
	case tree.RangeCodec4:
		return tree.ReplaceLocalRun4(family, ctx.storeView, ctx.root, any(rejected).(*tree.LocalReject[tree.RangeRecord[tree.RangeKey4]]), run, replacement)
	case tree.RangeCodec6:
		return tree.ReplaceLocalRun6(family, ctx.storeView, ctx.root, any(rejected).(*tree.LocalReject[tree.RangeRecord[tree.RangeKey6]]), run, replacement)
	default:
		return tree.ReplaceLocalRun(ctx.family, ctx.storeView, ctx.root, *rejected, run, replacement)
	}
}
