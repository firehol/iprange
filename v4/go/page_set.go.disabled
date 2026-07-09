package iprangedb

// pageSet is a bitset for tracking page numbers. It replaces map[uint32]struct{} for
// the Writer's privatePages set — a direct array access is 3-5× faster than Go's map
// hashing + probing for small integer keys (profile showed mapaccess2_fast32 at 5% of
// append time). Pages are allocated sequentially, so the bitset stays compact.
type pageSet struct {
	bits []uint64
}

func newPageSet() *pageSet {
	return &pageSet{bits: make([]uint64, 0, 16)}
}

func (s *pageSet) mark(pgno uint32) {
	word := int(pgno >> 6)
	for word >= len(s.bits) {
		s.bits = append(s.bits, 0)
	}
	s.bits[word] |= 1 << (pgno & 63)
}

func (s *pageSet) contains(pgno uint32) bool {
	word := int(pgno >> 6)
	if word >= len(s.bits) {
		return false
	}
	return s.bits[word]&(1<<(pgno&63)) != 0
}

func (s *pageSet) clear() {
	for i := range s.bits {
		s.bits[i] = 0
	}
}
