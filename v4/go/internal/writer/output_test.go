// Immutable one-shot output round trips (Rust immutable_output_tests.rs):
// build a fresh output through the append-only builder, finish it, reopen
// it with the public immutable reader, and verify identity, scans, feed
// catalog, and membership bitmaps. The builder mapping is closed before
// reopening because the output holds the exclusive lifetime lock.

package writer_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

func corruptErr(detail string) error {
	return &format.Error{Code: format.CodeFormatInvalid, Detail: detail}
}

const (
	tagFirstSeen = "first-seen"
	tagFeeds     = "feeds"
)

func directSpec(family uint8) writer.OutputSpec {
	return writer.OutputSpec{
		AddressFamily:  family,
		ValueKind:      format.ValueKindDirect,
		StructureKind:  format.StructureKindNone,
		ValueTag:       fixedTag(tagFirstSeen),
		DatabaseID:     fixed16(3),
		TxnID:          7,
		CommitNonce:    fixed16(4),
		FeedIndexLimit: 0,
	}
}

func membershipSpec(feedIndexLimit uint64) writer.OutputSpec {
	return writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindMembership,
		StructureKind:  format.StructureKindNone,
		ValueTag:       fixedTag(tagFeeds),
		DatabaseID:     fixed16(5),
		TxnID:          11,
		CommitNonce:    fixed16(6),
		FeedIndexLimit: feedIndexLimit,
	}
}

func fixedTag(text string) [16]byte {
	var tag [16]byte
	copy(tag[:], text)
	return tag
}

func fixed16(value byte) [16]byte {
	var out [16]byte
	for index := range out {
		out[index] = value
	}
	return out
}

func generousBudget() writer.OutputBudget {
	return writer.OutputBudget{MaxOutputPages: 100_000}
}

// newOutput builds one fresh output at a unique path. The reference
// batch is configured like the Rust test builder() helper (a 2 MiB
// operation heap): membership and structured specs get the full
// 1024-entry batch, direct specs get none.
func newOutput(t *testing.T, spec writer.OutputSpec, budget writer.OutputBudget) (*writer.OutputBuilder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "output.iprdb")
	entries := 0
	if spec.ValueKind == format.ValueKindMembership || spec.ValueKind == format.ValueKindStructured {
		entries = writer.ReferenceBatchEntryLimit
	}
	b, err := writer.NewOutputBuilder(path, spec, budget, entries, nil)
	if err != nil {
		t.Fatalf("NewOutputBuilder: %v", err)
	}
	return b, path
}

// finishOutput finishes and closes the builder so the exclusive lifetime
// lock is released before the reader opens the file.
func finishOutput(t *testing.T, b *writer.OutputBuilder) string {
	t.Helper()
	if err := b.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	path := b.Path()
	if err := b.Close(); err != nil {
		t.Fatalf("Close after finish: %v", err)
	}
	return path
}

func reopen(t *testing.T, path string) *iprangedb.ImmutableReader {
	t.Helper()
	r, err := iprangedb.OpenImmutable(path)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	return r
}

// outputMeta opens the finished file through the internal reader and
// returns the committed meta for exact field assertions the public facade
// does not expose (feed limit, membership limits, entry counts).
func outputMeta(t *testing.T, path string) format.Meta {
	t.Helper()
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatalf("reader.OpenImmutable: %v", err)
	}
	defer r.Close()
	return r.Meta()
}

// checkDataPageChecksums verifies every data page carries a matching CRC
// and that the two meta pages are identical (Rust finish seals all
// non-meta pages and writes the dual meta).
func checkDataPageChecksums(t *testing.T, path string, pageCount uint64) {
	t.Helper()
	m, err := mapping.OpenImmutable(path, nil)
	if err != nil {
		t.Fatalf("mapping.OpenImmutable: %v", err)
	}
	defer m.Close()
	for pageNumber := uint32(2); uint64(pageNumber) < pageCount; pageNumber++ {
		page, err := m.Page(pageNumber)
		if err != nil {
			t.Fatalf("Page(%d): %v", pageNumber, err)
		}
		if !format.PageChecksumValid(page) {
			t.Fatalf("data page %d CRC check failed", pageNumber)
		}
	}
	p0, err := m.Page(0)
	if err != nil {
		t.Fatalf("Page(0): %v", err)
	}
	p1, err := m.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	if !bytes.Equal(p0, p1) {
		t.Fatalf("meta pages are not identical")
	}
}

func TestOutputDirectReopensWithExactIdentity(t *testing.T) {
	b, path := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	if err := b.PushDirectV4(0, 9, 11); err != nil {
		t.Fatalf("push 0-9: %v", err)
	}
	if err := b.PushDirectV4(10, 99, 12); err != nil {
		t.Fatalf("push 10-99: %v", err)
	}
	if err := b.PushDirectV4(1_000, 0xFFFFFFFF, 13); err != nil {
		t.Fatalf("push 1000-max: %v", err)
	}
	path = finishOutput(t, b)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	checkDataPageChecksums(t, path, 2)
	rBefore := reopen(t, path)
	infoBefore, err := rBefore.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	rBefore.Close()
	if int64(len(raw)) != int64(infoBefore.PageCount)*format.PageSize {
		t.Fatalf("file size %d, want %d pages", len(raw), infoBefore.PageCount)
	}

	r := reopen(t, path)
	defer r.Close()
	info, err := r.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.DatabaseID != fixed16(3) || info.TransactionID != 7 || info.CommitNonce != fixed16(4) {
		t.Fatalf("identity mismatch: %+v", info)
	}
	if info.RangeRecordCount != 3 {
		t.Fatalf("range record count %d, want 3", info.RangeRecordCount)
	}

	var ranges []iprangedb.DirectRangeV4
	if err := r.DirectRangesV4(func(rr iprangedb.DirectRangeV4) error {
		ranges = append(ranges, rr)
		return nil
	}); err != nil {
		t.Fatalf("DirectRangesV4: %v", err)
	}
	want := []iprangedb.DirectRangeV4{
		{From: 0, To: 9, Value: 11},
		{From: 10, To: 99, Value: 12},
		{From: 1_000, To: 0xFFFFFFFF, Value: 13},
	}
	if len(ranges) != len(want) {
		t.Fatalf("scan %d ranges, want %d", len(ranges), len(want))
	}
	for index := range want {
		if ranges[index] != want[index] {
			t.Fatalf("range %d = %+v, want %+v", index, ranges[index], want[index])
		}
	}
	value, found, err := r.LookupDirectV4(5)
	if err != nil || !found || value != 11 {
		t.Fatalf("lookup 5 = %d/%v/%v, want 11/true/nil", value, found, err)
	}
	value, found, err = r.LookupDirectV4(0xFFFFFFFF)
	if err != nil || !found || value != 13 {
		t.Fatalf("lookup max = %d/%v/%v, want 13/true/nil", value, found, err)
	}
}

func TestOutputFullSpaceIPv6(t *testing.T) {
	b, path := newOutput(t, directSpec(format.AddressFamilyIPv6), generousBudget())
	if err := b.PushDirectV6(0, 0, 0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFF, 42); err != nil {
		t.Fatalf("push full space: %v", err)
	}
	path = finishOutput(t, b)

	r := reopen(t, path)
	defer r.Close()
	value, found, err := r.LookupDirectV6(iprangedb.IPv6{})
	if err != nil || !found || value != 42 {
		t.Fatalf("lookup min = %d/%v/%v", value, found, err)
	}
	value, found, err = r.LookupDirectV6(iprangedb.IPv6{Hi: 0xFFFFFFFFFFFFFFFF, Lo: 0xFFFFFFFFFFFFFFFF})
	if err != nil || !found || value != 42 {
		t.Fatalf("lookup max = %d/%v/%v", value, found, err)
	}
}

func TestOutputEmptyDirectAndMembership(t *testing.T) {
	direct, path := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	path = finishOutput(t, direct)
	r := reopen(t, path)
	info, err := r.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.RangeRecordCount != 0 {
		t.Fatalf("empty direct record count %d", info.RangeRecordCount)
	}
	r.Close()

	membership, path := newOutput(t, membershipSpec(1_000), generousBudget())
	path = finishOutput(t, membership)
	meta := outputMeta(t, path)
	if meta.FeedIndexLimit != 1_000 || meta.ActiveFeedCount != 0 || meta.RangeRecordCount != 0 {
		t.Fatalf("empty membership meta %+v", meta)
	}
	if meta.MembershipIDLimit != 1 {
		t.Fatalf("empty membership id limit %d, want 1", meta.MembershipIDLimit)
	}
}

func TestOutputMultiLevelDirect(t *testing.T) {
	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	for index := uint32(0); index < 2_000; index++ {
		from := index * 3
		if err := b.PushDirectV4(from, from+1, index%3); err != nil {
			t.Fatalf("push %d: %v", index, err)
		}
	}
	path := finishOutput(t, b)

	r := reopen(t, path)
	defer r.Close()
	info, err := r.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.RangeRecordCount != 2_000 {
		t.Fatalf("record count %d, want 2000", info.RangeRecordCount)
	}
	count := 0
	previous := iprangedb.DirectRangeV4{}
	err = r.DirectRangesV4(func(rr iprangedb.DirectRangeV4) error {
		if rr.From != uint32(count)*3 || rr.To != rr.From+1 || rr.Value != uint32(count)%3 {
			t.Fatalf("record %d = %+v", count, rr)
		}
		if count > 0 && rr.From <= previous.To {
			t.Fatalf("ranges not ascending at %d", count)
		}
		previous = rr
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != 2_000 {
		t.Fatalf("scan count %d, want 2000", count)
	}
}

func TestOutputBranchOverflowKeepsOneChildRightEdge(t *testing.T) {
	const leafCapacity = (format.PageSize - format.SlottedHeaderSize) / (16*2 + 4 + 2)
	const branchCapacity = (format.PageSize - format.SlottedHeaderSize) / (16 + 4 + 2)
	const recordCount = leafCapacity*branchCapacity + 1

	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv6), generousBudget())
	for index := 0; index < recordCount; index++ {
		address := uint64(index) * 2
		if err := b.PushDirectV6(address, 0, address, 0, uint32(index)); err != nil {
			t.Fatalf("push %d: %v", index, err)
		}
	}
	path := finishOutput(t, b)

	r := reopen(t, path)
	defer r.Close()
	info, err := r.Info()
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.RangeRecordCount != uint64(recordCount) {
		t.Fatalf("record count %d, want %d", info.RangeRecordCount, recordCount)
	}
	count := 0
	err = r.DirectRangesV6(func(rr iprangedb.DirectRangeV6) error {
		if rr.FromHi != uint64(count)*2 || rr.FromLo != 0 || rr.ToHi != rr.FromHi || rr.ToLo != 0 || rr.Value != uint32(count) {
			t.Fatalf("record %d = %+v", count, rr)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != recordCount {
		t.Fatalf("scan count %d, want %d", count, recordCount)
	}
}

func TestOutputMembershipStreamsSparseWords(t *testing.T) {
	b, _ := newOutput(t, membershipSpec(32_002), generousBudget())
	if err := b.PushFeed("alpha", 3); err != nil {
		t.Fatalf("push feed alpha: %v", err)
	}
	if err := b.PushFeed("middle", 31_999); err != nil {
		t.Fatalf("push feed middle: %v", err)
	}
	if err := b.PushFeed("omega", 32_001); err != nil {
		t.Fatalf("push feed omega: %v", err)
	}

	wide := make(writer.OutputWords, 501)
	wide[0] = 1 << 3
	wide[499] = 1 << 63
	wide[500] = 1 << 1
	alpha := writer.OutputWords{1 << 3}
	if err := b.PushMembershipV4(0, 9, wide); err != nil {
		t.Fatalf("push wide 0-9: %v", err)
	}
	if err := b.PushMembershipV4(10, 19, alpha); err != nil {
		t.Fatalf("push alpha 10-19: %v", err)
	}
	if err := b.PushMembershipV4(30, 39, wide); err != nil {
		t.Fatalf("push wide 30-39: %v", err)
	}
	path := finishOutput(t, b)

	meta := outputMeta(t, path)
	if meta.FeedIndexLimit != 32_002 || meta.ActiveFeedCount != 3 ||
		meta.MembershipEntryCount != 2 || meta.MembershipIDLimit != 3 ||
		meta.RangeRecordCount != 3 {
		t.Fatalf("membership meta %+v", meta)
	}
	r := reopen(t, path)
	defer r.Close()

	for name, wantIndex := range map[string]uint32{"alpha": 3, "middle": 31_999, "omega": 32_001} {
		entry, found, err := r.LookupFeed(name)
		if err != nil {
			t.Fatalf("LookupFeed(%s): %v", name, err)
		}
		if !found || entry.Index != wantIndex {
			t.Fatalf("LookupFeed(%s) = %+v/%v, want index %d", name, entry, found, wantIndex)
		}
	}
	if _, found, err := r.LookupFeed("absent"); err != nil || found {
		t.Fatalf("LookupFeed(absent) = %v/%v, want false", found, err)
	}

	pin, err := r.Pin()
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	defer pin.Close()

	first, found, err := pin.LookupMembershipV4(5)
	if err != nil || !found {
		t.Fatalf("LookupMembershipV4(5) = %v/%v", found, err)
	}
	if count, err := first.WordCount(); err != nil || count != 501 {
		t.Fatalf("first word count %d/%v, want 501", count, err)
	}
	for _, index := range []uint32{3, 31_999, 32_001} {
		if has, err := first.ContainsIndex(index); err != nil || !has {
			t.Fatalf("first contains %d = %v/%v", index, has, err)
		}
	}
	if has, err := first.ContainsIndex(4); err != nil || has {
		t.Fatalf("first contains 4 = %v/%v, want false", has, err)
	}

	second, found, err := pin.LookupMembershipV4(15)
	if err != nil || !found {
		t.Fatalf("LookupMembershipV4(15) = %v/%v", found, err)
	}
	if count, err := second.WordCount(); err != nil || count != 1 {
		t.Fatalf("second word count %d/%v, want 1", count, err)
	}
	if has, err := second.ContainsIndex(3); err != nil || !has {
		t.Fatalf("second contains 3 = %v/%v", has, err)
	}
	if has, err := second.ContainsIndex(31_999); err != nil || has {
		t.Fatalf("second contains 31999 = %v/%v, want false", has, err)
	}

	// The deferred third range reuses the same wide bitmap: the reader
	// decodes the same dictionary record.
	third, found, err := pin.LookupMembershipV4(35)
	if err != nil || !found {
		t.Fatalf("LookupMembershipV4(35) = %v/%v", found, err)
	}
	if count, err := third.WordCount(); err != nil || count != 501 {
		t.Fatalf("third word count %d/%v, want 501", count, err)
	}
	words := make([]uint64, 501)
	if _, err := third.ReadWords(0, words); err != nil {
		t.Fatalf("third ReadWords: %v", err)
	}
	if words[0] != 1<<3 || words[499] != 1<<63 || words[500] != 1<<1 {
		t.Fatalf("third words mismatch: %x %x %x", words[0], words[499], words[500])
	}
}

func TestOutputMembershipReferencesAppliedOnce(t *testing.T) {
	b, _ := newOutput(t, membershipSpec(1), generousBudget())
	if err := b.PushFeed("feed", 0); err != nil {
		t.Fatalf("push feed: %v", err)
	}
	membership, err := b.InternMembership(writer.OutputWords{1})
	if err != nil {
		t.Fatalf("intern: %v", err)
	}
	if membership != 1 {
		t.Fatalf("interned id %d, want 1", membership)
	}
	for index := uint32(0); index < 512; index++ {
		address := index * 2
		if err := b.PushInternedMembershipV4(address, address, membership); err != nil {
			t.Fatalf("push %d: %v", index, err)
		}
	}
	path := finishOutput(t, b)

	meta := outputMeta(t, path)
	if meta.RangeRecordCount != 512 || meta.MembershipEntryCount != 1 {
		t.Fatalf("meta %+v", meta)
	}
	r := reopen(t, path)
	defer r.Close()
	pin, err := r.Pin()
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	defer pin.Close()
	view, found, err := pin.LookupMembershipV4(100)
	if err != nil || !found {
		t.Fatalf("LookupMembershipV4(100) = %v/%v", found, err)
	}
	if has, err := view.ContainsIndex(0); err != nil || !has {
		t.Fatalf("contains 0 = %v/%v", has, err)
	}
}

func TestOutputMembershipRejectsInactiveBitsAndTrailingZeros(t *testing.T) {
	inactive, path := newOutput(t, membershipSpec(128), generousBudget())
	if err := inactive.PushFeed("alpha", 3); err != nil {
		t.Fatalf("push feed: %v", err)
	}
	err := inactive.PushMembershipV4(0, 1, writer.OutputWords{1 << 4})
	if !isWriterCode(err, format.CodeInvalidArgument) || !containsDetail(err, "inactive feed") {
		t.Fatalf("inactive push error %v", err)
	}
	if err := inactive.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = path

	trailing, _ := newOutput(t, membershipSpec(128), generousBudget())
	if err := trailing.PushFeed("alpha", 3); err != nil {
		t.Fatalf("push feed: %v", err)
	}
	err = trailing.PushMembershipV4(0, 1, writer.OutputWords{1 << 3, 0})
	if !isWriterCode(err, format.CodeInvalidArgument) || !containsDetail(err, "not canonical") {
		t.Fatalf("trailing zero error %v", err)
	}
	if err := trailing.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOutputPageBudgetRefusesGrowthAndPoisonedFinish(t *testing.T) {
	b, path := newOutput(t, directSpec(format.AddressFamilyIPv4), writer.OutputBudget{MaxOutputPages: 2})
	err := b.PushDirectV4(1, 2, 3)
	if !isWriterCode(err, format.CodeInsufficientResourceBudget) || !containsDetail(err, "immutable output pages") {
		t.Fatalf("budget push error %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 2*format.PageSize {
		t.Fatalf("file size %d, want %d", info.Size(), 2*format.PageSize)
	}
	err = b.Finish()
	if !isWriterCode(err, format.CodeWrongState) {
		t.Fatalf("poisoned finish error %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOutputMalformedOrderIsRejectedPermanently(t *testing.T) {
	reversed, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	err := reversed.PushDirectV4(2, 1, 1)
	if !isWriterCode(err, format.CodeInvalidArgument) || !containsDetail(err, "after its end") {
		t.Fatalf("reversed push error %v", err)
	}
	err = reversed.Finish()
	if !isWriterCode(err, format.CodeWrongState) {
		t.Fatalf("poisoned finish error %v", err)
	}
	reversed.Close()

	overlap, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	if err := overlap.PushDirectV4(0, 10, 1); err != nil {
		t.Fatalf("push 0-10: %v", err)
	}
	err = overlap.PushDirectV4(10, 20, 2)
	if !isWriterCode(err, format.CodeInvalidArgument) || !containsDetail(err, "not canonical") {
		t.Fatalf("overlap push error %v", err)
	}
	overlap.Close()

	adjacent, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	if err := adjacent.PushDirectV4(0, 9, 1); err != nil {
		t.Fatalf("push 0-9: %v", err)
	}
	err = adjacent.PushDirectV4(10, 20, 1)
	if !isWriterCode(err, format.CodeInvalidArgument) || !containsDetail(err, "not canonical") {
		t.Fatalf("adjacent same-value push error %v", err)
	}
	adjacent.Close()
}

func TestOutputLeafRolloverWithoutHeap(t *testing.T) {
	const leafCapacity = (format.PageSize - format.SlottedHeaderSize) / (8*2 + 4 + 2)
	b, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	for index := uint32(0); index < leafCapacity; index++ {
		address := index * 2
		if err := b.PushDirectV4(address, address, index); err != nil {
			t.Fatalf("push %d: %v", index, err)
		}
	}
	address := uint32(leafCapacity) * 2
	if err := b.PushDirectV4(address, address, uint32(leafCapacity)); err != nil {
		t.Fatalf("rollover push: %v", err)
	}
	path := finishOutput(t, b)

	r := reopen(t, path)
	defer r.Close()
	count := 0
	err := r.DirectRangesV4(func(rr iprangedb.DirectRangeV4) error {
		if rr.From != rr.To || rr.From != uint32(count)*2 {
			t.Fatalf("record %d = %+v", count, rr)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != leafCapacity+1 {
		t.Fatalf("scan count %d, want %d", count, leafCapacity+1)
	}
}

func TestOutputStoreGuardrails(t *testing.T) {
	// The append-only store refuses retires and discards. These store
	// calls bypass the mutation latch and never poison the builder.
	guards, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	if err := guards.RetirePages(nil); err != nil {
		t.Fatalf("empty retire: %v", err)
	}
	err := guards.RetirePages([]uint32{9})
	if !isWriterCode(err, format.CodeFormatInvalid) {
		t.Fatalf("non-empty retire error %v", err)
	}
	err = guards.DiscardPrivate(2)
	if !isWriterCode(err, format.CodeFormatInvalid) {
		t.Fatalf("discard error %v", err)
	}
	// Page area is [2, pageCount): meta pages and out-of-range pages are
	// refused before any page bytes are touched.
	for _, pageNumber := range []uint32{0, 1} {
		err = guards.Inspect(pageNumber, func(page []byte) error { return nil })
		if !isWriterCode(err, format.CodeFormatInvalid) {
			t.Fatalf("Inspect(%d) error %v", pageNumber, err)
		}
	}
	err = guards.Update(1, func(page []byte) error { return nil })
	if !isWriterCode(err, format.CodeFormatInvalid) {
		t.Fatalf("Update(1) error %v", err)
	}
	err = guards.CopyPage(3, 1, func(source, output []byte) error { return nil })
	if !isWriterCode(err, format.CodeFormatInvalid) {
		t.Fatalf("CopyPage dest error %v", err)
	}
	if err := guards.Finish(); err != nil {
		t.Fatalf("guards did not poison the builder: %v", err)
	}
	guards.Close()

	// Wrong-mode operations refuse with the typed code; each failed
	// mutation poisons the builder (Rust Builder::mutate), so every case
	// needs a fresh builder.
	feed, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	err = feed.PushFeed("feed", 0)
	if !isWriterCode(err, format.CodeWrongValueKind) {
		t.Fatalf("PushFeed on direct error %v", err)
	}
	feed.Close()

	family, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	err = family.PushDirectV6(0, 0, 1, 1, 1)
	if !isWriterCode(err, format.CodeWrongAddressFamily) {
		t.Fatalf("PushDirectV6 on v4 output error %v", err)
	}
	family.Close()

	membership, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	err = membership.PushMembershipV4(0, 1, writer.OutputWords{1})
	if !isWriterCode(err, format.CodeWrongValueKind) {
		t.Fatalf("PushMembershipV4 on direct error %v", err)
	}

	// The failed PushMembershipV4 poisoned its builder.
	err = membership.Finish()
	if !isWriterCode(err, format.CodeWrongState) {
		t.Fatalf("poisoned finish error %v", err)
	}
	membership.Close()
}

func TestOutputRefusesExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.iprdb")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := writer.NewOutputBuilder(path, directSpec(format.AddressFamilyIPv4), generousBudget(), 0, nil)
	if !isWriterCode(err, format.CodeNameExists) {
		t.Fatalf("existing path error %v", err)
	}
}

func isWriterCode(err error, code format.ErrorCode) bool {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return false
	}
	return fe.Code == code
}

func containsDetail(err error, detail string) bool {
	var fe *format.Error
	if !errors.As(err, &fe) {
		return false
	}
	return bytes.Contains([]byte(fe.Detail), []byte(detail))
}
