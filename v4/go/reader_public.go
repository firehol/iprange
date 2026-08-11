package iprangedb

import (
	"errors"
	"sync/atomic"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// This file is the milestone-1 public immutable reader facade. It reuses the
// verified scalar aliases (IPv4, IPv6, Cardinality129, ValueTag, ErrorCode)
// from the existing public package and adds the structured value kind, the
// missing error codes 65-69, and the reader surface. The old scalar files are
// deleted only when the reset lands (approved by the user after milestone-1
// evidence); until then the aliases are the transfer point.

// ValueKindStructured is the meta value-kind value for structured files.
const ValueKindStructured = ValueKind(3)

// Error codes 65-69 complete the exact 1-69 table. Codes 1-64 live in the
// existing errors.go; the milestone-0 review established that code 46's Go
// name is obsolete (the wire value is authoritative and unchanged).
const (
	ErrorFaultWorkerUnavailable ErrorCode = 65 + iota
	ErrorFaultWorkerFailed
	ErrorUnsupportedStructure
	ErrorWrongStructureKind
	ErrorStructureIDExhausted
)

// StructureKind selects the immutable hardcoded structure of a structured
// database (binary-format-v4.md section 9A).
type StructureKind uint8

const (
	StructureKindNone                StructureKind = 0
	StructureKindNetworkEnrichmentV1 StructureKind = 1
)

// DirectSemantic is the engine-defined semantic of one direct database,
// derived from its exact value tag.
type DirectSemantic uint8

const (
	DirectSemanticGeneric DirectSemantic = iota
	DirectSemanticFirstSeen
	DirectSemanticLastSeen
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

// shared is the reader-wide lifetime state. Lookups and scans on an open
// reader are concurrent-safe and synchronize nothing in their traversal
// (design-iprange-engine.md: concurrent lookups and independent scans without
// a per-call mutex, atomic, or active counter). The only shared state is the
// close flag and the public child-view count, and they are only ever touched
// at view boundaries and Close — the same guard layer the frozen Rust C ABI
// imposes around the sync-free engine (iprange-capi reader view handles).
// Callers must not race Close with reader work (spec:401).
type shared struct {
	closed atomic.Bool
	views  atomic.Int64 // live public views derived from this reader
}

// ImmutableReader is one opened immutable v4 database.
//
// Concurrent lookups and scans are safe. Close must not race reader work:
// call Close only after all concurrent operations joined. A reader with live
// views cannot close: Close returns ErrorHandleBusy until every view is
// released (binary-format-v4.md: handles are children of the reader). Any
// operation on a closed reader reports ErrorHandleClosed instead of crashing.
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

// ensureOpen guards every public entry point: after a successful Close the
// mapping is gone, and any later operation must report a typed HandleClosed
// error (the Rust borrow checker encodes this statically; the C ABI checks
// its handle registry per call — this single atomic load is the Go analog).
func (r *ImmutableReader) ensureOpen() error {
	if r.sh.closed.Load() {
		return &Error{Code: ErrorHandleClosed, Detail: "reader closed"}
	}
	return nil
}

// Close releases the mapping and shared lifetime lock. It returns
// ErrorHandleBusy while public views derived from this reader are still
// alive (release them first), ErrorHandleClosed after a successful close,
// and never unmaps while any view could still touch the mapping.
func (r *ImmutableReader) Close() error {
	if r.sh.closed.Swap(true) {
		return &Error{Code: ErrorHandleClosed, Detail: "reader already closed"}
	}
	if r.sh.views.Load() > 0 {
		r.sh.closed.Store(false)
		return &Error{Code: ErrorHandleBusy, Detail: "live views still held"}
	}
	if err := r.inner.Close(); err != nil {
		r.sh.closed.Store(false)
		return publicError(err)
	}
	return nil
}

// Info returns the public identity of the selected generation.
func (r *ImmutableReader) Info() ImmutableInfo {
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
	}
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

// LookupDirectV4 returns the direct value covering ip, or false when absent.
func (r *ImmutableReader) LookupDirectV4(ip IPv4) (uint32, bool, error) {
	if err := r.ensureOpen(); err != nil {
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
	if err := r.ensureOpen(); err != nil {
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
	if err := r.ensureOpen(); err != nil {
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
	if err := r.ensureOpen(); err != nil {
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
	if err := r.ensureOpen(); err != nil {
		return Cardinality129{}, err
	}
	value, err := r.inner.Cardinality()
	if err != nil {
		return Cardinality129{}, publicError(err)
	}
	return NewCardinality129(value.Bit128(), value.Hi(), value.Lo())
}

// FeedEntry is one catalog entry.
type FeedEntry struct {
	Index uint32
	Name  string
}

// LookupFeed returns the catalog entry for one exact feed name.
func (r *ImmutableReader) LookupFeed(name string) (FeedEntry, bool, error) {
	if err := r.ensureOpen(); err != nil {
		return FeedEntry{}, false, err
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

// MembershipView exposes one canonical membership bitmap. The view borrows
// the reader and keeps its mapping alive: Release returns the borrow (the
// reader then reports ErrorHandleClosed on the released value, and a reader
// with unreleased views reports ErrorHandleBusy on Close). The released
// state belongs to the view value: like the Rust borrow, a view must not be
// copied and used past its Release, and Release must not race the view's
// own operations. The zero MembershipView (absent membership) is inert.
type MembershipView struct {
	reader   *ImmutableReader
	released bool
	inner    reader.MembershipView
}

func (v *MembershipView) check() error {
	if v.reader == nil || v.released {
		return &Error{Code: ErrorHandleClosed, Detail: "view released or reader closed"}
	}
	return nil
}

// Release returns the view to its reader. Idempotent; pointer receiver so
// the released state lands on the caller's variable.
func (v *MembershipView) Release() {
	if v.reader == nil || v.released {
		return
	}
	v.released = true
	v.reader.sh.views.Add(-1)
}

// WordCount returns the canonical bitmap word count.
func (v *MembershipView) WordCount() uint32 {
	if err := v.check(); err != nil {
		return 0
	}
	return v.inner.WordCount()
}

// Word returns word i of the bitmap, or false when out of range.
func (v *MembershipView) Word(i uint32) (uint64, bool, error) {
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
func (v *MembershipView) ReadWords(start uint32, output []uint64) (int, error) {
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
func (v *MembershipView) ContainsIndex(feedIndex uint32) (bool, error) {
	if err := v.check(); err != nil {
		return false, err
	}
	has, err := v.inner.ContainsIndex(feedIndex)
	if err != nil {
		return false, publicError(err)
	}
	return has, nil
}

// needsRelease reports whether a found view must be released later.
func (r *ImmutableReader) holdView() {
	r.sh.views.Add(1)
}

// LookupMembershipV4 returns the membership bitmap covering ip. The view
// must be released when done; it keeps the mapping alive until then.
func (r *ImmutableReader) LookupMembershipV4(ip IPv4) (MembershipView, bool, error) {
	if err := r.ensureOpen(); err != nil {
		return MembershipView{}, false, err
	}
	if err := r.requireMembership(4); err != nil {
		return MembershipView{}, false, err
	}
	view, found, err := r.inner.LookupMembership4(uint32(ip))
	if err != nil {
		return MembershipView{}, false, publicError(err)
	}
	if !found {
		return MembershipView{}, false, nil
	}
	r.holdView()
	return MembershipView{reader: r, inner: view}, true, nil
}

// LookupMembershipV6 returns the membership bitmap covering ip. The view
// must be released when done; it keeps the mapping alive until then.
func (r *ImmutableReader) LookupMembershipV6(ip IPv6) (MembershipView, bool, error) {
	if err := r.ensureOpen(); err != nil {
		return MembershipView{}, false, err
	}
	if err := r.requireMembership(6); err != nil {
		return MembershipView{}, false, err
	}
	view, found, err := r.inner.LookupMembership6(ip.Hi, ip.Lo)
	if err != nil {
		return MembershipView{}, false, publicError(err)
	}
	if !found {
		return MembershipView{}, false, nil
	}
	r.holdView()
	return MembershipView{reader: r, inner: view}, true, nil
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

// NetworkEnrichmentV1View is a logical handle to one structured entry. Like
// MembershipView it borrows the reader: Release it when done. The zero view
// (absent entry) is inert.
type NetworkEnrichmentV1View struct {
	reader   *ImmutableReader
	released bool
	inner    reader.NetworkEnrichmentV1View
}

func (v *NetworkEnrichmentV1View) check() error {
	if v.reader == nil || v.released {
		return &Error{Code: ErrorHandleClosed, Detail: "view released or reader closed"}
	}
	return nil
}

// Release returns the view to its reader. Idempotent; pointer receiver so
// the released state lands on the caller's variable.
func (v *NetworkEnrichmentV1View) Release() {
	if v.reader == nil || v.released {
		return
	}
	v.released = true
	v.reader.sh.views.Add(-1)
}

// Value decodes the structured payload.
func (v *NetworkEnrichmentV1View) Value() (NetworkEnrichmentV1, error) {
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

// ThreatMembership returns the linked membership bitmap, or a zero view when
// the payload has no threat membership. The returned view is a separately
// held borrow: release it when done.
func (v *NetworkEnrichmentV1View) ThreatMembership() (MembershipView, error) {
	if err := v.check(); err != nil {
		return MembershipView{}, err
	}
	if v.inner.MembershipID() == 0 {
		return MembershipView{}, nil
	}
	view, err := v.inner.ThreatMembership()
	if err != nil {
		return MembershipView{}, publicError(err)
	}
	v.reader.holdView()
	return MembershipView{reader: v.reader, inner: view}, nil
}

// LookupNetworkEnrichmentV1V4 returns the structured value covering ip. The
// view must be released when done.
func (r *ImmutableReader) LookupNetworkEnrichmentV1V4(ip IPv4) (NetworkEnrichmentV1View, bool, error) {
	if err := r.ensureOpen(); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	if err := r.requireNetworkEnrichment(4); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := r.inner.LookupNetworkEnrichmentV14(uint32(ip))
	if err != nil {
		return NetworkEnrichmentV1View{}, false, publicError(err)
	}
	if !found {
		return NetworkEnrichmentV1View{}, false, nil
	}
	r.holdView()
	return NetworkEnrichmentV1View{reader: r, inner: view}, true, nil
}

// LookupNetworkEnrichmentV1V6 returns the structured value covering ip. The
// view must be released when done.
func (r *ImmutableReader) LookupNetworkEnrichmentV1V6(ip IPv6) (NetworkEnrichmentV1View, bool, error) {
	if err := r.ensureOpen(); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	if err := r.requireNetworkEnrichment(6); err != nil {
		return NetworkEnrichmentV1View{}, false, err
	}
	view, found, err := r.inner.LookupNetworkEnrichmentV16(ip.Hi, ip.Lo)
	if err != nil {
		return NetworkEnrichmentV1View{}, false, publicError(err)
	}
	if !found {
		return NetworkEnrichmentV1View{}, false, nil
	}
	r.holdView()
	return NetworkEnrichmentV1View{reader: r, inner: view}, true, nil
}

// MetadataJSON returns the exact decompressed opaque metadata bytes. present
// is false when metadata is absent; empty bytes with present true are the
// distinct empty state.
func (r *ImmutableReader) MetadataJSON() ([]byte, bool, error) {
	if err := r.ensureOpen(); err != nil {
		return nil, false, err
	}
	bytes, present, err := r.inner.ReadMetadataJSON()
	if err != nil {
		return nil, false, publicError(err)
	}
	return bytes, present, nil
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
		if e, ok := err.(*format.Error); ok {
			return &Error{Code: ErrorCode(e.Code), Detail: e.Detail}
		}
	}
	return err
}
