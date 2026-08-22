// Structure dictionary write state and interning (Rust
// structured_value/manager.rs): the structure ID namespace (used bitmap,
// id limit, entry count), the id/hash tree pair, payload interning with
// SHA-256 deduplication, and refcount maintenance through the bounded
// reference batch. The ID tree is the sparse radix table of table.rs; the
// hash tree reuses the shared fixed B+tree with 36-byte digest+id keys.

package writer

import (
	"slices"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// structureState is the writable dictionary state (Rust State).
type structureState struct {
	idRoot     uint32
	hashRoot   uint32
	usedRoot   uint32
	entryCount uint64
	idLimit    uint64
}

// structureInterned is the outcome of one intern (Rust Interned): the
// allocated id, the payload's linked membership id (zero when none), and
// whether the record was created or served from the dictionary.
type structureInterned struct {
	id           uint32
	membershipID uint32
	created      bool
}

func structureIDExhausted() error {
	return &format.Error{Code: format.CodeStructureIdExhausted, Detail: "structure ID namespace exhausted"}
}

// internStructure returns the dictionary ID for one payload, creating the
// record when the payload is new (Rust structured_value::intern): typed
// validation first, then the digest lookup, then the lowest free ID and
// both tree records.
func internStructure(codec structurePayloadCodec, store tree.RetiringStore, state *structureState, payload structurePayload) (structureInterned, error) {
	if err := codec.validate(payload.Slice()); err != nil {
		return structureInterned{}, err
	}
	membershipID := codec.membershipID(&payload)
	if codec.isAbsent(&payload) {
		return structureInterned{id: 0, membershipID: 0, created: false}, nil
	}
	digest, err := structurePayloadDigest(codec, &payload)
	if err != nil {
		return structureInterned{}, err
	}
	if id, found, err := findEqualStructure(codec, store, state, &payload, digest); err != nil {
		return structureInterned{}, err
	} else if found {
		return structureInterned{id: id, membershipID: membershipID, created: false}, nil
	}
	return insertNewStructure(codec, store, state, &payload, digest, membershipID)
}

// insertNewStructure allocates the lowest free ID and inserts both tree
// records (Rust insert_new).
func insertNewStructure(codec structurePayloadCodec, store tree.RetiringStore, state *structureState, payload *structurePayload, digest [32]byte, membershipID uint32) (structureInterned, error) {
	id, err := bitmap.AllocateLowestID(store, &state.usedRoot, &state.idLimit, state.entryCount,
		bitmap.KindStructure, structureIDExhausted)
	if err != nil {
		return structureInterned{}, err
	}
	record, err := encodeStructureRecord(codec, id, digest, payload)
	if err != nil {
		return structureInterned{}, err
	}
	if err := structureTableInsert(codec, store, &state.idRoot, state.idLimit, record.Slice()); err != nil {
		return structureInterned{}, err
	}
	key := structureHashKey(digest, id)
	if err := insertStructureHashRecord(store, structureHashCodec{kind: codec.kind()}, &state.hashRoot, key[:]); err != nil {
		return structureInterned{}, err
	}
	next := state.entryCount + 1
	if next < state.entryCount {
		return structureInterned{}, overflow("structure entry count")
	}
	state.entryCount = next
	return structureInterned{id: id, membershipID: membershipID, created: true}, nil
}

// insertStructureHashRecord inserts one record into the structure hash
// tree, retiring the COW pages (Rust manager.rs insert + fixed_tree
// insert_retiring).
func insertStructureHashRecord(store tree.RetiringStore, codec tree.Codec[structureHashRecord], root *uint32, record []byte) error {
	retired, changed, err := tree.Insert(codec, store, root, record, tree.RetiredPages{})
	if err != nil {
		return err
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	if !changed {
		return corrupt("structure dictionary key already exists")
	}
	return nil
}

// findEqualStructure searches the hash tree for an equal payload (Rust
// find_equal): at_or_after over (digest, id), then the record's exact
// payload comparison.
func findEqualStructure(codec structurePayloadCodec, store tree.Store, state *structureState, payload *structurePayload, digest [32]byte) (uint32, bool, error) {
	if state.hashRoot == 0 {
		return 0, false, nil
	}
	key := structureHashProbe(digest, 1)
	for {
		value, found, err := tree.AtOrAfter(structureHashCodec{kind: codec.kind()}, store, state.hashRoot, tree.RawKey(key[:]))
		if err != nil {
			return 0, false, err
		}
		if !found {
			return 0, false, nil
		}
		candidate := value
		if candidate.digest != digest {
			return 0, false, nil
		}
		record, found, err := structureTableFind(codec, store, state.idRoot, state.idLimit, candidate.id)
		if err != nil {
			return 0, false, err
		}
		if !found {
			return 0, false, corrupt("structure hash points to a missing ID")
		}
		if slices.Equal(record.payload.Slice(), payload.Slice()) {
			return candidate.id, true, nil
		}
		if candidate.id == ^uint32(0) {
			return 0, false, nil
		}
		key = structureHashProbe(digest, candidate.id+1)
	}
}

// applyStructureDelta applies one aggregated range-refcount delta and
// returns the linked membership ID released with a deleted record (Rust
// structured_value::apply_delta). The one-shot output path only adds
// references, so records never leave the dictionary there; the deletion
// contract mirrors the authority and is exercised by the dictionary
// tests.
func applyStructureDelta(codec structurePayloadCodec, store tree.RetiringStore, state *structureState, id uint32, change int64) (uint32, error) {
	record, deleted, err := structureTableChangeRefcount(codec, store, &state.idRoot, state.idLimit, id, change)
	if err != nil {
		return 0, err
	}
	if !deleted {
		return 0, nil
	}
	probe := structureHashProbe(record.digest, record.id)
	retired, err := tree.DeleteExisting(structureHashCodec{kind: codec.kind()}, store, &state.hashRoot, tree.RawKey(probe[:]), tree.RetiredPages{})
	if err != nil {
		return 0, err
	}
	if err := store.RetirePages(retired); err != nil {
		return 0, err
	}
	cleared, err := bitmap.ClearUsed(store, &state.usedRoot, state.idLimit, bitmap.KindStructure, record.id, &retired)
	if err != nil {
		return 0, err
	}
	if err := store.RetirePages(retired); err != nil {
		return 0, err
	}
	if !cleared {
		return 0, corrupt("structure used bit is missing")
	}
	if state.entryCount == 0 {
		return 0, overflow("structure entry count")
	}
	state.entryCount--
	limit, err := bitmap.ShrinkStructure(store, &state.usedRoot, state.idLimit)
	if err != nil {
		return 0, err
	}
	state.idLimit = limit
	if err := structureTableShrink(codec, store, &state.idRoot, state.idLimit); err != nil {
		return 0, err
	}
	return codec.membershipID(&record.payload), nil
}
