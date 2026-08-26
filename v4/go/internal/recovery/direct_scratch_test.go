package recovery

// Authorized-scratch direct construction tests ported from the Rust
// recovery/direct_scratch_tests.rs: the file-backed page table keeps
// an ordered recovery correct inside a tiny heap, and a sink stop
// during external output cleans every scratch file.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// scratchDirectBudget builds one recovery budget with authorized
// scratch limits (Rust direct_scratch_tests budget).
func scratchDirectBudget(scratchDirectory string, maxOpenFiles uint32, maxScratchFiles uint32) *RecoveryBudget {
	return &RecoveryBudget{
		MaxHeapBytes:     32,
		MaxOutputPages:   20_000,
		MaxOpenFiles:     maxOpenFiles,
		MaxScratchBytes:  16 * 1024,
		MaxScratchFiles:  maxScratchFiles,
		ScratchDirectory: scratchDirectory,
	}
}

// TestOrderedRecoveryUsesOneFileBackedPageTable mirrors Rust
// ordered_recovery_uses_one_file_backed_page_table: the tiny heap
// migrates the page-ownership table into the scratch directory, the
// ordered output is still canonical, and the cleanup proves the
// directory empty.
func TestOrderedRecoveryUsesOneFileBackedPageTable(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	outputPath := filepath.Join(dir, "output.iprdb")
	scratchDirectory := filepath.Join(dir, "scratch")
	if err := os.Mkdir(scratchDirectory, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	ranges := [][3]uint32{{0, 9, 1}, {20, 29, 2}, {40, 49, 3}}
	meta := finishRanges(t, directSourceBuilder(t, sourcePath), ranges)
	source := mapSource(t, sourcePath)
	defer source.Close()
	builder := directOutputBuilder(t, outputPath)
	construction, failure := directConstruct(source, meta, builder, scratchDirectBudget(scratchDirectory, 3, 1), nil, nil)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if construction.scratch == nil {
		t.Fatal("construction carried no scratch cleanup")
	}
	if !construction.scratch.clean() {
		t.Fatal("scratch cleanup reports residues after a clean recovery")
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	assertRanges(t, outputRanges(t, outputPath), ranges)
	entries, err := os.ReadDir(scratchDirectory)
	if err != nil {
		t.Fatalf("ReadDir scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory left %d entries, want 0", len(entries))
	}
}

// TestSinkStopDuringExternalOutputCleansEveryScratchFile mirrors Rust
// sink_stop_during_external_output_cleans_every_scratch_file: the
// damaged source forces the external sort, the tree-order damage is
// reported first, the overlap damage stops the sink, and the failure
// terminal proves every scratch file removed.
func TestSinkStopDuringExternalOutputCleansEveryScratchFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	outputPath := filepath.Join(dir, "output.iprdb")
	scratchDirectory := filepath.Join(dir, "scratch")
	if err := os.Mkdir(scratchDirectory, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	ranges := make([][3]uint32, 0, 120)
	for index := uint32(0); index < 120; index++ {
		from := index * 3
		ranges = append(ranges, [3]uint32{from, from + 1, index % 7})
	}
	meta := finishRanges(t, directSourceBuilder(t, sourcePath), ranges)
	rewriteSecondStart(t, sourcePath, meta, 1)
	swapFirstTwoRecords(t, sourcePath, meta)
	source := mapSource(t, sourcePath)
	defer source.Close()
	sawOrderDamage := false
	sink := RecoverySinkFunc(func(envelope *RecoveryUnknownEnvelope) (RecoverySinkControl, error) {
		switch envelope.Reason {
		case validation.ReasonTreeOrderInvalid:
			sawOrderDamage = true
			return RecoverySinkContinue, nil
		case validation.ReasonRangeOverlap:
			return RecoverySinkStop, nil
		default:
			return RecoverySinkContinue, nil
		}
	})
	builder := directOutputBuilder(t, outputPath)
	_, failure := directConstruct(source, meta, builder, scratchDirectBudget(scratchDirectory, 4, 2), nil, sink)
	if failure == nil {
		t.Fatal("construct succeeded with a sink stop")
	}
	if !sawOrderDamage {
		t.Fatal("missing TreeOrderInvalid envelope")
	}
	if !isCode(failure.cause, format.CodeStoppedBySink) {
		t.Fatalf("failure cause %v, want StoppedBySink", failure.cause)
	}
	if failure.scratch == nil {
		t.Fatal("failure carried no scratch cleanup")
	}
	if !failure.scratch.clean() {
		t.Fatal("scratch cleanup reports residues after a sink stop")
	}
	entries, err := os.ReadDir(scratchDirectory)
	if err != nil {
		t.Fatalf("ReadDir scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory left %d entries, want 0", len(entries))
	}
	failure.builder.Close()
}

// TestDisorderedDirectRecoveryUsesBoundedMultiPassScratch mirrors
// Rust direct_tests.disordered_direct_recovery_uses_bounded_multi_pass_scratch:
// a tiny heap forces the multi-pass scratch sort of a disordered
// source, the cleanup proves the directory empty, and the canonical
// output survives order damage.
func TestDisorderedDirectRecoveryUsesBoundedMultiPassScratch(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.iprdb")
	outputPath := filepath.Join(dir, "output.iprdb")
	scratchDirectory := filepath.Join(dir, "scratch")
	if err := os.Mkdir(scratchDirectory, 0o700); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	ranges := make([][3]uint32, 0, 120)
	for index := uint32(0); index < 120; index++ {
		from := index * 3
		ranges = append(ranges, [3]uint32{from, from + 1, index % 7})
	}
	meta := finishRanges(t, directSourceBuilder(t, sourcePath), ranges)
	swapFirstTwoRecords(t, sourcePath, meta)
	source := mapSource(t, sourcePath)
	defer source.Close()
	construction, failure := directConstruct(source, meta, directOutputBuilder(t, outputPath), scratchDirectBudget(scratchDirectory, 4, 2), nil, nil)
	if failure != nil {
		t.Fatalf("construct: %v", failure.cause)
	}
	if construction.scratch == nil {
		t.Fatal("external sort recorded no scratch attempt")
	}
	if !construction.scratch.clean() {
		t.Fatal("scratch cleanup reports residues")
	}
	if attemptID := construction.scratch.attemptID; attemptID == [16]byte{} {
		t.Fatal("scratch attempt id is all zero")
	}
	if construction.report.Ranges.Accepted != uint64(len(ranges)) {
		t.Fatalf("accepted %d, want %d", construction.report.Ranges.Accepted, len(ranges))
	}
	if err := construction.finished.Close(); err != nil {
		t.Fatalf("Close finished output: %v", err)
	}
	assertRanges(t, outputRanges(t, outputPath), ranges)
	entries, err := os.ReadDir(scratchDirectory)
	if err != nil {
		t.Fatalf("ReadDir scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory left %d entries, want 0", len(entries))
	}
	validateClean(t, outputPath)
}
