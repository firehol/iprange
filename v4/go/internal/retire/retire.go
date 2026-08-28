// Package retire implements the ordered retirement-extent B+tree, mirroring
// the Rust retirement module. When a COW edit replaces committed pages, the
// replaced pages are recorded here as (transaction, first, count) extents,
// coalescing neighbors; the publication and reclaim paths consume the
// extents in transaction order.
package retire

import (
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// Wire layout (Rust retirement.rs): 12-byte key (txn u64 LE, first u32 LE)
// plus 4-byte count.
const (
	branchType = format.PageTypeRetirementBranch
	leafType   = format.PageTypeRetirementLeaf
	aux        = uint32(0)
	keySize    = 12
	cellSize   = 16

	txnOffset   = 0
	firstOffset = 8
	countOffset = 12
)

// Key is one retirement key: pages retired by one transaction.
type Key struct {
	Txn   uint64
	First uint32
}

// ToTree converts the key to the tree comparison primitive: the 12
// canonical compare bytes (big-endian txn, big-endian first page).
func (k Key) ToTree() tree.Key {
	var bytes [12]byte
	binary.BigEndian.PutUint64(bytes[0:8], k.Txn)
	binary.BigEndian.PutUint32(bytes[8:12], k.First)
	return tree.KeyOfFixed(bytes[:])
}

// Extent is one contiguous retired range of a transaction (Rust Extent).
type Extent struct {
	Key   Key
	Count uint32
}

// Transaction returns the retiring transaction.
func (e Extent) Transaction() uint64 { return e.Key.Txn }

// FirstPage returns the first retired page number.
func (e Extent) FirstPage() uint32 { return e.Key.First }

// Pages returns the retired page range [first, first+count).
func (e Extent) Pages() (first, end uint64) {
	first = uint64(e.Key.First)
	end = first + uint64(e.Count)
	return
}

// PageCount returns the number of retired pages.
func (e Extent) PageCount() uint64 { return uint64(e.Count) }

// Reclamation is one bounded reclamation selection (Rust Reclamation).
type Reclamation struct {
	Transactions uint64
	Pages        uint64
	ThroughTxn   uint64
}

type codec struct{}

func (codec) BranchType() format.PageType { return branchType }
func (codec) LeafType() format.PageType   { return leafType }
func (codec) Aux() uint32                 { return aux }
func (codec) KeySize() int                { return keySize }
func (codec) LeafSize() int               { return cellSize }

func (codec) ReadKey(cell []byte, _ uint16) (tree.Key, error) {
	if len(cell) < keySize {
		return tree.Key{}, corrupt("retirement key is truncated")
	}
	return Key{Txn: format.U64(cell[txnOffset:]), First: format.U32(cell[firstOffset:])}.ToTree(), nil
}

// CompareKey compares one cell key without materializing a Key (Rust
// Key Ord): the retirement key is the composite (transaction, first
// page), so the compare splits the 12-key bytes into the u64 txn and
// the u32 first field.
func (codec) CompareKey(cell []byte, _ uint16, target tree.Key) (int, error) {
	if len(cell) < keySize {
		return 0, corrupt("retirement key is truncated")
	}
	bytes := target.FixedBytes()
	if compare := cmpU64(format.U64(cell[txnOffset:]), binary.BigEndian.Uint64(bytes[0:8])); compare != 0 {
		return compare, nil
	}
	return cmpU32(format.U32(cell[firstOffset:]), binary.BigEndian.Uint32(bytes[8:12])), nil
}

func (codec) ReadLeaf(cell []byte) (Extent, error) {
	if len(cell) != cellSize {
		return Extent{}, corrupt("retirement leaf has the wrong record size")
	}
	key := Key{Txn: format.U64(cell[txnOffset:]), First: format.U32(cell[firstOffset:])}
	count := format.U32(cell[countOffset:])
	if key.Txn <= 1 || key.First < 2 || count == 0 {
		return Extent{}, corrupt("retirement extent has invalid fields")
	}
	if uint64(key.First)+uint64(count) > 1<<32 {
		return Extent{}, corrupt("retirement extent endpoint overflow")
	}
	return Extent{Key: key, Count: count}, nil
}

func (codec) WriteKey(key tree.Key, output []byte) {
	bytes := key.FixedBytes()
	format.PutU64(output[txnOffset:], binary.BigEndian.Uint64(bytes[0:8]))
	format.PutU32(output[firstOffset:], binary.BigEndian.Uint32(bytes[8:12]))
}

func encode(extent Extent) []byte {
	cell := make([]byte, cellSize)
	format.PutU64(cell[txnOffset:], extent.Key.Txn)
	format.PutU32(cell[firstOffset:], extent.Key.First)
	format.PutU32(cell[countOffset:], extent.Count)
	return cell
}

// AddPage records one retired page, coalescing with the previous and next
// extents of the same transaction (Rust retirement::add_page). Returns the
// COW pages retired by the operation itself.
func AddPage(store tree.Store, root *uint32, extentCount *uint64, txn uint64, pageNumber uint32) (tree.RetiredPages, error) {
	if txn <= 1 {
		return tree.RetiredPages{}, invalid("retirement transaction must be above creation")
	}
	if pageNumber < 2 {
		return tree.RetiredPages{}, corrupt("a meta page cannot be retired")
	}
	key := Key{Txn: txn, First: pageNumber}
	previous, hasPrevious, err := predecessor(store, *root, key)
	if err != nil {
		return tree.RetiredPages{}, err
	}
	next, hasNext, err := atOrAfter(store, *root, key)
	if err != nil {
		return tree.RetiredPages{}, err
	}
	around := retiredNeighbors{previous: previous, hasPrevious: hasPrevious, next: next, hasNext: hasNext}
	kind, err := classifyNeighbors(key, around)
	if err != nil {
		return tree.RetiredPages{}, err
	}
	return applyPage(store, root, extentCount, key, around, kind)
}

// retiredNeighbors carries the extents adjacent to a retired-page key
// (Rust Option semantics without heap indirection).
type retiredNeighbors struct {
	previous    Extent
	hasPrevious bool
	next        Extent
	hasNext     bool
}

func classifyNeighbors(key Key, n retiredNeighbors) (neighbors, error) {
	if n.hasNext && n.next.Key == key {
		return 0, corrupt("page is already retired")
	}
	if n.hasPrevious && n.previous.Key.Txn == key.Txn &&
		uint64(key.First) < uint64(n.previous.Key.First)+uint64(n.previous.Count) {
		return 0, corrupt("page is already retired")
	}
	joinsPrevious := n.hasPrevious && n.previous.Key.Txn == key.Txn &&
		uint64(n.previous.Key.First)+uint64(n.previous.Count) == uint64(key.First)
	joinsNext := n.hasNext && n.next.Key.Txn == key.Txn &&
		uint64(key.First)+1 == uint64(n.next.Key.First)
	switch {
	case !joinsPrevious && !joinsNext:
		return neighborsNeither, nil
	case joinsPrevious && !joinsNext:
		return neighborsPrevious, nil
	case !joinsPrevious && joinsNext:
		return neighborsNext, nil
	case joinsPrevious && joinsNext:
		return neighborsBoth, nil
	default:
		return 0, corrupt("retirement neighbor classification failed")
	}
}

type neighbors uint8

const (
	neighborsNeither neighbors = iota
	neighborsPrevious
	neighborsNext
	neighborsBoth
)

func applyPage(store tree.Store, root *uint32, extentCount *uint64, key Key, n retiredNeighbors, kind neighbors) (tree.RetiredPages, error) {
	var retired tree.RetiredPages
	switch kind {
	case neighborsNeither:
		return insert(store, root, extentCount, Extent{Key: key, Count: 1}, retired)
	case neighborsPrevious:
		count, err := grow(n.previous.Count, 1)
		if err != nil {
			return retired, err
		}
		return insert(store, root, extentCount, Extent{Key: n.previous.Key, Count: count}, retired)
	case neighborsNext:
		retired, err := remove(store, root, extentCount, n.next.Key, retired)
		if err != nil {
			return retired, err
		}
		count, err := grow(n.next.Count, 1)
		if err != nil {
			return retired, err
		}
		return insert(store, root, extentCount, Extent{Key: key, Count: count}, retired)
	case neighborsBoth:
		retired, err := remove(store, root, extentCount, n.previous.Key, retired)
		if err != nil {
			return retired, err
		}
		retired, err = remove(store, root, extentCount, n.next.Key, retired)
		if err != nil {
			return retired, err
		}
		merged, err := grow(n.previous.Count, 1)
		if err != nil {
			return retired, err
		}
		merged, err = grow(merged, n.next.Count)
		if err != nil {
			return retired, err
		}
		return insert(store, root, extentCount, Extent{Key: n.previous.Key, Count: merged}, retired)
	}
	return retired, nil
}

func grow(count, by uint32) (uint32, error) {
	if uint64(count)+uint64(by) > 1<<32 {
		return 0, overflow("retirement extent length")
	}
	return count + by, nil
}

func insert(store tree.Store, root *uint32, extentCount *uint64, extent Extent, retired tree.RetiredPages) (tree.RetiredPages, error) {
	retired, inserted, err := tree.Insert(codec{}, store, root, encode(extent), retired)
	if err != nil {
		return tree.RetiredPages{}, err
	}
	if inserted {
		if *extentCount == ^uint64(0) {
			return tree.RetiredPages{}, overflow("retirement extent count")
		}
		*extentCount = *extentCount + 1
	}
	return retired, nil
}

func remove(store tree.Store, root *uint32, extentCount *uint64, key Key, retired tree.RetiredPages) (tree.RetiredPages, error) {
	retired, err := tree.DeleteExisting(codec{}, store, root, key.ToTree(), retired)
	if err != nil {
		return tree.RetiredPages{}, err
	}
	if *extentCount == 0 {
		return tree.RetiredPages{}, overflow("retirement extent count underflows")
	}
	*extentCount = *extentCount - 1
	return retired, nil
}

// RemoveExtent removes one whole extent (Rust remove_extent).
func RemoveExtent(store tree.Store, root *uint32, extentCount *uint64, extent Extent) (tree.RetiredPages, error) {
	return remove(store, root, extentCount, extent.Key, tree.RetiredPages{})
}

func predecessor(store tree.Store, root uint32, key Key) (Extent, bool, error) {
	return tree.Predecessor(codec{}, store, root, key.ToTree())
}

func atOrAfter(store tree.Store, root uint32, key Key) (Extent, bool, error) {
	return tree.AtOrAfter(codec{}, store, root, key.ToTree())
}

// First returns the lowest retirement extent (Rust retirement::first).
func First(store tree.Store, root uint32) (Extent, bool, error) {
	return atOrAfter(store, root, Key{Txn: 0, First: 0})
}

// After returns the first extent strictly after extent (Rust after).
func After(store tree.Store, root uint32, extent Extent) (Extent, bool, error) {
	first := extent.Key.First + 1
	txn := extent.Key.Txn
	if first == 0 {
		// first+1 wrapped at the u32 boundary: the next possible key is
		// the first extent of the next transaction; an overflowing txn
		// has no successor at all (Rust checked_add chain: first, then
		// txn, else None).
		if txn == ^uint64(0) {
			return Extent{}, false, nil
		}
		txn++
	}
	return atOrAfter(store, root, Key{Txn: txn, First: first})
}

// SelectReclamation selects the reclamation-safe prefix of the retirement
// tree (Rust select_reclamation_with_checkpoint): transaction groups not
// older than any live reader, bounded by the work limits.
// runCheckpoint invokes the optional checkpoint hook; a nil hook is a no-op
// (offline reclamation performs no live-reader checkpoint).
func runCheckpoint(checkpoint func() error) error {
	if checkpoint == nil {
		return nil
	}
	return checkpoint()
}

// SelectReclamation selects the reclamation-safe prefix of the
// retirement tree (Rust select_reclamation): transaction groups that no
// live reader can still hold, bounded by the work limits.
func SelectReclamation(store tree.Store, root uint32, selectedTxn uint64, oldestReader *uint64, maxTransactions, maxPages uint64, checkpoint func() error) (*Reclamation, error) {
	if maxTransactions == 0 || maxPages == 0 {
		return nil, invalid("reclamation work limits must be nonzero")
	}
	next, hasNext, err := First(store, root)
	if err != nil {
		return nil, err
	}
	selected := Reclamation{}
	for selected.Transactions < maxTransactions {
		if err := runCheckpoint(checkpoint); err != nil {
			return nil, err
		}
		if !hasNext {
			break
		}
		group, ok, err := safeGroup(store, root, next, selectedTxn, oldestReader, checkpoint)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if selected.Transactions == 0 && group.pages > maxPages {
			return nil, &format.Error{Code: format.CodeWorkLimitTooSmall, Detail: "reclamation work limit too small"}
		}
		ok, err = appendGroup(&selected, group.txn, group.pages, maxPages)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		next = group.next
		hasNext = group.hasNext
	}
	if selected.Transactions == 0 {
		return nil, nil
	}
	return &selected, nil
}

type group struct {
	txn     uint64
	pages   uint64
	next    Extent
	hasNext bool
}

func safeGroup(store tree.Store, root uint32, extent Extent, selectedTxn uint64, oldestReader *uint64, checkpoint func() error) (*group, bool, error) {
	if err := validateSelected(store, extent, selectedTxn); err != nil {
		return nil, false, err
	}
	if !readerSafe(oldestReader, extent.Key.Txn) {
		return nil, false, nil
	}
	pages, next, hasNext, err := scanGroup(store, root, extent, selectedTxn, checkpoint)
	if err != nil {
		return nil, false, err
	}
	return &group{txn: extent.Key.Txn, pages: pages, next: next, hasNext: hasNext}, true, nil
}

func readerSafe(oldestReader *uint64, retiredByTxn uint64) bool {
	if oldestReader == nil {
		return true
	}
	return *oldestReader >= retiredByTxn
}

func appendGroup(selected *Reclamation, txn uint64, groupPages uint64, maxPages uint64) (bool, error) {
	// Rust append_group: checked_add propagates ArithmeticOverflow, only a
	// limit exceed returns Ok(false); unreachable for coalesced
	// u32-bounded extents but the fail-closed shape is parity.
	total, ok := checkedAddPages(selected.Pages, groupPages)
	if !ok {
		return false, overflow("reclaimed page count")
	}
	if total > maxPages {
		return false, nil
	}
	selected.Transactions++
	selected.Pages = total
	selected.ThroughTxn = txn
	return true, nil
}

func scanGroup(store tree.Store, root uint32, firstExtent Extent, selectedTxn uint64, checkpoint func() error) (pages uint64, next Extent, hasNext bool, err error) {
	txn := firstExtent.Key.Txn
	extent := firstExtent
	for {
		if err := runCheckpoint(checkpoint); err != nil {
			return 0, Extent{}, false, err
		}
		if err := validateSelected(store, extent, selectedTxn); err != nil {
			return 0, Extent{}, false, err
		}
		nextCount, ok := checkedAddPages(pages, uint64(extent.Count))
		if !ok {
			return 0, Extent{}, false, overflow("reclaimed page count")
		}
		pages = nextCount
		next, hasNext, err = After(store, root, extent)
		if err != nil {
			return 0, Extent{}, false, err
		}
		if !hasNext {
			return pages, Extent{}, false, nil
		}
		if next.Key.Txn != txn {
			return pages, next, true, nil
		}
		end := uint64(extent.Key.First) + uint64(extent.Count)
		if uint64(next.Key.First) <= end {
			return 0, Extent{}, false, corrupt("retirement extents overlap or are not coalesced")
		}
		extent = next
	}
}

func validateSelected(store tree.Store, extent Extent, selectedTxn uint64) error {
	_, end := extent.Pages()
	if extent.Key.Txn > selectedTxn || end > store.PageLimit() {
		return corrupt("retirement extent exceeds the selected generation")
	}
	return nil
}

func corrupt(detail string) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
}

func invalid(detail string) error {
	return &format.Error{Code: format.CodeInvalidArgument, Detail: detail}
}

func overflow(detail string) error {
	return &format.Error{Code: format.CodeArithmeticOverflow, Detail: detail}
}

// checkedAddPages adds two page counts, reporting overflow (Rust
// checked_add semantics for the reclamation page totals).
func checkedAddPages(a, b uint64) (uint64, bool) {
	if ^uint64(0)-a < b {
		return 0, false
	}
	return a + b, true
}

// CellSize is the fixed retirement cell size (Rust retirement.rs
// CELL_SIZE).
const CellSize = cellSize

// DecodeKey decodes one retirement key from a cell without field
// validation (Rust retirement::decode_key; only the byte length is
// required, so the explicit validation walk can classify the fields
// itself).
func DecodeKey(cell []byte) (Key, bool) {
	if len(cell) < keySize {
		return Key{}, false
	}
	return Key{
		Txn:   format.U64(cell[txnOffset : txnOffset+8]),
		First: format.U32(cell[firstOffset : firstOffset+4]),
	}, true
}

// DecodeBranchChild decodes the child page of one retirement branch cell
// (Rust retirement::decode_branch_child; the child occupies the count
// slot of the 16-byte branch cell).
func DecodeBranchChild(cell []byte) (uint32, bool) {
	if len(cell) != cellSize {
		return 0, false
	}
	return format.U32(cell[countOffset : countOffset+4]), true
}

// DecodeRaw decodes one raw 16-byte retirement cell without field
// validation (Rust retirement::decode_raw); the validation extent checks
// classify the fields.
func DecodeRaw(cell []byte) (Extent, bool) {
	if len(cell) != cellSize {
		return Extent{}, false
	}
	key, ok := DecodeKey(cell)
	if !ok {
		return Extent{}, false
	}
	return Extent{Key: key, Count: format.U32(cell[countOffset : countOffset+4])}, true
}

func cmpU32(a, b uint32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func cmpU64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
