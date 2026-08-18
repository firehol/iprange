// Package retire implements the ordered retirement-extent B+tree, mirroring
// the Rust retirement module. When a COW edit replaces committed pages, the
// replaced pages are recorded here as (transaction, first, count) extents,
// coalescing neighbors; the publication and reclaim paths consume the
// extents in transaction order.
package retire

import (
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

// ToTree converts the key to the tree comparison primitive.
func (k Key) ToTree() tree.Key { return tree.Key{Hi: k.Txn, Lo: uint64(k.First)} }

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

func (codec) ReadLeaf(cell []byte) (any, error) {
	if len(cell) != cellSize {
		return nil, corrupt("retirement leaf has the wrong record size")
	}
	key := Key{Txn: format.U64(cell[txnOffset:]), First: format.U32(cell[firstOffset:])}
	count := format.U32(cell[countOffset:])
	if key.Txn <= 1 || key.First < 2 || count == 0 {
		return nil, corrupt("retirement extent has invalid fields")
	}
	if uint64(key.First)+uint64(count) > 1<<32 {
		return nil, corrupt("retirement extent endpoint overflow")
	}
	return Extent{Key: key, Count: count}, nil
}

func (codec) WriteKey(key tree.Key, output []byte) {
	format.PutU64(output[txnOffset:], key.Hi)
	format.PutU32(output[firstOffset:], uint32(key.Lo))
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
func AddPage(store tree.Store, root *uint32, extentCount *uint64, txn uint64, pageNumber uint32) (*tree.RetiredPages, error) {
	if txn <= 1 {
		return nil, invalid("retirement transaction must be above creation")
	}
	if pageNumber < 2 {
		return nil, corrupt("a meta page cannot be retired")
	}
	key := Key{Txn: txn, First: pageNumber}
	previous, err := predecessor(store, *root, key)
	if err != nil {
		return nil, err
	}
	next, err := atOrAfter(store, *root, key)
	if err != nil {
		return nil, err
	}
	kind, err := classifyNeighbors(key, previous, next)
	if err != nil {
		return nil, err
	}
	return applyPage(store, root, extentCount, key, previous, next, kind)
}

func classifyNeighbors(key Key, previous, next *Extent) (neighbors, error) {
	if next != nil && next.Key == key {
		return 0, corrupt("page is already retired")
	}
	if previous != nil && previous.Key.Txn == key.Txn &&
		uint64(key.First) < uint64(previous.Key.First)+uint64(previous.Count) {
		return 0, corrupt("page is already retired")
	}
	joinsPrevious := false
	if previous != nil {
		joinsPrevious = previous.Key.Txn == key.Txn &&
			uint64(previous.Key.First)+uint64(previous.Count) == uint64(key.First)
	}
	joinsNext := false
	if next != nil {
		joinsNext = next.Key.Txn == key.Txn && uint64(key.First)+1 == uint64(next.Key.First)
	}
	switch {
	case !joinsPrevious && !joinsNext:
		return neighborsNeither, nil
	case previous != nil && joinsPrevious && !joinsNext:
		return neighborsPrevious, nil
	case next != nil && !joinsPrevious && joinsNext:
		return neighborsNext, nil
	case previous != nil && next != nil && joinsPrevious && joinsNext:
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

func applyPage(store tree.Store, root *uint32, extentCount *uint64, key Key, previous, next *Extent, kind neighbors) (*tree.RetiredPages, error) {
	retired := tree.NewRetiredPages()
	switch kind {
	case neighborsNeither:
		return retired, insert(store, root, extentCount, Extent{Key: key, Count: 1}, retired)
	case neighborsPrevious:
		count, err := grow(previous.Count, 1)
		if err != nil {
			return retired, err
		}
		return retired, insert(store, root, extentCount, Extent{Key: previous.Key, Count: count}, retired)
	case neighborsNext:
		if err := remove(store, root, extentCount, next.Key, retired); err != nil {
			return retired, err
		}
		count, err := grow(next.Count, 1)
		if err != nil {
			return retired, err
		}
		return retired, insert(store, root, extentCount, Extent{Key: key, Count: count}, retired)
	case neighborsBoth:
		if err := remove(store, root, extentCount, previous.Key, retired); err != nil {
			return retired, err
		}
		if err := remove(store, root, extentCount, next.Key, retired); err != nil {
			return retired, err
		}
		merged, err := grow(previous.Count, 1)
		if err != nil {
			return retired, err
		}
		merged, err = grow(merged, next.Count)
		if err != nil {
			return retired, err
		}
		return retired, insert(store, root, extentCount, Extent{Key: previous.Key, Count: merged}, retired)
	}
	return retired, nil
}

func grow(count, by uint32) (uint32, error) {
	if uint64(count)+uint64(by) > 1<<32 {
		return 0, overflow("retirement extent length")
	}
	return count + by, nil
}

func insert(store tree.Store, root *uint32, extentCount *uint64, extent Extent, retired *tree.RetiredPages) error {
	inserted, err := tree.Insert(codec{}, store, root, encode(extent), retired)
	if err != nil {
		return err
	}
	if inserted {
		if *extentCount == ^uint64(0) {
			return overflow("retirement extent count")
		}
		*extentCount = *extentCount + 1
	}
	return nil
}

func remove(store tree.Store, root *uint32, extentCount *uint64, key Key, retired *tree.RetiredPages) error {
	if err := tree.DeleteExisting(codec{}, store, root, key.ToTree(), retired); err != nil {
		return err
	}
	if *extentCount == 0 {
		return overflow("retirement extent count underflows")
	}
	*extentCount = *extentCount - 1
	return nil
}

// RemoveExtent removes one whole extent (Rust remove_extent).
func RemoveExtent(store tree.Store, root *uint32, extentCount *uint64, extent Extent) (*tree.RetiredPages, error) {
	retired := tree.NewRetiredPages()
	if err := remove(store, root, extentCount, extent.Key, retired); err != nil {
		return nil, err
	}
	return retired, nil
}

func predecessor(store tree.Store, root uint32, key Key) (*Extent, error) {
	value, err := tree.Predecessor(codec{}, store, root, key.ToTree())
	if err != nil {
		return nil, err
	}
	return asExtent(value), nil
}

func atOrAfter(store tree.Store, root uint32, key Key) (*Extent, error) {
	value, err := tree.AtOrAfter(codec{}, store, root, key.ToTree())
	if err != nil {
		return nil, err
	}
	return asExtent(value), nil
}

func asExtent(value any) *Extent {
	if value == nil {
		return nil
	}
	extent := value.(Extent)
	return &extent
}

// First returns the lowest retirement extent (Rust retirement::first).
func First(store tree.Store, root uint32) (*Extent, error) {
	return atOrAfter(store, root, Key{Txn: 0, First: 0})
}

// After returns the first extent strictly after extent (Rust after).
func After(store tree.Store, root uint32, extent Extent) (*Extent, error) {
	first := extent.Key.First + 1
	txn := extent.Key.Txn
	if first == 0 {
		// first+1 wrapped at the u32 boundary: the next possible key is
		// the first extent of the next transaction; an overflowing txn
		// has no successor at all (Rust checked_add chain: first, then
		// txn, else None).
		if txn == ^uint64(0) {
			return nil, nil
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

func SelectReclamation(store tree.Store, root uint32, selectedTxn uint64, oldestReader *uint64, maxTransactions, maxPages uint64, checkpoint func() error) (*Reclamation, error) {
	if maxTransactions == 0 || maxPages == 0 {
		return nil, invalid("reclamation work limits must be nonzero")
	}
	next, err := First(store, root)
	if err != nil {
		return nil, err
	}
	selected := Reclamation{}
	for selected.Transactions < maxTransactions {
		if err := runCheckpoint(checkpoint); err != nil {
			return nil, err
		}
		if next == nil {
			break
		}
		group, ok, err := safeGroup(store, root, *next, selectedTxn, oldestReader, checkpoint)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		if selected.Transactions == 0 && group.pages > maxPages {
			return nil, &format.Error{Code: format.CodeWorkLimitTooSmall, Detail: "reclamation work limit too small"}
		}
		if !appendGroup(&selected, group.txn, group.pages, maxPages) {
			break
		}
		next = group.next
	}
	if selected.Transactions == 0 {
		return nil, nil
	}
	return &selected, nil
}

type group struct {
	txn   uint64
	pages uint64
	next  *Extent
}

func safeGroup(store tree.Store, root uint32, extent Extent, selectedTxn uint64, oldestReader *uint64, checkpoint func() error) (*group, bool, error) {
	if err := validateSelected(store, extent, selectedTxn); err != nil {
		return nil, false, err
	}
	if !readerSafe(oldestReader, extent.Key.Txn) {
		return nil, false, nil
	}
	pages, next, err := scanGroup(store, root, extent, selectedTxn, checkpoint)
	if err != nil {
		return nil, false, err
	}
	return &group{txn: extent.Key.Txn, pages: pages, next: next}, true, nil
}

func readerSafe(oldestReader *uint64, retiredByTxn uint64) bool {
	if oldestReader == nil {
		return true
	}
	return *oldestReader >= retiredByTxn
}

func appendGroup(selected *Reclamation, txn uint64, groupPages uint64, maxPages uint64) bool {
	total := selected.Pages + groupPages
	if total > maxPages {
		return false
	}
	selected.Transactions++
	selected.Pages = total
	selected.ThroughTxn = txn
	return true
}

func scanGroup(store tree.Store, root uint32, firstExtent Extent, selectedTxn uint64, checkpoint func() error) (uint64, *Extent, error) {
	txn := firstExtent.Key.Txn
	extent := firstExtent
	var pages uint64
	for {
		if err := runCheckpoint(checkpoint); err != nil {
			return 0, nil, err
		}
		if err := validateSelected(store, extent, selectedTxn); err != nil {
			return 0, nil, err
		}
		pages += uint64(extent.Count)
		next, err := After(store, root, extent)
		if err != nil {
			return 0, nil, err
		}
		if next == nil {
			return pages, nil, nil
		}
		if next.Key.Txn != txn {
			return pages, next, nil
		}
		end := uint64(extent.Key.First) + uint64(extent.Count)
		if uint64(next.Key.First) <= end {
			return 0, nil, corrupt("retirement extents overlap or are not coalesced")
		}
		extent = *next
	}
}

func validateSelected(store tree.Store, extent Extent, selectedTxn uint64) error {
	first, end := extent.Pages()
	if extent.Key.Txn > selectedTxn || end > store.PageLimit() {
		return corrupt("retirement extent exceeds the selected generation")
	}
	_ = first
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
