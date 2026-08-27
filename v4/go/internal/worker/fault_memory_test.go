//go:build linux || darwin || freebsd || windows

// Fault-memory tests (Rust worker.rs UNREADABLE_SOURCE_PAGES thread
// local): the sorting and duplicate refusal of the session source-page
// list, the binary-search query, and the copy-on-enumerate isolation.

package worker

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// wantInvalidArgumentDetail fails the test unless err is the exact
// InvalidArgument class with the given verbatim Rust detail.
func wantInvalidArgumentDetail(t *testing.T, err error, detail string) {
	t.Helper()
	var e *format.Error
	if !errors.As(err, &e) || e.Code != format.CodeInvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
	if e.Detail != detail {
		t.Fatalf("detail = %q, want %q", e.Detail, detail)
	}
}

func TestSetSourceUnreadablePagesSortsAndRefusesDuplicates(t *testing.T) {
	t.Cleanup(func() { _ = SetSourceUnreadablePages(nil) })

	// Unsorted input is sorted before it is stored (Rust
	// set_unreadable_source_pages sorts the request vec first).
	if err := SetSourceUnreadablePages([]uint32{7, 1, 4, 9}); err != nil {
		t.Fatal("set:", err)
	}
	if got := sourceUnreadablePages(); len(got) != 4 || got[0] != 1 || got[1] != 4 || got[2] != 7 || got[3] != 9 {
		t.Fatalf("pages = %v, want [1 4 7 9]", got)
	}

	// A duplicate is refused with the verbatim Rust class before any
	// state changes.
	err := SetSourceUnreadablePages([]uint32{1, 1})
	wantInvalidArgumentDetail(t, err, "unreadable source pages contain duplicates")
	err = SetSourceUnreadablePages([]uint32{3, 3, 5})
	wantInvalidArgumentDetail(t, err, "unreadable source pages contain duplicates")
	if got := sourceUnreadablePages(); len(got) != 4 || got[0] != 1 {
		t.Fatalf("pages after refusal = %v, want the previous [1 4 7 9]", got)
	}

	// An empty list clears the session state.
	if err := SetSourceUnreadablePages(nil); err != nil {
		t.Fatal("clear:", err)
	}
	if sourceUnreadablePages() != nil {
		t.Fatalf("pages after clear = %v, want nil", sourceUnreadablePages())
	}
}

func TestSourcePageUnreadableQuery(t *testing.T) {
	t.Cleanup(func() { _ = SetSourceUnreadablePages(nil) })
	if err := SetSourceUnreadablePages([]uint32{0, 4, 100}); err != nil {
		t.Fatal("set:", err)
	}
	for _, page := range []uint32{0, 4, 100} {
		if !sourcePageUnreadable(page) {
			t.Fatalf("page %d must be unreadable", page)
		}
	}
	for _, page := range []uint32{1, 3, 5, 99, 101, ^uint32(0)} {
		if sourcePageUnreadable(page) {
			t.Fatalf("page %d must be readable", page)
		}
	}
}

func TestSourceUnreadablePagesReturnsCopy(t *testing.T) {
	t.Cleanup(func() { _ = SetSourceUnreadablePages(nil) })
	if err := SetSourceUnreadablePages([]uint32{2, 6}); err != nil {
		t.Fatal("set:", err)
	}
	got := sourceUnreadablePages()
	got[0] = 99
	if sourcePageUnreadable(2) != true || sourcePageUnreadable(99) {
		t.Fatal("mutating the enumerated copy changed the session state")
	}
}
