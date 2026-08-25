//go:build !race

// Tree-scan layout-proof allocation pin (milestone-2 performance fix
// plus the M5 value-return refactor): the generic recovery tree walk
// proves the page layout exactly once per visited page (Rust read_page
// proves; scan_leaf and scan_branch consume), and InspectLayout
// returns the inspection by value, so the walk allocates nothing per
// page: the measured floor is the fixed leaf/branch walk baseline.
// Before the milestone-2 fix every visited page ran a second proof in
// the leaf/branch arm (twice the page count), and before the M5
// refactor the value-returning proof still escaped one
// LayoutInspection per page. Race and checkptr instrumentation
// allocate inside the measured path themselves, so the pin runs only
// in uninstrumented builds (publication pins carry the same tag for
// the same reason).

package recovery

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// treePinEvents counts the accepted pages of one tree scan without
// allocating (the measured run must not pay for its sink).
type treePinEvents struct {
	pages int
}

func (e *treePinEvents) pageAccepted() error {
	e.pages++
	return nil
}

func (e *treePinEvents) pageRejected(ioUnreadable bool) error {
	return nil
}

func (e *treePinEvents) unknown(reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error {
	return nil
}

func (e *treePinEvents) leaf(page uint32, index int, cell []byte, ok bool) error {
	return nil
}

// treePinSource builds one membership source whose ID tree spans many
// pages: 2000 two-word bitmaps intern as 2000 distinct ID-tree records
// (the ID tree keys by the bitmap signature) and overflow the leaf
// page capacity. The active-feed proof requires every set bit to name
// a pushed feed (feed index word*64+bit), so bitmap i sets bit
// 3+(i/61) in word 0 (feeds 3..35) and bit 3+(i%61) in word 1 (feeds
// 67..127), over that pushed feed set.
func treePinSource(t *testing.T) (string, format.Meta) {
	t.Helper()
	const ids = 2000
	path := filepath.Join(t.TempDir(), "tree-pin.iprdb")
	builder, err := writer.NewOutputBuilder(path, membershipSourceSpec(8_000_000), writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	for index := uint32(0); index < 33; index++ { // word 0 bits 3..35
		feed := 3 + index
		if err := builder.PushFeed(fmt.Sprintf("feed-%d", feed), feed); err != nil {
			t.Fatalf("PushFeed(%d): %v", feed, err)
		}
	}
	for index := uint32(0); index < 61; index++ { // word 1 bits 3..63
		feed := 64 + 3 + index
		if err := builder.PushFeed(fmt.Sprintf("feed-%d", feed), feed); err != nil {
			t.Fatalf("PushFeed(%d): %v", feed, err)
		}
	}
	for i := uint32(0); i < ids; i++ {
		words := writer.OutputWords{1 << (3 + i/61), 1 << (3 + i%61)}
		base := i * 100
		if err := builder.PushMembershipV4(base, base+9, words); err != nil {
			t.Fatalf("PushMembershipV4(%d): %v", i, err)
		}
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path, meta
}

// TestTreeScanNoLayoutProofAllocation pins the tree walk to the fixed
// leaf/branch walk baseline with nothing allocated per visited page:
// the single InspectLayout in readTreePage returns the inspection by
// value. A fresh (reset) page set is created per run so every
// measured run re-walks the whole tree.
func TestTreeScanNoLayoutProofAllocation(t *testing.T) {
	creationGate(t)
	path, meta := treePinSource(t)
	source := mapSource(t, path)
	defer source.Close()
	budget := recoveryBudget(1 << 22)
	pages, err := forRecovery(budget.MaxHeapBytes/2, meta.PageCount, meta, budget)
	if err != nil {
		t.Fatalf("page set: %v", err)
	}
	// Warm run: drain the page set once, count the visited pages, and
	// reset to an empty fresh set for the measured runs.
	probe := &treePinEvents{}
	if err := pages.reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := scanTree(membershipIDCodec{}, source, meta, meta.MembershipIDRoot, pages, nil, probe); err != nil {
		t.Fatalf("scan: %v", err)
	}
	want := probe.pages
	if want < 4 {
		t.Fatalf("fixture visits only %d pages, want a multi-page tree", want)
	}
	allocs := testing.AllocsPerRun(50, func() {
		if err := pages.reset(); err != nil {
			t.Fatal(err)
		}
		if err := scanTree(membershipIDCodec{}, source, meta, meta.MembershipIDRoot, pages, nil, probe); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("tree scan allocations per run over %d pages: %.0f", want, allocs)
	// The fixture walk measures exactly the fixed 2057-object
	// leaf/branch walk baseline: the value-returning layout proof
	// adds nothing per page. Before the M5 refactor the measured
	// floor was 2057 + want (one escaping LayoutInspection per page);
	// before the milestone-2 fix it was 2057 + 2*want.
	const walkBaseline = 2057
	if allocs != float64(walkBaseline) {
		t.Fatalf("tree scan allocates %.0f objects per run over %d pages, want exactly %d (measured walk baseline, no per-page layout allocation)", allocs, want, walkBaseline)
	}
}
