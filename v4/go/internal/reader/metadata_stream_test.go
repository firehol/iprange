package reader

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"hash/adler32"
	"io"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestMetadataStreamPayload pins the streaming metadata decode edges over
// valid chain geometry: the two-byte zlib header is skipped exactly once,
// multi-chunk chains stream across page boundaries, an empty payload is
// exact, and geometry-invalid chains (header split across chunks, a chunk
// shorter than the declared remainder) are refused on the read path.
func TestMetadataStreamPayload(t *testing.T) {
	const text = "hello metadata chain, streamed from mapped chunks"
	stream := zlibFixture(t, text)
	minimal := minimalZlibStream(t)
	big := zlibFixture(t, strings.Repeat("0123456789abcdef", 700))
	cases := []struct {
		name    string
		stream  []byte
		chunks  [][]byte
		wantErr bool
	}{
		{"single whole chunk", stream, [][]byte{stream}, false},
		{"multi-chunk chain", big, validChunks(big), false},
		{"minimal empty stream", minimal, [][]byte{minimal}, false},
		{"header split across chunks", stream, [][]byte{stream[0:1], stream[1:]}, true},
		{"truncated chain", stream, [][]byte{stream[:20]}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &metadataStream{
				page:      metadataChunkPageFunc(tc.chunks, 1),
				pageCount: uint64(len(tc.chunks)) + 2,
				txn:       1,
				next:      1,
				left:      uint64(len(tc.stream) - 6),
				count:     uint64(len(tc.stream)),
				skip:      2,
			}
			got, err := io.ReadAll(s)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ReadAll accepted an invalid chain")
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			payload := tc.stream[2 : len(tc.stream)-4]
			if !bytes.Equal(got, payload) {
				t.Fatalf("payload mismatch: got %d bytes want %d", len(got), len(payload))
			}
			if s.read != uint64(len(payload)) {
				t.Fatalf("read counter %d want %d", s.read, len(payload))
			}
			if _, err := s.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("second Read = %v, want EOF", err)
			}
		})
	}
}

// validChunks splits one stream into full metadata chunks (4048 bytes)
// plus the final shorter chunk, the only geometry the format permits.
func validChunks(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 4048 {
		out = append(out, b[:4048])
		b = b[4048:]
	}
	return append(out, b)
}

// TestMetadataStreamFlateCounting pins the flate integration Plato demanded:
// metadataStream implements ReadByte, so flate consumes it directly (no
// bufio read-ahead) and the post-inflate count is byte-exact. Junk placed
// between the final DEFLATE block and the Adler-32 trailer must leave bytes
// unread, which is exactly how ReadMetadataJSON rejects trailing garbage.
func TestMetadataStreamFlateCounting(t *testing.T) {
	const text = "counting fixture for flate integration"
	stream := zlibFixture(t, text)
	payload := stream[2 : len(stream)-4]

	valid := &metadataStream{page: metadataChunkPageFunc([][]byte{stream}, 1), pageCount: 5, txn: 1, next: 1, left: uint64(len(stream) - 6), count: uint64(len(stream)), skip: 2}
	got, err := io.ReadAll(flate.NewReader(valid))
	if err != nil {
		t.Fatalf("valid stream: %v", err)
	}
	if string(got) != text {
		t.Fatalf("valid stream decoded %q", got)
	}
	if valid.read != uint64(len(payload)) {
		t.Fatalf("valid stream count %d want %d", valid.read, len(payload))
	}

	junked := append(append([]byte{}, stream[:len(stream)-4]...), append([]byte{0xde, 0xad}, stream[len(stream)-4:]...)...)
	bad := &metadataStream{page: metadataChunkPageFunc([][]byte{junked}, 1), pageCount: 5, txn: 1, next: 1, left: uint64(len(junked) - 6), count: uint64(len(junked)), skip: 2}
	got, err = io.ReadAll(flate.NewReader(bad))
	if err != nil {
		t.Fatalf("junked stream deflate: %v", err)
	}
	if string(got) != text {
		t.Fatalf("junked stream decoded %q", got)
	}
	if bad.read >= uint64(len(junked)-6) {
		t.Fatalf("junk-before-trailer fully consumed: read %d of %d", bad.read, len(junked)-6)
	}
}

// zlibFixture builds a complete zlib stream for text (header, DEFLATE,
// Adler-32 trailer) the way the writer frames metadata.
func zlibFixture(t *testing.T, text string) []byte {
	t.Helper()
	var zbuf bytes.Buffer
	zbuf.Write([]byte{0x78, 0x01})
	fw, err := flate.NewWriter(&zbuf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], adler32.Checksum([]byte(text)))
	zbuf.Write(a[:])
	return zbuf.Bytes()
}

// splitEvery cuts b into n-byte chunks (last chunk may be shorter).
func splitEvery(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		if len(b) < n {
			n = len(b)
		}
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}

// minimalZlibStream builds a valid zlib stream for empty input (header,
// terminating stored/fixed block, Adler-32 trailer) the way the writer does.
func minimalZlibStream(t *testing.T) []byte {
	t.Helper()
	var zbuf bytes.Buffer
	zbuf.Write([]byte{0x78, 0x01})
	fw, err := flate.NewWriter(&zbuf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], adler32.Checksum(nil))
	zbuf.Write(a[:])
	return zbuf.Bytes()
}

// metadataChunkPageFunc links chunk payloads into fully valid metadata
// chunk pages (page header plus chunk fields) indexed 1..n and returns a
// page fetcher for metadataStream. Pages carry the shape the reader
// validates on every visit: type 13, level 0, aux 0, item count 1,
// lower 48+len, upper 4096, born transaction 1.
func metadataChunkPageFunc(chunks [][]byte, txn uint64) func(uint32) ([]byte, error) {
	pages := make([][]byte, len(chunks))
	var off uint64
	for i, c := range chunks {
		p := make([]byte, format.PageSize)
		copy(p[0:4], format.PageMagic[:])
		p[4] = byte(format.PageTypeMetadataChunk)
		binary.LittleEndian.PutUint16(p[6:8], 32)
		binary.LittleEndian.PutUint64(p[8:16], txn)
		binary.LittleEndian.PutUint16(p[16:18], 1)
		binary.LittleEndian.PutUint16(p[18:20], 0)
		binary.LittleEndian.PutUint16(p[20:22], uint16(48+len(c)))
		binary.LittleEndian.PutUint16(p[22:24], format.PageSize)
		binary.LittleEndian.PutUint32(p[24:28], 0)
		var next uint32
		if i+1 < len(chunks) {
			next = uint32(i + 2) // pages are 1-based
		}
		binary.LittleEndian.PutUint32(p[32:36], next)
		binary.LittleEndian.PutUint16(p[36:38], uint16(len(c)))
		binary.LittleEndian.PutUint64(p[40:48], off)
		copy(p[48:48+len(c)], c)
		pages[i] = p
		off += uint64(len(c))
	}
	return func(pg uint32) ([]byte, error) {
		if pg < 1 || int(pg) > len(pages) {
			return nil, corrupt("metadata page %d out of range", pg)
		}
		return pages[pg-1], nil
	}
}

// TestMetadataChainPageCap pins the Rust MAX_PAGES bound (metadata.rs
// 5_182): a chain longer than the fixed cap is refused on the read path
// even when its declared length would permit it, exactly like Rust
// walk_chain ("metadata chain exceeds its fixed bound").
func TestMetadataChainPageCap(t *testing.T) {
	// A valid chain of MAX_PAGES full chunks plus one more: the declared
	// payload needs one extra visit beyond the fixed bound, and the walk
	// refuses exactly like Rust walk_chain before visiting the extra
	// page. A full final chunk is valid geometry (final = the chunk that
	// exactly matches the declared remainder).
	chunks := make([][]byte, format.MaxMetadataChainPages+1)
	var off uint64
	for i := range chunks {
		b := make([]byte, format.MaxMetadataChunkLen)
		chunks[i] = b
		off += uint64(len(b))
	}
	count := off
	stream := &metadataStream{
		page:      metadataChunkPageFunc(chunks, 1),
		pageCount: uint64(len(chunks)) + 2,
		txn:       1,
		next:      1,
		left:      uint64(count) - 6,
		count:     uint64(count),
		skip:      2,
	}
	if _, err := io.ReadAll(stream); err == nil {
		t.Fatal("chain longer than the fixed bound was accepted")
	} else if !containsDetail(err, "metadata chain exceeds its fixed bound") {
		t.Fatalf("cap error = %v, want the fixed-bound refusal", err)
	}
	if stream.pages != format.MaxMetadataChainPages {
		t.Fatalf("visited %d pages before the cap, want %d", stream.pages, format.MaxMetadataChainPages)
	}
}

// containsDetail reports whether err carries the detail text somewhere in
// its message chain.
func containsDetail(err error, detail string) bool {
	for err != nil {
		if fe, ok := err.(*format.Error); ok && fe.Detail == detail {
			return true
		}
		if u := errorsUnwrap(err); u != nil {
			err = u
		} else {
			break
		}
	}
	return false
}

// errorsUnwrap returns the first wrapped error (errors.Unwrap).
func errorsUnwrap(err error) error {
	if err == nil {
		return nil
	}
	return errors.Unwrap(err)
}
