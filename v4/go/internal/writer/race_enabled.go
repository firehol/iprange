//go:build race

package writer

// raceEnabled reports whether the race detector is compiling this test
// binary. The detector's shadow memory inflates HeapAlloc, so the deflate
// workspace charge is only enforceable against the production allocator.
const raceEnabled = true
