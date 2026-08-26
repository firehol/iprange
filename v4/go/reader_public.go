package iprangedb

import (
	"errors"
	"sync/atomic"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// This file is the public immutable reader facade: a pinned
// reader surface whose lookups, scans, and view operations are zero-
// allocation and zero-atomic inside the hot loop (SOW-0025:175;
// design-iprange-engine.md: warm lookups allocate nothing and run without a
// per-call mutex, atomic, or active counter). Lifetime state lives at pin
// boundaries (Pin/Close), never in lookups.

// StructureKind selects the immutable hardcoded structure of a structured
// database (binary-format-v4.md section 9A).
type StructureKind uint8

const (
	StructureKindNone                StructureKind = 0
	StructureKindNetworkEnrichmentV1 StructureKind = 1
)

// DirectSemantic classifies the direct value tag. The numeric values are
// the engine-defined registry shared with Rust and the C ABI: generic = 1,
// first_seen = 2, last_seen = 3 (Rust contract.rs DirectSemantic).
type DirectSemantic uint8

const (
	DirectSemanticGeneric   DirectSemantic = 1
	DirectSemanticFirstSeen DirectSemantic = 2
	DirectSemanticLastSeen  DirectSemantic = 3
)

// MetaSelection reports how the selected meta was derived
// (binary-format-v4.md section 4.2).
type MetaSelection uint8

const (
	MetaSelectionProvenCurrent MetaSelection = iota
	MetaSelectionSoleMeta0
	MetaSelectionSoleMeta1
)

// DatabaseInfo is the public logical identity of the selected generation
// (Rust ValueTag parity name). ValueTag
// carries the canonical tag: the engine-defined first_seen and last_seen
// tags or any caller-created tag (binary-format-v4.md section 4: at most 15
// non-NUL bytes, then a mandatory NUL).
type DatabaseInfo struct {
	Family           AddressFamily
	ValueKind        ValueKind
	StructureKind    StructureKind
	ValueTag         ValueTag
	DatabaseID       [16]byte
	TransactionID    uint64
	CommitNonce      [16]byte
	PageCount        uint64
	RangeRecordCount uint64
	ActiveFeedCount  uint64
	MetaSelection    MetaSelection
}

// DirectSemantic returns the tag-derived semantic for direct databases.
// Classification compares the private canonical wire forms, so callers can
// never alter it.
func (i DatabaseInfo) DirectSemantic() (DirectSemantic, bool) {
	if i.ValueKind != ValueKindDirect {
		return DirectSemanticGeneric, false
	}
	switch i.ValueTag.wire {
	case firstSeenWire:
		return DirectSemanticFirstSeen, true
	case lastSeenWire:
		return DirectSemanticLastSeen, true
	default:
		return DirectSemanticGeneric, true
	}
}

// shared is the reader-wide lifetime state. Lookups and scans synchronize
// nothing: they read the plain closedState mirror, which is written only by
// Close, and the documented contract is that Close must not race reader work
// (design-iprange-engine.md; binary-format-v4.md). Atomically visible state
// exists only at pin boundaries and Close.
type shared struct {
	closed      atomic.Bool // closed transitions only inside Close/Pin
	closedState bool        // plain mirror for zero-atomic lookups
	pins        atomic.Int64
}

// ImmutableReader is one opened immutable v4 database.
//
// Reader-level hot paths (Info, direct lookups and scans, cardinality) are
// zero-allocation and take no atomics; feed lookup adds exactly one
// returned string copy and metadata decode allocates its compressed and
// decompressed buffers, all bounded caller-visible values per the format
// contract; all
// reader-level operations report WrongState when the reader is closed.
// Callers must not race Close
// with reader work. A reader with live pins cannot close: Close returns
// ErrorHandleBusy until every pin is closed. A second Close reports
// ErrorWrongState.
type ImmutableReader struct {
	inner *reader.ImmutableReader
	sh    *shared
}

// OpenImmutable opens path as an immutable v4 database with the exact
// bootstrap rules of the current format contract.
func OpenImmutable(path string) (*ImmutableReader, error) {
	inner, err := reader.OpenImmutable(path)
	if err != nil {
		return nil, publicError(err)
	}
	return &ImmutableReader{inner: inner, sh: &shared{}}, nil
}

// checkOpen is the plain closed-state read used by every reader-level
// operation: zero atomics per call, valid because Close must not race
// reader work (the same contract as the Rust SDK).
func (r *ImmutableReader) checkOpen() error {
	if r.sh.closedState {
		return &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	return nil
}

// Info returns the selected generation's logical identity.
func (r *ImmutableReader) Info() (DatabaseInfo, error) {
	if err := r.checkOpen(); err != nil {
		return DatabaseInfo{}, err
	}
	meta := r.inner.Meta()
	var selection MetaSelection
	switch r.inner.Selection() {
	case reader.MetaSelectionSoleMeta0:
		selection = MetaSelectionSoleMeta0
	case reader.MetaSelectionSoleMeta1:
		selection = MetaSelectionSoleMeta1
	default:
		selection = MetaSelectionProvenCurrent
	}
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
		MetaSelection:    selection,
	}, nil
}

// FileIdentity returns the device and inode of the mapped file (Rust
// OpenedMain::identity). The writer workflows compare it with the writer
// identity so a database can never be its own source (Rust
// require_compatible_source same-file check).
func (r *ImmutableReader) FileIdentity() (device uint64, inode uint64, err error) {
	if err := r.checkOpen(); err != nil {
		return 0, 0, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	device, inode, err = r.inner.FileIdentity()
	if err != nil {
		return 0, 0, publicError(err)
	}
	return device, inode, nil
}

// The five require* helpers mirror the Rust reader pre-checks exactly:
// wrong kind and wrong family are reported before any page is touched
// (reader_core/generation.rs require_direct/require_membership_family,
// membership_view.rs require_kind, structured_value/view.rs require_kind,
// feed_catalog.rs require_membership). The meta projections are shared
// package-level helpers so the immutable and live public facades apply
// exactly the same pre-checks.

func requireDirectMeta(core *reader.ImmutableReader, family uint8) error {
	m := core.Meta()
	if m.ValueKind != format.ValueKindDirect {
		return &Error{Code: ErrorWrongValueKind, Detail: "direct lookup requires a direct-value database"}
	}
	if m.AddressFamily != family {
		return &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	return nil
}

func requireMembershipMeta(core *reader.ImmutableReader, family uint8) error {
	m := core.Meta()
	if m.ValueKind != format.ValueKindMembership {
		return &Error{Code: ErrorWrongValueKind, Detail: "membership lookup requires a membership database"}
	}
	if m.AddressFamily != family {
		return &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	return nil
}

func requireNetworkEnrichmentMeta(core *reader.ImmutableReader, family uint8) error {
	m := core.Meta()
	if m.ValueKind != format.ValueKindStructured || m.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return &Error{Code: ErrorWrongStructureKind, Detail: "network enrichment lookup requires its matching structured database"}
	}
	if m.AddressFamily != family {
		return &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	return nil
}

func requireMembershipCapableMeta(core *reader.ImmutableReader) error {
	m := core.Meta()
	if m.ValueKind != format.ValueKindMembership && m.ValueKind != format.ValueKindStructured {
		return &Error{Code: ErrorWrongValueKind, Detail: "feed access requires a membership-capable database"}
	}
	return nil
}

// requireMembershipQueryMeta applies the strict membership-query gate:
// the query capability decodes address bitmaps, which only a membership
// database defines (Rust membership_query::Query::new; structured
// databases are refused).
func requireMembershipQueryMeta(core *reader.ImmutableReader) error {
	m := core.Meta()
	if m.ValueKind != format.ValueKindMembership {
		return &Error{Code: ErrorWrongValueKind, Detail: "membership query requires a membership database"}
	}
	return nil
}

func (r *ImmutableReader) requireDirect(family uint8) error {
	return requireDirectMeta(r.inner, family)
}

func (r *ImmutableReader) requireMembership(family uint8) error {
	return requireMembershipMeta(r.inner, family)
}

func (r *ImmutableReader) requireNetworkEnrichment(family uint8) error {
	return requireNetworkEnrichmentMeta(r.inner, family)
}

func (r *ImmutableReader) requireMembershipCapable() error {
	return requireMembershipCapableMeta(r.inner)
}

func (r *ImmutableReader) requireMembershipQuery() error {
	return requireMembershipQueryMeta(r.inner)
}

// pinHost is the minimal reader surface the shared Pin machinery needs.
// Both the immutable and the live public facades implement it, so one Pin
// implementation serves both readers (Rust: both reader surfaces expose
// the same pin-guarded lookup set). Pin lookups run against the core
// captured at pin creation; the host is retained only so Pin.Close can
// return the pin to its reader.
type pinHost interface {
	dropPin()
}

// core exposes the shared internal reader core to the pin machinery.
func (r *ImmutableReader) core() *reader.ImmutableReader { return r.inner }

// addPin registers one pin unless the reader already closed (the closed
// re-check makes the losing Pin race report WrongState instead of pinning
// a closed reader).
func (r *ImmutableReader) addPin() bool {
	r.sh.pins.Add(1)
	if r.sh.closed.Load() {
		r.sh.pins.Add(-1)
		return false
	}
	return true
}

// dropPin returns one pin to the reader.
func (r *ImmutableReader) dropPin() { r.sh.pins.Add(-1) }

// Close releases the mapping. A reader with live pins reports HandleBusy
// without closing; a second Close reports WrongState. Close must not race
// reader work.
func (r *ImmutableReader) Close() error {
	r.sh.closedState = true
	if !r.sh.closed.CompareAndSwap(false, true) {
		return &Error{Code: ErrorWrongState, Detail: "reader already closed"}
	}
	if r.sh.pins.Load() != 0 {
		r.sh.closed.Store(false)
		r.sh.closedState = false
		return &Error{Code: ErrorHandleBusy, Detail: "reader has live pins"}
	}
	return r.inner.Close()
}

// Pin registers one caller-owned lifetime pin. Pin once outside the
// workload: one pin may be shared across concurrent immutable lookups, and
// lookups through the pin allocate nothing and take no atomics. The reader
// cannot close while any pin exists (Close reports HandleBusy). Pin.Close
// must not race lookups using the pin.
//
// Pin is one caller-owned lifetime registration. Every Pin value refers to
// one shared private close state: pointer aliases (p2 := p1) and value
// copies (p2 := *p1) close the same logical pin, and the count is
// decremented exactly once. The first Close through any alias or copy
// succeeds; every later Close reports WrongState.
func (r *ImmutableReader) Pin() (*Pin, error) {
	if r.sh.closed.Load() {
		return nil, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	// A Close that raced this Pin either saw the added pin (HandleBusy) or
	// closed first; addPin's closed re-check makes the loser return
	// WrongState instead of pinning a closed reader.
	if !r.addPin() {
		return nil, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	return &Pin{st: &pinState{h: r, core: r.inner}}, nil
}

// pinState holds the one close flag shared by every alias and value copy
// of a single logical pin, plus the reader host the pin guards and the
// core captured at pin creation. The cached core is stable for the pin
// lifetime: a reader with live pins refuses Close, so the internal reader
// cannot close while any pin exists, and pin lookups therefore skip the
// per-call open check (Rust borrow parity: the pin holds the reader
// open). Enrichment-cursor pins leave core nil; only the five Pin lookup
// methods dereference it, and cursor views touch only the closed flag.
// Plain state: Pin.Close must not race pin operations.
type pinState struct {
	h      pinHost
	core   *reader.ImmutableReader
	closed bool
}

// Pin is one caller-owned lifetime registration. The pointer may be
// aliased and shared across goroutines; value copies (p2 := *p1) are also
// safe because every Pin value references the same private pinState.
// Create one Pin per goroutine or workload section and close it exactly
// once across all aliases and copies.
type Pin struct {
	st *pinState
}

// Close returns the pin to its reader. A second Close through any alias or
// copy reports WrongState.
func (p *Pin) Close() error {
	if p.st == nil {
		return &Error{Code: ErrorWrongState, Detail: "pin without a live reader"}
	}
	if p.st.closed {
		return &Error{Code: ErrorWrongState, Detail: "pin already closed"}
	}
	p.st.closed = true
	p.st.h.dropPin()
	return nil
}

func (p *Pin) checkOpen() error {
	if p.st == nil || p.st.closed {
		return &Error{Code: ErrorWrongState, Detail: "pin closed"}
	}
	return nil
}

// LookupDirectV4 returns the direct value covering ip, or false when absent.
func (r *ImmutableReader) LookupDirectV4(ip IPv4) (uint32, bool, error) {
	if err := r.checkOpen(); err != nil {
		return 0, false, err
	}
	if err := r.requireDirect(4); err != nil {
		return 0, false, err
	}
	value, found, err := r.inner.LookupDirect4(uint32(ip))
	if err != nil {
		return 0, false, publicError(err)
	}
	return value, found, nil
}

// LookupDirectV6 returns the direct value covering ip, or false when absent.
func (r *ImmutableReader) LookupDirectV6(ip IPv6) (uint32, bool, error) {
	if err := r.checkOpen(); err != nil {
		return 0, false, err
	}
	if err := r.requireDirect(6); err != nil {
		return 0, false, err
	}
	value, found, err := r.inner.LookupDirect6(ip.Hi, ip.Lo)
	if err != nil {
		return 0, false, publicError(err)
	}
	return value, found, nil
}

// DirectRangeV4 is one scanned IPv4 range record.
type DirectRangeV4 struct {
	From, To, Value uint32
}

// DirectRangeV6 is one scanned IPv6 range record.
type DirectRangeV6 struct {
	FromHi, FromLo, ToHi, ToLo uint64
	Value                      uint32
}

// DirectRangesV4 visits every committed IPv4 range record in ascending order.
// Errors returned by yield stop the scan and pass through unchanged.
func (r *ImmutableReader) DirectRangesV4(yield func(DirectRangeV4) error) error {
	if err := r.checkOpen(); err != nil {
		return err
	}
	if err := r.requireDirect(4); err != nil {
		return err
	}
	if err := r.inner.ScanDirect4(func(v reader.RangeVisit4) error {
		return yield(DirectRangeV4{From: v.From, To: v.To, Value: v.Value})
	}); err != nil {
		return publicError(err)
	}
	return nil
}

// DirectRangesV6 visits every committed IPv6 range record in ascending order.
// Errors returned by yield stop the scan and pass through unchanged.
func (r *ImmutableReader) DirectRangesV6(yield func(DirectRangeV6) error) error {
	if err := r.checkOpen(); err != nil {
		return err
	}
	if err := r.requireDirect(6); err != nil {
		return err
	}
	if err := r.inner.ScanDirect6(func(v reader.RangeVisit6) error {
		return yield(DirectRangeV6{FromHi: v.FromHi, FromLo: v.FromLo, ToHi: v.ToHi, ToLo: v.ToLo, Value: v.Value})
	}); err != nil {
		return publicError(err)
	}
	return nil
}

// Cardinality returns the exact inclusive address count of the selected
// generation.
func (r *ImmutableReader) Cardinality() (Cardinality129, error) {
	if err := r.checkOpen(); err != nil {
		return Cardinality129{}, err
	}
	value, err := r.inner.Cardinality()
	if err != nil {
		return Cardinality129{}, publicError(err)
	}
	return NewCardinality129(value.Bit128(), value.Hi(), value.Lo())
}

// FeedEntry is one catalog entry. The name is a copied entry
// (binary-format-v4.md: exact name lookup as copied {name,index}); the
// zero-allocation feed lookup is Pin.LookupFeedInto.
type FeedEntry struct {
	Index uint32
	Name  string
}

// LookupFeed returns the copied catalog entry for one exact feed name.
func (r *ImmutableReader) LookupFeed(name string) (FeedEntry, bool, error) {
	if err := r.checkOpen(); err != nil {
		return FeedEntry{}, false, err
	}
	// The name is validated before the kind check, mirroring
	// feed_catalog.rs, so an invalid name on a direct database reports
	// NameInvalid, not WrongValueKind.
	if !format.FeedNameValidString(name) {
		return FeedEntry{}, false, &Error{Code: ErrorNameInvalid, Detail: "invalid feed name"}
	}
	if err := r.requireMembershipCapable(); err != nil {
		return FeedEntry{}, false, err
	}
	entry, found, err := r.inner.LookupFeed(name)
	if err != nil {
		return FeedEntry{}, false, publicError(err)
	}
	if !found {
		return FeedEntry{}, false, nil
	}
	return FeedEntry{Index: entry.FeedIndex, Name: string(entry.Name)}, true, nil
}

// FeedInfo is the zero-allocation feed lookup result: the name bytes are
// written into the caller's buffer, and NameLen reports the canonical name
// length (also the required buffer size when BufferTooSmall is returned).
type FeedInfo struct {
	Index   uint32
	NameLen int
}

// LookupFeedInto writes the catalog entry name for one exact feed into dst.
// It performs no heap allocation. A dst shorter than the canonical name
// reports BufferTooSmall with the required size in FeedInfo.NameLen.
func (p *Pin) LookupFeedInto(name string, dst []byte) (FeedInfo, bool, error) {
	if err := p.checkOpen(); err != nil {
		return FeedInfo{}, false, err
	}
	if !format.FeedNameValidString(name) {
		return FeedInfo{}, false, &Error{Code: ErrorNameInvalid, Detail: "invalid feed name"}
	}
	if err := requireMembershipCapableMeta(p.st.core); err != nil {
		return FeedInfo{}, false, err
	}
	entry, found, err := p.st.core.LookupFeed(name)
	if err != nil {
		return FeedInfo{}, false, publicError(err)
	}
	if !found {
		return FeedInfo{}, false, nil
	}
	info := FeedInfo{Index: entry.FeedIndex, NameLen: len(entry.Name)}
	if len(dst) < len(entry.Name) {
		return info, false, &Error{Code: ErrorBufferTooSmall, Detail: "feed name buffer too small"}
	}
	copy(dst, entry.Name)
	return info, true, nil
}

// MetadataJSON returns the exact decompressed opaque metadata bytes. present
// is false when metadata is absent; empty bytes with present true are the
// distinct empty state.
func (r *ImmutableReader) MetadataJSON() ([]byte, bool, error) {
	if err := r.checkOpen(); err != nil {
		return nil, false, err
	}
	bytes, present, err := r.inner.ReadMetadataJSON()
	if err != nil {
		return nil, false, publicError(err)
	}
	return bytes, present, nil
}

// MembershipView exposes one canonical membership bitmap as a lightweight
// value. The view is valid while its pin remains open; no per-view release
// exists (decision 4A). A view derived from a closed pin reports WrongState.
// The zero MembershipView is inert and reports WrongState.
type MembershipView struct {
	// st is the immutable private pin state captured at view creation.
	// Storing *pinState instead of *Pin keeps the guard attached to the
	// logical pin: later reassignment of the Pin variable that created the
	// view (e.g. p = *otherPin) cannot retarget the guard to another
	// reader's lifetime state.
	st    *pinState
	inner reader.MembershipView
}

func (v MembershipView) check() error {
	if v.st == nil || v.st.closed {
		return &Error{Code: ErrorWrongState, Detail: "membership view without a live pin"}
	}
	return nil
}

// WordCount returns the canonical bitmap word count.
func (v MembershipView) WordCount() (uint32, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	return v.inner.WordCount(), nil
}

// Word returns word i of the bitmap, or false when out of range.
func (v MembershipView) Word(i uint32) (uint64, bool, error) {
	if err := v.check(); err != nil {
		return 0, false, err
	}
	word, ok, err := v.inner.Word(i)
	if err != nil {
		return 0, false, publicError(err)
	}
	return word, ok, nil
}

// ReadWords fills output with the sequential words starting at start and
// returns the copied count; start above the canonical length is
// InvalidArgument. The bitmap is never materialized: the output is the
// caller's buffer (binary-format-v4.md: caller-buffer batched word reads).
func (v MembershipView) ReadWords(start uint32, output []uint64) (int, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	count, err := v.inner.ReadWords(start, output)
	if err != nil {
		return 0, publicError(err)
	}
	return count, nil
}

// ContainsIndex reports whether the feed at feedIndex is a member. An index
// at or beyond this generation's feed index limit is InvalidArgument.
func (v MembershipView) ContainsIndex(feedIndex uint32) (bool, error) {
	if err := v.check(); err != nil {
		return false, err
	}
	has, err := v.inner.ContainsIndex(feedIndex)
	if err != nil {
		return false, publicError(err)
	}
	return has, nil
}

// LookupMembershipV4 returns the membership bitmap covering ip through the
// pin. Zero allocations, zero atomics; the view is valid while pin stays
// open.
func (p *Pin) LookupMembershipV4(ip IPv4) (MembershipView, bool, error) {
	if err := p.checkOpen(); err != nil {
		return MembershipView{}, false, err
	}
	if err := requireMembershipMeta(p.st.core, 4); err != nil {
		return MembershipView{}, false, err
	}
	view, found, err := p.st.core.LookupMembership4(uint32(ip))
	if err != nil {
		return MembershipView{}, false, publicError(err)
	}
	if !found {
		return MembershipView{}, false, nil
	}
	return MembershipView{st: p.st, inner: view}, true, nil
}

// LookupMembershipV6 returns the membership bitmap covering ip through the
// pin. Zero allocations, zero atomics; the view is valid while pin stays
// open.
func (p *Pin) LookupMembershipV6(ip IPv6) (MembershipView, bool, error) {
	if err := p.checkOpen(); err != nil {
		return MembershipView{}, false, err
	}
	if err := requireMembershipMeta(p.st.core, 6); err != nil {
		return MembershipView{}, false, err
	}
	view, found, err := p.st.core.LookupMembership6(ip.Hi, ip.Lo)
	if err != nil {
		return MembershipView{}, false, publicError(err)
	}
	if !found {
		return MembershipView{}, false, nil
	}
	return MembershipView{st: p.st, inner: view}, true, nil
}

// NetworkEnrichmentV1Location is the optional WGS84 location of one
// network-enrichment result, in millionths of a degree (Rust
// NetworkEnrichmentV1Location).
type NetworkEnrichmentV1Location struct {
	LatitudeMicrodegrees  int32
	LongitudeMicrodegrees int32
}

// NetworkEnrichmentV1 is one decoded network_enrichment_v1 payload.
//
// The approved parity matrix records the optional location as the value
// Location plus the presence flag HasLocation (decision 5A, ratified
// 2026-08-13; SOW decision log): with decision 4A's zero-allocation
// lookup contract a pointer inside a by-value result cannot reference
// stable storage without a per-call allocation, so Rust's
// Option<NetworkEnrichmentV1Location> is mirrored as a by-value struct.
// Names and fields match the matrix and the Rust authority.
type NetworkEnrichmentV1 struct {
	ASN         uint32
	CountryID   uint32
	StateID     uint32
	CityID      uint32
	Location    NetworkEnrichmentV1Location
	HasLocation bool
}

// NetworkEnrichmentV1View is a lightweight value exposing one structured
// entry. The view is valid while its pin remains open; no per-view release
// exists. The zero view reports WrongState.
type NetworkEnrichmentV1View struct {
	st    *pinState
	inner reader.NetworkEnrichmentV1View
}

func (v NetworkEnrichmentV1View) check() error {
	if v.st == nil || v.st.closed {
		return &Error{Code: ErrorWrongState, Detail: "enrichment view without a live pin"}
	}
	return nil
}

// Value returns the structured payload that was decoded at lookup.
func (v NetworkEnrichmentV1View) Value() (NetworkEnrichmentV1, error) {
	if err := v.check(); err != nil {
		return NetworkEnrichmentV1{}, err
	}
	payload, err := v.inner.Value()
	if err != nil {
		return NetworkEnrichmentV1{}, publicError(err)
	}
	return NetworkEnrichmentV1{
		ASN:       payload.ASN,
		CountryID: payload.CountryID,
		StateID:   payload.StateID,
		CityID:    payload.CityID,
		Location: NetworkEnrichmentV1Location{
			LatitudeMicrodegrees:  payload.LatitudeMicrodegrees,
			LongitudeMicrodegrees: payload.LongitudeMicrodegrees,
		},
		HasLocation: payload.Flags&format.NetworkEnrichmentV1HasLocation != 0,
	}, nil
}

// ThreatMembership returns the linked membership bitmap through the same
// pin. found=false with nil error reports the canonical absence result for
// a structured value without threat feeds (membership id zero), mirroring
// the Rust Option<MembershipView> return.
func (v NetworkEnrichmentV1View) ThreatMembership() (MembershipView, bool, error) {
	if err := v.check(); err != nil {
		return MembershipView{}, false, err
	}
	if v.inner.MembershipID() == 0 {
		return MembershipView{}, false, nil
	}
	view, err := v.inner.ThreatMembership()
	if err != nil {
		return MembershipView{}, false, publicError(err)
	}
	return MembershipView{st: v.st, inner: view}, true, nil
}

// LookupNetworkEnrichmentV1V4 returns the structured value covering ip
// through the pin. Zero allocations, zero atomics.
func (p *Pin) LookupNetworkEnrichmentV1V4(ip IPv4) (NetworkEnrichmentV1View, bool, error) {
	if err := p.checkOpen(); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	if err := requireNetworkEnrichmentMeta(p.st.core, 4); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := p.st.core.LookupNetworkEnrichmentV14(uint32(ip))
	if err != nil {
		return NetworkEnrichmentV1View{}, false, publicError(err)
	}
	if !found {
		return NetworkEnrichmentV1View{}, false, nil
	}
	return NetworkEnrichmentV1View{st: p.st, inner: view}, true, nil
}

// LookupNetworkEnrichmentV1V6 returns the structured value covering ip
// through the pin. Zero allocations, zero atomics.
func (p *Pin) LookupNetworkEnrichmentV1V6(ip IPv6) (NetworkEnrichmentV1View, bool, error) {
	if err := p.checkOpen(); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	if err := requireNetworkEnrichmentMeta(p.st.core, 6); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := p.st.core.LookupNetworkEnrichmentV16(ip.Hi, ip.Lo)
	if err != nil {
		return NetworkEnrichmentV1View{}, false, publicError(err)
	}
	if !found {
		return NetworkEnrichmentV1View{}, false, nil
	}
	return NetworkEnrichmentV1View{st: p.st, inner: view}, true, nil
}

// publicError converts internal typed errors into the public error type.
// Errors that are not typed format errors (for example an error returned by
// a scan callback) pass through unchanged and are never reinterpreted as
// database corruption.
func publicError(err error) error {
	if err == nil {
		return nil
	}
	var ferr *format.Error
	if errors.As(err, &ferr) {
		// A typed-nil internal error (for example a zero-valued
		// *format.Error carried inside a result struct) means "no
		// error"; mapping it to nil keeps the public boundary honest.
		if ferr == nil {
			return nil
		}
		return &Error{Code: ferr.Code, Detail: ferr.Detail}
	}
	// Internal header/decode validation failures (fixedsize header errors
	// and similar) are structural corruption at the public boundary.
	var herr *format.HeaderError
	if errors.As(err, &herr) {
		return &Error{Code: ErrorFormatInvalid, Detail: herr.Error()}
	}
	return err
}
