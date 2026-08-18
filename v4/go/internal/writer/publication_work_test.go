//go:build v4work

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// reclamationWorkBaseline snapshots the reclamation work counters before a
// reclamation publish (plain builds return an unusable baseline; the
// assertion is a no-op there, see publication_test.go).
func reclamationWorkBaseline() uint64 {
	return work.Read().PagesReclaimed
}

// checkReclaimedPages asserts the reclamation publish returned exactly the
// freed pages to the allocator machinery (Rust direct_workflow_tests.rs
// reclamation_counts_each_released_page_once: pages_reclaimed == page_count).
func checkReclaimedPages(t *testing.T, baseline, wantMax uint64) {
	t.Helper()
	reclaimed := work.Read().PagesReclaimed - baseline
	if reclaimed == 0 || reclaimed > wantMax {
		t.Fatalf("reclaimed %d pages after reclamation, want 1..%d", reclaimed, wantMax)
	}
}
