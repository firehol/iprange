// Package bitmap implements the canonical hierarchical-bitmap page layout
// and its COW used/free mutation, mirroring the Rust bitmap_page and
// free_bitmap modules. The free bitmap is the allocation core's free-page
// authority; the used bitmap supports the dictionary ID allocators that
// arrive with the edit workflows.
package bitmap

import (
	"bytes"
	"math/bits"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// Layout constants (binary-format-v4.md section 9, Rust bitmap_page.rs).
const (
	LeafWords         = format.BitmapLeafWords // 500
	LeafBits          = uint64(LeafWords * 64) // 32000
	BranchChildren    = format.BitmapFanout    // 256
	leafWordsOffset   = format.SlottedHeaderSize
	branchSummarySize = 32
	BranchChildrenOff = format.SlottedHeaderSize + branchSummarySize
	LeafEnd           = leafWordsOffset + LeafWords*8
	BranchEnd         = BranchChildrenOff + BranchChildren*4
	MaxLevel          = 3
	summaries         = 4 // 4 summary words of 64 bits = 256 bits
)

// Kind discriminates the four bitmap namespaces (Rust Kind).
type Kind uint32

const (
	KindFree       Kind = 1
	KindFeed       Kind = 2
	KindMembership Kind = 3
	KindStructure  Kind = 4
)

// FirstCandidate is the lowest allocatable bit of the kind.
func (k Kind) FirstCandidate() uint64 {
	if k == KindMembership || k == KindStructure {
		return 1
	}
	return 0
}

// Header is the validated geometry of one bitmap page.
type Header struct {
	Level     uint16
	ItemCount int
}

// PageLower is the fixed lower bound of one bitmap page level (Rust
// page_lower).
func PageLower(level uint16) int {
	if level == 0 {
		return LeafEnd
	}
	return BranchEnd
}

// HeaderProblemNone reports a healthy bitmap header; the other classes
// mirror Rust bitmap_page::HeaderProblem and select the validation
// reason.
type HeaderProblem uint8

const (
	HeaderProblemNone HeaderProblem = iota
	HeaderProblemHeader
	HeaderProblemBorn
	HeaderProblemLevel
	HeaderProblemType
)

// CheckedHeader inspects one bitmap page and classifies its defect (Rust
// bitmap_page::checked_header): Header, Born, Level, or Type select the
// validation reason classes, and the geometry is returned only for a
// healthy header.
func CheckedHeader(page []byte, selectedTxn uint64, kind Kind, expectedLevel *uint16) (Header, HeaderProblem) {
	level := format.U16(page[format.HeaderLevel:])
	lower := PageLower(level)
	// Rust page_header::common_valid runs on every bitmap parse (private
	// and committed): magic, zero flags, and the fixed 32-byte header
	// size. A torn write at offset 0 with the rest of the header intact
	// must be rejected here, before any COW copy re-seals the page with
	// a valid checksum. The magic compare is bounded (4 bytes) and never
	// moves mapped bytes into owned memory.
	if len(page) != format.PageSize ||
		!bytes.Equal(page[format.HeaderMagic:format.HeaderMagic+4], format.PageMagic[:]) ||
		page[format.HeaderFlags] != 0 ||
		int(format.U16(page[format.HeaderSizePos:])) != format.SlottedHeaderSize ||
		int(format.U16(page[format.HeaderLower:])) != lower ||
		int(format.U16(page[format.HeaderUpper:])) != format.PageSize {
		return Header{}, HeaderProblemHeader
	}
	born := format.U64(page[format.HeaderBorn:])
	if born == 0 || born > selectedTxn {
		return Header{}, HeaderProblemBorn
	}
	if level > MaxLevel || (expectedLevel != nil && *expectedLevel != level) {
		return Header{}, HeaderProblemLevel
	}
	expectedType := format.PageTypeBitmapLeaf
	if level != 0 {
		expectedType = format.PageTypeBitmapBranch
	}
	if page[format.HeaderType] != byte(expectedType) || format.U32(page[format.HeaderAux:]) != uint32(kind) {
		return Header{}, HeaderProblemType
	}
	count := int(format.U16(page[format.HeaderCount:]))
	maximum := LeafWords
	if level != 0 {
		maximum = BranchChildren
	}
	if count == 0 || count > maximum {
		return Header{}, HeaderProblemHeader
	}
	return Header{Level: level, ItemCount: count}, HeaderProblemNone
}

// InspectHeader validates one bitmap page and returns its geometry (Rust
// bitmap_page::inspect_header); the classified defect becomes the
// Corrupt class for the healthy-read callers.
func InspectHeader(page []byte, selectedTxn uint64, kind Kind, expectedLevel *uint16) (Header, error) {
	header, problem := CheckedHeader(page, selectedTxn, kind, expectedLevel)
	if problem != HeaderProblemNone {
		return Header{}, corrupt(headerProblemDetail(problem))
	}
	return header, nil
}

// headerProblemDetail is the Corrupt message of one header problem
// class (the pre-classification messages are unchanged).
func headerProblemDetail(problem HeaderProblem) string {
	switch problem {
	case HeaderProblemBorn:
		return "bitmap page transaction is invalid"
	case HeaderProblemLevel:
		return "bitmap page level is invalid"
	case HeaderProblemType:
		return "bitmap page type or discriminator is invalid"
	default:
		return "bitmap page header is invalid"
	}
}

// ReservedZero reports whether the reserved tail is all zeroes (Rust
// reserved_zero).
func ReservedZero(page []byte, level uint16) bool {
	lower := PageLower(level)
	for at := lower; at < format.PageSize; at++ {
		if page[at] != 0 {
			return false
		}
	}
	return true
}

// Initialize stamps one fresh bitmap page (Rust bitmap_page::initialize).
func Initialize(page []byte, txn uint64, level uint16, kind Kind) {
	format.InitializePageHeader(page, pageTypeForLevel(level), txn, 0, level, uint16(PageLower(level)), format.PageSize, uint32(kind))
}

func pageTypeForLevel(level uint16) format.PageType {
	if level == 0 {
		return format.PageTypeBitmapLeaf
	}
	return format.PageTypeBitmapBranch
}

// LeafWord reads one 8-byte bitmap word (Rust leaf_word).
func LeafWord(page []byte, index int) (uint64, error) {
	if index >= LeafWords {
		return 0, corrupt("bitmap word index is invalid")
	}
	work.BitmapProbe(1)
	return format.U64(page[leafWordsOffset+index*8:]), nil
}

// SetLeafWord writes one 8-byte bitmap word.
func SetLeafWord(page []byte, index int, word uint64) error {
	if index >= LeafWords {
		return corrupt("bitmap word index is invalid")
	}
	format.PutU64(page[leafWordsOffset+index*8:], word)
	work.BytesMoved(8) // Rust bitmap_page: page.put_u64
	return nil
}

// BranchChild reads one child page number (Rust branch_child).
func BranchChild(page []byte, index int) (uint32, error) {
	if index >= BranchChildren {
		return 0, corrupt("bitmap child index is invalid")
	}
	work.BitmapProbe(1)
	return format.U32(page[BranchChildrenOff+index*4:]), nil
}

// SetBranchChild writes one child page number.
func SetBranchChild(page []byte, index int, child uint32) error {
	if index >= BranchChildren {
		return corrupt("bitmap child index is invalid")
	}
	format.PutU32(page[BranchChildrenOff+index*4:], child)
	work.BytesMoved(4) // Rust bitmap_page: page.put_u32
	return nil
}

// CheckedBranchChild reads and bounds-checks one child (Rust
// checked_branch_child).
func CheckedBranchChild(page []byte, header Header, index int, pageLimit uint64) (uint32, error) {
	if header.Level == 0 || index >= BranchChildren {
		return 0, corrupt("bitmap child lookup is invalid")
	}
	child, err := BranchChild(page, index)
	if err != nil {
		return 0, err
	}
	if child != 0 && (child < 2 || uint64(child) >= pageLimit) {
		return 0, corrupt("bitmap child is outside page bounds")
	}
	return child, nil
}

// ReplaceBranchChild writes one child plus its summary bit and returns the
// new child count (Rust replace_branch_child).
func ReplaceBranchChild(page []byte, header Header, index int, child uint32, summary bool) (int, error) {
	if header.Level == 0 || index >= BranchChildren {
		return 0, corrupt("bitmap child index is invalid")
	}
	old, err := BranchChild(page, index)
	if err != nil {
		return 0, err
	}
	if err := SetBranchChild(page, index, child); err != nil {
		return 0, err
	}
	if err := SetSummary(page, index, summary); err != nil {
		return 0, err
	}
	count := header.ItemCount
	if old == 0 && child != 0 {
		count++
	}
	if old != 0 && child == 0 {
		count--
	}
	if count < 0 {
		return 0, corrupt("bitmap child count underflows")
	}
	format.PutU16(page[format.HeaderCount:], uint16(count))
	work.BytesMoved(2) // Rust set_count: page.put_u16
	return count, nil
}

// SummaryBit reads one branch summary bit (Rust summary_bit).
func SummaryBit(page []byte, index int) (bool, error) {
	if index >= BranchChildren {
		return false, corrupt("bitmap summary index is invalid")
	}
	work.BitmapProbe(1)
	at := format.SlottedHeaderSize + (index/64)*8
	return format.U64(page[at:])&(1<<(index%64)) != 0, nil
}

// SetSummary writes one branch summary bit (Rust set_summary).
func SetSummary(page []byte, index int, value bool) error {
	if index >= BranchChildren {
		return corrupt("bitmap summary index is invalid")
	}
	at := format.SlottedHeaderSize + (index/64)*8
	mask := uint64(1 << (index % 64))
	word := format.U64(page[at:])
	if value {
		word |= mask
	} else {
		word &^= mask
	}
	format.PutU64(page[at:], word)
	work.BytesMoved(8) // Rust summary bit set: page.put_u64
	return nil
}

// FirstSummary returns the first set summary bit at or after start and
// whether one exists (Rust first_summary's Option).
func FirstSummary(page []byte, start int) (int, bool, error) {
	if start >= BranchChildren {
		return 0, false, nil
	}
	wordIndex := start / 64
	word := format.U64(page[format.SlottedHeaderSize+wordIndex*8:]) & (^uint64(0) << (start % 64))
	for {
		if word != 0 {
			return wordIndex*64 + trailingZeros(word), true, nil
		}
		wordIndex++
		if wordIndex == summaries {
			return 0, false, nil
		}
		word = format.U64(page[format.SlottedHeaderSize+wordIndex*8:])
	}
}

func trailingZeros(word uint64) int { return bits.TrailingZeros64(word) }

// FirstLeafWord returns the first nonzero leaf word (Rust first_leaf_word).
func FirstLeafWord(page []byte) (int, uint64, error) {
	for index := 0; index < LeafWords; index++ {
		value, err := LeafWord(page, index)
		if err != nil {
			return 0, 0, err
		}
		if value != 0 {
			return index, value, nil
		}
	}
	return 0, 0, nil
}

// NonzeroChildren counts the nonzero branch children and verifies that
// every summary bit agrees with its child (Rust free_bitmap
// nonzero_children).
func NonzeroChildren(page []byte) (int, error) {
	count := 0
	for index := 0; index < BranchChildren; index++ {
		child, err := BranchChild(page, index)
		if err != nil {
			return 0, err
		}
		summary, err := SummaryBit(page, index)
		if err != nil {
			return 0, err
		}
		if summary != (child != 0) {
			return 0, corrupt("free bitmap summary disagrees with child")
		}
		if child != 0 {
			count++
		}
	}
	return count, nil
}

// NonzeroLeafWords counts the nonzero leaf words (Rust nonzero_leaf_words).
func NonzeroLeafWords(page []byte) (int, error) {
	count := 0
	for index := 0; index < LeafWords; index++ {
		value, err := LeafWord(page, index)
		if err != nil {
			return 0, err
		}
		if value != 0 {
			count++
		}
	}
	return count, nil
}

// Coverage returns the bit span of one level (Rust coverage).
func Coverage(level uint16) (uint64, error) {
	value := LeafBits
	for at := 0; at < int(level); at++ {
		value *= BranchChildren
	}
	return value, nil
}

// RequiredLevel returns the smallest level covering limit bits (Rust
// required_level).
func RequiredLevel(limit uint64) (uint16, error) {
	if limit == 0 || limit > 1<<32 {
		return 0, invalid("bitmap limit is invalid")
	}
	for level := uint16(0); level <= MaxLevel; level++ {
		coverage, err := Coverage(level)
		if err != nil {
			return 0, err
		}
		if coverage >= limit {
			return level, nil
		}
	}
	return 0, corrupt("bitmap limit")
}

// LeafWordIndex returns the word holding bit (Rust leaf_word_index).
func LeafWordIndex(bit uint32) int {
	return int((uint64(bit) % LeafBits) / 64)
}

// ChildIndex returns the branch child slot of bit at level (Rust
// child_index).
func ChildIndex(bit uint32, level uint16) (int, error) {
	coverage, err := Coverage(level - 1)
	if err != nil {
		return 0, err
	}
	return int((uint64(bit) / coverage) % BranchChildren), nil
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
