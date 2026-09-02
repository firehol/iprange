package reader

// FeedCursor seek tests over a synthetic wide numeric catalog (Rust
// feed_catalog_tests.rs wide_catalog_fixture parity: 150 entries across
// three index leaves under one level-1 branch, so seek and paging tests
// cross leaf boundaries). The committed membership fixtures keep their
// whole catalog in one leaf, so the branch-level seek machinery is
// exercised here against a hand-built tree.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// buildWideCatalogDatabase writes a membership database whose numeric
// catalog tree has one level-1 branch (page 2, children 3..5) and three
// index leaves holding feed-0..feed-149 (50 entries each). The meta
// pages are copied from the direct fixture and patched into a valid
// membership shape with an empty dictionary (multilevel_test.go
// pattern); only the index tree and the in-bounds stub roots are used
// by the FeedCursor tests.
func buildWideCatalogDatabase(t testing.TB) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	const pages = 8
	file := make([][]byte, pages)
	for i := range file {
		file[i] = make([]byte, format.PageSize)
	}
	copy(file[0], raw[:format.PageSize])
	copy(file[1], raw[format.PageSize:2*format.PageSize])
	for _, offs := range []struct{ off, val int }{
		{72, pages}, // page count
		{80, 0},     // range record count (empty dictionary)
		{88, 150},   // active feed count
		{96, 150},   // feed index limit
		{104, 0},    // membership entry count
		{112, 1},    // membership id limit (empty dictionary)
	} {
		format.PutU64(file[0][offs.off:offs.off+8], uint64(offs.val))
		format.PutU64(file[1][offs.off:offs.off+8], uint64(offs.val))
	}
	for _, root := range []struct {
		off, val int
	}{
		{144, 0}, // range root (no ranges)
		{148, 6}, // catalog name root (stub, never walked here)
		{152, 2}, // catalog index root
		{156, 7}, // feed used root (stub, never walked here)
		{160, 0}, // membership id root (empty dictionary)
		{164, 0}, // membership hash root (empty dictionary)
		{168, 0}, // membership used root (empty dictionary)
	} {
		format.PutU32(file[0][root.off:root.off+4], uint32(root.val))
		format.PutU32(file[1][root.off:root.off+4], uint32(root.val))
	}
	for _, meta := range file[:2] {
		meta[11] = format.AddressFamilyIPv4
		meta[12] = format.ValueKindMembership
		meta[13] = 0 // no structure kind
	}

	// Page 2: level-1 index branch with three children (pages 3..5).
	b := file[2]
	copy(b[0:4], format.PageMagic[:])
	b[4] = byte(format.PageTypeCatalogIndexBranch)
	format.PutU64(b[8:16], 2)
	format.PutU16(b[6:8], 32)
	format.PutU16(b[16:18], 3)    // item count
	format.PutU16(b[18:20], 1)    // level
	format.PutU16(b[20:22], 38)   // lower: 32 + 2*item_count
	format.PutU16(b[22:24], 4072) // upper: start of the record area
	format.PutU32(b[24:28], 0)    // aux
	for i, first := range []uint32{0, 50, 100} {
		format.PutU16(b[32+i*2:34+i*2], uint16(4072+i*8))
		format.PutU32(b[4072+i*8:4076+i*8], first)
		format.PutU32(b[4076+i*8:4080+i*8], uint32(3+i))
	}

	// Pages 3..5: index leaves of 50 name records each.
	entryName := func(index int) string {
		// feed-N with N in 0..149: interior '-' allowed by the grammar.
		return "feed-" + itoa(index)
	}
	leaf := func(p, first, n int) {
		page := file[p]
		copy(page[0:4], format.PageMagic[:])
		page[4] = byte(format.PageTypeCatalogIndexLeaf)
		format.PutU64(page[8:16], 2)
		lower, upper := 32+2*n, format.PageSize
		for i := 0; i < n; i++ {
			name := entryName(first + i)
			upper -= 12 + len(name)
		}
		format.PutU16(page[6:8], 32)
		format.PutU16(page[16:18], uint16(n))
		format.PutU16(page[18:20], 0) // level 0
		format.PutU16(page[20:22], uint16(lower))
		format.PutU16(page[22:24], uint16(upper))
		format.PutU32(page[24:28], 0) // aux
		off := upper
		for i := 0; i < n; i++ {
			format.PutU16(page[32+2*i:34+2*i], uint16(off))
			index := first + i
			name := entryName(index)
			format.PutU16(page[off:off+2], uint16(12+len(name)))
			format.PutU16(page[off+2:off+4], 0) // flags
			format.PutU32(page[off+4:off+8], uint32(index))
			page[off+8] = byte(len(name))
			page[off+9], page[off+10], page[off+11] = 0, 0, 0
			copy(page[off+12:off+12+len(name)], name)
			off += 12 + len(name)
		}
	}
	leaf(3, 0, 50)
	leaf(4, 50, 50)
	leaf(5, 100, 50)

	// Pages 6 and 7: in-bounds stub roots (never walked by the index
	// cursor tests); copy the source fixture's first data page so the
	// pages carry plausible content.
	copy(file[6], raw[2*format.PageSize:3*format.PageSize])
	copy(file[7], raw[2*format.PageSize:3*format.PageSize])

	format.PutU32(file[0][252:256], format.MetaCRC32C(file[0]))
	format.PutU32(file[1][252:256], format.MetaCRC32C(file[1]))
	tmp := filepath.Join(t.TempDir(), "wide-catalog.iprdb")
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

// itoa formats one non-negative integer without importing strconv in
// the hot builder loop (the name grammar needs decimal digits only).
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func openWideCatalog(t *testing.T) *ImmutableReader {
	t.Helper()
	r, err := OpenImmutable(buildWideCatalogDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestFeedCursorSeekByIndex mirrors Rust
// feed_cursor_seek_repositions_to_first_entry_at_or_after_target over
// the three-leaf wide catalog: at-or-after repositioning inside a leaf,
// exactly at a leaf boundary, between leaves, before the first entry
// (full sweep restart), past the last entry (finishes), and repeatable
// seeks on a finished cursor.
func TestFeedCursorSeekByIndex(t *testing.T) {
	r := openWideCatalog(t)
	defer r.Close()

	c, err := r.NewFeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := c.Next()
	if err != nil || !ok {
		t.Fatalf("first: ok=%v err=%v", ok, err)
	}
	if first.FeedIndex != 0 || string(first.Name) != "feed-0" {
		t.Fatalf("first = %+v, want feed-0", first)
	}

	for _, step := range []struct {
		target uint32
		want   []uint32
	}{
		{3, []uint32{3}},
		{50, []uint32{50}}, // exact leaf boundary
		{51, []uint32{51}}, // first entry of the next leaf
		{97, []uint32{97}},
		{0, []uint32{0, 1}}, // seek to 0 restarts the sweep
	} {
		if err := c.SeekByIndex(step.target); err != nil {
			t.Fatalf("seek(%d): %v", step.target, err)
		}
		for _, want := range step.want {
			entry, ok, err := c.Next()
			if err != nil || !ok {
				t.Fatalf("seek(%d) next %d: ok=%v err=%v", step.target, want, ok, err)
			}
			if entry.FeedIndex != want {
				t.Fatalf("seek(%d) = %d, want %d", step.target, entry.FeedIndex, want)
			}
		}
	}

	// Seek past the last entry finishes the cursor.
	if err := c.SeekByIndex(150); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.Next(); err != nil || ok {
		t.Fatalf("seek(150) next: ok=%v err=%v", ok, err)
	}

	// Seeking a finished cursor restarts it when the target exists.
	if err := c.SeekByIndex(100); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint32{100, 101, 102} {
		entry, ok, err := c.Next()
		if err != nil || !ok {
			t.Fatalf("seek(100) next %d: ok=%v err=%v", want, ok, err)
		}
		if entry.FeedIndex != want {
			t.Fatalf("seek(100) = %d, want %d", entry.FeedIndex, want)
		}
	}
}

// TestFeedCursorSeekSkipsCountHealthCheck pins the Rust seeked flag:
// after a seek the emitted count no longer covers the whole catalog, so
// reaching exhaustion without emitting active_feed_count entries is
// clean, not the incomplete-count corruption.
func TestFeedCursorSeekSkipsCountHealthCheck(t *testing.T) {
	r := openWideCatalog(t)
	defer r.Close()

	c, err := r.NewFeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SeekByIndex(149); err != nil {
		t.Fatal(err)
	}
	entry, ok, err := c.Next()
	if err != nil || !ok || entry.FeedIndex != 149 {
		t.Fatalf("seek(149) next = (%+v, %v, %v), want 149", entry, ok, err)
	}
	if _, ok, err := c.Next(); err != nil || ok {
		t.Fatalf("exhaustion after seek: ok=%v err=%v", ok, err)
	}
}

// TestFeedCursorPagingMatchesOneUnboundedSweep mirrors Rust
// feed_cursor_paging_matches_one_unbounded_sweep: one cursor per page
// seeked to the retained checkpoint must reproduce the unbounded sweep
// exactly (no entry skipped, none revisited).
func TestFeedCursorPagingMatchesOneUnboundedSweep(t *testing.T) {
	r := openWideCatalog(t)
	defer r.Close()

	// One unbounded sweep visits every entry exactly once.
	var reference []FeedEntry
	{
		c, err := r.NewFeedCursor()
		if err != nil {
			t.Fatal(err)
		}
		for {
			entry, ok, err := c.Next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			reference = append(reference, entry)
		}
	}
	if len(reference) != 150 {
		t.Fatalf("reference sweep emitted %d entries, want 150", len(reference))
	}

	var paged []FeedEntry
	var last *uint32
	for {
		c, err := r.NewFeedCursor()
		if err != nil {
			t.Fatal(err)
		}
		if last != nil {
			if err := c.SeekByIndex(*last + 1); err != nil {
				t.Fatal(err)
			}
		}
		page := make([]FeedEntry, 0, 7)
		for len(page) < 7 {
			entry, ok, err := c.Next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			page = append(page, entry)
		}
		done := len(page) < 7
		if len(page) > 0 {
			index := page[len(page)-1].FeedIndex
			last = &index
		}
		paged = append(paged, page...)
		if done {
			break
		}
	}
	if len(paged) != len(reference) {
		t.Fatalf("paged sweep emitted %d entries, want %d", len(paged), len(reference))
	}
	for i := range reference {
		if paged[i].FeedIndex != reference[i].FeedIndex || string(paged[i].Name) != string(reference[i].Name) {
			t.Fatalf("paged entry %d = %+v, want %+v", i, paged[i], reference[i])
		}
	}
}
