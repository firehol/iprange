// Package tree implements the generic mapped-page COW B+tree core,
// mirroring the Rust fixed_tree module. It is the single authoritative
// implementation of persistent ordered-tree editing: the retirement tree,
// the range/catalog/membership trees, and the structure hash tree all
// compose this core with their own codecs. All page views come from the
// caller's Store; no complete page is ever owned here.
package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Key is an ordered two-limb tree key: numeric order is (Hi, Lo). The v4
// ordered keys (retirement transaction extents, IPv4/IPv6 range bounds)
// all fit this single comparison primitive.
type Key struct {
	Hi uint64
	Lo uint64
}

// Less reports k < other.
func (k Key) Less(other Key) bool { return k.Hi < other.Hi || (k.Hi == other.Hi && k.Lo < other.Lo) }

// Equal reports k == other.
func (k Key) Equal(other Key) bool { return k.Hi == other.Hi && k.Lo == other.Lo }

// Codec is the per-tree wire contract (Rust fixed_tree::Codec). Branch
// cells are key bytes followed by a 4-byte child page number; leaf cells
// are codec-specific fixed-size cells.
type Codec interface {
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
	ReadLeaf(cell []byte) (any, error)
	// WriteKey encodes one key prefix into output (keySize bytes).
	WriteKey(key Key, output []byte)
}

// MaxBranchSize is the largest branch cell (Rust MAX_BRANCH_SIZE).
func MaxBranchSize(codec Codec) int { return codec.KeySize() + 4 }

// MaxLeafSize is the largest leaf cell (Rust MAX_LEAF_SIZE).
func MaxLeafSize(codec Codec) int { return codec.LeafSize() }

// FixedCellSize reports the fixed cell length for one level: leaf cells at
// level 0, branch cells above (Rust Codec::fixed_cell_size). A zero size
// means the page uses variable-size records.
func FixedCellSize(codec Codec, level uint16) (int, bool) {
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
// fixed_tree::Store). Every page callback receives a view of the mapped
// page at its final offset; production stores hand out direct mapping
// slices. Callbacks capture output values by closure.
type Store interface {
	TargetTxn() uint64
	PageLimit() uint64
	Inspect(pageNumber uint32, fn func(page []byte) error) error
	Allocate() (uint32, error)
	Update(pageNumber uint32, fn func(page []byte) error) error
	CopyPage(source, destination uint32, fn func(source, output []byte) error) error
	DiscardPrivate(pageNumber uint32) error
}

// RetiringStore extends Store with the retirement sink (Rust
// fixed_tree::RetiringStore).
type RetiringStore interface {
	Store
	RetirePages(pages []uint32) error
}

// CopyForCow copies one complete committed page into an allocated private
// page: full byte copy, born transaction stamped to the draft, checksum
// cleared (Rust Store::copy_for_cow). The page byte count is not counted
// as owned memory: the destination is mapped, never a heap buffer.
func CopyForCow(store Store, source, destination uint32) error {
	target := store.TargetTxn()
	return store.CopyPage(source, destination, func(src, output []byte) error {
		copy(output, src)
		format.PutU64(output[format.HeaderBorn:], target)
		format.PutU32(output[format.HeaderCRC:], 0)
		return nil
	})
}

// requireCodec rejects unusable codec geometry up front (Rust
// fixed_tree/page.rs require_codec).
func requireCodec(codec Codec) error {
	branchSize := MaxBranchSize(codec)
	leafSize := MaxLeafSize(codec)
	if branchSize == 0 || leafSize == 0 ||
		branchSize+2+format.SlottedHeaderSize > format.PageSize ||
		leafSize+2+format.SlottedHeaderSize > format.PageSize ||
		branchSize > maxTreeCell {
		return invalid("invalid B+tree codec")
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
