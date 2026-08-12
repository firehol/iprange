package format

// Structured-value dictionary records (binary-format-v4.md section 9A).

// NetworkEnrichmentV1 is the exact 32-byte canonical payload of structure
// kind 1. All fields are little-endian; zero ASN or label ID means absent.
type NetworkEnrichmentV1 struct {
	ASN                   uint32
	CountryID             uint32
	StateID               uint32
	CityID                uint32
	LatitudeMicrodegrees  int32
	LongitudeMicrodegrees int32
	MembershipID          uint32
	Flags                 uint32
}

const (
	NetworkEnrichmentV1PayloadSize = 32

	// NetworkEnrichmentV1HasLocation is bit 0 of Flags.
	NetworkEnrichmentV1HasLocation uint32 = 1

	// StructureRecordSlots is R for payload size 32:
	// floor((4096 - 32) / (48 + 32)) = 50.
	StructureRecordSlots = (PageSize - 32) / (48 + NetworkEnrichmentV1PayloadSize)
)

// DecodeNetworkEnrichmentV1 validates and decodes one exact 32-byte payload.
func DecodeNetworkEnrichmentV1(b []byte) (NetworkEnrichmentV1, error) {
	if len(b) < NetworkEnrichmentV1PayloadSize {
		return NetworkEnrichmentV1{}, headerErr("short structure payload %d", len(b))
	}
	v := NetworkEnrichmentV1{
		ASN:                   U32(b[0:4]),
		CountryID:             U32(b[4:8]),
		StateID:               U32(b[8:12]),
		CityID:                U32(b[12:16]),
		LatitudeMicrodegrees:  int32(U32(b[16:20])),
		LongitudeMicrodegrees: int32(U32(b[20:24])),
		MembershipID:          U32(b[24:28]),
		Flags:                 U32(b[28:32]),
	}
	if v.Flags&^NetworkEnrichmentV1HasLocation != 0 {
		return NetworkEnrichmentV1{}, headerErr("structure flags %x", v.Flags)
	}
	if v.Flags&NetworkEnrichmentV1HasLocation == 0 {
		if v.LatitudeMicrodegrees != 0 || v.LongitudeMicrodegrees != 0 {
			return NetworkEnrichmentV1{}, headerErr("location without flag")
		}
	} else {
		if v.LatitudeMicrodegrees < -90_000_000 || v.LatitudeMicrodegrees > 90_000_000 {
			return NetworkEnrichmentV1{}, headerErr("latitude %d", v.LatitudeMicrodegrees)
		}
		if v.LongitudeMicrodegrees < -180_000_000 || v.LongitudeMicrodegrees > 180_000_000 {
			return NetworkEnrichmentV1{}, headerErr("longitude %d", v.LongitudeMicrodegrees)
		}
	}
	return v, nil
}

// EncodeNetworkEnrichmentV1 writes the exact 32-byte canonical payload.
func EncodeNetworkEnrichmentV1(dst []byte, v NetworkEnrichmentV1) {
	PutU32(dst[0:4], v.ASN)
	PutU32(dst[4:8], v.CountryID)
	PutU32(dst[8:12], v.StateID)
	PutU32(dst[12:16], v.CityID)
	PutU32(dst[16:20], uint32(v.LatitudeMicrodegrees))
	PutU32(dst[20:24], uint32(v.LongitudeMicrodegrees))
	PutU32(dst[24:28], v.MembershipID)
	PutU32(dst[28:32], v.Flags)
}

// StructureRecordSize is the fixed record size for one structure-ID record.
const StructureRecordSize = 48 + NetworkEnrichmentV1PayloadSize

// StructureIDRecord is one fixed slot of a structure-ID record page. The
// Payload aliases the page view and must not outlive the operation.
type StructureIDRecord struct {
	RecordLen     uint16
	StructureID   uint32
	RangeRefcount uint64
	PayloadSHA256 [32]byte
	Payload       []byte
}

// DecodeStructureIDRecord parses one structure-ID record slot of exactly
// StructureRecordSize bytes.
func DecodeStructureIDRecord(b []byte) (StructureIDRecord, error) {
	if len(b) < StructureRecordSize {
		return StructureIDRecord{}, headerErr("short structure record %d", len(b))
	}
	if U16(b[2:4]) != 0 {
		return StructureIDRecord{}, headerErr("structure record reserved")
	}
	r := StructureIDRecord{
		RecordLen:     U16(b[0:2]),
		StructureID:   U32(b[4:8]),
		RangeRefcount: U64(b[8:16]),
	}
	if int(r.RecordLen) != StructureRecordSize {
		return StructureIDRecord{}, headerErr("structure record length %d", r.RecordLen)
	}
	if r.StructureID == 0 {
		return StructureIDRecord{}, headerErr("zero structure id")
	}
	if r.RangeRefcount == 0 {
		return StructureIDRecord{}, headerErr("zero structure refcount")
	}
	copy(r.PayloadSHA256[:], b[16:48])
	r.Payload = b[48:StructureRecordSize]
	return r, nil
}

// StructureDirectoryChildCount is the fixed number of children in one
// structure-ID directory page: 512 at bytes [32, 2080).
const StructureDirectoryChildCount = 512

// StructureLeafEnd is the fixed end of the record array of a level-0
// structure-ID page (32 + 50*80): the header lower field on record pages.
const StructureLeafEnd = 32 + StructureRecordSlots*StructureRecordSize

// StructureBranchEnd is the fixed end of the child array of a level>0
// structure-ID directory page (32 + 512*4): its header lower field.
const StructureBranchEnd = 32 + StructureDirectoryChildCount*4

// StructureSpanOfLevel returns the number of consecutive IDs covered by one
// node at the given tree level: R at level zero, R*512^(L-1) at level L>0.
func StructureSpanOfLevel(level uint32) (uint64, bool) {
	if level == 0 {
		return StructureRecordSlots, true
	}
	span := uint64(StructureRecordSlots)
	for i := uint32(1); i < level; i++ {
		if span > (1<<64-1)/StructureDirectoryChildCount {
			return 0, false
		}
		span *= StructureDirectoryChildCount
	}
	return span, true
}

// StructureRootLevel returns the smallest level whose coverage is at least
// structure_id_limit (section 9A.1).
func StructureRootLevel(structureIDLimit uint64) (uint32, bool) {
	if structureIDLimit < 1 {
		return 0, false
	}
	coverage := uint64(StructureRecordSlots)
	level := uint32(0)
	for coverage < structureIDLimit {
		if coverage > (1<<64-1)/StructureDirectoryChildCount {
			return 0, false
		}
		coverage *= StructureDirectoryChildCount
		level++
	}
	return level, true
}
