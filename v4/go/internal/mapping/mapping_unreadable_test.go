//go:build !windows

package mapping

// Unreadable-page mapping tests (Rust mapping.rs set_unreadable_pages +
// page read path): the sorted-unique proof, the io-unreadable refusal
// class of declared pages before the range check, and the untouched
// View path. These are deterministic slice checks: a declared page is
// refused without any SIGBUS because the mapping bytes are never
// touched.

import (
	"errors"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// wantMappingCode fails the test unless err is exactly the given class.
func wantMappingCode(t *testing.T, err error, code format.ErrorCode) {
	t.Helper()
	var e *format.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want class %d", err, code)
	}
}

func TestSetUnreadablePagesSortedUniqueProof(t *testing.T) {
	m, err := OpenImmutable(makePagesFile(t, t.TempDir(), 4), nil)
	if err != nil {
		t.Fatal("open:", err)
	}
	defer m.Close()
	// The bootstrap mapping is exactly the two meta pages; remap to the
	// full extent like the reader bootstrap before exercising pages.
	if err := m.Remap(4 * format.PageSize); err != nil {
		t.Fatal("remap:", err)
	}

	cases := [][]uint32{
		{2, 1},
		{1, 2, 2},
		{3, 1, 2},
		{1, 1},
	}
	for _, pages := range cases {
		err := m.SetUnreadablePages(pages)
		var e *format.Error
		if !errors.As(err, &e) || e.Code != format.CodeInvalidArgument {
			t.Fatalf("SetUnreadablePages(%v) = %v, want InvalidArgument", pages, err)
		}
		if e.Detail != "unreadable mapped pages must be sorted and unique" {
			t.Fatalf("detail = %q, want the verbatim Rust detail", e.Detail)
		}
	}

	// A sorted unique list is accepted and replaces the previous list.
	if err := m.SetUnreadablePages([]uint32{1, 3}); err != nil {
		t.Fatal("set:", err)
	}
	if len(m.unreadablePages) != 2 || m.unreadablePages[0] != 1 || m.unreadablePages[1] != 3 {
		t.Fatalf("unreadablePages = %v, want [1 3]", m.unreadablePages)
	}

	// An empty list clears the declaration (Rust
	// (!pages.is_empty()).then(...)).
	if err := m.SetUnreadablePages(nil); err != nil {
		t.Fatal("clear:", err)
	}
	if m.unreadablePages != nil {
		t.Fatalf("unreadablePages = %v after clear, want nil", m.unreadablePages)
	}
}

func TestPageUnreadableClass(t *testing.T) {
	m, err := OpenImmutable(makePagesFile(t, t.TempDir(), 6), nil)
	if err != nil {
		t.Fatal("open:", err)
	}
	defer m.Close()
	if err := m.Remap(6 * format.PageSize); err != nil {
		t.Fatal("remap:", err)
	}

	if err := m.SetUnreadablePages([]uint32{1, 3}); err != nil {
		t.Fatal("set:", err)
	}
	// Declared pages refuse with the io-unreadable class deterministically.
	for _, page := range []uint32{1, 3} {
		if _, err := m.Page(page); err == nil {
			t.Fatalf("Page(%d) succeeded on a declared unreadable page", page)
		} else {
			wantMappingCode(t, err, format.CodeIO)
		}
	}
	// Non-declared in-extent pages keep the plain read path.
	for _, page := range []uint32{0, 2, 4, 5} {
		if _, err := m.Page(page); err != nil {
			t.Fatalf("Page(%d) = %v, want success", page, err)
		}
	}
	// A declared page outside the mapped extent still refuses with the
	// io-unreadable class (the unreadable check runs before the range
	// check, Rust page() order), while a non-declared out-of-range page
	// keeps the range class.
	if err := m.SetUnreadablePages([]uint32{99}); err != nil {
		t.Fatal("set:", err)
	}
	if _, err := m.Page(99); err == nil {
		t.Fatal("Page(99) succeeded on a declared unreadable page")
	} else {
		wantMappingCode(t, err, format.CodeIO)
	}
	if _, err := m.Page(7); err == nil {
		t.Fatal("Page(7) succeeded beyond the extent")
	} else {
		wantMappingCode(t, err, format.CodeFormatInvalid)
	}
}

func TestViewUnaffectedByUnreadablePages(t *testing.T) {
	m, err := OpenImmutable(makePagesFile(t, t.TempDir(), 4), nil)
	if err != nil {
		t.Fatal("open:", err)
	}
	defer m.Close()

	if err := m.SetUnreadablePages([]uint32{0}); err != nil {
		t.Fatal("set:", err)
	}
	// bytes()/View are not the page() path: the Rust authority refuses
	// only full-page reads, and the writer views must stay intact.
	if _, err := m.View(0, format.PageSize); err != nil {
		t.Fatalf("View(0, page) = %v, want success", err)
	}
}
