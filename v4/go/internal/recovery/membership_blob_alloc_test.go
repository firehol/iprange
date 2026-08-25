//go:build !race

// Blob branch layout-proof allocation pin (milestone-2 performance
// fix plus the M5 value-return refactor): the membership blob walk
// proves each branch page layout exactly once (Rust branch(): parse
// and inspect_layout once, then branch_records_valid and
// branch_children consume the proved cells), and InspectLayout
// returns the inspection by value, so the walk allocates nothing per
// branch page: the measured floor is the fixed walk baseline. Before
// the milestone-2 fix every branch page ran three proofs (branch,
// branchRecordsValid, branchChildren); before the M5 refactor the
// value-returning proof still escaped one LayoutInspection per branch
// page. The pin runs only in uninstrumented builds: race and
// checkptr instrumentation allocate inside the measured path itself
// (publication pins carry the same tag for the same reason).

package recovery

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// blobPinConsume is the allocation-free byte sink of the measured blob
// scans.
func blobPinConsume(bytes []byte) error { return nil }

// TestMembershipBlobBranchNoLayoutProofAllocation pins the blob
// branch walk to the fixed walk baseline with nothing allocated per
// branch page: the single format.InspectLayout in branch() returns
// the inspection by value. The fixture is the three-level blob of the
// multi-level membership test (114356 words: 226 leaves, two level-1
// branches and one root branch = three branch pages; the 226 leaf
// pages allocate nothing). A fresh (reset) page set is created per
// run so every measured run re-walks the whole blob.
func TestMembershipBlobBranchNoLayoutProofAllocation(t *testing.T) {
	creationGate(t)
	const (
		wordCount = 114_356 // 226 leaves: 225 fill one branch level
		feedLimit = 8_000_000
	)
	words := make(writer.OutputWords, wordCount)
	words[0] = 1 << 3
	words[55_999] = 1 << 63
	words[wordCount-1] = 1 << 63
	dir := t.TempDir()
	path := filepath.Join(dir, "blob-pin.iprdb")
	meta := membershipSourceLimit(t, path, feedLimit, [][2]any{
		{"alpha", uint32(3)},
		{"middle", uint32(3_583_999)},
		{"omega", uint32(7_318_783)},
	}, []membershipRange{{from: 0, to: 9, words: words}})
	source := mapSource(t, path)
	defer source.Close()
	pages, tables := prepareMembershipRecovery(t, source, meta)
	rep := newReporter(nil)
	catalogRecovered, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	recovered, err := recoverMemberships(source, meta, catalogRecovered, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverMemberships: %v", err)
	}
	entry, found, err := recovered.get(tables, 1)
	if err != nil || !found {
		t.Fatalf("membership id 1 found=%v err=%v", found, err)
	}
	if entry.storage != format.MembershipStorageBlob {
		t.Fatalf("membership id 1 storage %v, want blob", entry.storage)
	}
	const wantBranchPages = 3
	complete := true
	allocs := testing.AllocsPerRun(50, func() {
		if err := pages.reset(); err != nil {
			t.Fatal(err)
		}
		var err error
		complete, err = scanMembershipBlob(source, meta, entry.blobRoot, entry.wordCount, pages, nil, rep, blobPinConsume)
		if err != nil {
			t.Fatal(err)
		}
	})
	if !complete {
		t.Fatal("blob scan incomplete")
	}
	t.Logf("blob scan allocations per run over %d branch pages: %.0f", wantBranchPages, allocs)
	// The fixture walk measures exactly the fixed 924-object leaf and
	// branch walk baseline (three per leaf page, one per branch
	// record, plus the branch walk state): the value-returning layout
	// proof adds nothing per branch page. Before the M5 refactor the
	// measured floor was 924 + wantBranchPages; before the
	// milestone-2 fix it was 924 + 3 per branch page.
	const walkBaseline = 924
	if allocs != walkBaseline {
		t.Fatalf("blob scan allocates %.0f objects per run, want exactly %d (measured walk baseline, no per-branch-page layout allocation)", allocs, walkBaseline)
	}
}
