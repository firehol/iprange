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
	"sync"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// sessionUnreadablePages is the session's sorted, duplicate-free
// unreadable source-page list (Rust UNREADABLE_SOURCE_PAGES); nil
// when no page is declared unreadable. The mutex makes the state
// race-free even though the shipped worker process runs exactly one
// session: the worker binary is the single writer (once, before the
// domain machine runs), the domain machines read the list at mapping
// creation, and library processes never write it. The invariant is
// one logical session per process; callers must not write the state
// from two goroutines expecting two sessions.
var (
	sessionUnreadableMu    sync.RWMutex
	sessionUnreadablePages []uint32
)

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
	sessionUnreadableMu.Lock()
	sessionUnreadablePages = sorted
	sessionUnreadableMu.Unlock()
	return nil
}

// SessionUnreadablePages returns a copy of the session's unreadable
// source pages (Rust worker.rs unreadable_source_pages; the copy
// keeps callers from mutating the session state).
func SessionUnreadablePages() []uint32 {
	sessionUnreadableMu.RLock()
	defer sessionUnreadableMu.RUnlock()
	return append([]uint32(nil), sessionUnreadablePages...)
}

// SessionPageUnreadable reports whether one source page is declared
// unreadable in this session (Rust worker.rs source_page_unreadable:
// a binary search over the sorted list).
func SessionPageUnreadable(page uint32) bool {
	sessionUnreadableMu.RLock()
	defer sessionUnreadableMu.RUnlock()
	// The emptiness guard mirrors Mapping.Page: the normal SDK path
	// (no session) skips the search entirely.
	if len(sessionUnreadablePages) == 0 {
		return false
	}
	index := sort.Search(len(sessionUnreadablePages), func(i int) bool {
		return sessionUnreadablePages[i] >= page
	})
	return index < len(sessionUnreadablePages) && sessionUnreadablePages[index] == page
}
