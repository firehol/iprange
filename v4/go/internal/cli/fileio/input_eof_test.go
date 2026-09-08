// EOF-completion regression tests (product finding 2): a final text
// line whose byte length is an exact multiple of the 64 KiB read
// buffer (65,536/131,072 without a trailing LF) was dropped because
// the empty-chunk EOF read returned no-line while the buffer-full
// chunks accumulated in the output accumulator. Rust read_limited_line
// emits the accumulated line at EOF; the Go reader must do the same on
// the direct reader, the streaming text source, and the @-file-list
// expansion path.

package fileio

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// eofInputBytes builds one text line `1.1.1.1` followed by trailing
// spaces to exactly `total` bytes, optionally LF-terminated (an exact
// 64 KiB multiple makes the buffer-full + empty-chunk EOF branch
// execute).
func eofInputBytes(total int, lf bool) []byte {
	line := []byte("1.1.1.1")
	line = append(line, bytes.Repeat([]byte(" "), total-len(line))...)
	if lf {
		line = append(line, '\n')
	}
	return line
}

func TestReadLimitedLineFinalLineAtBufferMultiples(t *testing.T) {
	for _, total := range []int{65535, 65536, 65537, 131072} {
		for _, lf := range []bool{false, true} {
			name := fmt.Sprintf("total=%d lf=%v", total, lf)
			reader := bufio.NewReaderSize(bytes.NewReader(eofInputBytes(total, lf)), 64*1024)
			var line []byte
			hadNewline, ok, err := readLimitedLine(reader, 1_048_576, &line)
			if err != nil {
				t.Fatalf("%s: unexpected error: %v", name, err)
			}
			if !ok {
				t.Fatalf("%s: final line dropped (ok=false)", name)
			}
			if hadNewline != lf {
				t.Fatalf("%s: hadNewline=%v, want %v", name, hadNewline, lf)
			}
			if len(line) != total || !bytes.HasPrefix(line, []byte("1.1.1.1")) {
				t.Fatalf("%s: line length %d, want %d with the address prefix", name, len(line), total)
			}
			// The stream is exhausted after the final line.
			var again []byte
			if _, ok, err := readLimitedLine(reader, 1_048_576, &again); err != nil || ok {
				t.Fatalf("%s: stream not exhausted after the final line: ok=%v err=%v", name, ok, err)
			}
		}
	}
}

func TestReadLimitedLineEmptyInputHasNoLine(t *testing.T) {
	reader := bufio.NewReaderSize(bytes.NewReader(nil), 64*1024)
	var line []byte
	if _, ok, err := readLimitedLine(reader, 1024, &line); err != nil || ok {
		t.Fatalf("empty input: ok=%v err=%v, want no line", ok, err)
	}
}

// TestTextInputSourcePersistsFinalLineAtBufferMultiples verifies the
// persisted address membership of the streaming source: every buffer
// multiple must yield the single address 1.1.1.1.
func TestTextInputSourcePersistsFinalLineAtBufferMultiples(t *testing.T) {
	dir := t.TempDir()
	for _, total := range []int{65535, 65536, 65537, 131072} {
		for _, lf := range []bool{false, true} {
			name := fmt.Sprintf("total=%d lf=%v", total, lf)
			path := filepath.Join(dir, fmt.Sprintf("f_%d_%v.txt", total, lf))
			if err := os.WriteFile(path, eofInputBytes(total, lf), 0o644); err != nil {
				t.Fatal(err)
			}
			source, err := NewTextInputSource4([]string{path}, opt4(32, true), true, 16)
			if err != nil {
				t.Fatalf("%s: source: %v", name, err)
			}
			var got []iprangedb.AddressRange4
			for {
				batch, err := source.NextBatch()
				if err != nil {
					t.Fatalf("%s: batch: %v", name, err)
				}
				if len(batch) == 0 {
					break
				}
				got = append(got, batch...)
			}
			source.Close()
			if len(got) != 1 || got[0].From != iprangedb.IPv4(0x01010101) || got[0].To != iprangedb.IPv4(0x01010101) {
				t.Fatalf("%s: ranges = %+v, want the single address 1.1.1.1", name, got)
			}
			if code := source.LastInputErrorCode(); code != "" {
				t.Fatalf("%s: unexpected source error code %q", name, code)
			}
		}
	}
}

// TestExpandPathsFinalListLineAtBufferMultiples covers the @-file-list
// expansion path: the final file-list line is an exact 64 KiB buffer
// multiple (target path plus trailing spaces, no LF) and must still
// expand to the referenced input file.
// TestTextInputSourceFinalLineAmongOthers covers a multi-line file
// whose final line is an exact buffer multiple without a trailing LF.
func TestTextInputSourceFinalLineAmongOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.txt")
	line := append([]byte("2.2.2.2"), bytes.Repeat([]byte(" "), 65536-7)...)
	content := append([]byte("1.1.1.1\n"), line...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewTextInputSource4([]string{path}, opt4(32, true), true, 16)
	if err != nil {
		t.Fatal(err)
	}
	var got []iprangedb.AddressRange4
	for {
		batch, err := source.NextBatch()
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		if len(batch) == 0 {
			break
		}
		got = append(got, batch...)
	}
	source.Close()
	if len(got) != 2 ||
		got[0].From != iprangedb.IPv4(0x01010101) || got[0].To != iprangedb.IPv4(0x01010101) ||
		got[1].From != iprangedb.IPv4(0x02020202) || got[1].To != iprangedb.IPv4(0x02020202) {
		t.Fatalf("ranges = %+v, want 1.1.1.1 then the unterminated final 2.2.2.2", got)
	}
}

func TestExpandPathsFinalListLineAtBufferMultiples(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "addrs.txt")
	if err := os.WriteFile(target, []byte("1.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target2 := filepath.Join(dir, "addrs2.txt")
	if err := os.WriteFile(target2, []byte("2.2.2.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "list.txt")
	// Line 1 is ordinary; line 2 (the final line) is an exact 64 KiB
	// buffer multiple (target path plus trailing spaces, no LF).
	content := append([]byte(target+"\n"), target2...)
	content = append(content, bytes.Repeat([]byte(" "), 65536-len(content))...)
	if err := os.WriteFile(list, content, 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := NewTextInputSource4([]string{"@" + list}, opt4(32, true), true, 16)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	var got []iprangedb.AddressRange4
	for {
		batch, err := source.NextBatch()
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		if len(batch) == 0 {
			break
		}
		got = append(got, batch...)
	}
	source.Close()
	if len(got) != 2 ||
		got[0].From != iprangedb.IPv4(0x01010101) ||
		got[1].From != iprangedb.IPv4(0x02020202) {
		t.Fatalf("ranges = %+v, want 1.1.1.1 and 2.2.2.2 from the @-expanded list including its final buffer-multiple line", got)
	}
}
