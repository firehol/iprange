//go:build v4work

// Differential tests for the emitted per-family gap/replace layer
// (SOW-0027 regression slice I-1b): every emitted entry in
// range_gap_v4.go / range_gap_v6.go must behave exactly like its
// generic reference in gap.go for the same operation stream. Two
// memory stores replay identical randomized range mutations - local
// gap inserts, cached interior inserts, rejected-gap completions,
// predecessor replacements, local-run replacements, and edge inserts -
// and every step must agree on the outcome, the necessary-work
// counters, and the resulting byte-identical page state. The counters
// exist only in the v4work build, so this file is v4work-tagged like
// the other necessary-work tests.

package tree

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// rangeDiffCodec is the codec surface the differential driver needs:
// the tree codec plus the family record encoder.
type rangeDiffCodec[T any] interface {
	Codec[T]
	EncodeRecord(r T, output []byte) (int, error)
}

// insertedRejectedOutcome is the comparable result of one
// InsertRejectedGap call.
type insertedRejectedOutcome struct {
	Position PrivatePosition
	Fits     bool
}

// rangeDiffSeam is one implementation of the gap/replace layer for a
// concrete record type: either the generic gap.go reference or one
// emitted per-family entry set.
type rangeDiffSeam[T any] struct {
	name string
	// insertLocal is InsertIfLocalGap (generic) or
	// InsertIfLocalGap4/6 (emitted).
	insertLocal func(codec Codec[T], store Store, root *uint32, cell []byte, retired RetiredPages, r T) (RetiredPages, LocalInsert[T], error)
	// cachedInterior is InsertIfCachedInteriorGap (generic or emitted).
	cachedInterior func(codec Codec[T], store Store, pageNumber uint32, cell []byte, r T) (CachedInsert, error)
	// insertRejected is InsertRejectedGap (generic or emitted).
	insertRejected func(codec Codec[T], store Store, root *uint32, cell []byte, rejected LocalReject[T]) (PrivatePosition, bool, error)
	// replacePredecessor is ReplaceLocalPredecessorWith (generic or
	// emitted).
	replacePredecessor func(codec Codec[T], store Store, root *uint32, rejected LocalReject[T], key Key, cells [][]byte) error
	// replaceRun is ReplaceLocalRun (generic or emitted).
	replaceRun func(codec Codec[T], store Store, root *uint32, rejected LocalReject[T], run LocalRun, replacement []byte) error
	// edgeGap is InsertIfEdgeGap (generic or emitted).
	edgeGap func(codec Codec[T], store Store, root *uint32, cell []byte, cached *PrivateEdge, edge Edge, knownGap bool, r T) (EdgeInsert[T], error)
}

// genericSeam4 routes every call to the generic gap.go reference over
// the v4 family.
var genericSeam4 = rangeDiffSeam[RangeRecord[RangeKey4]]{
	name: "generic-v4",
	insertLocal: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, cell []byte, retired RetiredPages, r RangeRecord[RangeKey4]) (RetiredPages, LocalInsert[RangeRecord[RangeKey4]], error) {
		gap := RangePrivateGap[RangeKey4]{Family: codec.(RangeFamily[RangeKey4]), R: r}
		return InsertIfLocalGap(codec, store, root, cell, retired, gap)
	},
	cachedInterior: func(codec Codec[RangeRecord[RangeKey4]], store Store, pageNumber uint32, cell []byte, r RangeRecord[RangeKey4]) (CachedInsert, error) {
		gap := RangePrivateGap[RangeKey4]{Family: codec.(RangeFamily[RangeKey4]), R: r}
		return InsertIfCachedInteriorGap(codec, store, pageNumber, cell, gap)
	},
	insertRejected: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, cell []byte, rejected LocalReject[RangeRecord[RangeKey4]]) (PrivatePosition, bool, error) {
		return InsertRejectedGap(codec, store, root, cell, rejected)
	},
	replacePredecessor: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey4]], key Key, cells [][]byte) error {
		return ReplaceLocalPredecessorWith(codec, store, root, rejected, key, cells)
	},
	replaceRun: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey4]], run LocalRun, replacement []byte) error {
		return ReplaceLocalRun(codec, store, root, rejected, run, replacement)
	},
	edgeGap: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, cell []byte, cached *PrivateEdge, edge Edge, knownGap bool, r RangeRecord[RangeKey4]) (EdgeInsert[RangeRecord[RangeKey4]], error) {
		gap := RangePrivateGap[RangeKey4]{Family: codec.(RangeFamily[RangeKey4]), R: r}
		return InsertIfEdgeGap(codec, store, root, cell, cached, edge, knownGap, gap)
	},
}

// emittedSeam4 routes every call to the emitted v4 layer.
var emittedSeam4 = rangeDiffSeam[RangeRecord[RangeKey4]]{
	name: "emitted-v4",
	insertLocal: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, cell []byte, retired RetiredPages, r RangeRecord[RangeKey4]) (RetiredPages, LocalInsert[RangeRecord[RangeKey4]], error) {
		var reject LocalReject[RangeRecord[RangeKey4]]
		retired, outcome, err := InsertIfLocalGap4(codec.(RangeCodec4), store, root, cell, retired, r, &reject)
		if err != nil {
			return RetiredPages{}, LocalInsert[RangeRecord[RangeKey4]]{}, err
		}
		if outcome.Inserted {
			return retired, LocalInsert[RangeRecord[RangeKey4]]{Inserted: true, PageNumber: outcome.PageNumber}, nil
		}
		return retired, LocalInsert[RangeRecord[RangeKey4]]{Reject: reject, Rejected: true}, nil
	},
	cachedInterior: func(codec Codec[RangeRecord[RangeKey4]], store Store, pageNumber uint32, cell []byte, r RangeRecord[RangeKey4]) (CachedInsert, error) {
		return InsertIfCachedInteriorGap4(codec.(RangeCodec4), store, pageNumber, cell, r)
	},
	insertRejected: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, cell []byte, rejected LocalReject[RangeRecord[RangeKey4]]) (PrivatePosition, bool, error) {
		return InsertRejectedGap4(codec.(RangeCodec4), store, root, cell, rejected)
	},
	replacePredecessor: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey4]], key Key, cells [][]byte) error {
		return ReplaceLocalPredecessorWith4(codec.(RangeCodec4), store, root, &rejected, key, cells)
	},
	replaceRun: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey4]], run LocalRun, replacement []byte) error {
		return ReplaceLocalRun4(codec.(RangeCodec4), store, root, &rejected, run, replacement)
	},
	edgeGap: func(codec Codec[RangeRecord[RangeKey4]], store Store, root *uint32, cell []byte, cached *PrivateEdge, edge Edge, knownGap bool, r RangeRecord[RangeKey4]) (EdgeInsert[RangeRecord[RangeKey4]], error) {
		return InsertIfEdgeGap4(codec.(RangeCodec4), store, root, cell, cached, edge, knownGap, r)
	},
}

// genericSeam6 routes every call to the generic gap.go reference over
// the v6 family.
var genericSeam6 = rangeDiffSeam[RangeRecord[RangeKey6]]{
	name: "generic-v6",
	insertLocal: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, cell []byte, retired RetiredPages, r RangeRecord[RangeKey6]) (RetiredPages, LocalInsert[RangeRecord[RangeKey6]], error) {
		gap := RangePrivateGap[RangeKey6]{Family: codec.(RangeFamily[RangeKey6]), R: r}
		return InsertIfLocalGap(codec, store, root, cell, retired, gap)
	},
	cachedInterior: func(codec Codec[RangeRecord[RangeKey6]], store Store, pageNumber uint32, cell []byte, r RangeRecord[RangeKey6]) (CachedInsert, error) {
		gap := RangePrivateGap[RangeKey6]{Family: codec.(RangeFamily[RangeKey6]), R: r}
		return InsertIfCachedInteriorGap(codec, store, pageNumber, cell, gap)
	},
	insertRejected: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, cell []byte, rejected LocalReject[RangeRecord[RangeKey6]]) (PrivatePosition, bool, error) {
		return InsertRejectedGap(codec, store, root, cell, rejected)
	},
	replacePredecessor: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey6]], key Key, cells [][]byte) error {
		return ReplaceLocalPredecessorWith(codec, store, root, rejected, key, cells)
	},
	replaceRun: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey6]], run LocalRun, replacement []byte) error {
		return ReplaceLocalRun(codec, store, root, rejected, run, replacement)
	},
	edgeGap: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, cell []byte, cached *PrivateEdge, edge Edge, knownGap bool, r RangeRecord[RangeKey6]) (EdgeInsert[RangeRecord[RangeKey6]], error) {
		gap := RangePrivateGap[RangeKey6]{Family: codec.(RangeFamily[RangeKey6]), R: r}
		return InsertIfEdgeGap(codec, store, root, cell, cached, edge, knownGap, gap)
	},
}

// emittedSeam6 routes every call to the emitted v6 layer.
var emittedSeam6 = rangeDiffSeam[RangeRecord[RangeKey6]]{
	name: "emitted-v6",
	insertLocal: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, cell []byte, retired RetiredPages, r RangeRecord[RangeKey6]) (RetiredPages, LocalInsert[RangeRecord[RangeKey6]], error) {
		var reject LocalReject[RangeRecord[RangeKey6]]
		retired, outcome, err := InsertIfLocalGap6(codec.(RangeCodec6), store, root, cell, retired, r, &reject)
		if err != nil {
			return RetiredPages{}, LocalInsert[RangeRecord[RangeKey6]]{}, err
		}
		if outcome.Inserted {
			return retired, LocalInsert[RangeRecord[RangeKey6]]{Inserted: true, PageNumber: outcome.PageNumber}, nil
		}
		return retired, LocalInsert[RangeRecord[RangeKey6]]{Reject: reject, Rejected: true}, nil
	},
	cachedInterior: func(codec Codec[RangeRecord[RangeKey6]], store Store, pageNumber uint32, cell []byte, r RangeRecord[RangeKey6]) (CachedInsert, error) {
		return InsertIfCachedInteriorGap6(codec.(RangeCodec6), store, pageNumber, cell, r)
	},
	insertRejected: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, cell []byte, rejected LocalReject[RangeRecord[RangeKey6]]) (PrivatePosition, bool, error) {
		return InsertRejectedGap6(codec.(RangeCodec6), store, root, cell, rejected)
	},
	replacePredecessor: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey6]], key Key, cells [][]byte) error {
		return ReplaceLocalPredecessorWith6(codec.(RangeCodec6), store, root, &rejected, key, cells)
	},
	replaceRun: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, rejected LocalReject[RangeRecord[RangeKey6]], run LocalRun, replacement []byte) error {
		return ReplaceLocalRun6(codec.(RangeCodec6), store, root, &rejected, run, replacement)
	},
	edgeGap: func(codec Codec[RangeRecord[RangeKey6]], store Store, root *uint32, cell []byte, cached *PrivateEdge, edge Edge, knownGap bool, r RangeRecord[RangeKey6]) (EdgeInsert[RangeRecord[RangeKey6]], error) {
		return InsertIfEdgeGap6(codec.(RangeCodec6), store, root, cell, cached, edge, knownGap, r)
	},
}

// counterDeltas returns every work counter change between two snapshots
// as a field-to-delta map.
func counterDeltas(before, after work.Snapshot) map[string]uint64 {
	delta := map[string]uint64{}
	bv := reflect.ValueOf(before)
	av := reflect.ValueOf(after)
	for i := 0; i < av.NumField(); i++ {
		name := av.Type().Field(i).Name
		d := av.Field(i).Uint() - bv.Field(i).Uint()
		if d != 0 {
			delta[name] = d
		}
	}
	return delta
}

func requireSameError(t *testing.T, op string, errA, errB error) {
	t.Helper()
	if (errA == nil) != (errB == nil) {
		t.Fatalf("%s: generic %v, emitted %v", op, errA, errB)
	}
	if errA != nil && errA.Error() != errB.Error() {
		t.Fatalf("%s: generic %q, emitted %q", op, errA, errB)
	}
}

// diffStep is one differential run: two stores, one op stream, every
// op compared between the generic and the emitted layer.
type diffStep[T any] struct {
	t             *testing.T
	codec         rangeDiffCodec[T]
	generic, emit rangeDiffSeam[T]
	mg, me        *memoryStore
	rootG, rootE  uint32
	step          int
	mutations     int
	// family helpers
	genRecord  func(rng *rand.Rand) T
	split      func(T) (T, bool)
	recordFrom func(T) Key
	mergePair  func(a, b T) T
	// beyond builds a record strictly beyond one edge boundary record
	// when the address space allows it.
	beyond func(rng *rand.Rand, boundary T, edge Edge) (T, bool)
}

func (d *diffStep[T]) fatal(format string, args ...any) {
	d.t.Helper()
	d.t.Fatalf("step %d: %s", d.step, fmt.Sprintf(format, args...))
}

// execute runs the same call on both lanes and compares the error, the
// work counter delta, and the returned values.
func (d *diffStep[T]) execute(op string, run func(seam rangeDiffSeam[T], store Store, root *uint32) (any, error)) {
	d.t.Helper()
	d.step++
	beforeG := work.Read()
	outG, errG := run(d.generic, d.mg, &d.rootG)
	deltaG := counterDeltas(beforeG, work.Read())
	beforeE := work.Read()
	outE, errE := run(d.emit, d.me, &d.rootE)
	deltaE := counterDeltas(beforeE, work.Read())
	requireSameError(d.t, op, errG, errE)
	if !reflect.DeepEqual(deltaG, deltaE) {
		d.fatal("%s: work counters generic %v, emitted %v", op, deltaG, deltaE)
	}
	d.mutations++
	if errG != nil {
		// An identical rejected operation is itself differential
		// coverage (both lanes refused the same way); the stores are
		// untouched, so the stream continues.
		return
	}
	if !reflect.DeepEqual(outG, outE) {
		d.fatal("%s: generic %#v, emitted %#v", op, outG, outE)
	}
}

// edgeBoundary decodes the first or last record of the single-leaf
// tree.
func (d *diffStep[T]) edgeBoundary(store *memoryStore, root uint32, edge Edge) (T, bool) {
	var zero T
	page, err := store.Inspect(root)
	if err != nil {
		return zero, false
	}
	header, err := parse(d.codec, page, store.TargetTxn(), 0, false)
	if err != nil || header.Level != 0 || header.ItemCount == 0 {
		return zero, false
	}
	index := 0
	if edge == EdgeLast {
		index = int(header.ItemCount) - 1
	}
	cell, err := codecCell(d.codec, page, &header, index)
	if err != nil {
		return zero, false
	}
	record, err := d.codec.ReadLeaf(cell)
	return record, err == nil
}

// privateLeaves lists the private leaf pages with at least minItems
// records on one store (both stores are byte-identical, so one side
// suffices).
func (d *diffStep[T]) privateLeaves(store *memoryStore, minItems int) []uint32 {
	var leaves []uint32
	for i := 1; i < len(store.pages); i++ {
		page, err := store.Inspect(uint32(i))
		if err != nil {
			continue
		}
		if format.U64(page[format.HeaderBorn:]) != store.TargetTxn() {
			continue
		}
		header, err := parse(d.codec, page, store.TargetTxn(), 0, false)
		if err != nil || header.Level != 0 || header.ItemCount < uint16(minItems) {
			continue
		}
		leaves = append(leaves, uint32(i))
	}
	return leaves
}

// singleLeaf reports whether the tree is exactly one private leaf.
func (d *diffStep[T]) singleLeaf(store *memoryStore, root uint32) bool {
	if root == 0 {
		return false
	}
	page, err := store.Inspect(root)
	if err != nil {
		return false
	}
	header, err := parse(d.codec, page, store.TargetTxn(), 0, false)
	return err == nil && header.Level == 0 && format.U64(page[format.HeaderBorn:]) == store.TargetTxn()
}

// encode renders one record into a fresh leaf cell.
func (d *diffStep[T]) encode(r T) []byte {
	cell := make([]byte, d.codec.LeafSize())
	if _, err := d.codec.EncodeRecord(r, cell); err != nil {
		d.t.Fatalf("encode: %v", err)
	}
	return cell
}

// insert runs one InsertIfLocalGap step on both lanes and returns the
// rejection for the follow-up mutation ops, when the gap closed.
func (d *diffStep[T]) insert(rng *rand.Rand) *LocalReject[T] {
	r := d.genRecord(rng)
	cell := d.encode(r)
	var rejected *LocalReject[T]
	d.execute("insert-local", func(seam rangeDiffSeam[T], store Store, root *uint32) (any, error) {
		retired, result, err := seam.insertLocal(d.codec, store, root, cell, RetiredPages{}, r)
		if err != nil {
			return nil, err
		}
		if retired.Len() != 0 {
			return nil, corrupt("diff insert retired a page")
		}
		if result.Rejected {
			rej := result.Reject
			rejected = &rej
		}
		return result, nil
	})
	return rejected
}

// cachedInterior runs one InsertIfCachedInteriorGap step on a random
// private leaf when one exists.
func (d *diffStep[T]) cachedInterior(rng *rand.Rand) {
	leaves := d.privateLeaves(d.mg, 2)
	if len(leaves) == 0 {
		return
	}
	pageNumber := leaves[rng.Intn(len(leaves))]
	r := d.genRecord(rng)
	cell := d.encode(r)
	d.execute("cached-interior", func(seam rangeDiffSeam[T], store Store, root *uint32) (any, error) {
		return seam.cachedInterior(d.codec, store, pageNumber, cell, r)
	})
}

// edgeGap runs one InsertIfEdgeGap step when the tree is one leaf.
func (d *diffStep[T]) edgeGap(rng *rand.Rand) {
	if !d.singleLeaf(d.mg, d.rootG) || !d.singleLeaf(d.me, d.rootE) {
		return
	}
	edge := EdgeLast
	if rng.Intn(2) == 0 {
		edge = EdgeFirst
	}
	boundary, ok := d.edgeBoundary(d.mg, d.rootG, edge)
	if !ok {
		return
	}
	r, ok := d.beyond(rng, boundary, edge)
	if !ok {
		return
	}
	cell := d.encode(r)
	cacheG := RootEdge(d.rootG)
	cacheE := RootEdge(d.rootE)
	d.execute("edge-gap", func(seam rangeDiffSeam[T], store Store, root *uint32) (any, error) {
		var cached *PrivateEdge
		if store == d.mg {
			cached = &cacheG
		} else {
			cached = &cacheE
		}
		return seam.edgeGap(d.codec, store, root, cell, cached, edge, false, r)
	})
	if !reflect.DeepEqual(cacheG, cacheE) {
		d.fatal("edge-gap: generic edge %#v, emitted edge %#v", cacheG, cacheE)
	}
}

// replacePredecessor runs one ReplaceLocalPredecessorWith step on a
// pending rejection whose predecessor spans a real address range.
func (d *diffStep[T]) replacePredecessor(rejected *LocalReject[T]) {
	if !rejected.predecessor.valid {
		return
	}
	pred := rejected.predecessor.value
	first, ok := d.split(pred)
	if !ok {
		return
	}
	cells := [][]byte{d.encode(first), d.encode(pred)}
	key := d.recordFrom(pred)
	d.execute("replace-predecessor", func(seam rangeDiffSeam[T], store Store, root *uint32) (any, error) {
		return nil, seam.replacePredecessor(d.codec, store, root, *rejected, key, cells)
	})
}

// replaceRun runs one ReplaceLocalRun step on a pending rejection for
// a run whose neighbors are valid.
func (d *diffStep[T]) replaceRun(rejected *LocalReject[T], rng *rand.Rand) {
	run := LocalRunPredecessor
	switch {
	case rejected.predecessor.valid && rejected.successor.valid && rng.Intn(2) == 0:
		run = LocalRunBoth
	case rejected.successor.valid && !rejected.predecessor.valid:
		run = LocalRunSuccessor
	case !rejected.predecessor.valid:
		return
	}
	replacement := d.runReplacement(*rejected, run)
	if replacement == nil {
		return
	}
	d.execute("replace-run", func(seam rangeDiffSeam[T], store Store, root *uint32) (any, error) {
		return nil, seam.replaceRun(d.codec, store, root, *rejected, run, d.encode(*replacement))
	})
}

// insertRejected completes the pending rejection at its original
// target with a fresh random record.
func (d *diffStep[T]) insertRejected(rejected *LocalReject[T], rng *rand.Rand) {
	r := d.genRecord(rng)
	cell := d.encode(r)
	d.execute("insert-rejected", func(seam rangeDiffSeam[T], store Store, root *uint32) (any, error) {
		position, fits, err := seam.insertRejected(d.codec, store, root, cell, *rejected)
		if err != nil {
			return nil, err
		}
		return insertedRejectedOutcome{Position: position, Fits: fits}, nil
	})
}

// runReplacement builds the record a local-run replacement writes, or
// nil when the run cannot be covered.
func (d *diffStep[T]) runReplacement(rejected LocalReject[T], run LocalRun) *T {
	switch run {
	case LocalRunPredecessor:
		return &rejected.predecessor.value
	case LocalRunSuccessor:
		return &rejected.successor.value
	case LocalRunBoth:
		merged := d.mergePair(rejected.predecessor.value, rejected.successor.value)
		return &merged
	}
	return nil
}

// verifyPageState asserts the two stores are byte-identical.
func (d *diffStep[T]) verifyPageState() {
	d.t.Helper()
	if len(d.mg.pages) != len(d.me.pages) {
		d.fatal("page count generic %d, emitted %d", len(d.mg.pages), len(d.me.pages))
	}
	for i := 1; i < len(d.mg.pages); i++ {
		if !bytes.Equal(d.mg.pages[i][:], d.me.pages[i][:]) {
			d.fatal("page %d differs between generic and emitted stores", i)
		}
	}
}

// runFamilyDiff replays randomized range mutations on both lanes for
// two rounds and compares every step.
func runFamilyDiff[T any](t *testing.T, codec rangeDiffCodec[T], generic, emit rangeDiffSeam[T], seed int64,
	genRecord func(rng *rand.Rand) T, split func(T) (T, bool), recordFrom func(T) Key, mergePair func(a, b T) T,
	beyond func(rng *rand.Rand, boundary T, edge Edge) (T, bool)) {
	t.Helper()
	for round := 0; round < 2; round++ {
		rng := rand.New(rand.NewSource(seed + int64(round)*7919))
		d := &diffStep[T]{
			t:          t,
			codec:      codec,
			generic:    generic,
			emit:       emit,
			mg:         newMemoryStore(),
			me:         newMemoryStore(),
			genRecord:  genRecord,
			split:      split,
			recordFrom: recordFrom,
			mergePair:  mergePair,
			beyond:     beyond,
		}
		var pending *LocalReject[T]
		for i := 0; i < 400; i++ {
			switch rng.Intn(100) {
			case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
				16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
				30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43,
				44, 45, 46, 47, 48, 49:
				pending = d.insert(rng)
			case 50, 51, 52, 53, 54, 55, 56, 57, 58, 59:
				if pending != nil {
					d.replaceRun(pending, rng)
				}
				pending = nil
			case 60, 61, 62, 63, 64:
				if pending != nil {
					d.replacePredecessor(pending)
				}
				pending = nil
			case 65, 66, 67, 68, 69, 70, 71:
				if pending != nil {
					d.insertRejected(pending, rng)
				}
				pending = nil
			case 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84,
				85, 86, 87, 88, 89, 90, 91:
				d.cachedInterior(rng)
			default:
				d.edgeGap(rng)
			}
			if d.mutations%100 == 0 {
				d.verifyPageState()
			}
		}
		d.verifyPageState()
	}
}

// ---- v4 family helpers ----

const diffSpace4 = 1 << 20

func diffRecord4(rng *rand.Rand) RangeRecord[RangeKey4] {
	from := uint32(rng.Intn(diffSpace4))
	to := from + uint32(rng.Intn(1<<10))
	if to >= diffSpace4 {
		to = diffSpace4 - 1
	}
	return RangeRecord[RangeKey4]{
		From:  RangeKey4(from),
		To:    RangeKey4(to),
		Value: uint32(rng.Intn(4)),
	}
}

func splitRecord4(r RangeRecord[RangeKey4]) (RangeRecord[RangeKey4], bool) {
	if r.From == r.To {
		return RangeRecord[RangeKey4]{}, false
	}
	first := r
	first.To = RangeKey4((uint64(r.From) + uint64(r.To)) / 2)
	return first, true
}

func recordFrom4(r RangeRecord[RangeKey4]) Key { return KeyOfU32(uint32(r.From)) }

func mergePair4(a, b RangeRecord[RangeKey4]) RangeRecord[RangeKey4] {
	return RangeRecord[RangeKey4]{From: a.From, To: b.To, Value: a.Value}
}

func beyond4(rng *rand.Rand, boundary RangeRecord[RangeKey4], edge Edge) (RangeRecord[RangeKey4], bool) {
	if edge == EdgeLast {
		base := uint64(boundary.To) + 1 + uint64(rng.Intn(8))
		if base >= diffSpace4 {
			return RangeRecord[RangeKey4]{}, false
		}
		to := base + uint64(rng.Intn(4))
		if to >= diffSpace4 {
			to = diffSpace4 - 1
		}
		return RangeRecord[RangeKey4]{
			From:  RangeKey4(base),
			To:    RangeKey4(to),
			Value: uint32(rng.Intn(4)),
		}, true
	}
	if boundary.From == 0 {
		return RangeRecord[RangeKey4]{}, false
	}
	to := uint64(boundary.From) - 1
	from := to - uint64(rng.Intn(4))
	return RangeRecord[RangeKey4]{
		From:  RangeKey4(from),
		To:    RangeKey4(to),
		Value: uint32(rng.Intn(4)),
	}, true
}

// ---- v6 family helpers ----

const diffLo6 = 1 << 32

func diffRecord6(rng *rand.Rand) RangeRecord[RangeKey6] {
	from := RangeKey6{
		Hi: uint64(rng.Intn(1 << 16)),
		Lo: uint64(rng.Intn(diffLo6 - 1024)),
	}
	return RangeRecord[RangeKey6]{
		From:  from,
		To:    RangeKey6{Hi: from.Hi, Lo: from.Lo + uint64(rng.Intn(1<<10))},
		Value: uint32(rng.Intn(4)),
	}
}

func splitRecord6(r RangeRecord[RangeKey6]) (RangeRecord[RangeKey6], bool) {
	if r.From == r.To {
		return RangeRecord[RangeKey6]{}, false
	}
	first := r
	first.To = RangeKey6{Hi: r.From.Hi, Lo: r.From.Lo + (r.To.Lo-r.From.Lo)/2}
	return first, true
}

func recordFrom6(r RangeRecord[RangeKey6]) Key { return KeyOfU128(r.From.Hi, r.From.Lo) }

func mergePair6(a, b RangeRecord[RangeKey6]) RangeRecord[RangeKey6] {
	return RangeRecord[RangeKey6]{From: a.From, To: b.To, Value: a.Value}
}

func beyond6(rng *rand.Rand, boundary RangeRecord[RangeKey6], edge Edge) (RangeRecord[RangeKey6], bool) {
	if edge == EdgeLast {
		base := advance6(boundary.To, 1+uint64(rng.Intn(8)))
		return RangeRecord[RangeKey6]{
			From:  base,
			To:    advance6(base, uint64(rng.Intn(4))),
			Value: uint32(rng.Intn(4)),
		}, true
	}
	if boundary.From.Hi == 0 && boundary.From.Lo == 0 {
		return RangeRecord[RangeKey6]{}, false
	}
	to := back6(boundary.From, 1+uint64(rng.Intn(8)))
	return RangeRecord[RangeKey6]{
		From:  back6(to, uint64(rng.Intn(4))),
		To:    to,
		Value: uint32(rng.Intn(4)),
	}, true
}

// advance6 adds delta to one v6 key (delta must not cross the 128-bit
// maximum, which the test address space never reaches).
func advance6(k RangeKey6, delta uint64) RangeKey6 {
	lo := k.Lo + delta
	hi := k.Hi
	if lo < k.Lo {
		hi++
	}
	return RangeKey6{Hi: hi, Lo: lo}
}

// back6 subtracts delta from one v6 key (delta must not cross zero,
// which the caller verifies).
func back6(k RangeKey6, delta uint64) RangeKey6 {
	lo := k.Lo - delta
	hi := k.Hi
	if lo > k.Lo {
		hi--
	}
	return RangeKey6{Hi: hi, Lo: lo}
}

// TestRangeFamilyEmittedMatchesGenericV4 replays 800 randomized v4
// mutations through the generic and the emitted layer.
func TestRangeFamilyEmittedMatchesGenericV4(t *testing.T) {
	runFamilyDiff(t, RangeCodec4{}, genericSeam4, emittedSeam4, 0xC0FFEE,
		diffRecord4, splitRecord4, recordFrom4, mergePair4, beyond4)
}

// TestRangeFamilyEmittedMatchesGenericV6 replays 800 randomized v6
// mutations (asymmetric 128-bit keys) through the generic and the
// emitted layer.
func TestRangeFamilyEmittedMatchesGenericV6(t *testing.T) {
	runFamilyDiff(t, RangeCodec6{}, genericSeam6, emittedSeam6, 0x6F0D,
		diffRecord6, splitRecord6, recordFrom6, mergePair6, beyond6)
}
