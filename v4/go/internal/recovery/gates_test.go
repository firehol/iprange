package recovery

// Platform gates for the recovery test suite (SOW-0025 4-12D): the
// suite splits into tests that create or open live database pairs,
// tests whose recovery terminal creates a private output (the
// creator-only publication security machine), and tests that refuse
// before any of that. Each gate names the platform capability the
// test needs so the skip reason is honest. On linux every gate is a
// no-op.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// liveGate skips one test that builds or inspects live database pairs
// (the fixture needs live creation, which requires the creator-only
// security machine and the proven live coordination).
func liveGate(t *testing.T) {
	t.Helper()
	if err := live.CreationSupported(); err != nil {
		t.Skipf("live database creation is not supported on this platform: %v", err)
	}
}

// publicationGate skips one test whose recovery terminal creates a
// private output before the source is inspected (the publication
// attempt applies the creator-only security policy; recovery creates
// its private output before any source inspection).
func publicationGate(t *testing.T) {
	t.Helper()
	if !security.CreatorOnlySupported() {
		t.Skip("creator-only publication security is not available on this platform (pure-Go xattr machine is linux only)")
	}
}
