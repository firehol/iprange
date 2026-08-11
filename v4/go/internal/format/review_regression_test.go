package format

import "testing"

// Regression tests for review findings: crafted pages must produce typed
// errors, never panics, and the metadata compressed bound is part of
// bootstrap validity.

// TestSlottedPageRejectsOutOfPageGeometry crafts slotted pages whose upper or
// slot offsets exceed the page size and requires typed rejection.
func TestSlottedPageRejectsOutOfPageGeometry(t *testing.T) {
	cases := []struct {
		name     string
		upper    uint16
		slot     uint16
		wantOpen bool // whether OpenSlotted itself must reject
	}{
		{"upper-above-page", 60000, 0, true},
		{"slot-above-page", 64, 60000, false},
		{"slot-at-page-end", 64, PageSize, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := make([]byte, PageSize)
			copy(page[0:4], PageMagic[:])
			page[4] = byte(PageTypeRangeLeaf)
			PutU16(page[6:8], 32)
			PutU64(page[8:16], 1)
			PutU16(page[16:18], 1)
			PutU16(page[20:22], 34)
			PutU16(page[22:24], tc.upper)
			PutU32(page[24:28], 4) // range aux
			PutU16(page[32:34], tc.slot)
			sl, err := OpenSlotted(page, 2, PageTypeRangeLeaf, 4, SlotItemsPerPage)
			if tc.wantOpen {
				if err == nil {
					t.Fatal("expected open rejection")
				}
				return
			}
			if err != nil {
				t.Fatal("unexpected open rejection:", err)
			}
			rec, err := sl.Record(0)
			if err == nil && tc.slot == PageSize {
				// A record exactly at the page end yields a zero-length
				// slice; the codec must reject it (safety: no panic).
				if len(rec) != 0 {
					t.Fatalf("expected zero-length slice, got %d", len(rec))
				}
				if _, derr := DecodeRangeRecordV4(rec); derr == nil {
					t.Fatal("expected codec rejection of empty record")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected record rejection, got %d bytes", len(rec))
			}
		})
	}
}

// TestMetaRejectsCompressedLengthBound crafts a meta whose compressed length
// exceeds the section-11 bound and requires rejection at bootstrap validity.
func TestMetaRejectsCompressedLengthBound(t *testing.T) {
	m, ok := ParseIdentity(fixtureBytes(t)[:PageSize])
	if !ok {
		t.Fatal("fixture meta not identity-readable")
	}
	m.MetadataCompressed = 1 << 40
	if err := m.ValidateKindInvariants(); err == nil {
		t.Fatal("out-of-bound compressed length accepted")
	}
	// A bound-compliant value still passes.
	m.MetadataCompressed = MetadataCompressedBound(m.MetadataUncompressed)
	if err := m.ValidateKindInvariants(); err != nil {
		t.Fatalf("bound-compliant compressed length rejected: %v", err)
	}
}

// TestCatalogReservedBytesRejected crafts catalog records with nonzero
// reserved bytes and requires rejection.
func TestCatalogReservedBytesRejected(t *testing.T) {
	// name leaf record: len=14, flags 0, feed 1, name_len 2, reserved [9:12]
	// nonzero, name "ab".
	rec := make([]byte, 32)
	PutU16(rec[0:2], 14)
	PutU32(rec[4:8], 1)
	rec[8] = 2
	rec[9] = 1
	rec[12] = 'a'
	rec[13] = 'b'
	if _, err := DecodeCatalogNameRecord(rec); err == nil {
		t.Fatal("catalog record with reserved bytes accepted")
	}
	rec[9] = 0
	if _, err := DecodeCatalogNameRecord(rec); err != nil {
		t.Fatalf("clean catalog record rejected: %v", err)
	}
	// branch record with reserved bytes nonzero.
	br := make([]byte, 32)
	PutU16(br[0:2], 14)
	PutU32(br[4:8], 2)
	br[8] = 2
	br[11] = 1
	br[12] = 'a'
	br[13] = 'b'
	if _, err := DecodeCatalogNameBranch(br); err == nil {
		t.Fatal("catalog branch with reserved bytes accepted")
	}
	br[11] = 0
	if _, err := DecodeCatalogNameBranch(br); err != nil {
		t.Fatalf("clean catalog branch rejected: %v", err)
	}
}
