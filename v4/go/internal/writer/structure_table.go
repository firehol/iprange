// Direct ID-indexed mapped structure records (Rust
// structured_value/table.rs): a sparse radix table over the structure-ID
// namespace. A level-0 record page holds StructureRecordSlots fixed
// records addressed by id % slots; a level>0 directory page holds 512
// child page numbers addressed by (id / coverage(level-1)) % 512. The
// geometry constants are shared with the reader through format/structure.go
// (StructureRecordSlots, StructureRecordSize, StructureLeafEnd,
// StructureBranchEnd, StructureRootLevel, StructureSpanOfLevel). Every
// record and child is written at its final offset inside the store's
// mapped page; the one-shot output builder allocates only private pages,
// so COW touches never produce retired pages there, and the append-only
// store refuses any retirement exactly like the Rust Builder.

package writer

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

const (
	structureBranchChildren = format.StructureDirectoryChildCount // 512
	structureMaxLevel       = 3
)

// structureTableHeader is one parsed structure table page header (Rust
// table::Header).
type structureTableHeader struct {
	level     uint16
	itemCount uint16
}

// structureHeaderProblem classifies one header defect (Rust
// HeaderProblem).
type structureHeaderProblem uint8

const (
	structureHeaderOK structureHeaderProblem = iota
	structureHeaderProblemHeader
	structureHeaderProblemBorn
	structureHeaderProblemType
	structureHeaderProblemLevel
	structureHeaderProblemShape
)

// structurePathFrame is one branch of a located record path (Rust Frame).
type structurePathFrame struct {
	pageNumber uint32
	childIndex int
	itemCount  uint16
}

// structurePath tracks the branch frames of one refcount descent (Rust
// Path).
type structurePath struct {
	frames [structureMaxLevel]structurePathFrame
	depth  int
}

func (p *structurePath) push(frame structurePathFrame) error {
	if p.depth >= len(p.frames) {
		return corrupt("structure table path exceeds maximum height")
	}
	p.frames[p.depth] = frame
	p.depth++
	return nil
}

// structureTableFind locates and decodes one structure record (Rust
// table::find + read_record): an absent root or empty slot is a clean
// miss; a nonzero slot that decodes to a different id is corruption.
func structureTableFind(codec structurePayloadCodec, store tree.Store, root uint32, idLimit uint64, id uint32) (structureRecord, bool, error) {
	leaf, found, err := structureTableLocateLeaf(codec, store, root, idLimit, id)
	if err != nil || !found {
		return structureRecord{}, false, err
	}
	var record structureRecord
	recordFound := false
	err = store.Inspect(leaf, func(page []byte) error {
		cell, err := structureTableLeafCell(codec, page, id)
		if err != nil {
			return err
		}
		storedID := format.U32(cell[structureIDOffset:])
		if storedID == 0 {
			if allZero(cell) {
				return nil
			}
			return corrupt("empty structure table slot is nonzero")
		}
		decoded, err := decodeStructureRecord(codec, cell)
		if err != nil {
			return err
		}
		if decoded.id != id {
			return corrupt("structure table record is in the wrong slot")
		}
		record = decoded
		recordFound = true
		return nil
	})
	return record, recordFound, err
}

// structureTableLocateLeaf descends the radix path of id (Rust
// locate_leaf): id zero or at/above the limit, an empty root, or an empty
// directory child is a clean miss.
func structureTableLocateLeaf(codec structurePayloadCodec, store tree.Store, root uint32, idLimit uint64, id uint32) (uint32, bool, error) {
	if id == 0 || uint64(id) >= idLimit || root == 0 {
		return 0, false, nil
	}
	if root < 2 || uint64(root) >= store.PageLimit() {
		return 0, false, corrupt("structure table root is outside page bounds")
	}
	level, err := structureRequiredLevel(idLimit)
	if err != nil {
		return 0, false, err
	}
	pageNumber := root
	for level > 0 {
		child := uint32(0)
		if err := store.Inspect(pageNumber, func(page []byte) error {
			header, err := structureTableParse(codec, page, store.TargetTxn(), &level)
			if err != nil {
				return err
			}
			index, err := structureChildIndex(id, level)
			if err != nil {
				return err
			}
			child, err = structureBranchChild(page, header, index, store.PageLimit())
			return err
		}); err != nil {
			return 0, false, err
		}
		if child == 0 {
			return 0, false, nil
		}
		pageNumber = child
		level--
	}
	return pageNumber, true, nil
}

// structureTableInsert writes one record into the radix table, growing
// the root and creating subtrees as needed (Rust table::insert): the ID
// must be below the limit, and the record slot must be empty.
func structureTableInsert(codec structurePayloadCodec, store tree.RetiringStore, root *uint32, idLimit uint64, record []byte) error {
	decoded, err := decodeStructureRecord(codec, record)
	if err != nil {
		return err
	}
	if uint64(decoded.id) >= idLimit {
		return corrupt("structure table insertion exceeds its limit")
	}
	targetLevel, err := structureRequiredLevel(idLimit)
	if err != nil {
		return err
	}
	retired := tree.NewRetiredPages()
	if err := structureGrowRoot(codec, store, root, targetLevel); err != nil {
		return err
	}
	if *root == 0 {
		pageNumber, err := structureNewSubtree(codec, store, targetLevel, decoded.id, record)
		if err != nil {
			return err
		}
		*root = pageNumber
		return nil
	}
	privateRoot, header, err := structureTableTouch(codec, store, *root, targetLevel, retired)
	if err != nil {
		return err
	}
	*root = privateRoot
	pageNumber := privateRoot
	for header.level > 0 {
		index, err := structureChildIndex(decoded.id, header.level)
		if err != nil {
			return err
		}
		child := uint32(0)
		if err := store.Inspect(pageNumber, func(page []byte) error {
			var err error
			child, err = structureBranchChild(page, header, index, store.PageLimit())
			return err
		}); err != nil {
			return err
		}
		if child == 0 {
			child, err := structureNewSubtree(codec, store, header.level-1, decoded.id, record)
			if err != nil {
				return err
			}
			if err := store.Update(pageNumber, func(page []byte) error {
				return structureSetBranchChild(page, header, index, child)
			}); err != nil {
				return err
			}
			return store.RetirePages(retired.Slice())
		}
		privateChild, nextHeader, err := structureTableTouch(codec, store, child, header.level-1, retired)
		if err != nil {
			return err
		}
		if privateChild != child {
			if err := store.Update(pageNumber, func(page []byte) error {
				return structureReplaceBranchChild(page, header, index, privateChild)
			}); err != nil {
				return err
			}
		}
		pageNumber = privateChild
		header = nextHeader
	}
	if err := store.Update(pageNumber, func(page []byte) error {
		return structureInsertLeaf(codec, page, header, record)
	}); err != nil {
		return err
	}
	return store.RetirePages(retired.Slice())
}

// structureTableChangeRefcount applies one refcount change to one record,
// deleting the record and collapsing empty pages when the refcount reaches
// zero (Rust table::change_refcount).
func structureTableChangeRefcount(codec structurePayloadCodec, store tree.RetiringStore, root *uint32, idLimit uint64, id uint32, change int64) (structureRecord, bool, error) {
	if id == 0 || uint64(id) >= idLimit || *root == 0 {
		return structureRecord{}, false, corrupt("structure refcount names an absent ID")
	}
	expectedLevel, err := structureRequiredLevel(idLimit)
	if err != nil {
		return structureRecord{}, false, err
	}
	retired := tree.NewRetiredPages()
	privateRoot, header, err := structureTableTouch(codec, store, *root, expectedLevel, retired)
	if err != nil {
		return structureRecord{}, false, err
	}
	*root = privateRoot
	pageNumber := privateRoot
	var path structurePath
	for header.level > 0 {
		index, err := structureChildIndex(id, header.level)
		if err != nil {
			return structureRecord{}, false, err
		}
		child := uint32(0)
		if err := store.Inspect(pageNumber, func(page []byte) error {
			var err error
			child, err = structureBranchChild(page, header, index, store.PageLimit())
			return err
		}); err != nil {
			return structureRecord{}, false, err
		}
		if child == 0 {
			return structureRecord{}, false, corrupt("structure refcount path is missing")
		}
		if err := path.push(structurePathFrame{
			pageNumber: pageNumber,
			childIndex: index,
			itemCount:  header.itemCount,
		}); err != nil {
			return structureRecord{}, false, err
		}
		privateChild, nextHeader, err := structureTableTouch(codec, store, child, header.level-1, retired)
		if err != nil {
			return structureRecord{}, false, err
		}
		if privateChild != child {
			if err := store.Update(pageNumber, func(page []byte) error {
				return structureReplaceBranchChild(page, header, index, privateChild)
			}); err != nil {
				return structureRecord{}, false, err
			}
		}
		pageNumber = privateChild
		header = nextHeader
	}
	record, found, err := structureReadRecordAt(codec, store, pageNumber, id)
	if err != nil {
		return structureRecord{}, false, err
	}
	if !found {
		return structureRecord{}, false, corrupt("structure refcount ID is missing")
	}
	next, err := structureChangedRefcount(record.refcount, change)
	if err != nil {
		return structureRecord{}, false, err
	}
	if next != 0 {
		if err := store.Update(pageNumber, func(page []byte) error {
			at := structureRecordOffset(id) + structureRefcountOffset
			if at+8 > len(page) {
				return corrupt("structure table record is outside its page")
			}
			format.PutU64(page[at:], next)
			return nil
		}); err != nil {
			return structureRecord{}, false, err
		}
		if err := store.RetirePages(retired.Slice()); err != nil {
			return structureRecord{}, false, err
		}
		return record, false, nil
	}
	if err := store.Update(pageNumber, func(page []byte) error {
		return structureDeleteLeaf(page, header, id)
	}); err != nil {
		return structureRecord{}, false, err
	}
	if err := structureRemoveEmptyPath(store, root, pageNumber, header.itemCount-1, &path); err != nil {
		return structureRecord{}, false, err
	}
	if err := store.RetirePages(retired.Slice()); err != nil {
		return structureRecord{}, false, err
	}
	return record, true, nil
}

// structureTableShrink lowers the root while its only child is the whole
// tree (Rust table::shrink).
func structureTableShrink(codec structurePayloadCodec, store tree.RetiringStore, root *uint32, idLimit uint64) error {
	if *root == 0 {
		if idLimit != 1 {
			return corrupt("empty structure table has a nonempty limit")
		}
		return nil
	}
	wanted, err := structureRequiredLevel(idLimit)
	if err != nil {
		return err
	}
	retired := tree.NewRetiredPages()
	for {
		header, err := structureParsePage(codec, store, *root, nil)
		if err != nil {
			return err
		}
		if header.level == wanted {
			return store.RetirePages(retired.Slice())
		}
		if header.level < wanted || header.itemCount != 1 {
			return corrupt("structure table root cannot shrink")
		}
		private, privateHeader, err := structureTableTouch(codec, store, *root, header.level, retired)
		if err != nil {
			return err
		}
		*root = private
		child := uint32(0)
		if err := store.Inspect(private, func(page []byte) error {
			var err error
			child, err = structureBranchChild(page, privateHeader, 0, store.PageLimit())
			if err != nil {
				return err
			}
			for index := 1; index < structureBranchChildren; index++ {
				other, err := structureRawBranchChild(page, index)
				if err != nil {
					return err
				}
				if other != 0 {
					return corrupt("structure table root has data above its new limit")
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if child == 0 {
			return corrupt("structure table shrinking root has no first child")
		}
		*root = child
		if err := store.DiscardPrivate(private); err != nil {
			return err
		}
	}
}

// structureTableParse validates one page header (Rust table::parse):
// every defect class maps to the exact Rust detail string.
func structureTableParse(codec structurePayloadCodec, page []byte, selectedTxn uint64, expectedLevel *uint16) (structureTableHeader, error) {
	header, problem := structureInspectHeader(codec, page, selectedTxn, expectedLevel)
	switch problem {
	case structureHeaderProblemHeader:
		return structureTableHeader{}, corrupt("structure table page header is invalid")
	case structureHeaderProblemBorn:
		return structureTableHeader{}, corrupt("structure table page transaction is invalid")
	case structureHeaderProblemType:
		return structureTableHeader{}, corrupt("structure table page type is invalid")
	case structureHeaderProblemLevel:
		return structureTableHeader{}, corrupt("structure table page level is invalid")
	case structureHeaderProblemShape:
		return structureTableHeader{}, corrupt("structure table page shape is invalid")
	}
	return header, nil
}

// structureParsePage parses one store page (Rust shrink's
// inspect_page + parse).
func structureParsePage(codec structurePayloadCodec, store tree.Store, pageNumber uint32, expectedLevel *uint16) (structureTableHeader, error) {
	var header structureTableHeader
	err := store.Inspect(pageNumber, func(page []byte) error {
		var err error
		header, err = structureTableParse(codec, page, store.TargetTxn(), expectedLevel)
		return err
	})
	return header, err
}

// structureInspectHeader mirrors the Rust inspect_header problem
// classification: common header, born transaction, level bound, page type
// and aux, and the fixed geometry (leaf_end or branch_end lower, page-size
// upper, nonzero item count within the level maximum).
func structureInspectHeader(codec structurePayloadCodec, page []byte, selectedTxn uint64, expectedLevel *uint16) (structureTableHeader, structureHeaderProblem) {
	if !structureCommonValid(page) {
		return structureTableHeader{}, structureHeaderProblemHeader
	}
	if !structureBornValid(page, selectedTxn) {
		return structureTableHeader{}, structureHeaderProblemBorn
	}
	level := format.U16(page[format.HeaderLevel:])
	if level > structureMaxLevel || (expectedLevel != nil && *expectedLevel != level) {
		return structureTableHeader{}, structureHeaderProblemLevel
	}
	pageType := format.PageTypeStructureIDRecord
	lower := uint16(format.StructureLeafEnd)
	if level != 0 {
		pageType = format.PageTypeStructureIDDirectory
		lower = format.StructureBranchEnd
	}
	if page[format.HeaderType] != byte(pageType) || format.U32(page[format.HeaderAux:]) != uint32(codec.kind()) {
		return structureTableHeader{}, structureHeaderProblemType
	}
	if format.U16(page[format.HeaderLower:]) != lower || format.U16(page[format.HeaderUpper:]) != format.PageSize {
		return structureTableHeader{}, structureHeaderProblemShape
	}
	itemCount := format.U16(page[format.HeaderCount:])
	maximum := uint16(format.StructureRecordSlots)
	if level != 0 {
		maximum = structureBranchChildren
	}
	if itemCount == 0 || itemCount > maximum {
		return structureTableHeader{}, structureHeaderProblemShape
	}
	return structureTableHeader{level: level, itemCount: itemCount}, structureHeaderOK
}

func structureCommonValid(page []byte) bool {
	return len(page) == format.PageSize &&
		bytes.Equal(page[format.HeaderMagic:format.HeaderMagic+4], format.PageMagic[:]) &&
		page[format.HeaderFlags] == 0 &&
		format.U16(page[format.HeaderSizePos:]) == format.PageHeaderSize
}

func structureBornValid(page []byte, selectedTxn uint64) bool {
	born := format.U64(page[format.HeaderBorn:])
	return born != 0 && born <= selectedTxn
}

// structureRequiredLevel returns the smallest radix level covering the ID
// limit (Rust required_level through the shared format authority).
func structureRequiredLevel(idLimit uint64) (uint16, error) {
	if idLimit < 1 || idLimit > 1<<32 {
		return 0, corrupt("structure table ID limit is invalid")
	}
	level, ok := format.StructureRootLevel(idLimit)
	if !ok {
		return 0, corrupt("structure table exceeds its maximum height")
	}
	return uint16(level), nil
}

// structureGrowRoot lifts the root while the target level is above the
// current one (Rust grow_root).
func structureGrowRoot(codec structurePayloadCodec, store tree.RetiringStore, root *uint32, wanted uint16) error {
	if *root == 0 {
		return nil
	}
	level, err := structureParseLevel(codec, store, *root)
	if err != nil {
		return err
	}
	if level > wanted {
		return corrupt("structure table root level exceeds its limit")
	}
	for level < wanted {
		next, err := store.Allocate()
		if err != nil {
			return err
		}
		targetTxn := store.TargetTxn()
		if err := store.Update(next, func(page []byte) error {
			structureInitialize(codec, page, targetTxn, level+1, 1)
			at := format.SlottedHeaderSize // child index zero
			if at+4 > len(page) {
				return corrupt("structure table child is outside page bounds")
			}
			format.PutU32(page[at:], *root)
			return nil
		}); err != nil {
			return err
		}
		*root = next
		level++
	}
	return nil
}

// structureParseLevel parses the root level without an expected bound
// (Rust grow_root).
func structureParseLevel(codec structurePayloadCodec, store tree.Store, root uint32) (uint16, error) {
	header, err := structureParsePage(codec, store, root, nil)
	if err != nil {
		return 0, err
	}
	return header.level, nil
}

// structureNewSubtree allocates one radix subtree holding record (Rust
// new_subtree): a level-0 page initializes with item count one and writes
// the record at its id slot; a directory page creates the child subtree
// and stores it at the id-derived child index.
func structureNewSubtree(codec structurePayloadCodec, store tree.RetiringStore, level uint16, id uint32, record []byte) (uint32, error) {
	pageNumber, err := store.Allocate()
	if err != nil {
		return 0, err
	}
	targetTxn := store.TargetTxn()
	if level == 0 {
		if err := store.Update(pageNumber, func(page []byte) error {
			structureInitialize(codec, page, targetTxn, 0, 1)
			at := structureRecordOffset(id)
			if at+len(record) > len(page) {
				return corrupt("structure table record is outside its page")
			}
			copy(page[at:], record)
			return nil
		}); err != nil {
			return 0, err
		}
		return pageNumber, nil
	}
	child, err := structureNewSubtree(codec, store, level-1, id, record)
	if err != nil {
		return 0, err
	}
	index, err := structureChildIndex(id, level)
	if err != nil {
		return 0, err
	}
	if err := store.Update(pageNumber, func(page []byte) error {
		structureInitialize(codec, page, targetTxn, level, 1)
		at := format.SlottedHeaderSize + index*4
		if at+4 > len(page) {
			return corrupt("structure table child is outside page bounds")
		}
		format.PutU32(page[at:], child)
		return nil
	}); err != nil {
		return 0, err
	}
	return pageNumber, nil
}

// structureTableTouch returns the page number to mutate, copying the page
// when it predates the target transaction (Rust touch): pages born in the
// target transaction are already private, so the append-only output
// builder never copies and never retires.
func structureTableTouch(codec structurePayloadCodec, store tree.RetiringStore, pageNumber uint32, level uint16, retired *tree.RetiredPages) (uint32, structureTableHeader, error) {
	header, private, err := structureTouchHeader(codec, store, pageNumber, level)
	if err != nil {
		return 0, structureTableHeader{}, err
	}
	if private {
		return pageNumber, header, nil
	}
	copyPage, err := store.Allocate()
	if err != nil {
		return 0, structureTableHeader{}, err
	}
	if err := tree.CopyForCow(store, pageNumber, copyPage); err != nil {
		return 0, structureTableHeader{}, err
	}
	if err := retired.Push(pageNumber); err != nil {
		return 0, structureTableHeader{}, err
	}
	return copyPage, header, nil
}

// structureTouchHeader inspects one page and reports whether it is
// already private (born in the target transaction).
func structureTouchHeader(codec structurePayloadCodec, store tree.Store, pageNumber uint32, level uint16) (structureTableHeader, bool, error) {
	var header structureTableHeader
	private := false
	err := store.Inspect(pageNumber, func(page []byte) error {
		var err error
		header, err = structureTableParse(codec, page, store.TargetTxn(), &level)
		if err != nil {
			return err
		}
		private = format.U64(page[format.HeaderBorn:]) == store.TargetTxn()
		return nil
	})
	return header, private, err
}

// structureInitialize writes the radix page header (Rust initialize): the
// record or directory page type, the level geometry bounds, and the
// structure kind as aux. The header write is infallible on a mapped page
// (Rust initialize returns Result only because its sink methods can).
func structureInitialize(codec structurePayloadCodec, page []byte, txn uint64, level uint16, itemCount uint16) {
	pageType := format.PageTypeStructureIDRecord
	lower := uint16(format.StructureLeafEnd)
	if level != 0 {
		pageType = format.PageTypeStructureIDDirectory
		lower = format.StructureBranchEnd
	}
	format.InitializePageHeader(page, pageType, txn, itemCount, level, lower, format.PageSize, uint32(codec.kind()))
}

// structureInsertLeaf writes one record into its empty id slot and bumps
// the item count (Rust insert_leaf).
func structureInsertLeaf(codec structurePayloadCodec, page []byte, header structureTableHeader, record []byte) error {
	decoded, err := decodeStructureRecord(codec, record)
	if err != nil {
		return err
	}
	at := structureRecordOffset(decoded.id)
	if at+format.StructureRecordSize > len(page) {
		return corrupt("structure table record is outside its page")
	}
	if !allZero(page[at : at+format.StructureRecordSize]) {
		return corrupt("structure table ID already exists")
	}
	copy(page[at:], record)
	format.PutU16(page[format.HeaderCount:], header.itemCount+1)
	return nil
}

// structureDeleteLeaf zeroes one record slot and decrements the item
// count (Rust delete_leaf).
func structureDeleteLeaf(page []byte, header structureTableHeader, id uint32) error {
	at := structureRecordOffset(id)
	if at+format.StructureRecordSize > len(page) {
		return corrupt("structure table record is outside its page")
	}
	clear(page[at : at+format.StructureRecordSize])
	format.PutU16(page[format.HeaderCount:], header.itemCount-1)
	return nil
}

// structureRemoveEmptyPath discards the empty leaf and every directory
// level that became empty, zeroing the parent child slots (Rust
// remove_empty_path).
func structureRemoveEmptyPath(store tree.Store, root *uint32, child uint32, childCount uint16, path *structurePath) error {
	if childCount != 0 {
		return nil
	}
	if err := store.DiscardPrivate(child); err != nil {
		return err
	}
	for depth := path.depth - 1; depth >= 0; depth-- {
		frame := path.frames[depth]
		if err := store.Update(frame.pageNumber, func(page []byte) error {
			at := format.SlottedHeaderSize + frame.childIndex*4
			if at+4 > len(page) {
				return corrupt("structure table child is outside page bounds")
			}
			format.PutU32(page[at:], 0)
			format.PutU16(page[format.HeaderCount:], frame.itemCount-1)
			return nil
		}); err != nil {
			return err
		}
		child = frame.pageNumber
		childCount = frame.itemCount - 1
		if childCount != 0 {
			return nil
		}
		if err := store.DiscardPrivate(child); err != nil {
			return err
		}
	}
	*root = 0
	return nil
}

// structureReadRecordAt reads one record at its id slot of one leaf page
// (Rust change_refcount's read_record).
func structureReadRecordAt(codec structurePayloadCodec, store tree.Store, pageNumber uint32, id uint32) (structureRecord, bool, error) {
	var record structureRecord
	found := false
	err := store.Inspect(pageNumber, func(page []byte) error {
		cell, err := structureTableLeafCell(codec, page, id)
		if err != nil {
			return err
		}
		if format.U32(cell[structureIDOffset:]) == 0 {
			if !allZero(cell) {
				return corrupt("empty structure table slot is nonzero")
			}
			return nil
		}
		decoded, err := decodeStructureRecord(codec, cell)
		if err != nil {
			return err
		}
		if decoded.id != id {
			return corrupt("structure table record is in the wrong slot")
		}
		record = decoded
		found = true
		return nil
	})
	return record, found, err
}

// structureChangedRefcount applies one signed change (Rust
// changed_refcount).
func structureChangedRefcount(current uint64, change int64) (uint64, error) {
	if change >= 0 {
		next := current + uint64(change)
		if next < current {
			return 0, overflow("structure refcount")
		}
		return next, nil
	}
	amount := uint64(-change)
	if amount > current {
		return 0, overflow("structure refcount")
	}
	return current - amount, nil
}

// structureLeafIndex is the record slot of one id (Rust leaf_index).
func structureLeafIndex(id uint32) int {
	return int(id % format.StructureRecordSlots)
}

// structureRecordOffset is the byte offset of one record slot (Rust
// record_offset).
func structureRecordOffset(id uint32) int {
	return format.SlottedHeaderSize + structureLeafIndex(id)*format.StructureRecordSize
}

// structureTableLeafCell returns one fixed record slot (Rust leaf_cell).
func structureTableLeafCell(codec structurePayloadCodec, page []byte, id uint32) ([]byte, error) {
	slot := structureLeafIndex(id)
	if slot >= format.StructureRecordSlots {
		return nil, corrupt("structure table record slot is invalid")
	}
	at := format.SlottedHeaderSize + slot*format.StructureRecordSize
	if at+format.StructureRecordSize > len(page) {
		return nil, corrupt("structure table record is outside its page")
	}
	return page[at : at+format.StructureRecordSize], nil
}

// structureChildIndex is the directory child of one id (Rust
// child_index): the id within the parent level span, modulo the 512
// children.
func structureChildIndex(id uint32, level uint16) (int, error) {
	if level == 0 || level > structureMaxLevel {
		return 0, corrupt("structure table branch level is invalid")
	}
	span, ok := format.StructureSpanOfLevel(uint32(level))
	if !ok {
		return 0, overflow("structure table coverage")
	}
	return int((uint64(id) / span) % structureBranchChildren), nil
}

// structureBranchChild reads and bounds one directory child (Rust
// branch_child).
func structureBranchChild(page []byte, header structureTableHeader, index int, pageLimit uint64) (uint32, error) {
	if header.level == 0 || index < 0 || index >= structureBranchChildren {
		return 0, corrupt("structure table child index is invalid")
	}
	child, err := structureRawBranchChild(page, index)
	if err != nil {
		return 0, err
	}
	if child != 0 && (child < 2 || uint64(child) >= pageLimit) {
		return 0, corrupt("structure table child is outside page bounds")
	}
	return child, nil
}

// structureRawBranchChild reads one directory child (Rust
// raw_branch_child).
func structureRawBranchChild(page []byte, index int) (uint32, error) {
	if index < 0 || index >= structureBranchChildren {
		return 0, corrupt("structure table child index is invalid")
	}
	at := format.SlottedHeaderSize + index*4
	if at+4 > len(page) {
		return 0, corrupt("structure table child is outside page bounds")
	}
	return format.U32(page[at : at+4]), nil
}

// structureSetBranchChild stores one new child and bumps the item count
// (Rust set_branch_child).
func structureSetBranchChild(page []byte, header structureTableHeader, index int, child uint32) error {
	if header.level == 0 || index < 0 || index >= structureBranchChildren || child == 0 {
		return corrupt("structure table child insertion is invalid")
	}
	at := format.SlottedHeaderSize + index*4
	if at+4 > len(page) {
		return corrupt("structure table child is outside page bounds")
	}
	if format.U32(page[at:]) != 0 {
		return corrupt("structure table child already exists")
	}
	format.PutU32(page[at:], child)
	format.PutU16(page[format.HeaderCount:], header.itemCount+1)
	return nil
}

// structureReplaceBranchChild rewires one existing child (Rust
// replace_branch_child).
func structureReplaceBranchChild(page []byte, header structureTableHeader, index int, child uint32) error {
	if header.level == 0 || index < 0 || index >= structureBranchChildren || child == 0 {
		return corrupt("structure table child replacement is invalid")
	}
	at := format.SlottedHeaderSize + index*4
	if at+4 > len(page) {
		return corrupt("structure table child is outside page bounds")
	}
	format.PutU32(page[at:], child)
	return nil
}

// allZero reports whether b is entirely zero.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
