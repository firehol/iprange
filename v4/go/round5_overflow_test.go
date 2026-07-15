package iprangedb

import (
	"bytes"
	"fmt"
	"math"
	"testing"
)

const round5OverflowPayload = PageSize - overflowPayloadOff

func round5Bitmap(size int) []byte {
	bitmap := make([]byte, size)
	for i := range bitmap {
		bitmap[i] = byte((i*131 + 17) % 251)
	}
	if size > 0 {
		bitmap[size-1] |= 1
	}
	return bitmap
}

func round5CommittedOverflowFixture(t *testing.T, bitmap []byte) ([]byte, meta, uint32, []uint32) {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	scopeID, err := w.ScopeIntern(bitmap)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(10, 20, scopeID); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	m, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	leaf := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
	if decodeHeader(leaf).pageType != PageTypeScopeLeaf {
		t.Fatal("fixture needs a scope leaf root")
	}
	recOff := PageHeaderSize
	if u16le(leaf, recOff+4) != ScopeBitmapOverflow {
		t.Fatalf("fixture bitmap is inline, length=%d", len(bitmap))
	}
	first := u32le(leaf, recOff+10)
	if first == 0 {
		t.Fatal("fixture has no overflow chain")
	}
	var pages []uint32
	seen := make(map[uint32]struct{})
	for pgno := first; pgno != 0; {
		if _, duplicate := seen[pgno]; duplicate {
			t.Fatalf("fixture overflow chain cycles at page %d", pgno)
		}
		seen[pgno] = struct{}{}
		pages = append(pages, pgno)
		page := image[int(pgno)*PageSize : int(pgno+1)*PageSize]
		pgno = u32le(page, PageHeaderSize)
	}
	return image, m, scopeID, pages
}

func round5RequireInvalidOverflow(t *testing.T, image []byte) {
	t.Helper()
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err == nil {
		t.Error("Reader.Validate accepted a malformed scope overflow chain")
	}
	if writer, err := openWriter[Ipv4Key](newVecPageStore(append([]byte(nil), image...))); err == nil {
		_ = writer
		t.Error("writable open accepted a malformed scope overflow chain")
	}
}

func TestRound5ScopeBitmapRoundTripsEveryStorageBoundary(t *testing.T) {
	sizes := []int{
		MaxBitmapWidth,
		MaxBitmapWidth + 1,
		round5OverflowPayload,
		round5OverflowPayload + 1,
		32*round5OverflowPayload + 1,
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("bytes-%d", size), func(t *testing.T) {
			want := round5Bitmap(size)
			w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
			if err != nil {
				t.Fatal(err)
			}
			id, err := w.ScopeIntern(want)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Set(1, 1, id); err != nil {
				t.Fatal(err)
			}
			if err := w.Commit(1, math.MaxUint64); err != nil {
				t.Fatal(err)
			}
			if got := w.ScopeResolve(id); !bytes.Equal(got, want) {
				t.Fatalf("writer resolved %d bytes, want %d", len(got), len(want))
			}
			image, ok := w.IntoImage()
			if !ok {
				t.Fatal("missing committed image")
			}
			r, err := Open(image)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Validate(); err != nil {
				t.Fatalf("valid bitmap rejected: %v", err)
			}
			if got := r.ScopeResolve(id); !bytes.Equal(got, want) {
				t.Fatalf("reader resolved %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

func TestRound5CommittedOverflowScopeParticipatesInAllOverlapAPIs(t *testing.T) {
	bitmap := make([]byte, MaxBitmapWidth+1)
	bitmap[0] = 1
	bitmap[MaxBitmapWidth] = 1

	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern(bitmap)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(10, 19, id); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}

	var pairs []FeedOverlap
	if err := AllToAllOverlap(w, func(overlap FeedOverlap) { pairs = append(pairs, overlap) }); err != nil {
		t.Fatal(err)
	}
	wantPair := FeedOverlap{FeedA: 0, FeedB: MaxBitmapWidth * 8, IPCount: 10}
	if len(pairs) != 1 || pairs[0] != wantPair {
		t.Fatalf("all-to-all callbacks = %#v, want %#v", pairs, []FeedOverlap{wantPair})
	}

	counts := make(map[uint32]uint64)
	if err := ForeignVsAllFromSlice(w, []ForeignRange[Ipv4Key]{{From: 12, To: 15}}, func(feed, _ uint32, n uint64) {
		counts[feed] += n
	}); err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 || counts[0] != 4 || counts[MaxBitmapWidth*8] != 4 {
		t.Fatalf("foreign overlap = %#v, want feeds 0 and %d with 4 each", counts, MaxBitmapWidth*8)
	}
}

func TestRound5AllToAllRejectsAccumulatedPairCountOverflow(t *testing.T) {
	w, err := Create[Ipv6Key](ScopeModeBitmap, 0)
	if err != nil {
		t.Fatal(err)
	}
	half := uint64(1) << 63
	if err := w.Set(Ipv6Key{}, Ipv6Key{Lo: half - 1}, 0b11); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv6Key{Hi: 1}, Ipv6Key{Hi: 1, Lo: half - 1}, 0b11); err != nil {
		t.Fatal(err)
	}
	if err := AllToAllOverlap(w, func(FeedOverlap) {}); err == nil {
		t.Fatal("two individually representable spans whose sum is 2^64 did not report overflow")
	}
}

func TestRound5ScopeOverflowValidationRejectsMalformedChains(t *testing.T) {
	onePage, oneMeta, _, onePages := round5CommittedOverflowFixture(t, round5Bitmap(MaxBitmapWidth+1))
	twoPage, twoMeta, _, twoPages := round5CommittedOverflowFixture(t, round5Bitmap(round5OverflowPayload+1))
	if len(onePages) != 1 || len(twoPages) != 2 {
		t.Fatalf("unexpected fixture chains: one=%v two=%v", onePages, twoPages)
	}

	tests := []struct {
		name   string
		base   []byte
		meta   meta
		pages  []uint32
		mutate func([]byte, meta, []uint32)
	}{
		{
			name:  "noncanonical-overflow-length",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, m meta, _ []uint32) {
				leaf := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
				putU32(leaf, PageHeaderSize+6, MaxBitmapWidth)
				finalizeChecksum(leaf)
			},
		},
		{
			name:  "absurd-persisted-length",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, m meta, _ []uint32) {
				leaf := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
				putU32(leaf, PageHeaderSize+6, math.MaxUint32)
				finalizeChecksum(leaf)
			},
		},
		{
			name:  "zero-first-page",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, m meta, _ []uint32) {
				leaf := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
				putU32(leaf, PageHeaderSize+10, 0)
				finalizeChecksum(leaf)
			},
		},
		{
			name:  "bad-overflow-crc",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				page[overflowPayloadOff] ^= 0x80
			},
		},
		{
			name:  "nonzero-unused-payload-tail",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				page[overflowPayloadOff+MaxBitmapWidth+1] = 0xa5
				finalizeChecksum(page)
			},
		},
		{
			name:  "wrong-page-type",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				page[PHPageType] = PageTypeLeaf
				finalizeChecksum(page)
			},
		},
		{
			name:  "reserved-header-byte",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				page[PHReserved] = 1
				finalizeChecksum(page)
			},
		},
		{
			name:  "self-page-number-mismatch",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				putU32(page, PHPgno, pages[0]+1)
				finalizeChecksum(page)
			},
		},
		{
			name:  "nonzero-entry-count",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				putU16(page, PHEntryCount, 1)
				finalizeChecksum(page)
			},
		},
		{
			name:  "next-page-out-of-bounds",
			base:  onePage,
			meta:  oneMeta,
			pages: onePages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				putU32(page, PageHeaderSize, uint32(len(image)/PageSize))
				finalizeChecksum(page)
			},
		},
		{
			name:  "cycle-before-required-length",
			base:  twoPage,
			meta:  twoMeta,
			pages: twoPages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				putU32(page, PageHeaderSize, pages[0])
				finalizeChecksum(page)
			},
		},
		{
			name:  "early-chain-termination",
			base:  twoPage,
			meta:  twoMeta,
			pages: twoPages,
			mutate: func(image []byte, _ meta, pages []uint32) {
				page := image[int(pages[0])*PageSize : int(pages[0]+1)*PageSize]
				putU32(page, PageHeaderSize, 0)
				finalizeChecksum(page)
			},
		},
		{
			name:  "extra-page-after-declared-length",
			base:  twoPage,
			meta:  twoMeta,
			pages: twoPages,
			mutate: func(image []byte, m meta, _ []uint32) {
				leaf := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
				putU32(leaf, PageHeaderSize+6, MaxBitmapWidth+1)
				finalizeChecksum(leaf)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			image := append([]byte(nil), tt.base...)
			tt.mutate(image, tt.meta, tt.pages)
			round5RequireInvalidOverflow(t, image)
		})
	}
}

func TestRound5ScopeOverflowPageCannotBeOwnedByTwoScopes(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := w.ScopeIntern(round5Bitmap(MaxBitmapWidth + 1))
	if err != nil {
		t.Fatal(err)
	}
	secondBitmap := round5Bitmap(MaxBitmapWidth + 2)
	secondBitmap[0] ^= 0x40
	secondID, err := w.ScopeIntern(secondBitmap)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(1, 1, firstID); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(3, 3, secondID); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	m, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	leaf := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
	firstRec := PageHeaderSize
	secondRec := PageHeaderSize + ScopeEntrySize
	firstOverflow := u32le(leaf, firstRec+10)
	if firstOverflow == 0 || u32le(leaf, secondRec+10) == 0 {
		t.Fatal("fixture lacks overflow pages")
	}
	putU32(leaf, secondRec+10, firstOverflow)
	finalizeChecksum(leaf)
	round5RequireInvalidOverflow(t, image)
}

func TestRound5ScopeOverflowPageCannotAliasLiveData(t *testing.T) {
	image, m, _, _ := round5CommittedOverflowFixture(t, round5Bitmap(MaxBitmapWidth+1))
	leaf := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
	putU32(leaf, PageHeaderSize+10, m.rootPgno)
	finalizeChecksum(leaf)
	round5RequireInvalidOverflow(t, image)
}

func TestRound5ScopeBranchSeparatorMustEqualRightChildMinimum(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := w.ScopeIntern([]byte{byte(i), 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	m, err := selectActiveMeta(image)
	if err != nil {
		t.Fatal(err)
	}
	root := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
	h := decodeHeader(root)
	if h.pageType != PageTypeScopeBranch || h.entryCount != 1 {
		t.Fatalf("fixture needs one scope separator, type=%d count=%d", h.pageType, h.entryCount)
	}
	bv := newBranchView(root, int(h.entryCount), ScopeKeyWidth)
	separator := u32le(bv.sep(0), 0)
	putU32(bv.sep(0), 0, separator+1)
	finalizeChecksum(root)

	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err == nil {
		t.Fatal("Reader.Validate accepted a scope separator that does not match the right-child minimum")
	}
	if writer, err := openWriter[Ipv4Key](newVecPageStore(image)); err == nil {
		_ = writer
		t.Fatal("writable open accepted a scope separator that does not match the right-child minimum")
	}
}

func TestRound5OverflowScopeSurvivesReopenAndScopeTableRebuild(t *testing.T) {
	want := round5Bitmap(round5OverflowPayload + 1)
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(1, 1, id); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}

	for generation := uint64(2); generation <= 5; generation++ {
		w, err = openWriter[Ipv4Key](newVecPageStore(append([]byte(nil), image...)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.ScopeIntern([]byte{byte(generation), 1}); err != nil {
			t.Fatal(err)
		}
		if err := w.Commit(generation, math.MaxUint64); err != nil {
			t.Fatal(err)
		}
		if got := w.ScopeResolve(id); !bytes.Equal(got, want) {
			t.Fatalf("generation %d resolved %d bytes, want %d", generation, len(got), len(want))
		}
		image, ok = w.IntoImage()
		if !ok {
			t.Fatal("missing rebuilt image")
		}
		r, err := Open(image)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Validate(); err != nil {
			t.Fatalf("generation %d validation: %v", generation, err)
		}
		if got := r.ScopeResolve(id); !bytes.Equal(got, want) {
			t.Fatalf("generation %d reader resolved %d bytes, want %d", generation, len(got), len(want))
		}
	}
}

func TestRound5OverflowScopeRebuildReclaimsEveryLongChainPage(t *testing.T) {
	image, _, _, oldPages := round5CommittedOverflowFixture(t, round5Bitmap(34*round5OverflowPayload+1))
	if len(oldPages) <= int(TreeHeightMax)+1 {
		t.Fatalf("fixture overflow chain has %d pages, want more than %d", len(oldPages), TreeHeightMax+1)
	}
	w, err := openWriter[Ipv4Key](newVecPageStore(append([]byte(nil), image...)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeIntern([]byte{0x55, 0x01}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	after, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing rebuilt image")
	}
	m, err := selectActiveMeta(after)
	if err != nil {
		t.Fatal(err)
	}
	if m.freeListHead == 0 {
		t.Fatal("scope rebuild produced no free-list entries")
	}
	store := newVecPageStore(after)
	entries, err := ReadChain(store, m.freeListHead)
	if err != nil {
		t.Fatal(err)
	}
	freed := make(map[uint32]struct{}, len(entries))
	for _, entry := range entries {
		if entry.FreedTxnID != math.MaxUint64 {
			freed[entry.Pgno] = struct{}{}
		}
	}
	for _, pgno := range oldPages {
		if _, ok := freed[pgno]; !ok {
			t.Errorf("scope rebuild orphaned overflow page %d from a %d-page chain", pgno, len(oldPages))
		}
	}
}

func round5OverflowOverlapWriter(t *testing.T, records int) *Writer[Ipv4Key] {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	bitmap := make([]byte, MaxBitmapWidth+1)
	bitmap[0] = 1
	bitmap[MaxBitmapWidth] = 1
	id, err := w.ScopeIntern(bitmap)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < records; i++ {
		ip := Ipv4Key(i * 2)
		if err := w.Append(ip, ip, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestRound5OverflowAllToAllAllocationsDoNotScaleWithRecordCount(t *testing.T) {
	measure := func(records int) float64 {
		w := round5OverflowOverlapWriter(t, records)
		return testing.AllocsPerRun(10, func() {
			if err := AllToAllOverlap(w, func(FeedOverlap) {}); err != nil {
				t.Fatal(err)
			}
		})
	}
	one := measure(1)
	many := measure(100)
	if many > one+4 {
		t.Fatalf("all-to-all allocations scale with records sharing one overflow scope: one=%.0f many=%.0f", one, many)
	}
}

func TestRound5OverflowForeignVsAllAllocationsDoNotScaleWithRecordCount(t *testing.T) {
	measure := func(records int) float64 {
		w := round5OverflowOverlapWriter(t, records)
		foreign := []ForeignRange[Ipv4Key]{{From: 0, To: Ipv4Key((records - 1) * 2)}}
		return testing.AllocsPerRun(10, func() {
			if err := ForeignVsAllFromSlice(w, foreign, func(uint32, uint32, uint64) {}); err != nil {
				t.Fatal(err)
			}
		})
	}
	one := measure(1)
	many := measure(100)
	if many > one+4 {
		t.Fatalf("foreign-vs-all allocations scale with records sharing one overflow scope: one=%.0f many=%.0f", one, many)
	}
}
