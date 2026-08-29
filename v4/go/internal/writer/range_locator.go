// Bounded scalar navigation hints for one private range build (Rust
// range_mutation/locator.rs): a capacity-bounded first-key -> page hint
// table serves the assignment and union inputs, so an overwrite stream
// inserts straight into the cached private leaf without re-descending
// the tree. The adaptive union variant stops probing after a burst of
// local conflicts and releases the hints entirely at the conflict limit;
// the plain assignment variant always probes its (IPv4-only) locator.

package writer

import (
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

const (
	// maxLocatorBytes caps the locator hint table (Rust
	// MAX_LOCATOR_BYTES).
	maxLocatorBytes = 256 * 1024
	// locatorConflictLimit stops paying locator branches once the input
	// proves to be an overwrite run (Rust LOCATOR_CONFLICT_LIMIT).
	locatorConflictLimit = 8
)

// leafHint[K] is one locator entry: the family first key and its page
// number. Its size equals the Rust LeafHint<K> size (8 bytes for IPv4,
// 24 bytes for IPv6), so the 256 KiB budget yields the same hint
// coverage as Rust.
type leafHint[K any] struct {
	first      K
	pageNumber uint32
}

// locatorCandidate is one located hint (Rust Candidate).
type locatorCandidate struct {
	index      int
	pageNumber uint32
}

// leafLocator[K] is the bounded hint table of one private build (Rust
// LeafLocator<K>). The hint entry size matches the Rust LeafHint<K>
// size, so the budget charge, the 256 KiB cap, and the hint coverage all
// match Rust exactly. enabled state follows the slice capacity exactly
// like the Rust Vec: make gives capacity, release drops to zero.
type leafLocator[K any] struct {
	family uint8
	hints  []leafHint[K]
}

// newLeafLocator charges the hint table against the caller's byte budget
// and fails to zero capacity exactly like the Rust Heap vector fallback
// (Rust LeafLocator::new).
func newLeafLocator[K any](family uint8, maxHeapBytes uint64) leafLocator[K] {
	bytes := maxHeapBytes
	if bytes > maxLocatorBytes {
		bytes = maxLocatorBytes
	}
	size := leafHintSize(family)
	capacity := uint64(bytes / size)
	if capacity == 0 {
		return leafLocator[K]{}
	}
	budget := newHeapBudget(bytes)
	if err := budget.vector(capacity, size, "private leaf locator"); err != nil {
		return leafLocator[K]{}
	}
	return leafLocator[K]{family: family, hints: make([]leafHint[K], 0, capacity)}
}

// leafHintSize reports the element size of one hint for the budget
// charge, identical to Rust size_of::<LeafHint<K>>() (8 for IPv4, 24
// for IPv6) and to the real Go element size of the family hint entry.
func leafHintSize(family uint8) uint64 {
	if family == format.AddressFamilyIPv4 {
		return 8
	}
	return 24
}

func (l *leafLocator[K]) enabled() bool { return cap(l.hints) != 0 }

// candidate returns the hint at or below key (Rust LeafLocator::candidate
// over partition_point of first <= key).
func (l *leafLocator[K]) candidate(codec rangeFamily[K], key K) (locatorCandidate, bool) {
	index := sort.Search(len(l.hints), func(i int) bool {
		return codec.Less(key, l.hints[i].first)
	})
	if index == 0 {
		return locatorCandidate{}, false
	}
	index--
	return locatorCandidate{index: index, pageNumber: l.hints[index].pageNumber}, true
}

// learn records the first key and page of one freshly inserted private
// leaf, coalescing with an existing hint for the same page (Rust
// LeafLocator::learn).
func (l *leafLocator[K]) learn(codec rangeFamily[K], first K, pageNumber uint32, c locatorCandidate, hasCandidate bool) {
	following := 0
	if hasCandidate {
		following = c.index + 1
	}
	existing := -1
	switch {
	case hasCandidate && c.pageNumber == pageNumber:
		existing = c.index
	case following < len(l.hints) && l.hints[following].pageNumber == pageNumber:
		existing = following
	}
	if existing >= 0 {
		if codec.Equal(l.hints[existing].first, first) {
			return
		}
		l.hints = append(l.hints[:existing], l.hints[existing+1:]...)
	}
	index := sort.Search(len(l.hints), func(i int) bool {
		return !codec.Less(l.hints[i].first, first)
	})
	if index < len(l.hints) && codec.Equal(l.hints[index].first, first) {
		l.hints[index].pageNumber = pageNumber
		return
	}
	if len(l.hints) < cap(l.hints) {
		l.hints = append(l.hints, leafHint[K]{})
		copy(l.hints[index+1:], l.hints[index:])
		l.hints[index] = leafHint[K]{first: first, pageNumber: pageNumber}
	}
}

// clear keeps the capacity and drops the entries (Rust Vec::clear).
func (l *leafLocator[K]) clear() {
	l.hints = l.hints[:0]
}

// release drops the table and disables the locator (Rust
// LeafLocator::release).
func (l *leafLocator[K]) release() {
	l.hints = nil
}

// privateInput is the locator-backed private range input (Rust
// PrivateInput<K, ADAPTIVE>). adaptive mirrors the Rust const generic: the
// assignment input never adapts, the union input stops probing after
// local conflicts and releases the locator at the conflict limit.
type privateInput[K any] struct {
	locator             leafLocator[K]
	probeLocator        bool
	localConflicts      uint8
	pendingLocatorBytes uint64
	adaptive            bool
	family              rangeFamily[K]
}

// newAssignmentInput starts the eager assignment input (Rust
// PrivateInput::new; IPv4 only, IPv6 leaves are too short to pay for
// strict-interior hints).
func newAssignmentInput[K any](codec rangeFamily[K], family uint8, maxHeapBytes uint64) privateInput[K] {
	return privateInput[K]{
		locator:      newLeafLocator[K](family, unionLocatorBytes(family, maxHeapBytes)),
		probeLocator: true,
		family:       codec,
	}
}

// unionLocatorBytes reports the hint budget a private build may use for
// one address family (Rust K::WIDTH == 4 selection).
func unionLocatorBytes(family uint8, maxHeapBytes uint64) uint64 {
	if family == format.AddressFamilyIPv4 {
		return maxHeapBytes
	}
	return 0
}

// enable arms the lazy locator from the pending budget (Rust
// PrivateInput::enable).
func (p *privateInput[K]) enable() {
	bytes := p.pendingLocatorBytes
	p.pendingLocatorBytes = 0
	if bytes == 0 {
		return
	}
	p.locator = newLeafLocator[K](locatorFamily(p.family), bytes)
	p.probeLocator = true
	p.localConflicts = 0
}

// disabled reports the input has no locator now and none pending (Rust
// PrivateInput::disabled).
func (p *privateInput[K]) disabled() bool {
	return !p.locator.enabled() && p.pendingLocatorBytes == 0
}

// release drops the locator and the pending budget (Rust
// PrivateInput::release).
func (p *privateInput[K]) release() {
	p.locator.release()
	p.probeLocator = false
	p.pendingLocatorBytes = 0
}

// locatorFamily reports the format family byte of one family codec for
// the hint-budget charge (Rust K::FAMILY).
func locatorFamily[K any](codec rangeFamily[K]) uint8 {
	if _, ok := any(codec).(rangeCodec4); ok {
		return format.AddressFamilyIPv4
	}
	return format.AddressFamilyIPv6
}

// noteRejection adapts the probe policy after one rejected local probe
// (Rust PrivateInput::note_rejection).
func (p *privateInput[K]) noteRejection(rejected tree.LocalReject[rangeRecord[K]]) {
	p.locator.clear()
	if !p.adaptive {
		return
	}
	localConflict := false
	if _, has := rejected.Predecessor(); has {
		localConflict = true
	}
	if _, has := rejected.Successor(); has {
		localConflict = true
	}
	if localConflict {
		if p.localConflicts != 255 {
			p.localConflicts++
		}
		p.probeLocator = false
	} else {
		p.localConflicts = 0
		p.probeLocator = true
	}
	if p.localConflicts == locatorConflictLimit {
		p.locator.release()
	}
}

// privateInputInsert is the outcome of one locator-backed private probe
// (Rust PrivateInputInsert): the range was inserted, or the probe
// rejected with the positioned proof for the caller's merge.
type privateInputInsert[K any] struct {
	inserted bool
	reject   tree.LocalReject[rangeRecord[K]]
	rejected bool
}

// insertPrivateInputGap inserts one range through the locator input when
// the input is armed (Rust insert_private_input_gap): the cached leaf is
// probed first, then the ordinary local gap, learning the inserted leaf
// after a successful local insert.
func insertPrivateInputGap[K any](ctx *rangeCtx[K], r rangeRecord[K], input *privateInput[K]) (privateInputInsert[K], error) {
	if ctx.family.Less(r.To, r.From) {
		return privateInputInsert[K]{}, invalid("range start is after its end")
	}
	if input.disabled() {
		result, err := insertPrivateGap(ctx, r)
		if err != nil {
			return privateInputInsert[K]{}, err
		}
		if result.Inserted {
			return privateInputInsert[K]{inserted: true}, nil
		}
		return privateInputInsert[K]{reject: result.Reject, rejected: true}, nil
	}
	locatorEnabled := input.locator.enabled()
	probe, err := probeCached(ctx, r, input)
	if err != nil {
		return privateInputInsert[K]{}, err
	}
	if probe.inserted {
		return privateInputInsert[K]{inserted: true}, nil
	}
	result, err := insertPrivateGap(ctx, r)
	if err != nil {
		return privateInputInsert[K]{}, err
	}
	if result.Inserted {
		if locatorEnabled {
			first, err := tree.PrivateLeafFirst(ctx.family, ctx.storeView, result.PageNumber)
			if err != nil {
				return privateInputInsert[K]{}, err
			}
			fk := decodeCodecKey(ctx.family, first)
			input.locator.learn(ctx.family, fk, result.PageNumber, probe.candidate, probe.hasCandidate)
			if input.adaptive {
				input.probeLocator = true
				input.localConflicts = 0
			}
		}
		return privateInputInsert[K]{inserted: true}, nil
	}
	input.noteRejection(result.Reject)
	return privateInputInsert[K]{reject: result.Reject, rejected: true}, nil
}

// cachedProbe is the outcome of one cached-leaf probe (Rust CachedProbe:
// Inserted or Continue with the located candidate).
type cachedProbe struct {
	inserted     bool
	candidate    locatorCandidate
	hasCandidate bool
}

// probeCached probes the hinted private leaf first (Rust probe_cached):
// the locator hit inserts straight into the cached page; a miss charges
// the fallback and continues with the ordinary gap path.
func probeCached[K any](ctx *rangeCtx[K], r rangeRecord[K], input *privateInput[K]) (cachedProbe, error) {
	if !input.locator.enabled() || (input.adaptive && !input.probeLocator) || *ctx.root == 0 {
		return cachedProbe{}, nil
	}
	selected, has := input.locator.candidate(ctx.family, r.From)
	if !has {
		work.LeafLocatorMiss(1)
		work.LeafLocatorFallback(1)
		return cachedProbe{}, nil
	}
	cell, err := ctx.encodeRecord(0, r)
	if err != nil {
		return cachedProbe{}, err
	}
	inserted, err := gapCachedInterior(ctx, r, cell, selected.pageNumber)
	if err != nil {
		return cachedProbe{}, err
	}
	if inserted == tree.CachedInsertInserted {
		if err := rangeRecordAdded(ctx, r.Value); err != nil {
			return cachedProbe{}, err
		}
		work.LeafLocatorHit(1)
		if input.adaptive {
			input.localConflicts = 0
		}
		return cachedProbe{inserted: true}, nil
	}
	work.LeafLocatorFallback(1)
	return cachedProbe{candidate: selected, hasCandidate: true}, nil
}

// decodeCodecKey converts one canonical tree key back into the family
// key space (the tree core returns canonical keys from PrivateLeafFirst;
// the locator stores family keys).
func decodeCodecKey[K any](codec rangeFamily[K], key tree.Key) K {
	if _, ok := any(codec).(rangeCodec4); ok {
		return any(key4(key.U32())).(K)
	}
	hi, lo := key.U128()
	return any(key6{Hi: hi, Lo: lo}).(K)
}

// AssignmentInput is the public per-workflow private assignment input
// facade (Rust range_mutation::AssignmentInput over the workflow address
// family). It owns exactly one family side, selected at construction;
// the edit arms route the per-family methods to that side, so the
// structured and direct workflows never see the family key types.
type AssignmentInput struct {
	family uint8
	v4     privateInput[key4]
	v6     privateInput[key6]
}

// NewAssignmentInput starts one assignment input for the given address
// family with the declared heap budget (Rust AssignmentInput::new).
func NewAssignmentInput(family uint8, maxHeapBytes uint64) AssignmentInput {
	if family == format.AddressFamilyIPv4 {
		return AssignmentInput{family: family, v4: newAssignmentInput(rangeCodec4{}, family, maxHeapBytes)}
	}
	return AssignmentInput{family: family, v6: newAssignmentInput(rangeCodec6{}, family, maxHeapBytes)}
}

// Release drops the locator and the pending budget of the input
// (exported for the public direct workflows; Rust
// AssignmentInput::release).
func (a *AssignmentInput) Release() {
	if a.family == format.AddressFamilyIPv4 {
		a.v4.release()
		return
	}
	a.v6.release()
}
