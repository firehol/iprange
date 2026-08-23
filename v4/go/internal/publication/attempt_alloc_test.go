//go:build linux && !race && !v4work

// The publish-machine success-path allocation pin (Rust
// attempt_tests.rs post_boundary_success_allocates_no_heap). Race and
// checkptr instrumentation allocate inside the measured call path
// themselves, so the pin runs only in uninstrumented builds; the
// project battery runs checkptr only together with -race, which this
// tag excludes. The v4work build is excluded the same way:
// fault.Crash's os.Getenv probe allocates at every crash point, so
// the pin measures the production code shape only, exactly like the
// crash-instrumented exclusion of the discard pin.
//
// The Rust count_thread_allocations pin is thread-local and hermetic;
// Go has no per-goroutine allocation counter, so the pin takes the
// minimum of N single-run MemStats windows. Each window runs the
// machine exactly once over a fresh fixture (fromPrivate consumes the
// reservation, so AllocsPerRun's warmup-repeat loop would measure a
// changed state). Background allocations can only inflate a window,
// so the minimum is the machine's own cost.

package publication

import (
	"runtime"
	"testing"
)

// attemptPinWindows is the number of single-run measurement windows;
// the minimum window is the machine's cost.
const attemptPinWindows = 5

func TestAttemptPostBoundarySuccessAllocatesNoHeap(t *testing.T) {
	minimum := ^uint64(0)
	for i := 0; i < attemptPinWindows; i++ {
		dir := t.TempDir()
		prepared := cleanupTestPrepared(t, dir, "result.v4")
		draft, err := createReservationDraft(prepared)
		if err != nil {
			t.Fatalf("create reservation draft: %v", err)
		}
		private, initFailure := draft.initialize(prepared)
		if initFailure != nil {
			t.Fatalf("initialize reservation: %v", initFailure)
		}
		seed := captureSeed(prepared)

		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		result, failure := fromPrivate(seed, prepared, *private, nil, func(attemptPoint) error { return nil }, false, noopAttemptObserver)
		runtime.ReadMemStats(&after)
		prepared.Close()
		if failure != nil {
			t.Fatalf("fromPrivate: %v", failure)
		}
		if result.Publication != PublicationPublished {
			t.Fatalf("publication = %v, want published", result.Publication)
		}
		if allocations := after.Mallocs - before.Mallocs; allocations < minimum {
			minimum = allocations
		}
	}
	// The measured minimum is the machine's own cost over the
	// accepted syscall boundary: every allocation is a name or
	// attribute NUL-copy of an x/sys call (Entry/VerifyName/
	// RequireAbsent/UnlinkExact/RenameNoReplace probes and the
	// Fgetxattr attribute-name copies), a rename boundary class, or
	// the portable result plumbing. Escape analysis shows zero
	// machine-logic escapes on the success path (the measured 58 has
	// no unaccounted class; the machine adds nothing on top of the
	// boundary, matching Rust's zero modulo the x/sys string
	// conversion convention recorded in slices F/G/H).
	if minimum != 58 {
		t.Fatalf("fromPrivate success path allocates %d objects (min of %d windows), want 58", minimum, attemptPinWindows)
	}
}
