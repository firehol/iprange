package iprangedb

import (
	"errors"
	"sync/atomic"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// This file is the milestone-1 public immutable reader facade: a pinned
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

// ImmutableInfo is the public logical identity of the selected generation.
// ValueTag carries the exact 16 raw wire bytes (binary-format-v4.md section
// 4: at most 15 non-NUL bytes, then a mandatory NUL).
type ImmutableInfo struct {
	Family           AddressFamily
	ValueKind        ValueKind
	StructureKind    StructureKind
	ValueTag         [16]byte
	DatabaseID       [16]byte
	TransactionID    uint64
	CommitNonce      [16]byte
	PageCount        uint64
	RangeRecordCount uint64
	ActiveFeedCount  uint64
	MetaSelection    MetaSelection
}

// DirectSemantic returns the tag-derived semantic for direct databases.
func (i ImmutableInfo) DirectSemantic() (DirectSemantic, bool) {
	if i.ValueKind != ValueKindDirect {
		return DirectSemanticGeneric, false
	}
	switch i.ValueTag {
	case firstSeenTag:
		return DirectSemanticFirstSeen, true
	case lastSeenTag:
		return DirectSemanticLastSeen, true
	default:
		return DirectSemanticGeneric, true
	}
}

var (
	firstSeenTag = [16]byte{0x66, 0x69, 0x72, 0x73, 0x74, 0x5f, 0x73, 0x65, 0x65, 0x6e}
	lastSeenTag  = [16]byte{0x6c, 0x61, 0x73, 0x74, 0x5f, 0x73, 0x65, 0x65, 0x6e}
)

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
// Reader-level operations (Info, direct lookups and scans, cardinality,
// feed lookup, metadata) are zero-allocation and take no atomics; they
// report WrongState when the reader is closed. Callers must not race Close
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
func (r *ImmutableReader) Info() (ImmutableInfo, error) {
	if err := r.checkOpen(); err != nil {
		return ImmutableInfo{}, err
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
	return ImmutableInfo{
		Family:           AddressFamily(meta.AddressFamily),
		ValueKind:        ValueKind(meta.ValueKind),
		StructureKind:    StructureKind(meta.StructureKind),
		ValueTag:         meta.ValueTag,
		DatabaseID:       meta.DatabaseID,
		TransactionID:    meta.TxnID,
		CommitNonce:      meta.CommitNonce,
		PageCount:        meta.PageCount,
		RangeRecordCount: meta.RangeRecordCount,
		ActiveFeedCount:  meta.ActiveFeedCount,
		MetaSelection:    selection,
	}, nil
}

// The four require* helpers mirror the Rust reader pre-checks exactly:
// wrong kind and wrong family are reported before any page is touched
// (reader_core/generation.rs require_direct/require_membership_family,
// membership_view.rs require_kind, structured_value/view.rs require_kind,
// feed_catalog.rs require_membership).

func (r *ImmutableReader) requireDirect(family uint8) error {
	m := r.inner.Meta()
	if m.ValueKind != format.ValueKindDirect {
		return &Error{Code: ErrorWrongValueKind, Detail: "direct lookup requires a direct-value database"}
	}
	if m.AddressFamily != family {
		return &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	return nil
}

func (r *ImmutableReader) requireMembership(family uint8) error {
	m := r.inner.Meta()
	if m.ValueKind != format.ValueKindMembership {
		return &Error{Code: ErrorWrongValueKind, Detail: "membership lookup requires a membership database"}
	}
	if m.AddressFamily != family {
		return &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	return nil
}

func (r *ImmutableReader) requireNetworkEnrichment(family uint8) error {
	m := r.inner.Meta()
	if m.ValueKind != format.ValueKindStructured || m.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return &Error{Code: ErrorWrongStructureKind, Detail: "network enrichment lookup requires its matching structured database"}
	}
	if m.AddressFamily != family {
		return &Error{Code: ErrorWrongAddressFamily, Detail: "lookup address family does not match the database"}
	}
	return nil
}

func (r *ImmutableReader) requireMembershipCapable() error {
	m := r.inner.Meta()
	if m.ValueKind != format.ValueKindMembership && m.ValueKind != format.ValueKindStructured {
		return &Error{Code: ErrorWrongValueKind, Detail: "feed access requires a membership-capable database"}
	}
	return nil
}

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
	r.sh.pins.Add(1)
	// A Close that raced this Pin either saw the added pin (HandleBusy) or
	// closed first; the second check makes the loser return WrongState
	// instead of pinning a closed reader.
	if r.sh.closed.Load() {
		r.sh.pins.Add(-1)
		return nil, &Error{Code: ErrorWrongState, Detail: "reader closed"}
	}
	return &Pin{st: &pinState{r: r}}, nil
}

// pinState holds the one close flag shared by every alias and value copy
// of a single logical pin. Plain state: Pin.Close must not race pin
// operations.
type pinState struct {
	r      *ImmutableReader
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
	if p.st.closed {
		return &Error{Code: ErrorWrongState, Detail: "pin already closed"}
	}
	p.st.closed = true
	p.st.r.sh.pins.Add(-1)
	return nil
}

func (p *Pin) checkOpen() error {
	if p.st.closed {
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
	if err := p.st.r.requireMembershipCapable(); err != nil {
		return FeedInfo{}, false, err
	}
	entry, found, err := p.st.r.inner.LookupFeed(name)
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
	p     *Pin
	inner reader.MembershipView
}

func (v MembershipView) check() error {
	if v.p == nil || v.p.st.closed {
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
	if err := p.st.r.requireMembership(4); err != nil {
		return MembershipView{}, false, err
	}
	view, found, err := p.st.r.inner.LookupMembership4(uint32(ip))
	if err != nil {
		return MembershipView{}, false, publicError(err)
	}
	if !found {
		return MembershipView{}, false, nil
	}
	return MembershipView{p: p, inner: view}, true, nil
}

// LookupMembershipV6 returns the membership bitmap covering ip through the
// pin. Zero allocations, zero atomics; the view is valid while pin stays
// open.
func (p *Pin) LookupMembershipV6(ip IPv6) (MembershipView, bool, error) {
	if err := p.checkOpen(); err != nil {
		return MembershipView{}, false, err
	}
	if err := p.st.r.requireMembership(6); err != nil {
		return MembershipView{}, false, err
	}
	view, found, err := p.st.r.inner.LookupMembership6(ip.Hi, ip.Lo)
	if err != nil {
		return MembershipView{}, false, publicError(err)
	}
	if !found {
		return MembershipView{}, false, nil
	}
	return MembershipView{p: p, inner: view}, true, nil
}

// NetworkEnrichmentV1 is one decoded network_enrichment_v1 payload.
type NetworkEnrichmentV1 struct {
	ASN                   uint32
	CountryID             uint32
	StateID               uint32
	CityID                uint32
	LatitudeMicrodegrees  int32
	LongitudeMicrodegrees int32
	HasLocation           bool
}

// NetworkEnrichmentV1View is a lightweight value exposing one structured
// entry. The view is valid while its pin remains open; no per-view release
// exists. The zero view reports WrongState.
type NetworkEnrichmentV1View struct {
	p     *Pin
	inner reader.NetworkEnrichmentV1View
}

func (v NetworkEnrichmentV1View) check() error {
	if v.p == nil || v.p.st.closed {
		return &Error{Code: ErrorWrongState, Detail: "enrichment view without a live pin"}
	}
	return nil
}

// Value decodes the structured payload.
func (v NetworkEnrichmentV1View) Value() (NetworkEnrichmentV1, error) {
	if err := v.check(); err != nil {
		return NetworkEnrichmentV1{}, err
	}
	payload, err := v.inner.Value()
	if err != nil {
		return NetworkEnrichmentV1{}, publicError(err)
	}
	return NetworkEnrichmentV1{
		ASN:                   payload.ASN,
		CountryID:             payload.CountryID,
		StateID:               payload.StateID,
		CityID:                payload.CityID,
		LatitudeMicrodegrees:  payload.LatitudeMicrodegrees,
		LongitudeMicrodegrees: payload.LongitudeMicrodegrees,
		HasLocation:           payload.Flags&format.NetworkEnrichmentV1HasLocation != 0,
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
	return MembershipView{p: v.p, inner: view}, true, nil
}

// LookupNetworkEnrichmentV1V4 returns the structured value covering ip
// through the pin. Zero allocations, zero atomics.
func (p *Pin) LookupNetworkEnrichmentV1V4(ip IPv4) (NetworkEnrichmentV1View, bool, error) {
	if err := p.checkOpen(); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	if err := p.st.r.requireNetworkEnrichment(4); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := p.st.r.inner.LookupNetworkEnrichmentV14(uint32(ip))
	if err != nil {
		return NetworkEnrichmentV1View{}, false, publicError(err)
	}
	if !found {
		return NetworkEnrichmentV1View{}, false, nil
	}
	return NetworkEnrichmentV1View{p: p, inner: view}, true, nil
}

// LookupNetworkEnrichmentV1V6 returns the structured value covering ip
// through the pin. Zero allocations, zero atomics.
func (p *Pin) LookupNetworkEnrichmentV1V6(ip IPv6) (NetworkEnrichmentV1View, bool, error) {
	if err := p.checkOpen(); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	if err := p.st.r.requireNetworkEnrichment(6); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := p.st.r.inner.LookupNetworkEnrichmentV16(ip.Hi, ip.Lo)
	if err != nil {
		return NetworkEnrichmentV1View{}, false, publicError(err)
	}
	if !found {
		return NetworkEnrichmentV1View{}, false, nil
	}
	return NetworkEnrichmentV1View{p: p, inner: view}, true, nil
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
