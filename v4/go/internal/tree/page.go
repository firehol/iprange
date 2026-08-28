// Codec-driven tree page encoding and search (Rust fixed_tree/page.rs).

package tree

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Header is the tree view of one slotted page geometry (Rust
// slotted_page::Header).
type Header = format.PageHeader

// parse validates one complete tree page view at the expected level and
// returns its header by value (Rust slotted_page::parse_tree). The value
// return keeps the descent loops allocation-free: a heap-allocated header
// would be one object per visited page per operation.
func parse[T any](codec Codec[T], page []byte, selectedTxn uint64, expectedLevel uint16, checkLevel bool) (Header, error) {
	work.PageParse(1)
	h, err := format.DecodePageHeader(page, selectedTxn)
	if err != nil {
		return Header{}, corrupt("slotted-page header is invalid: " + err.Error())
	}
	expectedType := codec.LeafType()
	if h.Level != 0 {
		expectedType = codec.BranchType()
	}
	if h.PageType != expectedType || h.Aux != codec.Aux() {
		return Header{}, corrupt("slotted-page type or discriminator is invalid")
	}
	if checkLevel && expectedLevel != h.Level {
		return Header{}, corrupt("slotted-page child level is invalid")
	}
	if !format.SlottedShapeValid(&h) {
		return Header{}, corrupt("slotted-page bounds are invalid")
	}
	return h, nil
}

// FixedSearch is one fixed-cell page whose shape was checked once; every
// probe re-checks the persistent slot value (Rust FixedSearch).
type FixedSearch struct {
	page    []byte
	header  Header
	cellLen int
}

func newFixedSearch(page []byte, header Header, cellLen int) (FixedSearch, error) {
	if len(page) != format.PageSize || !format.SlottedShapeValid(&header) ||
		cellLen == 0 || cellLen > format.PageSize {
		return FixedSearch{}, corrupt("fixed slotted-page search shape is invalid")
	}
	return FixedSearch{page: page, header: header, cellLen: cellLen}, nil
}

// cellAt reads the fixed cell at an index already bounded by the search
// algorithm (Rust FixedSearch::cell_at): the page shape was validated
// once by newFixedSearch and the caller guarantees index < ItemCount, so
// the slot-table read needs no per-probe index re-check. The persistent
// slot value stays untrusted and its complete extent is validated on
// every probe; the returned slice is a view of the caller's page.
func (f FixedSearch) cellAt(index int) ([]byte, error) {
	work.CellProbe(1)
	work.SlotRead(1)
	start := int(format.U16(f.page[format.SlottedHeaderSize+index*2:]))
	if start < int(f.header.Upper) || start > format.PageSize-f.cellLen {
		return nil, corrupt("slotted-page cell is outside the record area")
	}
	return f.page[start : start+f.cellLen], nil
}

// lowerBound locates the first index whose key is >= key (Rust
// fixed_tree/page.rs lower_bound). With insertion true the result is the
// insertion point; otherwise a nonexact result steps back one record (the
// greatest key < key).
//
// Codecs that carry the ordered key as a plain fixed cell prefix opt in
// to the inline prefix probe (prefixKeyProbe): those searches read one
// persistent slot and the key prefix per probe and never materialize a
// Key, never dispatch through the codec interface, and never call per
// probe through a closure. Every other codec runs the equivalent
// closure-free loop through Codec.CompareKey. Both loops keep the
// persistent slot-table read per probe (Rust slotted_page cell) and
// reuse the final probe for the exact-match check.
func lowerBound[T any](codec Codec[T], page []byte, header *Header, key Key, insertion bool) (int, bool, error) {
	if _, prefix := codec.(PrefixKeyProbe); prefix {
		if cellLen, fixed := FixedCellSize(codec, header.Level); fixed {
			var validate func(cell []byte) error
			if v, ok := codec.(ProbeValidator); ok {
				validate = v.ValidateProbeCell
			}
			return fixedLowerBound(page, header, cellLen, codec.KeySize(), key, insertion, validate)
		}
	}
	return lowerBoundCompare(codec, page, header, key, insertion)
}

// PrefixKeyProbe is the optional plain-prefix probe contract (Rust
// fixed_tree key_at inlined into lower_bound). A codec implements it
// exactly when every fixed-size cell of its pages (branch cells and
// non-variable leaf cells) carries the entire ordered key as the cell
// prefix in one of the canonical layouts handled by comparePrefixKey.
// The codec remains the authority for its geometry through KeySize and
// LeafSize; the marker only selects the inline compare.
type PrefixKeyProbe interface {
	PrefixKeyProbe()
}

// ProbeValidator is the optional per-probe semantic check of the
// inline fixed-cell probe (Rust read_key decoding inside lower_bound):
// codecs whose cells carry validated fields beyond the ordered key
// bytes (the membership and structure hash suffix IDs and word counts)
// implement it so the probe rejects exactly what the codec's ReadKey
// rejects, while plain numeric-prefix codecs keep the zero-dispatch
// probe.
type ProbeValidator interface {
	ValidateProbeCell(cell []byte) error
}

// fixedLowerBound is the width-specialized binary search over fixed-size
// cells (Rust fixed_tree/page.rs lower_bound with an inlined key_at):
// per probe it reads the persistent slot and compares only the key
// prefix bytes, so the hot path allocates nothing, builds no Key value,
// dispatches through no interface, and calls no closure.
func fixedLowerBound(page []byte, header *Header, cellLen, keySize int, key Key, insertion bool, validate func(cell []byte) error) (int, bool, error) {
	if keySize == 0 || keySize > cellLen {
		return 0, false, corrupt("fixed slotted-page search shape is invalid")
	}
	search, err := newFixedSearch(page, *header, cellLen)
	if err != nil {
		return 0, false, err
	}
	lower := 0
	upper := int(header.ItemCount)
	lastCompare := 0
	lastIndex := -1
	for lower < upper {
		middle := lower + (upper-lower)/2
		work.KeyProbe(1)
		cell, err := search.cellAt(middle)
		if err != nil {
			return 0, false, corrupt("slotted-page cell is outside the record area")
		}
		if validate != nil {
			if err := validate(cell); err != nil {
				return 0, false, err
			}
		}
		compare, err := comparePrefixKey(cell, keySize, key)
		if err != nil {
			return 0, false, err
		}
		lastCompare = compare
		lastIndex = middle
		if compare < 0 {
			lower = middle + 1
		} else {
			upper = middle
		}
	}
	exists := false
	if lower < int(header.ItemCount) {
		compare := lastCompare
		if lastIndex != lower {
			work.KeyProbe(1)
			cell, err := search.cellAt(lower)
			if err != nil {
				return 0, false, corrupt("slotted-page cell is outside the record area")
			}
			if validate != nil {
				if err := validate(cell); err != nil {
					return 0, false, err
				}
			}
			var errCompare error
			compare, errCompare = comparePrefixKey(cell, keySize, key)
			if errCompare != nil {
				return 0, false, errCompare
			}
		}
		exists = compare == 0
	}
	if insertion || exists || lower == 0 {
		return lower, exists, nil
	}
	return lower - 1, false, nil
}

// comparePrefixKey compares one fixed cell's key prefix with the target
// key without materializing a Key (Rust key_at plus the derived Ord of
// the key type, inlined). The canonical layouts are: 4-byte and 8-byte
// little-endian cell widths compared against the target's canonical
// big-endian bytes, 12-byte (u64 transaction, u32 page), 16-byte
// little-endian into the numeric high and low limbs, and 32+ byte raw
// probe cells whose digest bytes compare byte-for-byte while every
// little-endian u32 suffix word compares numerically with the probe's
// big-endian word. Narrower or wider widths are geometry errors; codecs
// with other key layouts (variable leaves, composite keys) stay on
// Codec.CompareKey.
func comparePrefixKey(cell []byte, keySize int, key Key) (int, error) {
	switch keySize {
	case 4:
		if len(cell) < 4 {
			return 0, corrupt("tree key is truncated")
		}
		return cmpU32(format.U32(cell), key.U32()), nil
	case 8:
		if len(cell) < 8 {
			return 0, corrupt("tree key is truncated")
		}
		return cmpU64(format.U64(cell), key.U64()), nil
	case 12:
		if len(cell) < 12 {
			return 0, corrupt("tree key is truncated")
		}
		if compare := cmpU64(format.U64(cell), key.U64()); compare != 0 {
			return compare, nil
		}
		return cmpU32(format.U32(cell[8:12]), beU32(key.data[8:12])), nil
	case 16:
		if len(cell) < 16 {
			return 0, corrupt("tree key is truncated")
		}
		hi, lo := format.U128(cell)
		thi, tlo := key.U128()
		return cmpU128(hi, lo, thi, tlo), nil
	default:
		return CompareRawKey(cell, keySize, &key)
	}
}

// CompareRawKey compares one raw-key cell with the normalized probe
// bytes (Rust HashKey derive Ord): the digest bytes compare
// byte-for-byte and every little-endian u32 suffix word compares
// numerically with the probe's big-endian word, so wire cells order
// exactly like the Rust derived Ord without materializing a probe key.
// Codecs with digest-plus-numeric keys (membership hash, structure
// hash) share this single ordering authority with the inline prefix
// probe.
func CompareRawKey(cell []byte, keySize int, target *Key) (int, error) {
	if len(cell) < keySize || keySize > len(target.data) || keySize < 32 {
		return 0, corrupt("tree key is truncated")
	}
	if compare := bytes.Compare(cell[:32], target.data[:32]); compare != 0 {
		return compare, nil
	}
	for at := 32; at+4 <= keySize; at += 4 {
		if compare := cmpU32(format.U32(cell[at:at+4]), beU32(target.data[at:at+4])); compare != 0 {
			return compare, nil
		}
	}
	return 0, nil
}

func beU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func beU64(b []byte) uint64 {
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

func bePutU64(b []byte, v uint64) {
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
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

func cmpU128(ahi, alo, bhi, blo uint64) int {
	if compare := cmpU64(ahi, bhi); compare != 0 {
		return compare
	}
	return cmpU64(alo, blo)
}

// lowerBoundCompare is the closure-free general search (Rust
// fixed_tree/page.rs lower_bound_by): per probe it reads one cell
// through the codec geometry (fixed or variable) and asks the codec to
// compare the cell key with the target without materializing a Key.
func lowerBoundCompare[T any](codec Codec[T], page []byte, header *Header, key Key, insertion bool) (int, bool, error) {
	lower := 0
	upper := int(header.ItemCount)
	lastCompare := 0
	lastIndex := -1
	for lower < upper {
		middle := lower + (upper-lower)/2
		work.KeyProbe(1)
		cell, err := codecCell(codec, page, header, middle)
		if err != nil {
			return 0, false, err
		}
		compare, err := codec.CompareKey(cell, header.Level, key)
		if err != nil {
			return 0, false, err
		}
		lastCompare = compare
		lastIndex = middle
		if compare < 0 {
			lower = middle + 1
		} else {
			upper = middle
		}
	}
	exists := false
	if lower < int(header.ItemCount) {
		compare := lastCompare
		if lastIndex != lower {
			work.KeyProbe(1)
			cell, err := codecCell(codec, page, header, lower)
			if err != nil {
				return 0, false, err
			}
			compare, err = codec.CompareKey(cell, header.Level, key)
			if err != nil {
				return 0, false, err
			}
		}
		exists = compare == 0
	}
	if insertion || exists || lower == 0 {
		return lower, exists, nil
	}
	return lower - 1, false, nil
}

// lowerBoundBy is the reference closure-driven search kept for the
// probe-count pinning test and as the textual mirror of the Rust
// lower_bound_by; production searches use the closure-free loops above.
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
func keyAt[T any](codec Codec[T], page []byte, header *Header, index int) (Key, error) {
	cell, err := codecCell(codec, page, header, index)
	if err != nil {
		return Key{}, err
	}
	return codec.ReadKey(cell, header.Level)
}

// branchChild reads and validates one branch child page number (Rust
// branch_child). Variable branch records name the child through the
// codec's ReadBranchChild override.
func branchChild[T any](codec Codec[T], page []byte, header *Header, index int, pageLimit uint64) (uint32, error) {
	cell, err := codecCell(codec, page, header, index)
	if err != nil {
		return 0, err
	}
	var child uint32
	if variable, ok := codec.(VariableCodec[T]); ok && codec.KeySize() == 0 {
		child, err = variable.ReadBranchChild(cell)
	} else {
		child = readChild(cell, codec.KeySize())
	}
	if err != nil {
		return 0, err
	}
	if child < 2 || uint64(child) >= pageLimit {
		return 0, corrupt("B+tree child page is invalid")
	}
	return child, nil
}

func readChild(cell []byte, keySize int) uint32 {
	return format.U32(cell[keySize : keySize+4])
}

// codecCell reads one leaf or branch cell by level (Rust codec_cell).
// Page levels with variable-size records (codec size zero) read through
// the concrete slotted record helper with the codec's length bounds;
// fixed-size levels keep the fast slotted path. Cell bytes are always
// slices of the caller's page (never copied).
func codecCell[T any](codec Codec[T], page []byte, header *Header, index int) ([]byte, error) {
	variable, hasVariable := codec.(VariableCodec[T])
	if header.Level == 0 {
		if hasVariable && codec.LeafSize() == 0 {
			minimum, maximum := variable.LeafRecordBounds()
			cell, err := format.SlottedRecord(page, header, index, minimum, maximum)
			if err != nil {
				return nil, err
			}
			return cell, nil
		}
		cell, err := format.SlottedCell(page, header, index, codec.LeafSize())
		if err != nil {
			return nil, corrupt("slotted-page cell is outside the record area")
		}
		return cell, nil
	}
	if hasVariable && codec.KeySize() == 0 {
		minimum, maximum := variable.BranchRecordBounds()
		cell, err := format.SlottedRecord(page, header, index, minimum, maximum)
		if err != nil {
			return nil, err
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

// newBranchCell encodes one branch cell (key + child) into the caller's
// buffer (Rust CellBuf::branch returns a stack value; the Go caller owns
// the 512-byte CellBuf so encoding never allocates). Variable branch
// records (catalog name branches) route through the codec's WriteBranch
// override, which returns the encoded length.
func newBranchCell[T any](codec Codec[T], key Key, child uint32, out *CellBuf) error {
	if variable, ok := codec.(VariableCodec[T]); ok && codec.KeySize() == 0 {
		length, err := variable.WriteBranch(key, child, out.bytes[:])
		if err != nil {
			return err
		}
		out.len = length
	} else {
		out.len = codec.KeySize() + 4
		codec.WriteKey(key, out.bytes[:codec.KeySize()])
		format.PutU32(out.bytes[codec.KeySize():out.len], child)
	}
	if out.len == 0 || out.len > MaxBranchSize(codec) || out.len > maxTreeCell {
		return unsupported("B+tree branch encoding is invalid")
	}
	return nil
}

// Bytes returns the encoded branch cell.
func (c CellBuf) Bytes() []byte { return c.bytes[:c.len] }

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
func editFits[T any](codec Codec[T], page []byte, header *Header, edit Edit) (bool, error) {
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
func replacementFits[T any](codec Codec[T], page []byte, header *Header, edit Replacement) (bool, error) {
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
func splitIndex[T any](codec Codec[T], page []byte, header *Header, edit Edit) (int, error) {
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
func replacementSplitIndex[T any](codec Codec[T], page []byte, header *Header, edit Replacement) (int, error) {
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
func buildEdit[T any](codec Codec[T], source []byte, header *Header, edit Edit, start, end int, output []byte) error {
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
func buildReplacement[T any](codec Codec[T], source []byte, header *Header, edit Replacement, start, end int, output []byte) error {
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
func virtualCell[T any](codec Codec[T], source []byte, header *Header, edit Edit, virtualIndex int) ([]byte, error) {
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
func replacementCell[T any](codec Codec[T], source []byte, header *Header, edit Replacement, index int) ([]byte, error) {
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
func truncate[T any](codec Codec[T], page []byte, header *Header, keep int) (Header, error) {
	if cellLen, ok := FixedCellSize(codec, header.Level); ok {
		shape, err := format.SlottedTruncateFixed(page, header, keep, cellLen)
		if err != nil {
			return Header{}, corrupt(err.Error())
		}
		return shapeHeader(header, shape), nil
	}
	shape, err := format.SlottedTruncate(page, header, keep)
	if err != nil {
		return Header{}, corrupt(err.Error())
	}
	return shapeHeader(header, shape), nil
}

func shapeHeader(header *Header, shape format.SlottedShape) Header {
	out := *header
	out.ItemCount = uint16(shape.ItemCount)
	out.Lower = shape.Lower
	out.Upper = shape.Upper
	return out
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
