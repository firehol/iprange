package exactv4

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

func TestValueTagCanonicalAndRetentionExact(t *testing.T) {
	tag := RetentionTag()
	if got := string(tag.Bytes()); got != "retention" {
		t.Fatalf("retention bytes = %q", got)
	}
	if got := tag.Wire(); got != [16]byte{'r', 'e', 't', 'e', 'n', 't', 'i', 'o', 'n'} {
		t.Fatalf("retention wire = %x", got)
	}
	if _, err := NewValueTag([]byte("123456789012345")); err != nil {
		t.Fatalf("15-byte tag rejected: %v", err)
	}
	if _, err := NewValueTag([]byte("1234567890123456")); err == nil {
		t.Fatal("16-byte tag accepted")
	}
	if _, err := NewValueTag([]byte{'b', 'a', 'd', 0, 't', 'a', 'g'}); err == nil {
		t.Fatal("embedded NUL accepted")
	}
	if _, ok := valueTagFromWire([16]byte{'b', 'a', 'd', 0, 'x'}); ok {
		t.Fatal("nonzero byte after first NUL accepted")
	}
	if _, ok := valueTagFromWire([16]byte{'1', '2', '3', '4', '5', '6', '7', '8', '9', '0', '1', '2', '3', '4', '5', '6'}); ok {
		t.Fatal("wire tag without mandatory NUL accepted")
	}
}

func TestExactMetaOffsetsAndCRC(t *testing.T) {
	meta := emptyDirectMeta(7)
	page := meta.EncodePage()

	if string(page[0:8]) != MetaMagic {
		t.Fatalf("magic = %q", page[0:8])
	}
	if got := binary.LittleEndian.Uint16(page[8:10]); got != 256 {
		t.Fatalf("meta size = %d", got)
	}
	if page[10] != 12 || page[11] != 4 || page[12] != 1 {
		t.Fatalf("fixed fields = %d/%d/%d", page[10], page[11], page[12])
	}
	if got := binary.LittleEndian.Uint64(page[48:56]); got != 7 {
		t.Fatalf("txn id = %d", got)
	}
	if got := binary.LittleEndian.Uint64(page[72:80]); got != 2 {
		t.Fatalf("page count = %d", got)
	}
	stored := binary.LittleEndian.Uint32(page[MetaCRCOffset : MetaCRCOffset+4])
	if got := metaCRC(page[:]); got != stored {
		t.Fatalf("meta CRC = %#x, want %#x", stored, got)
	}
	if !bytes.Equal(page[256:], make([]byte, PageSize-256)) {
		t.Fatal("bytes after exact 256-byte meta are not zero")
	}
}

func TestExactNonMetaConstants(t *testing.T) {
	if PageHeaderSize != 32 || MaxTreeLevel != 31 || PageMagic != "IP4P" {
		t.Fatalf("header constants = %d/%d/%q", PageHeaderSize, MaxTreeLevel, PageMagic)
	}
	if PageTypeRangeBranch != 1 || PageTypeRetirementLeaf != 17 {
		t.Fatalf("page type endpoints = %d/%d", PageTypeRangeBranch, PageTypeRetirementLeaf)
	}
}

func TestCRC32CAndExactOnlyIdentity(t *testing.T) {
	if got := crc32.Update(0, castagnoliTable, []byte("123456789")); got != 0xe3069283 {
		t.Fatalf("CRC-32C check value = %#x", got)
	}

	page := emptyDirectMeta(1).EncodePage()
	binary.LittleEndian.PutUint16(page[8:10], 98) // obsolete experimental meta size
	binary.LittleEndian.PutUint32(page[MetaCRCOffset:MetaCRCOffset+4], metaCRC(page[:]))
	data := make([]byte, 2*PageSize)
	copy(data[:PageSize], page[:])
	copy(data[PageSize:], page[:])
	problem := requireBootstrapCode(t, openError(data, OpenImmutableReader), BootstrapErrNoBootstrapMeta)
	if problem.Meta0 != MetaProblemFixedValue || problem.Meta1 != MetaProblemFixedValue {
		t.Fatalf("obsolete meta findings = %d/%d", problem.Meta0, problem.Meta1)
	}
}
