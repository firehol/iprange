//go:build (linux || darwin || freebsd || windows) && (amd64 || arm64)

// Worker-side fault memory (Rust worker.rs thread-local
// UNREADABLE_SOURCE_PAGES with set_unreadable_source_pages /
// source_page_unreadable / unreadable_source_pages): the sorted,
// duplicate-free source-page list of one worker session. The worker
// process serves exactly one session per process, so a package-level
// list is the exact Go analog of the Rust thread-local; the storage
// lives in the internal/mapping leaf (mapping.SetSessionUnreadablePages)
// because validation and recovery apply the list to their mappings and
// cannot import worker without a cycle. The mode drivers
// (cmd/iprange-v4-worker modes.go) record each request's pages here
// before the domain machine runs, exactly like Rust worker.rs:316-333;
// the mapping-application arms consume the list at the Rust
// validation.rs:310 / source_guard / inspection.rs:260 parity points,
// and the classification arms consult the mapping leaf session query at the Rust
// inspection.rs:260 parity point. The worker-facing API surface is
// SetSourceUnreadablePages; duplicates are refused verbatim before
// any machine runs.
package worker

import (
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// SetSourceUnreadablePages records one request's unreadable source
// pages (Rust set_unreadable_source_pages: the list is sorted and a
// duplicate is InvalidArgument with the verbatim Rust detail; an
// empty list clears the session state). The sort, duplicate proof,
// and storage are delegated to the mapping leaf session setter, the
// single owner of the list.
func SetSourceUnreadablePages(pages []uint32) error {
	return mapping.SetSessionUnreadablePages(pages)
}

// sourcePageUnreadable reports whether one source page is declared
// unreadable in this session (Rust worker.rs source_page_unreadable:
// the classification arms consult the mapping leaf session state
// directly, and this worker-package reader exists for the fault-memory
// tests and the future domain application seam).
func sourcePageUnreadable(page uint32) bool {
	return mapping.SessionPageUnreadable(page)
}

// sourceUnreadablePages returns a copy of the session's unreadable
// source pages (Rust worker.rs unreadable_source_pages; the copy
// keeps callers from mutating the session state).
func sourceUnreadablePages() []uint32 {
	return mapping.SessionUnreadablePages()
}
