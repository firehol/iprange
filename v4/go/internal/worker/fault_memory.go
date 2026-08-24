//go:build linux && amd64

// Worker-side fault memory (Rust worker.rs thread-local
// UNREADABLE_SOURCE_PAGES with set_unreadable_source_pages /
// source_page_unreadable / unreadable_source_pages): the sorted,
// duplicate-free source-page list of one worker session. The worker
// process serves exactly one session per process, so a package-level
// list is the exact Go analog of the Rust thread-local. The mode
// drivers (cmd/iprange-v4-worker modes.go) record each request's pages
// here before the domain machine runs, exactly like Rust
// worker.rs:316-333; the mapping-application arms consume the list at
// the Rust validation.rs:310 parity point through
// mapping.SetUnreadablePages once the domain machines carry that call,
// and the classification arms consult SourcePageUnreadable at the Rust
// validation.rs:393 / inspection.rs:260 parity points. In this slice
// the queries and the enumeration pin the ported surface, and the mode
// drivers prove the refusal classes (duplicates are refused verbatim
// before any machine runs).
package worker

import (
	"sort"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// sourceUnreadablePages is the session's sorted, duplicate-free
// unreadable source-page list (Rust UNREADABLE_SOURCE_PAGES); nil when
// no page is declared unreadable.
var sourceUnreadablePages []uint32

// SetSourceUnreadablePages records one request's unreadable source
// pages after sorting and duplicate rejection (Rust
// set_unreadable_source_pages: a duplicate is InvalidArgument with the
// verbatim Rust detail). An empty list clears the session state.
func SetSourceUnreadablePages(pages []uint32) error {
	sorted := append([]uint32(nil), pages...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for index := 1; index < len(sorted); index++ {
		if sorted[index] == sorted[index-1] {
			return &format.Error{Code: format.CodeInvalidArgument, Detail: "unreadable source pages contain duplicates"}
		}
	}
	sourceUnreadablePages = sorted
	return nil
}

// SourcePageUnreadable reports whether one source page is declared
// unreadable in this session (Rust worker.rs source_page_unreadable:
// a binary search over the sorted list).
func SourcePageUnreadable(page uint32) bool {
	index := sort.Search(len(sourceUnreadablePages), func(i int) bool {
		return sourceUnreadablePages[i] >= page
	})
	return index < len(sourceUnreadablePages) && sourceUnreadablePages[index] == page
}

// SourceUnreadablePages returns a copy of the session's unreadable
// source pages (Rust worker.rs unreadable_source_pages; the copy keeps
// callers from mutating the session state).
func SourceUnreadablePages() []uint32 {
	return append([]uint32(nil), sourceUnreadablePages...)
}
