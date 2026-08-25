package validation

// Platform gate for the live validation tests (SOW-0025 4-12D): the
// LiveCurrent and live-fixture tests create and open live database
// pairs, which needs live creation (the creator-only security machine
// and the proven live coordination). On platforms where
// CreationSupported refuses, those tests skip with the same reason
// the live package-wide gate uses; immutable validation of prebuilt
// files stays ungated and keeps running everywhere.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// liveGate skips one test whose fixture creates or opens a live
// database pair.
func liveGate(t *testing.T) {
	t.Helper()
	if err := live.CreationSupported(); err != nil {
		t.Skipf("live database creation is not supported on this platform: %v", err)
	}
}
