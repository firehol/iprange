// Structure intern allocation pins (SOW-0025 slice C): the typed
// dictionary dedup hit must allocate nothing per intern, like the Rust
// structured_value/manager.rs monomorphization. The payload validate
// takes the payload by value and the dictionary codecs are generics
// over the registry entry, so the shape-stenciled decode keeps its
// payload on the stack.

package writer

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestSliceCStructureInternZeroAlloc pins the intern dedup path: one
// seeded record, then repeated interns of the equal payload. AllocsPerRun
// warmup builds the second fresh draft, so the fresh-draft setup cancels
// on subtraction and any nonzero result is a per-intern allocation.
func TestSliceCStructureInternZeroAlloc(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.iprdb")
	raw := make([]byte, 8*format.PageSize)
	for i := uint64(0); i < 2; i++ {
		page := raw[i*format.PageSize : (i+1)*format.PageSize]
		copy(page, format.MainMagic[:])
		putMetaFieldsForTest(page, 8)
	}
	if err := osWriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	nonce := [16]byte{1, 2, 3}
	_, store, _ := openDraftStore(t, path, testBudget(), nonce)
	state := structureState{idLimit: 1}
	payload, err := encodeNetworkEnrichmentV1(format.NetworkEnrichmentV1{ASN: 64512, CountryID: 1, StateID: 2, CityID: 3}, 4)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the record so the measured loop only hits the dedup path.
	if _, err := internStructure(structureNetworkEnrichmentV1{}, store, &state, &payload); err != nil {
		t.Fatal(err)
	}
	store.storeStructureState(state)
	const perRun = 20
	allocs := testing.AllocsPerRun(200, func() {
		for i := 0; i < perRun; i++ {
			if _, err := internStructure(structureNetworkEnrichmentV1{}, store, &state, &payload); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Logf("structure intern dedup allocations per %d interns: %.0f", perRun, allocs)
	if allocs != 0 {
		t.Fatalf("structure intern dedup path allocates %.0f objects per %d interns, contract is exactly zero", allocs, perRun)
	}
}
