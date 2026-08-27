// Public membership import tests (Rust tests/membership_import.rs
// parity): a complete name-based import from a live or immutable
// pinned membership reader unions the translated source memberships
// into the preserved destination, reports exactly, treats an equal
// import as a clean no-change, handles an empty-feed catalog change
// and the full IPv6 space, and fails atomically on preconditions,
// cancellation, source corruption, and budget exhaustion while
// keeping the writer reusable.

package iprangedb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// importMembershipPair creates one fresh live membership pair with the
// membership value tag and the default reader capacity.
func importMembershipPair(t *testing.T, label string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "import-"+label+".iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, mustTag(t, "membership"), 4, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// importMembershipPair6 creates one fresh live IPv6 membership pair.
func importMembershipPair6(t *testing.T, label string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "import-"+label+".iprdb")
	if _, err := CreateLive(path, AddressFamilyIPv6, ValueKindMembership, StructureKindNone, mustTag(t, "membership"), 4, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// importChanged commits one import workflow that must be a change (Rust
// change_finished).
func importChanged(t *testing.T, finished *FinishedWorkflow, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if !finished.IsChanged() {
		t.Fatal("import unexpectedly produced no change")
	}
	result, err := finished.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("import commit status = %v, want Committed", result.Status)
	}
}

// TestLiveImportUnionsNamesPreservesDestinationAndReportsExactly ports
// the Rust live_import_unions_names_preserves_destination_and_reports_exactly
// test: the source has two overlapping feeds, the destination has one
// shared and one private feed, and the import unions the names, keeps
// the destination-only feed, and reports the exact six-way
// classification, source facts, and per-address membership.
func TestLiveImportUnionsNamesPreservesDestinationAndReportsExactly(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	sourcePath := importMembershipPair(t, "source")
	destinationPath := importMembershipPair(t, "destination")

	sourceWriter, err := OpenLiveWriter(sourcePath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	{
		transaction, err := sourceWriter.BeginMembershipTransaction(nil)
		if err != nil {
			t.Fatal(err)
		}
		alpha, err := transaction.EnsureFeed(feedName(t, "alpha"))
		if err != nil {
			t.Fatal(err)
		}
		beta, err := transaction.EnsureFeed(feedName(t, "beta"))
		if err != nil {
			t.Fatal(err)
		}
		empty, err := transaction.EmptyMembership()
		if err != nil {
			t.Fatal(err)
		}
		alphaMember, err := transaction.AddFeed(empty, alpha)
		if err != nil {
			t.Fatal(err)
		}
		betaMember, err := transaction.AddFeed(empty, beta)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ApplyV4(IPv4(0), IPv4(9), alphaMember, MembershipUnion); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ApplyV4(IPv4(5), IPv4(14), betaMember, MembershipUnion); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.SetMetadataJSON([]byte("source-only")); err != nil {
			t.Fatal(err)
		}
		result, err := transaction.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != CommitCommitted {
			t.Fatalf("source commit status = %v, want Committed", result.Status)
		}
	}
	if _, err := sourceWriter.Close(); err != nil {
		t.Fatal(err)
	}

	writer, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	{
		transaction, err := writer.BeginMembershipTransaction(nil)
		if err != nil {
			t.Fatal(err)
		}
		alpha, err := transaction.EnsureFeed(feedName(t, "alpha"))
		if err != nil {
			t.Fatal(err)
		}
		charlie, err := transaction.EnsureFeed(feedName(t, "charlie"))
		if err != nil {
			t.Fatal(err)
		}
		empty, err := transaction.EmptyMembership()
		if err != nil {
			t.Fatal(err)
		}
		alphaMember, err := transaction.AddFeed(empty, alpha)
		if err != nil {
			t.Fatal(err)
		}
		charlieMember, err := transaction.AddFeed(empty, charlie)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ApplyV4(IPv4(8), IPv4(12), alphaMember, MembershipUnion); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.ApplyV4(IPv4(20), IPv4(29), charlieMember, MembershipUnion); err != nil {
			t.Fatal(err)
		}
		if _, err := transaction.SetMetadataJSON([]byte("destination-old")); err != nil {
			t.Fatal(err)
		}
		result, err := transaction.Commit()
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != CommitCommitted {
			t.Fatalf("destination commit status = %v, want Committed", result.Status)
		}
	}

	source, err := OpenLiveReader(sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldDestination, err := OpenLiveReader(destinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	importHandle, err := writer.BeginMembershipImport(MembershipImportSourceLive(source), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	finished, err := importHandle.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !finished.IsChanged() {
		t.Fatal("import unexpectedly produced no change")
	}
	report := finished.Report()
	if report.Workflow != WorkflowMembershipImport {
		t.Fatalf("workflow kind = %v, want MembershipImport", report.Workflow)
	}
	if report.LogicalChange != LogicalChanged {
		t.Fatalf("logical change = %v, want Changed", report.LogicalChange)
	}
	if report.InputRecordCount != 3 || report.InputNormalizedIntervalCount != 3 {
		t.Fatalf("input record counts = %d/%d, want 3/3", report.InputRecordCount, report.InputNormalizedIntervalCount)
	}
	if report.BeforeRangeRecordCount != 2 || report.AfterRangeRecordCount != 4 {
		t.Fatalf("range record counts = %d/%d, want 2/4", report.BeforeRangeRecordCount, report.AfterRangeRecordCount)
	}
	expectCard(t, "input", report.InputAddresses, format.CardinalityFromUint64(15))
	expectCard(t, "before", report.BeforeAddresses, format.CardinalityFromUint64(15))
	expectCard(t, "after", report.AfterAddresses, format.CardinalityFromUint64(25))
	expectCard(t, "unchanged", report.UnchangedValueAddresses, format.CardinalityFromUint64(10))
	expectCard(t, "changed", report.ChangedValueAddresses, format.CardinalityFromUint64(5))
	expectCard(t, "added", report.AddedAddresses, format.CardinalityFromUint64(10))
	expectCard(t, "removed", report.RemovedAddresses, format.CardinalityFromUint64(0))
	if report.SourceFeedCount != 2 || report.MatchedFeedCount != 1 || report.CreatedFeedCount != 1 {
		t.Fatalf("feed counts = %d/%d/%d, want 2/1/1", report.SourceFeedCount, report.MatchedFeedCount, report.CreatedFeedCount)
	}
	if report.SourceDistinctMembershipCount != 3 || report.TranslatedMembershipCount != 3 {
		t.Fatalf("membership counts = %d/%d, want 3/3", report.SourceDistinctMembershipCount, report.TranslatedMembershipCount)
	}
	if _, err := finished.SetMetadataJSON([]byte("destination-new")); err != nil {
		t.Fatal(err)
	}
	if _, err := finished.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, found, err := oldDestination.LookupFeed("beta"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("destination feed beta exists before the import")
	}
	oldMetadata, present, err := oldDestination.MetadataJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !present || string(oldMetadata) != "destination-old" {
		t.Fatalf("old destination metadata = %q present %v, want destination-old", oldMetadata, present)
	}
	if _, err := oldDestination.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenLiveReader(destinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	alpha, found, err := reader.LookupFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("destination feed alpha missing after the import")
	}
	beta, found, err := reader.LookupFeed("beta")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("destination feed beta missing after the import")
	}
	charlie, found, err := reader.LookupFeed("charlie")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("destination feed charlie missing after the import")
	}
	pin, err := reader.Pin()
	if err != nil {
		t.Fatal(err)
	}
	for address, expected := range map[uint32][3]bool{
		0:  {true, false, false},
		4:  {true, false, false},
		5:  {true, true, false},
		12: {true, true, false},
		13: {false, true, false},
		14: {false, true, false},
		20: {false, false, true},
		29: {false, false, true},
	} {
		view, found, err := pin.LookupMembershipV4(IPv4(address))
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("address %d has no membership after the import", address)
		}
		has, err := view.ContainsIndex(alpha.Index)
		if err != nil {
			t.Fatal(err)
		}
		if has != expected[0] {
			t.Fatalf("address %d alpha membership = %v, want %v", address, has, expected[0])
		}
		has, err = view.ContainsIndex(beta.Index)
		if err != nil {
			t.Fatal(err)
		}
		if has != expected[1] {
			t.Fatalf("address %d beta membership = %v, want %v", address, has, expected[1])
		}
		has, err = view.ContainsIndex(charlie.Index)
		if err != nil {
			t.Fatal(err)
		}
		if has != expected[2] {
			t.Fatalf("address %d charlie membership = %v, want %v", address, has, expected[2])
		}
	}
	metadata, present, err := reader.MetadataJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !present || string(metadata) != "destination-new" {
		t.Fatalf("destination metadata after import = %q present %v, want destination-new", metadata, present)
	}
	if err := pin.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestImmutableEqualImportIsACleanNoChange ports the Rust
// immutable_equal_import_is_a_clean_no_change test: importing the exact
// copy of the destination through the immutable source variant is a
// clean no-change report and leaves no pending transaction.
func TestImmutableEqualImportIsACleanNoChange(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	destinationPath := importMembershipPair(t, "copy-origin")
	sourcePath := filepath.Join(t.TempDir(), "import-immutable-copy.iprdb")

	writer, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	create, err := writer.BeginCreateFeed(feedName(t, "alpha"), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4([]AddressRange4{{From: IPv4(10), To: IPv4(19)}}); err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	importChanged(t, finished, err)
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	source, err := OpenImmutable(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := OpenLiveReader(destinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	destinationInfo, err := destination.Info()
	if err != nil {
		t.Fatal(err)
	}
	if destinationInfo.DatabaseID != sourceInfoDatabaseID(t, source) {
		t.Fatal("copied source database id differs from the destination")
	}
	if _, err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err = OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	importHandle, err := writer.BeginMembershipImport(MembershipImportSourceImmutable(source), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	finished, err = importHandle.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if finished.IsChanged() {
		t.Fatal("equal import changed the destination")
	}
	report := finished.Report()
	if report.LogicalChange != LogicalNoChange {
		t.Fatalf("logical change = %v, want NoChange", report.LogicalChange)
	}
	expectCard(t, "unchanged", report.UnchangedValueAddresses, format.CardinalityFromUint64(10))
	if report.SourceFeedCount != 1 || report.MatchedFeedCount != 1 || report.CreatedFeedCount != 0 {
		t.Fatalf("feed counts = %d/%d/%d, want 1/1/0", report.SourceFeedCount, report.MatchedFeedCount, report.CreatedFeedCount)
	}
	if report.SourceDistinctMembershipCount != 1 || report.TranslatedMembershipCount != 1 {
		t.Fatalf("membership counts = %d/%d, want 1/1", report.SourceDistinctMembershipCount, report.TranslatedMembershipCount)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// sourceInfoDatabaseID reads the database id of one immutable source.
func sourceInfoDatabaseID(t *testing.T, source *ImmutableReader) [16]byte {
	t.Helper()
	info, err := source.Info()
	if err != nil {
		t.Fatal(err)
	}
	return info.DatabaseID
}

// TestEmptyFeedImportIsACatalogChangeAndFullIPv6IsExact ports the Rust
// empty_feed_import_is_a_catalog_change_and_full_ipv6_is_exact test: a
// feed with no ranges is still a catalog change, and an import of the
// full IPv6 space covers exactly the whole space.
func TestEmptyFeedImportIsACatalogChangeAndFullIPv6IsExact(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)

	emptySourcePath := importMembershipPair(t, "empty-source")
	emptyDestinationPath := importMembershipPair(t, "empty-destination")
	sourceWriter, err := OpenLiveWriter(emptySourcePath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	create, err := sourceWriter.BeginCreateFeed(feedName(t, "empty"), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	importChanged(t, finished, err)
	if _, err := sourceWriter.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := OpenLiveReader(emptySourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := OpenLiveWriter(emptyDestinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	importHandle, err := writer.BeginMembershipImport(MembershipImportSourceLive(source), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	emptyFinished, err := importHandle.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	if !emptyFinished.IsChanged() {
		t.Fatal("empty-feed import produced no change")
	}
	emptyReport := emptyFinished.Report()
	if emptyReport.SourceFeedCount != 1 || emptyReport.CreatedFeedCount != 1 {
		t.Fatalf("empty import feed counts = %d/%d, want 1/1", emptyReport.SourceFeedCount, emptyReport.CreatedFeedCount)
	}
	expectCard(t, "empty after", emptyReport.AfterAddresses, format.CardinalityZero())
	importChanged(t, emptyFinished, nil)
	if _, err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenLiveReader(emptyDestinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.LookupFeed("empty"); err != nil {
		t.Fatal(err)
	} else if !found {
		t.Fatal("empty feed missing after the import")
	}
	if _, err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	sourcePath := importMembershipPair6(t, "ipv6-source")
	destinationPath := importMembershipPair6(t, "ipv6-destination")
	sourceWriter, err = OpenLiveWriter(sourcePath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	create, err = sourceWriter.BeginCreateFeed(feedName(t, "all"), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV6([]AddressRange6{{FromHi: 0, FromLo: 0, ToHi: ^uint64(0), ToLo: ^uint64(0)}}); err != nil {
		t.Fatal(err)
	}
	finished, err = create.FinishInput()
	importChanged(t, finished, err)
	if _, err := sourceWriter.Close(); err != nil {
		t.Fatal(err)
	}
	source, err = OpenLiveReader(sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer, err = OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	importHandle, err = writer.BeginMembershipImport(MembershipImportSourceLive(source), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	fullFinished, err := importHandle.FinishInput()
	if err != nil {
		t.Fatal(err)
	}
	fullReport := fullFinished.Report()
	expectCard(t, "full input", fullReport.InputAddresses, fullIPv6Cardinality())
	expectCard(t, "full added", fullReport.AddedAddresses, fullIPv6Cardinality())
	importChanged(t, fullFinished, nil)
	if _, err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err = OpenLiveReader(destinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := reader.FeedRangeCursorV6("all", RangeDirectionForward)
	if err != nil {
		t.Fatal(err)
	}
	interval, ok, err := cursor.NextRange()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("full IPv6 import produced no feed range")
	}
	if interval.FromHi != 0 || interval.FromLo != 0 || interval.ToHi != ^uint64(0) || interval.ToLo != ^uint64(0) {
		t.Fatalf("full IPv6 range = %x:%x-%x:%x, want full space", interval.FromHi, interval.FromLo, interval.ToHi, interval.ToLo)
	}
	if _, ok, err := cursor.NextRange(); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("full IPv6 import produced a second range")
	}
	if _, err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

// fullIPv6Cardinality is the 2^128 address count of the full IPv6
// space [::, ffff:...], the inclusive interval size of (MIN, MAX).
func fullIPv6Cardinality() format.Cardinality129 {
	return format.FullIPv6Space()
}

// TestImportPreconditionsCancellationSourceFailureAndBudgetFailureAreAtomic
// ports the Rust
// import_preconditions_cancellation_source_failure_and_budget_failure_are_atomic
// test: every import failure arm aborts atomically, keeps the writer
// reusable, and never publishes a partial import.
func TestImportPreconditionsCancellationSourceFailureAndBudgetFailureAreAtomic(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	sourcePath := importMembershipPair(t, "failure-source")
	destinationPath := importMembershipPair(t, "failure-destination")

	sourceWriter, err := OpenLiveWriter(sourcePath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	create, err := sourceWriter.BeginCreateFeed(feedName(t, "alpha"), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := create.AddRangesV4([]AddressRange4{{From: IPv4(1), To: IPv4(2)}}); err != nil {
		t.Fatal(err)
	}
	finished, err := create.FinishInput()
	importChanged(t, finished, err)
	if _, err := sourceWriter.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := OpenLiveReader(sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}

	writer, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := NewCancellationToken()
	cancelled.Cancel()
	if _, err := writer.BeginMembershipImport(MembershipImportSourceLive(source), cancelled); lifecycleCode(err) != ErrorCancelled {
		t.Fatalf("cancelled begin = %v, want Cancelled", err)
	}
	during := NewCancellationToken()
	importHandle, err := writer.BeginMembershipImport(MembershipImportSourceLive(source), during)
	if err != nil {
		t.Fatal(err)
	}
	during.Cancel()
	if _, err := importHandle.FinishInput(); abortCauseCode(err) != ErrorCancelled {
		t.Fatalf("cancelled finish = %v, want transaction aborted wrapping cancelled", err)
	}
	// The aborted import left no pending transaction: a fresh workflow
	// begins cleanly (Rust writer.commit NoPendingTransaction parity).
	create, err = writer.BeginCreateFeed(feedName(t, "post-cancel"), NewCancellationToken())
	if err != nil {
		t.Fatal("writer not usable after a cancelled import:", err)
	}
	finished, err = create.FinishInput()
	importChanged(t, finished, err)
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// The source cannot import onto itself (same local file).
	sameWriter, err := OpenLiveWriter(sourcePath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sameWriter.BeginMembershipImport(MembershipImportSourceLive(source), NewCancellationToken()); lifecycleCode(err) != ErrorInvalidArgument {
		t.Fatalf("same-file import = %v, want InvalidArgument", err)
	}
	if _, err := sameWriter.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		label    string
		family   AddressFamily
		kind     ValueKind
		tag      string
		expected ErrorCode
	}{
		{"wrong-family", AddressFamilyIPv6, ValueKindMembership, "membership", ErrorWrongAddressFamily},
		{"wrong-tag", AddressFamilyIPv4, ValueKindMembership, "other", ErrorWrongValueTag},
		{"wrong-kind", AddressFamilyIPv4, ValueKindDirect, "membership", ErrorWrongValueKind},
	} {
		path := filepath.Join(t.TempDir(), "incompat-"+test.label+".iprdb")
		if _, err := CreateLive(path, test.family, test.kind, StructureKindNone, mustTag(t, test.tag), 4, nil); err != nil {
			t.Fatal(err)
		}
		incompatible, err := OpenLiveReader(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := OpenLiveWriter(destinationPath, DefaultBudget(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := candidate.BeginMembershipImport(MembershipImportSourceLive(incompatible), NewCancellationToken()); lifecycleCode(err) != test.expected {
			t.Fatalf("%s import = %v, want %v", test.label, err, test.expected)
		}
		// Rust parity: the failed begin left no pending transaction and the
		// writer starts the next workflow cleanly.
		create, err := candidate.BeginCreateFeed(feedName(t, "reusable-"+test.label), NewCancellationToken())
		if err != nil {
			t.Fatal(test.label, "writer not reusable:", err)
		}
		finished, err := create.FinishInput()
		importChanged(t, finished, err)
		if _, err := candidate.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := incompatible.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Tiny budget: the import cannot allocate a single draft page and
	// fails as TransactionAborted with the budget class inside.
	tinyBudget := DefaultBudget()
	tinyBudget.MaxHeapBytes = 2 << 20
	tinyBudget.MaxPrivatePages = 0
	tinyBudget.MaxGrowthPages = 0
	tinyWriter, err := OpenLiveWriter(destinationPath, tinyBudget, nil)
	if err != nil {
		t.Fatal(err)
	}
	tinyImport, err := tinyWriter.BeginMembershipImport(MembershipImportSourceLive(source), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tinyImport.FinishInput(); abortCauseCode(err) != ErrorInsufficientResourceBudget {
		t.Fatalf("tiny-budget finish = %v, want transaction aborted wrapping insufficient budget", err)
	}
	// The aborted import released the tiny-budget transaction; the
	// writer is closed explicitly like every other arm of this test
	// (Rust drops the writer at scope end; the live mapping would
	// otherwise block TempDir cleanup on Windows).
	if _, err := tinyWriter.Close(); err != nil {
		t.Fatalf("tiny-budget close: %v", err)
	}

	// The source read failure aborts the import and leaves the writer
	// reusable for a normal workflow (Rust corrupt_selected_range_root).
	brokenDestinationPath := importMembershipPair(t, "source-read-failure")
	reusable, err := OpenLiveWriter(brokenDestinationPath, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	brokenImport, err := reusable.BeginMembershipImport(MembershipImportSourceLive(source), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	corruptImportSelectedRangeRoot(t, sourcePath)
	if _, err := brokenImport.FinishInput(); abortCauseCode(err) != ErrorFormatInvalid {
		t.Fatalf("corrupt source finish = %v, want transaction aborted wrapping format invalid", err)
	}
	create, err = reusable.BeginCreateFeed(feedName(t, "writer-remains-usable"), NewCancellationToken())
	if err != nil {
		t.Fatal("writer not usable after a source failure:", err)
	}
	finished, err = create.FinishInput()
	importChanged(t, finished, err)
	if _, err := reusable.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

// abortCauseCode walks the TransactionAborted wrapper of one live-writer
// failure and reports the nested cause class (Rust
// TransactionAborted(cause)): the live writer nests the internal
// classedError with the public Unwrap chain, unlike the immutable
// writer path which used to return the public abortError directly (removed with the Writer surface).
func abortCauseCode(err error) ErrorCode {
	cause := errors.Unwrap(err)
	if cause == nil {
		return 0
	}
	return causeCode(cause)
}

// corruptImportSelectedRangeRoot corrupts the range-root page of the
// selected meta of one source file (Rust corrupt_selected_range_root):
// the selected meta is the one with the higher transaction id, and the
// byte at page offset 4 of the referenced range tree is overwritten so
// any later proof sees a broken tree.
func corruptImportSelectedRangeRoot(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var metas [2][format.PageSize]byte
	if _, err := file.ReadAt(metas[0][:], 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.ReadAt(metas[1][:], int64(format.PageSize)); err != nil {
		t.Fatal(err)
	}
	transaction := func(page *[format.PageSize]byte) uint64 { return format.U64(page[48:56]) }
	selected := 0
	if transaction(&metas[1]) > transaction(&metas[0]) {
		selected = 1
	}
	root := format.U32(metas[selected][144:148])
	if root == 0 {
		t.Fatal("selected meta has a zero range root")
	}
	if _, err := file.WriteAt([]byte{0xff}, int64(root)*int64(format.PageSize)+4); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
}
