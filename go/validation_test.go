package iprangeformat

import "testing"

func TestReaderRejectsNonAscendingMembershipSet(t *testing.T) {
	w := NewWriterV4(FeedMeta{Name: "m"}, 0, 0)
	body := []byte{1, 0, 0, 0, 5, 0, 0, 0} // ascending LE u32 ids 1, 5
	if err := w.AddRange(10, 20, &Value{TypeID: 1, Bytes: body}); err != nil {
		t.Fatal(err)
	}
	bytes, err := w.Build()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(bytes)
	if err != nil {
		t.Fatal(err)
	}
	// payload (count4|type4|len4|bytes): id0 at valuesOff+12, id1 at valuesOff+16.
	// Rewrite to [5, 1] (non-ascending) — validateValues (pre-hash) must reject.
	bytes[r.valuesOff+12] = 5
	bytes[r.valuesOff+16] = 1
	if _, err := Open(bytes); err == nil {
		t.Fatal("expected rejection of non-ascending membership set")
	}
}

func TestReaderRejectsCorruptedUniqueCount(t *testing.T) {
	w := NewWriterV4(FeedMeta{Name: "m"}, 0, 0)
	if err := w.AddRange(0x0a000000, 0x0a0000ff, nil); err != nil {
		t.Fatal(err)
	}
	bytes, err := w.Build()
	if err != nil {
		t.Fatal(err)
	}
	bytes[56] ^= 0xff // unique_ip_count_lo (header offset 56) — header is not hashed
	if _, err := Open(bytes); err == nil {
		t.Fatal("expected rejection of corrupted unique_ip_count")
	}
}

func TestWriterRejectsReservedLicenseFlags(t *testing.T) {
	w := NewWriterV4(FeedMeta{Name: "m"}, 0b10, 0) // reserved bit set
	if err := w.AddRange(1, 2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Build(); err == nil {
		t.Fatal("expected rejection of reserved license_flags")
	}
}

func TestWriterRejectsInvalidUTF8FeedMeta(t *testing.T) {
	w := NewWriterV4(FeedMeta{Name: string([]byte{0xff, 0xfe})}, 0, 0) // invalid UTF-8
	if err := w.AddRange(1, 2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Build(); err == nil {
		t.Fatal("expected rejection of invalid-UTF-8 feed-meta")
	}
}
