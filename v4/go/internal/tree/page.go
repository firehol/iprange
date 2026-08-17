// Codec-driven tree page encoding and search (Rust fixed_tree/page.rs).

package tree

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Header is the tree view of one slotted page geometry (Rust
// slotted_page::Header).
type Header = format.PageHeader

// parse validates one complete tree page view at the expected level and
// returns its header (Rust slotted_page::parse_tree).
func parse(codec Codec, page []byte, selectedTxn uint64, expectedLevel *uint16) (*Header, error) {
	work.PageParse(1)
	h, err := format.DecodePageHeader(page, selectedTxn)
	if err != nil {
		return nil, corrupt("slotted-page header is invalid: " + err.Error())
	}
	expectedType := codec.LeafType()
	if h.Level != 0 {
		expectedType = codec.BranchType()
	}
	if h.PageType != expectedType || h.Aux != codec.Aux() {
		return nil, corrupt("slotted-page type or discriminator is invalid")
	}
	if expectedLevel != nil && *expectedLevel != h.Level {
		return nil, corrupt("slotted-page child level is invalid")
	}
	if !format.SlottedShapeValid(&h) {
		return nil, corrupt("slotted-page bounds are invalid")
	}
	return &h, nil
}

// FixedSearch is one fixed-cell page whose shape was checked once; every
// probe re-checks the persistent slot value (Rust FixedSearch).
type FixedSearch struct {
	page    []byte
	header  *Header
	cellLen int
}

func newFixedSearch(page []byte, header *Header, cellLen int) (FixedSearch, error) {
	if len(page) != format.PageSize || !format.SlottedShapeValid(header) ||
		cellLen == 0 || cellLen > format.PageSize {
		return FixedSearch{}, corrupt("fixed slotted-page search shape is invalid")
	}
	return FixedSearch{page: page, header: header, cellLen: cellLen}, nil
}

// cellAt reads the fixed cell at an index already bounded by the search
// algorithm (Rust FixedSearch::cell_at).
func (f FixedSearch) cellAt(index int) ([]byte, error) {
	cell, err := format.SlottedCell(f.page, f.header, index, f.cellLen)
	if err != nil {
		return nil, corrupt("slotted-page cell is outside the record area")
	}
	return cell, nil
}

// lowerBound locates the first index whose key is >= key (Rust
// fixed_tree/page.rs lower_bound). With insertion true the result is the
// insertion point; otherwise a nonexact result steps back one record (the
// greatest key < key).
func lowerBound(codec Codec, page []byte, header *Header, key Key, insertion bool) (int, bool, error) {
	if cellLen, ok := FixedCellSize(codec, header.Level); ok {
		return lowerBoundFixed(codec, page, header, key, insertion, cellLen)
	}
	return lowerBoundBy(header, key, insertion, func(index int) (Key, error) {
		return keyAt(codec, page, header, index)
	})
}

func lowerBoundFixed(codec Codec, page []byte, header *Header, key Key, insertion bool, cellLen int) (int, bool, error) {
	search, err := newFixedSearch(page, header, cellLen)
	if err != nil {
		return 0, false, err
	}
	return lowerBoundBy(header, key, insertion, func(index int) (Key, error) {
		cell, err := search.cellAt(index)
		if err != nil {
			return Key{}, err
		}
		return codec.ReadKey(cell, header.Level)
	})
}

func lowerBoundBy(header *Header, key Key, insertion bool, keyAt func(int) (Key, error)) (int, bool, error) {
	lower := 0
	upper := int(header.ItemCount)
	var lastKey Key
	lastValid := false
	lastIndex := 0
	for lower < upper {
		middle := lower + (upper-lower)/2
		work.KeyProbe(1)
		current, err := keyAt(middle)
		if err != nil {
			return 0, false, err
		}
		lastKey = current
		lastValid = true
		lastIndex = middle
		if current.Less(key) {
			lower = middle + 1
		} else {
			upper = middle
		}
	}
	exists := false
	if lower < int(header.ItemCount) {
		current := lastKey
		if !lastValid || lastIndex != lower {
			work.KeyProbe(1)
			var err error
			current, err = keyAt(lower)
			if err != nil {
				return 0, false, err
			}
		}
		exists = current.Equal(key)
	}
	if insertion || exists || lower == 0 {
		return lower, exists, nil
	}
	return lower - 1, false, nil
}

// keyAt decodes the key of one cell (Rust key_at).
func keyAt(codec Codec, page []byte, header *Header, index int) (Key, error) {
	cell, err := codecCell(codec, page, header, index)
	if err != nil {
		return Key{}, err
	}
	return codec.ReadKey(cell, header.Level)
}

// branchChild reads and validates one branch child page number (Rust
// branch_child).
func branchChild(codec Codec, page []byte, header *Header, index int, pageLimit uint64) (uint32, error) {
	cell, err := codecCell(codec, page, header, index)
	if err != nil {
		return 0, err
	}
	child := readChild(cell, codec.KeySize())
	if child < 2 || uint64(child) >= pageLimit {
		return 0, corrupt("B+tree child page is invalid")
	}
	return child, nil
}

func readChild(cell []byte, keySize int) uint32 {
	return format.U32(cell[keySize : keySize+4])
}

// codecCell reads one leaf or branch cell by level (Rust codec_cell).
func codecCell(codec Codec, page []byte, header *Header, index int) ([]byte, error) {
	if header.Level == 0 {
		cell, err := format.SlottedCell(page, header, index, codec.LeafSize())
		if err != nil {
			return nil, corrupt("slotted-page cell is outside the record area")
		}
		return cell, nil
	}
	cell, err := format.SlottedCell(page, header, index, codec.KeySize()+4)
	if err != nil {
		return nil, corrupt("slotted-page cell is outside the record area")
	}
	return cell, nil
}

// CellBuf is one bounded branch-cell buffer (Rust CellBuf).
type CellBuf struct {
	bytes [maxTreeCell]byte
	len   int
}

// newBranchCell encodes one branch cell (key + child).
func newBranchCell(codec Codec, key Key, child uint32) (*CellBuf, error) {
	var cell CellBuf
	cell.len = codec.KeySize() + 4
	if cell.len == 0 || cell.len > MaxBranchSize(codec) || cell.len > maxTreeCell {
		return nil, unsupported("B+tree branch encoding is invalid")
	}
	codec.WriteKey(key, cell.bytes[:codec.KeySize()])
	format.PutU32(cell.bytes[codec.KeySize():cell.len], child)
	return &cell, nil
}

// Bytes returns the encoded branch cell.
func (c *CellBuf) Bytes() []byte { return c.bytes[:c.len] }

// Edit is one in-place or inserted cell edit (Rust Edit).
type Edit struct {
	index   int
	replace bool
	cell    []byte
}

func (e Edit) total(sourceCount int) int {
	if e.replace {
		return sourceCount
	}
	return sourceCount + 1
}

// Replacement is one multi-cell replacement of a single cell (Rust
// Replacement).
type Replacement struct {
	index int
	cells [][]byte
}

func (r Replacement) total(sourceCount int) int { return sourceCount + len(r.cells) - 1 }

// editFits reports whether one edit fits the free area (Rust edit_fits).
func editFits(codec Codec, page []byte, header *Header, edit Edit) (bool, error) {
	work.EditFitProbe(1)
	if edit.replace {
		if edit.index >= int(header.ItemCount) {
			return false, corrupt("B+tree replacement index is invalid")
		}
		oldLen, err := codecCell(codec, page, header, edit.index)
		if err != nil {
			return false, err
		}
		return format.SlottedReplaceFits(header, len(oldLen), len(edit.cell)), nil
	}
	if edit.index > int(header.ItemCount) {
		return false, corrupt("B+tree insertion index is invalid")
	}
	return format.SlottedInsertFits(header, len(edit.cell)), nil
}

// replacementFits reports whether one multi-cell replacement fits (Rust
// replacement_fits).
func replacementFits(codec Codec, page []byte, header *Header, edit Replacement) (bool, error) {
	work.EditFitProbe(1)
	if edit.index >= int(header.ItemCount) || len(edit.cells) == 0 {
		return false, corrupt("B+tree replacement is invalid")
	}
	oldCell, err := codecCell(codec, page, header, edit.index)
	if err != nil {
		return false, err
	}
	available := len(oldCell) + int(header.Upper-header.Lower)
	payload := 0
	for _, cell := range edit.cells {
		payload += len(cell)
	}
	required := payload + (len(edit.cells)-1)*2
	return required <= available, nil
}

// splitIndex picks the split point (Rust split_index).
func splitIndex(codec Codec, page []byte, header *Header, edit Edit) (int, error) {
	total := edit.total(int(header.ItemCount))
	if cellLen, ok := FixedCellSize(codec, header.Level); ok {
		return fixedSplitIndex(total, cellLen)
	}
	return splitBySize(total, func(index int) (int, error) {
		cell, err := virtualCell(codec, page, header, edit, index)
		if err != nil {
			return 0, err
		}
		return len(cell), nil
	})
}

// replacementSplitIndex picks the split point for one replacement (Rust
// replacement_split_index).
func replacementSplitIndex(codec Codec, page []byte, header *Header, edit Replacement) (int, error) {
	total := edit.total(int(header.ItemCount))
	if cellLen, ok := FixedCellSize(codec, header.Level); ok {
		return fixedSplitIndex(total, cellLen)
	}
	return splitBySize(total, func(index int) (int, error) {
		cell, err := replacementCell(codec, page, header, edit, index)
		if err != nil {
			return 0, err
		}
		return len(cell), nil
	})
}

// buildEdit builds one fresh page from a source page and one edit over
// [start, end) virtual records (Rust build_edit).
func buildEdit(codec Codec, source []byte, header *Header, edit Edit, start, end int, output []byte) error {
	pageType := codec.LeafType()
	if header.Level != 0 {
		pageType = codec.BranchType()
	}
	b := format.NewSlottedBuilder(output, pageType, format.U64(source[format.HeaderBorn:]), header.Level, codec.Aux())
	for virtualIndex := start; virtualIndex < end; virtualIndex++ {
		cell, err := virtualCell(codec, source, header, edit, virtualIndex)
		if err != nil {
			return err
		}
		if err := b.Push(output, cell); err != nil {
			return corrupt("B+tree page build failed: " + err.Error())
		}
	}
	return b.Finish(output)
}

// buildReplacement builds one fresh page from a source page and one
// replacement over [start, end) (Rust build_replacement).
func buildReplacement(codec Codec, source []byte, header *Header, edit Replacement, start, end int, output []byte) error {
	pageType := codec.LeafType()
	if header.Level != 0 {
		pageType = codec.BranchType()
	}
	b := format.NewSlottedBuilder(output, pageType, format.U64(source[format.HeaderBorn:]), header.Level, codec.Aux())
	for index := start; index < end; index++ {
		cell, err := replacementCell(codec, source, header, edit, index)
		if err != nil {
			return err
		}
		if err := b.Push(output, cell); err != nil {
			return corrupt("B+tree page build failed: " + err.Error())
		}
	}
	return b.Finish(output)
}

// virtualCell maps one virtual index to the edited or existing cell (Rust
// virtual_cell).
func virtualCell(codec Codec, source []byte, header *Header, edit Edit, virtualIndex int) ([]byte, error) {
	if virtualIndex == edit.index {
		return edit.cell, nil
	}
	sourceIndex := virtualIndex
	if virtualIndex > edit.index && !edit.replace {
		sourceIndex = virtualIndex - 1
	}
	return codecCell(codec, source, header, sourceIndex)
}

// replacementCell maps one virtual index to a replacement cell or an
// existing cell (Rust replacement_cell).
func replacementCell(codec Codec, source []byte, header *Header, edit Replacement, index int) ([]byte, error) {
	if offset := index - edit.index; offset >= 0 && offset < len(edit.cells) {
		return edit.cells[offset], nil
	}
	sourceIndex := index
	if index >= edit.index+len(edit.cells) {
		sourceIndex = index - len(edit.cells) + 1
	}
	return codecCell(codec, source, header, sourceIndex)
}

// truncate keeps the first keep logical records (Rust page::truncate).
func truncate(codec Codec, page []byte, header *Header, keep int) (*Header, error) {
	if cellLen, ok := FixedCellSize(codec, header.Level); ok {
		shape, err := format.SlottedTruncateFixed(page, header, keep, cellLen)
		if err != nil {
			return nil, corrupt(err.Error())
		}
		return shapeHeader(header, shape), nil
	}
	shape, err := format.SlottedTruncate(page, header, keep)
	if err != nil {
		return nil, corrupt(err.Error())
	}
	return shapeHeader(header, shape), nil
}

func shapeHeader(header *Header, shape format.SlottedShape) *Header {
	out := *header
	out.ItemCount = uint16(shape.ItemCount)
	out.Lower = shape.Lower
	out.Upper = shape.Upper
	return &out
}

func fixedSplitIndex(total, cellLen int) (int, error) {
	if total < 2 {
		return 0, corrupt("B+tree split has fewer than two records")
	}
	middle := total / 2
	if pageSize(middle, middle*cellLen) > format.PageSize ||
		pageSize(total-middle, (total-middle)*cellLen) > format.PageSize {
		return 0, invalid("B+tree record cannot be split")
	}
	return middle, nil
}

func pageSize(count, payload int) int {
	return count*2 + format.SlottedHeaderSize + payload
}

func splitBySize(total int, cellLen func(int) (int, error)) (int, error) {
	if total < 2 {
		return 0, corrupt("B+tree split has fewer than two records")
	}
	payload := 0
	for index := 0; index < total; index++ {
		length, err := cellLen(index)
		if err != nil {
			return 0, err
		}
		payload += length
	}
	leftPayload := 0
	best := -1
	bestDifference := 0
	for middle := 1; middle < total; middle++ {
		length, err := cellLen(middle - 1)
		if err != nil {
			return 0, err
		}
		leftPayload += length
		difference, ok := splitDifference(total, middle, payload, leftPayload)
		if !ok {
			continue
		}
		if best == -1 || difference < bestDifference {
			best = middle
			bestDifference = difference
		}
	}
	if best == -1 {
		return 0, invalid("B+tree record cannot be split")
	}
	return best, nil
}

func splitDifference(total, middle, payload, leftPayload int) (int, bool) {
	left := pageSize(middle, leftPayload)
	right := pageSize(total-middle, payload-leftPayload)
	if left > format.PageSize || right > format.PageSize {
		return 0, false
	}
	difference := left - right
	if difference < 0 {
		difference = -difference
	}
	return difference, true
}
