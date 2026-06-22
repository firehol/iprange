package iprangeformat

import (
	"crypto/sha256"
	"testing"
)

// craftV4FeedMeta hand-builds a structurally-consistent empty v4 file whose feed-meta
// section is exactly `fm` bytes, at the given version_minor (offsets/lengths/hashes
// all correct). Mirrors the Rust craft helper.
func craftV4FeedMeta(fm []byte, versionMinor uint16) []byte {
	idx := (&indexSubHeader{recordSize: 12, keyWidth: 4, recordCount: 0}).encode()
	dirEnd := uint64(headerSize) + 3*dirEntrySize
	fmOff, _ := alignUp(dirEnd, 8)
	fmLen := uint64(len(fm))
	idxOff, _ := alignUp(fmOff+fmLen, 16)
	idxLen := uint64(len(idx))
	sigOff, _ := alignUp(idxOff+idxLen, 8)
	hdr := &header{
		versionMinor: versionMinor, headerSize: headerSize, flags: 0, fileSize: sigOff,
		directoryOffset: headerSize, directoryCount: 3,
	}
	entries := []dirEntry{
		{kind: kindFeedMeta, flags: dirFlagMustUnderstand, offset: fmOff, length: fmLen, align: 8, hash: sha256.Sum256(fm)},
		{kind: kindIndex, flags: dirFlagMustUnderstand, offset: idxOff, length: idxLen, align: 16, hash: sha256.Sum256(idx)},
		{kind: kindSignature, flags: 0, offset: sigOff, length: 0, align: 8, hash: sha256.Sum256(nil)},
	}
	out := hdr.encode()
	for i := range entries {
		out = append(out, entries[i].encode()...)
	}
	for uint64(len(out)) < fmOff {
		out = append(out, 0)
	}
	out = append(out, fm...)
	for uint64(len(out)) < idxOff {
		out = append(out, 0)
	}
	out = append(out, idx...)
	for uint64(len(out)) < sigOff {
		out = append(out, 0)
	}
	return out
}

func TestReaderAcceptsFutureExtraFeedMetaFields(t *testing.T) {
	// A future header-extending minor (v3.2+) may declare >6 feed-meta fields; a v3.1
	// reader reads the 6 it knows and skips the extras (additive forward-compat, §7).
	// v3.0 AND v3.1 both pin field_count == 6, so the future minor is 2.
	var fm []byte
	fm = le.AppendUint32(fm, 8) // 8 fields
	for i := 0; i < 8; i++ {
		fm = le.AppendUint32(fm, 1) // each field: 1 byte
		fm = append(fm, byte('a'+i))
	}
	r, err := Open(craftV4FeedMeta(fm, 2)) // version_minor = 2 (future)
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.FeedMeta()
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "a" || v.License != "f" { // field 5; fields 6,7 skipped
		t.Fatalf("feed-meta = %+v", v)
	}
	// a v3.0 file (version_minor 0) with >6 fields is still rejected (pinned to 6).
	if _, err := Open(craftV4FeedMeta(fm, 0)); err == nil {
		t.Fatal("v3.0 file with >6 feed-meta fields should be rejected")
	}
	// and a v3.1 file (version_minor 1) with >6 fields is rejected too.
	if _, err := Open(craftV4FeedMeta(fm, 1)); err == nil {
		t.Fatal("v3.1 file with >6 feed-meta fields should be rejected")
	}
}

func TestReaderRejectsShortFeedMeta(t *testing.T) {
	// feed-meta length 0 (< 4) — rejected, not a panic (Go always had the guard).
	if _, err := Open(craftV4FeedMeta(nil, 0)); err == nil {
		t.Fatal("short feed-meta should be rejected")
	}
}
