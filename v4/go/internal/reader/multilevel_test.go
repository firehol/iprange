package reader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Synthetic multi-level range tree.
//
// Every committed Rust fixture stores its range set in a single leaf page
// (4 / 3 / 3 / 3 / 4 records), so the level-1 range branch was never
// exercised by the corpus. This test hand-builds a direct IPv4 database
// with 900 ranges spread over four leaves under one level-1 branch, then
// probes boundaries across leaves and scans the full enumeration.

const multilevelRecordCount = 900

func buildMultiLevelDatabase(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	const pages = 13
	file := make([][]byte, pages)
	for i := range file {
		file[i] = make([]byte, format.PageSize)
	}
	copy(file[0], raw[:format.PageSize])
	copy(file[1], raw[format.PageSize:2*format.PageSize])
	for _, offs := range []struct{ off, val int }{
		{72, pages},                 // page count
		{80, multilevelRecordCount}, // range record count
	} {
		format.PutU64(file[0][offs.off:offs.off+8], uint64(offs.val))
		format.PutU64(file[1][offs.off:offs.off+8], uint64(offs.val))
	}

	// Page 2: level-1 branch with four children (pages 3..6).
	b := file[2]
	copy(b[0:4], format.PageMagic[:])
	b[4] = byte(format.PageTypeRangeBranch)
	format.PutU64(b[8:16], 2)
	format.PutU16(b[6:8], 32)
	format.PutU16(b[18:20], 1)    // level
	format.PutU16(b[16:18], 4)    // item count
	format.PutU16(b[20:22], 40)   // lower: exactly 32 + 2*item_count
	format.PutU16(b[22:24], 4064) // upper: start of the record area
	format.PutU32(b[24:28], 4)    // aux = family (v4)
	for i, first := range []uint32{0, 290000, 580000, 870000} {
		format.PutU32(b[4064+i*8:4068+i*8], first)
		format.PutU32(b[4068+i*8:4072+i*8], uint32(3+i))
		format.PutU16(b[32+i*2:34+i*2], uint16(4064+i*8))
	}

	// Pages 3..6: leaves of 290/290/290/30 records.
	const perLeaf = 290
	leaf := func(p, first, n int) {
		page := file[p]
		copy(page[0:4], format.PageMagic[:])
		page[4] = byte(format.PageTypeRangeLeaf)
		format.PutU64(page[8:16], 2)
		lower, upper := 32+2*n, 4096-12*n
		format.PutU16(page[6:8], 32)
		format.PutU16(page[16:18], uint16(n))
		format.PutU16(page[18:20], 0) // level 0
		format.PutU16(page[20:22], uint16(lower))
		format.PutU16(page[22:24], uint16(upper))
		format.PutU32(page[24:28], 4)
		for i := 0; i < n; i++ {
			format.PutU16(page[32+2*i:34+2*i], uint16(upper+12*i))
			from := uint32(first + i)
			format.PutU32(page[upper+12*i:upper+12*i+4], from*1000)
			format.PutU32(page[upper+12*i+4:upper+12*i+8], from*1000+999)
			format.PutU32(page[upper+12*i+8:upper+12*i+12], (from%9)+1)
		}
	}
	leaf(3, 0, perLeaf)
	leaf(4, perLeaf, perLeaf)
	leaf(5, 2*perLeaf, perLeaf)
	leaf(6, 3*perLeaf, 30)

	format.PutU32(file[0][252:256], format.MetaCRC32C(file[0]))
	format.PutU32(file[1][252:256], format.MetaCRC32C(file[1]))
	tmp := filepath.Join(t.TempDir(), "multilevel.iprdb")
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	for _, p := range file {
		if _, err := out.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	return tmp
}

func TestMultiLevelRangeTree(t *testing.T) {
	path := buildMultiLevelDatabase(t)
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	probes := []struct {
		addr  uint32
		value uint32
		found bool
	}{
		{0, 1, true}, {999, 1, true}, {499, 1, true},
		{289999, 2, true}, {290000, 3, true}, {290500, 3, true},
		{579999, 4, true}, {580000, 5, true}, {580999, 5, true},
		{869999, 6, true}, {870000, 7, true}, {899999, 9, true},
		{899981, 9, true}, {900000, 0, false},
	}
	for _, p := range probes {
		v, found, err := r.LookupDirect4(p.addr)
		if err != nil {
			t.Fatal(err)
		}
		if found != p.found || (found && v != p.value) {
			t.Errorf("addr %d: (%d,%v) want (%d,%v)", p.addr, v, found, p.value, p.found)
		}
	}
	// Exact full enumeration: 900 records, ascending, canonical values.
	count := 0
	var prev uint32
	err = r.ScanDirect4(func(v RangeVisit4) error {
		if count > 0 && v.From <= prev {
			t.Errorf("not ascending at %d", count)
		}
		prev = v.From
		wantFrom := uint32(count) * 1000
		if v.From != wantFrom || v.To != wantFrom+999 || v.Value != uint32((count%9)+1) {
			t.Errorf("record %d = %d-%d=%d", count, v.From, v.To, v.Value)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != multilevelRecordCount {
		t.Errorf("enumerated %d records, want %d", count, multilevelRecordCount)
	}
}
