// Operation-private aggregation of dictionary refcount changes (Rust
// membership_delta.rs): a two-slot pending buffer in front of one private
// fixed tree of 12-byte delta records. The draft edits accumulate
// refcount changes here; the workflow finish flushes the buffer and drains
// the tree in key order, retiring every private delta page through the
// consuming cursor.

package writer

import (
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

const (
	deltaBranchType   = format.PageType(250)
	deltaLeafType     = format.PageType(251)
	deltaAux          = uint32(0x4d44454c)
	deltaIDOffset     = 0
	deltaChangeOffset = 4
	deltaRecordSize   = 12
	deltaPendingSlots = 2
)

// memberDelta is one id refcount change (Rust Delta). The change is an
// i64 on the wire, sign included, exactly like the Rust record.
type memberDelta struct {
	id     uint32
	change int64
}

// writeMemberDelta encodes one delta record into dst (Rust Delta::encode).
// The caller owns the bounded target; the tree insert copies the bytes
// into the mapped page, so a caller-held scratch never allocates.
func writeMemberDelta(dst []byte, id uint32, change int64) {
	format.PutU32(dst[deltaIDOffset:deltaChangeOffset], id)
	binary.LittleEndian.PutUint64(dst[deltaChangeOffset:], uint64(change))
}

// deltaPending is the two-slot pending buffer of one draft (Rust
// Pending): slots hold [empty | one delta] by value; a full buffer
// spills the oldest slot into the delta tree. The value slots keep the
// batch merge allocation-free (Rust [Option<Delta>; PENDING_SLOTS]).
// scratch is the encode target of the spill inserts; it lives on the
// draft so tree inserts copy the record into the mapped page without a
// heap escape (the Go generic tree interface makes stack encodes
// escape).
type deltaPending struct {
	slots   [deltaPendingSlots]memberDelta
	used    [deltaPendingSlots]bool
	scratch [deltaRecordSize]byte
}

func newDeltaPending() deltaPending { return deltaPending{} }

// isEmpty reports no pending deltas (Rust Pending::is_empty).
func (p *deltaPending) isEmpty() bool {
	return !p.used[0] && !p.used[1]
}

// slot returns the used slot matching id, when present.
func (p *deltaPending) slot(id uint32) *memberDelta {
	for index := range p.used {
		if p.used[index] && p.slots[index].id == id {
			return &p.slots[index]
		}
	}
	return nil
}

// free returns the first empty slot, when present.
func (p *deltaPending) free() *memberDelta {
	for index := range p.used {
		if !p.used[index] {
			p.used[index] = true
			return &p.slots[index]
		}
	}
	return nil
}

// drop frees the slot at index.
func (p *deltaPending) drop(index int) {
	p.used[index] = false
	p.slots[index] = memberDelta{}
}

// deltaCodec is the wire contract of the delta tree (Rust DeltaCodec:
// fixed 4-byte id keys and 12-byte leaf records).
type deltaCodec struct{}

func (deltaCodec) BranchType() format.PageType { return deltaBranchType }
func (deltaCodec) LeafType() format.PageType   { return deltaLeafType }
func (deltaCodec) Aux() uint32                 { return deltaAux }
func (deltaCodec) KeySize() int                { return deltaChangeOffset }
func (deltaCodec) LeafSize() int               { return deltaRecordSize }

func (deltaCodec) ReadKey(cell []byte, _ uint16) (tree.Key, error) {
	if len(cell) < deltaChangeOffset {
		return tree.Key{}, corrupt("refcount delta key is truncated")
	}
	return tree.Key{Hi: uint64(format.U32(cell))}, nil
}

func (deltaCodec) ReadLeaf(cell []byte) (memberDelta, error) {
	return decodeMemberDelta(cell)
}

func (deltaCodec) WriteKey(key tree.Key, output []byte) {
	format.PutU32(output[deltaIDOffset:deltaChangeOffset], uint32(key.Hi))
}

func decodeMemberDelta(cell []byte) (memberDelta, error) {
	if len(cell) != deltaRecordSize {
		return memberDelta{}, corrupt("refcount delta record is malformed")
	}
	return memberDelta{
		id:     format.U32(cell[deltaIDOffset:deltaChangeOffset]),
		change: int64(binary.LittleEndian.Uint64(cell[deltaChangeOffset:])),
	}, nil
}

// trackBufferedDelta records one refcount change, merging equal ids in
// the pending slots and spilling the oldest slot when both are full
// (Rust track_buffered). id 0 (the empty bitmap) is never tracked.
func trackBufferedDelta(store tree.RetiringStore, root *uint32, pending *deltaPending, id uint32, change int64) error {
	if id == 0 {
		return nil
	}
	if slot := pending.slot(id); slot != nil {
		next, err := checkedDeltaChange(slot.change, change)
		if err != nil {
			return err
		}
		slot.change = next
		return nil
	}
	if slot := pending.free(); slot != nil {
		*slot = memberDelta{id: id, change: change}
		return nil
	}
	oldest := pending.slots[0]
	if !pending.used[0] {
		return corrupt("refcount delta pending slot is empty")
	}
	if err := trackMembershipDelta(store, root, oldest.id, oldest.change, pending.scratch[:]); err != nil {
		return err
	}
	pending.slots[0] = pending.slots[1]
	pending.used[0] = pending.used[1]
	pending.slots[1] = memberDelta{id: id, change: change}
	pending.used[1] = true
	return nil
}

// flushMembershipDeltas writes every pending slot into the delta tree
// (Rust flush).
func flushMembershipDeltas(store tree.RetiringStore, root *uint32, pending *deltaPending) error {
	for index := range pending.used {
		if !pending.used[index] {
			continue
		}
		slot := pending.slots[index]
		if err := trackMembershipDelta(store, root, slot.id, slot.change, pending.scratch[:]); err != nil {
			return err
		}
		pending.drop(index)
	}
	return nil
}

// trackMembershipDelta inserts or merges one delta record (Rust track: a
// merge is a u64 field edit at the change offset; a zero change on an
// existing record is a no-op).
func trackMembershipDelta(store tree.RetiringStore, root *uint32, id uint32, change int64, scratch []byte) error {
	work.MembershipDeltaSpill(1)
	current, found, err := findMemberDelta(store, *root, id)
	if err != nil {
		return err
	}
	if found {
		if change == 0 {
			return nil
		}
		next, err := checkedDeltaChange(current.change, change)
		if err != nil {
			return err
		}
		retired, _, err := tree.MutateLeafU64(deltaCodec{}, store, root, tree.Key{Hi: uint64(id)},
			deltaChangeOffset, tree.RetiredPages{}, func(leaf memberDelta) (tree.LeafU64Mutation, error) {
				return tree.LeafU64Mutation{DoReplace: true, Replace: uint64(next)}, nil
			})
		if err != nil {
			return err
		}
		return requirePrivateDeltaRetirement(&retired)
	}
	writeMemberDelta(scratch, id, change)
	retired, inserted, err := tree.Insert(deltaCodec{}, store, root, scratch[:deltaRecordSize], tree.RetiredPages{})
	if err != nil {
		return err
	}
	if !inserted {
		return corrupt("refcount delta key already exists")
	}
	return requirePrivateDeltaRetirement(&retired)
}

func checkedDeltaChange(current, change int64) (int64, error) {
	next := current + change
	if (change > 0 && next < current) || (change < 0 && next > current) {
		return 0, overflow("dictionary refcount delta")
	}
	return next, nil
}

func findMemberDelta(store tree.Store, root uint32, id uint32) (memberDelta, bool, error) {
	if root == 0 {
		return memberDelta{}, false, nil
	}
	found, ok, err := tree.Predecessor(deltaCodec{}, store, root, tree.Key{Hi: uint64(id)})
	if err != nil {
		return memberDelta{}, false, err
	}
	if !ok || found.id != id {
		return memberDelta{}, false, nil
	}
	return found, true, nil
}

func requirePrivateDeltaRetirement(retired *tree.RetiredPages) error {
	if retired.Len() == 0 {
		return nil
	}
	return corrupt("refcount delta tree contains a committed page")
}

// membershipDeltaDrain consumes one private delta tree in key order,
// retiring every page into the draft private stack through the consuming
// cursor (Rust Drain over Cursor::new_consuming).
type membershipDeltaDrain struct {
	cursor *tree.ForwardCursor[memberDelta]
}

func newMembershipDeltaDrain(store tree.RetiringStore, root uint32) (*membershipDeltaDrain, error) {
	cursor, err := tree.NewForwardCursor[memberDelta](deltaCodec{}, store, root, true)
	if err != nil {
		return nil, err
	}
	return &membershipDeltaDrain{cursor: cursor}, nil
}

// next returns the next delta in ascending id order (Rust
// next_consuming). The boolean is false at the end of the drain; this is
// a zero-allocation value return, unlike a pointer that would escape the
// cursor frame.
func (d *membershipDeltaDrain) next(store tree.RetiringStore) (memberDelta, bool, error) {
	delta, ok, err := d.cursor.Next()
	if err != nil {
		return memberDelta{}, false, err
	}
	return delta, ok, nil
}
