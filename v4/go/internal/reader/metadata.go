package reader

import (
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
	// Pass 1 walks the whole chain and validates its geometry without
	// copying the payload into owned memory, capturing only the stream's
	// first two bytes (zlib header) and last four bytes (Adler-32 trailer).
	var (
		first [2]byte
		n1    int
		last  [4]byte
		n4    int
		total uint64
		pgno  = meta.MetadataRoot
	)
	var offset uint64
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
		// Bytes after the chunk must be zero (binary-format-v4.md:1051);
		// Rust rejects the page on the read path (metadata.rs:274) and as
		// PageReservedNonzero on validation.
		for _, b := range page[48+int(chunk.ChunkLen):] {
			if b != 0 {
				return nil, false, corrupt("metadata chunk tail nonzero")
			}
		}
		if chunk.LogicalOffset != offset {
			return nil, false, corrupt("metadata offset %d expected %d", chunk.LogicalOffset, offset)
		}
		if total+uint64(chunk.ChunkLen) > meta.MetadataCompressed {
			return nil, false, corrupt("metadata chain longer than declared")
		}
		// Keep only the first two stream bytes.
		d := chunk.Data
		if n1 < 2 {
			take := 2 - n1
			if take > len(d) {
				take = len(d)
			}
			copy(first[n1:n1+take], d[:take])
			n1 += take
			d = d[take:]
		} else if len(d) > 0 {
			// The window stays filled: the first two bytes are fixed the
			// moment the second stream byte is seen, so later chunks never
			// change them.
		}
		// Keep the last four stream bytes (sliding window).
		d = chunk.Data
		if n4 < 4 {
			take := 4 - n4
			if take > len(d) {
				take = len(d)
			}
			copy(last[n4:n4+take], d[:take])
			n4 += take
			d = d[take:]
		}
		if n4 == 4 && len(d) > 0 {
			if len(d) >= 4 {
				copy(last[:], d[len(d)-4:])
			} else {
				copy(last[:4-len(d)], last[len(d):])
				copy(last[4-len(d):], d)
			}
		}
		total += uint64(chunk.ChunkLen)
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
	if total < 6 {
		return nil, false, corrupt("metadata stream shorter than zlib header+trailer")
	}
	if first[0]&0x0f != 8 || first[0]>>4 > 7 || first[1]>>5&1 != 0 {
		return nil, false, corrupt("metadata zlib header flags")
	}
	// RFC 1950: the header check bits must satisfy (CMF*256+FLG) % 31 == 0.
	if (uint16(first[0])<<8|uint16(first[1]))%31 != 0 {
		return nil, false, corrupt("metadata zlib header check mismatch")
	}
	// One single inflation validates the whole stream, read straight from
	// the mapped chunk views (Rust metadata.rs streams the chain into the
	// inflater the same way): the payload is the stream minus the two
	// header bytes and the four trailer bytes. metadataStream implements
	// ReadByte, so flate uses it directly (no bufio read-ahead) and stops
	// exactly at the final DEFLATE block; the output cap keeps work bounded
	// at declared+1 (metadata.rs step bounds output the same way). Nothing
	// accumulates the compressed stream in owned memory.
	stream := &metadataStream{page: r.page, next: meta.MetadataRoot, left: total - 6, skip: 2}
	// One exact allocation for the declared uncompressed size plus the
	// one-byte overflow probe: a truncation is ErrUnexpectedEOF, an
	// over-long stream leaves the probe byte set. No growth reallocations.
	out := make([]byte, int(meta.MetadataUncompressed)+1)
	zr := flate.NewReader(stream)
	if _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {
		zr.Close()
		return nil, false, corrupt("metadata deflate stream: %v", err)
	}
	n, err := io.ReadFull(zr, out[int(meta.MetadataUncompressed):])
	zr.Close()
	if n != 0 || err != io.EOF {
		return nil, false, corrupt("metadata decompressed %d declared %d", int(meta.MetadataUncompressed)+n, meta.MetadataUncompressed)
	}
	if stream.read != total-6 {
		return nil, false, corrupt("metadata stream trailing bytes")
	}
	out = out[:int(meta.MetadataUncompressed)]
	if binary.BigEndian.Uint32(last[:]) != adler32.Checksum(out) {
		return nil, false, corrupt("metadata adler32 trailer")
	}
	return out, true, nil
}

// metadataStream exposes the DEFLATE payload of a validated metadata chain
// (the stream minus its 2-byte zlib header and 4-byte Adler-32 trailer) as
// an io.Reader over the mapped chunk views. The chunk views alias the
// mapping; the compressed stream is never accumulated in owned memory.
// Pass 1 of ReadMetadataJSON validated the whole chain, so Read only
// follows the recorded Next pointers and serves the chunk payloads.
type metadataStream struct {
	page func(uint32) ([]byte, error)
	next uint32 // next chain page; 0 = chain end
	view []byte // unread payload of the current chunk
	skip uint64 // header bytes still to skip (2 at start)
	left uint64 // payload bytes still to expose
	read uint64 // payload bytes delivered
	err  error  // page or decode failure (defensive; pass 1 validated the chain)
}

// ReadByte serves one payload byte (flate.Reader contract). Counting here
// and in Read keeps the post-inflate check exact: flate consumes the source
// directly without buffering, so bytes between the final DEFLATE block and
// the Adler-32 trailer are never read and the count falls short.
func (s *metadataStream) ReadByte() (byte, error) {
	if s.err != nil {
		return 0, s.err
	}
	for {
		if s.left == 0 {
			return 0, io.EOF
		}
		if len(s.view) == 0 {
			if s.next == 0 {
				s.err = io.ErrUnexpectedEOF
				return 0, s.err
			}
			page, err := s.page(s.next)
			if err != nil {
				s.err = err
				return 0, err
			}
			chunk, err := format.DecodeMetadataChunk(page)
			if err != nil {
				s.err = err
				return 0, err
			}
			s.view = chunk.Data
			s.next = chunk.Next
		}
		v := s.view
		if s.skip > 0 {
			if uint64(len(v)) <= s.skip {
				s.skip -= uint64(len(v))
				s.view = nil
				continue
			}
			v = v[s.skip:]
			s.skip = 0
		}
		s.view = v[1:]
		s.left--
		s.read++
		return v[0], nil
	}
}

func (s *metadataStream) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	delivered := 0
	for len(p) > 0 && s.left > 0 {
		if len(s.view) == 0 {
			if s.next == 0 {
				break
			}
			page, err := s.page(s.next)
			if err != nil {
				s.err = err
				break
			}
			chunk, err := format.DecodeMetadataChunk(page)
			if err != nil {
				s.err = err
				break
			}
			s.view = chunk.Data
			s.next = chunk.Next
		}
		v := s.view
		if s.skip > 0 {
			if uint64(len(v)) <= s.skip {
				s.skip -= uint64(len(v))
				s.view = nil
				continue
			}
			v = v[s.skip:]
			s.skip = 0
		}
		if uint64(len(v)) > s.left {
			v = v[:s.left]
		}
		c := copy(p, v)
		s.view = v[c:]
		s.left -= uint64(c)
		s.read += uint64(c)
		delivered += c
		p = p[c:]
	}
	if delivered > 0 {
		return delivered, nil
	}
	if s.err != nil {
		return 0, s.err
	}
	return 0, io.EOF
}
