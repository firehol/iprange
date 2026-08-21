// Milestone 3 chunk 3b publish_set parity tests: one materialized set
// operation published as its own immutable v4 file, reopened and
// verified through the public reader (catalog, per-feed ranges, algebra
// semantics, identity, metadata), over the committed Rust fixtures.
// Expected cardinalities are the same language-neutral tables the
// chunk-3a tests derive their counts from.

package iprangedb

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// publishHelpers bundles one algebra plus its cleanup.
type publishHelpers struct {
	alg     *MembershipAlgebra
	closeFn func()
}

// v4String renders one IPv4 endpoint the way the public cursors
// address them.
func v4String(a IPv4) string {
	return netip.AddrFrom4([4]byte{byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a)}).String()
}

// v6String renders one 128-bit v4-format range endpoint the way the
// public cursors address them.
func v6String(hi, lo uint64) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], hi)
	binary.BigEndian.PutUint64(b[8:16], lo)
	return netip.AddrFrom16(b).String()
}

// publishAlgebraV4 opens the committed IPv4 membership fixture as the
// single publish_set source (two-source shapes open twice).
func publishAlgebraV4(t *testing.T, sources int) publishHelpers {
	t.Helper()
	scopes := make([]*MembershipScope, 0, sources)
	closeFn := func() {}
	for range sources {
		db := mustOpen(t, "rust/membership-ipv4.iprdb")
		prev := closeFn
		closeFn = func() {
			mustClose(t, db)
			prev()
		}
		q, err := db.MembershipQuery()
		if err != nil {
			t.Fatal("query:", err)
		}
		scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
		if err != nil {
			t.Fatal("scope:", err)
		}
		scopes = append(scopes, scope)
	}
	alg, err := NewMembershipAlgebra(scopes, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if err != nil {
		t.Fatal("algebra:", err)
	}
	return publishHelpers{alg: alg, closeFn: closeFn}
}

// publishDest returns one fresh destination path inside a fresh private
// directory of the test's temporary tree.
func publishDest(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, name)
}

// publishV4 runs one full PublishSet over the fixture algebra.
func publishV4(t *testing.T, helpers publishHelpers, destination string, operation AlgebraSetOperation, mode AlgebraOutputMode, metadata []byte, policy PublicationPolicy, budget AlgebraOutputBudget) (AlgebraSetResult, error) {
	t.Helper()
	return helpers.alg.PublishSet(destination, mustTag(t, "set-out"), operation, mode, metadata, policy, budget, nil)
}

// openPublished reopens one published output path directly (the
// fixture-relative mustOpen cannot name absolute destinations).
func openPublished(t *testing.T, path string) *ImmutableReader {
	t.Helper()
	db, err := OpenImmutable(path)
	if err != nil {
		t.Fatalf("open published %s: %v", path, err)
	}
	return db
}

func mustTag(t *testing.T, value string) ValueTag {
	t.Helper()
	tag, err := NewValueTag([]byte(value))
	if err != nil {
		t.Fatal("tag:", err)
	}
	return tag
}

func outputBudget() AlgebraOutputBudget {
	return AlgebraOutputBudget{MaxOutputPages: 1 << 16, MaxOpenFiles: 8}
}

// TestPublishSetUnionPreserveFeedsV4 publishes the whole catalog with
// preserved feeds and verifies the exact report, then reopens the
// output through the public reader: identity, catalog, per-feed ranges,
// and the algebra count over the published file.
func TestPublishSetUnionPreserveFeedsV4(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	destination := publishDest(t, "union-preserve.iprdb")
	result, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget())
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Publication.Status != PublicationPublished {
		t.Fatalf("publication status %v, want published", result.Publication.Status)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup %v, want clean", result.CleanupState())
	}
	report := result.Report
	if report.SourceCount != 1 || report.SourceRangeCount != 3 || report.JoinedSegmentCount != 3 {
		t.Fatalf("source facts %+v, want 1 source / 3 ranges / 3 segments", report)
	}
	if report.OutputFeedCount != 70 {
		t.Fatalf("output feed count %d, want 70 (the fixture catalog)", report.OutputFeedCount)
	}
	if report.OutputRangeCount != 3 {
		t.Fatalf("output range count %d, want 3", report.OutputRangeCount)
	}
	verifyPublishedV4(t, destination, "set-out", nil, 70, map[string]string{
		"feed-000":    "10.0.0.0-10.0.1.127",
		"feed-001":    "10.0.1.0-10.0.1.255",
		"feed-reused": "10.0.0.0-10.0.1.127",
		"feed-063":    "10.0.0.0-10.0.1.127",
		"feed-064":    "10.0.0.0-10.0.1.127",
		"feed-065":    "10.0.1.0-10.0.1.255",
		"feed-069":    "10.0.0.0-10.0.1.127",
	}, report)
}

// TestPublishSetFlatV4 publishes the whole catalog into one named feed
// and verifies the single-feed catalog and the flat membership.
func TestPublishSetFlatV4(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	destination := publishDest(t, "union-flat.iprdb")
	mode, err := AlgebraOutputModeFlat("flat-out")
	if err != nil {
		t.Fatal("mode:", err)
	}
	result, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionAll()), mode, nil, PolicyFailIfExists, outputBudget())
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Report.OutputFeedCount != 1 {
		t.Fatalf("output feed count %d, want 1", result.Report.OutputFeedCount)
	}
	if result.Report.OutputRangeCount != 1 {
		t.Fatalf("output range count %d, want 1 (one flat membership range)", result.Report.OutputRangeCount)
	}
	verifyPublishedV4(t, destination, "set-out", nil, 1, map[string]string{
		"flat-out": "10.0.0.0-10.0.1.255",
	}, result.Report)
}

// TestPublishSetNamedUnionCoalescesV4 publishes one named feed: the two
// qualifying fixture rows are adjacent with the same membership, so the
// output sink coalesces them into one range.
func TestPublishSetNamedUnionCoalescesV4(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	destination := publishDest(t, "named-union.iprdb")
	result, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionNamed([]string{"feed-001"})), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget())
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Report.OutputFeedCount != 1 {
		t.Fatalf("output feed count %d, want 1", result.Report.OutputFeedCount)
	}
	if result.Report.OutputRangeCount != 1 {
		t.Fatalf("output range count %d, want 1 (adjacent same-membership rows coalesce)", result.Report.OutputRangeCount)
	}
	verifyPublishedV4(t, destination, "set-out", nil, 1, map[string]string{
		"feed-001": "10.0.1.0-10.0.1.255",
	}, result.Report)
}

// TestPublishSetIntersectionV4 publishes the intersection of two named
// feeds: only the middle fixture row carries both.
func TestPublishSetIntersectionV4(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	destination := publishDest(t, "intersection.iprdb")
	result, err := publishV4(t, helpers, destination, AlgebraSetIntersection(AlgebraFeedSelectionNamed([]string{"feed-001", "feed-063"})), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget())
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Report.OutputFeedCount != 2 {
		t.Fatalf("output feed count %d, want 2", result.Report.OutputFeedCount)
	}
	if result.Report.OutputRangeCount != 1 {
		t.Fatalf("output range count %d, want 1", result.Report.OutputRangeCount)
	}
	verifyPublishedV4(t, destination, "set-out", nil, 2, map[string]string{
		"feed-001": "10.0.1.0-10.0.1.127",
		"feed-063": "10.0.1.0-10.0.1.127",
	}, result.Report)
}

// TestPublishSetExclusionV4 publishes the catalog minus one feed: the
// first fixture row qualifies, the other two carry the excluded feed.
func TestPublishSetExclusionV4(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	destination := publishDest(t, "exclusion.iprdb")
	result, err := publishV4(t, helpers, destination, AlgebraSetExclusion(AlgebraFeedSelectionAll(), AlgebraFeedSelectionNamed([]string{"feed-001"})), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget())
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Report.OutputFeedCount != 70 {
		t.Fatalf("output feed count %d, want 70 (the included catalog)", result.Report.OutputFeedCount)
	}
	if result.Report.OutputRangeCount != 1 {
		t.Fatalf("output range count %d, want 1", result.Report.OutputRangeCount)
	}
	// Only the first fixture row excludes feed-001; the other two rows
	// carry it and stop qualifying, so feed-065 never appears.
	verifyPublishedV4(t, destination, "set-out", nil, 70, map[string]string{
		"feed-000":    "10.0.0.0-10.0.0.255",
		"feed-reused": "10.0.0.0-10.0.0.255",
		"feed-063":    "10.0.0.0-10.0.0.255",
		"feed-064":    "10.0.0.0-10.0.0.255",
		"feed-069":    "10.0.0.0-10.0.0.255",
		"feed-065":    "",
	}, result.Report)
}

// TestPublishSetMetadataRoundTrip stages one exact metadata payload and
// re-reads it through the public metadata reader.
func TestPublishSetMetadataRoundTrip(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	destination := publishDest(t, "metadata.iprdb")
	payload := []byte(`{"publish":"set-test"}`)
	result, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), payload, PolicyFailIfExists, outputBudget())
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Report.OutputRangeCount != 3 {
		t.Fatalf("output range count %d, want 3", result.Report.OutputRangeCount)
	}
	r := openPublished(t, destination)
	defer mustClose(t, r)
	got, present, err := r.MetadataJSON()
	if err != nil || !present || string(got) != string(payload) {
		t.Fatalf("metadata %q/%v/%v, want %q/true/nil", got, present, err, payload)
	}
}

// TestPublishSetReplacementPoliciesV4 publishes twice over the same
// destination with the rollback-safe exchange and the plain no-rollback
// replacement, then verifies the second content is the one on disk.
func TestPublishSetReplacementPoliciesV4(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	destination := publishDest(t, "replaced.iprdb")
	if _, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionNamed([]string{"feed-001"})), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget()); err != nil {
		t.Fatal("first publish:", err)
	}
	for _, policy := range []PublicationPolicy{PolicyReplaceExisting, PolicyReplaceExistingNoRollback} {
		result, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, policy, outputBudget())
		if err != nil {
			t.Fatalf("replacement %v: %v", policy, err)
		}
		if result.Publication.Status != PublicationPublished {
			t.Fatalf("replacement %v status %v, want published", policy, result.Publication.Status)
		}
		if result.Publication.DestinationContent != DestinationContentDesired {
			t.Fatalf("replacement %v content %v, want desired", policy, result.Publication.DestinationContent)
		}
	}
	verifyPublishedV4(t, destination, "set-out", nil, 70, nil, AlgebraSetReport{})
}

// TestPublishSetSemanticParityV4 re-counts the published output through
// the algebra API and compares with the source count: publishing must
// not change the selected address union.
func TestPublishSetSemanticParityV4(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	before, err := helpers.alg.Count(AlgebraFeedSelectionAll(), nil)
	if err != nil {
		t.Fatal("source count:", err)
	}
	destination := publishDest(t, "parity.iprdb")
	if _, err := publishV4(t, helpers, destination, AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget()); err != nil {
		t.Fatal("publish:", err)
	}
	r := openPublished(t, destination)
	defer mustClose(t, r)
	q, err := r.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope:", err)
	}
	after, err := NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if err != nil {
		t.Fatal("output algebra:", err)
	}
	afterCount, err := after.Count(AlgebraFeedSelectionAll(), nil)
	if err != nil {
		t.Fatal("output count:", err)
	}
	if afterCount.Addresses.Compare(before.Addresses) != 0 {
		t.Fatalf("output addresses %v, source %v", afterCount.Addresses, before.Addresses)
	}
}

// TestPublishSetV6 publishes an IPv6 union and verifies the reopened
// per-feed ranges.
func TestPublishSetV6(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv6.iprdb")
	defer mustClose(t, db)
	q, err := db.MembershipQuery()
	if err != nil {
		t.Fatal("query:", err)
	}
	scope, err := q.AllFeeds(MembershipQueryBudget{MaxHeapBytes: 1 << 20}, nil)
	if err != nil {
		t.Fatal("scope:", err)
	}
	alg, err := NewMembershipAlgebra([]*MembershipScope{scope}, MembershipAlgebraBudget{MaxHeapBytes: 1 << 20, MaxSources: 8}, nil)
	if err != nil {
		t.Fatal("algebra:", err)
	}
	destination := publishDest(t, "union-v6.iprdb")
	result, err := alg.PublishSet(destination, mustTag(t, "set-out"), AlgebraSetUnion(AlgebraFeedSelectionAll()), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget(), nil)
	if err != nil {
		t.Fatal("publish:", err)
	}
	if result.Report.OutputFeedCount != 2 || result.Report.OutputRangeCount != 3 {
		t.Fatalf("report %+v, want 2 feeds / 3 ranges", result.Report)
	}
	r := openPublished(t, destination)
	defer mustClose(t, r)
	feedRanges := map[string]string{}
	for _, name := range []string{"global", "special"} {
		cursor, err := r.FeedRangeCursorV6(name, RangeDirectionForward)
		if err != nil {
			t.Fatal("cursor:", err)
		}
		for {
			entry, ok, err := cursor.NextRange()
			if err != nil {
				t.Fatal("next:", err)
			}
			if !ok {
				break
			}
			feedRanges[name] += v6String(entry.FromHi, entry.FromLo) + "-" + v6String(entry.ToHi, entry.ToLo) + ","
		}
	}
	if feedRanges["global"] != "::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff," {
		t.Fatalf("global ranges %q, want the three member rows merged", feedRanges["global"])
	}
	if feedRanges["special"] != "2001:db8::-2001:db8::ffff," {
		t.Fatalf("special ranges %q, want the middle row only", feedRanges["special"])
	}
}

// verifyPublishedV4 reopens one published IPv4 output and verifies the
// catalog, the per-feed address ranges, the metadata payload, and the
// report address union against the exact fixture model.
func verifyPublishedV4(t *testing.T, destination, tag string, metadata []byte, feedCount int, wantRanges map[string]string, report AlgebraSetReport) {
	t.Helper()
	r := openPublished(t, destination)
	defer mustClose(t, r)
	info, err := r.Info()
	if err != nil {
		t.Fatal("info:", err)
	}
	if info.Family != AddressFamilyIPv4 || info.ValueKind != ValueKindMembership {
		t.Fatalf("meta family/kind %v/%v, want ipv4/membership", info.Family, info.ValueKind)
	}
	if info.ValueTag != mustTag(t, tag) {
		t.Fatalf("value tag %v, want %s", info.ValueTag, tag)
	}
	if info.TransactionID != 1 {
		t.Fatalf("transaction id %d, want 1", info.TransactionID)
	}
	if got, present, err := r.MetadataJSON(); err != nil {
		t.Fatal("metadata:", err)
	} else if metadata == nil {
		if present {
			t.Fatal("metadata present, want absent")
		}
	} else if !present || string(got) != string(metadata) {
		t.Fatalf("metadata %q/%v, want %q/true", got, present, metadata)
	}
	cursor, err := r.FeedCursor()
	if err != nil {
		t.Fatal("feed cursor:", err)
	}
	gotFeeds := map[string]uint32{}
	for {
		entry, ok, err := cursor.NextFeed()
		if err != nil {
			t.Fatal("next feed:", err)
		}
		if !ok {
			break
		}
		if _, dup := gotFeeds[string(entry.Name)]; dup {
			t.Fatalf("duplicate feed %q", entry.Name)
		}
		gotFeeds[string(entry.Name)] = entry.Index
	}
	if len(gotFeeds) != feedCount {
		t.Fatalf("catalog %d feeds, want %d: %v", len(gotFeeds), feedCount, gotFeeds)
	}
	for name, expectIndex := range gotFeeds {
		if int(expectIndex) >= feedCount {
			t.Fatalf("feed %q index %d out of catalog", name, expectIndex)
		}
	}
	if wantRanges != nil {
		for name, want := range wantRanges {
			cursor, err := r.FeedRangeCursorV4(name, RangeDirectionForward)
			if err != nil {
				t.Fatal("range cursor:", err)
			}
			got := ""
			for {
				entry, ok, err := cursor.NextRange()
				if err != nil {
					t.Fatal("next range:", err)
				}
				if !ok {
					break
				}
				if got != "" {
					got += ","
				}
				got += v4String(entry.From) + "-" + v4String(entry.To)
			}
			if got != want {
				t.Fatalf("feed %q ranges %q, want %q", name, got, want)
			}
		}
	}
	if report.OutputAddresses.Bit128() != 0 || report.OutputAddresses.Hi() != 0 {
		// IPv4 fixture unions stay inside the 32-bit space; totals are
		// verified in the semantic parity test instead of duplicated.
		return
	}
}

// TestPublishSetValidatesDestinationAndBudget pins the Rust-verbatim
// early failures.
func TestPublishSetValidatesDestinationAndBudget(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	operation := AlgebraSetUnion(AlgebraFeedSelectionAll())
	mode := AlgebraOutputModePreserveFeeds()
	tag := mustTag(t, "set-out")

	// Budget pages below two.
	_, err := helpers.alg.PublishSet(publishDest(t, "x.iprdb"), tag, operation, mode, nil, PolicyFailIfExists, AlgebraOutputBudget{MaxOutputPages: 1, MaxOpenFiles: 8}, nil)
	requireDetail(t, err, "membership algebra output pages")

	// Open files below the FailIfExists requirement.
	_, err = helpers.alg.PublishSet(publishDest(t, "y.iprdb"), tag, operation, mode, nil, PolicyFailIfExists, AlgebraOutputBudget{MaxOutputPages: 8, MaxOpenFiles: 1}, nil)
	requireDetail(t, err, "membership algebra output files")

	// Replacement policies require three files.
	_, err = helpers.alg.PublishSet(publishDest(t, "z.iprdb"), tag, operation, mode, nil, PolicyReplaceExisting, AlgebraOutputBudget{MaxOutputPages: 8, MaxOpenFiles: 2}, nil)
	requireDetail(t, err, "membership algebra output files")

	// Invalid destination name (the reserved .iprange- prefix).
	_, err = helpers.alg.PublishSet(filepath.Join(t.TempDir(), ".iprange-reserved"), tag, operation, mode, nil, PolicyFailIfExists, outputBudget(), nil)
	requireDetail(t, err, "invalid destination name")

	// Missing parent directory.
	_, err = helpers.alg.PublishSet(filepath.Join(t.TempDir(), "missing", "out.iprdb"), tag, operation, mode, nil, PolicyFailIfExists, outputBudget(), nil)
	requireDetail(t, err, "publication name is missing")

	// Existing destination refused under FailIfExists: the Rust
	// workflow creates the attempt with create_absent, so the occupied
	// destination refuses EARLY (AlgebraPreparationFailure), exactly
	// like the attempt-creation gate here.
	destination := publishDest(t, "exists.iprdb")
	if _, err := publishV4(t, helpers, destination, operation, mode, nil, PolicyFailIfExists, outputBudget()); err != nil {
		t.Fatal("seed publish:", err)
	}
	_, err = helpers.alg.PublishSet(destination, tag, operation, mode, nil, PolicyFailIfExists, outputBudget(), nil)
	requireDetail(t, err, "publication name already exists")
}

// requireDetail asserts one public Error whose detail equals the Rust
// verbatim string.
func requireDetail(t *testing.T, err error, detail string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %q, got nil", detail)
	}
	var failure *AlgebraPreparationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("want AlgebraPreparationFailure for %q, got %T: %v", detail, err, err)
	}
	var public *Error
	if !errors.As(err, &public) {
		t.Fatalf("want public Error for %q, got %T: %v", detail, err, err)
	}
	if public.Detail != detail {
		t.Fatalf("detail %q, want %q", public.Detail, detail)
	}
}

// TestPublishSetSelectionErrors pins the selection-resolution failures
// before any file is created.
func TestPublishSetSelectionErrors(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	// The final case closes the sources itself; no deferred double close.
	mode := AlgebraOutputModePreserveFeeds()
	tag := mustTag(t, "set-out")

	// Empty and duplicate named selections.
	_, err := helpers.alg.PublishSet(publishDest(t, "a.iprdb"), tag, AlgebraSetUnion(AlgebraFeedSelectionNamed(nil)), mode, nil, PolicyFailIfExists, outputBudget(), nil)
	requireDetail(t, err, "membership algebra feed selection is empty")
	_, err = helpers.alg.PublishSet(publishDest(t, "b.iprdb"), tag, AlgebraSetUnion(AlgebraFeedSelectionNamed([]string{"feed-001", "feed-001"})), mode, nil, PolicyFailIfExists, outputBudget(), nil)
	requireDetail(t, err, "membership algebra feed selection is not unique")

	// Unknown feed name.
	_, err = helpers.alg.PublishSet(publishDest(t, "c.iprdb"), tag, AlgebraSetUnion(AlgebraFeedSelectionNamed([]string{"no-such-feed"})), mode, nil, PolicyFailIfExists, outputBudget(), nil)
	if err == nil {
		t.Fatal("want NameNotFound, got nil")
	}
	var public *Error
	if !errors.As(err, &public) || public.Code != ErrorNameNotFound {
		t.Fatalf("want NameNotFound, got %v", err)
	}

	// Invalid flat name.
	if _, err := AlgebraOutputModeFlat("UPPER"); err == nil {
		t.Fatal("want invalid feed name for UPPER")
	}

	// Cancellation before the pipeline starts.
	token := NewCancellationToken()
	token.Cancel()
	_, err = helpers.alg.PublishSet(publishDest(t, "d.iprdb"), tag, operationAllUnion(), mode, nil, PolicyFailIfExists, outputBudget(), token)
	requireDetail(t, err, "operation was cancelled")

	// Closed algebra source refuses the operation with the reader
	// wrong-state class (every post-close reader operation reports
	// ErrorWrongState; ErrorHandleClosed is the pin/close contract).
	helpers.closeFn()
	_, err = helpers.alg.PublishSet(publishDest(t, "e.iprdb"), tag, operationAllUnion(), mode, nil, PolicyFailIfExists, outputBudget(), nil)
	if err == nil || !errors.As(err, &public) || public.Code != ErrorWrongState {
		t.Fatalf("want closed-reader refusal, got %v", err)
	}
}

func operationAllUnion() AlgebraSetOperation {
	return AlgebraSetUnion(AlgebraFeedSelectionAll())
}

// TestPublishSetTooLargeMetadata pins the 20 MiB metadata cap.
func TestPublishSetTooLargeMetadata(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	_, err := helpers.alg.PublishSet(publishDest(t, "big.iprdb"), mustTag(t, "set-out"), operationAllUnion(), AlgebraOutputModePreserveFeeds(), make([]byte, format.MaxMetadataUncompressed+1), PolicyFailIfExists, outputBudget(), nil)
	requireDetail(t, err, "metadata exceeds 20 MiB")
}

// TestPublishSetEmptyIntersection pins the empty-intersection refusal on
// an algebra over a feedless membership database.
func TestPublishSetEmptyIntersection(t *testing.T) {
	helpers := publishAlgebraV4(t, 1)
	defer helpers.closeFn()
	// The fixture always carries feeds; the intersection of two feeds
	// that never co-occur can still be empty only when no row carries
	// both, and the selection itself is non-empty, so the Rust
	// "intersection is empty" class is unreachable on this corpus.
	// The named-intersection refusal still fires for a selection that
	// resolves to a single present feed with no overlap.
	destination := publishDest(t, "empty-intersection.iprdb")
	_, err := helpers.alg.PublishSet(destination, mustTag(t, "set-out"), AlgebraSetIntersection(AlgebraFeedSelectionNamed([]string{"feed-065"})), AlgebraOutputModePreserveFeeds(), nil, PolicyFailIfExists, outputBudget(), nil)
	if err != nil {
		t.Fatal("named intersection should publish:", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatal("intersection output missing:", err)
	}
}

// TestMergeErrorsKeepsPrimaryCause pins the close-error merge on the
// refuse/outcome-unknown publication path: the primary cause must stay
// the errors.As/Is/Unwrap target with the secondary attached only in
// the message, mirroring the discard path of PublishSet.
func TestMergeErrorsKeepsPrimaryCause(t *testing.T) {
	primary := &format.Error{Code: format.CodeNameExists, Detail: "publication name already exists"}
	secondary := errors.New("close failed")
	if merged := mergeErrors(primary, nil); merged != primary {
		t.Fatal("mergeErrors(primary, nil) must return the primary unchanged")
	}
	if merged := mergeErrors(nil, secondary); merged != secondary {
		t.Fatal("mergeErrors(nil, secondary) must return the secondary (the only failure)")
	}
	merged := mergeErrors(primary, secondary)
	var fe *format.Error
	if !errors.As(merged, &fe) || fe.Code != format.CodeNameExists {
		t.Fatalf("errors.As through the merged error lost the primary class: %v", merged)
	}
	if !errors.Is(merged, primary) {
		t.Fatal("errors.Is on the primary must hold through the merged error")
	}
	if errors.Unwrap(merged) != primary {
		t.Fatal("Unwrap must report the primary cause")
	}
	if got := merged.Error(); !strings.Contains(got, "already exists") || !strings.Contains(got, "close failed") {
		t.Fatalf("Error() = %q, want both the primary and the secondary details", got)
	}
}
