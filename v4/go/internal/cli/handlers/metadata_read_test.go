package handlers

import (
	"bytes"
	"os"
	"testing"
)

// Platform-neutral metadata-source pins: the read loop, the bounded
// cap, and the class the non-regular refusal carries. These hold on
// every OS (no FIFO and no POSIX-only call is involved), so the
// loop-to-EOF and cap behaviour is not guarded only by the unix-tagged
// FIFO file.

// openRegularFixture returns one regular file descriptor with the given
// content; the caller closes it through cleanup.
func openRegularFixture(t *testing.T, content []byte) (*os.File, func()) {
	t.Helper()
	path := t.TempDir() + "/fixture"
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	return file, func() { _ = file.Close() }
}

// readMetadataFile commits exactly the bytes of the file it opened:
// neither a fabricated NUL tail nor a truncation may be committed as
// valid metadata.
func TestReadMetadataFileCommitsExactBytes(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"plain":   []byte("tenant-a"),
		"nul":     []byte{0x00, 0x01, 0x00, 0xff, 0x00},
		"long":    bytes.Repeat([]byte("ab\ncd"), 5000),
		"onebyte": []byte{0x7f},
	}
	for name, content := range cases {
		path := dir + "/meta-" + name
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		got, herr := readMetadataFile(path)
		if herr != nil {
			t.Fatalf("%s: %v", name, herr)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("%s: committed %d bytes, want the %d bytes read", name, len(got), len(content))
		}
	}
}

// readMetadataBounded takes its length from read(2), never from
// st_size: procfs and sysfs sources report a size the read does not
// honour (a zero st_size with real content, or a page-sized st_size with
// less content), so a stat-sized buffer would fabricate a NUL tail or
// truncate the source. observed is the opened descriptor's size and only
// seeds the capacity.
func TestReadMetadataBoundedIgnoresObservedSize(t *testing.T) {
	const content = "65536\n"
	file, cleanup := openRegularFixture(t, []byte(content))
	defer cleanup()
	got, herr := readMetadataBounded(file, 0, 1<<20)
	if herr != nil {
		t.Fatalf("bounded read: %v", herr)
	}
	if string(got) != content {
		t.Fatalf("observed 0: committed %q, want the exact bytes read", got)
	}
	file2, cleanup2 := openRegularFixture(t, []byte(content))
	defer cleanup2()
	got, herr = readMetadataBounded(file2, 4096, 1<<20)
	if herr != nil {
		t.Fatalf("bounded read: %v", herr)
	}
	if string(got) != content {
		t.Fatalf("observed 4096: committed %d bytes, want %d", len(got), len(content))
	}
}

// The 20 MiB cap is enforced against the bytes actually read: a source
// that reports a small st_size and then yields more than the limit is
// refused with the io class (Rust read_bounded's other arm), and no
// partial content is committed.
func TestReadMetadataBoundedCapCountsBytesRead(t *testing.T) {
	file, cleanup := openRegularFixture(t, bytes.Repeat([]byte("x"), 4096))
	defer cleanup()
	got, herr := readMetadataBounded(file, 0, 1024)
	if got != nil {
		t.Fatalf("over-cap read committed %d bytes", len(got))
	}
	if herr == nil || herr.Code != "io" {
		t.Fatalf("readMetadataBounded over cap = %+v, want io", herr)
	}
}

// A real procfs source whose st_size is zero while read(2) yields
// content is the deterministic detector for the stat-sized-buffer
// defect: committing st_size bytes would commit an empty metadata blob.
func TestReadMetadataFileProcfsZeroSizeCommitsContent(t *testing.T) {
	const path = "/proc/self/cmdline"
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("%s unavailable: %v", path, err)
	}
	if info.Size() != 0 {
		t.Skipf("%s reports st_size %d, this pin needs the zero-size shape", path, info.Size())
	}
	want, err := os.ReadFile(path)
	if err != nil || len(want) == 0 {
		t.Skipf("%s unreadable or empty: %v", path, err)
	}
	got, herr := readMetadataFile(path)
	if herr != nil {
		t.Fatalf("readMetadataFile(%s): %v", path, herr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: committed %d bytes, want the %d bytes read", path, len(got), len(want))
	}
}

// A sysfs source whose st_size is a whole page while read(2) yields only
// a few bytes is the other half of the stat-sized-buffer defect: sizing
// the commit from st_size publishes a fabricated NUL tail as valid
// metadata. The committed bytes must be exactly what read(2) returned.
func TestReadMetadataFileSysfsOversizedStSizeCommitsNoNulTail(t *testing.T) {
	const path = "/sys/class/net/lo/mtu"
	want, err := os.ReadFile(path)
	if err != nil || len(want) == 0 {
		t.Skipf("%s unavailable: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Skipf("%s unusable: %v", path, err)
	}
	if info.Size() <= int64(len(want)) {
		t.Skipf("%s reports st_size %d, this pin needs the oversized-stat shape", path, info.Size())
	}
	got, herr := readMetadataFile(path)
	if herr != nil {
		t.Fatalf("readMetadataFile(%s): %v", path, herr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: committed %d bytes (%q), want the %d bytes read", path, len(got), got, len(want))
	}
}

// The pre-open refusal and the authoritative post-open refusal must be
// the same answer on a node that is not a regular file for a reason
// every platform can produce without a FIFO: a directory. This pins the
// class mapping (invalid_path/not_started, and the shared metadata
// refusal for the opened-descriptor arm) off the POSIX surface.
func TestReadMetadataFileNonRegularDirectoryClassMapping(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/subdir"
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	standing, herr := readMetadataFile(path)
	if standing != nil {
		t.Fatal("readMetadataFile returned content with a refusal")
	}
	if herr == nil || herr.Code != "invalid_path" || herr.Outcome != "not_started" {
		t.Fatalf("readMetadataFile(directory) = %+v, want invalid_path/not_started", herr)
	}
	mapped := metadataSourceNotRegular(path)
	if mapped.Code != herr.Code || mapped.Outcome != herr.Outcome {
		t.Fatalf("post-open refusal %+v differs from pre-open refusal %+v", mapped, herr)
	}
	// The opened-descriptor check refuses the same node, whichever
	// platform arm produced it.
	file, err := openMetadataSourceNoBlock(path)
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("openMetadataSourceNoBlock accepted a directory")
	}
}
