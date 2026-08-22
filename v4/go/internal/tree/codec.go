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
)

// Key is one ordered tree key (Rust Key: Copy + Ord). Fixed keys compare
// numerically as (Hi, Lo) and cover the v4 ordered keys (retirement
// transaction extents, IPv4/IPv6 range bounds). Variable keys compare
// lexicographically: catalog names view the caller's record (bytes), and
// hash keys (Rust HashKey, Copy) carry their up-to-40 ordered probe bytes
// inline (raw) so decoding one probe never allocates. bytes views the
// caller's or the live page's record and is only read within the
// operation that produced it; raw is a value.
type Key struct {
	Hi    uint64
	Lo    uint64
	bytes []byte
	// Raw holds the up-to-40 ordered probe bytes of a hash key (Rust
	// HashKey value), zero-padded past its length; meaningful only for
	// keys built by RawKey. Codecs read it as a plain value field: a
	// method returning a slice of the receiver would allocate a copy.
	Raw   [40]byte
	isRaw bool
}

// IsVar reports whether the key is a variable-size byte key.
func (k Key) IsVar() bool { return k.bytes != nil || k.isRaw }

// Bytes returns the long variable key bytes, or nil for fixed keys. Hash
// probe keys never call Bytes: their codecs consume the raw field
// directly (returning a slice of the receiver would allocate a copy).
func (k Key) Bytes() []byte { return k.bytes }

// RawKey builds a variable key from ordered bytes of at most 40 (Rust
// HashKey value). The bytes are copied into the key, so the caller's
// buffer may be a stack local.
func RawKey(bytes []byte) Key {
	var k Key
	copy(k.Raw[:], bytes)
	k.isRaw = true
	return k
}

// Less reports k < other. A variable key compares greater than any fixed
// key, but codecs never mix the two forms.
func (k Key) Less(other Key) bool {
	if k.isRaw || other.isRaw {
		return bytes.Compare(k.Raw[:], other.Raw[:]) < 0
	}
	if k.bytes != nil || other.bytes != nil {
		return bytes.Compare(k.bytes, other.bytes) < 0
	}
	return k.Hi < other.Hi || (k.Hi == other.Hi && k.Lo < other.Lo)
}

// Equal reports k == other.
func (k Key) Equal(other Key) bool {
	if k.isRaw || other.isRaw {
		return bytes.Equal(k.Raw[:], other.Raw[:])
	}
	if k.bytes != nil || other.bytes != nil {
		return bytes.Equal(k.bytes, other.bytes)
	}
	return k.Hi == other.Hi && k.Lo == other.Lo
}

// VarKey builds a variable key from its ordered bytes.
func VarKey(bytes []byte) Key { return Key{bytes: bytes} }

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
	RestoreDirty(pageNumber uint32, tag uint32) error
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
	return store.RestoreDirty(destination, tag)
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
