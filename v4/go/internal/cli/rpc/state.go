// Connection-owned reader and cursor state for the JSON-RPC methods.
//
// Both reader modes expose the same semantic operations; delegation
// keeps handlers mode-neutral while preserving the SDK's distinct
// immutable and registered-live ownership and close behavior.
// Cursor progression mirrors the Rust design: public cursors borrow
// their reader, so the connection retains a semantic checkpoint and
// each `next` operation opens a fresh cursor and seeks to it, keeping
// every response bounded.

package rpc

import (
	"sort"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// ReaderValue is one connection-local reader handle: an immutable
// reader or a registered live reader.
type ReaderValue struct {
	Immutable *iprangedb.ImmutableReader
	Live      *iprangedb.LiveReader
}

// CloseLive closes only a registered live reader. Immutable readers
// have no registration lease and are released by the connection map.
func (rv *ReaderValue) CloseLive() (iprangedb.ReaderCloseResult, bool, error) {
	if rv.Live == nil {
		return iprangedb.ReaderCloseResult{}, false, nil
	}
	result, err := rv.Live.Close()
	return result, true, err
}

// CursorPoint is one canonical address checkpoint used to re-open and
// seek a cursor.
type CursorPoint struct {
	V4 *uint32
	V6 *iprangedb.IPv6
}

// CursorKind is the method family that owns a cursor; handles from one
// family are not accepted by the other family's next/close methods.
type CursorKind uint8

const (
	CursorKindFeeds CursorKind = iota
	CursorKindRanges
)

// CursorView is the logical view represented by a cursor.
type CursorView struct {
	Direct     bool
	Structured bool
	FeedName   string // non-empty for feed views
}

// CursorValue is the connection-retained cursor progression state.
type CursorValue struct {
	Kind          CursorKind
	Reader        string
	View          CursorView
	Reverse       bool
	Point         *CursorPoint
	LastFeedIndex *uint32
	BatchSize     int
	Exhausted     bool
}

// ClosedHandleTombstoneCap bounds the closed-handle tombstones
// retained per handle family. Closed handles are kept so a later
// operation can distinguish a closed handle from a totally unknown
// one (`handle_closed` / `cursor_closed`); the maps stay bounded with
// FIFO eviction, and an evicted tombstone answers as unknown, which
// the spec permits.
const ClosedHandleTombstoneCap = 1024

// ConnectionState is the mutable per-connection resource set.
type ConnectionState struct {
	Readers           map[string]*ReaderValue
	ClosedReaders     map[string]bool
	Cursors           map[string]*CursorValue
	ClosedCursors     map[string]bool
	closedReaderOrder []string
	closedCursorOrder []string
}

func NewConnectionState() *ConnectionState {
	return &ConnectionState{
		Readers:       make(map[string]*ReaderValue),
		ClosedReaders: make(map[string]bool),
		Cursors:       make(map[string]*CursorValue),
		ClosedCursors: make(map[string]bool),
	}
}

// RecordClosedReader keeps a bounded FIFO tombstone.
func (cs *ConnectionState) RecordClosedReader(handle string) {
	cs.recordClosed(cs.ClosedReaders, &cs.closedReaderOrder, handle)
}

// RecordClosedCursor keeps a bounded FIFO tombstone.
func (cs *ConnectionState) RecordClosedCursor(handle string) {
	cs.recordClosed(cs.ClosedCursors, &cs.closedCursorOrder, handle)
}

// recordClosed inserts one tombstone and FIFO-evicts the oldest entry
// of the family when the bound is exceeded.
func (cs *ConnectionState) recordClosed(set map[string]bool, order *[]string, handle string) {
	set[handle] = true
	*order = append(*order, handle)
	for len(*order) > ClosedHandleTombstoneCap {
		oldest := (*order)[0]
		*order = (*order)[1:]
		delete(set, oldest)
	}
}

// CloseAll is the transport-shutdown cleanup: drop every cursor
// checkpoint and close each registered live reader in deterministic
// handle order (immutable readers need no close). Returns one entry
// per live reader whose close failed outright or finished incomplete
// so the caller can report an incomplete transport shutdown.
func (cs *ConnectionState) CloseAll() []string {
	cs.Cursors = make(map[string]*CursorValue)
	cs.ClosedCursors = make(map[string]bool)
	cs.closedCursorOrder = nil
	handles := make([]string, 0, len(cs.Readers))
	for handle := range cs.Readers {
		handles = append(handles, handle)
	}
	sort.Strings(handles)
	var failures []string
	for _, handle := range handles {
		reader := cs.Readers[handle]
		if reader == nil || reader.Live == nil {
			continue
		}
		result, err := reader.Live.Close()
		if err != nil {
			failures = append(failures, handle+": "+err.Error())
			continue
		}
		if result.Outcome != iprangedb.CloseOutcomeClosed || result.Cause != nil {
			var cause string
			if result.Cause != nil {
				cause = result.Cause.Error()
			} else {
				cause = "live reader close is incomplete"
			}
			failures = append(failures, handle+": "+cause)
		}
	}
	cs.Readers = make(map[string]*ReaderValue)
	cs.ClosedReaders = make(map[string]bool)
	cs.closedReaderOrder = nil
	return failures
}
