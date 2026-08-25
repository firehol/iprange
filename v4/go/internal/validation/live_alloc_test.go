//go:build linux || darwin

package validation

// Allocation pin of the live validation sweep (regression guard for
// the range-walk per-record escape): the sweep of a dense committed
// generation must not allocate per range record or per page. The
// walk cursors hold the previous record by value with a presence
// flag, the proof and header travel by value, and the refusal
// envelopes copy the page number inside the cold arms, so a
// 2000-record sweep costs only the fixed one-time open machinery
// (measured ~103 allocations per Validate call at this HEAD: the
// directory/mapping open path, not the sweep). The bound catches the
// per-record pointer-cursor form (~2000 extra) while tolerating
// Go-version fixed-overhead drift.

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

func TestValidateLiveSweepAllocationPin(t *testing.T) {
	liveGate(t)
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
	t.Logf("live sweep allocations: %.0f", allocs)
	if allocs > 200 {
		t.Fatalf("live sweep allocations %.0f, want <= 200 (per-record or per-page allocation regression)", allocs)
	}
}
