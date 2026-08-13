package reader

// Adversarial regression tests for the fifth-pass review findings. Each test
// mutates a real fixture (or builds a synthetic page) and requires the typed
// refusal the format contract specifies. Page CRCs are intentionally not
// maintained for data-page mutations: the immutable reader does not check
// page-level CRCs at ordinary access time, mirroring the Rust reader.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func mustRead(t *testing.T, file *os.File, page, off, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := file.ReadAt(buf, int64(page*format.PageSize+off)); err != nil && len(buf) != 0 {
		t.Fatal(err)
	}
	return buf
}

func fixtureSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// auditMetaNonzeroTail rejects meta pages whose bytes [256,4096) are not
// zero (binary-format-v4.md:252).
func TestMetaNonzeroTailRejected(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "meta-tail.iprdb")
	patchMetaEach(t, path, func(_ int, page []byte) {
		page[300] = 0x01
	})
	if _, err := OpenImmutable(path); err == nil {
		t.Fatal("open accepted nonzero meta tail bytes")
	}
}

// auditAuxRequiredRejected: catalog pages require aux == 0; an arbitrary
// aux must be refused before following any pointer (binary-format-v4.md:521).
func TestCatalogAuxRequiredRejected(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "catalog-aux.iprdb")
	// Page 2 is the catalog name leaf (type 4, aux 0).
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteAt([]byte{0, 0, 0, 99}, int64(2*format.PageSize+24)); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, err := r.LookupFeed("feed-000"); err == nil {
		t.Fatal("catalog lookup accepted page with wrong aux")
	}
}

// auditMetadataTrailingBytes: one complete zlib stream only; trailing bytes
// are not v4 metadata (binary-format-v4.md:1016).
func TestMetadataTrailingBytesRejected(t *testing.T) {
	path := copyFixture(t, "membership-ipv6.iprdb", "meta-trail.iprdb")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Chunk page 9 holds the single 1039-byte stream; extend it by one byte.
	const chunkPage = 9
	chunkLen := format.U16(mustRead(t, file, chunkPage, 36, 2))
	if chunkLen != 1039 {
		t.Fatalf("unexpected fixture chunk length %d", chunkLen)
	}
	// chunk_len + 1, lower + 1, one trailing zero byte at the data end.
	cl := chunkLen + 1
	if _, err := file.WriteAt([]byte{byte(cl & 0xff), byte(cl >> 8)}, int64(chunkPage*format.PageSize+36)); err != nil {
		t.Fatal(err)
	}
	lower := format.U16(mustRead(t, file, chunkPage, 20, 2))
	if _, err := file.WriteAt([]byte{byte((lower + 1) & 0xff), byte((lower + 1) >> 8)}, int64(chunkPage*format.PageSize+20)); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0}, int64(chunkPage*format.PageSize+48+chunkLen)); err != nil {
		t.Fatal(err)
	}
	// Declared compressed length + 1 in both metas, CRCs repaired.
	patchMetaEach(t, path, func(_ int, page []byte) {
		comp := format.U64(page[128:136])
		format.PutU64(page[128:136], comp+1)
	})
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, err := r.ReadMetadataJSON(); err == nil {
		t.Fatal("metadata accepted with trailing byte")
	}
}

// auditMetadataChunkTailZero: bytes after each metadata chunk must be
// zero (binary-format-v4.md:1051). Rust rejects the page as corrupt on the
// read path (metadata.rs:274) and as PageReservedNonzero on validation;
// Go must reject it too instead of accepting the nonzero tail.
func TestMetadataChunkTailNonzeroRejected(t *testing.T) {
	path := copyFixture(t, "membership-ipv6.iprdb", "meta-tailnz.iprdb")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Chunk page 9 holds the single 1039-byte stream; make the first
	// post-data byte nonzero without touching any declared length.
	const chunkPage = 9
	chunkLen := format.U16(mustRead(t, file, chunkPage, 36, 2))
	if chunkLen != 1039 {
		t.Fatalf("unexpected fixture chunk length %d", chunkLen)
	}
	if _, err := file.WriteAt([]byte{0xaa}, int64(chunkPage*format.PageSize+48+int(chunkLen))); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, err := r.ReadMetadataJSON(); err == nil || !isFormatError(err, format.CodeFormatInvalid) {
		t.Fatalf("metadata accepted with nonzero chunk tail: %v", err)
	}
}

// auditStructuredPayloadDecodedAtLookup: the payload is decoded at lookup
// time, not at Value() time (Rust parity: structured_value/view.rs decodes
// during the lookup), and normal lookup performs no implicit semantic
// validation (binary-format-v4.md: normal operations never invoke Validate):
// flags-bit corruption is plausible corruption that decodes as-is, while
// record-geometry corruption is still rejected at lookup as a memory-safety
// violation.
func TestStructuredPlausibleCorruptionAcceptedAtLookup(t *testing.T) {
	// Baseline value from the pristine fixture for the same range.
	base, err := OpenImmutable(fixture(t, "structured-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	baseView, found, err := base.LookupNetworkEnrichmentV14(0x0a010000) // 10.1.0.0
	if err != nil || !found {
		t.Fatalf("baseline lookup: found=%v err=%v", found, err)
	}
	want, err := baseView.Value()
	if err != nil {
		t.Fatal(err)
	}
	base.Close()

	path := copyFixture(t, "structured-ipv4.iprdb", "struct-bad.iprdb")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Find the structure-ID record page (type 19) and set an unknown flag
	// bit 0x10 in slot 0's payload (record base 32, payload at +48, flags
	// +28). The location bit (0x1) is preserved.
	target := int64(-1)
	for p := 2; p < int(fixtureSize(t, path))/format.PageSize; p++ {
		page := mustRead(t, file, p, 0, format.PageSize)
		if string(page[0:4]) != string(format.PageMagic[:]) || page[4] != byte(format.PageTypeStructureIDRecord) {
			continue
		}
		if format.U32(page[24:28]) != 1 { // kind NetworkEnrichmentV1
			continue
		}
		target = int64(p*format.PageSize + 32 + 1*format.StructureRecordSize + 48 + 28) // slot = id % StructureRecordSlots
		break
	}
	if target < 0 {
		t.Fatal("structure record page not found")
	}
	flags := mustRead(t, file, int(target/format.PageSize), int(target%format.PageSize), 1)
	if _, err := file.WriteAt([]byte{flags[0] | 0x10}, target); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	view, found, err := r.LookupNetworkEnrichmentV14(0x0a010000) // 10.1.0.0 → structure id 1
	if err != nil || !found {
		t.Fatalf("plausible corruption rejected at lookup: found=%v err=%v", found, err)
	}
	val, err := view.Value()
	if err != nil {
		t.Fatal(err)
	}
	// Unknown flag bits are ignored (Rust decode_mapped parity); the
	// location bit is unchanged, and the scalar fields decode as-is.
	if val.ASN != want.ASN || val.CountryID != want.CountryID ||
		val.Flags&format.NetworkEnrichmentV1HasLocation == 0 {
		t.Fatalf("decoded %+v want asn=%d country=%d flags-location-set", val, want.ASN, want.CountryID)
	}
}

// auditStructuredRecordGeometryRejectedAtLookup: a corrupt record length
// field is a memory-safety violation and is rejected when the payload is
// decoded at lookup time.
func TestStructuredRecordGeometryRejectedAtLookup(t *testing.T) {
	path := copyFixture(t, "structured-ipv4.iprdb", "struct-bad-geom.iprdb")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	target := int64(-1)
	for p := 2; p < int(fixtureSize(t, path))/format.PageSize; p++ {
		page := mustRead(t, file, p, 0, format.PageSize)
		if string(page[0:4]) != string(format.PageMagic[:]) || page[4] != byte(format.PageTypeStructureIDRecord) {
			continue
		}
		if format.U32(page[24:28]) != 1 { // kind NetworkEnrichmentV1
			continue
		}
		target = int64(p*format.PageSize + 32 + 1*format.StructureRecordSize) // RecordLen field
		break
	}
	if target < 0 {
		t.Fatal("structure record page not found")
	}
	if _, err := file.WriteAt([]byte{0, 0}, target); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, found, err := r.LookupNetworkEnrichmentV14(0x0a010000) // 10.1.0.0 → structure id 1
	if err == nil || found {
		t.Fatalf("record-geometry corruption accepted: found=%v err=%v", found, err)
	}
}

// auditOversizedBasename: the canonical sidecar (main + ".readers") must fit
// the filesystem component limit (binary-format-v4.md:114).
func TestOversizedBasenameRejected(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("c", 248) + ".iprdb" // 253 chars; sidecar would be 261
	dst := filepath.Join(dir, name)
	data, err := os.ReadFile(fixture(t, "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Skipf("filesystem rejects the test name itself: %v", err)
	}
	if _, err := OpenImmutable(dst); err == nil {
		t.Fatal("open accepted basename whose sidecar exceeds the component limit")
	}
	if _, err := OpenImmutable(dst); err != nil && !strings.Contains(err.Error(), "component limit") {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

// auditNameBranchBelowFirstKey: a target lexicographically below the first
// branch key is absent, never corruption.
func TestNameBranchBelowFirstKeyAbsent(t *testing.T) {
	page := make([]byte, format.PageSize)
	copy(page[0:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeCatalogNameBranch)
	format.PutU16(page[6:8], 32) // header_size
	format.PutU64(page[8:16], 2) // born txn
	format.PutU16(page[16:18], 2)
	format.PutU16(page[18:20], 1)            // branch level
	format.PutU16(page[20:22], uint16(32+4)) // lower: after the slot array
	format.PutU16(page[22:24], 4056)         // upper: first entry (2 records fit)
	// Entries: "feed-000" → 5, "zeta" → 6.
	var names = []struct {
		name  string
		child uint32
	}{{"feed-000", 5}, {"zeta", 6}}
	off := 4056 // records are packed contiguously from upper downward
	for i, e := range names {
		format.PutU16(page[off:off+2], uint16(12+len(e.name)))
		format.PutU32(page[off+4:off+8], e.child)
		page[off+8] = byte(len(e.name))
		copy(page[off+12:off+12+len(e.name)], e.name)
		format.PutU16(page[32+i*2:34+i*2], uint16(off))
		off += 12 + len(e.name)
	}
	sl, err := format.OpenSlotted(page, 2, format.PageTypeCatalogNameBranch, 0, format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	child, err := nameBranchChild(sl, "aaa", format.MaxPageCount)
	if err != nil || child != 0 {
		t.Fatalf("below-first-key: child=%d err=%v", child, err)
	}
	child, err = nameBranchChild(sl, "feed-000", format.MaxPageCount)
	if err != nil || child != 5 {
		t.Fatalf("first key: child=%d err=%v", child, err)
	}
	child, err = nameBranchChild(sl, "zzz", format.MaxPageCount)
	if err != nil || child != 6 {
		t.Fatalf("past last key: child=%d err=%v", child, err)
	}
}
