package mapping

// Session unreadable-page state tests (Rust worker.rs
// UNREADABLE_SOURCE_PAGES thread-local): the sorting and duplicate
// refusal of the session list, the binary-search query, the
// copy-on-enumerate isolation, and the clear-to-empty transition.
// The session state is package-level, so every test restores the
// empty list in its cleanup exactly like the worker fault-memory
// suite.

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func TestSetSessionUnreadablePagesSortsAndRefusesDuplicates(t *testing.T) {
	t.Cleanup(func() { sessionUnreadablePages = nil })

	// Unsorted input is sorted before it is stored (Rust
	// set_unreadable_source_pages sorts the request list first).
	if err := SetSessionUnreadablePages([]uint32{7, 1, 4, 9}); err != nil {
		t.Fatal("set:", err)
	}
	if got := SessionUnreadablePages(); len(got) != 4 || got[0] != 1 || got[1] != 4 || got[2] != 7 || got[3] != 9 {
		t.Fatalf("pages = %v, want [1 4 7 9]", got)
	}

	// A duplicate is refused with the verbatim worker detail before
	// any state changes.
	for _, pages := range [][]uint32{{1, 1}, {3, 3, 5}} {
		err := SetSessionUnreadablePages(pages)
		var e *format.Error
		if !errors.As(err, &e) || e.Code != format.CodeInvalidArgument {
			t.Fatalf("SetSessionUnreadablePages(%v) = %v, want InvalidArgument", pages, err)
		}
		if e.Detail != "unreadable source pages contain duplicates" {
			t.Fatalf("detail = %q, want the verbatim worker detail", e.Detail)
		}
	}
	if got := SessionUnreadablePages(); len(got) != 4 || got[0] != 1 {
		t.Fatalf("pages after refusal = %v, want the previous [1 4 7 9]", got)
	}

	// An empty list clears the session state.
	if err := SetSessionUnreadablePages(nil); err != nil {
		t.Fatal("clear:", err)
	}
	if SessionUnreadablePages() != nil {
		t.Fatalf("pages after clear = %v, want nil", SessionUnreadablePages())
	}
}

func TestSessionPageUnreadableQuery(t *testing.T) {
	t.Cleanup(func() { sessionUnreadablePages = nil })
	if err := SetSessionUnreadablePages([]uint32{0, 4, 100}); err != nil {
		t.Fatal("set:", err)
	}
	for _, page := range []uint32{0, 4, 100} {
		if !SessionPageUnreadable(page) {
			t.Fatalf("page %d must be unreadable", page)
		}
	}
	for _, page := range []uint32{1, 3, 5, 99, 101, ^uint32(0)} {
		if SessionPageUnreadable(page) {
			t.Fatalf("page %d must be readable", page)
		}
	}
}

func TestSessionUnreadablePagesReturnsCopy(t *testing.T) {
	t.Cleanup(func() { sessionUnreadablePages = nil })
	if err := SetSessionUnreadablePages([]uint32{2, 6}); err != nil {
		t.Fatal("set:", err)
	}
	got := SessionUnreadablePages()
	got[0] = 99
	if !SessionPageUnreadable(2) || SessionPageUnreadable(99) {
		t.Fatal("mutating the enumerated copy changed the session state")
	}
}
