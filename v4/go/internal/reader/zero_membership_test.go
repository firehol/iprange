package reader

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestMembershipZeroIDRejectedAtAggregation pins the Rust by_id contract:
// a range record naming membership ID 0 must fail Corrupt instead of
// decoding as an empty selection. The Go scratch used to short-circuit on
// its zero marker, so an ID-0 range silently vanished; Rust's
// Option<MembershipToken> always resolves the first ID and
// membership_view.rs by_id refuses 0 with "range names the empty
// membership ID". The same scratch serves the direct and membership
// joins, so one aggregate probe covers all three surfaces.
func TestMembershipZeroIDRejectedAtAggregation(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "ms-zero-id.iprdb")
	orig := openFixture(t, "membership-ipv4.iprdb")

	// Descend the range tree to the leftmost leaf (entry 0 of every
	// branch page) so the patched record is the first range scanned.
	cur := orig.meta.RangeRoot
	level := uint16(0)
	first := true
	var leafPage uint32
	leafFound := false
	for {
		page, err := orig.page(cur)
		if err != nil {
			t.Fatal(err)
		}
		h, err := format.DecodePageHeader(page, orig.meta.TxnID)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			level = h.Level
			first = false
		} else if h.Level != level {
			t.Fatalf("range level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(orig.meta.AddressFamily), format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		switch h.PageType {
		case format.PageTypeRangeBranch:
			b, err := sl.Record(0)
			if err != nil {
				t.Fatal(err)
			}
			_, child, err := format.DecodeRangeEntryV4(b)
			if err != nil || !format.PageNumberValid(child, orig.meta.PageCount) {
				t.Fatalf("range child: %d err %v", child, err)
			}
			cur, level = child, level-1
		case format.PageTypeRangeLeaf:
			leafPage = cur
			leafFound = true
		default:
			t.Fatalf("unexpected range page type %d", h.PageType)
		}
		if leafFound {
			break
		}
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	page := mustRead(t, file, int(leafPage), 0, format.PageSize)
	h, err := format.DecodePageHeader(page, orig.meta.TxnID)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := format.OpenSlottedHeader(page, h, h.PageType, uint32(orig.meta.AddressFamily), format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	recOff, err := sl.SlotOffset(0)
	if err != nil {
		t.Fatal(err)
	}
	// RangeRecordV4 value occupies bytes 8:12.
	if _, err := file.WriteAt(make([]byte, 4), int64(int(leafPage)*format.PageSize+int(recOff)+8)); err != nil {
		t.Fatal(err)
	}
	file.Close()
	orig.Close()

	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	scope, err := r.ResolveAllFeeds(1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.AggregateScope(scope, format.AddressFamilyIPv4, AggregationCardinalities, "", nil, nil, nil, nil)
	if err == nil {
		t.Fatal("aggregation over a membership ID 0 range: want Corrupt, got nil")
	}
	var ferr *format.Error
	if !errors.As(err, &ferr) || ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("aggregation error = %v, want CodeFormatInvalid", err)
	}
	if !strings.Contains(ferr.Detail, "range names the empty membership ID") {
		t.Fatalf("aggregation detail = %q, want the Rust by_id message", ferr.Detail)
	}
}
