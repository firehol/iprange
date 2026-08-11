package reader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Synthetic blob-backed membership database.
//
// The five committed conformance fixtures store every membership bitmap
// inline (the v4 fixture uses 2 words, the v6 fixture 1 word), so the blob
// tree with a branch level was never exercised. This test hand-builds a
// small valid v4 membership database whose single bitmap is 4,800 bytes =
// 600 words, stored in two blob leaves under one blob branch, and then reads
// words across the leaf boundary through the public lookup path. Page CRCs
// are intentionally absent: ordinary access never verifies them (only meta
// CRCs are checked at bootstrap, and those are recomputed here).

func buildBlobDatabase(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "rust", "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	const pages = 7
	file := make([][]byte, pages)
	for i := range file {
		file[i] = make([]byte, format.PageSize)
	}
	copy(file[0], raw[:format.PageSize])
	copy(file[1], raw[format.PageSize:2*format.PageSize])

	// Meta patch to a membership database with one feed, one range, one
	// blob-backed membership entry, and a 7-page committed extent.
	meta0, meta1 := file[0], file[1]
	meta0[12] = format.ValueKindMembership
	meta1[12] = format.ValueKindMembership
	putU64(meta0, 72, 7)
	putU64(meta1, 72, 7)
	putU64(meta0, 80, 1) // range_record_count
	putU64(meta1, 80, 1)
	putU64(meta0, 88, 1) // active_feed_count
	putU64(meta1, 88, 1)
	putU64(meta0, 96, 64) // feed_index_limit: bits up to 63 observable
	putU64(meta1, 96, 64)
	putU64(meta0, 104, 1) // membership_entry_count
	putU64(meta1, 104, 1)
	putU64(meta0, 112, 2) // membership_id_limit
	putU64(meta1, 112, 2)
	putU64(meta0, 120, 0) // metadata lengths 0
	putU64(meta1, 120, 0)
	putU64(meta0, 128, 0)
	putU64(meta1, 128, 0)
	putU32(meta0, 148, 3) // catalog name root
	putU32(meta1, 148, 3)
	putU32(meta0, 152, 3) // catalog index root
	putU32(meta1, 152, 3)
	putU32(meta0, 156, 3) // feed used root
	putU32(meta1, 156, 3)
	putU32(meta0, 160, 3) // membership id root
	putU32(meta1, 160, 3)
	putU32(meta0, 164, 3) // membership hash root
	putU32(meta1, 164, 3)
	putU32(meta0, 168, 3) // membership used root
	putU32(meta1, 168, 3)
	putU32(meta0, 172, 0) // metadata root
	putU32(meta1, 172, 0)
	for _, meta := range [][]byte{meta0, meta1} {
		format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	}

	const wordCount = 600
	const bitmapLen = wordCount * 8 // 4800 bytes: two blob leaves

	// Page 2: range leaf, one record 10.0.0.0-255 = membership 1.
	p := file[2]
	header(p, format.PageTypeRangeLeaf, 4, 0, 1, 46, 46)
	format.PutU16(p[32:34], 46)
	format.PutU32(p[46:50], 0x0a000000)
	format.PutU32(p[50:54], 0x0a0000ff)
	format.PutU32(p[54:58], 1)

	// Page 3: membership ID leaf, one blob-stored record.
	p = file[3]
	header(p, format.PageTypeMembershipIDLeaf, 0, 0, 1, 96, 96)
	format.PutU16(p[32:34], 96)
	format.PutU16(p[96:98], 64)  // record_len
	p[98] = 1                    // storage: blob
	format.PutU32(p[100:104], 1) // membership id
	format.PutU64(p[104:112], 1) // refcount
	format.PutU32(p[112:116], wordCount)
	format.PutU32(p[116:120], bitmapLen)
	format.PutU32(p[120:124], 4) // blob root

	// Page 4: blob branch, two entries over two leaves.
	p = file[4]
	header(p, format.PageTypeBlobBranch, 1, 1, 2, 48, 48)
	format.PutU16(p[32:34], 48)
	format.PutU16(p[34:36], 64)
	format.PutU64(p[48:56], 0)
	format.PutU32(p[56:60], 5)
	format.PutU64(p[64:72], 4048)
	format.PutU32(p[72:76], 6)

	// Page 5: blob leaf 1, offsets 0..4047 (words 0..505).
	p = file[5]
	header(p, format.PageTypeBlobLeaf, 1, 0, 1, 4096, 4096)
	format.PutU64(p[32:40], 0)
	format.PutU16(p[40:42], 4048)
	// word 0 = bits 0 and 63; word 505 = 0xdeadbeef.
	format.PutU64(p[48:56], 0x8000000000000001)
	format.PutU64(p[48+4040:48+4048], 0xdeadbeef)

	// Page 6: blob leaf 2, offsets 4048..4799 (words 506..599).
	p = file[6]
	header(p, format.PageTypeBlobLeaf, 1, 0, 1, 752+48, 4096)
	format.PutU64(p[32:40], 4048)
	format.PutU16(p[40:42], 752)
	format.PutU64(p[48:56], 0xdeadbeef)  // word 506 (first word of leaf 2)
	format.PutU64(p[48+744:48+752], 0x1) // word 599 (last word of leaf 2)

	dir := t.TempDir()
	path := filepath.Join(dir, "blob.iprdb")
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range file {
		if _, err := out.Write(page); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func header(p []byte, typ format.PageType, aux uint32, level uint16, itemCount uint16, lower, upper uint16) {
	copy(p[0:4], format.PageMagic[:])
	p[4] = byte(typ)
	format.PutU16(p[6:8], 32)
	format.PutU64(p[8:16], 2) // born txn: committed by txn 2
	format.PutU16(p[16:18], itemCount)
	format.PutU16(p[18:20], level)
	format.PutU16(p[20:22], lower)
	format.PutU16(p[22:24], upper)
	format.PutU32(p[24:28], aux)
}

func putU64(p []byte, off int, v uint64) { format.PutU64(p[off:off+8], v) }
func putU32(p []byte, off int, v uint32) { format.PutU32(p[off:off+4], v) }

// TestBlobBranchDescent opens the synthetic blob-backed membership database
// and reads words across the branch and the leaf boundary.
func TestBlobBranchDescent(t *testing.T) {
	path := buildBlobDatabase(t)
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatalf("open synthetic blob database: %v", err)
	}
	defer r.Close()

	view, found, err := r.LookupMembership4(0x0a000000)
	if err != nil || !found {
		t.Fatalf("membership lookup: %v %v", found, err)
	}
	if view.WordCount() != 600 {
		t.Fatalf("word count %d", view.WordCount())
	}
	// ContainsIndex across words.
	for _, idx := range []uint32{0, 63} {
		has, err := view.ContainsIndex(idx)
		if err != nil || !has {
			t.Fatalf("feed %d: %v %v", idx, has, err)
		}
	}
	has, err := view.ContainsIndex(1)
	if err != nil || has {
		t.Fatalf("feed 1: %v %v", has, err)
	}
	// Word reads at the leaf boundary and at both edges.
	checks := []struct {
		word uint32
		want uint64
	}{
		{0, 0x8000000000000001},
		{505, 0xdeadbeef},
		{506, 0xdeadbeef},
		{599, 0x1},
	}
	for _, c := range checks {
		got, ok, err := view.Word(c.word)
		if err != nil || !ok {
			t.Fatalf("word %d: %v %v", c.word, ok, err)
		}
		if got != c.want {
			t.Fatalf("word %d = %x want %x", c.word, got, c.want)
		}
	}
	// Out of range word.
	if _, ok, err := view.Word(600); err != nil || ok {
		t.Fatalf("out-of-range word: %v %v", ok, err)
	}
	// Absent address does not resolve a membership.
	if _, found, err := r.LookupMembership4(0x0b000000); err != nil || found {
		t.Fatalf("absent address: %v %v", found, err)
	}
}
