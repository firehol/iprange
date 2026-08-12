package format

// Literal wire-byte vectors for every fixed-layout codec. Each vector is
// built from the little-endian field offsets of binary-format-v4.md, so a
// layout drift in any codec fails its direct test even when reader-level
// integration tests still pass.

import "testing"

func mustHex(t *testing.T, h string) []byte {
	t.Helper()
	b := make([]byte, len(h)/2)
	for i := 0; i < len(b); i++ {
		v := hexVal(h[i*2])<<4 | hexVal(h[i*2+1])
		b[i] = byte(v)
	}
	return b
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		panic("bad hex")
	}
}

func TestVectorsRangeRecordV6(t *testing.T) {
	// from 2001:db8::1 (lo=1, hi=0x20010db800000000), to 2001:db8::2,
	// value 0x0a000001.
	b := mustHex(t, "0100000000000000"+"00000000b80d0120"+
		"0200000000000000"+"00000000b80d0120"+"0100000a")
	r, err := DecodeRangeRecordV6(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.FromHi != 0x20010db800000000 || r.FromLo != 1 || r.ToHi != 0x20010db800000000 || r.ToLo != 2 || r.Value != 0x0a000001 {
		t.Fatalf("decoded %+v", r)
	}
	if _, err := DecodeRangeRecordV6(b[:35]); err == nil {
		t.Fatal("short v6 range accepted")
	}
	// Reversed: from > to is corruption.
	rev := mustHex(t, "0200000000000000"+"00000000b80d0120"+
		"0100000000000000"+"00000000b80d0120"+"0100000a")
	if _, err := DecodeRangeRecordV6(rev); err == nil {
		t.Fatal("reversed v6 range accepted")
	}
}

func TestVectorsRangeEntries(t *testing.T) {
	// Entry v4: first 10.0.0.0 (0x0a000000), child page 5.
	first, child, err := DecodeRangeEntryV4(mustHex(t, "0000000a"+"05000000"))
	if err != nil || first != 0x0a000000 || child != 5 {
		t.Fatalf("entry v4: %x %d %v", first, child, err)
	}
	if _, _, err := DecodeRangeEntryV4(mustHex(t, "0000000a"+"01000000")); err == nil {
		t.Fatal("entry v4 meta-page child accepted")
	}
	// Entry v6: first 2001:db8:: (lo=0, hi=0x20010db800000000), child 7.
	hi, lo, child, err := DecodeRangeEntryV6(mustHex(t, "0000000000000000"+"00000000b80d0120"+"07000000"))
	if err != nil || hi != 0x20010db800000000 || lo != 0 || child != 7 {
		t.Fatalf("entry v6: %x %x %d %v", hi, lo, child, err)
	}
	if _, _, _, err := DecodeRangeEntryV6(mustHex(t, "0000000000000000"+"00000000b80d0120"+"05000000")); err != nil {
		t.Fatal("entry v6 valid child rejected")
	}
}

func TestVectorsMembershipLeafInline(t *testing.T) {
	// record_len 72, storage 0, reserved 0, id 42, owner 7, words 1,
	// bitmap_len 8, blob_root 0, reserved 0, sha256 zero, inline
	// bitmap feed0 set.
	b := mustHex(t, "48000000"+"2a000000"+"0700000000000000"+"01000000"+"08000000"+
		"00000000"+"00000000"+
		"0000000000000000000000000000000000000000000000000000000000000000"+
		"ff00000000000000")
	r, err := DecodeMembershipIDLeaf(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Storage != MembershipStorageInline || r.MembershipID != 42 || r.OwnerRef != 7 ||
		r.WordCount != 1 || r.BitmapLen != 8 || r.BlobRoot != 0 || len(r.Inline) != 8 || r.Inline[0] != 0xff {
		t.Fatalf("decoded %+v inline=%x", r, r.Inline)
	}
	if _, err := DecodeMembershipIDLeaf(b[:63]); err == nil {
		t.Fatal("short inline leaf accepted")
	}
}

func TestVectorsMembershipLeafBlob(t *testing.T) {
	// record_len 64, storage 1, id 1, owner 2, words 8, bitmap_len 64,
	// blob_root page 9, reserved 0, sha256 zero.
	b := mustHex(t, "40000100"+"01000000"+"0200000000000000"+"08000000"+"40000000"+
		"09000000"+"00000000"+
		"0000000000000000000000000000000000000000000000000000000000000000")
	r, err := DecodeMembershipIDLeaf(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Storage != MembershipStorageBlob || r.WordCount != 8 || r.BitmapLen != 64 || r.BlobRoot != 9 {
		t.Fatalf("decoded %+v", r)
	}
	if len(r.Inline) != 0 {
		t.Fatal("blob leaf carries inline bytes")
	}
}

func TestVectorsMembershipBranch(t *testing.T) {
	r, err := DecodeMembershipIDBranch(mustHex(t, "00010000"+"0b000000"))
	if err != nil || r.FirstID != 0x00000100 || r.Child != 11 {
		t.Fatalf("branch %+v %v", r, err)
	}
	if _, err := DecodeMembershipIDBranch(mustHex(t, "00010000"+"01000000")); err == nil {
		t.Fatal("branch meta-page child accepted")
	}
}

func TestVectorsStructureIDRecord(t *testing.T) {
	// record_len 80, reserved 0, id 0x00000100, refcount 3, sha256 zero,
	// payload: ASN 1234, country 5, no location, no membership.
	b := mustHex(t, "50000000"+"00010000"+"0300000000000000"+
		"0000000000000000000000000000000000000000000000000000000000000000"+
		"d2040000"+"05000000"+"00000000"+"00000000"+
		"00000000"+"00000000"+"00000000"+"00000000")
	r, err := DecodeStructureIDRecord(b)
	if err != nil {
		t.Fatal(err)
	}
	if r.StructureID != 0x00000100 || r.RangeRefcount != 3 || len(r.Payload) != NetworkEnrichmentV1PayloadSize {
		t.Fatalf("record %+v", r)
	}
	v, err := DecodeNetworkEnrichmentV1(r.Payload)
	if err != nil || v.ASN != 1234 || v.CountryID != 5 {
		t.Fatalf("payload: %+v %v", v, err)
	}
	if _, err := DecodeStructureIDRecord(b[:79]); err == nil {
		t.Fatal("short structure record accepted")
	}
}

func TestVectorsNetworkEnrichmentV1(t *testing.T) {
	// ASN 15169 (0x3b41), no location: lat/long zero without the flag.
	want := NetworkEnrichmentV1{ASN: 15169}
	var enc [NetworkEnrichmentV1PayloadSize]byte
	EncodeNetworkEnrichmentV1(enc[:], want)
	v, err := DecodeNetworkEnrichmentV1(enc[:])
	if err != nil {
		t.Fatal(err)
	}
	if v != want {
		t.Fatalf("round trip %+v want %+v", v, want)
	}
	// Location set: lat 1_000_000 (0x000f4240), long -2_000_000
	// (0xffe17b80), membership 9, flags 1.
	b := mustHex(t, "413b0000"+"00000000"+"00000000"+"00000000"+
		"40420f00"+"807be1ff"+"09000000"+"01000000")
	v, err = DecodeNetworkEnrichmentV1(b)
	if err != nil {
		t.Fatal(err)
	}
	if v.ASN != 15169 || v.LatitudeMicrodegrees != 1_000_000 ||
		v.LongitudeMicrodegrees != -2_000_000 || v.MembershipID != 9 ||
		v.Flags != NetworkEnrichmentV1HasLocation {
		t.Fatalf("decoded %+v", v)
	}
	var out [NetworkEnrichmentV1PayloadSize]byte
	EncodeNetworkEnrichmentV1(out[:], v)
	for i := range b {
		if out[i] != b[i] {
			t.Fatalf("encode byte %d: %x want %x", i, out[i], b[i])
		}
	}
	// A nonzero location without the flag is invalid.
	bad := mustHex(t, "413b0000"+"00000000"+"00000000"+"00000000"+
		"40420f00"+"807be1ff"+"09000000"+"00000000")
	if _, err := DecodeNetworkEnrichmentV1(bad); err == nil {
		t.Fatal("unflagged location accepted")
	}
}

func TestVectorsBlobBranch(t *testing.T) {
	// logical offset 0x1000, child page 12, reserved zero.
	r, err := DecodeBlobBranch(mustHex(t, "0010000000000000"+"0c000000"+"00000000"))
	if err != nil || r.LogicalOffset != 0x1000 || r.Child != 12 {
		t.Fatalf("branch %+v %v", r, err)
	}
	if _, err := DecodeBlobBranch(mustHex(t, "0010000000000000"+"05000000"+"00000001")); err == nil {
		t.Fatal("blob branch reserved byte accepted")
	}
	if _, err := DecodeBlobBranch(mustHex(t, "0010000000000000"+"01000000"+"00000000")); err == nil {
		t.Fatal("blob branch meta-page child accepted")
	}
}
