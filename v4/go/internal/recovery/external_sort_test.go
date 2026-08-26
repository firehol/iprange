package recovery

// External-sort wire vectors (Rust recovery/external_sort.rs +
// external_sort/streams.rs): every run is the 16-byte "IPR4RUN1"
// header plus a little-endian u64 count, and every scratch record is
// the fixed 12-byte IPv4 shape (from u32le, to u32le, value u32le).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestScratchRunFramingMatchesTheRustWireShape pins the exact run
// header and scratch-record bytes through the real writeRun/readRun
// path: "IPR4RUN1" + count u64le, then from/to/value u32le records.
func TestScratchRunFramingMatchesTheRustWireShape(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatalf("scratchStart: %v", err)
	}
	slot, err := scratch.create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	codec := rangeV4Codec{}
	records := []rangeRecord{
		{from: rangeKey{hi: 0x01020304}, to: rangeKey{hi: 0x05060708}, value: 0x0a0b0c0d},
		{from: rangeKey{hi: 0x11121314}, to: rangeKey{hi: 0x15161718}, value: 0x1a1b1c1d},
	}
	end, err := writeRun(newSortWorkspace(), scratch, codec, slot, scratchHeaderSize, records)
	if err != nil {
		t.Fatalf("writeRun: %v", err)
	}
	raw := make([]byte, int(end-scratchHeaderSize))
	if err := scratch.read(slot, scratchHeaderSize, raw); err != nil {
		t.Fatalf("read: %v", err)
	}
	wantHeader := make([]byte, 16)
	copy(wantHeader[0:8], "IPR4RUN1")
	format.PutU64(wantHeader[8:16], 2)
	wantRecords := []byte{
		0x04, 0x03, 0x02, 0x01, 0x08, 0x07, 0x06, 0x05, 0x0d, 0x0c, 0x0b, 0x0a,
		0x14, 0x13, 0x12, 0x11, 0x18, 0x17, 0x16, 0x15, 0x1d, 0x1c, 0x1b, 0x1a,
	}
	want := append(wantHeader, wantRecords...)
	if string(raw) != string(want) {
		t.Fatalf("run bytes mismatch\n got %x\nwant %x", raw, want)
	}
	run, err := readRun(scratch, codec, slot, scratchHeaderSize)
	if err != nil {
		t.Fatalf("readRun: %v", err)
	}
	if run.count != 2 || run.recordsAt != scratchHeaderSize+16 || run.end != scratchHeaderSize+16+24 {
		t.Fatalf("decoded run %+v, want count 2 at records 16 end 40", run)
	}

	// The scratch directory must be left empty by the cleanup.
	cleanup := scratch.cleanup()
	if !cleanup.clean() {
		t.Fatal("cleanup left residues")
	}
	entries, err := os.ReadDir(filepath.Join(directory))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory left %d entries, want 0", len(entries))
	}
}

// TestScratchRunV6RecordFramingMatchesTheRustWireShape pins the v6
// scratch-record bytes through the real writeRun/readRun path: each
// IPv6 key is one little-endian u128 (low limb first, Rust
// Ipv6Key::write_le), so the 36-byte record is lo/hi/lo/hi u64le
// limbs followed by the u32le value.
func TestScratchRunV6RecordFramingMatchesTheRustWireShape(t *testing.T) {
	directory := t.TempDir()
	scratch, err := scratchStart(directory, scratchTestMeta(), 4096, 2, 4)
	if err != nil {
		t.Fatalf("scratchStart: %v", err)
	}
	slot, err := scratch.create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	codec := rangeV6Codec{}
	records := []rangeRecord{
		{from: rangeKey{hi: 0x0102030405060708, lo: 0x090a0b0c0d0e0f10}, to: rangeKey{hi: 0x1112131415161718, lo: 0x191a1b1c1d1e1f20}, value: 0x2a2b2c2d},
	}
	end, err := writeRun(newSortWorkspace(), scratch, codec, slot, scratchHeaderSize, records)
	if err != nil {
		t.Fatalf("writeRun: %v", err)
	}
	raw := make([]byte, int(end-scratchHeaderSize))
	if err := scratch.read(slot, scratchHeaderSize, raw); err != nil {
		t.Fatalf("read: %v", err)
	}
	wantHeader := make([]byte, 16)
	copy(wantHeader[0:8], "IPR4RUN1")
	format.PutU64(wantHeader[8:16], 1)
	wantRecord := make([]byte, 36)
	format.PutU64(wantRecord[0:8], 0x090a0b0c0d0e0f10)   // from.lo
	format.PutU64(wantRecord[8:16], 0x0102030405060708)  // from.hi
	format.PutU64(wantRecord[16:24], 0x191a1b1c1d1e1f20) // to.lo
	format.PutU64(wantRecord[24:32], 0x1112131415161718) // to.hi
	format.PutU32(wantRecord[32:36], 0x2a2b2c2d)
	want := append(wantHeader, wantRecord...)
	if string(raw) != string(want) {
		t.Fatalf("v6 run bytes mismatch\n got %x\nwant %x", raw, want)
	}
	run, err := readRun(scratch, codec, slot, scratchHeaderSize)
	if err != nil {
		t.Fatalf("readRun: %v", err)
	}
	if run.count != 1 || run.end != scratchHeaderSize+16+36 {
		t.Fatalf("decoded run %+v, want count 1 end %d", run, scratchHeaderSize+16+36)
	}
	cleanup := scratch.cleanup()
	if !cleanup.clean() {
		t.Fatal("cleanup left residues")
	}
}
