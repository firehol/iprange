package format

// Opaque compressed metadata chain (binary-format-v4.md section 11).

// MaxMetadataChunkLen is the fixed payload capacity of one metadata
// chunk page (Rust metadata.rs CHUNK_CAPACITY).
const MaxMetadataChunkLen = 4048

// MetadataDataOffset is the fixed chunk-payload offset inside one
// metadata-chunk page (Rust metadata.rs DATA_OFFSET).
const MetadataDataOffset = 48

// MaxMetadataChainPages is the fixed bound on one metadata chain (Rust
// metadata.rs MAX_PAGES: the compressed bound of the 20 MiB cap divided by
// the chunk capacity, rounded up).
const MaxMetadataChainPages = 5182

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

// MetadataChunkFields mirrors metadata.rs chunk_fields: the exact chunk
// body checks of the explicit validation chain walk (chunk length equal
// to the remaining stream bound, reserved word zero, the logical offset,
// the slotted geometry, the single-item zero level, and the chain link).
func MetadataChunkFields(page []byte, pageNumber uint32, pageCount uint64, expectedOffset, remaining uint64) (MetadataChunk, bool) {
	var chunk MetadataChunk
	if len(page) != PageSize {
		return chunk, false
	}
	chunk.Next = U32(page[32:36])
	chunk.ChunkLen = U16(page[36:38])
	expected := remaining
	if expected > MaxMetadataChunkLen {
		expected = MaxMetadataChunkLen
	}
	final := remaining == uint64(chunk.ChunkLen)
	chunk.LogicalOffset = U64(page[40:48])
	if uint64(chunk.ChunkLen) != expected ||
		chunk.ChunkLen == 0 ||
		U16(page[38:40]) != 0 ||
		chunk.LogicalOffset != expectedOffset ||
		U16(page[20:22]) != uint16(MetadataDataOffset+int(chunk.ChunkLen)) ||
		U16(page[22:24]) != PageSize ||
		U16(page[16:18]) != 1 ||
		U16(page[18:20]) != 0 ||
		!metadataLinkValid(pageNumber, chunk.Next, pageCount, final) {
		return chunk, false
	}
	chunk.Data = page[MetadataDataOffset : MetadataDataOffset+int(chunk.ChunkLen)]
	return chunk, true
}

// metadataLinkValid mirrors metadata.rs link_valid: the final chunk ends
// the chain; every other chunk names a distinct in-bounds page.
func metadataLinkValid(pageNumber, next uint32, pageCount uint64, final bool) bool {
	if final {
		return next == 0
	}
	return next >= 2 && next != pageNumber && uint64(next) < pageCount
}

// MetadataChunkTailZero reports whether the bytes after the chunk payload
// are all zero (metadata.rs reserved_zero).
func MetadataChunkTailZero(page []byte, length int) bool {
	return AllZero(page[MetadataDataOffset+length:])
}
