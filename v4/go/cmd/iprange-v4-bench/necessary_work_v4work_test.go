//go:build v4work

// Necessary-work comparison evidence (SOW-0027 direction item 6): this
// test replays the nested-overwrite and live-direct-random-lookup
// benchmark scenarios over the same deterministic workloads through the
// public SDK and prints the work.Snapshot as tab-separated rows
// (go <scenario> <field> <count>). The Rust peer in
// iprange-livedb/src/necessary_work_tests.rs prints the same rows for
// the same workloads; the evidence CSV in the SOW compares the counts
// to prove the Go reader/writer performs no more necessary work than
// Rust (descents, probes, parses, replacement/split work, bytes moved).

package main

import (
	"fmt"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/work"
)

// goWorkRows maps the Go snapshot to the Rust snake-case field names.
func goWorkRows(snap work.Snapshot) []struct {
	field string
	value uint64
} {
	return []struct {
		field string
		value uint64
	}{
		{"tree_lookups", snap.TreeLookups},
		{"tree_descents", snap.TreeDescents},
		{"pages_visited", snap.PagesVisited},
		{"page_parses", snap.PagesParsed},
		{"key_probes", snap.KeyProbes},
		{"cell_probes", snap.CellProbes},
		{"leaf_validations", snap.LeafValidations},
		{"bitmap_probes", snap.BitmapProbes},
		{"slot_reads", snap.SlotReads},
		{"slot_scan_steps", snap.SlotScanSteps},
		{"edit_fit_probes", snap.EditFitProbes},
		{"pages_created", snap.PagesCreated},
		{"pages_copied", snap.PagesCopied},
		{"pages_split", snap.PagesSplit},
		{"first_fence_updates", snap.FirstFenceUpdates},
		{"edge_path_checks", snap.EdgePathChecks},
		{"leaf_locator_hits", snap.LeafLocatorHits},
		{"leaf_locator_misses", snap.LeafLocatorMisses},
		{"leaf_locator_fallbacks", snap.LeafLocatorFallbacks},
		{"pages_retired", snap.PagesRetired},
		{"pages_reclaimed", snap.PagesReclaimed},
		{"pages_sealed", snap.PagesSealed},
		{"ranges_consumed", snap.RangesConsumed},
		{"ranges_emitted", snap.RangesEmitted},
		{"ranges_split", snap.RangesSplit},
		{"ranges_coalesced", snap.RangesCoalesced},
		{"catalog_lookups", snap.CatalogLookups},
		{"catalog_interns", snap.CatalogInterns},
		{"membership_lookups", snap.MembershipLookups},
		{"membership_interns", snap.MembershipInterns},
		{"structure_decodes", snap.StructureDecodes},
		{"structure_interns", snap.StructureInterns},
		{"mapping_growths", snap.MappingGrowths},
		{"mapping_remaps", snap.MappingRemaps},
		{"mapping_flushes", snap.MappingFlushes},
		{"file_syncs", snap.FileSyncs},
		{"bytes_moved", snap.BytesMoved},
		{"bytes_zeroed", snap.BytesZeroed},
		{"membership_refcount_batches", snap.MembershipRefcountBatches},
		{"membership_delta_spills", snap.MembershipDeltaSpills},
		{"source_passes", snap.SourcePasses},
		{"input_source_passes", snap.InputSourcePasses},
		{"output_passes", snap.OutputPasses},
		{"history_window_tests", snap.HistoryWindowTests},
		{"membership_decodes", snap.MembershipDecodes},
		{"membership_decode_cache_hits", snap.MembershipDecodeCacheHits},
		{"membership_word_reads", snap.MembershipWordReads},
		{"membership_combinations", snap.MembershipCombinations},
		{"membership_intern_cache_hits", snap.MembershipInternCacheHits},
		{"aggregation_contributions", snap.AggregationContributions},
		{"aggregation_results", snap.AggregationResults},
		{"join_advances", snap.JoinAdvances},
	}
}

func dumpGoWork(scenario string, snap work.Snapshot) {
	for _, row := range goWorkRows(snap) {
		fmt.Printf("go\t%s\t%s\t%d\n", scenario, row.field, row.value)
	}
}

// TestDumpNecessaryWork prints the Go necessary-work snapshot for the two
// SOW-0027 direction item 6 scenarios (nested-overwrite and
// live-direct-random-lookup) at size 100,000. The tests are evidence
// generation, not assertions: run with -count=1 and capture stdout.
func TestDumpNecessaryWork(t *testing.T) {
	const n = 100000

	// Write scenario: nested overwrite, one complete replacement through
	// the live writer (the bench directNested flow, measured region
	// identical to applyDirect: open through close).
	tag, err := directTag([]byte("timestamp"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := newTestDatabase("necessary-write")
	if err != nil {
		t.Fatal(err)
	}
	defer db.cleanup()
	if _, err := iprangedb.CreateLive(db.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 1, nil); err != nil {
		t.Fatal(err)
	}
	input, err := newNestedSource(n)
	if err != nil {
		t.Fatal(err)
	}
	work.Reset()
	if err := applyDirect(db, input, n); err != nil {
		t.Fatal(err)
	}
	dumpGoWork("nested-overwrite", work.Read())

	// Read scenario: live direct random lookups over a seeded database
	// (the bench scenarioLiveDirectRandomLookup flow; the lookup loop is
	// the measured region, exactly like the bench).
	db, err = readSeededDirect("necessary-read", n, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.cleanup()
	points, err := randomPoints(n)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := iprangedb.OpenLiveReader(db.main, nil)
	if err != nil {
		t.Fatal(err)
	}
	repetitions, _, err := readerWork(n)
	if err != nil {
		t.Fatal(err)
	}
	work.Reset()
	if _, err := countRandomPoints(points, repetitions, func(address iprangedb.IPv4) (bool, error) {
		_, found, err := reader.LookupDirectV4(address)
		if err != nil {
			return false, err
		}
		return found, nil
	}); err != nil {
		t.Fatal(err)
	}
	dumpGoWork("live-direct-random-lookup", work.Read())
	if err := closeLiveReader(reader); err != nil {
		t.Fatal(err)
	}
}
