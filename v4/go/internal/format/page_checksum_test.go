package format

import (
	"hash/crc32"
	"testing"
)

// TestCRC32CCheckValue pins the reflected Castagnoli check value used by
// every v4 page seal.
func TestCRC32CCheckValue(t *testing.T) {
	if got := CRC32C([]byte("123456789")); got != 0xe3069283 {
		t.Fatalf("CRC-32C check value = %#x, want 0xe3069283", got)
	}
}

// TestCRC32CWithZeroed compares the zeroed-window helper against a direct
// computation over a zero-substituted copy, mirroring Rust crc32c_with_zeroed
// semantics.
func TestCRC32CWithZeroed(t *testing.T) {
	data := []byte("the quick brown fox jumps over the lazy dog")
	for _, tc := range []struct {
		zeroAt, zeroLen int
	}{
		{0, 0},
		{4, 0},
		{0, 4},
		{len(data), 0},
		{4, 10},
		{10, 20},
		{0, len(data)},
	} {
		want := make([]byte, len(data))
		copy(want, data)
		for i := tc.zeroAt; i < tc.zeroAt+tc.zeroLen; i++ {
			want[i] = 0
		}
		got, ok := CRC32CWithZeroed(data, tc.zeroAt, tc.zeroLen)
		if !ok {
			t.Fatalf("CRC32CWithZeroed(%d,%d) reported invalid", tc.zeroAt, tc.zeroLen)
		}
		if wantCRC := crc32.Checksum(want, crc32cTable); got != wantCRC {
			t.Fatalf("CRC32CWithZeroed(%d,%d) = %#x, want %#x", tc.zeroAt, tc.zeroLen, got, wantCRC)
		}
	}
}

// TestCRC32CWithZeroedInvalid pins the invalid-range refusal: the zeroed
// range must never exceed the input.
func TestCRC32CWithZeroedInvalid(t *testing.T) {
	if _, ok := CRC32CWithZeroed([]byte("abc"), 1, 3); ok {
		t.Fatal("range exceeding input reported valid")
	}
	if _, ok := CRC32CWithZeroed([]byte("abc"), -1, 1); ok {
		t.Fatal("negative offset reported valid")
	}
}

// TestSealAndValid pins the seal lifecycle: SealPageChecksum produces a valid
// seal, clearing or mutating the page invalidates it, and short pages are
// refused.
func TestSealAndValid(t *testing.T) {
	page := make([]byte, PageSize)
	if PageChecksumValid(page) {
		t.Fatal("unsealed page reported valid")
	}
	if err := SealPageChecksum(page); err != nil {
		t.Fatal("seal:", err)
	}
	if !PageChecksumValid(page) {
		t.Fatal("sealed page reported invalid")
	}
	// The seal covers the whole page with the checksum field zeroed: a
	// single flipped data byte must invalidate it.
	page[100] ^= 0xff
	if PageChecksumValid(page) {
		t.Fatal("mutated page still reported valid")
	}
	page[100] ^= 0xff
	PutU32(page[PageChecksumOffset:], 0)
	if PageChecksumValid(page) {
		t.Fatal("cleared checksum field still reported valid")
	}
	if err := SealPageChecksum(page[:PageChecksumOffset]); err == nil {
		t.Fatal("seal of short page succeeded")
	}
	if PageChecksumValid(page[:PageChecksumOffset+PageChecksumLength-1]) {
		t.Fatal("short page reported valid")
	}
}
