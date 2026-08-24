package recovery

// Indirect recovery analysis (Rust recovery/membership.rs): one
// analyze pass counts and recovers the catalog, membership, and
// structure tables over the page-ownership set, analyzes the ranges,
// and reads the metadata inside the heap retained by the tables.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// indirectAnalysis is the membership-backed recovery analysis (Rust
// IndirectAnalysis): the completed report, the readable-records count,
// the order proof, the reconciled tables, the metadata, and the
// page-ownership set.
type indirectAnalysis struct {
	report          RecoveryReport
	readableRecords uint64
	ordered         bool
	catalog         *catalog
	memberships     *membershipIndex
	structures      *structureIndex
	tables          *tableStore
	metadata        []byte
	pages           *pageSet
}

// indirectAnalyze runs the indirect recovery analysis (Rust
// membership::analyze: the budget and cancellation preflight, the
// value-kind proof, the page set over half the heap, and the graph
// analysis; every later failure carries the page-set terminal).
func indirectAnalyze(m *mapping.Mapping, meta format.Meta, budget *RecoveryBudget, check func() error, sink RecoverySink, kind uint8) (*indirectAnalysis, *analysisFailure) {
	if err := budget.validate(); err != nil {
		return nil, analysisFailureOf(err, RecoveryReport{}, nil)
	}
	if err := live.Checkpoint(check); err != nil {
		return nil, analysisFailureOf(err, RecoveryReport{}, nil)
	}
	if (kind != format.ValueKindMembership && kind != format.ValueKindStructured) || meta.ValueKind != kind {
		return nil, analysisFailureOf(&format.Error{Code: format.CodeWrongValueKind, Detail: "indirect recovery value kind does not match its source"}, RecoveryReport{}, nil)
	}
	physicalPages := m.Size() / format.PageSize
	expected := meta.PageCount
	if physicalPages < expected {
		expected = physicalPages
	}
	rep := newReporter(sink)
	pages, err := forRecovery(budget.MaxHeapBytes/2, expected, meta, budget)
	if err != nil {
		return nil, analysisFailureOf(err, rep.finish(), nil)
	}
	result, err := indirectAnalyzeGraphs(m, meta, budget, check, pages, rep)
	report := rep.finish()
	if err != nil {
		return nil, analysisFailureWithPages(pages, err, report)
	}
	return result, nil
}

// indirectAnalyzeGraphs runs the ordered graph analysis (Rust
// analyze_graphs: the tables, the catalog and membership and structure
// recovery, the range analysis, and the retained-heap metadata read).
func indirectAnalyzeGraphs(m *mapping.Mapping, meta format.Meta, budget *RecoveryBudget, check func() error, pages *pageSet, rep *reporter) (*indirectAnalysis, error) {
	tables, err := indirectPrepareTables(m, meta, budget, check, pages)
	if err != nil {
		return nil, err
	}
	catalogRecovered, memberships, structures, err := indirectRecoverTables(m, meta, check, pages, rep, tables)
	if err != nil {
		return nil, err
	}
	codec, ok := indirectCodec(meta.AddressFamily)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery indirect family is invalid"}
	}
	readable, ordered, err := analyzeRanges(codec, m, meta, pages, check, rep)
	if err != nil {
		return nil, err
	}
	metadata, err := readMetadataRetained(m, meta, pages, tables.retainedBytes(), budget.MaxHeapBytes, check, rep)
	if err != nil {
		return nil, err
	}
	return &indirectAnalysis{
		report:          rep.finish(),
		readableRecords: readable,
		ordered:         ordered,
		catalog:         catalogRecovered,
		memberships:     memberships,
		structures:      structures,
		tables:          tables,
		metadata:        metadata,
		pages:           pages,
	}, nil
}

// indirectPrepareTables counts and allocates the recovery tables (Rust
// prepare_tables: the catalog, membership, and structure counts, the
// page-set reset, and the allocation inside the required table heap
// reserve).
func indirectPrepareTables(m *mapping.Mapping, meta format.Meta, budget *RecoveryBudget, check func() error, pages *pageSet) (*tableStore, error) {
	catalogCount, err := catalogCount(m, meta, pages, check)
	if err != nil {
		return nil, err
	}
	membershipCount, err := membershipCount(m, meta, pages, check)
	if err != nil {
		return nil, err
	}
	structureCount, err := countStructures(m, meta, pages, check)
	if err != nil {
		return nil, err
	}
	if err := pages.reset(); err != nil {
		return nil, err
	}
	reserve, err := requiredTableHeapReserve(meta)
	if err != nil {
		return nil, err
	}
	return allocateTables(tableCounts{catalog: catalogCount, memberships: membershipCount, structures: structureCount}, pages, budget, reserve)
}

// indirectRecoverTables reconciles the recovery tables in order (Rust
// recover_tables: the catalog, then the memberships over the catalog,
// then the structures over the memberships).
func indirectRecoverTables(m *mapping.Mapping, meta format.Meta, check func() error, pages *pageSet, rep *reporter, tables *tableStore) (*catalog, *membershipIndex, *structureIndex, error) {
	catalogRecovered, err := recoverCatalog(m, meta, pages, tables, check, rep)
	if err != nil {
		return nil, nil, nil, err
	}
	memberships, err := recoverMemberships(m, meta, catalogRecovered, pages, tables, check, rep)
	if err != nil {
		return nil, nil, nil, err
	}
	structures, err := recoverStructures(m, meta, memberships, pages, tables, check, rep)
	if err != nil {
		return nil, nil, nil, err
	}
	return catalogRecovered, memberships, structures, nil
}

// countStructures counts the recovery-readable structure records (Rust
// count_structures: the kind dispatch of one source).
func countStructures(m *mapping.Mapping, meta format.Meta, pages *pageSet, check func() error) (uint64, error) {
	switch {
	case meta.ValueKind == format.ValueKindMembership && meta.StructureKind == format.StructureKindNone:
		return 0, nil
	case meta.ValueKind == format.ValueKindStructured && meta.StructureKind == format.StructureKindNetworkEnrichmentV1:
		// The structure dictionary recovery is delivered by the
		// follow-up slice D3b (structure_index.rs port); until then the
		// structured recovery refuses with the Rust unsupported class.
		return 0, &format.Error{Code: format.CodeUnsupportedStructure, Detail: "recovery structure dictionary is not yet implemented"}
	default:
		return 0, &format.Error{Code: format.CodeUnsupportedStructure, Detail: "recovery structure kind is unsupported"}
	}
}

// recoverStructures recovers the structure dictionary of one source
// (Rust recover_structures: the kind dispatch of one source; the
// membership source has no structures).
func recoverStructures(m *mapping.Mapping, meta format.Meta, memberships *membershipIndex, pages *pageSet, tables *tableStore, check func() error, rep *reporter) (*structureIndex, error) {
	switch {
	case meta.ValueKind == format.ValueKindMembership && meta.StructureKind == format.StructureKindNone:
		return nil, nil
	case meta.ValueKind == format.ValueKindStructured && meta.StructureKind == format.StructureKindNetworkEnrichmentV1:
		return nil, &format.Error{Code: format.CodeUnsupportedStructure, Detail: "recovery structure dictionary is not yet implemented"}
	default:
		return nil, &format.Error{Code: format.CodeUnsupportedStructure, Detail: "recovery structure kind is unsupported"}
	}
}

// requiredTableHeapReserve is the fixed recovery heap reserved beside
// the tables (Rust required_table_heap_reserve: the metadata
// uncompressed payout with the inflater overhead, and the range record
// of the source family).
func requiredTableHeapReserve(meta format.Meta) (uint64, error) {
	var metadata uint64
	if meta.MetadataRoot != 0 {
		total, ok := checkedAdd(meta.MetadataUncompressed, metadataInflateOverhead)
		if !ok {
			return 0, overflowError("recovery metadata heap")
		}
		metadata = total
	}
	var rangeRecord uint64
	if meta.RangeRoot != 0 {
		switch meta.AddressFamily {
		case format.AddressFamilyIPv4:
			rangeRecord = 12
		case format.AddressFamilyIPv6:
			rangeRecord = 40
		}
	}
	total, ok := checkedAdd(metadata, rangeRecord)
	if !ok {
		return 0, overflowError("recovery table heap reserve")
	}
	return total, nil
}
