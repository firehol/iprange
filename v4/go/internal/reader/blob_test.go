package reader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Synthetic blob-backed membership database.
//
// The six committed conformance fixtures store every membership bitmap
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
	putU64(meta0, 96, 38400) // feed_index_limit: 600 words = 38400 bits
	putU64(meta1, 96, 38400)
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
	// Canonical slotted geometry: lower == 32+2, upper == 4096-12.
	p := file[2]
	header(p, format.PageTypeRangeLeaf, 4, 0, 1, 34, 4084)
	format.PutU16(p[32:34], 4084)
	format.PutU32(p[4084:4088], 0x0a000000)
	format.PutU32(p[4088:4092], 0x0a0000ff)
	format.PutU32(p[4092:4096], 1)

	// Page 3: membership ID leaf, one blob-stored record.
	p = file[3]
	header(p, format.PageTypeMembershipIDLeaf, 0, 0, 1, 34, 4032)
	format.PutU16(p[32:34], 4032)
	format.PutU16(p[4032:4034], 64) // record_len
	p[4034] = 1                     // storage: blob
	format.PutU32(p[4036:4040], 1)  // membership id
	format.PutU64(p[4040:4048], 1)  // refcount
	format.PutU32(p[4048:4052], wordCount)
	format.PutU32(p[4052:4056], bitmapLen)
	format.PutU32(p[4056:4060], 4) // blob root

	// Page 4: blob branch, two entries over two leaves.
	p = file[4]
	header(p, format.PageTypeBlobBranch, 1, 1, 2, 36, 4064)
	format.PutU16(p[32:34], 4064)
	format.PutU16(p[34:36], 4080)
	format.PutU64(p[4064:4072], 0)
	format.PutU32(p[4072:4076], 5)
	format.PutU64(p[4080:4088], 4048)
	format.PutU32(p[4088:4092], 6)

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

// TestBlobReadWordsAcrossLeafBoundary pins the batched blob-membership
// read contract: ReadWords must cross a blob-leaf boundary by copying
// per-leaf chunks (blob_tree.rs read_words_from), not fail on it. The
// synthetic database stores words 0..505 in leaf 1 and 506..599 in leaf 2;
// a batch starting at word 505 spans both leaves. This fails on the
// single-span implementation with "blob leaf does not cover the requested
// bytes" (code 32).
func TestBlobReadWordsAcrossLeafBoundary(t *testing.T) {
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

	// Crosses the 505/506 leaf boundary.
	got := make([]uint64, 4)
	n, err := view.ReadWords(505, got)
	if err != nil {
		t.Fatalf("batched read across leaf boundary: %v", err)
	}
	if n != 4 || got[0] != 0xdeadbeef || got[1] != 0xdeadbeef || got[2] != 0 || got[3] != 0 {
		t.Fatalf("words 505..508 = %x n=%d", got, n)
	}

	// Batch starting exactly at the second leaf.
	if n, err := view.ReadWords(506, got[:1]); err != nil || n != 1 || got[0] != 0xdeadbeef {
		t.Fatalf("word 506 = %x n=%d err=%v", got[0], n, err)
	}

	// Full-span batch over both leaves, ending at the canonical last word.
	all := make([]uint64, view.WordCount())
	n, err = view.ReadWords(0, all)
	if err != nil {
		t.Fatalf("full-span batched read: %v", err)
	}
	if n != 600 || all[0] != 0x8000000000000001 || all[505] != 0xdeadbeef ||
		all[506] != 0xdeadbeef || all[599] != 0x1 {
		t.Fatalf("full-span words mismatch at boundary: n=%d first=%x w505=%x w506=%x w599=%x",
			n, all[0], all[505], all[506], all[599])
	}

	// Per-word reads at the boundary stay correct (no cross-talk).
	for word, want := range map[uint32]uint64{504: 0, 505: 0xdeadbeef, 506: 0xdeadbeef, 507: 0} {
		got, ok, err := view.Word(word)
		if err != nil || !ok || got != want {
			t.Fatalf("word %d = %x ok=%v err=%v want %x", word, got, ok, err, want)
		}
	}
}
