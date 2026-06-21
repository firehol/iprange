package iprangeformat

import (
	"crypto/sha256"
	"slices"
	"unicode/utf8"
)

// FeedMeta holds the six feed-meta fields (§7), in order. Strings must be valid
// UTF-8 (Go strings are not guaranteed UTF-8; the writer does not re-validate, so
// pass well-formed UTF-8 — the reader rejects invalid UTF-8 on read).
type FeedMeta struct {
	Name          string
	Category      string
	Maintainer    string
	MaintainerURL string
	SourceURL     string
	License       string
}

func (m FeedMeta) validate() error {
	for _, f := range []string{m.Name, m.Category, m.Maintainer, m.MaintainerURL, m.SourceURL, m.License} {
		if !utf8.ValidString(f) {
			return errInvalidInput("feed-meta field is not valid UTF-8")
		}
		// each field length is a uint32 on disk — reject rather than truncate.
		if uint64(len(f)) > uint64(^uint32(0)) {
			return errInvalidInput("feed-meta field length exceeds u32")
		}
	}
	return nil
}

func (m FeedMeta) encode() []byte {
	fields := []string{m.Name, m.Category, m.Maintainer, m.MaintainerURL, m.SourceURL, m.License}
	out := make([]byte, 0, 4+len(fields)*4)
	out = le.AppendUint32(out, feedMetaFieldCount)
	for _, f := range fields {
		out = le.AppendUint32(out, uint32(len(f)))
		out = append(out, f...)
	}
	return out
}

// Value is an opaque per-range value (§10). type_id 0 is invalid; type_id 1 is a
// membership set (non-empty, %4==0, strictly-ascending LE uint32 feed-ids).
type Value struct {
	TypeID uint32
	Bytes  []byte
}

func (v *Value) validate() error {
	if v.TypeID == 0 {
		return errInvalidInput("value type_id 0 is reserved/invalid")
	}
	// byte_length is a uint32 on disk — reject rather than silently truncate.
	if uint64(len(v.Bytes)) > uint64(^uint32(0)) {
		return errInvalidInput("value bytes length exceeds u32")
	}
	if v.TypeID == 1 {
		if len(v.Bytes) == 0 || len(v.Bytes)%4 != 0 {
			return errInvalidInput("type_id 1 membership set must be a non-empty multiple of 4 bytes")
		}
		var prev uint32
		first := true
		for i := 0; i < len(v.Bytes); i += 4 {
			id := le.Uint32(v.Bytes[i:])
			if !first && id <= prev {
				return errInvalidInput("type_id 1 feed-ids must be strictly ascending")
			}
			prev, first = id, false
		}
	}
	return nil
}

type rangeInput[K ipKey[K]] struct {
	start, end K
	value      *Value // nil = sentinel
}

// Writer builds a v3 file for key width K. Use NewWriterV4 / NewWriterV6.
type Writer[K ipKey[K]] struct {
	meta       FeedMeta
	license    uint32
	generation uint64
	ranges     []rangeInput[K]
}

// NewWriterV4 starts an IPv4 writer.
func NewWriterV4(meta FeedMeta, licenseFlags uint32, generationUnix uint64) *Writer[Ipv4Key] {
	return &Writer[Ipv4Key]{meta: meta, license: licenseFlags, generation: generationUnix}
}

// NewWriterV6 starts an IPv6 writer.
func NewWriterV6(meta FeedMeta, licenseFlags uint32, generationUnix uint64) *Writer[Ipv6Key] {
	return &Writer[Ipv6Key]{meta: meta, license: licenseFlags, generation: generationUnix}
}

// AddRange adds an inclusive range [start, end] with an optional value (nil = none).
func (w *Writer[K]) AddRange(start, end K, value *Value) error {
	if start.cmp(end) > 0 {
		return errInvalidInput("range start > end")
	}
	if value != nil {
		if err := value.validate(); err != nil {
			return err
		}
	}
	w.ranges = append(w.ranges, rangeInput[K]{start: start, end: end, value: value})
	return nil
}

func sameValue(a, b *Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.TypeID == b.TypeID && slices.Equal(a.Bytes, b.Bytes)
}

func valueKey(v *Value) string {
	var k [4]byte
	le.PutUint32(k[:], v.TypeID)
	return string(k[:]) + string(v.Bytes)
}

// Build produces the complete v3 file bytes, or an error if not encodable.
func (w *Writer[K]) Build() ([]byte, error) {
	var zero K

	// Reject inputs the reader would refuse, so a Go-built file is never one Rust
	// would refuse to build: feed-meta MUST be valid UTF-8 (§7), and license_flags
	// MUST NOT set reserved bits (§7). (Rust's String guarantees the former.)
	if err := w.meta.validate(); err != nil {
		return nil, err
	}
	if w.license&^licenseFlagDontRedist != 0 {
		return nil, errInvalidInput("license_flags sets reserved bits")
	}

	// (1) sort by start (starts are unique once disjointness is enforced below).
	rs := slices.Clone(w.ranges)
	slices.SortFunc(rs, func(a, b rangeInput[K]) int { return a.start.cmp(b.start) })

	// validate disjointness + coalesce same-content contiguous neighbours (§9).
	coalesced := make([]rangeInput[K], 0, len(rs))
	for _, r := range rs {
		if n := len(coalesced); n > 0 {
			last := &coalesced[n-1]
			if r.start.cmp(last.end) <= 0 {
				return nil, errInvalidInput("overlapping input ranges (not disjoint)")
			}
			if inc, ok := last.end.checkedInc(); ok && sameValue(last.value, r.value) && inc.cmp(r.start) == 0 {
				last.end = r.end
				continue
			}
		}
		coalesced = append(coalesced, r)
	}
	entryCount := uint64(len(coalesced))

	// (3) assign value_ids by sweeping coalesced records; sentinels skipped.
	dedup := map[string]uint32{}
	var valuesOrder []*Value
	records := make([]record[K], 0, len(coalesced))
	var uniqueLo, uniqueHi uint64
	for i := range coalesced {
		r := &coalesced[i]
		szLo, szHi, ok := r.start.rangeSize(r.end)
		if !ok {
			return nil, errInvalidInput("range covers the entire IPv6 space")
		}
		var of bool
		uniqueLo, uniqueHi, of = add128(uniqueLo, uniqueHi, szLo, szHi)
		if of {
			return nil, errOverflow("unique_ip_count sum exceeds 2^128-1")
		}
		valueID := valueIDNone
		if r.value != nil {
			key := valueKey(r.value)
			if id, seen := dedup[key]; seen {
				valueID = id
			} else {
				// reject before the uint32 cast can wrap (>= sentinel is too many).
				if uint64(len(valuesOrder)) >= uint64(valueIDNone) {
					return nil, errInvalidInput("more than 2^32-1 distinct values")
				}
				id := uint32(len(valuesOrder))
				dedup[key] = id
				valuesOrder = append(valuesOrder, r.value)
				valueID = id
			}
		}
		records = append(records, record[K]{start: r.start, end: r.end, valueID: valueID})
	}

	// encode sections.
	feedMetaBytes := w.meta.encode()
	indexBytes := encodeIndex(records)
	var valuesBytes []byte
	if len(valuesOrder) > 0 {
		valuesBytes = encodeValues(valuesOrder)
	}

	type sect struct {
		kind  uint32
		bytes []byte
	}
	sections := []sect{
		{kindFeedMeta, feedMetaBytes},
		{kindIndex, indexBytes},
	}
	if valuesBytes != nil {
		sections = append(sections, sect{kindValues, valuesBytes})
	}
	sections = append(sections, sect{kindSignature, nil}) // empty, last

	directoryCount := uint32(len(sections))
	dirBytesLen := uint64(directoryCount) * dirEntrySize
	cursor, ok := add64(headerSize, dirBytesLen)
	if !ok {
		return nil, errOverflow("directory end")
	}

	entries := make([]dirEntry, 0, len(sections))
	type placed struct {
		offset uint64
		bytes  []byte
	}
	places := make([]placed, 0, len(sections))
	for _, s := range sections {
		align, _ := kindAlign(s.kind)
		offset, ok := alignUp(cursor, align)
		if !ok {
			return nil, errOverflow("section offset")
		}
		length := uint64(len(s.bytes))
		entries = append(entries, dirEntry{
			kind:   s.kind,
			flags:  kindFlags(s.kind),
			offset: offset,
			length: length,
			align:  align,
			hash:   sha256.Sum256(s.bytes),
		})
		places = append(places, placed{offset: offset, bytes: s.bytes})
		cursor, ok = add64(offset, length)
		if !ok {
			return nil, errOverflow("section end")
		}
	}
	fileSize := cursor

	h := &header{
		versionMinor:    versionMinor,
		headerSize:      headerSize,
		flags:           ipVersionFlag(zero),
		fileSize:        fileSize,
		directoryOffset: headerSize,
		directoryCount:  directoryCount,
		licenseFlags:    w.license,
		entryCount:      entryCount,
		generationUnix:  w.generation,
		uniqueIPCountLo: uniqueLo,
		uniqueIPCountHi: uniqueHi,
	}

	// assemble: header || directory || (zero-padding + section)*.
	out := make([]byte, 0, fileSize)
	out = append(out, h.encode()...)
	for i := range entries {
		out = append(out, entries[i].encode()...)
	}
	for _, p := range places {
		for uint64(len(out)) < p.offset {
			out = append(out, 0)
		}
		out = append(out, p.bytes...)
	}
	return out, nil
}

func encodeIndex[K ipKey[K]](records []record[K]) []byte {
	var zero K
	rs := zero.recordSize()
	sub := indexSubHeader{recordSize: uint32(rs), keyWidth: uint32(zero.width()), recordCount: uint64(len(records))}
	out := make([]byte, 0, indexSubHeaderLen+len(records)*rs)
	out = append(out, sub.encode()...)
	buf := make([]byte, rs)
	for _, r := range records {
		for i := range buf {
			buf[i] = 0
		}
		r.encodeInto(buf)
		out = append(out, buf...)
	}
	return out
}

func encodeValues(values []*Value) []byte {
	out := make([]byte, 0, 4)
	out = le.AppendUint32(out, uint32(len(values)))
	for _, v := range values {
		out = le.AppendUint32(out, v.TypeID)
		out = le.AppendUint32(out, uint32(len(v.Bytes)))
		out = append(out, v.Bytes...)
	}
	return out
}

func add64(a, b uint64) (uint64, bool) {
	s := a + b
	if s < a {
		return 0, false
	}
	return s, true
}

// ipVersionFlag returns the header flags bit for the key type of zero.
func ipVersionFlag[K ipKey[K]](zero K) uint16 {
	if zero.width() == 16 {
		return flagIPVersion
	}
	return 0
}
