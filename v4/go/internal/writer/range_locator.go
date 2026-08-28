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

// leafHint4 is one IPv4 locator entry: the first key (always a 32-bit
// address carried in Key.Hi with Lo zero) and its page number. It is 8
// bytes, exactly like Rust LeafHint<Ipv4Key>, so the 256 KiB budget
// yields the same 32,768-hint coverage as Rust.
type leafHint4 struct {
	first      uint32
	pageNumber uint32
}

// leafHint6 is one IPv6 locator entry: the 128-bit first key and its
// page number, 24 bytes exactly like Rust LeafHint<Ipv6Key>.
type leafHint6 struct {
	first      tree.Key
	pageNumber uint32
}

// locatorCandidate is one located hint (Rust Candidate).
type locatorCandidate struct {
	index      int
	pageNumber uint32
}

// leafLocator is the bounded hint table of one private build (Rust
// LeafLocator). family picks the family-sized hint entry so the Go
// element size equals the Rust LeafHint<K> size (8 bytes IPv4, 24 bytes
// IPv6): the budget charge, the 256 KiB cap, and the hint coverage all
// match Rust exactly. enabled state follows the slice capacity exactly
// like the Rust Vec: make gives capacity, release drops to zero.
type leafLocator struct {
	family uint8
	hints4 []leafHint4
	hints6 []leafHint6
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
	if family == format.AddressFamilyIPv4 {
		return leafLocator{family: family, hints4: make([]leafHint4, 0, capacity)}
	}
	return leafLocator{family: family, hints6: make([]leafHint6, 0, capacity)}
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

func (l *leafLocator) enabled() bool { return cap(l.hints4)+cap(l.hints6) != 0 }

// candidate returns the hint at or below key (Rust LeafLocator::candidate
// over partition_point of first <= key).
func (l *leafLocator) candidate(key tree.Key) (locatorCandidate, bool) {
	if l.family == format.AddressFamilyIPv4 {
		index := sort.Search(len(l.hints4), func(i int) bool {
			return l.hints4[i].first > key.U32()
		})
		if index == 0 {
			return locatorCandidate{}, false
		}
		index--
		return locatorCandidate{index: index, pageNumber: l.hints4[index].pageNumber}, true
	}
	index := sort.Search(len(l.hints6), func(i int) bool {
		return key.Less(l.hints6[i].first)
	})
	if index == 0 {
		return locatorCandidate{}, false
	}
	index--
	return locatorCandidate{index: index, pageNumber: l.hints6[index].pageNumber}, true
}

// learn records the first key and page of one freshly inserted private
// leaf, coalescing with an existing hint for the same page (Rust
// LeafLocator::learn).
func (l *leafLocator) learn(first tree.Key, pageNumber uint32, c locatorCandidate, hasCandidate bool) {
	if l.family == format.AddressFamilyIPv4 {
		l.learn4(first.U32(), pageNumber, c, hasCandidate)
		return
	}
	l.learn6(first, pageNumber, c, hasCandidate)
}

// learn4 is the IPv4 form of learn (Rust LeafLocator::<Ipv4Key>::learn):
// the first key is the 32-bit address in Key.Hi.
func (l *leafLocator) learn4(first uint32, pageNumber uint32, c locatorCandidate, hasCandidate bool) {
	following := 0
	if hasCandidate {
		following = c.index + 1
	}
	existing := -1
	switch {
	case hasCandidate && c.pageNumber == pageNumber:
		existing = c.index
	case following < len(l.hints4) && l.hints4[following].pageNumber == pageNumber:
		existing = following
	}
	if existing >= 0 {
		if l.hints4[existing].first == first {
			return
		}
		l.hints4 = append(l.hints4[:existing], l.hints4[existing+1:]...)
	}
	index := sort.Search(len(l.hints4), func(i int) bool {
		return uint64(l.hints4[i].first) >= uint64(first)
	})
	if index < len(l.hints4) && l.hints4[index].first == first {
		l.hints4[index].pageNumber = pageNumber
		return
	}
	if len(l.hints4) < cap(l.hints4) {
		l.hints4 = append(l.hints4, leafHint4{})
		copy(l.hints4[index+1:], l.hints4[index:])
		l.hints4[index] = leafHint4{first: first, pageNumber: pageNumber}
	}
}

// learn6 is the IPv6 form of learn (Rust LeafLocator::<Ipv6Key>::learn).
func (l *leafLocator) learn6(first tree.Key, pageNumber uint32, c locatorCandidate, hasCandidate bool) {
	following := 0
	if hasCandidate {
		following = c.index + 1
	}
	existing := -1
	switch {
	case hasCandidate && c.pageNumber == pageNumber:
		existing = c.index
	case following < len(l.hints6) && l.hints6[following].pageNumber == pageNumber:
		existing = following
	}
	if existing >= 0 {
		if l.hints6[existing].first.Equal(first) {
			return
		}
		l.hints6 = append(l.hints6[:existing], l.hints6[existing+1:]...)
	}
	index := sort.Search(len(l.hints6), func(i int) bool {
		return !l.hints6[i].first.Less(first)
	})
	if index < len(l.hints6) && l.hints6[index].first.Equal(first) {
		l.hints6[index].pageNumber = pageNumber
		return
	}
	if len(l.hints6) < cap(l.hints6) {
		l.hints6 = append(l.hints6, leafHint6{})
		copy(l.hints6[index+1:], l.hints6[index:])
		l.hints6[index] = leafHint6{first: first, pageNumber: pageNumber}
	}
}

// clear keeps the capacity and drops the entries (Rust Vec::clear).
func (l *leafLocator) clear() {
	l.hints4 = l.hints4[:0]
	l.hints6 = l.hints6[:0]
}

// release drops the table and disables the locator (Rust
// LeafLocator::release).
func (l *leafLocator) release() {
	l.hints4 = nil
	l.hints6 = nil
}

// privateInput is the locator-backed private range input (Rust
// PrivateInput<K, ADAPTIVE>). adaptive mirrors the Rust const generic: the
// assignment input never adapts, the union input stops probing after
// local conflicts and releases the locator at the conflict limit.
// AssignmentInput is the exported view of the private assignment input
// (Rust range_mutation::AssignmentInput): the structured transaction
// owns one input per family and passes it through the edit bindings so
// the leaf-locator state survives across assigns on a private range
// tree.
type AssignmentInput = privateInput

// NewAssignmentInput starts one assignment input for the family with the
// declared heap budget (Rust AssignmentInput::new).
func NewAssignmentInput(family uint8, maxHeapBytes uint64) AssignmentInput {
	return newAssignmentInput(family, maxHeapBytes)
}

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

// Release drops the locator and the pending budget of one assignment
// input (exported for the public direct workflows; Rust
// AssignmentInput::release).
func (a *AssignmentInput) Release() { a.release() }
