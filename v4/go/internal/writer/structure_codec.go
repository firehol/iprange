// Structure dictionary write codecs (Rust structured_value/codec.rs and
// network_enrichment_v1.rs): the fixed 80-byte structure-ID records, the
// 36-byte hash-tree keys, and the typed network_enrichment_v1 payload
// semantics. Record pages are radix-addressed by structure ID
// (structure_table.go); the hash tree reuses the shared fixed B+tree with
// a structure hash codec exactly like the membership hash tree.

package writer

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// Structure record and hash-key byte layout (codec.rs offsets).
const (
	structureLengthOffset     = 0
	structureReservedOffset   = 2
	structureIDOffset         = 4
	structureRefcountOffset   = 8
	structureDigestOffset     = 16
	structurePayloadOffset    = 48
	structureHashDigestOffset = 0
	structureHashIDOffset     = 32

	structureMaxPayloadSize = 32
	structureHashKeySize    = 36
	structureHashBranchSize = 40
)

// structurePayload is one exact payload of the compile-time structure
// registry (Rust codec::Payload): the fixed 32-byte backing with the
// canonical length.
type structurePayload struct {
	bytes  [structureMaxPayloadSize]byte
	length uint16
}

// Slice returns the canonical payload bytes.
func (p structurePayload) Slice() []byte { return p.bytes[:p.length] }

// newStructurePayload copies one payload into the fixed backing (Rust
// Payload::new).
func newStructurePayload(source []byte) (structurePayload, error) {
	if len(source) > structureMaxPayloadSize {
		return structurePayload{}, invalid("structure payload exceeds the hardcoded registry limit")
	}
	var payload structurePayload
	payload.length = uint16(len(source))
	copy(payload.bytes[:], source)
	return payload, nil
}

// structurePayloadCodec is one compile-time structure registry entry (Rust
// PayloadCodec): the structure kind, the fixed payload size, and the typed
// semantics of the payload fields.
type structurePayloadCodec interface {
	kind() uint8
	payloadSize() int
	validate(payload []byte) error
	membershipID(payload *structurePayload) uint32
	isAbsent(payload *structurePayload) bool
}

// Network enrichment payload field offsets and limits
// (network_enrichment_v1.rs).
const (
	enrichmentASNOffset     = 0
	enrichmentCountryOffset = 4
	enrichmentStateOffset   = 8
	enrichmentCityOffset    = 12
	enrichmentLatOffset     = 16
	enrichmentLonOffset     = 20
	enrichmentMembershipOff = 24
	enrichmentFlagsOffset   = 28

	enrichmentLatitudeLimit  = 90_000_000
	enrichmentLongitudeLimit = 180_000_000
)

// structureNetworkEnrichmentV1 is the network_enrichment_v1 registry entry
// (Rust network_enrichment_v1.rs Codec + PayloadCodec).
type structureNetworkEnrichmentV1 struct{}

func (structureNetworkEnrichmentV1) kind() uint8 {
	return format.StructureKindNetworkEnrichmentV1
}

func (structureNetworkEnrichmentV1) payloadSize() int {
	return format.NetworkEnrichmentV1PayloadSize
}

// validate mirrors the Rust PayloadCodec::validate on one canonical
// 32-byte payload: unknown flag bits, an absent location with nonzero
// coordinates, and out-of-range present coordinates are all corruption.
func (structureNetworkEnrichmentV1) validate(payload []byte) error {
	if len(payload) != format.NetworkEnrichmentV1PayloadSize {
		return corrupt("network enrichment payload length is invalid")
	}
	flags := format.U32(payload[enrichmentFlagsOffset : enrichmentFlagsOffset+4])
	if flags & ^format.NetworkEnrichmentV1HasLocation != 0 {
		return corrupt("network enrichment payload flags are invalid")
	}
	latitude := int32(format.U32(payload[enrichmentLatOffset : enrichmentLatOffset+4]))
	longitude := int32(format.U32(payload[enrichmentLonOffset : enrichmentLonOffset+4]))
	if flags == 0 {
		if latitude != 0 || longitude != 0 {
			return corrupt("absent network location has nonzero coordinates")
		}
	} else if latitude > enrichmentLatitudeLimit || latitude < -enrichmentLatitudeLimit ||
		longitude > enrichmentLongitudeLimit || longitude < -enrichmentLongitudeLimit {
		return corrupt("network enrichment coordinates are outside their limits")
	}
	return nil
}

func (structureNetworkEnrichmentV1) membershipID(payload *structurePayload) uint32 {
	return format.U32(payload.bytes[enrichmentMembershipOff : enrichmentMembershipOff+4])
}

func (structureNetworkEnrichmentV1) isAbsent(payload *structurePayload) bool {
	return payload.length == 0 || allZero(payload.bytes[:payload.length])
}

// encodeNetworkEnrichmentV1 writes the canonical 32-byte payload (Rust
// Codec::encode): an absent location writes zero coordinates and zero
// flags, a present location writes the HAS_LOCATION flag bit and clears
// every other flag bit.
func encodeNetworkEnrichmentV1(value format.NetworkEnrichmentV1, membershipID uint32) (structurePayload, error) {
	location := value.Flags&format.NetworkEnrichmentV1HasLocation != 0
	if location {
		if value.LatitudeMicrodegrees > enrichmentLatitudeLimit || value.LatitudeMicrodegrees < -enrichmentLatitudeLimit ||
			value.LongitudeMicrodegrees > enrichmentLongitudeLimit || value.LongitudeMicrodegrees < -enrichmentLongitudeLimit {
			return structurePayload{}, invalid("network enrichment coordinates are outside their limits")
		}
	}
	latitude := int32(0)
	longitude := int32(0)
	if location {
		latitude = value.LatitudeMicrodegrees
		longitude = value.LongitudeMicrodegrees
	}
	var bytes [format.NetworkEnrichmentV1PayloadSize]byte
	format.EncodeNetworkEnrichmentV1(bytes[:], format.NetworkEnrichmentV1{
		ASN:                   value.ASN,
		CountryID:             value.CountryID,
		StateID:               value.StateID,
		CityID:                value.CityID,
		LatitudeMicrodegrees:  latitude,
		LongitudeMicrodegrees: longitude,
		MembershipID:          membershipID,
		Flags:                 value.Flags & format.NetworkEnrichmentV1HasLocation,
	})
	return newStructurePayload(bytes[:])
}

// withMembership returns one canonical payload with the membership
// field replaced in place (Rust Codec::with_membership): validation runs
// first so a corrupt stored payload is refused like any intern input,
// and every other byte is preserved exactly.
func (structureNetworkEnrichmentV1) withMembership(payload structurePayload, membershipID uint32) (structurePayload, error) {
	codec := structureNetworkEnrichmentV1{}
	if err := codec.validate(payload.Slice()); err != nil {
		return structurePayload{}, err
	}
	var bytes [format.NetworkEnrichmentV1PayloadSize]byte
	copy(bytes[:], payload.Slice())
	format.PutU32(bytes[enrichmentMembershipOff:enrichmentMembershipOff+4], membershipID)
	return newStructurePayload(bytes[:])
}

// structurePayloadDigest is the SHA-256 structure identity (Rust
// payload_digest): the "IPR4STRUCT" domain prefix, the structure kind, the
// little-endian payload length, and the canonical payload bytes.
func structurePayloadDigest(codec structurePayloadCodec, payload *structurePayload) ([32]byte, error) {
	if uint64(payload.length) > 0xFFFF {
		return [32]byte{}, invalid("structure payload is too large")
	}
	var digest [32]byte
	hasher := sha256.New()
	hasher.Write([]byte("IPR4STRUCT"))
	hasher.Write([]byte{codec.kind()})
	var lengthBytes [2]byte
	binary.LittleEndian.PutUint16(lengthBytes[:], payload.length)
	hasher.Write(lengthBytes[:])
	hasher.Write(payload.Slice())
	hasher.Sum(digest[:0])
	return digest, nil
}

// structureRecord is one decoded structure-ID record (Rust codec::Record).
type structureRecord struct {
	id       uint32
	refcount uint64
	digest   [32]byte
	payload  structurePayload
}

// structureEncoded is one encoded structure-ID record (Rust
// codec::encode): a caller-owned bounded 80-byte buffer that is copied
// into its mapped record slot by the table writer.
type structureEncoded struct {
	bytes  [format.StructureRecordSize]byte
	length int
}

// Slice returns the encoded record bytes.
func (e structureEncoded) Slice() []byte { return e.bytes[:e.length] }

// encodeStructureRecord builds the ID-tree record for one structure entry
// (Rust codec::encode): length, zero reserved, id, zero refcount, digest,
// payload.
func encodeStructureRecord(codec structurePayloadCodec, id uint32, digest [32]byte, payload *structurePayload) (structureEncoded, error) {
	if err := requireStructurePayload(codec, payload); err != nil {
		return structureEncoded{}, err
	}
	if id == 0 {
		return structureEncoded{}, invalid("structure ID zero is reserved")
	}
	length := structurePayloadOffset + codec.payloadSize()
	var encoded structureEncoded
	binary.LittleEndian.PutUint16(encoded.bytes[structureLengthOffset:], uint16(length))
	binary.LittleEndian.PutUint32(encoded.bytes[structureIDOffset:], id)
	copy(encoded.bytes[structureDigestOffset:structurePayloadOffset], digest[:])
	copy(encoded.bytes[structurePayloadOffset:length], payload.Slice())
	encoded.length = length
	return encoded, nil
}

// requireStructurePayload mirrors codec.rs require_payload: the payload
// must be exactly the registry size and pass the typed validation.
func requireStructurePayload(codec structurePayloadCodec, payload *structurePayload) error {
	if int(payload.length) != codec.payloadSize() || codec.payloadSize() > structureMaxPayloadSize {
		return invalid("structure payload does not match its hardcoded kind")
	}
	return codec.validate(payload.Slice())
}

// decodeStructureRecord validates one structure record cell and returns
// the canonical view (Rust codec::decode_record + payload_source): the
// cell length and stored length field must be the exact record size, the
// reserved bytes zero, the id nonzero, and the payload typed-valid.
func decodeStructureRecord(codec structurePayloadCodec, cell []byte) (structureRecord, error) {
	expected := structurePayloadOffset + codec.payloadSize()
	if len(cell) != expected ||
		binary.LittleEndian.Uint16(cell[structureLengthOffset:]) != uint16(expected) ||
		binary.LittleEndian.Uint16(cell[structureReservedOffset:]) != 0 {
		return structureRecord{}, corrupt("structure dictionary record is malformed")
	}
	id := binary.LittleEndian.Uint32(cell[structureIDOffset:])
	if id == 0 {
		return structureRecord{}, corrupt("structure dictionary contains ID zero")
	}
	if err := codec.validate(cell[structurePayloadOffset:expected]); err != nil {
		return structureRecord{}, err
	}
	var payload structurePayload
	payload.length = uint16(codec.payloadSize())
	copy(payload.bytes[:], cell[structurePayloadOffset:expected])
	var digest [32]byte
	copy(digest[:], cell[structureDigestOffset:structurePayloadOffset])
	return structureRecord{
		id:       id,
		refcount: binary.LittleEndian.Uint64(cell[structureRefcountOffset:]),
		digest:   digest,
		payload:  payload,
	}, nil
}

// structureHashRecord is one decoded hash-tree key or leaf (Rust
// codec::HashKey).
type structureHashRecord struct {
	digest [32]byte
	id     uint32
}

// structureHashKey encodes one hash-tree LEAF record (Rust
// codec::encode_hash): digest bytes followed by the little-endian id.
// These are the wire bytes of the hash record; the tree orders keys by
// the typed Rust HashKey Ord (digest bytes, then the numeric id), so the
// tree Key keeps the id big-endian (structureHashProbe) while the cells
// on disk stay exactly the Rust wire layout.
func structureHashKey(digest [32]byte, id uint32) [structureHashKeySize]byte {
	var key [structureHashKeySize]byte
	copy(key[structureHashDigestOffset:], digest[:])
	binary.LittleEndian.PutUint32(key[structureHashIDOffset:], id)
	return key
}

// structureHashProbe encodes one hash-tree search key in the numeric
// total order of the Rust HashKey Ord: the digest bytes followed by the
// big-endian id. The tree compares these probe bytes with the keys it
// decodes from wire cells (ReadKey normalizes to the same orientation),
// so (digest, 255) < (digest, 256) exactly like the Rust derived Ord.
func structureHashProbe(digest [32]byte, id uint32) [structureHashKeySize]byte {
	var key [structureHashKeySize]byte
	copy(key[structureHashDigestOffset:], digest[:])
	binary.BigEndian.PutUint32(key[structureHashIDOffset:], id)
	return key
}

// decodeStructureHash parses one hash-tree key or leaf (Rust
// codec::decode_hash).
func decodeStructureHash(cell []byte) (structureHashRecord, error) {
	if len(cell) != structureHashKeySize {
		return structureHashRecord{}, corrupt("structure hash record is malformed")
	}
	var record structureHashRecord
	copy(record.digest[:], cell[structureHashDigestOffset:])
	record.id = binary.LittleEndian.Uint32(cell[structureHashIDOffset:])
	if record.id == 0 {
		return structureHashRecord{}, corrupt("structure hash contains ID zero")
	}
	return record, nil
}

// structureHashCodec is the structure hash tree (Rust HashCodec): fully
// fixed 36-byte keys and leaves, 40-byte branch cells, page types 20/21
// with the structure kind as aux. The key is the raw record bytes, so byte
// Comparison is the Rust derived Ord.
type structureHashCodec struct {
	kind uint8
}

func (structureHashCodec) BranchType() format.PageType { return format.PageTypeStructureHashBranch }
func (structureHashCodec) LeafType() format.PageType   { return format.PageTypeStructureHashLeaf }
func (c structureHashCodec) Aux() uint32               { return uint32(c.kind) }
func (structureHashCodec) KeySize() int                { return structureHashKeySize }
func (structureHashCodec) LeafSize() int               { return structureHashKeySize }

func (c structureHashCodec) ReadKey(cell []byte, level uint16) (tree.Key, error) {
	if level != 0 {
		// Branch cells are keySize+4 bytes; the key is the first
		// keySize bytes (Rust read_key for branch cells).
		if len(cell) < structureHashKeySize {
			return tree.Key{}, corrupt("structure hash branch record is malformed")
		}
		cell = cell[:structureHashKeySize]
	}
	record, err := decodeStructureHash(cell)
	if err != nil {
		return tree.Key{}, err
	}
	probe := structureHashProbe(record.digest, record.id)
	return tree.RawKey(probe[:]), nil
}

func (c structureHashCodec) ReadLeaf(cell []byte) (structureHashRecord, error) {
	record, err := decodeStructureHash(cell)
	if err != nil {
		return structureHashRecord{}, err
	}
	return record, nil
}

func (structureHashCodec) WriteKey(key tree.Key, output []byte) {
	// Branch cells carry the wire layout (digest + little-endian id);
	// the tree Key is the numeric orientation, so the id bytes reverse.
	// The probe bytes are the key's raw inline field, never a slice of
	// a local.
	raw := key.Raw
	copy(output[structureHashDigestOffset:structureHashIDOffset], raw[:structureHashIDOffset])
	output[32] = raw[35]
	output[33] = raw[34]
	output[34] = raw[33]
	output[35] = raw[32]
}
