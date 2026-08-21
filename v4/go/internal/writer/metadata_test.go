package writer

// Draft metadata edit tests (Rust draft_store set_metadata / clear_metadata
// over the real mapped file, SOW-0025 chunk-6 design record D4): bounded
// zlib compression with the stored-zlib fallback, the forward chunk chain
// written at final offsets, base-chain retirement, one stage per
// transaction, and exact read-back through the immutable reader.

import (
	"bytes"
	"compress/flate"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// commitMetadata stages one metadata replacement on a fresh draft of path
// and publishes it, returning the committed reader.
func commitMetadata(t *testing.T, path string, payload []byte, budget PageBudget) *reader.ImmutableReader {
	t.Helper()
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetMetadata(payload); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireDraftLength(); err != nil {
		t.Fatal(err)
	}
	res := c.Publish(nil)
	if res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// TestMetadataSetCommitReadBack stores a representative JSON payload,
// commits it, and reads it back byte-exact with the immutable reader.
func TestMetadataSetCommitReadBack(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	payload := []byte(`{"fixture":"go-metadata-roundtrip","producer":"go","items":[1,2,3],"nested":{"a":"b"}}`)
	r := commitMetadata(t, path, payload, testBudget())
	got, present, err := r.ReadMetadataJSON()
	if err != nil || !present || !bytes.Equal(got, payload) {
		t.Fatalf("metadata = %q present %v err %v, want exact payload present", got, present, err)
	}
	m := r.Meta()
	if m.MetadataUncompressed != uint64(len(payload)) {
		t.Fatalf("uncompressed len = %d, want %d", m.MetadataUncompressed, len(payload))
	}
	if m.MetadataCompressed == 0 || m.MetadataCompressed > format.MetadataCompressedBound(uint64(len(payload))) {
		t.Fatalf("compressed len = %d outside the declared bound", m.MetadataCompressed)
	}
}

// TestMetadataEmptyPayloadIsPresentEmpty verifies the exact empty-payload
// state: an empty non-nil payload commits as present with zero bytes
// (binary-format-v4.md section 11).
func TestMetadataEmptyPayloadIsPresentEmpty(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	r := commitMetadata(t, path, []byte{}, testBudget())
	got, present, err := r.ReadMetadataJSON()
	if err != nil || !present || len(got) != 0 {
		t.Fatalf("empty metadata = %q present %v err %v, want present empty", got, present, err)
	}
}

// TestMetadataClearStagesAbsence commits one payload, then clears it in a
// second transaction and reads the absent state back.
func TestMetadataClearStagesAbsence(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	commitMetadata(t, path, []byte(`{"once":true}`), testBudget()).Close()

	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	if changed, err := c.ClearMetadata(); err != nil || !changed {
		t.Fatalf("clear on present metadata = changed %v err %v, want true/nil", changed, err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v)", res.Status, res.Err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, present, err := r.ReadMetadataJSON(); err != nil || present {
		t.Fatalf("cleared metadata = present %v err %v, want absent", present, err)
	}
}

// TestMetadataClearOnAbsentIsNoOp verifies clear on an already-absent
// database reports false without staging anything (Rust clear_metadata).
func TestMetadataClearOnAbsentIsNoOp(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	if changed, err := c.ClearMetadata(); err != nil || changed {
		t.Fatalf("clear on absent metadata = changed %v err %v, want false/nil", changed, err)
	}
	if c.Draft().Changed() {
		t.Fatal("no-op clear marked the draft changed")
	}
}

// TestMetadataOneStagePerTransaction verifies the require_metadata_available
// gate: the second metadata stage in one transaction is refused with
// WrongState, and the first stage survives for commit.
func TestMetadataOneStagePerTransaction(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetMetadata([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetMetadata([]byte(`{"b":2}`)); !isCode(err, format.CodeWrongState) {
		t.Fatalf("second set err = %v, want CodeWrongState", err)
	}
	if _, err := c.ClearMetadata(); !isCode(err, format.CodeWrongState) {
		t.Fatalf("clear after set err = %v, want CodeWrongState", err)
	}
	// The refused stages must not corrupt the staged one: the committed
	// payload is still the first set.
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v)", res.Status, res.Err)
	}
}

// TestMetadataOver20MiBRefused verifies the 20 MiB cap and the heap
// budget: oversized payloads and over-budget compression heaps are refused
// before any page is touched.
func TestMetadataOver20MiBRefused(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, format.MaxMetadataUncompressed+1)
	if _, err := c.SetMetadata(big); !isCode(err, format.CodeInvalidArgument) {
		t.Fatalf("oversized set err = %v, want CodeInvalidArgument", err)
	}
	if c.Draft().Changed() {
		t.Fatal("refused oversized set marked the draft changed")
	}
	// Release the writer before re-opening with the starved budget: the
	// exclusive lifetime lock blocks a second writer open.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	// A compression heap below the declared bound is refused with
	// InsufficientResourceBudget (Rust compress: bound > max_heap_bytes).
	hungry := make([]byte, 1024)
	bound := format.MetadataCompressedBound(uint64(len(hungry)))
	core, err := Open(path, PageBudget{MaxHeapBytes: bound - 1, MaxPrivatePages: 4096, MaxGrowthPages: 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if err := core.BeginDraft(); err != nil {
		t.Fatal(err)
	}
	if _, err := core.SetMetadata(hungry); !isCode(err, format.CodeInsufficientResourceBudget) {
		t.Fatalf("under-budget set err = %v, want CodeInsufficientResourceBudget", err)
	}
}

// TestMetadataStoredZlibFallback forces the stored-zlib encoding with a
// heap budget that excludes the deflate workspace and verifies the exact
// byte-exact roundtrip plus the deterministic stored bound (Rust
// encode_stored_zlib).
func TestMetadataStoredZlibFallback(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	payload := bytes.Repeat([]byte("stored-zlib-fallback-payload-0123456789"), 4096)
	bound := format.MetadataCompressedBound(uint64(len(payload)))
	budget := PageBudget{MaxHeapBytes: bound, MaxPrivatePages: 4096, MaxGrowthPages: 4096}
	r := commitMetadata(t, path, payload, budget)
	got, present, err := r.ReadMetadataJSON()
	if err != nil || !present || !bytes.Equal(got, payload) {
		t.Fatalf("fallback metadata mismatch: present %v err %v len %d want %d", present, err, len(got), len(payload))
	}
	// The stored encoding is deterministic: the committed compressed
	// length equals the exact stored-zlib length (header 2 + one stored
	// block header per 65535-byte input chunk + Adler-32 4).
	want := format.MetadataCompressedBound(uint64(len(payload)))
	if got2 := r.Meta().MetadataCompressed; got2 != want {
		t.Fatalf("stored compressed len = %d, want exact bound %d", got2, want)
	}
}

// TestMetadataDeflatePathSmallPayload verifies the deflate path commits a
// stream strictly smaller than the stored bound for repetitive payloads
// and reads back byte-exact.
func TestMetadataDeflatePathSmallPayload(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	payload := bytes.Repeat([]byte("deflate-me-"), 1<<14)
	r := commitMetadata(t, path, payload, testBudget())
	got, present, err := r.ReadMetadataJSON()
	if err != nil || !present || !bytes.Equal(got, payload) {
		t.Fatalf("deflate metadata mismatch: present %v err %v", present, err)
	}
	if m := r.Meta(); m.MetadataCompressed >= format.MetadataCompressedBound(uint64(len(payload))) {
		t.Fatalf("deflate compressed len %d not smaller than the stored bound %d", m.MetadataCompressed, format.MetadataCompressedBound(uint64(len(payload))))
	}
}

// TestMetadataMultiPageChain stores a payload that spans many chunk pages
// and verifies the forward chain reads back byte-exact (Rust write_chain /
// collect_pages across the fixed bound).
func TestMetadataMultiPageChain(t *testing.T) {
	path, _ := createDirect(t, format.AddressFamilyIPv4)
	payload := bytes.Repeat([]byte{0x41, 0x42, 0x43}, 1<<19) // 1.5 MiB -> ~380 chunk pages
	bound := format.MetadataCompressedBound(uint64(len(payload)))
	r := commitMetadata(t, path, payload, PageBudget{MaxHeapBytes: bound, MaxPrivatePages: 1 << 16, MaxGrowthPages: 1 << 16})
	got, present, err := r.ReadMetadataJSON()
	if err != nil || !present || !bytes.Equal(got, payload) {
		t.Fatalf("multi-page metadata mismatch: present %v err %v len %d want %d", present, err, len(got), len(payload))
	}
}

// TestMetadataDeflateHeapOverheadCoversWorkspace pins the honest deflate
// heap charge: compress/flate at DefaultCompression keeps a pinned
// workspace (~0.8 MiB) far larger than the Rust miniz backend's 512 KiB,
// so the Go charge must be measured, not mirrored. GC is disabled while
// measuring so every compressor buffer is still live at the second
// sample; the peak across payload sizes must fit inside the declared
// overhead, and a sanity floor stops a broken measurement from passing
// vacuously (Rust metadata.rs: "allocation tests enforce it").
func TestMetadataDeflateHeapOverheadCoversWorkspace(t *testing.T) {
	if raceEnabled {
		t.Skip("race shadow memory inflates HeapAlloc; the charge pins the production workspace")
	}
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)

	var peak uint64
	for _, n := range []int{1 << 10, 1 << 20, 8 << 20, format.MaxMetadataUncompressed} {
		input := bytes.Repeat([]byte("deflate-workspace-probe-0123456789"), (n+33)/34)
		input = input[:n]
		bound := format.MetadataCompressedBound(uint64(n))

		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		base := ms.HeapAlloc

		var out bytes.Buffer
		out.Grow(int(bound))
		out.Write([]byte{0x78, 0x01})
		enc, err := flate.NewWriter(&out, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
		written, err := enc.Write(input)
		if err != nil || written != len(input) {
			t.Fatalf("flate write n=%d err=%v, want full input", written, err)
		}
		_ = enc // keep the pinned workspace live until the sample
		runtime.ReadMemStats(&ms)
		workspace := ms.HeapAlloc - base - uint64(bound)
		if workspace > peak {
			peak = workspace
		}
		if err := enc.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if peak < 700*1024 {
		t.Fatalf("deflate workspace measured %d bytes: measurement broken or the stdlib shrank", peak)
	}
	if peak > deflateHeapOverhead {
		t.Fatalf("deflate workspace %d bytes exceeds declared overhead %d: the honest charge must cover it", peak, deflateHeapOverhead)
	}
}
