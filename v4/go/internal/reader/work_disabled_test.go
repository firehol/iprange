//go:build !v4work

package reader

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestWorkCountersDisabled asserts the production build compiles the
// counters out: Enabled is const false, so the counter calls are
// inlineable no-ops and the counter state cannot exist.
func TestWorkCountersDisabled(t *testing.T) {
	if work.Enabled {
		t.Fatal("work counters enabled in a production build")
	}
}
