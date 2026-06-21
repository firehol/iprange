package iprangeformat

import "encoding/binary"

var le = binary.LittleEndian

// header is the fixed 72-byte file header (§5). magic and version_major are implied
// constants; entry_count and unique_ip_count_* are computed (backpatched) fields.
type header struct {
	versionMinor    uint16
	headerSize      uint16
	flags           uint16
	fileSize        uint64
	directoryOffset uint64
	directoryCount  uint32
	licenseFlags    uint32
	entryCount      uint64
	generationUnix  uint64
	uniqueIPCountLo uint64
	uniqueIPCountHi uint64
}

func (h *header) encode() []byte {
	b := make([]byte, headerSize)
	copy(b[0:8], magic[:])
	le.PutUint16(b[8:], versionMajor)
	le.PutUint16(b[10:], h.versionMinor)
	le.PutUint16(b[12:], h.headerSize)
	le.PutUint16(b[14:], h.flags)
	le.PutUint64(b[16:], h.fileSize)
	le.PutUint64(b[24:], h.directoryOffset)
	le.PutUint32(b[32:], h.directoryCount)
	le.PutUint32(b[36:], h.licenseFlags)
	le.PutUint64(b[40:], h.entryCount)
	le.PutUint64(b[48:], h.generationUnix)
	le.PutUint64(b[56:], h.uniqueIPCountLo)
	le.PutUint64(b[64:], h.uniqueIPCountHi)
	return b
}

func decodeHeader(b []byte) (*header, error) {
	if len(b) < headerSize {
		return nil, errFileTooShort(headerSize, uint64(len(b)))
	}
	if [8]byte(b[0:8]) != magic {
		return nil, errBadMagic()
	}
	if vm := le.Uint16(b[8:]); vm != versionMajor {
		return nil, errUnsupportedMajor(vm)
	}
	h := &header{
		versionMinor:    le.Uint16(b[10:]),
		headerSize:      le.Uint16(b[12:]),
		flags:           le.Uint16(b[14:]),
		fileSize:        le.Uint64(b[16:]),
		directoryOffset: le.Uint64(b[24:]),
		directoryCount:  le.Uint32(b[32:]),
		licenseFlags:    le.Uint32(b[36:]),
		entryCount:      le.Uint64(b[40:]),
		generationUnix:  le.Uint64(b[48:]),
		uniqueIPCountLo: le.Uint64(b[56:]),
		uniqueIPCountHi: le.Uint64(b[64:]),
	}
	if h.headerSize < headerSize || h.headerSize%8 != 0 {
		return nil, errBadHeaderSize(h.headerSize)
	}
	if h.versionMinor == 0 && h.headerSize != headerSize {
		return nil, errBadHeaderSize(h.headerSize)
	}
	if h.flags&^flagIPVersion != 0 {
		return nil, errNonZeroReserved("header.flags bits 1-15")
	}
	if h.licenseFlags&^licenseFlagDontRedist != 0 {
		return nil, errNonZeroReserved("header.license_flags bits 1-31")
	}
	return h, nil
}

func (h *header) ipVersion() ipVersion {
	if h.flags&flagIPVersion != 0 {
		return ipV6
	}
	return ipV4
}

// dirEntry is a 72-byte directory entry (§6). hash is the full 32-byte SHA-256.
type dirEntry struct {
	kind   uint32
	flags  uint32
	offset uint64
	length uint64
	align  uint64
	hash   [32]byte
}

func (e *dirEntry) encode() []byte {
	b := make([]byte, dirEntrySize)
	le.PutUint32(b[0:], e.kind)
	le.PutUint32(b[4:], e.flags)
	le.PutUint64(b[8:], e.offset)
	le.PutUint64(b[16:], e.length)
	le.PutUint64(b[24:], e.align)
	// bytes 32..40 reserved (zero)
	copy(b[40:72], e.hash[:])
	return b
}

func decodeDirEntry(b []byte) (*dirEntry, error) {
	if len(b) < dirEntrySize {
		return nil, errFileTooShort(dirEntrySize, uint64(len(b)))
	}
	flags := le.Uint32(b[4:])
	if flags&^dirFlagMustUnderstand != 0 {
		return nil, errNonZeroReserved("dir_entry.flags bits 1-31")
	}
	if le.Uint64(b[32:]) != 0 {
		return nil, errNonZeroReserved("dir_entry.reserved")
	}
	e := &dirEntry{
		kind:   le.Uint32(b[0:]),
		flags:  flags,
		offset: le.Uint64(b[8:]),
		length: le.Uint64(b[16:]),
		align:  le.Uint64(b[24:]),
	}
	copy(e.hash[:], b[40:72])
	return e, nil
}

// indexSubHeader is the 32-byte index sub-header (§9).
type indexSubHeader struct {
	recordSize  uint32
	keyWidth    uint32
	recordCount uint64
}

func (s *indexSubHeader) encode() []byte {
	b := make([]byte, indexSubHeaderLen)
	le.PutUint32(b[0:], s.recordSize)
	le.PutUint32(b[4:], s.keyWidth)
	le.PutUint64(b[8:], s.recordCount)
	// bytes 16..32 reserved (zero)
	return b
}

func decodeIndexSubHeader(b []byte) (*indexSubHeader, error) {
	if len(b) < indexSubHeaderLen {
		return nil, errFileTooShort(indexSubHeaderLen, uint64(len(b)))
	}
	for _, x := range b[16:32] {
		if x != 0 {
			return nil, errNonZeroReserved("index_subheader.reserved")
		}
	}
	s := &indexSubHeader{
		recordSize:  le.Uint32(b[0:]),
		keyWidth:    le.Uint32(b[4:]),
		recordCount: le.Uint64(b[8:]),
	}
	switch {
	case s.keyWidth == 4 && s.recordSize == 12:
	case s.keyWidth == 16 && s.recordSize == 40:
	default:
		return nil, errStructural("index sub-header record_size/key_width mismatch")
	}
	return s, nil
}

// record is one interval-map record, generic over the key width.
type record[K ipKey[K]] struct {
	start   K
	end     K
	valueID uint32
}

// encodeInto writes the record into the first recordSize() bytes of out (which the
// caller has pre-zeroed, so the v6 pad is zero).
func (r record[K]) encodeInto(out []byte) {
	w := r.start.width()
	r.start.writeLE(out[0:w])
	r.end.writeLE(out[w : 2*w])
	le.PutUint32(out[2*w:], r.valueID)
	// out[2*w+4 : recordSize] is the pad (left zero by the caller).
}

func decodeRecord[K ipKey[K]](src []byte) (record[K], error) {
	var zero K
	w := zero.width()
	rs := zero.recordSize()
	start := zero.readLE(src[0:w])
	end := zero.readLE(src[w : 2*w])
	valueID := le.Uint32(src[2*w:])
	for _, x := range src[2*w+4 : rs] {
		if x != 0 {
			return record[K]{}, errNonZeroReserved("v6 record pad")
		}
	}
	return record[K]{start: start, end: end, valueID: valueID}, nil
}
