package format

import "crypto/sha256"

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

// DecodeNetworkEnrichmentV1 decodes one exact 32-byte payload with the same
// semantics as the Rust hot path (structured_value/network_enrichment_v1.rs
// decode_mapped): fields are read as-is and no semantic validation runs.
// Normal open, lookup, and scan never invoke Validate (binary-format-v4.md
// normal-operation rule), so plausible corruption can produce an incorrect
// value when the caller skips validation; the length check below is the
// mandatory memory-safety bound, and flag bits other than the location bit
// are ignored exactly like the Rust decoder ignores them.
func DecodeNetworkEnrichmentV1(b []byte) (NetworkEnrichmentV1, error) {
	if len(b) < NetworkEnrichmentV1PayloadSize {
		return NetworkEnrichmentV1{}, headerErr("short structure payload %d", len(b))
	}
	return NetworkEnrichmentV1{
		ASN:                   U32(b[0:4]),
		CountryID:             U32(b[4:8]),
		StateID:               U32(b[8:12]),
		CityID:                U32(b[12:16]),
		LatitudeMicrodegrees:  int32(U32(b[16:20])),
		LongitudeMicrodegrees: int32(U32(b[20:24])),
		MembershipID:          U32(b[24:28]),
		Flags:                 U32(b[28:32]),
	}, nil
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

// StructureHashKey is one reverse-index hash key (Rust HashKey: digest
// then id; ordered lexicographically by those bytes).
type StructureHashKey struct {
	Digest [32]byte
	ID     uint32
}

const (
	// StructureHashKeySize is the fixed hash leaf record size (Rust
	// HASH_KEY_SIZE = 36).
	StructureHashKeySize = 36
	// StructureHashBranchSize is the fixed hash branch entry size
	// (Rust HASH_BRANCH_SIZE = HASH_KEY_SIZE + 4).
	StructureHashBranchSize = StructureHashKeySize + 4
)

// DecodeStructureHashKey parses one fixed 36-byte reverse-index key
// (Rust codec::decode_hash; the id is nonzero and the digest is the
// first 32 bytes).
func DecodeStructureHashKey(b []byte) (StructureHashKey, error) {
	if len(b) != StructureHashKeySize {
		return StructureHashKey{}, headerErr("structure hash record size %d", len(b))
	}
	key := StructureHashKey{
		Digest: [32]byte(b[0:32]),
		ID:     U32(b[32:36]),
	}
	if key.ID == 0 {
		return StructureHashKey{}, headerErr("structure hash id is zero")
	}
	return key, nil
}

// DecodeStructureHashBranchFields parses one fixed 40-byte hash branch
// entry without the child-page-number check (Rust codec::
// decode_hash_branch: the key decodes in place and the child is raw).
func DecodeStructureHashBranchFields(b []byte) (StructureHashKey, uint32, error) {
	if len(b) != StructureHashBranchSize {
		return StructureHashKey{}, 0, headerErr("structure hash branch size %d", len(b))
	}
	key, err := DecodeStructureHashKey(b[0:StructureHashKeySize])
	if err != nil {
		return StructureHashKey{}, 0, err
	}
	return key, U32(b[StructureHashKeySize:StructureHashBranchSize]), nil
}

// StructureRecord is one parsed structure-ID record (Rust codec::Record):
// the fields of one fixed 80-byte dictionary slot. Payload aliases the
// slot view and must not outlive the operation.
type StructureRecord struct {
	ID       uint32
	Refcount uint64
	Digest   [32]byte
	Payload  []byte
}

// DecodeStructureRecord parses one structure-ID record slot at the
// implied position for expectedID (Rust codec::decode_record for the
// NetworkEnrichmentV1 payload codec): the record length proves the
// 80-byte span, the reserved word is zero, the stored id is nonzero and
// equals the slot's implied id, and the payload passes the kind semantic
// validation. Refcount and digest are deliberately not checked: the
// validation walk reports them as their own finding classes.
func DecodeStructureRecord(b []byte, expectedID uint64) (StructureRecord, error) {
	if len(b) != StructureRecordSize {
		return StructureRecord{}, headerErr("structure record size %d", len(b))
	}
	if U16(b[0:2]) != StructureRecordSize || U16(b[2:4]) != 0 {
		return StructureRecord{}, headerErr("structure record envelope is malformed")
	}
	id := U32(b[4:8])
	if id == 0 {
		return StructureRecord{}, headerErr("structure record id is zero")
	}
	if uint64(id) != expectedID {
		return StructureRecord{}, headerErr("structure record id %d at slot implying %d", id, expectedID)
	}
	payload := b[48:StructureRecordSize]
	if err := ValidateNetworkEnrichmentV1Payload(payload); err != nil {
		return StructureRecord{}, err
	}
	return StructureRecord{
		ID:       id,
		Refcount: U64(b[8:16]),
		Digest:   [32]byte(b[16:48]),
		Payload:  payload,
	}, nil
}

// ValidateNetworkEnrichmentV1Payload checks one exact 32-byte payload
// with the Rust NetworkEnrichmentV1Codec semantic rules
// (network_enrichment_v1.rs validate): only the location flag bit is
// allowed, an absent location must carry zero coordinates, and a present
// location must stay within the microdegree limits.
func ValidateNetworkEnrichmentV1Payload(payload []byte) error {
	if len(payload) != NetworkEnrichmentV1PayloadSize {
		return headerErr("network enrichment payload size %d", len(payload))
	}
	flags := U32(payload[28:32])
	if flags & ^NetworkEnrichmentV1HasLocation != 0 {
		return headerErr("network enrichment payload flags are invalid")
	}
	latitude := int32(U32(payload[16:20]))
	longitude := int32(U32(payload[20:24]))
	if flags == 0 {
		if latitude != 0 || longitude != 0 {
			return headerErr("absent network location has nonzero coordinates")
		}
	} else if uint32Abs(latitude) > 90_000_000 || uint32Abs(longitude) > 180_000_000 {
		return headerErr("network enrichment coordinates are outside their limits")
	}
	return nil
}

// uint32Abs is the magnitude of one signed value without the overflow
// of negating MinInt32 (Rust i32::unsigned_abs).
func uint32Abs(v int32) uint32 {
	if v >= 0 {
		return uint32(v)
	}
	return uint32(-(v + 1)) + 1
}

// StructurePayloadDigest is the SHA-256 structure identity (Rust
// payload_digest): the "IPR4STRUCT" domain prefix, the structure kind
// byte, the little-endian payload length, and the payload bytes.
func StructurePayloadDigest(kind uint8, payload []byte) ([32]byte, error) {
	if len(payload) > 0xFFFF {
		return [32]byte{}, headerErr("structure payload is too large")
	}
	var digest [32]byte
	hasher := sha256.New()
	hasher.Write([]byte("IPR4STRUCT"))
	hasher.Write([]byte{kind})
	var lengthBytes [2]byte
	PutU16(lengthBytes[:], uint16(len(payload)))
	hasher.Write(lengthBytes[:])
	hasher.Write(payload)
	hasher.Sum(digest[:0])
	return digest, nil
}
