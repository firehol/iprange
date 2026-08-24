//go:build linux

package mapping

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

var shrinkTestDBID = [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
var shrinkTestNonce = [16]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20}

// marker is exactly 16 bytes so it fills a 16-byte slice without a trailing
// zero byte (a 15-byte marker would leave byte 16 zero and break equality).
const marker = "shrink-survives!"

// makeValidDB creates a minimal valid empty direct database: two identical
// meta pages with txn 1, page count 2 (proven-current parity page 1).
func makeValidDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "valid.iprdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		page := make([]byte, format.PageSize)
		copy(page[0:8], format.MainMagic[:])
		format.PutU16(page[8:10], format.MetaSize)
		page[10] = format.PageShift
		page[11] = format.AddressFamilyIPv4
		page[12] = format.ValueKindDirect
		copy(page[16:32], "direct\x00")
		copy(page[32:48], shrinkTestDBID[:])
		format.PutU64(page[48:56], 1)
		copy(page[56:72], shrinkTestNonce[:])
		format.PutU64(page[72:80], 2)
		format.PutU32(page[252:256], format.MetaCRC32C(page))
		if _, err := f.Write(page); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// TestShrinkTruncatesFileAndMapping pins the shrink-or-retain contract:
// the file and the mapping both land at the requested extent, the physical
// tracking follows, bytes inside the committed region survive, and page
// views beyond the new extent are refused.
func TestShrinkTruncatesFileAndMapping(t *testing.T) {
	dir := t.TempDir()
	path := makePagesFile(t, dir, 6)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Remap(6 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	// Write a marker inside the region that survives the shrink.
	page1, err := m.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	copy(page1[3000:3016], marker)
	if err := m.Shrink(4 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if m.Size() != 4*format.PageSize || m.PhysicalSize() != 4*format.PageSize {
		t.Fatalf("size %d physical %d, want 4 pages", m.Size(), m.PhysicalSize())
	}
	if fileSize(t, path) != 4*format.PageSize {
		t.Fatalf("file size %d, want 4 pages", fileSize(t, path))
	}
	// The surviving marker is still visible through the new mapping.
	page1, err = m.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	if string(page1[3000:3016]) != marker {
		t.Fatal("marker lost after shrink")
	}
	// Page 4 (beyond the new extent) is refused; page 3 is the last page.
	if _, err := m.Page(4); err == nil {
		t.Fatal("page beyond shrunk extent accepted")
	}
	if _, err := m.Page(3); err != nil {
		t.Fatal("last committed page refused:", err)
	}
	// A fresh mutable open sees the truncated file and can grow again.
	m.Close()
	m2, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if m2.PhysicalSize() != 4*format.PageSize {
		t.Fatalf("reopen physical %d, want 4 pages", m2.PhysicalSize())
	}
	if err := m2.Grow(5 * format.PageSize); err != nil {
		t.Fatal("grow after shrink:", err)
	}
	if fileSize(t, path) != 5*format.PageSize {
		t.Fatalf("file size after grow %d, want 5 pages", fileSize(t, path))
	}
}

// TestShrinkNoOp pins the same-extent fast path: shrinking to the current
// mapped and physical extent changes nothing.
func TestShrinkNoOp(t *testing.T) {
	dir := t.TempDir()
	path := makeValidDB(t, dir)

	m, err := OpenMutable(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.Size() != 2*format.PageSize || m.PhysicalSize() != 2*format.PageSize {
		t.Fatalf("size %d physical %d, want 2 pages", m.Size(), m.PhysicalSize())
	}
	if err := m.Shrink(2 * format.PageSize); err != nil {
		t.Fatal(err)
	}
	if m.Size() != 2*format.PageSize || m.PhysicalSize() != 2*format.PageSize {
		t.Fatalf("no-op changed state: size %d physical %d", m.Size(), m.PhysicalSize())
	}
}

// TestShrinkRefusals pins the refusal classes: shrink above the physical
// extent (Corrupt class, FormatInvalid), unaligned requests, read-only
// mappings, and closed mappings.
func TestShrinkRefusals(t *testing.T) {
	t.Run("above-physical", func(t *testing.T) {
		dir := t.TempDir()
		m, err := OpenMutable(makePagesFile(t, dir, 4), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		if err := m.Remap(4 * format.PageSize); err != nil {
			t.Fatal(err)
		}
		err = m.Shrink(6 * format.PageSize)
		var ferr *format.Error
		if !errors.As(err, &ferr) || ferr.Code != format.CodeFormatInvalid {
			t.Fatalf("error %v, want FormatInvalid", err)
		}
	})
	t.Run("unaligned", func(t *testing.T) {
		dir := t.TempDir()
		m, err := OpenMutable(makePagesFile(t, dir, 4), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		err = m.Shrink(4*format.PageSize + 100)
		var ferr *format.Error
		if !errors.As(err, &ferr) || ferr.Code != format.CodeFormatInvalid {
			t.Fatalf("error %v, want FormatInvalid", err)
		}
	})
	t.Run("read-only", func(t *testing.T) {
		dir := t.TempDir()
		m, err := OpenImmutable(makeValidDB(t, dir), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		err = m.Shrink(2 * format.PageSize)
		var ferr *format.Error
		if !errors.As(err, &ferr) || ferr.Code != format.CodeWrongState {
			t.Fatalf("error %v, want WrongState", err)
		}
	})
	t.Run("closed", func(t *testing.T) {
		dir := t.TempDir()
		m, err := OpenMutable(makePagesFile(t, dir, 4), nil)
		if err != nil {
			t.Fatal(err)
		}
		m.Close()
		err = m.Shrink(2 * format.PageSize)
		var ferr *format.Error
		if !errors.As(err, &ferr) || ferr.Code != format.CodeWrongState {
			t.Fatalf("error %v, want WrongState", err)
		}
	})
}
