package format

// Opaque compressed metadata chain (binary-format-v4.md section 11).

const MaxMetadataChunkLen = 4048

// MetadataChunk is one parsed metadata-chunk page body. Data aliases the page
// view and must not outlive the operation.
type MetadataChunk struct {
	Next          uint32
	ChunkLen      uint16
	LogicalOffset uint64
	Data          []byte
}

// DecodeMetadataChunk parses one metadata chunk page body.
func DecodeMetadataChunk(page []byte) (MetadataChunk, error) {
	if len(page) != PageSize {
		return MetadataChunk{}, headerErr("metadata chunk length %d", len(page))
	}
	next := U32(page[32:36])
	chunkLen := U16(page[36:38])
	if U16(page[38:40]) != 0 {
		return MetadataChunk{}, headerErr("metadata chunk reserved")
	}
	off := U64(page[40:48])
	if chunkLen < 1 || chunkLen > MaxMetadataChunkLen {
		return MetadataChunk{}, headerErr("metadata chunk length %d", chunkLen)
	}
	return MetadataChunk{Next: next, ChunkLen: chunkLen, LogicalOffset: off, Data: page[48 : 48+int(chunkLen)]}, nil
}

// MetadataCompressedBound returns the exact bound on the compressed length
// for a given uncompressed length (section 11). A writer can always satisfy
// it with stored blocks.
func MetadataCompressedBound(uncompressed uint64) uint64 {
	blocks := (uncompressed + 65535 - 1) / 65535
	if blocks < 1 {
		blocks = 1
	}
	return uncompressed + 5*blocks + 6
}
