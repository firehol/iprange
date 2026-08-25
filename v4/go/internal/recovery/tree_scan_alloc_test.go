//go:build !race

// Tree-scan layout-proof allocation pin (milestone-2 performance
// fix): the generic recovery tree walk must prove the page layout
// exactly once per visited page (Rust read_page proves; scan_leaf and
// scan_branch consume). The measured floor is one LayoutInspection per
// page, which format.InspectLayout must heap-allocate because it
// cannot be inlined; before the fix every visited page ran a second
// proof in the leaf/branch arm, so a multi-page tree measured twice
// the page count. Race and checkptr instrumentation allocate inside
// the measured path themselves, so the pin runs only in
// uninstrumented builds (publication pins carry the same tag for the
// same reason).

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

// TestTreeScanOneLayoutProofPerPage pins the tree walk to exactly one
// heap object per visited page: the LayoutInspection allocated by the
// single format.InspectLayout in readTreePage. A fresh (reset) page
// set is created per run so every measured run re-walks the whole
// tree.
func TestTreeScanOneLayoutProofPerPage(t *testing.T) {
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
	// The fixture walk measures 2138 objects: a fixed 2057-object
	// leaf/branch walk baseline plus exactly one LayoutInspection per
	// visited page (want). Before the fix the leaf/branch arms
	// re-proved the page and the fixture measured 2219 = 2138 + want.
	const walkBaseline = 2057
	if allocs != float64(walkBaseline+want) {
		t.Fatalf("tree scan allocates %.0f objects per run over %d pages, want exactly %d (measured walk baseline plus one layout proof per page)", allocs, want, walkBaseline+want)
	}
}
