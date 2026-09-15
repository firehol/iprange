package calleropen

import (
	"sync"
	"sync/atomic"
)

// Witness of the caller-side opens, so a committed test can prove that a
// production caller routed its path through this package.
//
// The refusal-class pins call the caller-open owners directly, which cannot
// tell whether a caller still uses them: replacing one call site with a
// plain os.Open keeps every one of those pins green while reintroducing the
// hazard the owner exists to close (the caller trusts a path check that may
// already be stale, and a FIFO standing behind it blocks the request thread
// inside open(2)). This witness is the caller half of that pin, mirroring
// the Rust judge_witness of v4/rust/iprange-cli/src/io/caller_open.rs: a
// test arms it, drives the real handler against a plain regular fixture,
// and fails when the path it handed to that handler was never opened here.
//
// Production cost: the pointer is nil, so an instrumented open pays one
// atomic load and one nil test - the same seam shape as the worker
// candidate hook in internal/worker/client.go. Nothing is recorded until a
// test arms the witness, and the counters live only in the armed value.

// armed holds the witness a test installed, or nil in production.
var armed atomic.Pointer[Witness]

// OpenCounts is what the witness recorded for one path.
type OpenCounts struct {
	// OpenCalls counts the calls of Open that named the path.
	OpenCalls int
	// WrapCalls counts the calls of Blocking that named it. They are
	// kept apart because Blocking wraps a descriptor its caller opened
	// elsewhere, which does not show that the caller used the prompt
	// open of that path.
	WrapCalls int
	// LastFlags carries the flags of the most recent witnessed Open of
	// the path. It is meaningful only when OpenCalls is non-zero: a
	// read-only open legitimately has no flag bits at all.
	LastFlags int
}

// Total is the number of witnessed caller-side opens of one path.
func (c OpenCounts) Total() int { return c.OpenCalls + c.WrapCalls }

// Witness records the caller-side opens performed while it was armed.
type Witness struct {
	mu     sync.Mutex
	byPath map[string]*OpenCounts
	// maxPaths bounds the recorded keys. A test arms the witness for one
	// handler call, so reaching the bound means the seam was left armed
	// across unrelated work; Dropped reports it instead of growing
	// without limit.
	maxPaths int
	dropped  int
}

// WatchOpensForTest arms the witness and returns it with the function that
// disarms it. The witness keeps reporting what it saw up to the moment it
// was disarmed, so a test reads its verdict after the handler returned.
//
// It panics when a witness is already armed: one process records one
// witness, and two overlapping pins would silently judge each other's
// opens. Test binaries run their sequential tests one at a time, so an
// overlap is a test defect, not a race to absorb.
func WatchOpensForTest() (*Witness, func()) {
	witness := &Witness{byPath: map[string]*OpenCounts{}, maxPaths: 4096}
	if previous := armed.Swap(witness); previous != nil {
		armed.Store(previous)
		panic("calleropen: a caller-open witness is already armed")
	}
	return witness, func() {
		if current := armed.Load(); current == witness {
			armed.Store(nil)
		}
	}
}

// Counts reports the witnessed opens of one path.
func (w *Witness) Counts(path string) OpenCounts {
	w.mu.Lock()
	defer w.mu.Unlock()
	if recorded, ok := w.byPath[path]; ok {
		return *recorded
	}
	return OpenCounts{}
}

// Dropped reports how many distinct paths fell outside the recorded bound.
func (w *Witness) Dropped() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dropped
}

// note records one caller-side open. It is called from Open and Blocking
// only, so it is reached on every platform arm of this package.
func (w *Witness) note(op, path string, flags int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	recorded, ok := w.byPath[path]
	if !ok {
		if len(w.byPath) >= w.maxPaths {
			w.dropped++
			return
		}
		recorded = &OpenCounts{}
		w.byPath[path] = recorded
	}
	switch op {
	case openOpCall:
		recorded.OpenCalls++
		recorded.LastFlags = flags
	case openOpWrap:
		recorded.WrapCalls++
	}
}

const (
	openOpCall = "open"
	openOpWrap = "blocking"
)

// noteOpen hands one caller-side open to the armed witness, if any.
func noteOpen(op, path string, flags int) {
	if witness := armed.Load(); witness != nil {
		witness.note(op, path, flags)
	}
}
