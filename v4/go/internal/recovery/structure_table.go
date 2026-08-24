package recovery

// Fixed-record structure payload storage and source-ID lookup (Rust
// recovery/structure_table.rs): the 48-byte structure locator (16-byte
// header plus the up-to-32-byte payload copy) shares the recovery id
// table machinery and the source-ID probe with conflicts.

import "github.com/firehol/iprange/v4/go/internal/format"

const (
	structureRecordPresent    = 0
	structureRecordRejected   = 1
	structureRecordLength     = 2
	structureRecordID         = 4
	structureRecordMembership = 8
	structureRecordLeafPage   = 12
	structureRecordPayload    = 16
)

// structureLocator is one recovered structure record (Rust Locator):
// the payload is a fixed copy, never a view of the table buffer.
type structureLocator struct {
	id           uint32
	membershipID uint32
	leafPage     uint32
	payload      [format.NetworkEnrichmentV1PayloadSize]byte
	payloadLen   int
	rejected     bool
}

// payloadBytes returns the payload as a slice (Rust Payload::as_slice).
func (s structureLocator) payloadBytes() []byte {
	return s.payload[:s.payloadLen]
}

// structureIndex is the recovery structure table of one source (Rust
// StructureIndex): the kind guard and the shared id-index terminals.
type structureIndex struct {
	kind  uint8
	table *idIndex[structureLocator]
}

// newStructureIndex builds one structure index of the given kind (Rust
// StructureIndex::new).
func newStructureIndex(tables *tableStore, kind uint8) *structureIndex {
	return &structureIndex{kind: kind, table: newIDIndex(tables, structureCodec())}
}

// kindOf returns the structure kind of one index (Rust StructureIndex::kind).
func kindOf(s *structureIndex) uint8 { return s.kind }

// structureCodec adapts the structure locator to the shared id table
// (Rust StructureCodec).
func structureCodec() recordCodec[structureLocator] {
	return recordCodec[structureLocator]{
		width:         int(structureRecordSize),
		invalidRecord: "recovery structure record index is invalid",
		full:          "recovery structure ID table is full",
		regions: func(layout tableLayout) (tableRegion, tableRegion) {
			return layout.structureRecords, layout.structureIDs
		},
		encode:     encodeStructureLocator,
		decode:     decodeStructureLocator,
		isRejected: func(record structureLocator) bool { return record.rejected },
		reject:     func(record *structureLocator) { record.rejected = true },
	}
}

// encodeStructureLocator encodes one locator (Rust structure_table::
// encode: the present flag, the payload length, and every wire field).
func encodeStructureLocator(locator structureLocator, output []byte) {
	for index := range output {
		output[index] = 0
	}
	output[structureRecordPresent] = 1
	if locator.rejected {
		output[structureRecordRejected] = 1
	}
	format.PutU16(output[structureRecordLength:], uint16(locator.payloadLen))
	format.PutU32(output[structureRecordID:], locator.id)
	format.PutU32(output[structureRecordMembership:], locator.membershipID)
	format.PutU32(output[structureRecordLeafPage:], locator.leafPage)
	copy(output[structureRecordPayload:structureRecordPayload+locator.payloadLen], locator.payload[:locator.payloadLen])
}

// decodeStructureLocator decodes one locator (Rust structure_table::
// decode: the present flag, the length bound, and every wire field;
// every refusal is the Corrupt class).
func decodeStructureLocator(bytes []byte) (structureLocator, error) {
	var locator structureLocator
	if len(bytes) != int(structureRecordSize) {
		return locator, corruptError("recovery structure locator has wrong size")
	}
	if bytes[structureRecordPresent] != 1 {
		return locator, corruptError("recovery structure locator is malformed")
	}
	length := int(format.U16(bytes[structureRecordLength:]))
	if length > format.NetworkEnrichmentV1PayloadSize {
		return locator, corruptError("recovery structure payload is too large")
	}
	end := structureRecordPayload + length
	locator.id = format.U32(bytes[structureRecordID:])
	locator.membershipID = format.U32(bytes[structureRecordMembership:])
	locator.leafPage = format.U32(bytes[structureRecordLeafPage:])
	locator.payloadLen = length
	copy(locator.payload[:length], bytes[structureRecordPayload:end])
	locator.rejected = bytes[structureRecordRejected] != 0
	return locator, nil
}
