//go:build windows

package live

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// gcTestHeader builds one exact Windows GC header (Rust
// gc_codec_tests.rs header: UTF-16LE source basename, kind 2 identity
// and security kinds, full payload tuple).
func gcTestHeader() *gcHeader {
	source := gcNameBytesPlatform("source")
	return &gcHeader{
		kind:                  ArtifactPrivateOutput,
		basenameEncoding:      2,
		attemptID:             [16]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		ordinal:               7,
		directoryIdentityKind: 2,
		artifactIdentityKind:  2,
		directoryIdentity:     [32]byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
		sourceCommitment:      gcSourceCommitment(2, source),
		inertCommitment:       gcInertCommitment(2, []byte("inert")),
		artifactIdentity:      [32]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3},
		payload: &gcPayload{
			byteLength:    8192,
			sha512:        [64]byte{4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4},
			databaseID:    [16]byte{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5},
			transactionID: 6,
			commitNonce:   [16]byte{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7},
		},
		creationSecurityKind:   2,
		directoryRole:          DirectoryRoleDestination,
		creationSecurityCommit: [32]byte{8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8},
		sourceBasename:         source,
		sequence:               1,
	}
}

func gcTestFileBytes(header *gcHeader) []byte {
	bytes := make([]byte, gcEnvelopeSize)
	if err := header.gcEncode(bytes[:format.PageSize]); err != nil {
		panic(err)
	}
	if err := header.gcEncode(bytes[format.PageSize:]); err != nil {
		panic(err)
	}
	return bytes
}

func TestGCExactLayoutRoundTripsWithEitherCompleteCopy(t *testing.T) {
	expected := gcTestHeader()
	bytes := gcTestFileBytes(expected)
	selected, err := gcSelect(bytes)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !gcHeaderEqual(selected, expected) {
		t.Fatalf("selected header does not round trip")
	}
	if string(bytes[:8]) != "IPR4GCA1" {
		t.Fatalf("magic %q", bytes[:8])
	}
	if format.U16(bytes[8:10]) != 512 {
		t.Fatalf("record size %d", format.U16(bytes[8:10]))
	}
	if format.U16(bytes[136:138]) != 1 {
		t.Fatalf("payload kind %d", format.U16(bytes[136:138]))
	}
	if format.U16(bytes[290:292]) != 1 {
		t.Fatalf("directory role %d", format.U16(bytes[290:292]))
	}
	if format.U64(bytes[496:504]) != 1 {
		t.Fatalf("sequence %d", format.U64(bytes[496:504]))
	}
	bytes[508] ^= 1
	selected, err = gcSelect(bytes)
	if err != nil || !gcHeaderEqual(selected, expected) {
		t.Fatalf("right copy must select after left CRC corruption: %v", err)
	}
}

func TestGCMalformedReservedPayloadAndDisagreementFailClosed(t *testing.T) {
	expected := gcTestHeader()
	bytes := gcTestFileBytes(expected)
	bytes[292] = 1
	bytes[format.PageSize+292] = 1
	if _, err := gcSelect(bytes); err == nil {
		t.Fatalf("reserved security bytes must fail selection")
	}

	bytes = gcTestFileBytes(expected)
	other := *expected
	other.ordinal = 8
	other.sequence = 2
	copy(bytes[format.PageSize:], gcTestFileBytes(&other)[format.PageSize:])
	if _, err := gcSelect(bytes); err == nil {
		t.Fatalf("disagreeing authorities must fail selection")
	}
}

func TestGCUnknownPayloadRequiresEveryOptionalFieldZero(t *testing.T) {
	expected := gcTestHeader()
	expected.payload = nil
	bytes := gcTestFileBytes(expected)
	if _, err := gcSelect(bytes); err != nil {
		t.Fatalf("select nil payload: %v", err)
	}
	// A stray payload length with a recomputed CRC must fail: the
	// reserved range check folds the record.
	bytes[176] = 1
	checksum, ok := format.CRC32CWithZeroed(bytes[:format.PageSize], gcCRCOffset, gcCRCWindow)
	if !ok || checksum == 0 {
		t.Fatalf("checksum computation failed")
	}
	format.PutU32(bytes[gcCRCOffset:], checksum)
	copy(bytes[format.PageSize:], bytes[:format.PageSize])
	if _, err := gcSelect(bytes); err == nil {
		t.Fatalf("stray payload length must fail selection")
	}
}

func TestGCSourceFilenameIsStoredWithoutAPathAndAuthenticated(t *testing.T) {
	expected := gcTestHeader()
	bytes := gcTestFileBytes(expected)
	if int(format.U32(bytes[gcSourceLenOff:])) != len(expected.sourceBasename) {
		t.Fatalf("source length mismatch")
	}
	if string(bytes[gcSourceOffset:gcSourceOffset+len(expected.sourceBasename)]) != string(expected.sourceBasename) {
		t.Fatalf("source bytes mismatch")
	}
	bytes[gcSourceOffset] ^= 1
	checksum, ok := format.CRC32CWithZeroed(bytes[:format.PageSize], gcCRCOffset, gcCRCWindow)
	if !ok {
		t.Fatalf("checksum computation failed")
	}
	format.PutU32(bytes[gcCRCOffset:], checksum)
	copy(bytes[format.PageSize:], bytes[:format.PageSize])
	if _, err := gcSelect(bytes); err == nil {
		t.Fatalf("tampered source bytes must fail selection")
	}
}

func TestGCExactPayloadMayDescribeArbitraryNonV4Bytes(t *testing.T) {
	expected := gcTestHeader()
	expected.payload = &gcPayload{
		byteLength: 7,
		sha512:     [64]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9},
	}
	if _, err := gcSelect(gcTestFileBytes(expected)); err != nil {
		t.Fatalf("unknown-payload tuple must select: %v", err)
	}
}

func TestGCSelectionPrefersTheHigherSequenceOfMatchingBlocks(t *testing.T) {
	left := gcTestHeader()
	right := *left
	right.sequence = 2
	fixed := *left
	fixed.sequence = 1
	bytes := make([]byte, gcEnvelopeSize)
	if err := left.gcEncode(bytes[:format.PageSize]); err != nil {
		t.Fatal(err)
	}
	if err := right.gcEncode(bytes[format.PageSize:]); err != nil {
		t.Fatal(err)
	}
	selected, err := gcSelect(bytes)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if selected.sequence != 2 {
		t.Fatalf("selected sequence %d, want 2", selected.sequence)
	}
	_ = fixed
}

func TestGCNameEncodingIsExactLowercaseFixedWidth(t *testing.T) {
	attempt := [16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}
	name, err := gcEnvelopeName(attempt, 0x89abcdef)
	if err != nil {
		t.Fatal(err)
	}
	if name != ".iprange-gcauth-0123456789abcdeffedcba9876543210-89abcdef.tmp" {
		t.Fatalf("envelope name %q", name)
	}
	decoded, ordinal, ok := gcDecodeEnvelope([]byte(name))
	if !ok || decoded != attempt || ordinal != 0x89abcdef {
		t.Fatalf("decode round trip failed")
	}
	inert, err := gcInertName(attempt, 7)
	if err != nil {
		t.Fatal(err)
	}
	if inert != ".iprange-gc-0123456789abcdeffedcba9876543210-00000007.tmp" {
		t.Fatalf("inert name %q", inert)
	}
	if _, _, ok := gcDecodeInert([]byte(".iprange-gc-0123456789abcdeffedcba9876543210-00000007.tmp")); !ok {
		t.Fatalf("inert decode failed")
	}
	if _, _, ok := gcDecodeEnvelope([]byte(".iprange-gcauth-0123456789ABCDEFFEDCBA9876543210-89abcdef.tmp")); ok {
		t.Fatalf("uppercase hex must be rejected")
	}
	if _, _, ok := gcDecodeEnvelope([]byte(".iprange-gcauth-00000000000000000000000000000000-00000000.tmp")); ok {
		t.Fatalf("zero attempt must be rejected")
	}
	if _, _, ok := gcDecodeEnvelope([]byte(".iprange-gcauth-0123456789abcdeffedcba9876543210-1.tmp")); ok {
		t.Fatalf("short ordinal must be rejected")
	}
}
