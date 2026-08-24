package validation

// Slice-B metadata chain tests: the validating zlib walk over crafted
// metadata chains with the exact reason classes, the chain geometry
// refusals, and the clean-sweep PASS. The fixtures respect the writer
// guarantees the bootstrap enforces: the declared compressed length
// stays inside the per-uncompressed bound and the physical capacity.

import (
	"bytes"
	"compress/zlib"
	"errors"
	"hash/adler32"
	"math/rand"
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// fixtureRand is the deterministic source of incompressible fixture
// payloads.
var fixtureRand = rand.New(rand.NewSource(42))

// randomBytes builds one incompressible payload of the given length.
func randomBytes(length int) []byte {
	payload := make([]byte, length)
	fixtureRand.Read(payload)
	return payload
}

// zlibFixture compresses one payload into one zlib stream with the Go
// writer (Rust metadata round-trip fixtures use the same writer
// contract).
func zlibFixture(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// storedZlib builds one zlib stream of stored deflate blocks for the
// exact payload bytes: one block per full 65535-byte span, so the stream
// length equals the declared compressed bound exactly (the writer
// guarantee; used where the stream must fill whole chain chunks).
func storedZlib(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+40)
	out = append(out, 0x78, 0x01) // RFC 1950 header, check bits valid
	remaining := payload
	for {
		length := len(remaining)
		if length > 65535 {
			length = 65535
		}
		final := byte(0)
		if length == len(remaining) {
			final = 1
		}
		out = append(out, final, byte(length), byte(length>>8), byte(^length), byte(^(length >> 8)))
		out = append(out, remaining[:length]...)
		remaining = remaining[length:]
		if len(remaining) == 0 {
			break
		}
	}
	checksum := adler32.Checksum(payload)
	out = append(out, byte(checksum>>24), byte(checksum>>16), byte(checksum>>8), byte(checksum))
	return out
}

// metadataChainPage builds one committed metadata-chunk page carrying
// the given stream bytes.
func metadataChainPage(t *testing.T, born uint64, next uint32, data []byte, offset uint64, mutate func([]byte)) []byte {
	t.Helper()
	page := make([]byte, format.PageSize)
	copy(page[:4], format.PageMagic[:])
	page[4] = byte(format.PageTypeMetadataChunk)
	format.PutU16(page[6:8], 32)
	format.PutU64(page[8:16], born)
	format.PutU16(page[16:18], 1) // item count
	format.PutU16(page[18:20], 0) // level
	format.PutU16(page[20:22], uint16(format.MetadataDataOffset+len(data)))
	format.PutU16(page[22:24], format.PageSize) // upper
	format.PutU32(page[24:28], 0)               // aux
	format.PutU32(page[32:36], next)
	format.PutU16(page[36:38], uint16(len(data)))
	format.PutU16(page[38:40], 0) // reserved
	format.PutU64(page[40:48], offset)
	copy(page[format.MetadataDataOffset:], data)
	if mutate != nil {
		mutate(page)
	}
	if err := format.SealPageChecksum(page); err != nil {
		t.Fatal(err)
	}
	return page
}

// metadataChain builds the chain pages of one stream: every nonfinal
// page carries a full 4048-byte chunk and the final page carries the
// remainder, exactly like the writer lays the chain out.
func metadataChain(t *testing.T, born uint64, stream []byte) [][]byte {
	t.Helper()
	var pages [][]byte
	cursor := 0
	for cursor < len(stream) {
		length := len(stream) - cursor
		if length > format.MaxMetadataChunkLen {
			length = format.MaxMetadataChunkLen
		}
		next := uint32(0)
		if cursor+length < len(stream) {
			next = uint32(2 + len(pages) + 1)
		}
		pages = append(pages, metadataChainPage(t, born, next, stream[cursor:cursor+length], uint64(cursor), nil))
		cursor += length
	}
	return pages
}

// metadataDBPages builds one database with a metadata chain root at page
// 2, the given declared lengths, and the given committed page count;
// metadataDB derives the page count from the chain pages.
func metadataDBPages(t *testing.T, uncompressed, compressed, pageCount uint64, pages ...[]byte) (string, []ValidationFinding) {
	t.Helper()
	meta := metaPage(2, pageCount)
	format.PutU64(meta[120:128], uncompressed)
	format.PutU64(meta[128:136], compressed)
	format.PutU32(meta[172:176], 2) // MetadataRoot
	format.PutU32(meta[252:256], format.MetaCRC32C(meta))
	path := dbWithMeta(t, meta, pageCount, pages...)
	_, failure, findings := collectFindings(t, path)
	if failure != nil {
		t.Fatalf("sweep failed: %v", failure.Cause)
	}
	return path, findings
}

func metadataDB(t *testing.T, uncompressed, compressed uint64, pages ...[]byte) (string, []ValidationFinding) {
	t.Helper()
	return metadataDBPages(t, uncompressed, compressed, 2+uint64(len(pages)), pages...)
}

// metadataSingle builds one final chain page carrying the whole stream.
func metadataSingle(t *testing.T, stream []byte) []byte {
	t.Helper()
	return metadataChainPage(t, 2, 0, stream, 0, nil)
}

func TestValidateMetadataClean(t *testing.T) {
	// One final chain page with a complete stored-block stream: every
	// page is claimed by the graph walk, so the sweep is a clean PASS.
	payload := randomBytes(3000)
	stream := storedZlib(payload)
	path, findings := metadataDB(t, uint64(len(payload)), uint64(len(stream)), metadataSingle(t, stream))
	result, _, _ := collectFindings(t, path)
	if result == nil || !result.Valid || len(findings) != 0 {
		t.Fatalf("clean metadata sweep: valid=%v findings=%+v", result, findings)
	}
	if result.Progress.CheckedUniquePages != 1 || result.Progress.ExaminedFor(ObjectMetadata) != 1 {
		t.Fatalf("progress %+v", result.Progress)
	}
}

func TestValidateMetadataChainedClean(t *testing.T) {
	// A stream spanning three chain pages (two full chunks plus the
	// final remainder) decodes cleanly with the exact page visits.
	payload := randomBytes(9600)
	stream := storedZlib(payload)
	pages := metadataChain(t, 2, stream)
	if len(pages) != 3 {
		t.Fatalf("chain pages %d, want 3", len(pages))
	}
	path, findings := metadataDB(t, uint64(len(payload)), uint64(len(stream)), pages...)
	result, _, _ := collectFindings(t, path)
	if result == nil || !result.Valid || len(findings) != 0 {
		t.Fatalf("chained clean sweep: valid=%v findings=%+v", result, findings)
	}
	if result.Progress.CheckedUniquePages != 3 {
		t.Fatalf("checked pages %d", result.Progress.CheckedUniquePages)
	}
}

func TestValidateMetadataZlibFailures(t *testing.T) {
	// Decoder failures surface as the MetadataZlibInvalid finding of the
	// page being fed.
	payload := randomBytes(3000)
	stream := storedZlib(payload)
	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"zlib header", func(p []byte) { p[format.MetadataDataOffset] ^= 0x10 }},
		{"check bits", func(p []byte) { p[format.MetadataDataOffset+1] ^= 1 }},
		{"adler trailer", func(p []byte) { p[format.MetadataDataOffset+len(stream)-1] ^= 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := metadataChainPage(t, 2, 0, stream, 0, tc.mutate)
			_, findings := metadataDB(t, uint64(len(payload)), uint64(len(stream)), page)
			if len(findings) != 1 || findings[0].Reason != ReasonMetadataZlibInvalid ||
				findings[0].Object != ObjectMetadata ||
				findings[0].PageNumber == nil || *findings[0].PageNumber != 2 {
				t.Fatalf("findings %+v", findings)
			}
		})
	}
}

func TestValidateMetadataLengthFailures(t *testing.T) {
	// Compressible payloads keep the fixture streams small (the declared
	// lengths must stay inside the bootstrap bound).
	payload := bytes.Repeat([]byte("a"), 3000)
	stream := zlibFixture(t, payload)
	compressible := bytes.Repeat([]byte("a"), 1000)
	compressibleStream := zlibFixture(t, compressible)
	cases := []struct {
		name         string
		data         []byte
		uncompressed uint64
		compressed   uint64
		pageNil      bool // finish-class finding (no page)
	}{
		{
			// The declared stream stops before the deflate stream
			// completes: the finish-class length finding.
			name:         "truncated stream",
			data:         stream[:len(stream)-4],
			uncompressed: uint64(len(payload)),
			compressed:   uint64(len(stream) - 4),
			pageNil:      true,
		},
		{
			// The declared stream continues past the clean zlib end:
			// the trailing bytes report on the page carrying them.
			name:         "trailing stream",
			data:         append(append([]byte{}, stream...), 0, 0, 0, 0),
			uncompressed: uint64(len(payload)),
			compressed:   uint64(len(stream) + 4),
			pageNil:      false,
		},
		{
			// The stream produces more output than declared: the
			// overflow surfaces while the page feed still has input
			// (Rust step excess arm), so the finding carries the page
			// being fed.
			name:         "output too small",
			data:         compressibleStream,
			uncompressed: 500,
			compressed:   uint64(len(compressibleStream)),
			pageNil:      false,
		},
		{
			// The stream ends before the declared output is produced.
			name:         "output too large",
			data:         stream,
			uncompressed: uint64(len(payload)) + 3,
			compressed:   uint64(len(stream)),
			pageNil:      true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := metadataSingle(t, tc.data)
			_, findings := metadataDB(t, tc.uncompressed, tc.compressed, page)
			if len(findings) != 1 || findings[0].Reason != ReasonMetadataZlibInvalid ||
				(findings[0].PageNumber == nil) != tc.pageNil {
				t.Fatalf("findings %+v", findings)
			}
		})
	}
}

func TestValidateMetadataChainGeometry(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 3000)
	stream := zlibFixture(t, payload)
	cases := []struct {
		name   string
		mutate func([]byte)
		want   ValidationReason
	}{
		{"self link", func(p []byte) { format.PutU32(p[32:36], 2) }, ReasonMetadataLengthInvalid},
		{"meta-page link", func(p []byte) { format.PutU32(p[32:36], 1) }, ReasonMetadataLengthInvalid},
		{"reserved word", func(p []byte) { format.PutU16(p[38:40], 1) }, ReasonMetadataLengthInvalid},
		{"logical offset", func(p []byte) { format.PutU64(p[40:48], 1) }, ReasonMetadataLengthInvalid},
		{"geometry", func(p []byte) { format.PutU16(p[20:22], 50) }, ReasonMetadataLengthInvalid},
		{"item count", func(p []byte) { format.PutU16(p[16:18], 0) }, ReasonMetadataLengthInvalid},
		{"level", func(p []byte) { format.PutU16(p[18:20], 1) }, ReasonMetadataLengthInvalid},
		{"born", func(p []byte) { format.PutU64(p[8:16], 3) }, ReasonPageBornTxnInvalid},
		{"type", func(p []byte) { p[4] = byte(format.PageTypeRetirementLeaf) }, ReasonPageTypeMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := metadataChainPage(t, 2, 0, stream, 0, tc.mutate)
			_, findings := metadataDB(t, uint64(len(payload)), uint64(len(stream)), page)
			if len(findings) != 1 || findings[0].Reason != tc.want || *findings[0].PageNumber != 2 {
				t.Fatalf("findings %+v", findings)
			}
		})
	}
}

func TestValidateMetadataReservedTail(t *testing.T) {
	// A nonzero byte after the chunk payload is the PageReservedNonzero
	// finding; the chain still decodes completely.
	payload := bytes.Repeat([]byte("a"), 3000)
	stream := zlibFixture(t, payload)
	page := metadataChainPage(t, 2, 0, stream, 0, func(p []byte) {
		p[format.MetadataDataOffset+len(stream)] = 1
	})
	_, findings := metadataDB(t, uint64(len(payload)), uint64(len(stream)), page)
	if len(findings) != 1 || findings[0].Reason != ReasonPageReservedNonzero {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMetadataCrcMismatch(t *testing.T) {
	// A broken page checksum refuses the page at the graph read; no
	// zlib finding follows (the chain stops at the refusal).
	payload := bytes.Repeat([]byte("a"), 3000)
	stream := zlibFixture(t, payload)
	page := metadataSingle(t, stream)
	page[format.PageChecksumOffset] ^= 0xFF // stored checksum after sealing
	_, findings := metadataDB(t, uint64(len(payload)), uint64(len(stream)), page)
	if len(findings) != 1 || findings[0].Reason != ReasonPageCrcMismatch || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMetadataChainCycle(t *testing.T) {
	// A two-full-chunk chain whose second page links back to the head:
	// the graph claim reports the cycle on the second visit of page 2.
	// The stream needs three chunks of space, so the generation carries
	// one spare page (page 4 stays unclaimed and forms a partition run
	// after the cycle stops the walk).
	payload := randomBytes(9000)
	stream := storedZlib(payload)
	pageA := metadataChainPage(t, 2, 3, stream[:format.MaxMetadataChunkLen], 0, nil)
	pageB := metadataChainPage(t, 2, 2, stream[format.MaxMetadataChunkLen:2*format.MaxMetadataChunkLen], format.MaxMetadataChunkLen, nil)
	_, findings := metadataDBPages(t, uint64(len(payload)), uint64(len(stream)), 5, pageA, pageB)
	if len(findings) != 2 || findings[0].Reason != ReasonTreeCycle ||
		findings[0].Object != ObjectMetadata || *findings[0].PageNumber != 2 {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMetadataShortStream(t *testing.T) {
	// A one-byte declared stream cannot complete the zlib header: the
	// finish-class finding without a page (the Rust feed consumes the
	// short page and finish reports the incomplete stream).
	page := metadataChainPage(t, 2, 0, []byte{0x78}, 0, nil)
	_, findings := metadataDB(t, 0, 1, page)
	if len(findings) != 1 || findings[0].Reason != ReasonMetadataZlibInvalid || findings[0].PageNumber != nil {
		t.Fatalf("findings %+v", findings)
	}
}

func TestValidateMetadataCheckpointFailure(t *testing.T) {
	// A cancellation during the first chain-page visit must surface as
	// the typed operational failure, never as a zlib finding on page 0
	// (Rust validate_chain propagates the checkpoint with ?).
	payload := randomBytes(3000)
	stream := storedZlib(payload)
	path, _ := metadataDB(t, uint64(len(payload)), uint64(len(stream)), metadataSingle(t, stream))
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	m, err := mapping.MapFile(file, 3*format.PageSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	p0, err := m.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := m.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	result, err := bootstrap.Open(p0, p1, 3*format.PageSize, bootstrap.ModeImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx, err := newContext(m, result.Meta, HeapOnly(1<<20, 1), func() error {
		calls++
		if calls == 1 {
			return &format.Error{Code: format.CodeCancelled, Detail: "checkpoint during the metadata visit"}
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = validateMetadata(ctx)
	var fe *format.Error
	if !errors.As(err, &fe) || fe.Code != format.CodeCancelled {
		t.Fatalf("cause %v, want the cancelled class", err)
	}
	if calls != 1 {
		t.Fatalf("checkpoint calls %d, want 1", calls)
	}
}

func TestValidateMetadataCleanEndWalksTheChain(t *testing.T) {
	// A clean zlib end before the declared chain end: the first page
	// carrying bytes after the stream reports the trailing finding and
	// the remaining chain pages are still validated (Rust feed_chunk
	// keeps walking with the decoder disabled).
	payload := randomBytes(1000)
	stream := storedZlib(payload) // 1011 bytes
	pad := bytes.Repeat([]byte{0xAA}, 4048-len(stream))
	p2 := metadataChainPage(t, 2, 3, append(append([]byte{}, stream...), pad...), 0, nil)
	// The final page passes its own parse but holds a nonzero reserved
	// tail, pinning the post-decoder page validation.
	p3 := metadataChainPage(t, 2, 0, randomBytes(2155), uint64(len(stream)+len(pad)), func(p []byte) {
		p[format.MetadataDataOffset+2155] = 1
	})
	declared := uint64(len(stream) + len(pad) + 2155)
	_, findings := metadataDB(t, 30_000, declared, p2, p3)
	want := []ValidationReason{ReasonMetadataZlibInvalid, ReasonPageReservedNonzero}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v, want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != 2 {
		t.Fatalf("zlib finding page %d, want 2", *findings[0].PageNumber)
	}
	if *findings[1].PageNumber != 3 {
		t.Fatalf("reserved finding page %d, want 3", *findings[1].PageNumber)
	}
}

func TestValidateMetadataCleanEndAtChunkBoundary(t *testing.T) {
	// The zlib stream ends exactly at a chunk boundary: the probe pulls
	// the next chain page and the trailing finding lands on it after
	// its own parse finding — the same page carries both, in the Rust
	// consume_page order (parse first, feed finding second).
	payload := randomBytes(4037)
	stream := storedZlib(payload) // exactly 4048 bytes
	p2 := metadataChainPage(t, 2, 3, stream, 0, nil)
	p3 := metadataChainPage(t, 2, 0, randomBytes(2155), uint64(len(stream)), func(p []byte) {
		p[format.MetadataDataOffset+2155] = 1
	})
	declared := uint64(len(stream) + 2155)
	_, findings := metadataDB(t, 30_000, declared, p2, p3)
	want := []ValidationReason{ReasonPageReservedNonzero, ReasonMetadataZlibInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v, want %v", i, findings[i], reason)
		}
		if findings[i].Object != ObjectMetadata || *findings[i].PageNumber != 3 {
			t.Fatalf("finding %d object/page %+v, want metadata/3", i, findings[i])
		}
	}
}

func TestValidateMetadataOutputOverflowWalksTheChain(t *testing.T) {
	// The stream produces more output than declared while chain pages
	// remain: the overflow finding reports the page being fed and the
	// remaining page is still validated (Rust feed excess arm).
	payload := bytes.Repeat([]byte("a"), 30_000)
	stream := zlibFixture(t, payload) // compressible: far short of 4048
	pad := bytes.Repeat([]byte{0xBB}, 4048-len(stream))
	p2 := metadataChainPage(t, 2, 3, append(append([]byte{}, stream...), pad...), 0, nil)
	p3 := metadataChainPage(t, 3, 0, randomBytes(2155), uint64(len(stream)+len(pad)), nil)
	declared := uint64(len(stream) + len(pad) + 2155)
	_, findings := metadataDB(t, 25_000, declared, p2, p3)
	want := []ValidationReason{ReasonMetadataZlibInvalid, ReasonPageBornTxnInvalid}
	if len(findings) != len(want) {
		t.Fatalf("findings %+v", findings)
	}
	for i, reason := range want {
		if findings[i].Reason != reason {
			t.Fatalf("finding %d: %+v, want %v", i, findings[i], reason)
		}
	}
	if *findings[0].PageNumber != 2 {
		t.Fatalf("zlib finding page %d, want 2", *findings[0].PageNumber)
	}
}
