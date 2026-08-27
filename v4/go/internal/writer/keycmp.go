// Ordered-key comparators shared by the Codec.CompareKey
// implementations (mirroring the tree probe helpers and
// reader/search.go). Each codec decodes only the key fields of its own
// geometry and compares them in the exact Rust key order.

package writer

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
