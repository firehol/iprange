package writer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// feedErrorCode extracts the internal error code of one machine
// refusal.
func feedErrorCode(t *testing.T, err error) format.ErrorCode {
	t.Helper()
	var public *format.Error
	if !errors.As(err, &public) {
		t.Fatalf("not a format.Error: %v", err)
	}
	return public.Code
}

// testFeedSpec builds the fresh membership output spec of the
// immutable feed machine.
func testFeedSpec(t *testing.T, family uint8) OutputSpec {
	t.Helper()
	spec, err := FreshOutputSpec(family, format.ValueKindMembership, format.StructureKindNone, [16]byte{'t'}, 1)
	if err != nil {
		t.Fatalf("FreshOutputSpec: %v", err)
	}
	return spec
}

func TestImmutableFeedBudgetValidation(t *testing.T) {
	tooFewOutputPages := ImmutableFeedBudget{MaxHeapBytes: 1, MaxOutputPages: 1, MaxWorkspacePages: 1, MaxOpenFiles: 3}
	if _, err := PrepareImmutableFeedBudget(tooFewOutputPages); err == nil {
		t.Fatalf("one output page: want refusal")
	} else if code := feedErrorCode(t, err); code != format.CodeInsufficientResourceBudget {
		t.Errorf("one output page code = %v, want budget-exceeded", code)
	}
	tooFewFiles := ImmutableFeedBudget{MaxHeapBytes: 1, MaxOutputPages: 2, MaxWorkspacePages: 1, MaxOpenFiles: 2}
	if _, err := PrepareImmutableFeedBudget(tooFewFiles); err == nil {
		t.Fatalf("two open files: want refusal")
	} else if code := feedErrorCode(t, err); code != format.CodeInsufficientResourceBudget {
		t.Errorf("two open files code = %v, want budget-exceeded", code)
	}
	tooManyPages := ImmutableFeedBudget{MaxHeapBytes: 1, MaxOutputPages: 2, MaxWorkspacePages: 1 << 32, MaxOpenFiles: 3}
	if _, err := PrepareImmutableFeedBudget(tooManyPages); err == nil {
		t.Fatalf("oversized page space: want refusal")
	} else if code := feedErrorCode(t, err); code != format.CodePageSpaceExhausted {
		t.Errorf("oversized page space code = %v, want page-space-exhausted", code)
	}
}

// TestImmutableWorkspaceFreeList exercises the workspace store
// surfaces the tree core depends on: allocation bounds, the free-list
// cycle, and the discard ownership proof.
func TestImmutableWorkspaceFreeList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace.bin")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const limit = 8
	const pageSize = format.PageSize
	if err := file.Truncate(int64(limit * pageSize)); err != nil {
		t.Fatal(err)
	}
	m, err := mapping.MapFile(file, limit*pageSize, true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	w, err := newImmutableWorkspace(m, 2, limit, 1)
	if err != nil {
		t.Fatalf("newImmutableWorkspace: %v", err)
	}
	if w.pageCount() != 2 {
		t.Errorf("page count = %d, want 2", w.pageCount())
	}
	if _, err := w.Inspect(1); err == nil {
		t.Errorf("inspect below first: want refusal")
	}
	pages := make([]uint32, 0, 6)
	for i := uint64(2); i < limit; i++ {
		p, err := w.Allocate()
		if err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
		pages = append(pages, p)
	}
	// The workspace is now full: allocation hits the budget bound.
	if _, err := w.Allocate(); err == nil {
		t.Fatalf("allocate beyond the limit: want refusal")
	} else if code := feedErrorCode(t, err); code != format.CodeInsufficientResourceBudget {
		t.Errorf("limit code = %v, want budget-exceeded", code)
	}
	// The tree core stamps the born header when it creates pages; the
	// micro-test stamps it by hand so the discard ownership proof can
	// run without a tree.
	for _, p := range pages {
		page, err := m.Page(p)
		if err != nil {
			t.Fatal(err)
		}
		format.PutU64(page[format.HeaderBorn:], w.txn)
	}
	// Discard the last allocated page, then pop it back from the free
	// list: the free cell must carry the magic and the transaction.
	last := pages[len(pages)-1]
	if err := w.DiscardPrivate(last); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if err := w.DiscardPrivate(last); err == nil {
		t.Fatalf("double discard: want refusal")
	}
	popped, err := w.Allocate()
	if err != nil {
		t.Fatalf("pop free: %v", err)
	}
	if popped != last {
		t.Errorf("popped = %d, want %d", popped, last)
	}
	// A foreign page (born outside the workspace transaction) cannot
	// enter the free list.
	if err := w.DiscardPrivate(pages[0]); err != nil {
		t.Fatalf("discard first page: %v", err)
	}
	foreign, err := m.Page(pages[1])
	if err != nil {
		t.Fatal(err)
	}
	format.PutU64(foreign[format.HeaderBorn:], 99)
	w.free = 0
	w.next = uint64(pages[1]) + 1
	if err := w.DiscardPrivate(pages[1]); err == nil {
		t.Fatalf("foreign-page discard: want refusal")
	}
}

// TestImmutableFeedBuilderExtentRefusesWrittenFiles proves the extent
// constructor never adopts an already-written file (Rust
// require_new_output).
func TestImmutableFeedBuilderExtentRefusesWrittenFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "written.bin")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	spec := testFeedSpec(t, format.AddressFamilyIPv4)
	if _, err := NewImmutableFeedOutputBuilder(file, spec, OutputBudget{MaxOutputPages: 2}, 4, 0); err == nil {
		t.Fatalf("adopted a written file: want refusal")
	}
}
