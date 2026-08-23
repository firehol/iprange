// Structure dictionary tests mirroring the Rust structured_value
// manager_tests.rs and network_enrichment_v1.rs codec tests: payload
// deduplication and lowest-id reuse, equal-digest collisions, branch
// growth over the complete radix path, direct-table root shrinking, and
// the canonical network_enrichment_v1 payload bytes and validation.

package writer

import (
	"bytes"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// structureNetCodec is the registry codec under test.
var structureNetCodec = structureNetworkEnrichmentV1{}

// structureTestState is the fresh dictionary state of manager_tests.rs
// state().
func structureTestState() structureState {
	return structureState{idRoot: 0, hashRoot: 0, usedRoot: 0, entryCount: 0, idLimit: 1}
}

// structureTestPayload builds one canonical 32-byte payload (Rust
// payload(bytes) with TestCodec payloads: the first bytes name the value,
// the rest are zero, so every payload passes the network_enrichment_v1
// validation).
// internStructureTest interns one payload value (the production
// internStructure takes the address of a caller-owned payload; the test
// helper owns the value).
func internStructureTest(codec structurePayloadCodec, store tree.RetiringStore, state *structureState, value structurePayload) (structureInterned, error) {
	return internStructure(codec, store, state, &value)
}

func structureTestPayload(value ...byte) structurePayload {
	var bytes [format.NetworkEnrichmentV1PayloadSize]byte
	copy(bytes[:], value)
	payload, err := newStructurePayload(bytes[:])
	if err != nil {
		panic(err)
	}
	return payload
}

// structureTestRootLevel inspects the current id-root level (Rust
// inspect_page + table::parse).
func structureTestRootLevel(t *testing.T, store *rangeMemoryStore, root uint32) uint16 {
	t.Helper()
	page, err := store.Inspect(root)
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	header, err := structureTableParse(structureNetCodec, page, store.TargetTxn(), nil)
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	return header.level
}

func TestStructureEqualPayloadsDeduplicateAndLowestReleasedIDIsReused(t *testing.T) {
	store := newRangeMemoryStore()
	state := structureTestState()
	first, err := internStructureTest(structureNetCodec, store, &state, structureTestPayload(1, 2, 3, 4))
	if err != nil {
		t.Fatalf("intern first: %v", err)
	}
	same, err := internStructureTest(structureNetCodec, store, &state, structureTestPayload(1, 2, 3, 4))
	if err != nil {
		t.Fatalf("intern same: %v", err)
	}
	second, err := internStructureTest(structureNetCodec, store, &state, structureTestPayload(5, 6, 7, 8))
	if err != nil {
		t.Fatalf("intern second: %v", err)
	}
	if same.id != first.id || same.created {
		t.Fatalf("duplicate intern: got id=%d created=%v, want id=%d created=false", same.id, same.created, first.id)
	}
	if second.id == first.id {
		t.Fatalf("distinct payloads merged: both id %d", second.id)
	}

	for _, id := range []uint32{first.id, second.id} {
		if _, err := applyStructureDelta(structureNetCodec, store, &state, id, 1); err != nil {
			t.Fatalf("apply +1 for id %d: %v", id, err)
		}
	}
	if _, err := applyStructureDelta(structureNetCodec, store, &state, first.id, -1); err != nil {
		t.Fatalf("apply -1 for id %d: %v", first.id, err)
	}
	replacement, err := internStructureTest(structureNetCodec, store, &state, structureTestPayload(9, 10, 11, 12))
	if err != nil {
		t.Fatalf("intern replacement: %v", err)
	}
	if replacement.id != first.id {
		t.Fatalf("lowest released id not reused: got %d, want %d", replacement.id, first.id)
	}
	if !replacement.created {
		t.Fatalf("replacement intern reported existing")
	}
}

func TestStructureRefcountAccumulatesAndReleases(t *testing.T) {
	store := newRangeMemoryStore()
	state := structureTestState()
	interned, err := internStructureTest(structureNetCodec, store, &state, structureTestPayload(1, 0, 0, 0))
	if err != nil {
		t.Fatalf("intern: %v", err)
	}
	if _, err := applyStructureDelta(structureNetCodec, store, &state, interned.id, 1); err != nil {
		t.Fatalf("apply +1: %v", err)
	}
	if _, err := applyStructureDelta(structureNetCodec, store, &state, interned.id, 1); err != nil {
		t.Fatalf("apply +1: %v", err)
	}
	record, found, err := structureTableFind(structureNetCodec, store, state.idRoot, state.idLimit, interned.id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !found || record.refcount != 2 {
		t.Fatalf("refcount after two adds: got %d found=%v, want 2", record.refcount, found)
	}
	if _, err := applyStructureDelta(structureNetCodec, store, &state, interned.id, -1); err != nil {
		t.Fatalf("apply -1: %v", err)
	}
	record, found, err = structureTableFind(structureNetCodec, store, state.idRoot, state.idLimit, interned.id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !found || record.refcount != 1 {
		t.Fatalf("refcount after one release: got %d found=%v, want 1", record.refcount, found)
	}
	if _, err := applyStructureDelta(structureNetCodec, store, &state, interned.id, -1); err != nil {
		t.Fatalf("apply -1: %v", err)
	}
	if _, found, err := structureTableFind(structureNetCodec, store, state.idRoot, state.idLimit, interned.id); err != nil {
		t.Fatalf("find: %v", err)
	} else if found {
		t.Fatalf("released record still present")
	}
	if _, err := applyStructureDelta(structureNetCodec, store, &state, interned.id, -1); err == nil {
		t.Fatalf("releasing an absent ID succeeded; want corrupt")
	}
}

func TestStructureEqualDigestWithUnequalPayloadDoesNotMerge(t *testing.T) {
	store := newRangeMemoryStore()
	state := structureTestState()
	wanted := structureTestPayload(1, 2, 3, 4)
	digest, err := structurePayloadDigest(structureNetCodec, &wanted)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	fakeID, err := bitmap.AllocateLowestID(store, &state.usedRoot, &state.idLimit, state.entryCount,
		bitmap.KindStructure, structureIDExhausted)
	if err != nil {
		t.Fatalf("allocate fake id: %v", err)
	}
	other := structureTestPayload(5, 6, 7, 8)
	record, err := encodeStructureRecord(structureNetCodec, fakeID, digest, &other)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := structureTableInsert(structureNetCodec, store, &state.idRoot, state.idLimit, record.Slice()); err != nil {
		t.Fatalf("insert id record: %v", err)
	}
	key := structureHashKey(digest, fakeID)
	if err := insertStructureHashRecord(store, structureHashCodec{kind: structureNetCodec.kind()}, &state.hashRoot, key[:]); err != nil {
		t.Fatalf("insert hash record: %v", err)
	}
	state.entryCount = 1

	actual, err := internStructure(structureNetCodec, store, &state, &wanted)
	if err != nil {
		t.Fatalf("intern wanted: %v", err)
	}
	if !actual.created {
		t.Fatalf("equal-digest unequal-payload intern reported existing")
	}
	if actual.id == fakeID {
		t.Fatalf("colliding payload merged with id %d", fakeID)
	}
}

func TestStructureIDAndHashIndexesRemainExactAfterBranchGrowth(t *testing.T) {
	store := newRangeMemoryStore()
	state := structureTestState()
	ids := make(map[uint32]uint32, 512)
	for value := uint32(1); value <= 512; value++ {
		payload := structureTestPayload(byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
		interned, err := internStructure(structureNetCodec, store, &state, &payload)
		if err != nil {
			t.Fatalf("intern %d: %v", value, err)
		}
		if !interned.created {
			t.Fatalf("intern %d reported existing", value)
		}
		ids[interned.id] = value
	}
	if state.entryCount != 512 {
		t.Fatalf("entry count: got %d, want 512", state.entryCount)
	}
	for id, value := range ids {
		record, found, err := structureTableFind(structureNetCodec, store, state.idRoot, state.idLimit, id)
		if err != nil {
			t.Fatalf("find %d: %v", id, err)
		}
		if !found || !bytes.Equal(record.payload.Slice(), structureTestPayload(byte(value), byte(value>>8), byte(value>>16), byte(value>>24)).Slice()) {
			t.Fatalf("record %d payload mismatch", id)
		}
		duplicatePayload := structureTestPayload(byte(value), byte(value>>8), byte(value>>16), byte(value>>24))
		duplicate, err := internStructure(structureNetCodec, store, &state, &duplicatePayload)
		if err != nil {
			t.Fatalf("duplicate intern %d: %v", value, err)
		}
		if duplicate.id != id || duplicate.created {
			t.Fatalf("duplicate intern of %d: got id=%d created=%v, want id=%d created=false", value, duplicate.id, duplicate.created, id)
		}
	}
}

func TestStructureDirectTableRootShrinksWhenTrailingIDsAreReleased(t *testing.T) {
	store := newRangeMemoryStore()
	state := structureTestState()
	count := format.StructureRecordSlots + 2
	for value := uint32(1); value <= uint32(count); value++ {
		payload := structureTestPayload(byte(value), 0, 0, 0)
		interned, err := internStructure(structureNetCodec, store, &state, &payload)
		if err != nil {
			t.Fatalf("intern %d: %v", value, err)
		}
		if _, err := applyStructureDelta(structureNetCodec, store, &state, interned.id, 1); err != nil {
			t.Fatalf("apply +1 for id %d: %v", interned.id, err)
		}
	}
	if want := uint16(1); structureTestRootLevel(t, store, state.idRoot) != want {
		t.Fatalf("root level after %d entries: got %d, want %d", count, structureTestRootLevel(t, store, state.idRoot), want)
	}

	for id := uint32(format.StructureRecordSlots); id <= uint32(count); id++ {
		if _, err := applyStructureDelta(structureNetCodec, store, &state, id, -1); err != nil {
			t.Fatalf("release %d: %v", id, err)
		}
	}
	if state.idLimit != format.StructureRecordSlots {
		t.Fatalf("id limit after releases: got %d, want %d", state.idLimit, format.StructureRecordSlots)
	}
	if want := uint16(0); structureTestRootLevel(t, store, state.idRoot) != want {
		t.Fatalf("root level after releases: got %d, want %d", structureTestRootLevel(t, store, state.idRoot), want)
	}
}

func TestStructureAbsentPayloadNeverInterns(t *testing.T) {
	store := newRangeMemoryStore()
	state := structureTestState()
	interned, err := internStructureTest(structureNetCodec, store, &state, structureTestPayload())
	if err != nil {
		t.Fatalf("intern absent: %v", err)
	}
	if interned.id != 0 || interned.created || state.entryCount != 0 || state.idRoot != 0 || state.hashRoot != 0 {
		t.Fatalf("absent payload interned: %+v state %+v", interned, state)
	}
}

func TestStructureRefcountDeltaOnAbsentIDFails(t *testing.T) {
	store := newRangeMemoryStore()
	state := structureTestState()
	if _, err := applyStructureDelta(structureNetCodec, store, &state, 1, 1); err == nil {
		t.Fatalf("delta on empty dictionary succeeded")
	} else if err.Error() != (&format.Error{Code: format.CodeFormatInvalid, Detail: "structure refcount names an absent ID"}).Error() {
		t.Fatalf("delta error %v, want structure refcount names an absent ID", err)
	}
}

func TestNetworkEnrichmentV1CodecRoundTripsPresentZeroLocation(t *testing.T) {
	value := format.NetworkEnrichmentV1{
		ASN: 64512, CountryID: 7, StateID: 8, CityID: 9,
		Flags: format.NetworkEnrichmentV1HasLocation,
	}
	payload, err := encodeNetworkEnrichmentV1(value, 42)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := format.DecodeNetworkEnrichmentV1(payload.Slice())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ASN != value.ASN || decoded.CountryID != 7 || decoded.StateID != 8 || decoded.CityID != 9 ||
		decoded.MembershipID != 42 || decoded.Flags != format.NetworkEnrichmentV1HasLocation {
		t.Fatalf("round trip mismatch: %+v", decoded)
	}
	if got := structureNetCodec.membershipID(&payload); got != 42 {
		t.Fatalf("membership id: got %d, want 42", got)
	}
}

func TestNetworkEnrichmentV1PayloadMatchesLiteralBytes(t *testing.T) {
	value := format.NetworkEnrichmentV1{
		ASN: 0x11223344, CountryID: 0x55667788, StateID: 0x99aabbcc, CityID: 0xddeeff00,
		LatitudeMicrodegrees:  0x01020304,
		LongitudeMicrodegrees: -0x01020304,
		Flags:                 format.NetworkEnrichmentV1HasLocation,
	}
	payload, err := encodeNetworkEnrichmentV1(value, 0x0a0b0c0d)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := []byte{
		0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55, 0xcc, 0xbb, 0xaa, 0x99, 0x00, 0xff,
		0xee, 0xdd, 0x04, 0x03, 0x02, 0x01, 0xfc, 0xfc, 0xfd, 0xfe, 0x0d, 0x0c, 0x0b, 0x0a,
		0x01, 0x00, 0x00, 0x00,
	}
	if !bytes.Equal(payload.Slice(), want) {
		t.Fatalf("payload bytes mismatch:\n got %x\nwant %x", payload.Slice(), want)
	}
}

func TestNetworkEnrichmentV1RejectsNoncanonicalAbsentLocation(t *testing.T) {
	var bytes [format.NetworkEnrichmentV1PayloadSize]byte
	bytes[enrichmentLatOffset] = 1
	payload, err := newStructurePayload(bytes[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := structureNetCodec.validate(payload); err == nil {
		t.Fatalf("absent location with nonzero coordinates accepted")
	}
}

func TestNetworkEnrichmentV1EncodeRejectsOutOfRangeLocation(t *testing.T) {
	value := format.NetworkEnrichmentV1{
		ASN:                  64512,
		LatitudeMicrodegrees: 90_000_001,
		Flags:                format.NetworkEnrichmentV1HasLocation,
	}
	if _, err := encodeNetworkEnrichmentV1(value, 0); err == nil {
		t.Fatalf("out-of-range latitude accepted")
	}
	value.LatitudeMicrodegrees = 0
	value.LongitudeMicrodegrees = 180_000_001
	if _, err := encodeNetworkEnrichmentV1(value, 0); err == nil {
		t.Fatalf("out-of-range longitude accepted")
	}
}

func TestNetworkEnrichmentV1EncodeClearsUnknownFlagBits(t *testing.T) {
	value := format.NetworkEnrichmentV1{
		ASN:                  64512,
		LatitudeMicrodegrees: 1,
		Flags:                format.NetworkEnrichmentV1HasLocation | 0x80000000,
	}
	payload, err := encodeNetworkEnrichmentV1(value, 0)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := format.DecodeNetworkEnrichmentV1(payload.Slice())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Flags != format.NetworkEnrichmentV1HasLocation {
		t.Fatalf("flags not canonical: got %d, want 1", decoded.Flags)
	}
	if decoded.LatitudeMicrodegrees != 1 {
		t.Fatalf("latitude lost: got %d, want 1", decoded.LatitudeMicrodegrees)
	}
	if err := structureNetCodec.validate(payload); err != nil {
		t.Fatalf("canonical payload rejected: %v", err)
	}
}
