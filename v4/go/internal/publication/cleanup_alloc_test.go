//go:build linux && !race

// The discard success-path allocation pin. Race and checkptr
// instrumentation allocate inside the measured call path themselves
// (the 2 syscall-boundary copies measure 8 under -race +
// -gcflags=all=-d=checkptr=2), so the pin runs only in uninstrumented
// builds; the project battery runs checkptr only together with -race,
// which this tag excludes.

package publication

import "testing"

func TestDiscardWithZeroAllocations(t *testing.T) {
	dir := t.TempDir()
	d := testBoundDestination(t, dir)
	prepared := cleanupTestPrepared(t, dir, "result.v4")
	defer prepared.Close()
	seed := captureSeed(prepared)
	// The output is pre-unlinked once, so every measured run takes
	// the zero-link removal arm: no namespace mutation, one sync, no
	// ledger push. The machine itself must stay on the stack (Rust
	// Copy semantics; review F1). The exactly-two allowance is the
	// syscall-boundary string conversion of the two require-absent
	// name probes: Go's runtime copies every name to a NUL-terminated
	// buffer per syscall (x/sys ByteSliceFromString allocates), while
	// Rust amortizes it in the Name CString at name construction.
	// Machine logic adds nothing on top of that boundary cost.
	if _, err := d.directory().UnlinkExact(prepared.attempt.nameOf(), prepared.attempt.identityOf()); err != nil {
		t.Fatalf("pre-unlink output: %v", err)
	}
	allocations := testing.AllocsPerRun(20, func() {
		summary := discardWith(&seed, prepared, nil, func(cleanupPoint) error { return nil })
		if !summary.artifacts.Empty() {
			t.Fatalf("ledger %+v, want empty", summary.artifacts.Slice())
		}
	})
	if allocations != 2 {
		t.Fatalf("discard success path allocates %v objects per run, want the 2 syscall-boundary name copies", allocations)
	}
}
