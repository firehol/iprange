// Structure intern and clear allocation pins (SOW-0025 slice C): the
// typed dictionary dedup hit and the structured clear must allocate
// nothing per operation, like the Rust
// structured_value/manager.rs monomorphization and range_mutation
// Option<Rewrite> value semantics. The payload validate takes the
// payload by value, the dictionary codecs are generics over the
// registry entry, the draft owns one intern payload scratch, and the
// interval rewrite travels by value with presence flags, so every
// decode and rewrite local stays on the stack.

package writer

import (
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// putStructuredMetaFieldsForTest writes a minimal empty structured
// network_enrichment_v1 meta page (draft_test.go putMetaFieldsForTest
// structured variant): the membership and structure dictionary limits
// are one, exactly like the Rust empty_meta for the structured kind.
func putStructuredMetaFieldsForTest(page []byte, pages uint64) {
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindStructured
	page[13] = format.StructureKindNetworkEnrichmentV1
	copy(page[16:32], "structured\x00")
	copy(page[32:48], openTestDBID[:])
	format.PutU64(page[48:56], 1)
	copy(page[56:72], openTestNonce[:])
	format.PutU64(page[72:80], pages)
	format.PutU64(page[112:120], 1)
	format.PutU64(page[208:216], 1)
	format.PutU32(page[252:256], format.MetaCRC32C(page))
}

// structuredEmptyDraft opens one fresh empty structured draft over a
// raw two-page database (the pinned paths run over the real opened
// mapping; no owned page exists anywhere).
func structuredEmptyDraft(t *testing.T) *DraftStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "empty-structured.iprdb")
	raw := make([]byte, 8*format.PageSize)
	for i := uint64(0); i < 2; i++ {
		page := raw[i*format.PageSize : (i+1)*format.PageSize]
		copy(page, format.MainMagic[:])
		putStructuredMetaFieldsForTest(page, 8)
	}
	if err := osWriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	nonce := [16]byte{1, 2, 3}
	_, store, _ := openDraftStore(t, path, testBudget(), nonce)
	return store
}

// TestSliceCStructureInternZeroAlloc pins the real intern dedup path
// (DraftStore.internNetworkEnrichmentV1 -> internStructurePayload ->
// generic intern): one seeded record, then repeated interns of the
// equal payload. AllocsPerRun warmup cancels the fresh-draft setup, so
// any nonzero result is a per-intern allocation.
func TestSliceCStructureInternZeroAlloc(t *testing.T) {
	store := structuredEmptyDraft(t)
	value := format.NetworkEnrichmentV1{ASN: 64512, CountryID: 1, StateID: 2, CityID: 3}
	// Seed the record so the measured loop only hits the dedup path.
	if _, err := store.internNetworkEnrichmentV1(value, MembershipHandle{}); err != nil {
		t.Fatal(err)
	}
	const perRun = 20
	allocs := testing.AllocsPerRun(200, func() {
		for i := 0; i < perRun; i++ {
			if _, err := store.internNetworkEnrichmentV1(value, MembershipHandle{}); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Logf("structured intern dedup allocations per %d interns: %.0f", perRun, allocs)
	if allocs != 0 {
		t.Fatalf("structured intern dedup path allocates %.0f objects per %d interns, contract is exactly zero", allocs, perRun)
	}
}

// TestSliceCStructuredClearZeroAlloc pins the structured clear path
// (empty-structure assign -> DraftStore.clear -> trimPredecessor): a
// clear on an empty tree must not allocate per operation (Rust
// range_mutation::clear Option<Rewrite> value semantics).
func TestSliceCStructuredClearZeroAlloc(t *testing.T) {
	store := structuredEmptyDraft(t)
	const perRun = 20
	allocs := testing.AllocsPerRun(200, func() {
		for i := 0; i < perRun; i++ {
			if _, err := store.ClearV4(uint32(1), uint32(100)); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Logf("structured clear allocations per %d clears: %.0f", perRun, allocs)
	if allocs != 0 {
		t.Fatalf("structured clear path allocates %.0f objects per %d clears, contract is exactly zero", allocs, perRun)
	}
}
