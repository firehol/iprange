// Name-based membership import surface (Rust live_writer
// membership_import.rs parity): BeginMembershipImport starts one
// complete import from an explicitly pinned membership reader (live or
// immutable) into the live writer's membership database; FinishInput
// streams the source catalog and ranges through the shared draft,
// translates every source membership through the import cache, unions
// the translated memberships into the preserved destination
// generation, and prepares the exact import report. Source failures
// abort the workflow as TransactionAborted and leave the writer
// reusable, exactly like the Rust abort_after_source arms.

package iprangedb

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

const importWordBatch = 64

// MembershipImportSource is the explicitly pinned membership reader of
// one import (Rust MembershipImportSource): an immutable or a live
// reader, pinned for the import duration by the caller's open handle.
// The zero value names no reader and is refused.
type MembershipImportSource struct {
	immutable *ImmutableReader
	live      *LiveReader
}

// MembershipImportSourceImmutable builds the immutable-reader variant
// (Rust MembershipImportSource::Immutable).
func MembershipImportSourceImmutable(r *ImmutableReader) MembershipImportSource {
	return MembershipImportSource{immutable: r}
}

// MembershipImportSourceLive builds the live-reader variant (Rust
// MembershipImportSource::Live): the import resolves the pinned
// generation through the live reader's open state, so a closing or
// closed reader reports WrongState before any page is touched.
func MembershipImportSourceLive(r *LiveReader) MembershipImportSource {
	return MembershipImportSource{live: r}
}

// MembershipImport is one complete name-based import awaiting
// FinishInput (Rust MembershipImport).
type MembershipImport struct {
	w            *LiveWriter
	source       membershipImportSource
	cancellation *CancellationToken
}

// membershipImportSource is the resolved pinned source facts of one
// import (Rust MembershipImportSource::Source).
type membershipImportSource struct {
	core                 *reader.ImmutableReader
	meta                 format.Meta
	membershipEntryCount uint64
	identity             live.FileIdentity
}

// membershipImportStats is the exact import accounting (Rust
// ImportStats).
type membershipImportStats struct {
	inputRecords          uint64
	inputAddresses        format.Cardinality129
	sourceFeeds           uint64
	matchedFeeds          uint64
	createdFeeds          uint64
	sourceMemberships     uint64
	translatedMemberships uint64
	comparison            writer.Comparison
}

// BeginMembershipImport starts one complete import from one pinned
// membership reader (Rust LiveWriter::begin_membership_import): the
// feed-workflow preconditions, the source resolution and compatibility
// proof (membership value kind, same family and value tag, a different
// local file), the cancellation checkpoint, and the membership workflow
// draft. The zero source and the same-file source are refused; the
// cancellation, wrong-kind, wrong-family, and wrong-tag arms fail
// before any draft.
func (w *LiveWriter) BeginMembershipImport(source MembershipImportSource, cancellation *CancellationToken) (*MembershipImport, error) {
	if w.lw == nil {
		return nil, &Error{Code: format.CodeWrongState, Detail: "writer is closed"}
	}
	if err := requireFeedWorkflowReady(w); err != nil {
		return nil, publicError(err)
	}
	src, err := resolveMembershipImportSource(source)
	if err != nil {
		return nil, publicError(err)
	}
	if err := requireCompatibleImportSource(w, &src); err != nil {
		return nil, publicError(err)
	}
	if err := cancellation.check(); err != nil {
		return nil, publicError(err)
	}
	if err := w.coreOf().BeginMembershipWorkflow(); err != nil {
		return nil, publicError(err)
	}
	return &MembershipImport{w: w, source: src, cancellation: cancellation}, nil
}

// resolveMembershipImportSource resolves the pinned reader core of the
// selected variant with the variant's open proof (Rust Source::new:
// the immutable core is unconditionally open, the live core requires
// the open state; the retained identity, info, and membership entry
// count are captured once).
func resolveMembershipImportSource(source MembershipImportSource) (membershipImportSource, error) {
	var core *reader.ImmutableReader
	switch {
	case source.immutable != nil:
		if err := source.immutable.checkOpen(); err != nil {
			return membershipImportSource{}, publicError(err)
		}
		core = source.immutable.inner
	case source.live != nil:
		if err := source.live.checkOpen(); err != nil {
			return membershipImportSource{}, publicError(err)
		}
		core = source.live.core()
	default:
		return membershipImportSource{}, &Error{Code: format.CodeInvalidArgument, Detail: "membership import source is empty"}
	}
	device, inode, err := core.FileIdentity()
	if err != nil {
		return membershipImportSource{}, publicError(err)
	}
	return membershipImportSource{
		core:                 core,
		meta:                 core.Meta(),
		membershipEntryCount: core.Meta().MembershipEntryCount,
		identity:             live.IdentityFromDeviceInode(device, inode),
	}, nil
}

// requireCompatibleImportSource proves the source and destination are
// a compatible pair (Rust require_compatible_source): the source must
// be a membership database of the same address family and value tag on
// a different local file.
func requireCompatibleImportSource(w *LiveWriter, source *membershipImportSource) error {
	if source.meta.ValueKind != format.ValueKindMembership {
		return &Error{Code: format.CodeWrongValueKind, Detail: "membership import requires a membership source"}
	}
	if source.meta.AddressFamily != w.coreOf().BaseInfo().AddressFamily {
		return &Error{Code: format.CodeWrongAddressFamily, Detail: "membership import source family differs"}
	}
	if source.meta.ValueTag != w.coreOf().BaseInfo().ValueTag {
		return &Error{Code: format.CodeWrongValueTag, Detail: "membership import source value tag differs"}
	}
	if source.identity == w.lw.MainIdentity() {
		return &Error{Code: format.CodeInvalidArgument, Detail: "membership import source and destination are the same file"}
	}
	return nil
}

// FinishInput imports the complete pinned source and prepares its exact
// report (Rust MembershipImport::finish_input over
// finish_import_state): the active proof, the catalog and range sweeps,
// the membership translation, the exact comparison, the membership
// workflow finalization, and the terminal handle.
func (i *MembershipImport) FinishInput() (*FinishedWorkflow, error) {
	w := i.w
	if !w.coreOf().WorkflowInputOpen() {
		return nil, &Error{Code: format.CodeWrongState, Detail: "membership import is not active"}
	}
	if err := w.healthy(); err != nil {
		return nil, publicError(err)
	}
	stats, err := importAllMembership(w, i.source, i.cancellation)
	if err != nil {
		return nil, publicError(err)
	}
	if err := w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		return edit.FinalizeMembershipWorkflow(i.cancellation.check)
	}); err != nil {
		return nil, w.abortAfter(err)
	}
	report, err := prepareMembershipImportReport(w, stats, i.cancellation)
	if err != nil {
		return nil, publicError(err)
	}
	out, err := completeFeedWorkflow(w, report, i.cancellation)
	if err != nil {
		return nil, publicError(err)
	}
	return out.bind(w), nil
}

// importAllMembership runs the complete import sweeps (Rust
// import_all): the catalog sweep, the family range sweep, the source
// count verification, and the cache release.
func importAllMembership(w *LiveWriter, source membershipImportSource, cancellation *CancellationToken) (*membershipImportStats, error) {
	cache := writer.NewImportCache()
	stats := &membershipImportStats{}
	if err := importCatalogMembership(w, source, cache, stats, cancellation); err != nil {
		return nil, publicError(err)
	}
	switch source.meta.AddressFamily {
	case format.AddressFamilyIPv4:
		if err := importRangesMembership4(w, source, cache, stats, cancellation); err != nil {
			return nil, publicError(err)
		}
	case format.AddressFamilyIPv6:
		if err := importRangesMembership6(w, source, cache, stats, cancellation); err != nil {
			return nil, publicError(err)
		}
	}
	if err := verifyImportSourceCounts(w, source, cache, stats); err != nil {
		return nil, publicError(err)
	}
	stats.sourceMemberships = cache.SourceCount()
	stats.translatedMemberships = cache.TranslatedCount()
	if err := w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		return edit.ReleaseImportCache(cache, cancellation.check)
	}); err != nil {
		return nil, w.abortAfter(err)
	}
	return stats, nil
}

// importCatalogMembership sweeps the source feed catalog (Rust
// import_catalog): every source feed is re-proven by name, the
// destination feed is ensured, and the feed index mapping is recorded.
func importCatalogMembership(w *LiveWriter, source membershipImportSource, cache *writer.ImportCache, stats *membershipImportStats, cancellation *CancellationToken) error {
	cursor, err := source.core.NewFeedCursor()
	if err != nil {
		return w.abortAfterSource(err)
	}
	for {
		if err := cancellation.check(); err != nil {
			return w.abortAfterSource(err)
		}
		feed, ok, err := cursor.Next()
		if err != nil {
			return w.abortAfterSource(err)
		}
		if !ok {
			return nil
		}
		created, err := importFeedMembership(w, source, cache, feed)
		if err != nil {
			return publicError(err)
		}
		if err := recordImportFeed(w, stats, created); err != nil {
			return publicError(err)
		}
	}
}

// importFeedMembership ensures the destination feed and records the
// source-to-destination index mapping (Rust import_feed).
func importFeedMembership(w *LiveWriter, source membershipImportSource, cache *writer.ImportCache, feed reader.FeedEntry) (bool, error) {
	if err := requireSourceFeedMembership(w, source, feed); err != nil {
		return false, publicError(err)
	}
	var created bool
	err := w.coreOf().Mutate(func(edit *writer.WriterEdit) error {
		destination, isNew, err := edit.EnsureFeed(string(feed.Name))
		if err != nil {
			return publicError(err)
		}
		created = isNew
		return edit.MapImportFeed(cache, writer.FeedEntry{Name: string(feed.Name), Index: feed.FeedIndex}, destination)
	})
	if err != nil {
		return false, w.abortAfter(err)
	}
	return created, nil
}

// requireSourceFeedMembership re-proves one source feed by name (Rust
// require_source_feed): the catalog index cursor and the name lookup
// must agree.
func requireSourceFeedMembership(w *LiveWriter, source membershipImportSource, feed reader.FeedEntry) error {
	byName, found, err := source.core.LookupFeedBytes(feed.Name)
	if err != nil {
		return w.abortAfterSource(err)
	}
	if found && byName.FeedIndex == feed.FeedIndex {
		return nil
	}
	return w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source feed catalog indexes disagree"})
}

// recordImportFeed accounts one source feed (Rust record_feed).
func recordImportFeed(w *LiveWriter, stats *membershipImportStats, created bool) error {
	next, err := importCheckedIncrement(stats.sourceFeeds, "source feed count")
	if err != nil {
		return w.abortAfterSource(err)
	}
	stats.sourceFeeds = next
	if created {
		next, err = importCheckedIncrement(stats.createdFeeds, "created feed count")
		if err != nil {
			return w.abortAfterSource(err)
		}
		stats.createdFeeds = next
		return nil
	}
	next, err = importCheckedIncrement(stats.matchedFeeds, "matched feed count")
	if err != nil {
		return w.abortAfterSource(err)
	}
	stats.matchedFeeds = next
	return nil
}

// importRangesMembership4 imports the IPv4 membership ranges (Rust
// import_ranges over Ipv4Key): each canonical source range is
// translated through the import cache and pushed into the ordered
// merge over the preserved destination.
func importRangesMembership4(w *LiveWriter, source membershipImportSource, cache *writer.ImportCache, stats *membershipImportStats, cancellation *CancellationToken) error {
	cursor, err := source.core.NewMembershipRangeCursor4()
	if err != nil {
		return w.abortAfterSource(err)
	}
	edit, err := w.coreOf().BindEdit()
	if err != nil {
		return w.abortAfter(err)
	}
	merge, err := edit.BeginImportMerge(cancellation.check)
	if err != nil {
		return w.abortAfter(publicError(err))
	}
	var previous reader.MembershipRange4
	havePrevious := false
	for {
		if err := cancellation.check(); err != nil {
			return w.abortAfterSource(err)
		}
		record, ok, err := cursor.Next()
		if err != nil {
			return w.abortAfterSource(err)
		}
		if !ok {
			comparison, err := edit.FinishImportMerge(merge, cancellation.check)
			if err != nil {
				return w.abortAfter(publicError(err))
			}
			stats.comparison = comparison
			return nil
		}
		if havePrevious {
			if err := requireCanonicalImportRange4(w, &previous, record); err != nil {
				return publicError(err)
			}
		}
		previous = record
		havePrevious = true
		id, words, err := translateImportMembership(edit, w, source, cache, record.Membership, cancellation)
		if err != nil {
			return publicError(err)
		}
		err = edit.PushImportRange(merge, tree.Key{Hi: uint64(record.From)}, tree.Key{Hi: uint64(record.To)}, id, words, cancellation.check)
		if err != nil {
			return w.abortAfter(publicError(err))
		}
		cardinality, err := format.IPv4Inclusive(record.From, record.To)
		if err != nil {
			return w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "IPv4 interval cardinality"})
		}
		if err := recordImportInputRange(w, stats, cardinality); err != nil {
			return publicError(err)
		}
		work.RangeConsumed(1)
		previous = record
	}
}

// importRangesMembership6 imports the IPv6 membership ranges (Rust
// import_ranges over Ipv6Key).
func importRangesMembership6(w *LiveWriter, source membershipImportSource, cache *writer.ImportCache, stats *membershipImportStats, cancellation *CancellationToken) error {
	cursor, err := source.core.NewMembershipRangeCursor6()
	if err != nil {
		return w.abortAfterSource(err)
	}
	edit, err := w.coreOf().BindEdit()
	if err != nil {
		return w.abortAfter(err)
	}
	merge, err := edit.BeginImportMerge(cancellation.check)
	if err != nil {
		return w.abortAfter(publicError(err))
	}
	var previous reader.MembershipRange6
	havePrevious := false
	for {
		if err := cancellation.check(); err != nil {
			return w.abortAfterSource(err)
		}
		record, ok, err := cursor.Next()
		if err != nil {
			return w.abortAfterSource(err)
		}
		if !ok {
			comparison, err := edit.FinishImportMerge(merge, cancellation.check)
			if err != nil {
				return w.abortAfter(publicError(err))
			}
			stats.comparison = comparison
			return nil
		}
		if havePrevious {
			if err := requireCanonicalImportRange6(w, &previous, record); err != nil {
				return publicError(err)
			}
		}
		previous = record
		havePrevious = true
		id, words, err := translateImportMembership(edit, w, source, cache, record.Membership, cancellation)
		if err != nil {
			return publicError(err)
		}
		err = edit.PushImportRange(merge, tree.Key{Hi: record.FromHi, Lo: record.FromLo}, tree.Key{Hi: record.ToHi, Lo: record.ToLo}, id, words, cancellation.check)
		if err != nil {
			return w.abortAfter(publicError(err))
		}
		cardinality, err := format.IPv6Inclusive(record.FromHi, record.FromLo, record.ToHi, record.ToLo)
		if err != nil {
			return w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "IPv6 interval cardinality"})
		}
		if err := recordImportInputRange(w, stats, cardinality); err != nil {
			return publicError(err)
		}
		work.RangeConsumed(1)
		previous = record
	}
}

// translateImportMembership resolves one source membership token into
// its destination membership pair (Rust translate_membership over
// TranslatedMembership) over the caller's single edit binding: the
// cache fast paths first, then the source bitmap word sweep through
// the feed map, then the dictionary intern and translation record; the
// returned pair feeds the import merge exactly like Rust
// push_import_range, so the merge never re-resolves a translation.
// Binding once per import mirrors the Rust borrowed &mut WriterEdit:
// per-record Mutate bindings would allocate a fresh draft store for
// every dictionary step of every record.
func translateImportMembership(edit *writer.WriterEdit, w *LiveWriter, source membershipImportSource, cache *writer.ImportCache, sourceMembership uint32, cancellation *CancellationToken) (id, words uint32, err error) {
	if id, words, ok := cache.LastTranslation(sourceMembership); ok {
		return id, words, nil
	}
	id, words, present, err := edit.CachedImportMembership(cache, sourceMembership)
	if err != nil {
		return 0, 0, w.abortAfter(publicError(err))
	}
	if present {
		return id, words, nil
	}
	view, err := source.core.LookupMembershipID(sourceMembership)
	if err != nil {
		return 0, 0, w.abortAfterSource(err)
	}
	wordsSet := writer.NewImportWords()
	sourceWords := view.WordCount()
	var start uint32
	var buffer [importWordBatch]uint64
	for start < sourceWords {
		expected := sourceWords - start
		if expected > importWordBatch {
			expected = importWordBatch
		}
		read, err := view.ReadWords(start, buffer[:expected])
		if err != nil {
			return 0, 0, w.abortAfterSource(err)
		}
		if read != int(expected) {
			return 0, 0, w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source membership read ended early"})
		}
		missing, err := edit.MapImportWordBatch(cache, wordsSet, start, buffer[:expected], cancellation.check)
		if err != nil {
			return 0, 0, w.abortAfter(publicError(err))
		}
		if missing {
			return 0, 0, w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source membership names an inactive feed index"})
		}
		next, err := importCheckedAdd32(start, expected, "word index")
		if err != nil {
			return 0, 0, w.abortAfterSource(err)
		}
		start = next
	}
	if wordsSet.WordCount() == 0 {
		return 0, 0, w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source membership is empty"})
	}
	id, words, err = edit.FinishImportMembership(cache, sourceMembership, wordsSet, cancellation.check)
	if err != nil {
		return 0, 0, w.abortAfter(publicError(err))
	}
	return id, words, nil
}

// requireCanonicalImportRange4 refuses non-canonical source ranges
// (Rust require_canonical_source_range over Ipv4Key): reversed bounds
// or an overlapping, abutting-same-membership, or out-of-order
// predecessor is corrupt.
func requireCanonicalImportRange4(w *LiveWriter, previous *reader.MembershipRange4, current reader.MembershipRange4) error {
	invalid := current.From > current.To
	if !invalid && previous != nil {
		next := uint64(previous.To) + 1
		invalid = previous.From >= current.From ||
			uint64(previous.To) >= uint64(current.From) ||
			(previous.Membership == current.Membership && next <= uint64(^uint32(0)) && uint32(next) == current.From)
	}
	if invalid {
		return w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source membership ranges are not canonical"})
	}
	return nil
}

// requireCanonicalImportRange6 refuses non-canonical source ranges
// (Rust require_canonical_source_range over Ipv6Key).
func requireCanonicalImportRange6(w *LiveWriter, previous *reader.MembershipRange6, current reader.MembershipRange6) error {
	invalid := current.FromHi > current.ToHi || (current.FromHi == current.ToHi && current.FromLo > current.ToLo)
	if !invalid && previous != nil {
		previousLess := previous.FromHi < current.FromHi || (previous.FromHi == current.FromHi && previous.FromLo < current.FromLo)
		previousToLess := previous.ToHi < current.FromHi || (previous.ToHi == current.FromHi && previous.ToLo < current.FromLo)
		nextHi, nextLo, hasNext := importNext6(previous.ToHi, previous.ToLo)
		invalid = !previousLess || !previousToLess ||
			(previous.Membership == current.Membership && hasNext && nextHi == current.FromHi && nextLo == current.FromLo)
	}
	if invalid {
		return w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source membership ranges are not canonical"})
	}
	return nil
}

// recordImportInputRange accounts one source range and its address
// count (Rust record_input_range).
func recordImportInputRange(w *LiveWriter, stats *membershipImportStats, cardinality format.Cardinality129) error {
	next, err := importCheckedIncrement(stats.inputRecords, "source range record count")
	if err != nil {
		return w.abortAfterSource(err)
	}
	stats.inputRecords = next
	stats.inputAddresses, err = stats.inputAddresses.Add(cardinality)
	if err != nil {
		return w.abortAfterSource(&format.Error{Code: format.CodeArithmeticOverflow, Detail: "source addresses"})
	}
	return nil
}

// verifyImportSourceCounts proves the source sweep was complete (Rust
// verify_source_counts): every source feed and range record must have
// been imported, and every source membership must have been
// translated.
func verifyImportSourceCounts(w *LiveWriter, source membershipImportSource, cache *writer.ImportCache, stats *membershipImportStats) error {
	feedSum, err := importCheckedAdd(stats.matchedFeeds, stats.createdFeeds, "source feeds")
	if err != nil {
		return w.abortAfterSource(err)
	}
	valid := stats.sourceFeeds == source.meta.ActiveFeedCount &&
		feedSum == stats.sourceFeeds &&
		stats.inputRecords == source.meta.RangeRecordCount &&
		cache.SourceCount() == source.membershipEntryCount
	if !valid {
		return w.abortAfterSource(&format.Error{Code: format.CodeFormatInvalid, Detail: "source membership counts disagree"})
	}
	return nil
}

// prepareMembershipImportReport builds the exact import report (Rust
// membership_import/report.rs): the six-way before/after classifica-
// tion, the logical change (any created feed or any map change), and
// the source facts.
func prepareMembershipImportReport(w *LiveWriter, stats *membershipImportStats, cancellation *CancellationToken) (*WorkflowReport, error) {
	before := w.coreOf().BaseInfo()
	after := w.coreOf().Draft().Meta()
	if err := cancellation.check(); err != nil {
		return nil, w.abortAfter(err)
	}
	logical := classifyComparison(stats.comparison)
	if stats.createdFeeds != 0 || logical == LogicalChanged {
		logical = LogicalChanged
	}
	return &WorkflowReport{
		Workflow:                      WorkflowMembershipImport,
		LogicalChange:                 logical,
		InputRecordCount:              stats.inputRecords,
		InputNormalizedIntervalCount:  stats.inputRecords,
		BeforeRangeRecordCount:        before.RangeRecordCount,
		AfterRangeRecordCount:         after.RangeRecordCount,
		InputAddresses:                stats.inputAddresses,
		BeforeAddresses:               stats.comparison.Before,
		AfterAddresses:                stats.comparison.After,
		UnchangedValueAddresses:       stats.comparison.Unchanged,
		ChangedValueAddresses:         stats.comparison.Changed,
		AddedAddresses:                stats.comparison.Added,
		RemovedAddresses:              stats.comparison.Removed,
		SourceFeedCount:               stats.sourceFeeds,
		MatchedFeedCount:              stats.matchedFeeds,
		CreatedFeedCount:              stats.createdFeeds,
		SourceDistinctMembershipCount: stats.sourceMemberships,
		TranslatedMembershipCount:     stats.translatedMemberships,
	}, nil
}

// importNext6 advances one v6 bound (Rust Ipv6Key::checked_next).
func importNext6(hi, lo uint64) (uint64, uint64, bool) {
	if lo == ^uint64(0) {
		if hi == ^uint64(0) {
			return 0, 0, false
		}
		return hi + 1, 0, true
	}
	return hi, lo + 1, true
}

func importCheckedIncrement(value uint64, label string) (uint64, error) {
	if value == ^uint64(0) {
		return 0, &Error{Code: format.CodeArithmeticOverflow, Detail: label}
	}
	return value + 1, nil
}

func importCheckedAdd(left, right uint64, label string) (uint64, error) {
	sum := left + right
	if sum < left {
		return 0, &Error{Code: format.CodeArithmeticOverflow, Detail: label}
	}
	return sum, nil
}

func importCheckedAdd32(left, right uint32, label string) (uint32, error) {
	sum := uint64(left) + uint64(right)
	if sum > uint64(^uint32(0)) {
		return 0, &Error{Code: format.CodeArithmeticOverflow, Detail: label}
	}
	return uint32(sum), nil
}
