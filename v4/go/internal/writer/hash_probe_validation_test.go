// Hash-probe suffix validation (SOW-0027 4c/4d delta review, Rust-parity
// finding): the inline PrefixKeyProbe compares fixed cells without
// materializing a Key, and the membership and structure hash codecs
// must still refuse the cells their ReadKey would refuse (zero id, out
// of range word count) on every probed cell. The test corrupts the id
// suffix of the root page's middle cell (the exact first probe of the
// binary search) and inserts a new key: the probe must raise
// CodeFormatInvalid on that cell (Rust decode_hash inside key_at).
// Leaf cells are inserted in the LE wire layout exactly like the
// dictionary producers (membership intern and structure intern).

package writer

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// hashProbeTestDigest builds one deterministic digest whose first byte
// orders the key: byte(0) is the smallest key, then byte(1), ...
func hashProbeTestDigest(k int) (digest [32]byte) {
	digest[0] = byte(k)
	for i := 1; i < len(digest); i++ {
		digest[i] = byte(i*13 + k)
	}
	return digest
}

// membershipHashWire builds the LE wire record of one membership hash
// cell (Rust membership_dictionary codec record bytes).
func membershipHashWire(digest [32]byte, wordCount, id uint32) [membershipHashKeySize]byte {
	var key [membershipHashKeySize]byte
	copy(key[hashDigestOffset:], digest[:])
	binary.LittleEndian.PutUint32(key[hashWordCountOffset:], wordCount)
	binary.LittleEndian.PutUint32(key[hashIDOffset:], id)
	return key
}

// structureHashWire builds the LE wire record of one structure hash
// cell (Rust structured_value codec record bytes).
func structureHashWire(digest [32]byte, id uint32) [structureHashKeySize]byte {
	var key [structureHashKeySize]byte
	copy(key[structureHashDigestOffset:], digest[:])
	binary.LittleEndian.PutUint32(key[structureHashIDOffset:], id)
	return key
}

// corruptMiddleCellAndInsert builds one tree, zeroes the id suffix of
// the root page's middle cell (the first probe of every binary search),
// and requires the next insert to fail with CodeFormatInvalid. The
// healthy control accepts the same insert.
func corruptMiddleCellAndInsert(t *testing.T, insert func(store tree.Store, root *uint32, k int, id uint32) error, idOffset, cellLen, count int) {
	t.Helper()
	store := newRangeMemoryStore()
	var root uint32
	for k := 1; k <= count; k++ {
		if err := insert(store, &root, k, uint32(k+1)); err != nil {
			t.Fatalf("insert key %d: %v", k, err)
		}
	}
	page, err := store.Inspect(root)
	if err != nil {
		t.Fatalf("inspect root: %v", err)
	}
	header, err := format.DecodePageHeader(page, store.TargetTxn())
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	middle := int(header.ItemCount) / 2
	cell, err := format.SlottedCell(page, &header, middle, cellLen)
	if err != nil {
		t.Fatalf("middle cell: %v", err)
	}
	for i := idOffset; i < idOffset+4; i++ {
		cell[i] = 0
	}
	if err := insert(store, &root, 0, 1); err == nil {
		t.Fatal("insert over a zero-id probe cell succeeded; the probe did not validate the suffix")
	} else {
		var ferr *format.Error
		if !errors.As(err, &ferr) || ferr.Code != format.CodeFormatInvalid {
			t.Fatalf("insert over the corrupt cell = %v, want CodeFormatInvalid", err)
		}
	}
	// The same insert succeeds on the healthy tree.
	clean := newRangeMemoryStore()
	cleanRoot := uint32(0)
	for k := 1; k <= count; k++ {
		if err := insert(clean, &cleanRoot, k, uint32(k+1)); err != nil {
			t.Fatalf("clean insert key %d: %v", k, err)
		}
	}
	if err := insert(clean, &cleanRoot, 0, 1); err != nil {
		t.Fatalf("clean tree rejected the minimum key: %v", err)
	}
}

func TestMembershipHashProbeRejectsZeroIDSuffix(t *testing.T) {
	count := 300
	codec := hashCodec{}
	insert := func(store tree.Store, root *uint32, k int, id uint32) error {
		wire := membershipHashWire(hashProbeTestDigest(k), 1, id)
		_, _, err := tree.Insert(codec, store, root, wire[:], tree.RetiredPages{})
		return err
	}
	corruptMiddleCellAndInsert(t, insert, hashIDOffset, codec.KeySize()+4, count)
}

func TestStructureHashProbeRejectsZeroIDSuffix(t *testing.T) {
	count := 400
	codec := structureHashCodec{}
	insert := func(store tree.Store, root *uint32, k int, id uint32) error {
		wire := structureHashWire(hashProbeTestDigest(k), id)
		_, _, err := tree.Insert(codec, store, root, wire[:], tree.RetiredPages{})
		return err
	}
	corruptMiddleCellAndInsert(t, insert, structureHashIDOffset, codec.KeySize()+4, count)
}
