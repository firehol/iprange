//go:build !v4work

package writer

import "testing"

// Plain-build stubs: the reclaimed-page assertion needs the v4work
// counters; the functional reclamation test still runs everywhere.
func reclamationWorkBaseline() uint64 { return 0 }

func checkReclaimedPages(*testing.T, uint64, uint64) {}
