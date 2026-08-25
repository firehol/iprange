package recovery

// Allocation pins for the recovered-output hot paths (Rust authority:
// recovery/catalog_table.rs record accessor, recovery/membership_output.rs
// and recovery/structured_output.rs by-value flows). Each pin runs over a
// warmed fixture and pins the allocations of one per-record path:
//   - the catalog contains chain (the per-set-bit membership proof) must
//     decode every record as an arena view with no per-record heap work;
//   - the membership and structured output pushes must keep the policy
//     and push machinery allocation-free apart from the documented
//     resolve-token box imposed by the range-components any seam and the
//     writer intern hasher, both recorded as the tight bound.

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// pinTables builds one recovery table store sized by every counted
// record class of one source (catalog, membership, structure).
func pinTables(t *testing.T, source *mapping.Mapping, meta format.Meta) (*pageSet, *tableStore) {
	t.Helper()
	budget := recoveryBudget(1 << 22)
	pages, err := forRecovery(budget.MaxHeapBytes/2, meta.PageCount, meta, budget)
	if err != nil {
		t.Fatalf("pin page set: %v", err)
	}
	catalogRecords, err := catalogCount(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("pin catalog count: %v", err)
	}
	membershipRecords, err := membershipCount(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("pin membership count: %v", err)
	}
	structureRecords, err := countStructureRecords(source, meta, pages, nil)
	if err != nil {
		t.Fatalf("pin structure count: %v", err)
	}
	if err := pages.reset(); err != nil {
		t.Fatalf("pin page reset: %v", err)
	}
	tables, err := allocateTables(tableCounts{catalog: catalogRecords, memberships: membershipRecords, structures: structureRecords}, pages, budget, 0)
	if err != nil {
		t.Fatalf("pin tables: %v", err)
	}
	return pages, tables
}

// TestCatalogContainsRecordPathZeroAllocs pins the catalog record
// accessor through the per-set-bit contains chain of the membership
// proof (Rust Catalog::contains): the index probe, the record decode,
// the name probe, and the accepted-name proof must all view the store
// arena and allocate nothing per call.
func TestCatalogContainsRecordPathZeroAllocs(t *testing.T) {
	layout, err := newTableLayout(tableCounts{catalog: 4})
	if err != nil {
		t.Fatalf("catalog layout: %v", err)
	}
	tables := &tableStore{layout: layout, bytes: make([]byte, int(layout.bytes))}
	rep := newReporter(nil)
	builder := newCatalogBuilder(tables)
	for _, feed := range []catalogFeed{
		{name: []byte("alpha"), index: 0},
		{name: []byte("beta"), index: 1},
	} {
		if err := builder.push(feed, rep); err != nil {
			t.Fatalf("catalog push %q: %v", feed.name, err)
		}
	}
	catalog, err := builder.finish(rep)
	if err != nil {
		t.Fatalf("catalog finish: %v", err)
	}
	for _, index := range []uint32{0, 1, 9} {
		if _, err := catalog.contains(tables, index); err != nil {
			t.Fatalf("warm contains(%d): %v", index, err)
		}
	}
	allocs := testing.AllocsPerRun(16, func() {
		for _, index := range []uint32{0, 1, 9} {
			if _, err := catalog.contains(tables, index); err != nil {
				t.Fatalf("contains(%d): %v", index, err)
			}
		}
	})
	t.Logf("catalog contains chain allocs per run: %v", allocs)
	if allocs != 0 {
		t.Fatalf("catalog contains chain allocs %v per run, want 0", allocs)
	}
}

// pushDrive feeds one monotonically increasing measured pass into one
// output policy, so every measured run appends canonical non-adjacent
// ranges to the warmed destination (Rust Builder::push order rules).
type pushDrive struct {
	base uint32
}

// records returns one ordered pass of four gapped ranges.
func (d *pushDrive) records(value uint32) []rangeRecord {
	base := d.base
	d.base += 200
	return []rangeRecord{
		{from: rangeKey{hi: uint64(base)}, to: rangeKey{hi: uint64(base + 9)}, value: value},
		{from: rangeKey{hi: uint64(base + 20)}, to: rangeKey{hi: uint64(base + 29)}, value: value},
		{from: rangeKey{hi: uint64(base + 40)}, to: rangeKey{hi: uint64(base + 49)}, value: value},
		{from: rangeKey{hi: uint64(base + 60)}, to: rangeKey{hi: uint64(base + 69)}, value: value},
	}
}

// measureMembershipPush drives the membership policy over a warmed
// recovery output and returns the allocations of one measured pass.
func measureMembershipPush(t *testing.T) float64 {
	t.Helper()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	meta := membershipSource(t, sourcePath, [][2]any{{"alpha", uint32(0)}}, []membershipRange{
		{from: 0, to: 9, words: writer.OutputWords{1}},
	})
	source := mapSource(t, sourcePath)
	defer source.Close()
	pages, tables := pinTables(t, source, meta)
	rep := newReporter(nil)
	catalog, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	memberships, err := recoverMemberships(source, meta, catalog, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverMemberships: %v", err)
	}
	locator, found, err := memberships.get(tables, 1)
	if err != nil || !found || locator.rejected {
		t.Fatalf("membership id 1 recovered found=%v rejected=%v err=%v", found, locator.rejected, err)
	}
	out := membershipOutputBuilder(t, filepath.Join(dir, "out.iprdb"), meta)
	if err := out.PushFeed("alpha", 0); err != nil {
		t.Fatalf("destination PushFeed: %v", err)
	}
	policy := &membershipOutput{
		mapping:     source,
		meta:        meta,
		memberships: memberships,
		tables:      tables,
		builder:     out,
		rep:         newReporter(nil),
		family:      format.AddressFamilyIPv4,
	}
	drive := &pushDrive{}
	for warm := 0; warm < 2; warm++ {
		if err := driveMembership(policy, drive.records(locator.id)); err != nil {
			t.Fatalf("warm membership pass: %v", err)
		}
	}
	allocs := testing.AllocsPerRun(8, func() {
		if err := driveMembership(policy, drive.records(locator.id)); err != nil {
			t.Fatalf("membership pass: %v", err)
		}
	})
	t.Logf("membership output-push path allocs per run: %v", allocs)
	return allocs
}

// membershipPushAllocBound is the measured per-4-record bound of the
// membership push path after the arena/by-value fixes: one resolve
// seam box per record (the range-components any seam) plus one writer
// intern hasher per push. Every policy-internal allocation is gone.
const membershipPushAllocBound = 8

// driveMembership runs one measured membership policy pass: every
// record resolves, the gapped ranges push through the coalescer, and
// the pending range closes on finish (Rust MEMBERSHIP_OUTPUT accept
// plus finish_output).
func driveMembership(policy *membershipOutput, records []rangeRecord) error {
	for _, record := range records {
		resolved, err := policy.resolve(record)
		if err != nil {
			return err
		}
		if err := policy.accept(record, resolved); err != nil {
			return err
		}
	}
	return policy.finish()
}

// measureStructuredPush drives the structured policy over a warmed
// recovery output and returns the allocations of one measured pass.
func measureStructuredPush(t *testing.T) float64 {
	t.Helper()
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	meta := structuredSourceLimit(t, sourcePath, 64, [][2]any{{"alpha", uint32(0)}}, []structuredPush{
		{from: 0, to: 9, value: enrichment(64512), membership: writer.OutputWords{1}},
	})
	source := mapSource(t, sourcePath)
	defer source.Close()
	pages, tables := pinTables(t, source, meta)
	rep := newReporter(nil)
	catalog, err := recoverCatalog(source, meta, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverCatalog: %v", err)
	}
	memberships, err := recoverMemberships(source, meta, catalog, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverMemberships: %v", err)
	}
	structures, err := recoverStructureRecords(source, meta, memberships, pages, tables, nil, rep)
	if err != nil {
		t.Fatalf("recoverStructureRecords: %v", err)
	}
	structure, err := structures.record(tables, 0)
	if err != nil {
		t.Fatalf("structure record: %v", err)
	}
	if structure.rejected || structure.membershipID == 0 {
		t.Fatalf("structure rejected=%v membershipID=%d, want a valid membership-backed record", structure.rejected, structure.membershipID)
	}
	out := structuredOutputBuilder(t, filepath.Join(dir, "out.iprdb"), meta)
	if err := out.PushFeed("alpha", 0); err != nil {
		t.Fatalf("destination PushFeed: %v", err)
	}
	policy := &structuredOutput{
		mapping:     source,
		meta:        meta,
		memberships: memberships,
		structures:  structures,
		tables:      tables,
		builder:     out,
		rep:         newReporter(nil),
		family:      format.AddressFamilyIPv4,
	}
	drive := &pushDrive{}
	for warm := 0; warm < 2; warm++ {
		if err := driveStructured(policy, drive.records(structure.id)); err != nil {
			t.Fatalf("warm structured pass: %v", err)
		}
	}
	allocs := testing.AllocsPerRun(8, func() {
		if err := driveStructured(policy, drive.records(structure.id)); err != nil {
			t.Fatalf("structured pass: %v", err)
		}
	})
	t.Logf("structured output-push path allocs per run: %v", allocs)
	return allocs
}

// driveStructured runs one measured structured policy pass: every
// record resolves and pushes its decoded value with the optional
// membership words (Rust NetworkEnrichmentV1Output::accept).
func driveStructured(policy *structuredOutput, records []rangeRecord) error {
	for _, record := range records {
		resolved, err := policy.resolve(record)
		if err != nil {
			return err
		}
		if err := policy.accept(record, resolved); err != nil {
			return err
		}
	}
	return policy.finish()
}

// TestRecoveryOutputPushPathAllocPin pins the combined output-push
// path: the membership and the structured policy passes over a warmed
// destination must allocate only the documented bounds (the resolve
// seam box per record and the writer intern hasher per push), with no
// policy-internal per-record allocation left.
func TestRecoveryOutputPushPathAllocPin(t *testing.T) {
	creationGate(t)
	membershipAllocs := measureMembershipPush(t)
	structuredAllocs := measureStructuredPush(t)
	t.Logf("membership push path: %v allocs per 4-record pass (bound %d)", membershipAllocs, membershipPushAllocBound)
	t.Logf("structured push path: %v allocs per 4-record pass (bound %d)", structuredAllocs, structuredPushAllocBound)
	if membershipAllocs > membershipPushAllocBound {
		t.Fatalf("membership push path allocs %v per pass, want <= %d", membershipAllocs, membershipPushAllocBound)
	}
	if structuredAllocs > structuredPushAllocBound {
		t.Fatalf("structured push path allocs %v per pass, want <= %d", structuredAllocs, structuredPushAllocBound)
	}
}

// structuredPushAllocBound is the measured per-4-record bound of the
// structured push path after the value-shaped fixes: one resolve seam
// box per record plus the writer intern machinery for the structure
// payload and its membership bitmap per push. Every policy-internal
// allocation is gone.
const structuredPushAllocBound = 16
