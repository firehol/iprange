// Public live reader surface (Rust live_reader.rs LiveReader +
// reader_core/live.rs): OpenLiveReader registers one reader against a
// committed generation of a live database through the full sidecar
// coordination (shared main lifetime lock, reader-table gate, fresh-extent
// generation selection, slot claim). The reader exposes the same lookup
// and cursor surface as the immutable reader, including pins; Close
// clears the registration in the Rust order and reports the factual
// retryable close result.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// LiveReader is one registered reader against one committed generation of
// a live database (Rust LiveReader). It holds the shared main lifetime
// lock and one claimed sidecar reader slot from OpenLiveReader until
// Close; the writer keeps committing through the reader table while it is
// open, and every lookup reads this reader's pinned generation.
//
// Reader-level hot paths (Info, direct lookups and scans, cardinality)
// are zero-allocation and take no atomics; every operation reports
// WrongState when the reader is closing or closed. Callers must not race
// Close with reader work, and Close must not be called concurrently with
// another Close (the CAS loser reports the idempotent closed result). A
// reader with live pins cannot close: Close returns ErrorHandleBusy until
// every pin is closed. The closed transition is atomic, so Pin racing
// Close either pins the reader (HandleBusy) or reports WrongState,
// exactly like the immutable reader.
// A Close after the closed state reports the idempotent closed result; an
// incomplete close is retryable and reopens the facade for another Close
// while the internal reader stays close-only.
type LiveReader struct {
	lr *live.LiveReader
	sh *shared
}

// OpenLiveReader opens path as a registered reader of a live database
// (Rust LiveReader::open): the main file is mapped read-only under the
// shared lifetime lock, the ready reader table of the same database is
// gated, the committed generation is re-selected under the gate from a
// freshly sampled file extent, one reader slot is claimed, and the gate
// is released. The database must be a live pair (created by CreateLive or
// converted by InitializeLive). cancellation, when non-nil, is checked
// between every bounded step.
func OpenLiveReader(path string, cancellation *CancellationToken) (*LiveReader, error) {
	lr, err := live.OpenLiveReader(path, cancellation.check)
	if err != nil {
		return nil, publicError(err)
	}
	return &LiveReader{lr: lr, sh: &shared{}}, nil
}

// checkOpen is the plain closed-state read used by every reader-level
// operation: zero atomics per call, valid because Close must not race
// reader work (the same contract as the Rust SDK). The closed mirror is
// written only by Close; lookups that pass it still run the internal
// owner and open-state check (Rust LiveReader::core -> require_open).
func (r *LiveReader) checkOpen() error {
	if r.sh.closedState {
		return &Error{Code: ErrorWrongState, Detail: "live reader closed"}
	}
	if _, err := r.lr.Core(); err != nil {
		return publicError(err)
	}
	return nil
}

// Info returns the pinned generation's logical identity (Rust
// LiveReader::info). A live reader always reports a proven current
// selection: ModeLiveReader refuses a sole meta, so the immutable
// sole-meta selection states are unreachable here.
func (r *LiveReader) Info() (DatabaseInfo, error) {
	if err := r.checkOpen(); err != nil {
		return DatabaseInfo{}, err
	}
	meta := r.lr.CoreNoCheck().Meta()
	return DatabaseInfo{
		Family:           AddressFamily(meta.AddressFamily),
		ValueKind:        ValueKind(meta.ValueKind),
		StructureKind:    StructureKind(meta.StructureKind),
		ValueTag:         ValueTag{wire: meta.ValueTag},
		DatabaseID:       meta.DatabaseID,
		TransactionID:    meta.TxnID,
		CommitNonce:      meta.CommitNonce,
		PageCount:        meta.PageCount,
		RangeRecordCount: meta.RangeRecordCount,
		ActiveFeedCount:  meta.ActiveFeedCount,
		MetaSelection:    MetaSelectionProvenCurrent,
	}, nil
}

// FileIdentity returns the device and inode of the mapped main file. The
// pair is captured once from the opened descriptor at open and retained
// for the reader lifetime (Rust live_namespace::identity); the path is
// re-verified against it at registration and close. It mirrors
// ImmutableReader.FileIdentity so identity-comparison workflows (for
// example the writer same-file check) keep working.
func (r *LiveReader) FileIdentity() (device uint64, inode uint64, err error) {
	if err := r.checkOpen(); err != nil {
		return 0, 0, err
	}
	id := r.lr.Identity()
	device, inode = live.IdentityDeviceInode(&id)
	return device, inode, nil
}

// core exposes the internal reader core to the reader-level operations.
// Every public operation on this facade runs Core (requireOpen) first and
// then applies the shared require*Meta pre-checks through this accessor,
// so every operation pays exactly one open-state check (Rust parity).
func (r *LiveReader) core() *reader.ImmutableReader { return r.lr.CoreNoCheck() }

// addPin registers one pin unless the reader already closed (the closed
// re-check makes the losing Pin race report WrongState instead of pinning
// a closed reader).
func (r *LiveReader) addPin() bool {
	r.sh.pins.Add(1)
	if r.sh.closed.Load() {
		r.sh.pins.Add(-1)
		return false
	}
	return true
}

// dropPin returns one pin to the reader.
func (r *LiveReader) dropPin() { r.sh.pins.Add(-1) }

// Pin registers one caller-owned lifetime pin over the pinned generation.
// Pin once outside the workload: one pin may be shared across concurrent
// lookups, and lookups through the pin allocate nothing and take no
// atomics. The reader cannot close while any pin exists (Close reports
// HandleBusy). Pin.Close must not race lookups using the pin.
//
// Pin is one caller-owned lifetime registration. Every Pin value refers
// to one shared private close state: pointer aliases (p2 := p1) and value
// copies (p2 := *p1) close the same logical pin, and the count is
// decremented exactly once (the immutable Pin contract).
func (r *LiveReader) Pin() (*Pin, error) {
	if r.sh.closed.Load() {
		return nil, &Error{Code: ErrorWrongState, Detail: "live reader closed"}
	}
	// A Close that raced this Pin either saw the added pin (HandleBusy)
	// or closed first; addPin's closed re-check makes the loser return
	// WrongState instead of pinning a closed reader.
	if !r.addPin() {
		return nil, &Error{Code: ErrorWrongState, Detail: "live reader closed"}
	}
	// The owner and open-state check refuses pins on a closing or
	// close-only reader (a close-only reader from a failed close attempt
	// stays closeable but accepts no new pins); the pin added above is
	// returned on failure. The check cannot race the internal close: the
	// atomic closed transition above already blocks any close while this
	// pin is registered.
	core, err := r.lr.Core()
	if err != nil {
		r.dropPin()
		return nil, publicError(err)
	}
	return &Pin{st: &pinState{h: r, core: core}}, nil
}

// LookupDirectV4 returns the direct value covering ip in the pinned
// generation, or false when absent.
func (r *LiveReader) LookupDirectV4(ip IPv4) (uint32, bool, error) {
	core, err := r.lr.Core()
	if err != nil {
		return 0, false, publicError(err)
	}
	if err := requireDirectMeta(core, 4); err != nil {
		return 0, false, err
	}
	value, found, err := core.LookupDirect4(uint32(ip))
	if err != nil {
		return 0, false, publicError(err)
	}
	return value, found, nil
}

// LookupDirectV6 returns the direct value covering ip in the pinned
// generation, or false when absent.
func (r *LiveReader) LookupDirectV6(ip IPv6) (uint32, bool, error) {
	core, err := r.lr.Core()
	if err != nil {
		return 0, false, publicError(err)
	}
	if err := requireDirectMeta(core, 6); err != nil {
		return 0, false, err
	}
	value, found, err := core.LookupDirect6(ip.Hi, ip.Lo)
	if err != nil {
		return 0, false, publicError(err)
	}
	return value, found, nil
}

// DirectRangesV4 visits every range record of the pinned IPv4 generation
// in ascending order. Errors returned by yield stop the scan and pass
// through unchanged.
func (r *LiveReader) DirectRangesV4(yield func(DirectRangeV4) error) error {
	core, err := r.lr.Core()
	if err != nil {
		return publicError(err)
	}
	if err := requireDirectMeta(core, 4); err != nil {
		return err
	}
	if err := core.ScanDirect4(func(v reader.RangeVisit4) error {
		return yield(DirectRangeV4{From: v.From, To: v.To, Value: v.Value})
	}); err != nil {
		return publicError(err)
	}
	return nil
}

// DirectRangesV6 visits every range record of the pinned IPv6 generation
// in ascending order. Errors returned by yield stop the scan and pass
// through unchanged.
func (r *LiveReader) DirectRangesV6(yield func(DirectRangeV6) error) error {
	core, err := r.lr.Core()
	if err != nil {
		return publicError(err)
	}
	if err := requireDirectMeta(core, 6); err != nil {
		return err
	}
	if err := core.ScanDirect6(func(v reader.RangeVisit6) error {
		return yield(DirectRangeV6{FromHi: v.FromHi, FromLo: v.FromLo, ToHi: v.ToHi, ToLo: v.ToLo, Value: v.Value})
	}); err != nil {
		return publicError(err)
	}
	return nil
}

// Cardinality returns the exact inclusive address count of the pinned
// generation.
func (r *LiveReader) Cardinality() (Cardinality129, error) {
	core, err := r.lr.Core()
	if err != nil {
		return Cardinality129{}, publicError(err)
	}
	value, err := core.Cardinality()
	if err != nil {
		return Cardinality129{}, publicError(err)
	}
	return NewCardinality129(value.Bit128(), value.Hi(), value.Lo())
}

// LookupFeed returns the catalog entry for one exact feed name in the
// pinned generation. The name is a copied entry; the zero-allocation feed
// lookup is Pin.LookupFeedInto.
func (r *LiveReader) LookupFeed(name string) (FeedEntry, bool, error) {
	core, err := r.lr.Core()
	if err != nil {
		return FeedEntry{}, false, publicError(err)
	}
	// The name is validated before the kind check, mirroring
	// feed_catalog.rs, so an invalid name on a direct database reports
	// NameInvalid, not WrongValueKind.
	if !format.FeedNameValidString(name) {
		return FeedEntry{}, false, &Error{Code: ErrorNameInvalid, Detail: "invalid feed name"}
	}
	if err := requireMembershipCapableMeta(core); err != nil {
		return FeedEntry{}, false, err
	}
	entry, found, err := core.LookupFeed(name)
	if err != nil {
		return FeedEntry{}, false, publicError(err)
	}
	if !found {
		return FeedEntry{}, false, nil
	}
	return FeedEntry{Index: entry.FeedIndex, Name: string(entry.Name)}, true, nil
}

// MetadataJSON returns the exact decompressed opaque metadata bytes of
// the pinned generation. present is false when metadata is absent; empty
// bytes with present true are the distinct empty state.
func (r *LiveReader) MetadataJSON() ([]byte, bool, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, false, publicError(err)
	}
	bytes, present, err := core.ReadMetadataJSON()
	if err != nil {
		return nil, false, publicError(err)
	}
	return bytes, present, nil
}

// The eight cursor and query constructors below mirror the immutable
// reader surface (Rust live_reader.rs direct_cursor_v4/v6, feed_cursor,
// feed_range_cursor_v4/v6, network_enrichment_v1_cursor_v4/v6,
// membership_query). Each runs the owner and open-state check exactly
// once (Rust LiveReader::core -> require_open) and then applies the same
// meta pre-checks as the immutable reader.

// DirectCursorV4 opens the IPv4 direct-range cursor in direction (Rust
// LiveReader::direct_cursor_v4).
func (r *LiveReader) DirectCursorV4(direction RangeDirection) (*DirectCursorV4, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireDirectMeta(core, 4); err != nil {
		return nil, err
	}
	inner, err := core.NewDirectCursor4(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &DirectCursorV4{r: r, inner: inner}, nil
}

// DirectCursorV6 opens the IPv6 direct-range cursor in direction (Rust
// LiveReader::direct_cursor_v6).
func (r *LiveReader) DirectCursorV6(direction RangeDirection) (*DirectCursorV6, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireDirectMeta(core, 6); err != nil {
		return nil, err
	}
	inner, err := core.NewDirectCursor6(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &DirectCursorV6{r: r, inner: inner}, nil
}

// FeedCursor opens the forward catalog cursor (Rust
// LiveReader::feed_cursor).
func (r *LiveReader) FeedCursor() (*FeedCursor, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireMembershipCapableMeta(core); err != nil {
		return nil, err
	}
	inner, err := core.NewFeedCursor()
	if err != nil {
		return nil, publicError(err)
	}
	return &FeedCursor{r: r, inner: inner}, nil
}

// FeedRangeCursorV4 opens the IPv4 projection of the named feed in
// direction (Rust LiveReader::feed_range_cursor_v4). The name is
// validated by the core lookup with the same order as the immutable
// cursor: name validity first, then catalog presence.
func (r *LiveReader) FeedRangeCursorV4(name string, direction RangeDirection) (*FeedRangeCursorV4, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireMembershipCapableMeta(core); err != nil {
		return nil, err
	}
	if core.Meta().AddressFamily != 4 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "feed cursor address family does not match the database"}
	}
	entry, found, err := core.LookupFeed(name)
	if err != nil {
		return nil, publicError(err)
	}
	if !found {
		return nil, &Error{Code: ErrorNameNotFound, Detail: "feed name not in the catalog"}
	}
	inner, err := core.NewFeedRangeProjection4(entry.FeedIndex, reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &FeedRangeCursorV4{r: r, inner: inner}, nil
}

// FeedRangeCursorV6 opens the IPv6 projection of the named feed in
// direction (Rust LiveReader::feed_range_cursor_v6).
func (r *LiveReader) FeedRangeCursorV6(name string, direction RangeDirection) (*FeedRangeCursorV6, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireMembershipCapableMeta(core); err != nil {
		return nil, err
	}
	if core.Meta().AddressFamily != 6 {
		return nil, &Error{Code: ErrorWrongAddressFamily, Detail: "feed cursor address family does not match the database"}
	}
	entry, found, err := core.LookupFeed(name)
	if err != nil {
		return nil, publicError(err)
	}
	if !found {
		return nil, &Error{Code: ErrorNameNotFound, Detail: "feed name not in the catalog"}
	}
	inner, err := core.NewFeedRangeProjection6(entry.FeedIndex, reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	return &FeedRangeCursorV6{r: r, inner: inner}, nil
}

// NetworkEnrichmentV1CursorV4 opens the IPv4 enrichment cursor in the
// requested direction (Rust LiveReader::network_enrichment_v1_cursor_v4).
// The cursor holds one reader lifetime pin (Rust borrow parity): the
// reader refuses to close while the cursor is open, and Close releases
// the pin.
func (r *LiveReader) NetworkEnrichmentV1CursorV4(direction RangeDirection) (*NetworkEnrichmentV1CursorV4, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireNetworkEnrichmentMeta(core, 4); err != nil {
		return nil, err
	}
	inner, err := core.NewNetworkEnrichmentV1Cursor4(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	// A Close that raced this cursor either saw the added pin
	// (HandleBusy) or closed first; addPin's closed re-check makes the
	// loser return WrongState instead of pinning a closed reader.
	if !r.addPin() {
		return nil, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	return &NetworkEnrichmentV1CursorV4{r: r, inner: inner, state: &pinState{h: r}}, nil
}

// NetworkEnrichmentV1CursorV6 opens the IPv6 enrichment cursor in the
// requested direction (Rust LiveReader::network_enrichment_v1_cursor_v6).
// The cursor holds one reader lifetime pin (Rust borrow parity): the
// reader refuses to close while the cursor is open, and Close releases
// the pin.
func (r *LiveReader) NetworkEnrichmentV1CursorV6(direction RangeDirection) (*NetworkEnrichmentV1CursorV6, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireNetworkEnrichmentMeta(core, 6); err != nil {
		return nil, err
	}
	inner, err := core.NewNetworkEnrichmentV1Cursor6(reader.RangeDirection(direction))
	if err != nil {
		return nil, publicError(err)
	}
	// A Close that raced this cursor either saw the added pin
	// (HandleBusy) or closed first; addPin's closed re-check makes the
	// loser return WrongState instead of pinning a closed reader.
	if !r.addPin() {
		return nil, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	return &NetworkEnrichmentV1CursorV6{r: r, inner: inner, state: &pinState{h: r}}, nil
}

// MembershipQuery opens the membership query surface (Rust
// LiveReader::membership_query). The database must be a membership
// database (structured databases are refused; Rust
// membership_query::Query::new parity).
func (r *LiveReader) MembershipQuery() (*MembershipQuery, error) {
	core, err := r.lr.Core()
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireMembershipQueryMeta(core); err != nil {
		return nil, err
	}
	return &MembershipQuery{r: r}, nil
}

// Close clears this reader's registration (Rust LiveReader::close): the
// gate is taken shared, the registration is proven against the still
// current pair, the mapping is unmapped, the slot is cleared and
// unlocked, the gate is released, and finally the shared lifetime lock is
// released. A second Close on the closed state is idempotent success; an
// incomplete close is retryable and keeps the reader usable for another
// Close (Rust ReaderCloseResult parity). A reader with live pins reports
// HandleBusy without closing.
func (r *LiveReader) Close() (ReaderCloseResult, error) {
	r.sh.closedState = true
	if !r.sh.closed.CompareAndSwap(false, true) {
		return readerClosedResult(), nil
	}
	if r.sh.pins.Load() != 0 {
		r.sh.closed.Store(false)
		r.sh.closedState = false
		return ReaderCloseResult{}, &Error{Code: ErrorHandleBusy, Detail: "live reader has live pins"}
	}
	// The atomic closed transition now blocks every new pin, so the
	// internal close below never runs with a live pin registered. An
	// incomplete close reopens the facade: the internal reader keeps its
	// retry state, lookups and pins report WrongState through the
	// internal close-only state, and the caller retries Close.
	result, err := r.lr.Close()
	if err != nil {
		r.sh.closed.Store(false)
		r.sh.closedState = false
		return ReaderCloseResult{}, publicError(err)
	}
	if !result.Closed {
		r.sh.closed.Store(false)
		r.sh.closedState = false
	}
	return publicReaderCloseResult(result), nil
}

// ReaderCloseResult is the factual, retryable live-reader close result
// (Rust live_reader.rs ReaderCloseResult): the outcome, the coordination
// residue class the caller must still release, and the failure cause when
// the close is incomplete.
type ReaderCloseResult struct {
	Outcome             CloseOutcome
	CoordinationCleanup CoordinationCleanup
	Cause               error
}

// CleanupState reports whether the close left coordination residue (Rust
// ReaderCloseResult::cleanup_state).
func (r ReaderCloseResult) CleanupState() CleanupState {
	if r.CoordinationCleanup == CoordinationCleanupNone {
		return CleanupStateClean
	}
	return CleanupStateResiduePossible
}

// publicReaderCloseResult maps one internal live-reader close result to
// the public SDK surface (Rust From<LiveReaderClose> for
// ReaderCloseResult).
func publicReaderCloseResult(result live.LiveReaderClose) ReaderCloseResult {
	outcome := CloseOutcomeCloseIncomplete
	if result.Closed {
		outcome = CloseOutcomeClosed
	}
	return ReaderCloseResult{
		Outcome:             outcome,
		CoordinationCleanup: publicCoordinationCleanup(result.CoordinationCleanup),
		Cause:               publicError(result.Cause),
	}
}

// readerClosedResult builds the public success close result of an already
// closed live reader (Rust ReaderCloseResult from the closed state).
func readerClosedResult() ReaderCloseResult {
	return ReaderCloseResult{Outcome: CloseOutcomeClosed, CoordinationCleanup: CoordinationCleanupNone}
}
