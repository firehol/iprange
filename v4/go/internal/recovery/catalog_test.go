package recovery

// Recovery catalog reconciliation tests (Rust recovery/membership_tests
// catalog arms): the count over both catalog trees, the reconcile of
// equal conflict-free pairs into the accepted records proof, and the
// name-index rewrite conflict rejection.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// membershipSourceSpec builds the shared membership source identity of
// the catalog tests (Rust membership_tests source_builder: txn 7, the
// fixed feed limit and tag).
func membershipSourceSpec(feedIndexLimit uint64) writer.OutputSpec {
	return writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindMembership,
		StructureKind:  format.StructureKindNone,
		ValueTag:       tag16("feeds"),
		DatabaseID:     id16(9),
		TxnID:          7,
		CommitNonce:    id16(10),
		FeedIndexLimit: feedIndexLimit,
	}
}

// catalogSource builds one membership source with the given feed names
// and returns the committed meta (Rust membership_tests helpers: the
// membership reference batch mirrors the Rust test builder).
func catalogSource(t *testing.T, path string, names []string) format.Meta {
	t.Helper()
	builder, err := writer.NewOutputBuilder(path, membershipSourceSpec(8), writer.OutputBudget{MaxOutputPages: 20_000}, writer.ReferenceBatchEntryLimit, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	for index, name := range names {
		if err := builder.PushFeed(name, uint32(index)); err != nil {
			t.Fatalf("PushFeed(%s): %v", name, err)
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

// rewriteNameIndex rewrites the feed index of one name-tree leaf record
// and re-seals the page checksum (Rust membership_tests
// rewrite_name_index).
func rewriteNameIndex(t *testing.T, path string, meta format.Meta, name string, index uint32) {
	t.Helper()
	file, err := osOpenFileRW(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer file.Close()
	page := make([]byte, format.PageSize)
	if _, err := file.ReadAt(page, int64(meta.CatalogNameRoot)*format.PageSize); err != nil {
		t.Fatalf("read page: %v", err)
	}
	header, problem := format.InspectTreeHeader(page, meta.TxnID,
		byte(format.PageTypeCatalogNameBranch), byte(format.PageTypeCatalogNameLeaf), 0, nil)
	if problem != format.TreeHeaderProblemNone || header.Level != 0 {
		t.Fatalf("name root header problem %v level %d", problem, header.Level)
	}
	slotted, err := format.OpenSlottedHeader(page, header, format.PageTypeCatalogNameLeaf, 0, format.SlotItemsPerPage)
	if err != nil {
		t.Fatalf("slotted: %v", err)
	}
	cells := format.InspectLayout(page, &header, format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord)).Cells()
	found := false
	for cellIndex := 0; cellIndex < int(header.ItemCount); cellIndex++ {
		cell, ok := cells.Next()
		if !ok {
			t.Fatal("cell iteration ended early")
		}
		feedIndex, entryName, err := format.DecodeCatalogEntry(cell)
		if err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		if string(entryName) == name {
			found = true
		}
		_ = feedIndex
		_ = slotted
	}
	if !found {
		t.Fatalf("name %q not found on the root leaf", name)
	}
	// Re-decode the cells and rewrite the index of the matching name.
	cells = format.InspectLayout(page, &header, format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord)).Cells()
	for cellIndex := 0; cellIndex < int(header.ItemCount); cellIndex++ {
		cell, ok := cells.Next()
		if !ok {
			t.Fatal("cell iteration ended early")
		}
		_, entryName, err := format.DecodeCatalogEntry(cell)
		if err != nil {
			t.Fatalf("decode entry: %v", err)
		}
		if string(entryName) == name {
			format.PutU32(cell[4:8], index)
			break
		}
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := file.WriteAt(page, int64(meta.CatalogNameRoot)*format.PageSize); err != nil {
		t.Fatalf("write page: %v", err)
	}
}

func TestCatalogCountScansBothTrees(t *testing.T) {
	creationGate(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := catalogSource(t, path, []string{"alpha", "beta", "gamma"})
	source := mapSource(t, path)
	defer source.Close()
	pages, err := forRecovery(recoveryBudget(1<<20).MaxHeapBytes, meta.PageCount, meta, recoveryBudget(1<<20))
	if err != nil {
		t.Fatalf("page set: %v", err)
	}
	count, err := catalogCount(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("catalogCount: %v", err)
	}
	if count != 6 {
		t.Fatalf("catalog count %d, want 6 (3 name + 3 index records)", count)
	}
}

func TestRecoverCatalogReconcilesEqualConflictFreePairs(t *testing.T) {
	creationGate(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	meta := catalogSource(t, path, []string{"alpha", "beta", "gamma"})
	source := mapSource(t, path)
	defer source.Close()
	budget := recoveryBudget(1 << 20)
	pages, err := forRecovery(budget.MaxHeapBytes, meta.PageCount, meta, budget)
	if err != nil {
		t.Fatalf("page set: %v", err)
	}
	count, err := catalogCount(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("catalogCount: %v", err)
	}
	if err := pages.reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	tables, err := allocateTables(tableCounts{catalog: count}, pages, budget, 0)
	if err != nil {
		t.Fatalf("allocateTables: %v", err)
	}
	rep := newReporter(nil)
	recovered, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	report := rep.finish()
	// The Rust authority counts the occurrences: every name appeared in
	// both catalog trees, so the accepted records proof is 6 and the
	// rejected proof is 0.
	if report.CatalogEntries.Examined != 6 || report.CatalogEntries.Accepted != 6 || report.CatalogEntries.Rejected != 0 {
		t.Fatalf("catalog counts %+v, want 6/6/0", report.CatalogEntries)
	}
	var names []string
	if err := recovered.forEach(tables, func(entry catalogFeed) error {
		names = append(names, string(entry.name))
		return nil
	}); err != nil {
		t.Fatalf("forEach: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("accepted entries %v, want 3", names)
	}
	for index := uint32(0); index < 3; index++ {
		ok, err := recovered.contains(tables, index)
		if err != nil {
			t.Fatalf("contains(%d): %v", index, err)
		}
		if !ok {
			t.Fatalf("feed %d not contained", index)
		}
	}
}

func TestCatalogNameConflictRejectsThePair(t *testing.T) {
	creationGate(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "source.iprdb")
	names := []string{"alpha", "beta"}
	meta := catalogSource(t, path, names)
	rewriteNameIndex(t, path, meta, "alpha", 5)
	source := mapSource(t, path)
	defer source.Close()
	budget := recoveryBudget(1 << 20)
	pages, err := forRecovery(budget.MaxHeapBytes, meta.PageCount, meta, budget)
	if err != nil {
		t.Fatalf("page set: %v", err)
	}
	count, err := catalogCount(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("catalogCount: %v", err)
	}
	if err := pages.reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	tables, err := allocateTables(tableCounts{catalog: count}, pages, budget, 0)
	if err != nil {
		t.Fatalf("allocateTables: %v", err)
	}
	var unknown []RecoveryUnknownEnvelope
	rep := newReporter(RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		unknown = append(unknown, *envelope)
		return RecoverySinkContinue, nil
	}))
	recovered, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	report := rep.finish()
	if report.CatalogEntries.Examined != 4 || report.CatalogEntries.Rejected != 2 {
		t.Fatalf("catalog counts %+v, want 4 examined and 2 rejected", report.CatalogEntries)
	}
	found := false
	for _, envelope := range unknown {
		if envelope.Reason == validation.ReasonCatalogInvalid && envelope.Object == validation.ObjectCatalogNameTree {
			found = true
		}
	}
	if !found {
		t.Fatal("missing catalog name conflict envelope")
	}
	if ok, err := recovered.contains(tables, 5); err != nil || ok {
		t.Fatalf("rewritten feed 5 contained=%v err=%v, want absent", ok, err)
	}
	if ok, err := recovered.contains(tables, 1); err != nil || !ok {
		t.Fatalf("feed 1 contained=%v err=%v, want present", ok, err)
	}
}

// osOpenFileRW opens one file read-write for the test damage helpers.
func osOpenFileRW(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}
