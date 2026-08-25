//go:build v4work

package recovery

// Session-active push path allocation pin (milestone-4 P2): with a
// worker session hook installed, every probe arm and release on the
// output push path must stay closure-free and allocation-free, so the
// session-active path holds the same bounds as the library path
// (TestRecoveryOutputPushPathAllocPin). The installed hook is a no-op
// arm: the arm itself must not allocate and returns an inert
// (unarmed) release, so the measured path covers the session
// EnterProbe value shape, the hook call, and the inert release on
// every Store op. The real control arm and its release are pinned
// separately in the worker package (TestArmProbeAndReleaseAllocsZero),
// and the mapping package pins EnterProbe on both paths. The v4work
// tag keeps the session-mode machinery beside the crash fixtures and
// away from the plain library pins.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// TestSessionActiveRecoveryOutputPushPathAllocPin mirrors the
// no-session push pin with one session hook installed: the bounds are
// the same 8/16 per 4-record pass, proving the armed path adds zero
// allocations (Rust Probe is a stack value).
func TestSessionActiveRecoveryOutputPushPathAllocPin(t *testing.T) {
	creationGate(t)
	t.Cleanup(mapping.ClearSessionProbe)
	mapping.SetSessionProbe(func(mapping.ProbeRole, uintptr, uint64) (mapping.ProbeRelease, error) {
		return mapping.ProbeRelease{}, nil
	})

	membershipAllocs := measureMembershipPush(t)
	structuredAllocs := measureStructuredPush(t)
	t.Logf("session-active membership push path: %v allocs per 4-record pass (bound %d)", membershipAllocs, membershipPushAllocBound)
	t.Logf("session-active structured push path: %v allocs per 4-record pass (bound %d)", structuredAllocs, structuredPushAllocBound)
	if membershipAllocs > membershipPushAllocBound {
		t.Fatalf("session-active membership push path allocs %v per pass, want <= %d", membershipAllocs, membershipPushAllocBound)
	}
	if structuredAllocs > structuredPushAllocBound {
		t.Fatalf("session-active structured push path allocs %v per pass, want <= %d", structuredAllocs, structuredPushAllocBound)
	}
}
