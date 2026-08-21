// Structured output construction round trips (Rust immutable_output
// structured.rs over the membership/structured reference batches): build
// network_enrichment_v1 outputs with optional threat memberships, finish
// them, reopen them through the internal reader and the public facade,
// and verify values, membership bits, dedup, guards, and the structure
// reference batch full/flush path.

package writer_test

import (
	"strconv"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// structuredSpec builds one network_enrichment_v1 output spec (Rust
// OutputSpec for structured outputs: ValueKind::Structured with the
// structure kind set, an IPv4 or IPv6 family, a fresh tag, and the
// preserved feed-index limit for optional threat memberships).
func structuredSpec(family uint8, feedIndexLimit uint64) writer.OutputSpec {
	return writer.OutputSpec{
		AddressFamily:  family,
		ValueKind:      format.ValueKindStructured,
		StructureKind:  format.StructureKindNetworkEnrichmentV1,
		ValueTag:       fixedTag("enrich"),
		DatabaseID:     fixed16(7),
		TxnID:          13,
		CommitNonce:    fixed16(8),
		FeedIndexLimit: feedIndexLimit,
	}
}

func enrichment(asn uint32) format.NetworkEnrichmentV1 {
	return format.NetworkEnrichmentV1{
		ASN:       asn,
		CountryID: asn % 900,
		StateID:   asn % 30,
		CityID:    asn % 7,
	}
}

func enrichmentLocated(asn uint32, lat, lon int32) format.NetworkEnrichmentV1 {
	value := enrichment(asn)
	value.LatitudeMicrodegrees = lat
	value.LongitudeMicrodegrees = lon
	value.Flags = format.NetworkEnrichmentV1HasLocation
	return value
}

// TestOutputStructuredV4RoundTrip builds a structured IPv4 output with
// two structure entries (one with a threat membership, one without), a
// deduplicated second range of the first value, finishes, reopens, and
// verifies the reader resolves the values, the membership bitmap, and
// the absent gap. The five-argument NewOutputBuilder disables the
// structure batch, so every structure reference applies directly (the
// existing publish_set caller path).
func TestOutputStructuredV4RoundTrip(t *testing.T) {
	b, _ := newOutput(t, structuredSpec(format.AddressFamilyIPv4, 128), generousBudget())
	if err := b.PushFeed("alpha", 3); err != nil {
		t.Fatalf("push feed alpha: %v", err)
	}
	if err := b.PushFeed("beta", 100); err != nil {
		t.Fatalf("push feed beta: %v", err)
	}

	withMembership := enrichmentLocated(64512, 4_512_345, 12_345_678)
	withoutMembership := enrichment(64513)
	// Bit 3 (alpha) and bit 100 (beta): word 0 carries index 3, word 1
	// carries index 100 (100-64 = 36).
	membership := writer.OutputWords{1 << 3, 1 << 36}

	if err := b.PushNetworkEnrichmentV1V4(0, 9, withMembership, membership); err != nil {
		t.Fatalf("push 0-9: %v", err)
	}
	if err := b.PushNetworkEnrichmentV1V4(10, 19, withoutMembership, nil); err != nil {
		t.Fatalf("push 10-19: %v", err)
	}
	// The same value and the same membership words again: both
	// dictionaries deduplicate and the refcount accumulates.
	if err := b.PushNetworkEnrichmentV1V4(30, 39, withMembership, membership); err != nil {
		t.Fatalf("push 30-39: %v", err)
	}
	path := finishOutput(t, b)

	meta := outputMeta(t, path)
	if meta.FeedIndexLimit != 128 || meta.ActiveFeedCount != 2 ||
		meta.MembershipEntryCount != 1 || meta.MembershipIDLimit != 2 ||
		meta.StructureEntryCount != 2 || meta.StructureIDLimit != 3 ||
		meta.RangeRecordCount != 3 {
		t.Fatalf("structured meta %+v", meta)
	}

	r := reopen(t, path)
	defer r.Close()
	pin, err := r.Pin()
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	defer pin.Close()

	view, found, err := pin.LookupNetworkEnrichmentV1V4(5)
	if err != nil || !found {
		t.Fatalf("LookupNetworkEnrichmentV1V4(5) = %v/%v", found, err)
	}
	value, err := view.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if value.ASN != 64512 || !value.HasLocation ||
		value.Location.LatitudeMicrodegrees != 4_512_345 ||
		value.Location.LongitudeMicrodegrees != 12_345_678 {
		t.Fatalf("value at 5 = %+v", value)
	}
	membershipView, present, err := view.ThreatMembership()
	if err != nil || !present {
		t.Fatalf("ThreatMembership(5) = %v/%v, want present", present, err)
	}
	if has, err := membershipView.ContainsIndex(3); err != nil || !has {
		t.Fatalf("contains 3 = %v/%v, want true", has, err)
	}
	if has, err := membershipView.ContainsIndex(100); err != nil || !has {
		t.Fatalf("contains beta feed 100 = %v/%v, want true", has, err)
	}
	if has, err := membershipView.ContainsIndex(4); err != nil || has {
		t.Fatalf("contains 4 = %v/%v, want false", has, err)
	}

	plain, found, err := pin.LookupNetworkEnrichmentV1V4(15)
	if err != nil || !found {
		t.Fatalf("LookupNetworkEnrichmentV1V4(15) = %v/%v", found, err)
	}
	value, err = plain.Value()
	if err != nil {
		t.Fatalf("Value(15): %v", err)
	}
	if value.ASN != 64513 || value.HasLocation {
		t.Fatalf("value at 15 = %+v", value)
	}
	if _, present, err := plain.ThreatMembership(); err != nil || present {
		t.Fatalf("ThreatMembership(15) = %v/%v, want absent", present, err)
	}

	// The deduplicated second range resolves to the same payload.
	again, found, err := pin.LookupNetworkEnrichmentV1V4(35)
	if err != nil || !found {
		t.Fatalf("LookupNetworkEnrichmentV1V4(35) = %v/%v", found, err)
	}
	value, err = again.Value()
	if err != nil {
		t.Fatalf("Value(35): %v", err)
	}
	if value.ASN != 64512 {
		t.Fatalf("value at 35 = %+v", value)
	}

	// The gap between the ranges is absent.
	if _, found, err := pin.LookupNetworkEnrichmentV1V4(25); err != nil || found {
		t.Fatalf("LookupNetworkEnrichmentV1V4(25) = %v/%v, want absent", found, err)
	}

	entry, found, err := r.LookupFeed("alpha")
	if err != nil || !found || entry.Index != 3 {
		t.Fatalf("LookupFeed(alpha) = %+v/%v", entry, found)
	}
}

// TestOutputStructuredStructureBatchFullFlushes exercises the six-argument
// NewStructuredOutputBuilder with the structure batch capped at one entry
// (the Rust heap-charge order: membership batch first, structure batch
// second): every distinct structure fills the table, flush applies the
// pending delta, and the retry succeeds; the final refcounts survive the
// finish flush exactly like the direct path.
func TestOutputStructuredStructureBatchFullFlushes(t *testing.T) {
	path := t.TempDir() + "/output.iprdb"
	spec := structuredSpec(format.AddressFamilyIPv4, 0)
	b, err := writer.NewStructuredOutputBuilder(path, spec, generousBudget(), 0, 1, nil)
	if err != nil {
		t.Fatalf("NewStructuredOutputBuilder: %v", err)
	}
	first := enrichment(100)
	second := enrichment(200)
	if err := b.PushNetworkEnrichmentV1V4(0, 9, first, nil); err != nil {
		t.Fatalf("push first: %v", err)
	}
	// The distinct value fills the one-entry structure batch; the flush
	// applies first and the retry adds second.
	if err := b.PushNetworkEnrichmentV1V4(10, 19, second, nil); err != nil {
		t.Fatalf("push second: %v", err)
	}
	if err := b.PushNetworkEnrichmentV1V4(20, 29, first, nil); err != nil {
		t.Fatalf("push first again: %v", err)
	}
	path = finishOutput(t, b)

	meta := outputMeta(t, path)
	if meta.StructureEntryCount != 2 || meta.MembershipEntryCount != 0 ||
		meta.RangeRecordCount != 3 {
		t.Fatalf("batch meta %+v", meta)
	}
	r := reopen(t, path)
	defer r.Close()
	pin, err := r.Pin()
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	defer pin.Close()
	for address, wantASN := range map[uint32]uint32{5: 100, 15: 200, 25: 100} {
		view, found, err := pin.LookupNetworkEnrichmentV1V4(iprangedb.IPv4(address))
		if err != nil || !found {
			t.Fatalf("Lookup(%d) = %v/%v", address, found, err)
		}
		value, err := view.Value()
		if err != nil {
			t.Fatalf("Value(%d): %v", address, err)
		}
		if value.ASN != wantASN {
			t.Fatalf("value at %d = %+v, want ASN %d", address, value, wantASN)
		}
	}
}

// TestOutputStructuredV6RoundTrip builds a structured IPv6 output with a
// threat membership and verifies the v6 lookup path through the reader.
func TestOutputStructuredV6RoundTrip(t *testing.T) {
	b, _ := newOutput(t, structuredSpec(format.AddressFamilyIPv6, 64), generousBudget())
	if err := b.PushFeed("six", 7); err != nil {
		t.Fatalf("push feed six: %v", err)
	}
	value := enrichmentLocated(64514, -90_000_000, 180_000_000)
	membership := writer.OutputWords{1 << 7}
	if err := b.PushNetworkEnrichmentV1V6(0, 0, 0, 9, value, membership); err != nil {
		t.Fatalf("push v6 0-9: %v", err)
	}
	if err := b.PushNetworkEnrichmentV1V6(1, 0, 1, 9, enrichment(64515), nil); err != nil {
		t.Fatalf("push v6 1-9: %v", err)
	}
	path := finishOutput(t, b)

	meta := outputMeta(t, path)
	if meta.StructureEntryCount != 2 || meta.MembershipEntryCount != 1 ||
		meta.RangeRecordCount != 2 {
		t.Fatalf("v6 meta %+v", meta)
	}
	r := reopen(t, path)
	defer r.Close()
	pin, err := r.Pin()
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	defer pin.Close()

	view, found, err := pin.LookupNetworkEnrichmentV1V6(iprangedb.IPv6FromHalves(0, 5))
	if err != nil || !found {
		t.Fatalf("LookupNetworkEnrichmentV1V6(0,5) = %v/%v", found, err)
	}
	got, err := view.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got.ASN != 64514 || !got.HasLocation ||
		got.Location.LatitudeMicrodegrees != -90_000_000 ||
		got.Location.LongitudeMicrodegrees != 180_000_000 {
		t.Fatalf("v6 value = %+v", got)
	}
	membershipView, present, err := view.ThreatMembership()
	if err != nil || !present {
		t.Fatalf("v6 ThreatMembership = %v/%v", present, err)
	}
	if has, err := membershipView.ContainsIndex(7); err != nil || !has {
		t.Fatalf("v6 contains 7 = %v/%v", has, err)
	}
	if has, err := membershipView.ContainsIndex(0); err != nil || has {
		t.Fatalf("v6 contains 0 = %v/%v, want false", has, err)
	}

	plain, found, err := pin.LookupNetworkEnrichmentV1V6(iprangedb.IPv6FromHalves(1, 5))
	if err != nil || !found {
		t.Fatalf("LookupNetworkEnrichmentV1V6(1,5) = %v/%v", found, err)
	}
	got, err = plain.Value()
	if err != nil {
		t.Fatalf("Value(1,5): %v", err)
	}
	if got.ASN != 64515 || got.HasLocation {
		t.Fatalf("v6 plain value = %+v", got)
	}
	if _, present, err := plain.ThreatMembership(); err != nil || present {
		t.Fatalf("v6 plain ThreatMembership = %v/%v, want absent", present, err)
	}
}

// TestOutputStructuredGuards mirrors the Rust require_structure_mode and
// failure latching: the WrongStructureKind and WrongAddressFamily classes
// with the verbatim detail strings, the absent-payload InvalidArgument,
// and the WrongState latch after a failed mutation.
func TestOutputStructuredGuards(t *testing.T) {
	direct, _ := newOutput(t, directSpec(format.AddressFamilyIPv4), generousBudget())
	err := direct.PushNetworkEnrichmentV1V4(0, 9, enrichment(1), nil)
	if !isWriterCode(err, format.CodeWrongStructureKind) || !containsDetail(err, "immutable output operation does not match its structure kind") {
		t.Fatalf("direct guard error %v", err)
	}
	if err := direct.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	membership, _ := newOutput(t, membershipSpec(1), generousBudget())
	err = membership.PushNetworkEnrichmentV1V4(0, 9, enrichment(1), nil)
	if !isWriterCode(err, format.CodeWrongStructureKind) || !containsDetail(err, "immutable output operation does not match its structure kind") {
		t.Fatalf("membership guard error %v", err)
	}
	if err := membership.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A structured spec naming an unknown structure kind is still the
	// structure-kind guard, not a crash.
	unknown, _ := newOutput(t, writer.OutputSpec{
		AddressFamily:  format.AddressFamilyIPv4,
		ValueKind:      format.ValueKindStructured,
		StructureKind:  2,
		ValueTag:       fixedTag("enrich"),
		DatabaseID:     fixed16(7),
		TxnID:          13,
		CommitNonce:    fixed16(8),
		FeedIndexLimit: 0,
	}, generousBudget())
	err = unknown.PushNetworkEnrichmentV1V4(0, 9, enrichment(1), nil)
	if !isWriterCode(err, format.CodeWrongStructureKind) || !containsDetail(err, "immutable output operation does not match its structure kind") {
		t.Fatalf("unknown-kind guard error %v", err)
	}
	if err := unknown.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Family mismatches on both directions.
	v4, _ := newOutput(t, structuredSpec(format.AddressFamilyIPv4, 0), generousBudget())
	err = v4.PushNetworkEnrichmentV1V6(0, 0, 0, 9, enrichment(1), nil)
	if !isWriterCode(err, format.CodeWrongAddressFamily) || !containsDetail(err, "immutable output operation does not match its address family") {
		t.Fatalf("v4/v6 guard error %v", err)
	}
	if err := v4.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	v6, _ := newOutput(t, structuredSpec(format.AddressFamilyIPv6, 0), generousBudget())
	err = v6.PushNetworkEnrichmentV1V4(0, 9, enrichment(1), nil)
	if !isWriterCode(err, format.CodeWrongAddressFamily) || !containsDetail(err, "immutable output operation does not match its address family") {
		t.Fatalf("v6/v4 guard error %v", err)
	}
	if err := v6.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// An all-zero payload is the canonical absent structure: the encoder
	// accepts it, the dictionary never interns it, and the range push
	// refuses it with the Rust InvalidArgument detail.
	absent, _ := newOutput(t, structuredSpec(format.AddressFamilyIPv4, 0), generousBudget())
	err = absent.PushNetworkEnrichmentV1V4(0, 9, format.NetworkEnrichmentV1{}, nil)
	if !isWriterCode(err, format.CodeInvalidArgument) || !containsDetail(err, "an absent structure cannot create a range") {
		t.Fatalf("absent push error %v", err)
	}
	// The failed mutation latches the builder (Rust require_active).
	err = absent.PushNetworkEnrichmentV1V4(10, 19, enrichment(2), nil)
	if !isWriterCode(err, format.CodeWrongState) || !containsDetail(err, "immutable output construction failed") {
		t.Fatalf("post-failure push error %v", err)
	}
	if err := absent.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Page-budget exhaustion inside the structured path keeps the Rust
	// InsufficientResourceBudget class.
	tiny, _ := newOutput(t, structuredSpec(format.AddressFamilyIPv4, 0), writer.OutputBudget{MaxOutputPages: 2})
	err = tiny.PushNetworkEnrichmentV1V4(0, 9, enrichment(1), nil)
	if !isWriterCode(err, format.CodeInsufficientResourceBudget) {
		t.Fatalf("budget push error %v", err)
	}
	if err := tiny.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestOutputStructuredMembershipDedupExactAfterBranchGrowth is the
// membership side of the Rust id_and_hash_indexes_remain_exact_after_branch_growth
// contract: 512 distinct memberships and 512 distinct structures push the
// hash trees past their first branch split, and every re-intern must
// resolve the original id instead of creating a duplicate (this is the
// regression that a little-endian id in a byte-compare tree caused).
func TestOutputStructuredMembershipDedupExactAfterBranchGrowth(t *testing.T) {
	const feeds = 512
	b, _ := newOutput(t, structuredSpec(format.AddressFamilyIPv4, feeds), generousBudget())
	words := make([][]uint64, feeds)
	for index := uint32(0); index < feeds; index++ {
		if err := b.PushFeed("feed"+strconv.FormatUint(uint64(index), 10), index); err != nil {
			t.Fatalf("push feed %d: %v", index, err)
		}
		// Every bitmap carries its own bit plus the shared tail bit 511
		// so the final word is nonzero and the bitmap is canonical.
		set := make([]uint64, feeds/64)
		set[index/64] = 1 << (index % 64)
		set[feeds/64-1] |= 1 << ((feeds - 1) % 64)
		words[index] = set
	}

	// Intern every distinct bitmap; ids are allocated lowest-free in
	// push order, so the first 512 interns get ids 1..512.
	ids := make([]uint32, feeds)
	for index := uint32(0); index < feeds; index++ {
		id, err := b.InternMembership(writer.OutputWords(words[index]))
		if err != nil {
			t.Fatalf("intern %d: %v", index, err)
		}
		ids[index] = id
	}
	// Re-intern every bitmap: every lookup must hit the original record
	// (no duplicate creation) exactly like the Rust branch-growth test.
	for index := uint32(0); index < feeds; index++ {
		id, err := b.InternMembership(writer.OutputWords(words[index]))
		if err != nil {
			t.Fatalf("re-intern %d: %v", index, err)
		}
		if id != ids[index] {
			t.Fatalf("re-intern %d = %d, want %d", index, id, ids[index])
		}
	}

	// Push one structured range per membership (single-address ranges,
	// twice each) so the structure dictionary and the range bulk also
	// cross the branch boundaries, and the membership references
	// accumulated through the batch apply once per id.
	for index := uint32(0); index < feeds; index++ {
		value := format.NetworkEnrichmentV1{ASN: 10_000 + index, CountryID: index}
		if err := b.PushNetworkEnrichmentV1V4(index*2, index*2, value, writer.OutputWords(words[index])); err != nil {
			t.Fatalf("push %d: %v", index, err)
		}
	}
	// The deduplicated second pass starts after the first pass so the
	// range stream stays canonical.
	for index := uint32(0); index < feeds; index++ {
		value := format.NetworkEnrichmentV1{ASN: 10_000 + index, CountryID: index}
		if err := b.PushNetworkEnrichmentV1V4(2048+index*2, 2048+index*2, value, writer.OutputWords(words[index])); err != nil {
			t.Fatalf("re-push %d: %v", index, err)
		}
	}
	path := finishOutput(t, b)

	meta := outputMeta(t, path)
	if meta.ActiveFeedCount != feeds || meta.MembershipEntryCount != feeds ||
		meta.MembershipIDLimit != feeds+1 || meta.StructureEntryCount != feeds ||
		meta.StructureIDLimit != feeds+1 || meta.RangeRecordCount != 2*feeds {
		t.Fatalf("branch-growth meta %+v", meta)
	}

	r := reopen(t, path)
	defer r.Close()
	pin, err := r.Pin()
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	defer pin.Close()
	for _, index := range []uint32{0, 100, 255, 256, 300, 511} {
		view, found, err := pin.LookupNetworkEnrichmentV1V4(iprangedb.IPv4(index * 2))
		if err != nil || !found {
			t.Fatalf("Lookup(%d) = %v/%v", index, found, err)
		}
		value, err := view.Value()
		if err != nil {
			t.Fatalf("Value(%d): %v", index, err)
		}
		if value.ASN != 10_000+index || value.CountryID != index {
			t.Fatalf("value at %d = %+v", index, value)
		}
		membershipView, present, err := view.ThreatMembership()
		if err != nil || !present {
			t.Fatalf("ThreatMembership(%d) = %v/%v", index, present, err)
		}
		if has, err := membershipView.ContainsIndex(index); err != nil || !has {
			t.Fatalf("contains %d = %v/%v, want true", index, has, err)
		}
		if has, err := membershipView.ContainsIndex(511); err != nil || !has {
			t.Fatalf("contains shared tail 511 = %v/%v, want true", has, err)
		}
		probe := uint32(1)
		if index == 0 {
			probe = 1
		} else {
			probe = 0
		}
		if has, err := membershipView.ContainsIndex(probe); err != nil || has {
			t.Fatalf("contains %d = %v/%v, want false", probe, has, err)
		}
	}
}
