package iprangedb

import "testing"

func TestCRC32CTestVector(t *testing.T) {
	// D9 mandatory vector.
	if got := crc32c([]byte("123456789")); got != 0xE3069283 {
		t.Fatalf("crc32c(\"123456789\") = 0x%08X, want 0xE3069283", got)
	}
}

func TestCRC32CKnownVectors(t *testing.T) {
	if got := crc32c([]byte("")); got != 0 {
		t.Errorf("crc32c(\"\") = 0x%08X, want 0", got)
	}
	if got := crc32c([]byte{0}); got != 0x527D5351 {
		t.Errorf("crc32c([0]) = 0x%08X, want 0x527D5351", got)
	}
}

func TestPageChecksumIgnoresChecksumField(t *testing.T) {
	var page [pageSize]byte
	for i := range page {
		page[i] = byte(i % 251)
	}
	sum := pageChecksum(page[:])
	le.PutUint64(page[phChecksum:], sum)
	if pageChecksum(page[:]) != sum {
		t.Fatal("checksum field not excluded from the span")
	}
	if !verifyPage(page[:]) {
		t.Fatal("verifyPage failed on self-consistent page")
	}
}

func TestVerifyRejectsCorruptionAndNonzeroHighHalf(t *testing.T) {
	var page [pageSize]byte
	for i := range page {
		page[i] = 7
	}
	sum := pageChecksum(page[:])
	le.PutUint64(page[phChecksum:], sum)
	if !verifyPage(page[:]) {
		t.Fatal("baseline verify failed")
	}
	// Flip a data byte -> reject.
	bad := page
	bad[100] ^= 0x01
	if verifyPage(bad[:]) {
		t.Fatal("corrupt page must not verify")
	}
	// Set a high-half bit of the checksum field -> reject even though low 32 match.
	hi := page
	hi[phChecksum+4] = 0x01
	if verifyPage(hi[:]) {
		t.Fatal("non-zero high half must be rejected")
	}
}
