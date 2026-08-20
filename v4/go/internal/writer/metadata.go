// Draft metadata changes owned by the file-backed draft (Rust
// draft_store/metadata.rs + metadata.rs). SetMetadata replaces the exact
// opaque metadata payload: bounded zlib compression with a stored-zlib
// fallback, a forward chunk chain written at final offsets in the
// mapping, and retirement of the base chain pages. ClearMetadata stages
// absence. One metadata stage is allowed per transaction (Rust
// require_metadata_available / finish_metadata_stage parity).

package writer

import (
	"bytes"
	"compress/flate"
	"fmt"
	"hash/adler32"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// deflateHeapOverhead mirrors Rust metadata.rs DEFLATE_HEAP_OVERHEAD: the
// pinned miniz backend workspace that must fit inside the declared heap
// budget before a deflate attempt is made.
const deflateHeapOverhead = 512 * 1024

// SetMetadata stages one exact metadata replacement and reports whether
// the draft changed (Rust DraftStore::set_metadata). The 20 MiB cap, the
// compression bound, and the chain page bound are enforced exactly like
// the Rust authority.
func (s *DraftStore) SetMetadata(input []byte) (bool, error) {
	if err := s.requireMetadataAvailable(); err != nil {
		return false, err
	}
	compressed, err := metadataCompress(input, s.budget.MaxHeapBytes)
	if err != nil {
		return false, err
	}
	oldPages, err := metadataCollectPages(s, s.draft.base)
	if err != nil {
		return false, err
	}
	newRoot, err := metadataWriteChain(s, compressed)
	if err != nil {
		return false, err
	}
	for _, page := range oldPages {
		if err := s.retireOne(page); err != nil {
			return false, err
		}
	}
	s.draft.meta.MetadataRoot = newRoot
	s.draft.meta.MetadataUncompressed = uint64(len(input))
	s.draft.meta.MetadataCompressed = uint64(len(compressed))
	s.finishMetadataStage()
	return true, nil
}

// ClearMetadata stages metadata absence and reports whether the draft
// changed (Rust DraftStore::clear_metadata: a committed root of zero is a
// no-op).
func (s *DraftStore) ClearMetadata() (bool, error) {
	if err := s.requireMetadataAvailable(); err != nil {
		return false, err
	}
	if s.draft.meta.MetadataRoot == 0 {
		return false, nil
	}
	oldPages, err := metadataCollectPages(s, s.draft.base)
	if err != nil {
		return false, err
	}
	for _, page := range oldPages {
		if err := s.retireOne(page); err != nil {
			return false, err
		}
	}
	s.draft.meta.MetadataRoot = 0
	s.draft.meta.MetadataUncompressed = 0
	s.draft.meta.MetadataCompressed = 0
	s.finishMetadataStage()
	return true, nil
}

// requireMetadataAvailable mirrors Rust require_metadata_available: one
// metadata stage per transaction.
func (s *DraftStore) requireMetadataAvailable() error {
	if s.draft.metadataStaged {
		return &format.Error{Code: format.CodeWrongState, Detail: "this transaction already staged metadata"}
	}
	return nil
}

// finishMetadataStage mirrors Rust finish_metadata_stage.
func (s *DraftStore) finishMetadataStage() {
	s.draft.metadataStaged = true
	s.draft.changed = true
}

// metadataCompress mirrors Rust metadata.rs compress: reject inputs over
// the 20 MiB cap, refuse a compression heap over the declared budget, try
// the bounded deflate stream, and fall back to the exact stored-zlib
// encoding when deflate cannot finish inside the bound (or the deflate
// workspace does not fit the heap budget).
func metadataCompress(input []byte, maxHeapBytes uint64) ([]byte, error) {
	if uint64(len(input)) > format.MaxMetadataUncompressed {
		return nil, invalid("metadata exceeds 20 MiB")
	}
	bound := format.MetadataCompressedBound(uint64(len(input)))
	if bound > maxHeapBytes {
		return nil, budgetExceeded("metadata compression heap")
	}
	if maxHeapBytes >= bound+deflateHeapOverhead {
		if compressed, ok := tryMetadataDeflate(input, bound); ok {
			return compressed, nil
		}
	}
	return storedZlib(input), nil
}

// tryMetadataDeflate compresses input into one complete bounded zlib
// stream (Rust try_deflate): the 0x78 0x01 header, one raw DEFLATE
// stream, and the Adler-32 trailer, exactly like the Rust miniz zlib
// wrapper. The stream must end inside the bound with every input byte
// consumed. compress/flate is the permitted in-memory codec; the zlib
// framing is written by hand because compress/zlib itself is banned by
// the import graph gate.
func tryMetadataDeflate(input []byte, bound uint64) ([]byte, bool) {
	var output bytes.Buffer
	output.Grow(int(bound))
	output.Write([]byte{0x78, 0x01})
	encoder, err := flate.NewWriter(&output, flate.DefaultCompression)
	if err != nil {
		return nil, false
	}
	if _, err := encoder.Write(input); err != nil {
		return nil, false
	}
	if err := encoder.Close(); err != nil {
		return nil, false
	}
	sum := adler32.Checksum(input)
	output.Write([]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)})
	if uint64(output.Len()) > bound {
		return nil, false
	}
	return output.Bytes(), true
}

// storedZlib mirrors Rust encode_stored_zlib byte for byte: the 0x78 0x01
// header, one final stored block per 65,535-byte input chunk, and the
// Adler-32 trailer, so the fallback always fits the declared bound. The
// compression payload is the second legal bounded in-memory payload
// after the reader inflater; bytes.Buffer keeps the owned storage
// bounded by MetadataCompressedBound.
func storedZlib(input []byte) []byte {
	output := bytes.NewBuffer(make([]byte, 0, format.MetadataCompressedBound(uint64(len(input)))))
	output.Write([]byte{0x78, 0x01})
	if len(input) == 0 {
		writeStoredBlock(output, nil, true)
	} else {
		for start := 0; start < len(input); start += 65535 {
			end := start + 65535
			if end > len(input) {
				end = len(input)
			}
			writeStoredBlock(output, input[start:end], end == len(input))
		}
	}
	sum := adler32.Checksum(input)
	output.Write([]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)})
	return output.Bytes()
}

// writeStoredBlock writes one RFC 1950 stored (type 0) block.
func writeStoredBlock(output *bytes.Buffer, data []byte, final bool) {
	flags := byte(0)
	if final {
		flags = 1
	}
	output.WriteByte(flags) // bfinal=0/1, btype=00 -> low bit only
	length := len(data)
	output.WriteByte(byte(length))
	output.WriteByte(byte(length >> 8))
	output.WriteByte(byte(^length))
	output.WriteByte(byte(^length >> 8))
	output.Write(data)
}

// metadataWriteChain writes one forward chunk chain over freshly claimed
// private pages and returns its root (Rust metadata.rs write_chain): every
// page is the exact chunk geometry the readers enforce, the next pointer
// links forward, and the final page carries next zero.
func metadataWriteChain(s tree.Store, compressed []byte) (uint32, error) {
	if len(compressed) == 0 || uint64(len(compressed)) > format.MetadataCompressedBound(format.MaxMetadataUncompressed) {
		return 0, invalid("compressed metadata length is invalid")
	}
	count := (len(compressed) + format.MaxMetadataChunkLen - 1) / format.MaxMetadataChunkLen
	if count > format.MaxMetadataChainPages {
		return 0, corrupt("metadata chain exceeds its fixed bound")
	}
	pages := make([]uint32, count)
	for i := range pages {
		pageNumber, err := s.Allocate()
		if err != nil {
			return 0, err
		}
		pages[i] = pageNumber
	}
	targetTxn := s.TargetTxn()
	for i := range pages {
		start := i * format.MaxMetadataChunkLen
		end := start + format.MaxMetadataChunkLen
		if end > len(compressed) {
			end = len(compressed)
		}
		next := uint32(0)
		if i+1 < len(pages) {
			next = pages[i+1]
		}
		chunk := compressed[start:end]
		if err := s.Update(pages[i], func(page []byte) error {
			format.InitializePageHeader(page, format.PageTypeMetadataChunk, targetTxn, 1, 0, uint16(48+len(chunk)), format.PageSize, 0)
			format.PutU32(page[32:36], next)
			format.PutU16(page[36:38], uint16(len(chunk)))
			format.PutU64(page[40:48], uint64(start))
			copy(page[48:], chunk)
			return nil
		}); err != nil {
			return 0, err
		}
	}
	return pages[0], nil
}

// metadataCollectPages walks one committed metadata chain and returns
// every page number in order (Rust metadata.rs collect_pages). The walk
// applies the exact reader chain contract: header identity, chunk
// geometry, contiguous logical offsets, full non-final chunks, zero
// reserved fields, and zero tail bytes.
func metadataCollectPages(s tree.Store, meta format.Meta) ([]uint32, error) {
	var pages []uint32
	if meta.MetadataRoot == 0 {
		return nil, nil
	}
	pgno := meta.MetadataRoot
	offset := uint64(0)
	remaining := meta.MetadataCompressed
	for {
		if len(pages) == format.MaxMetadataChainPages {
			return nil, corrupt("metadata chain exceeds its fixed bound")
		}
		pages = append(pages, pgno)
		var next uint32
		var length uint16
		err := s.Inspect(pgno, func(page []byte) error {
			h, err := format.DecodePageHeader(page, meta.TxnID)
			if err != nil {
				return corrupt("metadata chunk page")
			}
			if h.PageType != format.PageTypeMetadataChunk || h.Level != 0 || h.Aux != 0 || h.ItemCount != 1 {
				return corrupt("metadata chunk page")
			}
			chunk, err := format.DecodeMetadataChunk(page)
			if err != nil {
				return corrupt("metadata chunk page")
			}
			if h.Lower != 48+chunk.ChunkLen || h.Upper != format.PageSize {
				return corrupt("metadata chunk geometry")
			}
			if chunk.LogicalOffset != offset {
				return corrupt(fmt.Sprintf("metadata offset %d expected %d", chunk.LogicalOffset, offset))
			}
			if uint64(chunk.ChunkLen) > remaining {
				return corrupt("metadata chain longer than declared")
			}
			if chunk.ChunkLen != format.MaxMetadataChunkLen && uint64(chunk.ChunkLen) != remaining {
				return corrupt("nonfinal metadata chunk shorter than full")
			}
			next = chunk.Next
			length = chunk.ChunkLen
			for _, b := range page[48+int(chunk.ChunkLen):] {
				if b != 0 {
					return corrupt("metadata chunk tail nonzero")
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		offset += uint64(length)
		remaining -= uint64(length)
		if next == 0 {
			break
		}
		if !format.PageNumberValid(next, meta.PageCount) {
			return nil, corrupt("metadata next out of range")
		}
		pgno = next
	}
	if remaining != 0 {
		return nil, corrupt(fmt.Sprintf("metadata chain ends at %d declared %d", offset, meta.MetadataCompressed))
	}
	return pages, nil
}

// WriteMetadata stages one exact metadata payload on a one-shot output
// (Rust immutable_output::Builder::write_metadata_with_budget): the
// caller heap budget bounds the compression workspace, the compressed
// bytes land through the forward chunk chain, and one metadata stage is
// allowed per output.
func (b *OutputBuilder) WriteMetadata(input []byte, maxHeapBytes uint64) error {
	return b.mutate(func() error {
		if b.metadataStaged {
			return wrongState("immutable output metadata is already set")
		}
		compressed, err := metadataCompress(input, maxHeapBytes)
		if err != nil {
			return err
		}
		root, err := metadataWriteChain(b, compressed)
		if err != nil {
			return err
		}
		b.meta.MetadataRoot = root
		b.meta.MetadataUncompressed = uint64(len(input))
		b.meta.MetadataCompressed = uint64(len(compressed))
		b.metadataStaged = true
		return nil
	})
}
