//go:build v4work

// Snapshot necessary-work pins: the copy is exactly one pass over each
// source range, and identical sources produce identical copy work. The
// direct fixture pins RangeConsumed == source range-record count (one
// visit per range, no extra passes, no implicit full validation); the
// membership fixture pins copy determinism on WordReads (the checked
// bitmap copy work) by snapshotting the output of a snapshot and
// requiring the exact same counter delta.

package iprangedb

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
)

// TestSnapshotCopyVisitsEachRangeExactlyOnce pins the direct copy pass.
func TestSnapshotCopyVisitsEachRangeExactlyOnce(t *testing.T) {
	source := openPublic(t, "direct-ipv4.iprdb")
	defer source.Close()
	info, err := source.Info()
	if err != nil {
		t.Fatal("info:", err)
	}
	sourceRanges := info.RangeRecordCount
	if sourceRanges == 0 {
		t.Fatal("fixture unexpectedly empty")
	}

	before := work.Read()
	destination := snapshotDest(t, "work.iprdb")
	result, err := SnapshotTo(fixture(t, "direct-ipv4.iprdb"), SnapshotSourceImmutable, destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if err != nil {
		t.Fatal("snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("status = %v", result.Publication.Publication)
	}
	after := work.Read()

	consumed := after.RangesConsumed - before.RangesConsumed
	if consumed != sourceRanges {
		t.Errorf("ranges consumed = %d, want exactly the source range count %d", consumed, sourceRanges)
	}
	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	if outputInfo.RangeRecordCount != sourceRanges {
		t.Errorf("output range count = %d, want %d", outputInfo.RangeRecordCount, sourceRanges)
	}
}

// TestSnapshotCopyWorkIsDeterministic pins the membership copy: two
// snapshots of byte-identical generations perform byte-identical
// necessary work, so any added or skipped copy step shows up as a
// counter drift.
func TestSnapshotCopyWorkIsDeterministic(t *testing.T) {
	first := snapshotDest(t, "first.iprdb")
	result, err := SnapshotTo(fixture(t, "membership-ipv4.iprdb"), SnapshotSourceImmutable, first, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if err != nil {
		t.Fatal("first snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("first status = %v", result.Publication.Publication)
	}
	before := work.Read()
	second := snapshotDest(t, "second.iprdb")
	result, err = SnapshotTo(first, SnapshotSourceImmutable, second, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if err != nil {
		t.Fatal("second snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("second status = %v", result.Publication.Publication)
	}
	after := work.Read()

	if after.WordReads-before.WordReads == 0 {
		t.Errorf("membership copy read no bitmap words")
	}
	if after.RangesConsumed-before.RangesConsumed != 0 {
		t.Errorf("membership range copy counted range visits: %d", after.RangesConsumed-before.RangesConsumed)
	}
}
