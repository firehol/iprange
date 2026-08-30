// Fixed-memory membership blob construction and word reads (Rust
// membership_dictionary/blob.rs + blob_tree.rs): a bottom-up 16-byte
// branch tree over 4048-byte payload leaves (aux = membership kind 1).
// Builds in up to five levels with only-child collapse; releases walk the
// same geometry in postorder.

package writer

import (
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

const (
	membershipBlobHeaderCount = 16
	membershipBlobLeafData    = 48
	membershipBlobBranchSize  = 16
	membershipBlobAux         = 1
	membershipBlobLeafWords   = (format.PageSize - membershipBlobLeafData) / 8
	membershipBlobBranchItems = (format.PageSize - 32) / (membershipBlobBranchSize + 2)
	membershipBlobBuildLevels = 5
)

const (
	membershipBlobBranchOffsetOffset = 0
	membershipBlobBranchChildOffset  = 8
	membershipBlobBranchReserved     = 12
	membershipBlobLeafStartOffset    = 32
	membershipBlobLeafLengthOffset   = 40
)

type membershipBlobNode struct {
	offset uint64
	page   uint32
	level  uint16
}

type membershipBlobLevel struct {
	nodes [membershipBlobBranchItems]membershipBlobNode
	len   int
}

// buildMembershipBlob writes the bitmap as one blob tree and returns its
// root (Rust blob::build). The source word count is nonzero.
func buildMembershipBlob[W membershipWords](store tree.Store, words W) (uint32, error) {
	if words.WordCount() == 0 {
		return 0, invalid("empty membership has no blob representation")
	}
	var levels [membershipBlobBuildLevels]membershipBlobLevel
	var offsetWords uint32
	for offsetWords < words.WordCount() {
		count := words.WordCount() - offsetWords
		if count > membershipBlobLeafWords {
			count = membershipBlobLeafWords
		}
		node, err := writeMembershipBlobLeaf(store, words, offsetWords, count)
		if err != nil {
			return 0, err
		}
		if err := pushMembershipBlobNode(store, levels[:], 0, node); err != nil {
			return 0, err
		}
		offsetWords += count
	}
	return finishMembershipBlob(store, levels[:])
}

// writeMembershipBlobLeaf writes one payload leaf (Rust write_leaf): the
// fixed geometry header plus the words in 64-word chunks.
func writeMembershipBlobLeaf[W membershipWords](store tree.Store, words W, offsetWords, count uint32) (membershipBlobNode, error) {
	pageNumber, err := store.Allocate()
	if err != nil {
		return membershipBlobNode{}, err
	}
	txn := store.TargetTxn()
	dataLen := int(count) * 8
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return membershipBlobNode{}, err
	}
	initializeMembershipBlobLeaf(page, txn, uint64(offsetWords)*8, dataLen)
	if err := store.FinishEdit(page, tag); err != nil {
		return membershipBlobNode{}, err
	}
	var written uint32
	for written < count {
		values, got, err := words.ReadChunk(offsetWords + written)
		if err != nil {
			return membershipBlobNode{}, err
		}
		n := count - written
		if n > membershipChunkWords {
			n = membershipChunkWords
		}
		if got < n {
			return membershipBlobNode{}, corrupt("membership words are outside the source bounds")
		}
		page, tag, err := store.Update(pageNumber)
		if err != nil {
			return membershipBlobNode{}, err
		}
		for index, value := range values[:n] {
			at := membershipBlobLeafData + (int(written)+index)*8
			binary.LittleEndian.PutUint64(page[at:], value)
		}
		if err := store.FinishEdit(page, tag); err != nil {
			return membershipBlobNode{}, err
		}
		written += n
	}
	return membershipBlobNode{offset: uint64(offsetWords) * 8, page: pageNumber, level: 0}, nil
}

// initializeMembershipBlobLeaf writes one blob leaf header (Rust
// blob_tree::initialize_leaf: page_header::initialize first, then the
// start offset and payload length at their fixed offsets).
func initializeMembershipBlobLeaf(page []byte, bornTxn uint64, start uint64, dataLen int) {
	format.InitializePageHeader(page, format.PageTypeBlobLeaf, bornTxn, 1, 0,
		uint16(membershipBlobLeafData+dataLen), format.PageSize, membershipBlobAux)
	format.PutU64(page[membershipBlobLeafStartOffset:], start)
	format.PutU16(page[membershipBlobLeafLengthOffset:], uint16(dataLen))
	work.BytesMoved(10) // Rust blob_tree initialize_leaf: start + length puts
}

// pushMembershipBlobNode inserts one node into the bottom-up levels,
// flushing full levels upward (Rust blob::push).
func pushMembershipBlobNode(store tree.Store, levels []membershipBlobLevel, level int, node membershipBlobNode) error {
	if level >= membershipBlobBuildLevels {
		return corrupt("membership blob exceeds its height bound")
	}
	if levels[level].len == membershipBlobBranchItems {
		parent, err := flushMembershipBlobLevel(store, &levels[level])
		if err != nil {
			return err
		}
		if err := pushMembershipBlobNode(store, levels, level+1, parent); err != nil {
			return err
		}
	}
	slot := levels[level].len
	levels[level].nodes[slot] = node
	levels[level].len++
	return nil
}

// finishMembershipBlob collapses the builder to one root (Rust finish).
func finishMembershipBlob(store tree.Store, levels []membershipBlobLevel) (uint32, error) {
	for {
		count := 0
		only := membershipBlobNode{}
		lowest := -1
		for index := range levels {
			if levels[index].len != 0 && lowest == -1 {
				lowest = index
			}
			count += levels[index].len
			if levels[index].len == 1 {
				only = levels[index].nodes[0]
			}
		}
		if count == 1 {
			if only.page == 0 {
				return 0, corrupt("membership blob builder is empty")
			}
			return only.page, nil
		}
		if lowest == -1 {
			return 0, corrupt("membership blob builder is empty")
		}
		parent, err := flushMembershipBlobLevel(store, &levels[lowest])
		if err != nil {
			return 0, err
		}
		if err := pushMembershipBlobNode(store, levels, lowest+1, parent); err != nil {
			return 0, err
		}
	}
}

// flushMembershipBlobLevel writes one branch page over the level's nodes
// (Rust blob::flush).
func flushMembershipBlobLevel(store tree.Store, level *membershipBlobLevel) (membershipBlobNode, error) {
	if level.len == 0 {
		return membershipBlobNode{}, corrupt("membership blob level is empty")
	}
	childLevel := level.nodes[0].level
	for _, node := range level.nodes[:level.len] {
		if node.level != childLevel {
			return membershipBlobNode{}, corrupt("membership blob level mixes child heights")
		}
	}
	pageNumber, err := store.Allocate()
	if err != nil {
		return membershipBlobNode{}, err
	}
	txn := store.TargetTxn()
	page, tag, err := store.Update(pageNumber)
	if err != nil {
		return membershipBlobNode{}, err
	}
	b := format.NewSlottedBuilder(page, format.PageTypeBlobBranch, txn, childLevel+1, membershipBlobAux)
	for _, node := range level.nodes[:level.len] {
		var record [membershipBlobBranchSize]byte
		binary.LittleEndian.PutUint64(record[membershipBlobBranchOffsetOffset:], node.offset)
		binary.LittleEndian.PutUint32(record[membershipBlobBranchChildOffset:], node.page)
		binary.LittleEndian.PutUint32(record[membershipBlobBranchReserved:], 0)
		if err := b.Push(page, record[:]); err != nil {
			return membershipBlobNode{}, err
		}
	}
	if err := b.Finish(page); err != nil {
		return membershipBlobNode{}, err
	}
	if err := store.FinishEdit(page, tag); err != nil {
		return membershipBlobNode{}, err
	}
	level.len = 0 // Rust flush ends with *level = EMPTY_LEVEL
	return membershipBlobNode{offset: level.nodes[0].offset, page: pageNumber, level: childLevel + 1}, nil
}

// releaseMembershipBlob retires every page of one blob tree (Rust
// blob::release): the postorder walk verifies the geometry and the
// declared byte coverage.
func releaseMembershipBlob(store tree.RetiringStore, root uint32, wordCount uint32) error {
	total := uint64(wordCount) * 8
	var next uint64
	if err := releaseMembershipBlobPage(store, root, nil, total, &next, 0); err != nil {
		return err
	}
	if next != total {
		return corrupt("membership blob does not cover its declared length")
	}
	return nil
}

func releaseMembershipBlobPage(store tree.RetiringStore, pageNumber uint32, expectedLevel *uint16, total uint64, next *uint64, depth uint16) error {
	if depth > format.MaxTreeLevel {
		return corrupt("membership blob exceeds its maximum height")
	}
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return err
	}
	level := format.U16(page[format.HeaderLevel:])
	born := format.U64(page[format.HeaderBorn:])
	if level == 0 {
		err = releaseMembershipBlobLeaf(store, pageNumber, expectedLevel, total, next)
	} else {
		err = releaseMembershipBlobBranch(store, pageNumber, level, expectedLevel, total, next, depth)
	}
	if err != nil {
		return err
	}
	if born == store.TargetTxn() {
		return store.DiscardPrivate(pageNumber)
	}
	return store.RetirePages(tree.RetireOne(pageNumber))
}

func releaseMembershipBlobBranch(store tree.RetiringStore, pageNumber uint32, level uint16, expectedLevel *uint16, total uint64, next *uint64, depth uint16) error {
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return err
	}
	h, err := format.DecodePageHeader(page, store.TargetTxn())
	if err != nil {
		return err
	}
	if h.PageType != format.PageTypeBlobBranch || h.Aux != membershipBlobAux ||
		(expectedLevel != nil && *expectedLevel != h.Level) {
		return corrupt("membership blob branch page is invalid")
	}
	header := &h
	for index := 0; index < int(header.ItemCount); index++ {
		var child uint32
		page, err := store.Inspect(pageNumber)
		if err != nil {
			return err
		}
		cell, err := format.SlottedCell(page, header, index, membershipBlobBranchSize)
		if err != nil {
			return err
		}
		if len(cell) != membershipBlobBranchSize ||
			binary.LittleEndian.Uint32(cell[membershipBlobBranchReserved:]) != 0 {
			return corrupt("membership blob branch record is malformed")
		}
		offset := binary.LittleEndian.Uint64(cell[membershipBlobBranchOffsetOffset:])
		if offset != *next {
			return corrupt("membership blob branch record is malformed")
		}
		child = binary.LittleEndian.Uint32(cell[membershipBlobBranchChildOffset:])
		if child < 2 || uint64(child) >= store.PageLimit() {
			return corrupt("membership blob branch record is malformed")
		}
		nextLevel := level - 1
		if err := releaseMembershipBlobPage(store, child, &nextLevel, total, next, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func releaseMembershipBlobLeaf(store tree.RetiringStore, pageNumber uint32, expectedLevel *uint16, total uint64, next *uint64) error {
	page, err := store.Inspect(pageNumber)
	if err != nil {
		return err
	}
	h, err := format.DecodePageHeader(page, store.TargetTxn())
	if err != nil {
		return err
	}
	if h.PageType != format.PageTypeBlobLeaf || h.Aux != membershipBlobAux ||
		(expectedLevel != nil && *expectedLevel != h.Level) {
		return corrupt("membership blob leaf page is invalid")
	}
	start := binary.LittleEndian.Uint64(page[membershipBlobLeafStartOffset:])
	length := int(binary.LittleEndian.Uint16(page[membershipBlobLeafLengthOffset:]))
	if start != *next || length <= 0 || length > format.PageSize-membershipBlobLeafData ||
		length%8 != 0 {
		return corrupt("membership blob leaf layout is malformed")
	}
	end := start + uint64(length)
	if end > total || (end < total && length != format.PageSize-membershipBlobLeafData) {
		return corrupt("membership blob leaf layout is malformed")
	}
	if int(h.Lower) != membershipBlobLeafData+length || int(h.Upper) != format.PageSize {
		return corrupt("membership blob leaf layout is malformed")
	}
	*next += uint64(length)
	return nil
}

// readMembershipBlobWords reads sequential words from one blob tree
// (Rust blob_tree::read_words_from).
func readMembershipBlobWords(store tree.Store, root uint32, totalWords, start uint32, output []uint64) error {
	totalBytes := uint64(totalWords) * 8
	offset := uint64(start) * 8
	written := 0
	for written < len(output) {
		leaf, err := findMembershipBlobLeaf(store, root, totalBytes, offset)
		if err != nil {
			return err
		}
		local := int(offset - leaf.offset)
		available := (leaf.dataLen - local) / 8
		count := available
		if count > len(output)-written {
			count = len(output) - written
		}
		if count == 0 {
			return corrupt("membership blob cannot advance by a complete word")
		}
		page, err := store.Inspect(leaf.pageNumber)
		if err != nil {
			return err
		}
		for index := 0; index < count; index++ {
			at := membershipBlobLeafData + local + index*8
			output[written+index] = binary.LittleEndian.Uint64(page[at:])
		}
		written += count
		offset += uint64(count) * 8
	}
	return nil
}

type membershipBlobFoundLeaf struct {
	pageNumber uint32
	offset     uint64
	dataLen    int
}

// findMembershipBlobLeaf descends to the leaf holding the byte offset
// (Rust blob_tree::find_leaf): branch selection by offset, leaf geometry
// verified against the expected start and the total length.
func findMembershipBlobLeaf(store tree.Store, root uint32, totalBytes, target uint64) (membershipBlobFoundLeaf, error) {
	work.TreeLookup(1)
	if target >= totalBytes {
		return membershipBlobFoundLeaf{}, corrupt("membership blob request exceeds its length")
	}
	pageNumber := root
	var expected uint16
	expectedSet := false
	expectedOffset := uint64(0)
	for depth := 0; depth <= int(format.MaxTreeLevel); depth++ {
		var child uint32
		var childOffset uint64
		var branchLevel uint16
		done := false
		var leafStart uint64
		var leafLen int
		page, err := store.Inspect(pageNumber)
		if err != nil {
			return membershipBlobFoundLeaf{}, err
		}
		level := format.U16(page[format.HeaderLevel:])
		if level == 0 {
			start := binary.LittleEndian.Uint64(page[membershipBlobLeafStartOffset:])
			length := int(binary.LittleEndian.Uint16(page[membershipBlobLeafLengthOffset:]))
			if start != expectedOffset || length <= 0 ||
				length > format.PageSize-membershipBlobLeafData || length%8 != 0 {
				return membershipBlobFoundLeaf{}, corrupt("membership blob leaf layout is malformed")
			}
			end := start + uint64(length)
			if end > totalBytes || target >= end {
				return membershipBlobFoundLeaf{}, corrupt("membership blob leaf layout is malformed")
			}
			if expectedSet && expected != 0 {
				return membershipBlobFoundLeaf{}, corrupt("membership blob leaf level is invalid")
			}
			done = true
			leafStart = start
			leafLen = length
		} else {
			h, err := format.DecodePageHeader(page, store.TargetTxn())
			if err != nil {
				return membershipBlobFoundLeaf{}, err
			}
			if h.PageType != format.PageTypeBlobBranch || h.Aux != membershipBlobAux ||
				(expectedSet && expected != h.Level) {
				return membershipBlobFoundLeaf{}, corrupt("membership blob branch page is invalid")
			}
			first, err := format.SlottedCell(page, &h, 0, membershipBlobBranchSize)
			if err != nil {
				return membershipBlobFoundLeaf{}, corrupt("membership blob branch record is malformed")
			}
			if binary.LittleEndian.Uint64(first[membershipBlobBranchOffsetOffset:]) != expectedOffset {
				return membershipBlobFoundLeaf{}, corrupt("membership blob branch starts at a wrong offset")
			}
			best := -1
			for index := 0; index < int(h.ItemCount); index++ {
				cell, err := format.SlottedCell(page, &h, index, membershipBlobBranchSize)
				if err != nil {
					return membershipBlobFoundLeaf{}, corrupt("membership blob branch record is malformed")
				}
				if binary.LittleEndian.Uint32(cell[membershipBlobBranchReserved:]) != 0 {
					return membershipBlobFoundLeaf{}, corrupt("membership blob branch record is malformed")
				}
				offset := binary.LittleEndian.Uint64(cell[membershipBlobBranchOffsetOffset:])
				if offset <= target {
					best = index
				} else {
					break
				}
			}
			if best < 0 {
				return membershipBlobFoundLeaf{}, corrupt("membership blob branch record is malformed")
			}
			cell, err := format.SlottedCell(page, &h, best, membershipBlobBranchSize)
			if err != nil {
				return membershipBlobFoundLeaf{}, corrupt("membership blob branch record is malformed")
			}
			child = binary.LittleEndian.Uint32(cell[membershipBlobBranchChildOffset:])
			if child < 2 || uint64(child) >= store.PageLimit() {
				return membershipBlobFoundLeaf{}, corrupt("membership blob branch record is malformed")
			}
			childOffset = binary.LittleEndian.Uint64(cell[membershipBlobBranchOffsetOffset:])
			branchLevel = h.Level
		}
		if done {
			return membershipBlobFoundLeaf{pageNumber: pageNumber, offset: leafStart, dataLen: leafLen}, nil
		}
		pageNumber = child
		expectedOffset = childOffset
		expected = branchLevel - 1
		expectedSet = true
		work.TreeDescent(1)
	}
	return membershipBlobFoundLeaf{}, corrupt("membership blob tree exceeds its maximum height")
}
