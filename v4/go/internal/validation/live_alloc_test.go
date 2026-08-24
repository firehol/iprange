//go:build linux || darwin

package validation

// Allocation pin of the live validation sweep (regression guard for
// the range-walk per-record escape): the sweep of a dense committed
// generation must not allocate per range record. The walk cursors
// hold the previous record by value with a presence flag, so a
// 2000-record sweep stays near the fixed per-page overhead (measured
// ~255 allocations at this HEAD; the pre-fix pointer-cursor form
// added one heap object per record, ~2255). The generous bound
// catches that shape while tolerating Go-version fixed-overhead
// drift.

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

func TestValidateLiveSweepAllocationPin(t *testing.T) {
	main := filepath.Join(t.TempDir(), "db.iprdb")
	if _, err := live.CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 2, nil); err != nil {
		t.Fatal(err)
	}
	w, err := live.OpenLiveWriter(main, writer.PageBudget{MaxHeapBytes: 1 << 20, MaxPrivatePages: 4096, MaxGrowthPages: 4096, MaxOpenFiles: 2}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.BeginDirect(); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 2000; i++ {
		if _, err := w.AssignV4(i*100+1, i*100+50, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Commit(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(3, func() {
		result, failure := Validate(main, ValidationModeLiveCurrent, HeapOnly(1<<20, 2), nil, nil)
		if failure != nil || result == nil || !result.Valid {
			t.Fatalf("validate: failure %v result %+v", failure, result)
		}
	})
	if allocs > 1024 {
		t.Fatalf("live sweep allocations %.0f, want <= 1024 (per-record allocation regression)", allocs)
	}
}
