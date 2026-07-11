package iprangedb

import "math/bits"

// pageSet is a page bitset for tracking dirty/private pages during a transaction.
// Pre-allocated at Writer.open() time — fixed allocation in the hot path.
//
// Size: total_pages / 8 bytes. For 1M pages = 128KB. For 100M pages = 12.5MB.
// Resized (amortized) when the file grows beyond the initial allocation.
type pageSet struct {
	bits     []uint64
	capacity int // max pages addressable
}

func newPageSet(capacity int) *pageSet {
	words := (capacity + 63) / 64
	return &pageSet{bits: make([]uint64, words), capacity: capacity}
}

// ensureCapacity grows the bitset to cover at least minCapacity pages.
func (s *pageSet) ensureCapacity(minCapacity int) {
	if minCapacity <= s.capacity {
		return
	}
	newCap := minCapacity
	if s.capacity*2 > newCap {
		newCap = s.capacity * 2
	}
	newWords := (newCap + 63) / 64
	if newWords > len(s.bits) {
		s.bits = append(s.bits, make([]uint64, newWords-len(s.bits))...)
	}
	s.capacity = newCap
}

func (s *pageSet) contains(pgno uint32) bool {
	p := int(pgno)
	if p >= s.capacity {
		return false
	}
	return s.bits[p/64]&(1<<uint(p%64)) != 0
}

func (s *pageSet) insert(pgno uint32) {
	p := int(pgno)
	if p >= s.capacity {
		s.ensureCapacity(p + 1)
	}
	s.bits[p/64] |= 1 << uint(p%64)
}

func (s *pageSet) remove(pgno uint32) {
	p := int(pgno)
	if p >= s.capacity {
		return
	}
	s.bits[p/64] &^= 1 << uint(p%64)
}

func (s *pageSet) clear() {
	for i := range s.bits {
		s.bits[i] = 0
	}
}

// iter returns all set page numbers in ascending order.
func (s *pageSet) iter() []uint32 {
	var result []uint32
	for wordIdx, word := range s.bits {
		for bit := 0; bit < 64; bit++ {
			if word&(1<<uint(bit)) != 0 {
				result = append(result, uint32(wordIdx*64+bit))
			}
		}
	}
	return result
}

func (s *pageSet) count() int {
	n := 0
	for _, w := range s.bits {
		n += bits.OnesCount64(w)
	}
	return n
}

func (s *pageSet) isEmpty() bool {
	for _, w := range s.bits {
		if w != 0 {
			return false
		}
	}
	return true
}
