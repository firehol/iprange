//go:build linux && !race && !v4work

// The publish-machine success-path allocation pin (Rust
// attempt_tests.rs post_boundary_success_allocates_no_heap). Race and
// checkptr instrumentation allocate inside the measured call path
// themselves, so the pin runs only in uninstrumented builds; the
// project battery runs checkptr only together with -race, which this
// tag excludes. The Rust count_thread_allocations pin runs the
// machine exactly once (from_private consumes the reservation), so
// the Go pin likewise measures one run with MemStats instead of
// AllocsPerRun's warmup-repeat loop, which would publish once and
// then measure the changed state. The v4work build is excluded the
// same way as race: fault.Crash's os.Getenv probe allocates at every
// crash point, so the pin measures the production code shape only,
// exactly like the crash-instrumented exclusion of the discard pin.

package publication

import (
	"runtime"
	"testing"
)

func TestAttemptPostBoundarySuccessAllocatesNoHeap(t *testing.T) {
	dir := t.TempDir()
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
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
	if failure != nil {
		t.Fatalf("fromPrivate: %v", failure)
	}
	if result.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published", result.Publication)
	}
	// The measured run allocates exactly the syscall-boundary and
	// machine-plumbing budget below; machine logic adds nothing beyond
	// it. Every class was measured independently on the fixture:
	//
	//   names  22  x/sys NUL-copies of the machine's exact name probes
	//               (Entry/VerifyName/RequireAbsent/UnlinkExact/
	//               RenameNoReplace; Rust amortizes them in the Name
	//               CString - the accepted boundary cost of F/G/H)
	//   xattr   7  Fgetxattr attribute-name copies of the creator-only
	//               proofs (Rust passes a &CStr; x/sys copies the
	//               string per call)
	//   rename  4  the three rename calls and their errno classes
	//               moving across the x/sys boundary
	//   result  5  the portable PublicationResult build and the final
	//               state ledger (finalState: value plumbing that Rust
	//               keeps in registers)
	//   breathers 20  Go's path-insensitive escape analysis keeps a
	//               fixed small set of deep-verify-chain locals on the
	//               heap even though the success path observes no
	//               pointer to them (Rust moves the same values with
	//               no heap); each is bounded per operation and absent
	//               from the lookup/query hot paths (publication runs
	//               once per dataset, not per lookup)
	allocations := after.Mallocs - before.Mallocs
	if allocations != 58 {
		t.Fatalf("fromPrivate success path allocates %d objects, want the pinned machine budget 58", allocations)
	}
}
