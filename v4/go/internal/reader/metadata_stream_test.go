package reader

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/adler32"
	"io"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestMetadataStreamPayload pins the streaming metadata decode edges: the
// two-byte zlib header and the four-byte Adler-32 trailer may both span
// chunk boundaries, and the reader must expose exactly the DEFLATE payload
// without ever materializing the compressed stream in owned memory.
func TestMetadataStreamPayload(t *testing.T) {
	const text = "hello metadata chain, streamed from mapped chunks"
	var zbuf bytes.Buffer
	zbuf.Write([]byte{0x78, 0x01}) // zlib header (CMF/FLG)
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
	stream := zbuf.Bytes()
	if len(stream) < 6 {
		t.Fatal("fixture stream too short")
	}
	minimal := minimalZlibStream(t)
	cases := []struct {
		name   string
		stream []byte
		splits [][]byte
	}{
		{"header-and-trailer split across chunks", stream, [][]byte{
			stream[0:1], stream[1:3], stream[3:10],
			stream[10 : len(stream)-3], stream[len(stream)-3:],
		}},
		{"one byte per chunk", stream, splitEvery(stream, 1)},
		{"single whole chunk", stream, [][]byte{stream}},
		{"minimal empty stream", minimal, splitEvery(minimal, 2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var total int
			for _, c := range tc.splits {
				total += len(c)
			}
			if total != len(tc.stream) {
				t.Fatalf("splits cover %d bytes, stream has %d", total, len(tc.stream))
			}
			payload := tc.stream[2 : len(tc.stream)-4]
			s := &metadataStream{
				page: metadataChunkPageFunc(tc.splits),
				next: 1,
				left: uint64(len(tc.stream) - 6),
				skip: 2,
			}
			got, err := io.ReadAll(s)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
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

// TestMetadataStreamFlateCounting pins the flate integration Plato demanded:
// metadataStream implements ReadByte, so flate consumes it directly (no
// bufio read-ahead) and the post-inflate count is byte-exact. Junk placed
// between the final DEFLATE block and the Adler-32 trailer must leave bytes
// unread, which is exactly how ReadMetadataJSON rejects trailing garbage.
func TestMetadataStreamFlateCounting(t *testing.T) {
	const text = "counting fixture for flate integration"
	stream := zlibFixture(t, text)
	payload := stream[2 : len(stream)-4]

	valid := &metadataStream{page: metadataChunkPageFunc([][]byte{stream}), next: 1, left: uint64(len(stream) - 6), skip: 2}
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
	bad := &metadataStream{page: metadataChunkPageFunc([][]byte{junked}), next: 1, left: uint64(len(junked) - 6), skip: 2}
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

// metadataChunkPageFunc links chunk payloads into validated-shape metadata
// chunk pages indexed 1..n and returns a page fetcher for metadataStream.
func metadataChunkPageFunc(chunks [][]byte) func(uint32) ([]byte, error) {
	pages := make([][]byte, len(chunks))
	var off uint64
	for i, c := range chunks {
		p := make([]byte, format.PageSize)
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
