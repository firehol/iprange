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

// leafHint is one locator entry: the first key of a private leaf and its
// page number (Rust LeafHint).
type leafHint struct {
	first      tree.Key
	pageNumber uint32
}

// locatorCandidate is one located hint (Rust Candidate).
type locatorCandidate struct {
	index      int
	pageNumber uint32
}

// leafLocator is the bounded hint table of one private build (Rust
// LeafLocator). enabled state follows the slice capacity exactly like the
// Rust Vec: make gives capacity, release drops to zero.
type leafLocator struct {
	hints []leafHint
}

// newLeafLocator charges the hint table against the caller's byte budget
// and fails to zero capacity exactly like the Rust Heap vector fallback
// (Rust LeafLocator::new).
func newLeafLocator(family uint8, maxHeapBytes uint64) leafLocator {
	bytes := maxHeapBytes
	if bytes > maxLocatorBytes {
		bytes = maxLocatorBytes
	}
	size := leafHintSize(family)
	capacity := uint64(bytes / size)
	if capacity == 0 {
		return leafLocator{}
	}
	budget := newHeapBudget(bytes)
	if err := budget.vector(capacity, size, "private leaf locator"); err != nil {
		return leafLocator{}
	}
	return leafLocator{hints: make([]leafHint, 0, capacity)}
}

// leafHintSize reports the Rust element size of one hint for the budget
// charge (Rust size_of::<LeafHint<K>>(): 8 for IPv4, 24 for IPv6).
func leafHintSize(family uint8) uint64 {
	if family == format.AddressFamilyIPv4 {
		return 8
	}
	return 24
}

func (l *leafLocator) enabled() bool { return cap(l.hints) != 0 }

// candidate returns the hint at or below key (Rust LeafLocator::candidate
// over partition_point of first <= key).
func (l *leafLocator) candidate(key tree.Key) (locatorCandidate, bool) {
	index := sort.Search(len(l.hints), func(i int) bool {
		return key.Less(l.hints[i].first)
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
func (l *leafLocator) learn(first tree.Key, pageNumber uint32, c locatorCandidate, hasCandidate bool) {
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
		if l.hints[existing].first.Equal(first) {
			return
		}
		l.hints = append(l.hints[:existing], l.hints[existing+1:]...)
	}
	index := sort.Search(len(l.hints), func(i int) bool {
		return !l.hints[i].first.Less(first)
	})
	if index < len(l.hints) && l.hints[index].first.Equal(first) {
		l.hints[index].pageNumber = pageNumber
		return
	}
	if len(l.hints) < cap(l.hints) {
		l.hints = append(l.hints, leafHint{})
		copy(l.hints[index+1:], l.hints[index:])
		l.hints[index] = leafHint{first: first, pageNumber: pageNumber}
	}
}

// clear keeps the capacity and drops the entries (Rust Vec::clear).
func (l *leafLocator) clear() { l.hints = l.hints[:0] }

// release drops the table and disables the locator (Rust
// LeafLocator::release).
func (l *leafLocator) release() { l.hints = nil }

// privateInput is the locator-backed private range input (Rust
// PrivateInput<K, ADAPTIVE>). adaptive mirrors the Rust const generic: the
// assignment input never adapts, the union input stops probing after
// local conflicts and releases the locator at the conflict limit.
type privateInput struct {
	locator             leafLocator
	probeLocator        bool
	localConflicts      uint8
	pendingLocatorBytes uint64
	adaptive            bool
	family              uint8
}

// newAssignmentInput starts the eager assignment input (Rust
// PrivateInput::new; IPv4 only, IPv6 leaves are too short to pay for
// strict-interior hints).
func newAssignmentInput(family uint8, maxHeapBytes uint64) privateInput {
	return privateInput{
		locator:      newLeafLocator(family, unionLocatorBytes(family, maxHeapBytes)),
		probeLocator: true,
		family:       family,
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
func (p *privateInput) enable() {
	bytes := p.pendingLocatorBytes
	p.pendingLocatorBytes = 0
	if bytes == 0 {
		return
	}
	p.locator = newLeafLocator(p.family, bytes)
	p.probeLocator = true
	p.localConflicts = 0
}

// disabled reports the input has no locator now and none pending (Rust
// PrivateInput::disabled).
func (p *privateInput) disabled() bool {
	return !p.locator.enabled() && p.pendingLocatorBytes == 0
}

// release drops the locator and the pending budget (Rust
// PrivateInput::release).
func (p *privateInput) release() {
	p.locator.release()
	p.probeLocator = false
	p.pendingLocatorBytes = 0
}

// noteRejection adapts the probe policy after one rejected local probe
// (Rust PrivateInput::note_rejection).
func (p *privateInput) noteRejection(rejected tree.LocalReject[rangeRecord]) {
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
type privateInputInsert struct {
	inserted bool
	reject   tree.LocalReject[rangeRecord]
	rejected bool
}

// insertPrivateInputGap inserts one range through the locator input when
// the input is armed (Rust insert_private_input_gap): the cached leaf is
// probed first, then the ordinary local gap, learning the inserted leaf
// after a successful local insert.
func insertPrivateInputGap(ctx *rangeCtx, r rangeRecord, input *privateInput) (privateInputInsert, error) {
	if r.to.Less(r.from) {
		return privateInputInsert{}, invalid("range start is after its end")
	}
	if input.disabled() {
		result, err := insertPrivateGap(ctx, r)
		if err != nil {
			return privateInputInsert{}, err
		}
		if result.Inserted {
			return privateInputInsert{inserted: true}, nil
		}
		return privateInputInsert{reject: result.Reject, rejected: true}, nil
	}
	locatorEnabled := input.locator.enabled()
	probe, err := probeCached(ctx, r, input)
	if err != nil {
		return privateInputInsert{}, err
	}
	if probe.inserted {
		return privateInputInsert{inserted: true}, nil
	}
	result, err := insertPrivateGap(ctx, r)
	if err != nil {
		return privateInputInsert{}, err
	}
	if result.Inserted {
		if locatorEnabled {
			first, err := tree.PrivateLeafFirst(ctx.family, ctx.store, result.PageNumber)
			if err != nil {
				return privateInputInsert{}, err
			}
			input.locator.learn(first, result.PageNumber, probe.candidate, probe.hasCandidate)
			if input.adaptive {
				input.probeLocator = true
				input.localConflicts = 0
			}
		}
		return privateInputInsert{inserted: true}, nil
	}
	input.noteRejection(result.Reject)
	return privateInputInsert{reject: result.Reject, rejected: true}, nil
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
func probeCached(ctx *rangeCtx, r rangeRecord, input *privateInput) (cachedProbe, error) {
	if !input.locator.enabled() || (input.adaptive && !input.probeLocator) || *ctx.root == 0 {
		return cachedProbe{}, nil
	}
	selected, has := input.locator.candidate(r.from)
	if !has {
		work.LeafLocatorMiss(1)
		work.LeafLocatorFallback(1)
		return cachedProbe{}, nil
	}
	cell, err := ctx.encodeRecord(0, r)
	if err != nil {
		return cachedProbe{}, err
	}
	gap := privateGap{family: ctx.family, r: r}
	inserted, err := tree.InsertIfCachedInteriorGap(ctx.family, ctx.store, selected.pageNumber, cell, gap)
	if err != nil {
		return cachedProbe{}, err
	}
	if inserted == tree.CachedInsertInserted {
		if err := rangeRecordAdded(ctx, r.value); err != nil {
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
