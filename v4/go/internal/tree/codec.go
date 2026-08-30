// Package tree implements the generic mapped-page COW B+tree core,
// mirroring the Rust fixed_tree module. It is the single authoritative
// implementation of persistent ordered-tree editing: the retirement tree,
// the range/catalog/membership trees, and the structure hash tree all
// compose this core with their own codecs. All page views come from the
// caller's Store; no complete page is ever owned here.

package tree

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Key is one ordered tree key. Fixed keys (numeric and hash) carry their
// canonical compare bytes in data[:Size]: numeric keys are big-endian
// (the wire cells are little-endian) and hash keys are the digest bytes
// followed by each suffix word big-endian, so bytewise comparison of the
// canonical bytes equals the Rust Ord of the key type (Ipv4Key, Ipv6Key,
// HashKey). Variable keys (catalog names) keep their ordered bytes in
// Var with Size zero. Codecs never mix the two forms. The value carries
// no heap reference for fixed keys, so hot-path copies stay on the stack.
//
// The hi and lo limbs cache the numeric value of the canonical compare
// bytes (hi is the leading u64, lo the next; narrower keys zero-extend
// into lo). Every fixed-key probe compares one cell key against the
// numeric value, so the limbs turn the per-probe big-endian byte
// assembly of U32/U64/U128 into a single load.
// keyLimbMax is the largest fixed key width whose numeric compare bytes
// fit the cached hi/lo limbs (Rust fixed key widths 4/8/12/16).
const keyLimbMax = 16

type Key struct {
	data [40]byte
	Size uint8
	hi   uint64
	lo   uint64
	Var  []byte
}

// IsVar reports whether the key is a variable-size byte key.
func (k Key) IsVar() bool { return k.Size == 0 }

// Bytes returns the ordered bytes of a variable key, or nil for fixed
// keys (Rust catalog name keys).
func (k Key) Bytes() []byte { return k.Var }

// FixedBytes returns the canonical compare bytes of a fixed key, or nil
// for a variable key.
func (k Key) FixedBytes() []byte {
	if k.Size == 0 {
		return nil
	}
	return k.data[:k.Size]
}

// Limb reads the numeric value of the N-byte canonical bytes as a
// single u64: a 4-byte key zero-extends, an 8-byte key is the value, and
// wider keys report their leading u64 (variable keys report zero).
func (k Key) limb() uint64 {
	switch k.Size {
	case 0:
		return 0
	case 4:
		return uint64(beU32(k.data[:4]))
	default:
		return beU64(k.data[:8])
	}
}

// U32 returns the numeric value of a 4-byte fixed key (wider keys
// report their leading 32 bits; codecs call it only for 4-byte keys).
func (k Key) U32() uint32 {
	if k.Size == 4 {
		return uint32(k.lo)
	}
	return uint32(k.hi >> 32)
}

// U64 returns the numeric value of an 8-byte fixed key (a 4-byte key
// zero-extends to its numeric value).
func (k Key) U64() uint64 {
	if k.Size < 8 {
		return k.lo
	}
	return k.hi
}

// U128 decodes the numeric high and low limbs of a 16-byte fixed key;
// narrower keys zero-extend into the low limb.
func (k Key) U128() (hi, lo uint64) {
	if k.Size == 8 {
		return 0, k.hi
	}
	return k.hi, k.lo
}

// Less reports k < other. A fixed key compares its canonical bytes
// bytewise, which equals the numeric order of the key type; a variable
// key compares its ordered bytes. Codecs never mix the two forms.
func (k Key) Less(other Key) bool {
	if k.Size == 0 || other.Size == 0 {
		return bytes.Compare(k.Var, other.Var) < 0
	}
	if k.Size == other.Size && k.Size <= keyLimbMax {
		return k.limbsLess(other)
	}
	return bytes.Compare(k.data[:k.Size], other.data[:other.Size]) < 0
}

// limbsLess compares two same-size fixed keys through their cached
// numeric limbs (big-endian canonical bytes; narrower keys zero-extend,
// so the limb pair compares exactly like the byte form). Hash keys
// (32-40 bytes) never take this path: their limb fields are unset.
func (k Key) limbsLess(other Key) bool {
	if k.hi != other.hi {
		return k.hi < other.hi
	}
	return k.lo < other.lo
}

// Equal reports k == other.
func (k Key) Equal(other Key) bool {
	if k.Size == 0 || other.Size == 0 {
		return bytes.Equal(k.Var, other.Var)
	}
	if k.Size != other.Size {
		return false
	}
	if k.Size <= keyLimbMax {
		return k.hi == other.hi && k.lo == other.lo
	}
	return bytes.Equal(k.data[:k.Size], other.data[:other.Size])
}

// KeyOfU32 builds a 4-byte fixed key from its numeric value. The
// parameter is generic over uint32-like named types (address values),
// so callers never convert.
func KeyOfU32[V ~uint32](value V) Key {
	var k Key
	k.Size = 4
	k.lo = uint64(value)
	k.data[0] = byte(value >> 24)
	k.data[1] = byte(value >> 16)
	k.data[2] = byte(value >> 8)
	k.data[3] = byte(value)
	return k
}

// KeyOfU64 builds an 8-byte fixed key from its numeric value.
func KeyOfU64[V ~uint64](value V) Key {
	var k Key
	k.Size = 8
	k.hi = uint64(value)
	bePutU64(k.data[:8], uint64(value))
	return k
}

// KeyOfU128 builds a 16-byte fixed key from its numeric high and low
// limbs.
func KeyOfU128[V ~uint64](hi, lo V) Key {
	var k Key
	k.Size = 16
	k.hi = uint64(hi)
	k.lo = uint64(lo)
	bePutU64(k.data[:8], uint64(hi))
	bePutU64(k.data[8:16], uint64(lo))
	return k
}

// KeyOfFixed builds a fixed key from its canonical compare bytes (4, 8,
// 12, 16, or 32..40 bytes), copying them into the value. Numeric keys
// cache their decoded limbs for the fixed-key probes; hash keys (32..40
// bytes) never read the limbs.
func KeyOfFixed(bytes []byte) Key {
	var k Key
	k.Size = uint8(len(bytes))
	copy(k.data[:], bytes)
	switch k.Size {
	case 4:
		k.lo = uint64(beU32(bytes[:4]))
	case 8:
		k.hi = beU64(bytes[:8])
	case 12:
		k.hi = beU64(bytes[:8])
		k.lo = uint64(beU32(bytes[8:12]))
	case 16:
		k.hi = beU64(bytes[:8])
		k.lo = beU64(bytes[8:16])
	}
	return k
}

// RawKey builds a hash key from its up-to-40 ordered probe bytes (Rust
// HashKey value): the digest bytes followed by each suffix word
// big-endian, the form produced by the writer's hashKey/hashProbe
// helpers. The bytes are copied into the key.
func RawKey(bytes []byte) Key { return KeyOfFixed(bytes) }

// VarKey builds a variable key from its ordered bytes.
func VarKey(bytes []byte) Key { return Key{Var: bytes} }

// Codec is the per-tree wire contract (Rust fixed_tree::Codec). Branch
// cells are key bytes followed by a 4-byte child page number; leaf cells
// are codec-specific fixed-size cells. The type parameter is the decoded
// leaf value: every read returns the concrete value, never a boxed any,
// so the tree hot paths stay allocation-free exactly like the Rust codecs.
type Codec[T any] interface {
	BranchType() format.PageType
	LeafType() format.PageType
	Aux() uint32
	KeySize() int
	LeafSize() int
	// ReadKey decodes one key from a branch or leaf cell. Branch cells
	// are keySize+4 bytes; leaf cells are leafSize bytes (level 0).
	ReadKey(cell []byte, level uint16) (Key, error)
	// CompareKey compares one cell's key with target without
	// materializing a Key (Rust key_at plus Ord; used by the
	// closure-free search fallback). The cell layout is identical to
	// ReadKey's, and the compare order must match ReadKey plus Key.Less
	// exactly.
	CompareKey(cell []byte, level uint16, target Key) (int, error)
	// ReadLeaf decodes one leaf value (used for validation and run
	// predicates).
	ReadLeaf(cell []byte) (T, error)
	// WriteKey encodes one key prefix into output (keySize bytes).
	WriteKey(key Key, output []byte)
}

// MaxBranchSize is the largest branch cell (Rust MAX_BRANCH_SIZE; codecs
// with variable-size branch records override it).
func MaxBranchSize[T any](codec Codec[T]) int {
	if variable, ok := codec.(VariableCodec[T]); ok {
		return variable.MaxBranchCell()
	}
	return codec.KeySize() + 4
}

// MaxLeafSize is the largest leaf cell (Rust MAX_LEAF_SIZE; codecs with
// variable-size leaf records override it).
func MaxLeafSize[T any](codec Codec[T]) int {
	if variable, ok := codec.(VariableCodec[T]); ok {
		return variable.MaxLeafCell()
	}
	return codec.LeafSize()
}

// VariableCodec extends Codec for variable-size records or keys (Rust
// codecs with KEY_SIZE == 0 or LEAF_SIZE == 0). The tree core routes every
// variable record READ through one concrete slotted helper using the
// codec's record-length bounds, and only the branch WRITE hooks dispatch
// through the interface (partial records and keys, exactly like the
// approved Codec methods). No page view ever dispatches through this
// interface: the concrete read keeps the record slices provably partial
// and never hands the tree core a complete page.
type VariableCodec[T any] interface {
	Codec[T]
	// MaxBranchCell is the largest encoded branch cell (Rust
	// MAX_BRANCH_SIZE override; e.g. the full name record with the child
	// in the index slot).
	MaxBranchCell() int
	// MaxLeafCell is the largest encoded leaf cell (Rust MAX_LEAF_SIZE
	// override; e.g. the name record or the membership ID record).
	MaxLeafCell() int
	// LeafRecordBounds returns the [minimum, maximum] byte length of one
	// variable leaf record (Rust LEAF_SIZE == 0 codecs; the record
	// carries its own u16 length prefix, so the tree core validates and
	// slices it concretely).
	LeafRecordBounds() (minimum, maximum int)
	// BranchRecordBounds returns the bounds of one variable branch
	// record (Rust KEY_SIZE == 0 codecs; the branch record is the full
	// record with the child inside it).
	BranchRecordBounds() (minimum, maximum int)
	// WriteBranch encodes one variable branch record and returns its byte
	// count (Rust write_branch override).
	WriteBranch(key Key, child uint32, output []byte) (int, error)
	// ReadBranchChild decodes the child page of one variable branch
	// record (Rust read_branch_child override; the child is not at the
	// keySize offset of a full record).
	ReadBranchChild(cell []byte) (uint32, error)
}

// FixedCellSize reports the fixed cell length for one level: leaf cells at
// level 0, branch cells above (Rust Codec::fixed_cell_size). A zero size
// means the page uses variable-size records.
func FixedCellSize[T any](codec Codec[T], level uint16) (int, bool) {
	if level == 0 {
		return codec.LeafSize(), codec.LeafSize() != 0
	}
	return codec.KeySize() + 4, codec.KeySize() != 0
}

// RetiredPages is the bounded COW retirement list (Rust RetiredPages):
// one retired page per tree level plus the leaf.
type RetiredPages struct {
	pages [maxPath + 1]uint32
	len   int
}

// NewRetiredPages returns an empty retirement list.
func NewRetiredPages() *RetiredPages { return &RetiredPages{} }

// Push appends one page to the list.
func (r *RetiredPages) Push(page uint32) error {
	if r.len >= len(r.pages) {
		return corrupt("COW path exceeds the maximum tree height")
	}
	r.pages[r.len] = page
	r.len++
	return nil
}

// Extend appends every page of pages.
func (r *RetiredPages) Extend(pages []uint32) error {
	for _, page := range pages {
		if err := r.Push(page); err != nil {
			return err
		}
	}
	return nil
}

// Len reports the number of retired pages.
func (r *RetiredPages) Len() int { return r.len }

// Slice returns the retired pages.
func (r *RetiredPages) Slice() []uint32 { return r.pages[:r.len] }

// Clear empties the list.
func (r *RetiredPages) Clear() { r.len = 0 }

// Store is the mutable page provider for one COW draft (Rust
// fixed_tree::Store). Every page view aliases the mapped page at its
// final offset; production stores hand out direct mapping slices.
//
// Inspect returns one mapped page view. Update returns one private page
// view for mutation together with the dirty-chain tag captured before
// the mutation; the caller must restore the tag with RestoreDirty after
// a successful mutation, because page-header writes clear the checksum
// slot that carries the tag until prepare seals the page (Rust
// Store::update_page re-arms the tag after the write closure).
// CopyPage returns the source and destination views of one COW copy with
// the destination's captured tag; the caller copies the bytes and then
// restores the tag the same way. The closure-free forms keep the drive
// loops allocation-free: a closure passed through the Store interface
// escapes to the heap, so no hot-path read or write dispatches through a
// callback.
type Store interface {
	TargetTxn() uint64
	PageLimit() uint64
	Inspect(pageNumber uint32) ([]byte, error)
	Allocate() (uint32, error)
	Update(pageNumber uint32) (page []byte, tag uint32, err error)
	// FinishEdit stamps the captured dirty-chain tag into the page view
	// that Update returned (Rust update_page/copy_page closures put the
	// tag through the same PageMut, so the page is fetched exactly once
	// per edit; Go's tag write reuses the already-held mapping view).
	FinishEdit(page []byte, tag uint32) error
	CopyPage(source, destination uint32) (sourcePage, outputPage []byte, tag uint32, err error)
	DiscardPrivate(pageNumber uint32) error
}

// RetiringStore extends Store with the retirement sink (Rust
// fixed_tree::RetiringStore). The sink takes the bounded value, not a
// slice of it, so callers never hand a view of a stack-local array
// through the interface (Rust passes RetiredPages by value).
type RetiringStore interface {
	Store
	RetirePages(retired RetiredPages) error
}

// RetireOne builds a single-page retirement list (Rust
// RetiredPages::single).
func RetireOne(page uint32) RetiredPages {
	var retired RetiredPages
	retired.pages[0] = page
	retired.len = 1
	return retired
}

// CopyForCow copies one complete committed page into an allocated private
// page: full byte copy, born transaction stamped to the draft, checksum
// cleared (Rust Store::copy_for_cow). The page byte count is not counted
// as owned memory: the destination is mapped, never a heap buffer.
func CopyForCow(store Store, source, destination uint32) error {

	target := store.TargetTxn()
	src, dst, tag, err := store.CopyPage(source, destination)
	if err != nil {
		return err
	}
	copy(dst, src)
	format.PutU64(dst[format.HeaderBorn:], target)
	format.PutU32(dst[format.HeaderCRC:], 0)
	// Necessary-work parity (Rust fixed_tree copy_for_cow through
	// PageMut): one full page copy plus the born stamp and checksum
	// clear, all counted as moved bytes (write_source / put_u64 /
	// put_u32; the Rust clear is a put_u32(0), so nothing is zeroed).
	work.BytesMoved(format.PageSize + 8 + 4)
	return store.FinishEdit(dst, tag)
}

// requireCodec rejects unusable codec geometry up front (Rust
// fixed_tree/page.rs require_codec).
func requireCodec[T any](codec Codec[T]) error {
	branchSize := MaxBranchSize(codec)
	leafSize := MaxLeafSize(codec)
	if branchSize == 0 || leafSize == 0 ||
		branchSize+2+format.SlottedHeaderSize > format.PageSize ||
		leafSize+2+format.SlottedHeaderSize > format.PageSize ||
		branchSize > maxTreeCell {
		return unsupported("invalid B+tree codec")
	}
	return nil
}

const maxPath = 31 // format.MaxTreeLevel
const maxTreeCell = 512

func corrupt(detail string) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
}

func invalid(detail string) error {
	return &format.Error{Code: format.CodeInvalidArgument, Detail: detail}
}

// unsupported mirrors Rust Error::Unsupported, which maps to the SDK
// OsUnsupported code (sdk_error.rs: Error::Unsupported -> OsUnsupported,
// 58). The Go port names the code OSUnsupported after the SDK table.
func unsupported(detail string) error {
	return &format.Error{Code: format.CodeOSUnsupported, Detail: detail}
}
