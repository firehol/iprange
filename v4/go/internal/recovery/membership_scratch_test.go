package recovery

// Authorized-scratch membership recovery vector ported from the Rust
// recovery/membership_adversarial_tests.rs
// disordered_membership_ranges_use_the_bounded_shared_external_sort:
// the disordered membership range tree is sorted through the shared
// scratch sort, the output preserves all 120 ranges, and the cleanup
// proves the scratch directory empty.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/writer"
)

// TestDisorderedMembershipRangesUseTheBoundedSharedExternalSort
// mirrors the Rust vector: two feeds, 120 alternating-word
// membership ranges, the first two range records swapped, and a
// tiny heap plus 64 KiB scratch forcing the shared external sort.
func TestDisorderedMembershipRangesUseTheBoundedSharedExternalSort(t *testing.T) {
	creationGate(t)
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	outputPath := filepath.Join(dir, "output.iprdb")
	scratchDirectory := filepath.Join(dir, "scratch")
	if err := os.Mkdir(scratchDirectory, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	feeds := [][2]any{{"a", uint32(1)}, {"b", uint32(5)}}
	ranges := make([]membershipRange, 0, 120)
	for index := uint32(0); index < 120; index++ {
		words := writer.OutputWords{1 << 5}
		if index%2 == 0 {
			words = writer.OutputWords{1 << 1}
		}
		ranges = append(ranges, membershipRange{from: index * 3, to: index*3 + 1, words: words})
	}
	meta := membershipSource(t, sourcePath, feeds, ranges)
	swapFirstTwoRecords(t, sourcePath, meta)
	source := mapSource(t, sourcePath)
	defer source.Close()
	budget := &RecoveryBudget{
		MaxHeapBytes:     128,
		MaxOutputPages:   100_000,
		MaxOpenFiles:     4,
		MaxScratchBytes:  64 * 1024,
		MaxScratchFiles:  2,
		ScratchDirectory: scratchDirectory,
	}
	construction, failure := constructMembership(t, source, meta, outputPath, budget, nil)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	if construction.scratch == nil {
		t.Fatal("external sort recorded no scratch attempt")
	}
	if !construction.scratch.clean() {
		t.Fatal("scratch cleanup reports residues")
	}
	if construction.report.Ranges.Accepted != 120 {
		t.Fatalf("accepted ranges %d, want 120", construction.report.Ranges.Accepted)
	}
	entries, err := os.ReadDir(scratchDirectory)
	if err != nil {
		t.Fatalf("ReadDir scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory left %d entries, want 0", len(entries))
	}
	r := reopenMember(t, outputPath)
	defer r.Close()
	if reopened := r.Meta(); reopened.RangeRecordCount != 120 {
		t.Fatalf("range record count %d, want 120", reopened.RangeRecordCount)
	}
	validateClean(t, outputPath)
}
