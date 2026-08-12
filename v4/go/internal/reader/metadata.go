package reader

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/adler32"
	"io"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// ReadMetadataJSON returns the exact decompressed opaque metadata bytes.
// present is false for absent metadata (root zero); an empty non-nil slice
// with present true is the exact empty-payload state (section 11).
func (r *ImmutableReader) ReadMetadataJSON() ([]byte, bool, error) {
	meta := r.meta
	if meta.MetadataRoot == 0 {
		return nil, false, nil
	}
	// Pre-allocation is bounded by the section-11 compressed bound (itself
	// enforced at bootstrap) and independently by the physical page count;
	// append grows beyond the bound if a corrupt chain slips through.
	bound := format.MetadataCompressedBound(meta.MetadataUncompressed)
	if pages := uint64(0); meta.PageCount >= 2 {
		pages = (meta.PageCount - 2) * format.MaxMetadataChunkLen
		if pages < bound {
			bound = pages
		}
	}
	compressed := make([]byte, 0, int(bound))
	pgno := meta.MetadataRoot
	offset := uint64(0)
	for {
		page, err := r.page(pgno)
		if err != nil {
			return nil, false, err
		}
		h, err := format.DecodePageHeader(page, meta.TxnID)
		if err != nil {
			return nil, false, err
		}
		if h.PageType != format.PageTypeMetadataChunk || h.Level != 0 || h.Aux != 0 || h.ItemCount != 1 {
			return nil, false, corrupt("metadata chunk page")
		}
		chunk, err := format.DecodeMetadataChunk(page)
		if err != nil {
			return nil, false, err
		}
		if h.Lower != 48+chunk.ChunkLen || h.Upper != format.PageSize {
			return nil, false, corrupt("metadata chunk geometry")
		}
		if chunk.LogicalOffset != offset {
			return nil, false, corrupt("metadata offset %d expected %d", chunk.LogicalOffset, offset)
		}
		if uint64(len(compressed))+uint64(chunk.ChunkLen) > meta.MetadataCompressed {
			return nil, false, corrupt("metadata chain longer than declared")
		}
		compressed = append(compressed, chunk.Data...)
		offset += uint64(chunk.ChunkLen)
		if chunk.Next == 0 {
			break
		}
		if chunk.ChunkLen != format.MaxMetadataChunkLen {
			return nil, false, corrupt("nonfinal metadata chunk shorter than full")
		}
		if !format.PageNumberValid(chunk.Next, meta.PageCount) {
			return nil, false, corrupt("metadata next out of range")
		}
		pgno = chunk.Next
	}
	if offset != meta.MetadataCompressed {
		return nil, false, corrupt("metadata chain ends at %d declared %d", offset, meta.MetadataCompressed)
	}
	// The stream must be exactly one complete RFC 1950 zlib stream
	// (section 11): header constraints checked below, the DEFLATE payload
	// must end exactly at the final block, and the last four bytes must be
	// the Adler-32 of the uncompressed output. Trailing bytes and
	// concatenated streams are rejected.
	if len(compressed) < 6 {
		return nil, false, corrupt("metadata stream shorter than zlib header+trailer")
	}
	if compressed[0]&0x0f != 8 || compressed[0]>>4 > 7 || compressed[1]>>5&1 != 0 {
		return nil, false, corrupt("metadata zlib header flags")
	}
	// RFC 1950: the header check bits must satisfy (CMF*256+FLG) % 31 == 0.
	if (uint16(compressed[0])<<8|uint16(compressed[1]))%31 != 0 {
		return nil, false, corrupt("metadata zlib header check mismatch")
	}
	payload := compressed[2 : len(compressed)-4]
	// One single inflation validates the whole stream: Go's flate consumes
	// input through ReadByte when the source implements io.ByteReader (a
	// bytes.Reader does), so the byte position after the final block is the
	// exact stream end. A consumed position short of the payload is trailing
	// garbage or a concatenated stream, an error is a malformed stream, and
	// the output cap keeps work bounded at declared+1 (metadata.rs step
	// bounds output the same way). The previous binary-search stream-length
	// probe inflated the payload O(log n) times (twelve times for the 1 MiB
	// fixture); this is one pass.
	cr := &consumedReader{r: bytes.NewReader(payload)}
	zr := flate.NewReader(cr)
	out, err := io.ReadAll(io.LimitReader(zr, int64(meta.MetadataUncompressed)+1))
	zr.Close()
	if err != nil {
		return nil, false, corrupt("metadata deflate stream: %v", err)
	}
	if cr.n != int64(len(payload)) {
		return nil, false, corrupt("metadata stream trailing bytes")
	}
	if uint64(len(out)) != meta.MetadataUncompressed {
		return nil, false, corrupt("metadata decompressed %d declared %d", len(out), meta.MetadataUncompressed)
	}
	if binary.BigEndian.Uint32(compressed[len(compressed)-4:]) != adler32.Checksum(out) {
		return nil, false, corrupt("metadata adler32 trailer")
	}
	return out, true, nil
}

// consumedReader tracks exactly how many input bytes the inflater consumed.
// bytes.Reader implements io.ByteReader, so Go's flate reads input
// byte-by-byte and stops at the final block: consumed == len(payload) proves
// one complete stream with no trailing bytes.
type consumedReader struct {
	r *bytes.Reader
	n int64
}

func (c *consumedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *consumedReader) ReadByte() (byte, error) {
	b, err := c.r.ReadByte()
	if err == nil {
		c.n++
	}
	return b, err
}
