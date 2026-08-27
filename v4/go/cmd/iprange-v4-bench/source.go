// Input generators (Rust benches/update_ipsets/source.rs): the exact
// permutation, direct, address, and feed-shape sources plus the
// splitmix64-shuffled random point list the scenarios consume.
package main

import (
	"math/bits"

	"github.com/firehol/iprange/v4/go"
)

const (
	batchCapacity = 1024
	dispersedSeed = 0x9e3779b97f4a7c15
)

type permutation struct {
	count  int
	step   int
	offset int
}

func newPermutation(count int, seed uint64) permutation {
	if count <= 1 {
		return permutation{count: count, step: 1, offset: 0}
	}
	// Rust uses usize arithmetic on the u64 seed (identity cast), so the
	// modulo runs in uint64 before the conversion to int; a direct
	// int(seed) would go negative for seeds >= 2^63.
	step := int(seed % uint64(count))
	if step == 0 {
		step = 1
	}
	for gcd(step, count) != 1 {
		step++
		if step == count {
			step = 1
		}
	}
	return permutation{
		count:  count,
		step:   step,
		offset: int(rotl64(seed, 17) % uint64(count)),
	}
}

func (p permutation) at(ordinal int) int {
	if p.count <= 1 {
		return 0
	}
	return int((uint64(ordinal)*uint64(p.step) + uint64(p.offset)) % uint64(p.count))
}

func gcd(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	return left
}

func rotl64(value uint64, shift uint) uint64 {
	return value<<shift | value>>(64-shift)
}

func requireAddressSpace(count int, phase uint32) error {
	if count <= 0 {
		return errBenchTooLarge
	}
	if uint64(count)+uint64(phase) > uint64(0xffffffff) {
		return errBenchTooLarge
	}
	return nil
}

var errBenchTooLarge = &benchTooLargeError{}

type benchTooLargeError struct{}

func (*benchTooLargeError) Error() string {
	return "benchmark size exceeds the IPv4 workload space"
}

// directSource feeds direct replacement workflows: dispersed singleton
// ranges (unordered) or the shrinking nested pattern.
type directSource struct {
	count  int
	next   int
	nested bool
	perm   permutation
}

func newDirectSource(count int) (*directSource, error) {
	if err := requireAddressSpace(count, 0); err != nil {
		return nil, err
	}
	return &directSource{count: count, perm: newPermutation(count, dispersedSeed)}, nil
}

func newNestedSource(count int) (*directSource, error) {
	if err := requireAddressSpace(count, 0); err != nil {
		return nil, err
	}
	return &directSource{count: count, nested: true}, nil
}

func (s *directSource) nextBatch() ([]iprangedb.DirectRangeV4, bool) {
	if s.next == s.count {
		return nil, false
	}
	length := min(s.count-s.next, batchCapacity)
	batch := make([]iprangedb.DirectRangeV4, length)
	for offset := 0; offset < length; offset++ {
		ordinal := s.next + offset
		if s.nested {
			index := ordinal
			end := s.count*4 + 1
			batch[offset] = iprangedb.DirectRangeV4{
				From:  uint32(index),
				To:    uint32(end - index),
				Value: uint32(index%2 + 1),
			}
			continue
		}
		index := s.perm.at(ordinal)
		start := uint32(index) * 4
		batch[offset] = iprangedb.DirectRangeV4{
			From:  start,
			To:    start + 1,
			Value: uint32(index%251 + 1),
		}
	}
	s.next += length
	return batch, true
}

// directSourceV6 mirrors DirectSourceV6: dispersed order over the low
// 32 bits scaled by four, values cycling through 1..251 (ranges are
// carried as inclusive [from,to] uint64 pairs).
type directSourceV6 struct {
	count int
	next  int
	perm  permutation
}

func newDirectSourceV6(count int) (*directSourceV6, error) {
	if err := requireAddressSpace(count, 0); err != nil {
		return nil, err
	}
	return &directSourceV6{count: count, perm: newPermutation(count, dispersedSeed)}, nil
}

// directRangeV6 is one inclusive IPv6 input range with its value
// (DirectRangeV6 shape; the Rust harness emits low-<2^32 ranges).
type directRangeV6 struct {
	fromHi, fromLo, toHi, toLo uint64
	value                      uint32
}

func (s *directSourceV6) nextBatch() ([]directRangeV6, bool) {
	if s.next == s.count {
		return nil, false
	}
	length := min(s.count-s.next, batchCapacity)
	batch := make([]directRangeV6, length)
	for offset := 0; offset < length; offset++ {
		index := s.perm.at(s.next + offset)
		start := uint64(index) * 4
		batch[offset] = directRangeV6{
			fromLo: start,
			toLo:   start + 1,
			value:  uint32(index%251 + 1),
		}
	}
	s.next += length
	return batch, true
}

// addressSource feeds first/last-seen refresh and feed replacement:
// singletons at (index+phase)*4 in dispersed order.
type addressSource struct {
	count int
	next  int
	phase uint32
	perm  permutation
}

func newAddressSource(count int, phase uint32) (*addressSource, error) {
	if err := requireAddressSpace(count, phase); err != nil {
		return nil, err
	}
	return &addressSource{
		count: count,
		phase: phase,
		perm:  newPermutation(count, uint64(phase)+29),
	}, nil
}

func (s *addressSource) nextBatch() ([]iprangedb.AddressRange4, bool) {
	if s.next == s.count {
		return nil, false
	}
	length := min(s.count-s.next, batchCapacity)
	batch := make([]iprangedb.AddressRange4, length)
	for offset := 0; offset < length; offset++ {
		index := s.perm.at(s.next + offset)
		start := uint32(index) + s.phase
		start *= 4
		batch[offset] = iprangedb.AddressRange4{From: iprangedb.IPv4(start), To: iprangedb.IPv4(start + 1)}
	}
	s.next += length
	return batch, true
}

type feedShape uint8

const (
	feedAscendingDisjoint feedShape = iota
	feedDescendingDisjoint
	feedRandomDisjoint
	feedRandomOverlapChain
)

func (s feedShape) expectedIntervals(count int) uint64 {
	if s == feedRandomOverlapChain {
		return 1
	}
	return uint64(count)
}

type feedShapeSource struct {
	count int
	next  int
	shape feedShape
	perm  permutation
}

func newFeedShapeSource(count int, shape feedShape) (*feedShapeSource, error) {
	if err := requireAddressSpace(count, 0); err != nil {
		return nil, err
	}
	return &feedShapeSource{
		count: count,
		shape: shape,
		perm:  newPermutation(count, dispersedSeed),
	}, nil
}

func (s *feedShapeSource) nextBatch() ([]iprangedb.AddressRange4, bool) {
	if s.next == s.count {
		return nil, false
	}
	length := min(s.count-s.next, batchCapacity)
	batch := make([]iprangedb.AddressRange4, length)
	for offset := 0; offset < length; offset++ {
		ordinal := s.next + offset
		index := ordinal
		switch s.shape {
		case feedDescendingDisjoint:
			index = s.count - ordinal - 1
		case feedRandomDisjoint, feedRandomOverlapChain:
			index = s.perm.at(ordinal)
		}
		from := uint32(index) * 4
		to := from + 1
		if s.shape == feedRandomOverlapChain {
			to = from + 7
		}
		batch[offset] = iprangedb.AddressRange4{From: iprangedb.IPv4(from), To: iprangedb.IPv4(to)}
	}
	s.next += length
	return batch, true
}

func splitmix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	value := *state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

// randomPoints builds the dispersed IPv4 point list the random lookup
// scenarios sweep (Rust scenarios::random_points).
func randomPoints(size int) ([]uint32, error) {
	points := make([]uint32, size)
	for index := range points {
		points[index] = uint32(index) * 4
	}
	state := uint64(0x6a09e667f3bcc909)
	for upper := len(points) - 1; upper >= 1; upper-- {
		random := splitmix64(&state)
		// The high 64 bits of the 128-bit product (Rust u128 >> 64).
		high, _ := bits.Mul64(random, uint64(upper+1))
		index := int(high)
		points[upper], points[index] = points[index], points[upper]
	}
	return points, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
