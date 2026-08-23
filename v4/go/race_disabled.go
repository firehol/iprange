//go:build !race

package iprangedb

// raceEnabled reports whether the race detector is compiling this test
// binary. The detector's shadow memory inflates MemStats allocation
// windows, so zero-allocation pins are only enforceable against the
// production allocator.
const raceEnabled = false
