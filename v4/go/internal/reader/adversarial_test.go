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

// auditMetadataJunkBeforeTrailer: garbage between the final DEFLATE block
// and the Adler-32 trailer must be rejected (binary-format-v4.md:1016). The
// trailer itself is intact, so only the byte-exact stream-end check can
// catch this; the Adler-32 check alone cannot.
func TestMetadataJunkBeforeTrailerRejected(t *testing.T) {
	path := copyFixture(t, "membership-ipv6.iprdb", "meta-junk.iprdb")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Chunk page 9 holds the single 1039-byte stream; splice one junk byte
	// between the DEFLATE stream and its four trailer bytes.
	const chunkPage = 9
	chunkLen := format.U16(mustRead(t, file, chunkPage, 36, 2))
	if chunkLen != 1039 {
		t.Fatalf("unexpected fixture chunk length %d", chunkLen)
	}
	data := mustRead(t, file, chunkPage, 48, int(chunkLen))
	mutated := make([]byte, 0, len(data)+1)
	mutated = append(mutated, data[:len(data)-4]...)
	mutated = append(mutated, 0x00)
	mutated = append(mutated, data[len(data)-4:]...)
	if _, err := file.WriteAt(mutated, int64(chunkPage*format.PageSize+48)); err != nil {
		t.Fatal(err)
	}
	// chunk_len + 1, lower + 1.
	cl := chunkLen + 1
	if _, err := file.WriteAt([]byte{byte(cl & 0xff), byte(cl >> 8)}, int64(chunkPage*format.PageSize+36)); err != nil {
		t.Fatal(err)
	}
	lower := format.U16(mustRead(t, file, chunkPage, 20, 2))
	if _, err := file.WriteAt([]byte{byte((lower + 1) & 0xff), byte((lower + 1) >> 8)}, int64(chunkPage*format.PageSize+20)); err != nil {
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
		t.Fatal("metadata accepted with junk before the trailer")
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

// auditNameFold: the reserved-basename matches are byte-wise
// ASCII-case-insensitive, exactly like Rust eq_ignore_ascii_case
// (path.rs validate_posix_name). A non-ASCII spelling that Unicode case
// folding would map onto the reserved suffix (U+017F LATIN SMALL LETTER
// LONG S folds to "s") is accepted, so the Go reader refuses neither
// more nor fewer names than Rust.
func TestUnicodeFoldBasenameAccepted(t *testing.T) {
	dir := t.TempDir()
	name := "db.reader\u017f" // Unicode long s; strings.ToLower maps it to "s"
	dst := filepath.Join(dir, name)
	data, err := os.ReadFile(fixture(t, "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Skipf("filesystem rejects the test name itself: %v", err)
	}
	r, err := OpenImmutable(dst)
	if err != nil {
		t.Fatalf("open refused a basename Rust accepts (ASCII-only fold): %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// The ASCII spellings of the reserved names still refuse, including
	// mixed case (ASCII-only fold).
	for _, reserved := range []string{".IpRange-x", "name.READERS"} {
		dst := filepath.Join(dir, reserved)
		data, err := os.ReadFile(fixture(t, "direct-ipv4.iprdb"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Skipf("filesystem rejects the test name itself: %v", err)
		}
		if _, err := OpenImmutable(dst); err == nil {
			t.Fatalf("open accepted reserved basename %q", reserved)
		}
	}
}

// auditNulName: a NUL byte in the basename is rejected up front with the
// typed invalid-argument error, like Rust validate_posix_name, not
// reported later as an I/O failure.
func TestNulBasenameRejected(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "db\u0000.iprdb")
	_, err := OpenImmutable(dst)
	if err == nil {
		t.Fatal("open accepted a NUL basename")
	}
	if fe, ok := err.(*format.Error); !ok || fe.Code != format.CodeInvalidArgument {
		t.Fatalf("NUL basename class = %T %v, want CodeInvalidArgument", err, err)
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

// membershipIDLeafLocation walks the ID tree to the leaf page and slot of
// the record with the given membership id, mirroring lookupMembershipID so
// a test can patch the exact stored bytes of the bitmap.
func membershipIDLeafLocation(t *testing.T, r *ImmutableReader, id uint32) (uint32, int) {
	t.Helper()
	cur := r.meta.MembershipIDRoot
	level := uint16(0)
	first := true
	for {
		page, err := r.page(cur)
		if err != nil {
			t.Fatal(err)
		}
		h, err := format.DecodePageHeader(page, r.meta.TxnID)
		if err != nil {
			t.Fatal(err)
		}
		if first {
			level = h.Level
			first = false
		} else if h.Level != level {
			t.Fatalf("membership level %d expected %d", h.Level, level)
		}
		sl, err := format.OpenSlottedHeader(page, h, h.PageType, 0, format.SlotItemsPerPage)
		if err != nil {
			t.Fatal(err)
		}
		switch h.PageType {
		case format.PageTypeMembershipIDBranch:
			child, err := membershipBranchChild(sl, id, r.meta.PageCount)
			if err != nil {
				t.Fatal(err)
			}
			cur, level = child, level-1
		case format.PageTypeMembershipIDLeaf:
			slot, _, found, err := membershipLeafFind(sl, id, r.meta.MembershipIDLimit, r.meta.FeedIndexLimit, r.meta.PageCount)
			if err != nil || !found {
				t.Fatalf("membership leaf find: %v %v", found, err)
			}
			return cur, int(slot)
		default:
			t.Fatalf("unexpected membership page type %d", h.PageType)
		}
	}
}

// membershipContainsIndexTrailingZeroRejected: ContainsIndex must apply the
// same trailing-word canonical check as Word and ReadWords (spec section 9),
// so a stored zero FINAL word is corrupt even when the queried bit happens
// to be absent — mirroring Rust membership_view_tests.rs word(1) on an
// inline [1, 0] bitmap. ContainsIndex used to read the word bytes directly
// and returned false instead of surfacing the corruption.
func TestMembershipContainsIndexTrailingZeroRejected(t *testing.T) {
	path := copyFixture(t, "membership-ipv4.iprdb", "ms-trailing-zero.iprdb")
	orig := openFixture(t, "membership-ipv4.iprdb")
	view, found, err := orig.LookupMembership4(0x0a000000)
	if err != nil || !found {
		t.Fatalf("lookup: %v %v", found, err)
	}
	if view.storage != format.MembershipStorageInline {
		t.Skipf("fixture storage %d, want inline", view.storage)
	}
	leafPage, slot := membershipIDLeafLocation(t, orig, view.id)
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	page := mustRead(t, file, int(leafPage), 0, format.PageSize)
	h, err := format.DecodePageHeader(page, orig.meta.TxnID)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := format.OpenSlottedHeader(page, h, h.PageType, 0, format.SlotItemsPerPage)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := sl.Record(slot)
	if err != nil {
		t.Fatal(err)
	}
	recOff, err := sl.SlotOffset(slot)
	if err != nil {
		t.Fatal(err)
	}
	recLen := int(format.U16(rec[0:2]))
	// The inline bitmap follows the 64-byte fixed record header inside the
	// record, whose own offset is slotted at the page tail.
	inlineStart := int(recOff) + recLen - int(view.bitmapLen)
	lastWord := int(view.wordCount-1) * 8
	if _, err := file.WriteAt(make([]byte, 8), int64(int(leafPage)*format.PageSize+inlineStart+lastWord)); err != nil {
		t.Fatal(err)
	}
	file.Close()
	orig.Close()
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	v2, found, err := r.LookupMembership4(0x0a000000)
	if err != nil || !found {
		t.Fatalf("reopen lookup: %v %v", found, err)
	}
	if _, err := v2.ContainsIndex((uint32(v2.wordCount)-1)*64 + 5); err == nil || !isFormatError(err, format.CodeFormatInvalid) {
		t.Fatalf("ContainsIndex on zero final word: %v, want CodeFormatInvalid", err)
	}
	// The canonical constraint applies to the final word only: an earlier
	// word stays readable and keeps its bits.
	if _, err := v2.ContainsIndex(0); err != nil {
		t.Fatalf("ContainsIndex on earlier word: %v", err)
	}
}
