// Direct-join batch emission (SOW-0027 milestone-4 external review):
// emit must deliver the optional direct values from one bounded arena
// per batch, never one heap scalar per mapped cell (Rust emits
// Option<u32> by value under the batch-lifetime contract).

package reader

import (
	"fmt"
	"testing"
)

func TestJoinDirectEmitAllocatesNothingPerCell(t *testing.T) {
	const cellsPerTable = 300
	scope := &ScopeData{entries: make([]FeedEntry, 4)}
	for i := range scope.entries {
		scope.entries[i] = FeedEntry{Name: []byte(fmt.Sprintf("feed-%03d", i))}
	}
	// Every cell is mapped with a distinct feed and direct value; the
	// unmapped cell (direct == 0) is exercised as well.
	table := &joinDirectTable{cells: make([]joinDirectCell, 0, cellsPerTable)}
	for i := 0; i < cellsPerTable; i++ {
		direct := uint64(i%3 + 1) // 1..3: mapped; alternate zero for unmapped
		feed := uint32(i % 4)
		if i%7 == 0 {
			direct = 0 // unmapped total cell
		}
		table.cells = append(table.cells, joinDirectCell{feed: feed, direct: direct})
	}

	delivered := 0
	var values uint64
	allocs := testing.AllocsPerRun(64, func() {
		delivered = 0
		values = 0
		if err := table.emit(scope, nil, func(batch []DirectJoinCell) error {
			delivered += len(batch)
			for i := range batch {
				if batch[i].DirectValue != nil {
					values += uint64(*batch[i].DirectValue)
				}
			}
			return nil
		}); err != nil {
			t.Fatal("emit:", err)
		}
	})
	// One batch backing slice per run; a per-cell heap scalar would add
	// one allocation per mapped cell (about 257 of 300 here).
	if allocs > 2 {
		t.Fatalf("emit allocated %.2f objects/run for %d cells: the optional direct values are allocated per cell", allocs, cellsPerTable)
	}
	if delivered != cellsPerTable {
		t.Fatalf("emit delivered %d cells, want %d", delivered, cellsPerTable)
	}
	if values == 0 || values%2 == 1 {
		t.Fatalf("emitted direct values look corrupted: sum %d", values)
	}
}

// TestJoinDirectEmitOrdersRowsPerSpec pins the normative
// iprange.v1.join.direct row order: each feed ascending by direct value
// with the uncovered (null) cell last. A covered cell whose wire
// direct_value is 0 is encoded as cell.direct 1, so it must emit before
// the uncovered cell (cell.direct == 0).

func TestJoinDirectEmitOrdersRowsPerSpec(t *testing.T) {
	scope := &ScopeData{entries: []FeedEntry{
		{Name: []byte("feed-a")},
		{Name: []byte("feed-b")},
	}}
	// Direct encoding: cell.direct = wire direct_value + 1 for covered
	// cells; cell.direct == 0 is the uncovered cell. Cells are added in
	// scrambled order so the assertion proves emit sorts, not insertion.
	table := &joinDirectTable{}
	table.cells = append(table.cells,
		joinDirectCell{feed: 0, direct: 3}, // covered, wire 2
		joinDirectCell{feed: 1, direct: 1}, // covered, wire 0
		joinDirectCell{feed: 0, direct: 0}, // uncovered
		joinDirectCell{feed: 0, direct: 1}, // covered, wire 0
		joinDirectCell{feed: 1, direct: 0}, // uncovered
	)

	var got []string
	if err := table.emit(scope, nil, func(batch []DirectJoinCell) error {
		for _, cell := range batch {
			if cell.DirectValue == nil {
				got = append(got, string(cell.Feed)+":null")
			} else {
				got = append(got, fmt.Sprintf("%s:%d", cell.Feed, *cell.DirectValue))
			}
		}
		return nil
	}); err != nil {
		t.Fatal("emit:", err)
	}
	want := []string{"feed-a:0", "feed-a:2", "feed-a:null", "feed-b:0", "feed-b:null"}
	if len(got) != len(want) {
		t.Fatalf("emit delivered %d rows, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}
