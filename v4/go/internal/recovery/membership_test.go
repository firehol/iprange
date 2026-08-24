package recovery

// Recovery membership analysis tests (Rust recovery/membership_tests
// arms): the ID tree count, the inline and blob membership validation
// against the digest and the feed catalog, and the accepted proof.

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// membershipFeedLimit is the shared feed-index limit of the
// membership tests (the wide bitmaps reference feeds up to word 799).
const membershipFeedLimit = 52_000

// membershipWordCount is the fixed word count of one wide membership
// bitmap of the tests (4008-byte blob payload with a branch root).
const membershipWordCount = 800

// wideBitmap builds one canonical bitmap exercising the first, a
// middle, and the last word (Rust membership_tests wide_words peer).
func wideBitmap() writer.OutputWords {
	words := make(writer.OutputWords, membershipWordCount)
	words[0] = 1 << 3
	words[499] = 1 << 63
	words[membershipWordCount-1] = 1 << 1
	return words
}

// membershipSource builds one membership source with the given
// (name, index) feeds and membership ranges and returns the committed
// meta.
func membershipSource(t *testing.T, path string, feeds [][2]any, ranges []membershipRange) format.Meta {
	return membershipSourceLimit(t, path, membershipFeedLimit, feeds, ranges)
}

// membershipSourceLimit builds one membership source with the given
// feed-index limit and returns the committed meta.
func membershipSourceLimit(t *testing.T, path string, feedLimit uint64, feeds [][2]any, ranges []membershipRange) format.Meta {
	t.Helper()
	builder, err := writer.NewOutputBuilder(path, membershipSourceSpec(feedLimit), writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	for _, pair := range feeds {
		name := pair[0].(string)
		index := pair[1].(uint32)
		if err := builder.PushFeed(name, index); err != nil {
			t.Fatalf("PushFeed(%s): %v", name, err)
		}
	}
	for _, item := range ranges {
		if err := builder.PushMembershipV4(item.from, item.to, item.words); err != nil {
			t.Fatalf("PushMembershipV4: %v", err)
		}
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return meta
}

// membershipRange is one membership push of the test sources.
type membershipRange struct {
	from, to uint32
	words    writer.OutputWords
}

// prepareMembershipRecovery counts and sizes the membership recovery
// passes of one source (Rust prepare_tables parity: count, reset, and
// the table store).
func prepareMembershipRecovery(t *testing.T, source *mapping.Mapping, meta format.Meta) (*pageSet, *tableStore) {
	t.Helper()
	budget := recoveryBudget(1 << 22)
	pages, err := forRecovery(budget.MaxHeapBytes/2, meta.PageCount, meta, budget)
	if err != nil {
		t.Fatalf("page set: %v", err)
	}
	memberships, err := membershipCount(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("membershipCount: %v", err)
	}
	catalogCount, err := catalogCount(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("catalogCount: %v", err)
	}
	if err := pages.reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	tables, err := allocateTables(tableCounts{catalog: catalogCount, memberships: memberships}, pages, budget, 0)
	if err != nil {
		t.Fatalf("allocateTables: %v", err)
	}
	return pages, tables
}

func TestRecoverMembershipsValidatesInlineAndBlobBitmaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	// The source mirrors the Rust clean-recovery test shape: two wide
	// pushes share one dictionary record (deduplicated), one inline
	// push adds a second record, and the wide bitmap exercises feeds
	// across three words including the last word.
	wide := wideBitmap()
	meta := membershipSource(t, path, [][2]any{{"alpha", uint32(3)}, {"middle", uint32(31_999)}, {"omega", uint32(51_137)}}, []membershipRange{
		{from: 0, to: 9, words: wide},
		{from: 20, to: 29, words: writer.OutputWords{1 << 3}},
		{from: 40, to: 49, words: wide},
	})
	source := mapSource(t, path)
	defer source.Close()
	pages, tables := prepareMembershipRecovery(t, source, meta)

	rep := newReporter(nil)
	catalogRecovered, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	recovered, err := recoverMemberships(source, meta, catalogRecovered, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverMemberships: %v", err)
	}
	report := rep.finish()
	if report.MembershipEntries.Examined != 2 || report.MembershipEntries.Accepted != 2 || report.MembershipEntries.Rejected != 0 {
		t.Fatalf("membership counts %+v, want 2/2/0", report.MembershipEntries)
	}
	for index := uint64(0); index < recovered.recordsLen(); index++ {
		entry, err := recovered.record(tables, index)
		if err != nil {
			t.Fatalf("record %d: %v", index, err)
		}
		if entry.rejected {
			t.Fatalf("record %d rejected", index)
		}
	}
	// The writer interns dictionary IDs from 1 (ID 0 is reserved), so
	// the wide record is ID 1 and the inline record is ID 2.
	first, found, err := recovered.get(tables, 1)
	if err != nil || !found {
		t.Fatalf("membership id 1 found=%v err=%v", found, err)
	}
	if first.wordCount != membershipWordCount {
		t.Fatalf("membership id 1 word count %d, want %d", first.wordCount, membershipWordCount)
	}
	if first.storage != format.MembershipStorageBlob {
		t.Fatalf("membership id 1 storage %v, want blob", first.storage)
	}
	second, found, err := recovered.get(tables, 2)
	if err != nil || !found {
		t.Fatalf("membership id 2 found=%v err=%v", found, err)
	}
	if second.wordCount != 1 {
		t.Fatalf("membership id 2 word count %d, want 1", second.wordCount)
	}
	if second.storage != format.MembershipStorageInline {
		t.Fatalf("membership id 2 storage %v, want inline", second.storage)
	}
}

// TestRecoverMembershipsMultiLevelBlobWords recovers one membership
// whose bitmap overflows a full branch level (226 blob leaves) and
// reads back words across every blob level (Rust blob_tree: the
// branch-over-branch walk through find_leaf and select_branch).
func TestRecoverMembershipsMultiLevelBlobWords(t *testing.T) {
	const (
		wordCount = 114_356 // 226 leaves: 225 fill one branch level
		feedLimit = 8_000_000
	)
	words := make(writer.OutputWords, wordCount)
	words[0] = 1 << 3
	words[55_999] = 1 << 63
	words[wordCount-1] = 1 << 63
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := membershipSourceLimit(t, path, feedLimit, [][2]any{
		{"alpha", uint32(3)},
		{"middle", uint32(3_583_999)},
		{"omega", uint32(7_318_783)},
	}, []membershipRange{{from: 0, to: 9, words: words}})
	source := mapSource(t, path)
	defer source.Close()
	pages, tables := prepareMembershipRecovery(t, source, meta)
	rep := newReporter(nil)
	catalogRecovered, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	recovered, err := recoverMemberships(source, meta, catalogRecovered, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverMemberships: %v", err)
	}
	report := rep.finish()
	if report.MembershipEntries.Examined != 1 || report.MembershipEntries.Accepted != 1 || report.MembershipEntries.Rejected != 0 {
		t.Fatalf("membership counts %+v, want 1/1/0", report.MembershipEntries)
	}
	entry, found, err := recovered.get(tables, 1)
	if err != nil || !found {
		t.Fatalf("membership id 1 found=%v err=%v", found, err)
	}
	if entry.wordCount != wordCount || entry.storage != format.MembershipStorageBlob {
		t.Fatalf("membership id 1 word count %d storage %v, want %d blob", entry.wordCount, entry.storage, wordCount)
	}
	reader := membershipWordReader{m: source, meta: meta, locator: entry}
	var got [1]uint64
	for start, want := range map[uint32]uint64{0: 1 << 3, 55_999: 1 << 63, uint32(wordCount - 1): 1 << 63} {
		if err := reader.readWords(start, got[:]); err != nil {
			t.Fatalf("readWords(%d): %v", start, err)
		}
		if got[0] != want {
			t.Fatalf("word %d = %#x, want %#x", start, got[0], want)
		}
	}
}

// blobRootFinder captures the first blob-storage record of the
// membership ID tree (test helper).
type blobRootFinder struct {
	root uint32
}

func (f *blobRootFinder) pageAccepted() error { return nil }
func (f *blobRootFinder) pageRejected(ioUnreadable bool) error {
	return nil
}
func (f *blobRootFinder) unknown(reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error {
	return nil
}
func (f *blobRootFinder) leaf(page uint32, index int, cell []byte, ok bool) error {
	if !ok {
		return nil
	}
	record, err := format.DecodeMembershipRecord(cell)
	if err != nil {
		return nil
	}
	if record.Storage == format.MembershipStorageBlob && f.root == 0 {
		f.root = record.BlobRoot
	}
	return nil
}

// TestRecoverMembershipsDamagedBlobRejectsEntry corrupts the blob root
// CRC and proves the membership rejects with the blob damage envelope
// (Rust damaged_blob_rejects_its_membership_and_known_range minus the
// range side, which the range recovery of a later slice owns).
func TestRecoverMembershipsDamagedBlobRejectsEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := membershipSource(t, path, [][2]any{{"alpha", uint32(3)}, {"middle", uint32(31_999)}, {"omega", uint32(51_137)}}, []membershipRange{
		{from: 0, to: 9, words: wideBitmap()},
	})
	source := mapSource(t, path)
	defer source.Close()
	budget := recoveryBudget(1 << 22)
	finderPages, err := forRecovery(budget.MaxHeapBytes/2, meta.PageCount, meta, budget)
	if err != nil {
		t.Fatalf("page set: %v", err)
	}
	finder := &blobRootFinder{}
	if err := scanTree(membershipIDCodec{}, source, meta, meta.MembershipIDRoot, finderPages, nil, finder); err != nil {
		t.Fatalf("blob root scan: %v", err)
	}
	if finder.root == 0 {
		t.Fatal("no blob membership found")
	}
	corruptCRC(t, path, finder.root)

	pages, tables := prepareMembershipRecovery(t, source, meta)
	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	rep := newReporter(sink)
	catalogRecovered, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	recovered, err := recoverMemberships(source, meta, catalogRecovered, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverMemberships: %v", err)
	}
	report := rep.finish()
	if report.MembershipEntries.Examined != 1 || report.MembershipEntries.Accepted != 0 || report.MembershipEntries.Rejected != 1 {
		t.Fatalf("membership counts %+v, want 1/0/1", report.MembershipEntries)
	}
	if report.Pages.Rejected != 1 {
		t.Fatalf("page counts %+v, want 1 rejected", report.Pages)
	}
	recoveredEntry, err := recovered.record(tables, 0)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !recoveredEntry.rejected || recoveredEntry.id != 1 {
		t.Fatalf("membership record %+v, want id 1 rejected", recoveredEntry)
	}
	blobCRC := false
	dictInvalid := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonPageCrcMismatch && envelope.Object == validation.ObjectMembershipBlob {
			blobCRC = true
		}
		if envelope.Reason == validation.ReasonMembershipInvalid && envelope.Object == validation.ObjectMembershipDictionary {
			dictInvalid = true
		}
	}
	if !blobCRC || !dictInvalid {
		t.Fatalf("envelopes %+v, want blob CRC and dictionary invalid", unknown)
	}
}

// TestRecoverCatalogEitherTreeIsSufficient corrupts the catalog name
// root CRC and proves the index tree alone reconciles the feeds and
// their memberships (Rust
// either_catalog_tree_is_sufficient_for_equal_conflict_free_pairs).
func TestRecoverCatalogEitherTreeIsSufficient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := membershipSourceLimit(t, path, 64, [][2]any{{"a", uint32(1)}, {"b", uint32(5)}}, []membershipRange{
		{from: 0, to: 9, words: writer.OutputWords{1 << 1}},
		{from: 20, to: 29, words: writer.OutputWords{1 << 5}},
	})
	source := mapSource(t, path)
	defer source.Close()
	corruptCRC(t, path, meta.CatalogNameRoot)

	pages, tables := prepareMembershipRecovery(t, source, meta)
	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	rep := newReporter(sink)
	catalogRecovered, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	report := rep.finish()
	if report.Pages.Rejected != 1 {
		t.Fatalf("page counts %+v, want 1 rejected", report.Pages)
	}
	if report.CatalogEntries.Examined != 2 || report.CatalogEntries.Accepted != 2 || report.CatalogEntries.Rejected != 0 {
		t.Fatalf("catalog counts %+v, want 2/2/0", report.CatalogEntries)
	}
	crcEnvelope := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonPageCrcMismatch && envelope.Object == validation.ObjectCatalogNameTree {
			crcEnvelope = true
		}
	}
	if !crcEnvelope {
		t.Fatalf("envelopes %+v, want catalog name CRC", unknown)
	}
	recovered, err := recoverMemberships(source, meta, catalogRecovered, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverMemberships: %v", err)
	}
	report = rep.finish()
	if report.MembershipEntries.Examined != 2 || report.MembershipEntries.Accepted != 2 || report.MembershipEntries.Rejected != 0 {
		t.Fatalf("membership counts %+v, want 2/2/0", report.MembershipEntries)
	}
	for id := uint32(1); id <= 2; id++ {
		entry, found, err := recovered.get(tables, id)
		if err != nil || !found || entry.rejected {
			t.Fatalf("membership id %d found=%v rejected=%v err=%v", id, found, entry.rejected, err)
		}
	}
}

// membershipOutputBuilder builds the recovery destination for one
// membership source (Rust output_builder over the source meta).
func membershipOutputBuilder(t *testing.T, path string, source format.Meta) *writer.OutputBuilder {
	t.Helper()
	spec, err := writer.FreshOutputSpec(source.AddressFamily, format.ValueKindMembership, format.StructureKindNone, source.ValueTag, source.FeedIndexLimit)
	if err != nil {
		t.Fatalf("FreshOutputSpec: %v", err)
	}
	builder, err := writer.NewOutputBuilder(path, spec, writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	return builder
}

// constructMembership runs one membership construction and closes the
// finished output (Rust construct + drop(finished.file)).
func constructMembership(t *testing.T, source *mapping.Mapping, meta format.Meta, outputPath string, budget *RecoveryBudget, sink RecoverySink) (*Construction, *constructionFailure) {
	t.Helper()
	builder := membershipOutputBuilder(t, outputPath, meta)
	construction, failure := membershipConstruct(source, meta, builder, budget, nil, sink)
	if failure == nil {
		if err := construction.finished.Close(); err != nil {
			t.Fatalf("Close finished output: %v", err)
		}
	}
	return construction, failure
}

// TestMembershipRecoveryConstructClean builds one membership source
// with feeds, wide and inline bitmaps, and metadata, recovers it into
// a fresh output, and proves the output preserves the feeds, the
// bitmaps, the metadata, and the report counts (Rust
// clean_membership_recovery_preserves_feeds_bitmaps_metadata_and_high_water).
func TestMembershipRecoveryConstructClean(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	builder, err := writer.NewOutputBuilder(sourcePath, membershipSourceSpec(32_002), writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	for _, pair := range [][2]any{{"alpha", uint32(3)}, {"middle", uint32(31_999)}, {"omega", uint32(32_001)}} {
		if err := builder.PushFeed(pair[0].(string), pair[1].(uint32)); err != nil {
			t.Fatalf("PushFeed(%s): %v", pair[0], err)
		}
	}
	wide := make(writer.OutputWords, 501)
	wide[0] = 1 << 3
	wide[499] = 1 << 63
	wide[500] = 1 << 1
	alpha := writer.OutputWords{1 << 3}
	if err := builder.PushMembershipV4(0, 9, wide); err != nil {
		t.Fatalf("push wide: %v", err)
	}
	if err := builder.PushMembershipV4(20, 29, alpha); err != nil {
		t.Fatalf("push alpha: %v", err)
	}
	if err := builder.PushMembershipV4(40, 49, wide); err != nil {
		t.Fatalf("push wide 40-49: %v", err)
	}
	if err := builder.WriteMetadataWithBudget([]byte(`{"kind":"membership"}`), 8*1024*1024); err != nil {
		t.Fatalf("WriteMetadataWithBudget: %v", err)
	}
	if err := builder.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	meta := builder.Meta()
	if err := builder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	source := mapSource(t, sourcePath)
	defer source.Close()

	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	construction, failure := constructMembership(t, source, meta, outputPath, recoveryBudget(1<<22), sink)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown envelopes %+v, want none", unknown)
	}
	report := construction.report
	if report.CatalogEntries.Examined != 6 || report.CatalogEntries.Accepted != 6 || report.CatalogEntries.Rejected != 0 {
		t.Fatalf("catalog counts %+v, want 6/6/0", report.CatalogEntries)
	}
	if report.MembershipEntries.Examined != 2 || report.MembershipEntries.Accepted != 2 || report.MembershipEntries.Rejected != 0 {
		t.Fatalf("membership counts %+v, want 2/2/0", report.MembershipEntries)
	}
	if report.Ranges.Accepted != 3 || report.Ranges.Rejected != 0 {
		t.Fatalf("range counts %+v, want 3 accepted", report.Ranges)
	}

	r := reopenMember(t, outputPath)
	defer r.Close()
	reopened := r.Meta()
	if reopened.ActiveFeedCount != 3 || reopened.RangeRecordCount != 3 {
		t.Fatalf("meta %+v, want 3 feeds 3 ranges", reopened)
	}
	for name, wantIndex := range map[string]uint32{"alpha": 3, "middle": 31_999, "omega": 32_001} {
		entry, found, err := r.LookupFeed(name)
		if err != nil || !found || entry.FeedIndex != wantIndex {
			t.Fatalf("LookupFeed(%s) = %+v/%v err=%v, want index %d", name, entry, found, err, wantIndex)
		}
	}
	view, found, err := r.LookupMembership4(5)
	if err != nil || !found {
		t.Fatalf("LookupMembership4(5) found=%v err=%v", found, err)
	}
	for _, index := range []uint32{3, 31_999, 32_001} {
		contained, err := view.ContainsIndex(index)
		if err != nil || !contained {
			t.Fatalf("contains(%d) = %v err=%v, want true", index, contained, err)
		}
	}
	metadata, found, err := r.ReadMetadataJSON()
	if err != nil || !found || string(metadata) != `{"kind":"membership"}` {
		t.Fatalf("ReadMetadataJSON = %q/%v err=%v", metadata, found, err)
	}
	validateClean(t, outputPath)
}

// reopenMember opens one finished membership output (Rust
// ImmutableReader::open; the internal reader keeps the recovery
// package free of the root import cycle once the root facade
// composes the recovery machine).
func reopenMember(t *testing.T, path string) *reader.ImmutableReader {
	t.Helper()
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	return r
}

// TestMembershipRecoveryConstructDamagedBlob corrupts one blob root
// CRC and proves the membership and its range reject while the feeds
// survive (Rust damaged_blob_rejects_its_membership_and_known_range).
func TestMembershipRecoveryConstructDamagedBlob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := membershipSource(t, path, [][2]any{{"alpha", uint32(3)}, {"middle", uint32(31_999)}, {"omega", uint32(51_137)}}, []membershipRange{
		{from: 0, to: 9, words: wideBitmap()},
	})
	source := mapSource(t, path)
	defer source.Close()
	budget := recoveryBudget(1 << 22)
	finderPages, err := forRecovery(budget.MaxHeapBytes/2, meta.PageCount, meta, budget)
	if err != nil {
		t.Fatalf("page set: %v", err)
	}
	finder := &blobRootFinder{}
	if err := scanTree(membershipIDCodec{}, source, meta, meta.MembershipIDRoot, finderPages, nil, finder); err != nil {
		t.Fatalf("blob root scan: %v", err)
	}
	if finder.root == 0 {
		t.Fatal("no blob membership found")
	}
	corruptCRC(t, path, finder.root)

	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	construction, failure := constructMembership(t, source, meta, outputPath, budget, sink)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	report := construction.report
	if report.MembershipEntries.Rejected != 1 || report.Ranges.Accepted != 0 || report.Ranges.Rejected != 1 {
		t.Fatalf("recovery counts %+v, want membership 1 rejected, 1 range rejected", report)
	}
	blobCRC := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonPageCrcMismatch && envelope.Object == validation.ObjectMembershipBlob {
			blobCRC = true
		}
	}
	if !blobCRC {
		t.Fatalf("envelopes %+v, want blob CRC", unknown)
	}
	r := reopenMember(t, outputPath)
	defer r.Close()
	reopened := r.Meta()
	if reopened.ActiveFeedCount != 3 || reopened.RangeRecordCount != 0 {
		t.Fatalf("meta %+v, want 3 feeds 0 ranges", reopened)
	}
	validateClean(t, outputPath)
}

// TestMembershipRecoveryConstructCatalogConflict rewrites one feed
// index in the catalog and proves only the dependent membership and
// its range reject (Rust
// catalog_conflicts_reject_only_dependent_memberships_and_ranges).
func TestMembershipRecoveryConstructCatalogConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := membershipSourceLimit(t, path, 64, [][2]any{{"a", uint32(1)}, {"b", uint32(5)}}, []membershipRange{
		{from: 0, to: 9, words: writer.OutputWords{1 << 1}},
		{from: 20, to: 29, words: writer.OutputWords{1 << 5}},
	})
	source := mapSource(t, path)
	defer source.Close()
	rewriteNameIndex(t, path, meta, "a", 7)

	var unknown []RecoveryUnknownEnvelope
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	})
	outputPath := filepath.Join(dir, "output.iprdb")
	construction, failure := constructMembership(t, source, meta, outputPath, recoveryBudget(1<<22), sink)
	if failure != nil {
		t.Fatalf("construct failure: %v", failure.cause)
	}
	report := construction.report
	if report.CatalogEntries.Examined != 4 || report.CatalogEntries.Accepted != 2 || report.CatalogEntries.Rejected != 2 {
		t.Fatalf("catalog counts %+v, want 4/2/2", report.CatalogEntries)
	}
	if report.MembershipEntries.Accepted != 1 || report.MembershipEntries.Rejected != 1 {
		t.Fatalf("membership counts %+v, want 1/1", report.MembershipEntries)
	}
	if report.Ranges.Accepted != 1 || report.Ranges.Rejected != 1 {
		t.Fatalf("range counts %+v, want 1/1", report.Ranges)
	}
	catalogInvalid := false
	membershipMissing := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonCatalogInvalid {
			catalogInvalid = true
		}
		if envelope.Reason == validation.ReasonMembershipMissing {
			membershipMissing = true
		}
	}
	if !catalogInvalid || !membershipMissing {
		t.Fatalf("envelopes %+v, want catalog invalid and membership missing", unknown)
	}
	r := reopenMember(t, outputPath)
	defer r.Close()
	if _, found, err := r.LookupFeed("a"); err != nil || found {
		t.Fatalf("LookupFeed(a) found=%v err=%v, want none", found, err)
	}
	entry, found, err := r.LookupFeed("b")
	if err != nil || !found || entry.FeedIndex != 5 {
		t.Fatalf("LookupFeed(b) = %+v/%v err=%v, want index 5", entry, found, err)
	}
	validateClean(t, outputPath)
}
