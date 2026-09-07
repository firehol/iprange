package fileio

import (
	"bufio"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func opt4(prefix uint32, fix bool) TextInputOptions {
	return TextInputOptions{Family: AddressFamilyInputIPv4, FixNetwork: fix, DefaultPrefix: prefix, DNSThreads: 1, DNSSilent: true, MaxLineBytes: 1_048_576}
}

func opt6(prefix uint32, fix bool) TextInputOptions {
	return TextInputOptions{Family: AddressFamilyInputIPv6, FixNetwork: fix, DefaultPrefix: prefix, DNSThreads: 1, DNSSilent: true, MaxLineBytes: 1_048_576}
}

func v6str(value parsedRange) string {
	var from [16]byte
	var to [16]byte
	from[0] = byte(value.fromHi >> 56)
	from[1] = byte(value.fromHi >> 48)
	from[2] = byte(value.fromHi >> 40)
	from[3] = byte(value.fromHi >> 32)
	from[4] = byte(value.fromHi >> 24)
	from[5] = byte(value.fromHi >> 16)
	from[6] = byte(value.fromHi >> 8)
	from[7] = byte(value.fromHi)
	from[8] = byte(value.fromLo >> 56)
	from[9] = byte(value.fromLo >> 48)
	from[10] = byte(value.fromLo >> 40)
	from[11] = byte(value.fromLo >> 32)
	from[12] = byte(value.fromLo >> 24)
	from[13] = byte(value.fromLo >> 16)
	from[14] = byte(value.fromLo >> 8)
	from[15] = byte(value.fromLo)
	to[0] = byte(value.toHi >> 56)
	to[1] = byte(value.toHi >> 48)
	to[2] = byte(value.toHi >> 40)
	to[3] = byte(value.toHi >> 32)
	to[4] = byte(value.toHi >> 24)
	to[5] = byte(value.toHi >> 16)
	to[6] = byte(value.toHi >> 8)
	to[7] = byte(value.toHi)
	to[8] = byte(value.toLo >> 56)
	to[9] = byte(value.toLo >> 48)
	to[10] = byte(value.toLo >> 40)
	to[11] = byte(value.toLo >> 32)
	to[12] = byte(value.toLo >> 24)
	to[13] = byte(value.toLo >> 16)
	to[14] = byte(value.toLo >> 8)
	to[15] = byte(value.toLo)
	return netip.AddrFrom16(from).String() + " " + netip.AddrFrom16(to).String()
}

func TestScratchV4Forms(t *testing.T) {
	cases := []struct {
		line string
		from uint32
		to   uint32
	}{
		{"1.2.3.4", 0x01020304, 0x01020304},
		{"10.0.0.7/24", 0x0a000000, 0x0a0000ff},
		{"10.0.0.7/255.255.255.0", 0x0a000000, 0x0a0000ff},
		{"10.0.0.10 - 10.0.0.8", 0x0a000008, 0x0a00000a},
		{"10.0.0.0/29 - 10.0.0.8/31", 0x0a000000, 0x0a000009},
		{"  010.0.0.1 # comment\r", 0x08000001, 0x08000001},
		{"10.3", 0x0a000003, 0x0a000003},
		{"::ffff:1.2.3.4", 0x01020304, 0x01020304},
		{"1.2.3.4#comment", 0x01020304, 0x01020304},
	}
	for _, c := range cases {
		parsed, err := parseTextLine([]byte(c.line), opt4(32, true))
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if parsed.kind != parsedRangeLine {
			t.Fatalf("%q: kind %v", c.line, parsed.kind)
		}
		if parsed.value.fromLo != uint64(c.from) || parsed.value.toLo != uint64(c.to) || !parsed.value.ipv4 {
			t.Fatalf("%q: got %x-%x ipv4=%v", c.line, parsed.value.fromLo, parsed.value.toLo, parsed.value.ipv4)
		}
	}
	if parsed, err := parseTextLine([]byte("# comment"), opt4(32, true)); err != nil || parsed.kind != parsedEmpty {
		t.Fatalf("comment: %v %v", parsed.kind, err)
	}
	if parsed, err := parseTextLine([]byte("2001:db8::1"), opt4(32, true)); err != nil || parsed.kind != parsedDroppedIPv6 {
		t.Fatalf("dropped: %v %v", parsed.kind, err)
	}
	if _, err := parseTextLine([]byte("1.2.3.999"), opt4(32, true)); err == nil {
		t.Fatal("1.2.3.999 must error")
	}
}

func TestScratchNetworkFixing(t *testing.T) {
	parsed, err := parseTextLine([]byte("1.2.3.5/30"), opt4(32, false))
	if err != nil || parsed.value.fromLo != 0x01020305 || parsed.value.toLo != 0x01020307 {
		t.Fatalf("no-fix: %+v %v", parsed, err)
	}
	parsed, err = parseTextLine([]byte("1.2.3.5"), opt4(30, true))
	if err != nil || parsed.value.fromLo != 0x01020304 || parsed.value.toLo != 0x01020307 {
		t.Fatalf("prefix30: %+v %v", parsed, err)
	}
	parsed, err = parseTextLine([]byte("2001:db8:0:1:0:0:0:1"), opt6(64, true))
	if err != nil {
		t.Fatal(err)
	}
	want := "2001:db8:0:1:: 2001:db8:0:1:ffff:ffff:ffff:ffff"
	if got := v6str(parsed.value); got != want {
		t.Fatalf("network v6: got %s want %s", got, want)
	}
}

func TestScratchV6Mapped(t *testing.T) {
	parsed, err := parseTextLine([]byte("10.0.0.1"), opt6(128, true))
	if err != nil || parsed.value.ipv4 || parsed.value.fromHi != 0xffff || parsed.value.fromLo != 0x0a000001 {
		t.Fatalf("mapped: %+v %v", parsed, err)
	}
	parsed, err = parseTextLine([]byte("2001:db8::10 - 2001:db8::1"), opt6(128, true))
	if err != nil {
		t.Fatal(err)
	}
	if got := v6str(parsed.value); got != "2001:db8::1 2001:db8::10" {
		t.Fatalf("range: %s", got)
	}
	if _, err := parseTextLine([]byte("10.0.0.1 - 2001:db8::1"), opt6(128, true)); err == nil {
		t.Fatal("mixed family must error")
	}
}

func TestScratchHostname(t *testing.T) {
	parsed, err := parseTextLine([]byte("host.example"), opt4(32, true))
	if err != nil || parsed.kind != parsedHostname {
		t.Fatalf("hostname: %+v %v", parsed, err)
	}
	if _, err := parseTextLine([]byte("host.example - other"), opt4(32, true)); err == nil {
		t.Fatal("range over hostname must error")
	}
}

func TestScratchLineBound(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader("12345\n"), 16)
	var out []byte
	if _, _, err := readLimitedLine(reader, 4, &out); err == nil || err.Code() != "input_format" {
		t.Fatalf("bound: %v", err)
	}
}

func TestScratchBinaryV4(t *testing.T) {
	var bytes []byte
	bytes = append(bytes, []byte("iprange binary format v1.0\noptimized\nrecord size 8\nrecords 2\nbytes 20\nlines 2\nunique ips 3\n")...)
	bytes = append(bytes, 0x4d, 0x3c, 0x2b, 0x1a) // 0x1a2b3c4d little endian
	bytes = append(bytes, 1, 0, 0, 0, 2, 0, 0, 0)
	bytes = append(bytes, 5, 0, 0, 0, 5, 0, 0, 0)
	path := filepath.Join(t.TempDir(), "in.bin")
	if err := os.WriteFile(path, bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewTextInputSource4([]string{path}, opt4(32, true), true, 10)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.NextBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || uint32(batch[0].From) != 1 || uint32(batch[0].To) != 2 || uint32(batch[1].From) != 5 {
		t.Fatalf("binary batch: %+v", batch)
	}
	if batch, err := source.NextBatch(); err != nil || batch != nil {
		t.Fatalf("end: %v %v", batch, err)
	}
}

func TestScratchAtExpansion(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "01.txt")
	f2 := filepath.Join(dir, "10.txt")
	_ = os.WriteFile(f1, []byte("1.2.3.4\n"), 0o644)
	_ = os.WriteFile(f2, []byte("5.6.7.8\n"), 0o644)
	_, err := expandPaths([]string{"@" + dir}, true, 1, 1_048_576)
	var inputErr *InputError
	if !errors.As(err, &inputErr) || inputErr.Code() != "invalid_path" {
		t.Fatalf("bound: %v", err)
	}
	expanded, err := expandPaths([]string{"@" + dir}, true, 2, 1_048_576)
	if err != nil || len(expanded) != 2 {
		t.Fatalf("expand: %v %v", expanded, err)
	}
}

// TestScratchAtExpansionRawPathThroughSymlinkParent pins the
// @-directory expansion over a raw referenced spelling: entry paths
// must be built by raw concatenation (Rust read_dir entry.path()),
// never by filepath.Join, which lexically cleans a symlinked
// intermediate plus ".." and would refuse the kernel-resolved
// directory or ingest a different one.  POSIX kernel-resolution
// class; on Windows both products pass the identical raw spelling to
// the same kernel, so parity cannot diverge there.
func TestScratchAtExpansionRawPathThroughSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX kernel-resolution class")
	}
	root := t.TempDir()
	for _, d := range []string{"real", "work", "realdir"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(root, "realdir", "01.txt"), []byte("1.2.3.4\n"), 0o644)
	if err := os.Symlink("../real", filepath.Join(root, "work", "jump")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sep := string(os.PathSeparator)
	// Kernel resolution: work/jump -> root/real, then ".." -> root, so
	// the referenced spelling names root/realdir.  filepath.Join on the
	// referenced spelling would instead name root/work/realdir.
	referenced := root + sep + "work" + sep + "jump" + sep + ".." + sep + "realdir"
	expanded, err := expandPaths([]string{"@" + referenced}, true, 2, 1_048_576)
	if err != nil {
		t.Fatalf("expand raw referenced dir: %v", err)
	}
	want := referenced + sep + "01.txt"
	if len(expanded) != 1 || expanded[0] != want {
		t.Fatalf("expanded = %v, want [%s]", expanded, want)
	}
}

// TestScratchAtExpansionTrailingSeparator pins the @-directory
// expansion separator rule: entry paths are built like Rust read_dir
// entry.path() (PathBuf push), which inserts a separator only when
// the referenced spelling does not already end with one, so a
// trailing-separator "@<dir>/" spelling never doubles the separator.
func TestScratchAtExpansionTrailingSeparator(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "01.txt"), []byte("1.2.3.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathSeparator)
	plain, err := expandPaths([]string{"@" + root}, true, 2, 1_048_576)
	if err != nil || len(plain) != 1 || plain[0] != root+sep+"01.txt" {
		t.Fatalf("plain expansion = %v (err %v), want [%s]", plain, err, root+sep+"01.txt")
	}
	trailing, err := expandPaths([]string{"@" + root + sep}, true, 2, 1_048_576)
	if err != nil || len(trailing) != 1 || trailing[0] != root+sep+"01.txt" {
		t.Fatalf("trailing-separator expansion = %v (err %v), want [%s]", trailing, err, root+sep+"01.txt")
	}
}

func TestScratchEndToEndV6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.txt")
	_ = os.WriteFile(path, []byte("# comment\n2001:db8::10\n2001:db8::1\n"), 0o644)
	source, err := NewTextInputSource6([]string{path}, opt6(128, true), true, 10)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.NextBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 {
		t.Fatalf("batch: %+v", batch)
	}
	if batch, err := source.NextBatch(); err != nil || batch != nil {
		t.Fatalf("end: %v %v", batch, err)
	}
}
