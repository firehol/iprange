package iprangeformat

import (
	"crypto/sha256"
	"unicode/utf8"
)

// ipVersion is the IP family of a file.
type ipVersion int

const (
	ipV4 ipVersion = iota
	ipV6
)

func (v ipVersion) keyWidth() uint32 {
	if v == ipV6 {
		return 16
	}
	return 4
}

// Hit is the result of a successful lookup.
type Hit struct {
	// ValueID is the matched record's value_id (valueIDNone = present, no value).
	ValueID uint32
}

// ValueRef is a borrowed value-table entry.
type ValueRef struct {
	TypeID uint32
	Bytes  []byte
}

// FeedMetaView holds the six UTF-8-validated feed-meta fields (§7).
type FeedMetaView struct {
	Name          string
	Category      string
	Maintainer    string
	MaintainerURL string
	SourceURL     string
	License       string
}

// Reader is a validated, read-only view over a v3 file's bytes.
type Reader struct {
	bytes         []byte
	hdr           *header
	ipVer         ipVersion
	feedMetaOff   int
	feedMetaLen   int
	indexRecOff   int
	indexRecLen   int
	recordCount   uint64
	valuesOff     int
	valuesLen     int
	valuesPresent bool
	valuesCount   uint32
}

// Open fully validates an untrusted file (§15 steps 1–13) and returns a reader.
func Open(b []byte) (*Reader, error) {
	r, err := parseStructure(b)
	if err != nil {
		return nil, err
	}
	if err := r.walkRecords(); err != nil {
		return nil, err
	}
	if err := r.verifyHashes(); err != nil {
		return nil, err
	}
	return r, nil
}

// OpenMetadataOnly validates header + directory + feed-meta only (steps 1–7). The
// result must not be used for lookups.
func OpenMetadataOnly(b []byte) (*Reader, error) {
	return parseStructure(b)
}

// RecordCount returns the number of index records.
func (r *Reader) RecordCount() uint64 { return r.recordCount }

// IsIPv6 reports whether the file is IPv6.
func (r *Reader) IsIPv6() bool { return r.ipVer == ipV6 }

func parseStructure(b []byte) (*Reader, error) {
	realSize := uint64(len(b))
	if realSize < headerSize {
		return nil, errFileTooShort(headerSize, realSize)
	}
	hdr, err := decodeHeader(b)
	if err != nil {
		return nil, err
	}
	if realSize < uint64(hdr.headerSize) {
		return nil, errFileTooShort(uint64(hdr.headerSize), realSize)
	}
	if hdr.fileSize != realSize {
		return nil, errFileSizeMismatch(hdr.fileSize, realSize)
	}
	ipVer := hdr.ipVersion()
	if ipVer == ipV4 && (hdr.uniqueIPCountHi != 0 || hdr.uniqueIPCountLo > (uint64(1)<<32)) {
		return nil, errStructural("IPv4 unique_ip_count out of range")
	}
	if hdr.directoryOffset != uint64(hdr.headerSize) {
		return nil, errStructural("directory_offset != header_size")
	}
	dirCount := uint64(hdr.directoryCount)
	if dirCount < 3 {
		return nil, errStructural("directory_count < 3")
	}
	dirBytes := dirCount * dirEntrySize
	if dirCount != 0 && dirBytes/dirCount != dirEntrySize {
		return nil, errOverflow("directory size")
	}
	dirEnd, ok := add64(hdr.directoryOffset, dirBytes)
	if !ok {
		return nil, errOverflow("directory end")
	}
	if dirEnd > realSize {
		return nil, errStructural("directory extends past file")
	}

	r := &Reader{bytes: b, hdr: hdr, ipVer: ipVer}
	var feedMeta, index, signature *[2]uint64 // (offset, length)
	var values *[2]uint64
	prevEnd := dirEnd
	var prevRank uint64
	for i := uint64(0); i < dirCount; i++ {
		at := hdr.directoryOffset + i*dirEntrySize
		e, err := decodeDirEntry(b[at:])
		if err != nil {
			return nil, err
		}
		if e.kind == 0 {
			return nil, errStructural("directory entry kind 0")
		}
		if !isValidAlign(e.align) {
			return nil, errStructural("align not in the valid set")
		}
		if canonAlign, known := kindAlign(e.kind); known {
			if e.align != canonAlign {
				return nil, errStructural("align != canonical value for known kind")
			}
			if e.flags != kindFlags(e.kind) {
				return nil, errStructural("flags != canonical value for known kind")
			}
		} else if e.flags&dirFlagMustUnderstand != 0 {
			return nil, errStructural("unknown must_understand=1 section")
		}
		expected, ok := alignUp(prevEnd, e.align)
		if !ok {
			return nil, errOverflow("offset")
		}
		if e.offset != expected {
			return nil, errStructural("section offset != align_up(prev_end, align)")
		}
		for _, x := range b[prevEnd:e.offset] {
			if x != 0 {
				return nil, errNonZeroReserved("inter-region padding")
			}
		}
		end, ok := add64(e.offset, e.length)
		if !ok {
			return nil, errOverflow("section end")
		}
		if end > realSize {
			return nil, errStructural("section extends past file")
		}
		rank := canonRank(e.kind)
		if rank < prevRank {
			return nil, errStructural("sections not in canonical order")
		}
		prevRank = rank
		// Track the known sections; a non-nil slot means a duplicate of that kind.
		loc := &[2]uint64{e.offset, e.length}
		switch e.kind {
		case kindFeedMeta:
			if feedMeta != nil {
				return nil, errStructural("duplicate mandatory section")
			}
			feedMeta = loc
		case kindIndex:
			if index != nil {
				return nil, errStructural("duplicate mandatory section")
			}
			index = loc
		case kindValues:
			if values != nil {
				return nil, errStructural("duplicate mandatory section")
			}
			values = loc
		case kindSignature:
			if signature != nil {
				return nil, errStructural("duplicate mandatory section")
			}
			signature = loc
		}
		prevEnd = end
	}
	if prevEnd != hdr.fileSize {
		return nil, errStructural("trailing bytes after last section")
	}
	if feedMeta == nil {
		return nil, errStructural("missing feed-meta")
	}
	if index == nil {
		return nil, errStructural("missing index")
	}
	if signature == nil {
		return nil, errStructural("missing signature")
	}
	if hdr.versionMinor == 0 && signature[1] != 0 {
		return nil, errStructural("signature length != 0 in v3.0")
	}

	// index sub-header (§8).
	idxOff, idxLen := index[0], index[1]
	if idxLen < indexSubHeaderLen {
		return nil, errStructural("index shorter than its sub-header")
	}
	sub, err := decodeIndexSubHeader(b[idxOff:])
	if err != nil {
		return nil, err
	}
	if sub.keyWidth != ipVer.keyWidth() {
		return nil, errStructural("key_width != header ip_version")
	}
	if sub.recordCount != hdr.entryCount {
		return nil, errStructural("record_count != header.entry_count")
	}
	rsz := uint64(sub.recordSize)
	if rsz == 0 || sub.recordCount > (maxUint64-indexSubHeaderLen)/rsz {
		return nil, errOverflow("record_count * record_size")
	}
	if indexSubHeaderLen+sub.recordCount*rsz != idxLen {
		return nil, errStructural("index length != 32 + record_count*record_size")
	}
	r.indexRecOff = int(idxOff + indexSubHeaderLen)
	r.indexRecLen = int(sub.recordCount * rsz)
	r.recordCount = sub.recordCount

	// values section (§10).
	if values != nil {
		count, err := validateValues(b[values[0] : values[0]+values[1]])
		if err != nil {
			return nil, err
		}
		r.valuesPresent = true
		r.valuesOff = int(values[0])
		r.valuesLen = int(values[1])
		r.valuesCount = count
	}

	r.feedMetaOff = int(feedMeta[0])
	r.feedMetaLen = int(feedMeta[1])
	// step 7: validate feed-meta content eagerly so both Open and OpenMetadataOnly
	// are a complete structural gate (not deferred to the FeedMeta accessor).
	if _, err := r.FeedMeta(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reader) walkRecords() error {
	if r.ipVer == ipV4 {
		return walkIndex[Ipv4Key](r)
	}
	return walkIndex[Ipv6Key](r)
}

func walkIndex[K ipKey[K]](r *Reader) error {
	var zero K
	rs := zero.recordSize()
	recs := r.bytes[r.indexRecOff : r.indexRecOff+r.indexRecLen]
	var walked uint64
	var prevEnd K
	havePrev := false
	anyValue := false
	var uniqLo, uniqHi uint64 // recomputed unique-IP count (§5 SHOULD)
	for i := 0; i < len(recs); i += rs {
		rec, err := decodeRecord[K](recs[i:])
		if err != nil {
			return err
		}
		if rec.start.cmp(rec.end) > 0 {
			return errInvariant("record start > end")
		}
		if havePrev && rec.start.cmp(prevEnd) <= 0 {
			return errInvariant("index not sorted/disjoint")
		}
		if rec.valueID != valueIDNone {
			if rec.valueID >= r.valuesCount {
				return errInvariant("value_id out of range")
			}
			anyValue = true
		}
		szLo, szHi, ok := rec.start.rangeSize(rec.end)
		if !ok {
			return errInvariant("range covers the entire IPv6 space")
		}
		var of bool
		uniqLo, uniqHi, of = add128(uniqLo, uniqHi, szLo, szHi)
		if of {
			return errOverflow("unique_ip_count recompute")
		}
		prevEnd = rec.end
		havePrev = true
		walked++
	}
	if walked != r.recordCount {
		return errInvariant("walked record count != entry_count")
	}
	// recomputed unique-IP count MUST match the header (the header is not hashed).
	if uniqLo != r.hdr.uniqueIPCountLo || uniqHi != r.hdr.uniqueIPCountHi {
		return errInvariant("unique_ip_count != recomputed sum")
	}
	if anyValue && !r.valuesPresent {
		return errStructural("value_id used but no values section")
	}
	return nil
}

func (r *Reader) verifyHashes() error {
	dirOff := r.hdr.directoryOffset
	for i := uint64(0); i < uint64(r.hdr.directoryCount); i++ {
		at := dirOff + i*dirEntrySize
		e, err := decodeDirEntry(r.bytes[at:])
		if err != nil {
			return err
		}
		got := sha256.Sum256(r.bytes[e.offset : e.offset+e.length])
		if got != e.hash {
			return errIntegrity("section hash mismatch")
		}
	}
	return nil
}

// LookupV4 looks up an IPv4 address; error if the file is not IPv4.
func (r *Reader) LookupV4(key Ipv4Key) (Hit, bool, error) {
	if r.ipVer != ipV4 {
		return Hit{}, false, errStructural("LookupV4 on a non-IPv4 file")
	}
	vid, found := searchIndex[Ipv4Key](r.bytes[r.indexRecOff:], r.recordCount, key)
	return Hit{ValueID: vid}, found, nil
}

// LookupV6 looks up an IPv6 address; error if the file is not IPv6.
func (r *Reader) LookupV6(key Ipv6Key) (Hit, bool, error) {
	if r.ipVer != ipV6 {
		return Hit{}, false, errStructural("LookupV6 on a non-IPv6 file")
	}
	vid, found := searchIndex[Ipv6Key](r.bytes[r.indexRecOff:], r.recordCount, key)
	return Hit{ValueID: vid}, found, nil
}

func searchIndex[K ipKey[K]](recs []byte, count uint64, key K) (uint32, bool) {
	var zero K
	rs := zero.recordSize()
	w := zero.width()
	lo, hi := uint64(0), count
	for lo < hi {
		mid := lo + (hi-lo)/2
		at := int(mid) * rs
		start := zero.readLE(recs[at:])
		if key.cmp(start) < 0 {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if lo == 0 {
		return 0, false
	}
	at := int(lo-1) * rs
	start := zero.readLE(recs[at:])
	end := zero.readLE(recs[at+w:])
	if start.cmp(key) <= 0 && key.cmp(end) <= 0 {
		return le.Uint32(recs[at+2*w:]), true
	}
	return 0, false
}

// Value resolves a value_id to its value-table entry (nil for the sentinel or an
// out-of-range id).
func (r *Reader) Value(valueID uint32) (ValueRef, bool) {
	if valueID == valueIDNone || valueID >= r.valuesCount || !r.valuesPresent {
		return ValueRef{}, false
	}
	b := r.bytes[r.valuesOff : r.valuesOff+r.valuesLen]
	pos := 4
	for idx := uint32(0); idx < r.valuesCount; idx++ {
		typeID := le.Uint32(b[pos:])
		blen := int(le.Uint32(b[pos+4:]))
		start := pos + 8
		if idx == valueID {
			return ValueRef{TypeID: typeID, Bytes: b[start : start+blen]}, true
		}
		pos = start + blen
	}
	return ValueRef{}, false
}

// FeedMeta returns the six UTF-8-validated feed-meta fields.
func (r *Reader) FeedMeta() (FeedMetaView, error) {
	b := r.bytes[r.feedMetaOff : r.feedMetaOff+r.feedMetaLen]
	if len(b) < 4 {
		return FeedMetaView{}, errStructural("feed-meta shorter than count")
	}
	count := le.Uint32(b[0:])
	if count < feedMetaFieldCount {
		return FeedMetaView{}, errStructural("feed-meta field_count < 6")
	}
	if r.hdr.versionMinor == 0 && count != feedMetaFieldCount {
		return FeedMetaView{}, errStructural("feed-meta field_count != 6 for v3.0")
	}
	// Read all `count` declared fields, keeping the 6 this version knows and skipping
	// any a future minor version added (additive forward-compat, §7).
	pos := 4
	var fields [6]string
	for i := 0; i < int(count); i++ {
		if pos+4 > len(b) {
			return FeedMetaView{}, errStructural("feed-meta field length runs past section")
		}
		flen := int(le.Uint32(b[pos:]))
		pos += 4
		if pos+flen > len(b) {
			return FeedMetaView{}, errStructural("feed-meta field bytes run past section")
		}
		if i < 6 {
			s := b[pos : pos+flen]
			if !utf8.Valid(s) {
				return FeedMetaView{}, errInvariant("feed-meta field is not valid UTF-8")
			}
			fields[i] = string(s)
		}
		pos += flen
	}
	// exact-length: no trailing garbage after the declared fields (like index/values).
	if pos != len(b) {
		return FeedMetaView{}, errStructural("feed-meta section length not exact")
	}
	return FeedMetaView{
		Name: fields[0], Category: fields[1], Maintainer: fields[2],
		MaintainerURL: fields[3], SourceURL: fields[4], License: fields[5],
	}, nil
}

func validateValues(b []byte) (uint32, error) {
	if len(b) < 4 {
		return 0, errStructural("values section shorter than count")
	}
	count := le.Uint32(b[0:])
	if count == 0 {
		return 0, errStructural("values section present with count 0")
	}
	pos := 4
	for i := uint32(0); i < count; i++ {
		if pos+8 > len(b) {
			return 0, errStructural("values entry header past section")
		}
		typeID := le.Uint32(b[pos:])
		if typeID == 0 {
			return 0, errStructural("values entry type_id 0")
		}
		blen := int(le.Uint32(b[pos+4:]))
		pos += 8
		if pos+blen > len(b) {
			return 0, errStructural("values entry bytes past section")
		}
		if typeID == 1 {
			if err := validateMembershipSet(b[pos : pos+blen]); err != nil {
				return 0, err
			}
		}
		pos += blen
	}
	if pos != len(b) {
		return 0, errStructural("values section length not exact")
	}
	return count, nil
}

// validateMembershipSet enforces the §10 type_id==1 rule on an untrusted file:
// non-empty, a multiple of 4 bytes, strictly-ascending little-endian u32 feed-ids.
func validateMembershipSet(b []byte) error {
	if len(b) == 0 || len(b)%4 != 0 {
		return errInvariant("type_id 1 membership set malformed")
	}
	var prev uint32
	first := true
	for i := 0; i < len(b); i += 4 {
		id := le.Uint32(b[i:])
		if !first && id <= prev {
			return errInvariant("type_id 1 feed-ids not strictly ascending")
		}
		prev, first = id, false
	}
	return nil
}

// ToWriterV4 reconstructs an editable IPv4 Writer from this file (owned-mutable).
func (r *Reader) ToWriterV4() (*Writer[Ipv4Key], error) {
	if r.ipVer != ipV4 {
		return nil, errStructural("ToWriterV4 on a non-IPv4 file")
	}
	return toWriter[Ipv4Key](r, NewWriterV4)
}

// ToWriterV6 reconstructs an editable IPv6 Writer from this file (owned-mutable).
func (r *Reader) ToWriterV6() (*Writer[Ipv6Key], error) {
	if r.ipVer != ipV6 {
		return nil, errStructural("ToWriterV6 on a non-IPv6 file")
	}
	return toWriter[Ipv6Key](r, NewWriterV6)
}

func toWriter[K ipKey[K]](r *Reader, mk func(FeedMeta, uint32, uint64) *Writer[K]) (*Writer[K], error) {
	fm, err := r.FeedMeta()
	if err != nil {
		return nil, err
	}
	w := mk(FeedMeta(fm), r.hdr.licenseFlags, r.hdr.generationUnix)
	var zero K
	rs := zero.recordSize()
	recs := r.bytes[r.indexRecOff : r.indexRecOff+r.indexRecLen]
	for i := 0; i < len(recs); i += rs {
		rec, err := decodeRecord[K](recs[i:])
		if err != nil {
			return nil, err
		}
		var value *Value
		if rec.valueID != valueIDNone {
			vr, ok := r.Value(rec.valueID)
			if !ok {
				return nil, errInvariant("dangling value_id")
			}
			value = &Value{TypeID: vr.TypeID, Bytes: append([]byte(nil), vr.Bytes...)}
		}
		if err := w.AddRange(rec.start, rec.end, value); err != nil {
			return nil, err
		}
	}
	return w, nil
}
