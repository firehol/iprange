package mapping

// Worker-session unreadable source-page state (Rust worker.rs
// UNREADABLE_SOURCE_PAGES thread-local): the sorted, duplicate-free
// page list of one worker session. The state lives in this leaf
// package because internal/worker imports validation and recovery,
// so those domain machines cannot import worker; the session list
// must still reach every mapping they create. The worker fault
// memory (internal/worker fault_memory.go) is the only writer, and
// the parity-point readers are the domain machines (validation
// sweep mappings, recovery source guards) and the recovery
// classification. In normal SDK use the session list is empty and
// no refusal is ever applied: local paths observe zero behavior
// change.

import (
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// sessionUnreadablePages is the session's sorted, duplicate-free
// unreadable source-page list (Rust UNREADABLE_SOURCE_PAGES); nil
// when no page is declared unreadable.
var sessionUnreadablePages []uint32

// SetSessionUnreadablePages records one worker session's unreadable
// source pages after sorting and duplicate rejection (Rust
// set_unreadable_source_pages: a duplicate is InvalidArgument with
// the verbatim Rust detail). An empty list clears the session state.
func SetSessionUnreadablePages(pages []uint32) error {
	sorted := append([]uint32(nil), pages...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for index := 1; index < len(sorted); index++ {
		if sorted[index] == sorted[index-1] {
			return &format.Error{Code: format.CodeInvalidArgument, Detail: "unreadable source pages contain duplicates"}
		}
	}
	sessionUnreadablePages = sorted
	return nil
}

// SessionUnreadablePages returns a copy of the session's unreadable
// source pages (Rust worker.rs unreadable_source_pages; the copy
// keeps callers from mutating the session state).
func SessionUnreadablePages() []uint32 {
	return append([]uint32(nil), sessionUnreadablePages...)
}

// SessionPageUnreadable reports whether one source page is declared
// unreadable in this session (Rust worker.rs source_page_unreadable:
// a binary search over the sorted list).
func SessionPageUnreadable(page uint32) bool {
	index := sort.Search(len(sessionUnreadablePages), func(i int) bool {
		return sessionUnreadablePages[i] >= page
	})
	return index < len(sessionUnreadablePages) && sessionUnreadablePages[index] == page
}
