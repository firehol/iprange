package reader

import (
	"compress/flate"
	"encoding/binary"
	"errors"
	"hash/adler32"
	"io"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// The chain walk cap is the shared format authority bound (Rust
// metadata.rs MAX_PAGES = 5_182; the section-11 compressed window).

// ReadMetadataJSON returns the exact decompressed opaque metadata bytes.
// present is false for absent metadata (root zero); an empty non-nil slice
// with present true is the exact empty-payload state (section 11).
//
// The chain is walked exactly once, like Rust metadata::read: each page is
// validated (header, chunk fields, geometry, reserved-zero tail, link
// validity, the MAX_PAGES cap) and its payload is fed to the inflater on
// the same visit. metadataStream implements ReadByte, so flate consumes
// the source directly without read-ahead and stops exactly at the final
// DEFLATE block; the read count then proves byte-exact stream end and any
// trailing junk is rejected. The compressed stream is never accumulated
// in owned memory.
func (r *ImmutableReader) ReadMetadataJSON() ([]byte, bool, error) {
	meta := r.meta
	if meta.MetadataRoot == 0 {
		return nil, false, nil
	}
	if meta.MetadataCompressed < 6 {
		return nil, false, corrupt("metadata stream shorter than zlib header+trailer")
	}
	stream := &metadataStream{
		page:      r.page,
		pageCount: meta.PageCount,
		txn:       meta.TxnID,
		next:      meta.MetadataRoot,
		skip:      2,
		left:      meta.MetadataCompressed - 6,
		count:     meta.MetadataCompressed,
	}
	// Prime the chain so the first two stream bytes (the RFC 1950 zlib
	// header) are captured and checked before any inflation starts,
	// preserving the two-pass reader's check order.
	if err := stream.primeHeader(); err != nil {
		return nil, false, err
	}
	// One exact allocation for the declared uncompressed size plus the
	// one-byte overflow probe: a truncation is ErrUnexpectedEOF, an
	// over-long stream leaves the probe byte set. No growth reallocations.
	out := make([]byte, int(meta.MetadataUncompressed)+1)
	zr := flate.NewReader(stream)
	if _, err := io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]); err != nil {
		zr.Close()
		var formatErr *format.Error
		if errors.As(err, &formatErr) {
			return nil, false, err
		}
		return nil, false, corrupt("metadata deflate stream: %v", err)
	}
	n, err := io.ReadFull(zr, out[int(meta.MetadataUncompressed):])
	zr.Close()
	if n != 0 || err != io.EOF {
		return nil, false, corrupt("metadata decompressed %d declared %d", int(meta.MetadataUncompressed)+n, meta.MetadataUncompressed)
	}
	if stream.read != stream.count-6 {
		return nil, false, corrupt("metadata stream trailing bytes")
	}
	out = out[:int(meta.MetadataUncompressed)]
	if binary.BigEndian.Uint32(stream.trailer()) != adler32.Checksum(out) {
		return nil, false, corrupt("metadata adler32 trailer")
	}
	return out, true, nil
}

// metadataStream serves the DEFLATE payload of a validated metadata chain
// (the stream minus its 2-byte zlib header and 4-byte Adler-32 trailer) as
// an io.Reader over the mapped chunk views, validating and feeding one
// page per visit (Rust walk_chain + Inflater parity). The chunk views
// alias the mapping; the compressed stream is never accumulated in owned
// memory. Geometry and link validation happen on the same visit that
// feeds the inflater, so a malformed chain can never hand unvalidated
// bytes to the decoder.
type metadataStream struct {
	page      func(uint32) ([]byte, error)
	pageCount uint64
	txn       uint64
	next      uint32 // next chain page; 0 = chain end
	view      []byte // unread payload of the current chunk
	skip      uint64 // header bytes still to skip (2 at start)
	left      uint64 // payload bytes still to expose
	read      uint64 // payload bytes delivered
	count     uint64 // declared compressed length (header+payload+trailer)
	off       uint64 // chain bytes visited (stream offset incl. header/trailer)
	pages     uint64 // chain pages visited (Rust MAX_PAGES cap)
	first     [2]byte
	n1        int
	last      [4]byte
	n4        int
	err       error // page or decode failure (defensive; each visit validates first)
}

// primeHeader visits chunks until the two zlib header bytes are captured
// and validates the RFC 1950 header constraints (binary-format-v4.md
// section 11) before any payload byte is served.
func (s *metadataStream) primeHeader() error {
	for s.err == nil && s.n1 < 2 {
		if len(s.view) == 0 {
			s.visitNext()
		} else {
			// The current view's bytes were already captured at the
			// visit; a sub-two-byte chunk contributes its whole payload
			// to the header window and the view is retired.
			dropped := uint64(len(s.view))
			s.view = nil
			if s.skip > dropped {
				s.skip -= dropped
			} else {
				s.skip = 0
			}
		}
	}
	if s.err != nil {
		return s.err
	}
	if s.first[0]&0x0f != 8 || s.first[0]>>4 > 7 || s.first[1]>>5&1 != 0 {
		return corrupt("metadata zlib header flags")
	}
	// RFC 1950: the header check bits must satisfy (CMF*256+FLG) % 31 == 0.
	if (uint16(s.first[0])<<8|uint16(s.first[1]))%31 != 0 {
		return corrupt("metadata zlib header check mismatch")
	}
	return nil
}

// visitNext fetches and validates the next chain page, captures the
// stream-edge windows, and prepares its payload for serving. Every check
// mirrors the Rust walk_chain parse_page + chunk_fields + reserved_zero
// on the same visit that feeds the inflater.
func (s *metadataStream) visitNext() {
	if s.pages == format.MaxMetadataChainPages {
		s.err = corrupt("metadata chain exceeds its fixed bound")
		return
	}
	page, err := s.page(s.next)
	if err != nil {
		s.err = err
		return
	}
	h, err := format.DecodePageHeader(page, s.txn)
	if err != nil {
		s.err = err
		return
	}
	if h.PageType != format.PageTypeMetadataChunk || h.Level != 0 || h.Aux != 0 || h.ItemCount != 1 {
		s.err = corrupt("metadata chunk page")
		return
	}
	chunk, err := format.DecodeMetadataChunk(page)
	if err != nil {
		s.err = err
		return
	}
	if h.Lower != 48+chunk.ChunkLen || h.Upper != format.PageSize {
		s.err = corrupt("metadata chunk geometry")
		return
	}
	// Bytes after the chunk must be zero (binary-format-v4.md:1051);
	// Rust rejects the page on the read path (metadata.rs:274) and as
	// PageReservedNonzero on validation.
	for _, b := range page[48+int(chunk.ChunkLen):] {
		if b != 0 {
			s.err = corrupt("metadata chunk tail nonzero")
			return
		}
	}
	if chunk.LogicalOffset != s.off {
		s.err = corrupt("metadata offset %d expected %d", chunk.LogicalOffset, s.off)
		return
	}
	remaining := s.count - s.off
	if uint64(chunk.ChunkLen) > remaining {
		s.err = corrupt("metadata chain longer than declared")
		return
	}
	final := uint64(chunk.ChunkLen) == remaining
	if !final && chunk.ChunkLen != format.MaxMetadataChunkLen {
		s.err = corrupt("nonfinal metadata chunk shorter than full")
		return
	}
	if final && chunk.Next != 0 {
		s.err = corrupt("metadata chain has an extra page")
		return
	}
	if !final && (!format.PageNumberValid(chunk.Next, s.pageCount) || chunk.Next == s.next) {
		s.err = corrupt("metadata next out of range")
		return
	}
	// Keep the first two stream bytes (zlib header).
	if s.n1 < 2 {
		take := 2 - s.n1
		if take > len(chunk.Data) {
			take = len(chunk.Data)
		}
		copy(s.first[s.n1:s.n1+take], chunk.Data[:take])
		s.n1 += take
	}
	// Keep the last four stream bytes (Adler-32 trailer; sliding window).
	d := chunk.Data
	if s.n4 < 4 {
		take := 4 - s.n4
		if take > len(d) {
			take = len(d)
		}
		copy(s.last[s.n4:s.n4+take], d[:take])
		s.n4 += take
		d = d[take:]
	}
	if s.n4 == 4 && len(d) > 0 {
		if len(d) >= 4 {
			copy(s.last[:], d[len(d)-4:])
		} else {
			copy(s.last[:4-len(d)], s.last[len(d):])
			copy(s.last[4-len(d):], d)
		}
	}
	s.next = chunk.Next
	s.view = chunk.Data
	s.off += uint64(chunk.ChunkLen)
	s.pages++
}

// trailer returns the captured Adler-32 trailer bytes; valid only when
// the whole declared stream was served (the caller checks the read count
// first).
func (s *metadataStream) trailer() []byte { return s.last[:] }

// ReadByte serves one payload byte (flate.Reader contract). Counting here
// and in Read keeps the post-inflate check exact: flate consumes the
// source directly without buffering, so bytes between the final DEFLATE
// block and the Adler-32 trailer are never read and the count falls short.
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
			s.visitNext()
			if s.err != nil {
				return 0, s.err
			}
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
			s.visitNext()
			if s.err != nil {
				break
			}
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
