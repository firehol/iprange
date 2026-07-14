package iprangedb

import (
	"math"
	"path/filepath"
	"testing"
)

// Re-audit 2: comprehensive tests for the remaining v4 issues.
//
//   C1 — MVCC atomicity: a file-backed reader pins its transaction snapshot
//        across two subsequent writer commits.
//   C2 — ExtSort randomized last-wins: 30 overlapping random ranges sorted with
//        ChunkSize = 3 (≈10 spill runs) must assign every IP the scope of the
//        LAST input record covering it.
//   C3 — CRC validation on open: openWriter must reject a file whose two meta
//        pages both fail CRC.
//   C4 — bitmap scope width cap: ScopeIntern must reject a bitmap wider than
//        MaxBitmapWidth (256).
//   A1 — 100 real churn cycles (delete + reinsert + commit each) must not grow
//        the file unboundedly.

// xorshift32 — deterministic, no external deps.
type rng uint32

func (r *rng) next() uint32 {
	x := uint32(*r)
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	*r = rng(x)
	return x
}

// ── C1: MVCC atomicity — reader pins snapshot across writer commits ──────────

func TestReaudit2_C1_MvccReaderPinsSnapshotAcrossCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reaudit2_c1.iprdb")

	// txn 1: insert 1000 records; key 500 carries scope 11.
	func() {
		fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); i < 1000; i++ {
			if err := fw.Set(Ipv4Key(i), Ipv4Key(i), 11); err != nil {
				t.Fatal(err)
			}
		}
		if err := fw.Commit(1); err != nil {
			t.Fatal(err)
		}
		if err := fw.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Open a reader AFTER txn 1 — it pins the txn-1 snapshot.
	mr, err := OpenMmap(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	if s, ok := mustLookup(t, mr, 500); !ok || s != 11 {
		t.Fatalf("txn1 scope at 500 = %d ok=%v, want 11", s, ok)
	}

	// txn 2: overwrite key 500 -> scope 22.
	func() {
		fw, err := OpenFile[Ipv4Key](path)
		if err != nil {
			t.Fatal(err)
		}
		defer fw.Close()
		if err := fw.Set(Ipv4Key(500), Ipv4Key(500), 22); err != nil {
			t.Fatal(err)
		}
		if err := fw.Commit(2); err != nil {
			t.Fatal(err)
		}
	}()

	// txn 3: full churn.
	func() {
		fw, err := OpenFile[Ipv4Key](path)
		if err != nil {
			t.Fatal(err)
		}
		defer fw.Close()
		for i := uint32(0); i < 1000; i++ {
			fw.Delete(Ipv4Key(i), Ipv4Key(i))
		}
		for i := uint32(0); i < 1000; i++ {
			if err := fw.Set(Ipv4Key(i), Ipv4Key(i), 33); err != nil {
				t.Fatal(err)
			}
		}
		if err := fw.Commit(3); err != nil {
			t.Fatal(err)
		}
	}()

	// The reader pinned at txn 1 MUST still observe scope 11 for key 500.
	for _, i := range []uint32{0, 1, 250, 500, 999} {
		if s, ok := mustLookup(t, mr, i); !ok || s != 11 {
			t.Fatalf("MVCC violation at key %d: saw %d ok=%v, want 11", i, s, ok)
		}
	}
}

func mustLookup(t *testing.T, mr *MmapReader, key uint32) (uint32, bool) {
	t.Helper()
	r, err := mr.Reader()
	if err != nil {
		t.Fatal(err)
	}
	return r.LookupV4(Ipv4Key(key))
}

// ── C2: ExtSort randomized last-wins across many spill runs ──────────────────

func TestReaudit2_C2_ExtSortRandomizedLastWins(t *testing.T) {
	// 30 random overlapping ranges over [0, 200) with scopes in [1, 10].
	r := rng(0xDEADBEEF)
	const span = 200
	input := make([]DesiredRecord[Ipv4Key], 0, 30)
	for i := 0; i < 30; i++ {
		a := r.next() % span
		b := r.next() % span
		from, to := a, b
		if from > to {
			from, to = to, from
		}
		scope := (r.next() % 10) + 1
		input = append(input, DesiredRecord[Ipv4Key]{
			From: Ipv4Key(from), To: Ipv4Key(to), ScopeID: scope,
		})
		_ = i
	}

	// Brute-force reference: expected[ip] = scope of the LAST input record
	// covering ip.
	expected := make([]int, span+1) // 0 = uncovered
	for _, rec := range input {
		for ip := uint32(rec.From); ip <= uint32(rec.To); ip++ {
			expected[ip] = int(rec.ScopeID)
		}
	}

	// ChunkSize = 3 => ~10 spill runs, exercising cross-run last-wins merge.
	s := NewExtSorter[Ipv4Key](&ExtSortConfig{ChunkSize: 3, TempDir: t.TempDir()})
	for _, rec := range input {
		if err := s.Add(rec.From, rec.To, rec.ScopeID); err != nil {
			t.Fatal(err)
		}
	}
	stream, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}

	// (a) scope correctness + coverage.
	covered := make([]bool, span+1)
	mismatches := 0
	for rec := stream.Next(); rec != nil; rec = stream.Next() {
		for ip := uint32(rec.From); ip <= uint32(rec.To); ip++ {
			covered[ip] = true
			if expected[ip] != int(rec.ScopeID) {
				mismatches++
				if mismatches <= 5 {
					t.Logf("C2 scope mismatch at ip=%d: ext_sort=%d expected=%d",
						ip, rec.ScopeID, expected[ip])
				}
			}
		}
	}
	if mismatches != 0 {
		t.Fatalf("ext_sort last-wins violated at %d IPs", mismatches)
	}

	// (b) coverage must equal the reference.
	for ip := uint32(0); ip <= span; ip++ {
		gotCovered := covered[ip]
		wantCovered := expected[ip] != 0
		if gotCovered != wantCovered {
			t.Fatalf("coverage mismatch at ip=%d: covered=%v expected=%v",
				ip, gotCovered, wantCovered)
		}
	}
}

// ── C3: openWriter must reject a file whose meta pages fail CRC ──────────────
//
// The core in-memory openWriter decodes both meta pages by txn_id without
// verifying their per-page CRC32C. A torn write or byte corruption that leaves
// the bytes decodable (but CRC-invalid) is silently accepted. The file-backed
// openers (OpenFile, OpenMmap) DO check CRC; the gap is the core open path used
// by every non-file consumer (and by reopen-over-image tests).
func TestReaudit2_C3_WriterOpenRejectsCorruptMeta(t *testing.T) {
	// Build a valid, CRC-sealed image.
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(1), Ipv4Key(10), 7); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(0, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected vecPageStore")
	}
	if len(img) < 2*PageSize {
		t.Fatal("image must hold two meta pages")
	}

	// Sanity: the clean image verifies.
	if !verifyPage(img[:PageSize]) {
		t.Fatal("clean meta page 0 must verify")
	}

	// Corrupt a body byte (NOT the checksum field [8..16)) on BOTH meta pages.
	// MetaCreatedUnix (42) is not validated by openWriter.
	bad := append([]byte(nil), img...)
	bad[MetaCreatedUnix] ^= 0xFF
	bad[PageSize+MetaCreatedUnix] ^= 0xFF

	if verifyPage(bad[:PageSize]) {
		t.Fatal("corrupted meta page 0 must fail CRC")
	}
	if verifyPage(bad[PageSize : 2*PageSize]) {
		t.Fatal("corrupted meta page 1 must fail CRC")
	}

	// openWriter MUST reject the file.
	if _, err := openWriter[Ipv4Key](newVecPageStore(bad)); err == nil {
		t.Fatal("openWriter accepted a CRC-corrupt file (missing CRC validation on open)")
	}
}

// ── C4: ScopeIntern must round-trip a bitmap wider than MaxBitmapWidth ───────
//
// Scope bitmaps wider than MaxBitmapWidth (256 bytes / 2048 feeds) spill to a
// chain of overflow pages instead of being silently truncated by the fixed-size
// inline entry. Interning a 257-byte bitmap MUST preserve every byte on disk.
func TestReaudit2_C4_ScopeInternRejectsOversizedBitmap(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0) // mode 2
	if err != nil {
		t.Fatal(err)
	}
	// A 256-byte bitmap is the maximum inline width and must be accepted.
	legal := make([]byte, 256)
	if _, err := w.ScopeIntern(legal); err != nil {
		t.Fatalf("256-byte bitmap (the inline cap) should be accepted: %v", err)
	}

	// A 257-byte bitmap spills to overflow and must round-trip without losing
	// the trailing byte.
	oversized := make([]byte, 257)
	oversized[256] = 0b1000_0001
	id, err := w.ScopeIntern(oversized)
	if err != nil {
		t.Fatalf("ScopeIntern must accept a 257-byte bitmap (stored via overflow, no truncation): %v", err)
	}
	if err := w.Set(1, 1, id); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing image")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	storedID, ok := r.LookupV4(1)
	if !ok {
		t.Fatal("stored range missing")
	}
	bitmap := r.ScopeResolve(storedID)
	if len(bitmap) != 257 {
		t.Fatalf("257-byte bitmap was truncated to %d bytes", len(bitmap))
	}
	if bitmap[255] != 0 || bitmap[256] != 0b1000_0001 {
		t.Fatalf("overflow bitmap bytes were not preserved: %v", bitmap[255:])
	}
}

// ── A1: 100 churn cycles must not grow the file unboundedly ──────────────────
//
// The append-only tombstone free-list records every freed page as a chain entry.
// 100 real churn cycles (each deleting and re-inserting the live set) append
// entries every commit; if the chain is not compacted, the page count grows
// without bound.
func TestReaudit2_A1_ChurnCyclesDoNotGrowUnbounded(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Small live set (~one leaf) so growth is dominated by the free-list chain.
	for i := uint32(0); i < 50; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	start := w.store.totalPages()

	for cycle := uint64(2); cycle <= 101; cycle++ {
		for i := uint32(0); i < 50; i++ {
			w.Delete(Ipv4Key(i), Ipv4Key(i))
		}
		for i := uint32(0); i < 50; i++ {
			if err := w.Set(Ipv4Key(i), Ipv4Key(i), uint32(cycle)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Commit(cycle, math.MaxUint64); err != nil {
			t.Fatal(err)
		}
	}
	end := w.store.totalPages()
	t.Logf("A1 churn: start=%d pages, end=%d pages after 100 cycles", start, end)

	if end > 50 {
		t.Fatalf("100 churn cycles grew the file to %d pages (start %d) — free-list chain not compacting",
			end, start)
	}

	// Correctness: the final state must reflect the last cycle (scope = 101).
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected vecPageStore")
	}
	r, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 50; i++ {
		if s, ok := r.LookupV4(Ipv4Key(i)); !ok || s != 101 {
			t.Fatalf("data corrupted at key %d: s=%d ok=%v", i, s, ok)
		}
	}
}
