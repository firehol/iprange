package handlers

import (
	"encoding/json"
	"net/netip"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

func u128From4(s string) u128 {
	addr := netip.MustParseAddr(s)
	var bytes [16]byte
	if addr.Is4() {
		v4 := addr.As4()
		return u128{lo: uint64(v4[0])<<24 | uint64(v4[1])<<16 | uint64(v4[2])<<8 | uint64(v4[3])}
	}
	bytes = addr.As16()
	return u128{
		hi: uint64(bytes[0])<<56 | uint64(bytes[1])<<48 | uint64(bytes[2])<<40 | uint64(bytes[3])<<32 |
			uint64(bytes[4])<<24 | uint64(bytes[5])<<16 | uint64(bytes[6])<<8 | uint64(bytes[7]),
		lo: uint64(bytes[8])<<56 | uint64(bytes[9])<<48 | uint64(bytes[10])<<40 | uint64(bytes[11])<<32 |
			uint64(bytes[12])<<24 | uint64(bytes[13])<<16 | uint64(bytes[14])<<8 | uint64(bytes[15]),
	}
}

func expNetset(t *testing.T, from, to string, filter *prefixFilter) []string {
	t.Helper()
	var output []string
	var line []byte
	f := u128From4(from)
	l := u128From4(to)
	err := emitNetset(f, l, filter, &line, func(text []byte, _ iprangedb.Cardinality129) *rpc.HandlerError {
		output = append(output, string(text))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func TestScratchExportNetset(t *testing.T) {
	f := allPrefixes(32)
	got := expNetset(t, "192.0.2.0", "192.0.2.255", f)
	if len(got) != 1 || got[0] != "192.0.2.0/24" {
		t.Fatalf("full block: %v", got)
	}
	got = expNetset(t, "192.0.2.4", "192.0.2.7", f)
	if len(got) != 1 || got[0] != "192.0.2.4/30" {
		t.Fatalf("crossing: %v", got)
	}
	got = expNetset(t, "192.0.2.10", "192.0.2.10", f)
	if len(got) != 1 || got[0] != "192.0.2.10" {
		t.Fatalf("single: %v", got)
	}
	min := minPrefixFilter(32, 25)
	got = expNetset(t, "192.0.2.0", "192.0.2.255", min)
	if len(got) != 2 || got[0] != "192.0.2.0/25" || got[1] != "192.0.2.128/25" {
		t.Fatalf("min prefix: %v", got)
	}
	listed := listedPrefixes(32, []uint32{24, 32})
	got = expNetset(t, "192.0.2.0", "192.0.2.255", listed)
	if len(got) != 1 || got[0] != "192.0.2.0/24" {
		t.Fatalf("listed: %v", got)
	}
	hostOnly := listedPrefixes(32, []uint32{32})
	got = expNetset(t, "10.0.0.0", "10.0.0.2", hostOnly)
	if len(got) != 3 || got[0] != "10.0.0.0" || got[1] != "10.0.0.1" || got[2] != "10.0.0.2" {
		t.Fatalf("host only: %v", got)
	}
	want := []string{"192.0.2.5", "192.0.2.6/31", "192.0.2.8/29", "192.0.2.16/28",
		"192.0.2.32/27", "192.0.2.64/26", "192.0.2.128/25", "192.0.3.0/30", "192.0.3.4/31", "192.0.3.6"}
	got = expNetset(t, "192.0.2.5", "192.0.3.6", f)
	if len(got) != len(want) {
		t.Fatalf("boundary: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("boundary[%d]: got %s want %s", i, got[i], want[i])
		}
	}
	// IPv6 /120 and full space.
	f6 := allPrefixes(128)
	got = expNetset(t, "2001:db8::", "2001:db8::ff", f6)
	if len(got) != 1 || got[0] != "2001:db8::/120" {
		t.Fatalf("v6 block: %v", got)
	}
	got = expNetset(t, "::", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", f6)
	if len(got) != 1 || got[0] != "::/0" {
		t.Fatalf("full v6: %v", got)
	}
}

func TestScratchExportLegacyHeader(t *testing.T) {
	header := legacyBinaryHeader(false, 2, u128{lo: 4660})
	want := "iprange binary format v1.0\noptimized\nrecord size 8\nrecords 2\nbytes 20\nlines 2\nunique ips 4660\n"
	if string(header) != want {
		t.Fatalf("header: %q", header)
	}
	v6 := legacyBinaryHeader(true, 1, u128{lo: 1})
	prefix := "iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 1\nbytes 36\n"
	if len(string(v6)) < len(prefix) || string(v6)[:len(prefix)] != prefix {
		t.Fatalf("v6 header: %q", v6)
	}
	record := legacyBinaryRecordV4(0xC0000200, 0xC00002FF)
	if len(record) != 8 || record[0] != 0x00 || record[3] != 0xc0 || record[4] != 0xff || record[7] != 0xc0 {
		t.Fatalf("record v4: %x", record)
	}
}

func TestScratchExportJSONString(t *testing.T) {
	cases := []string{"feed-a", "42", "", "a\"b\\c", "line\nfeed\tTab\r", "\x01\x1f", "greek: αβγ δ", "emoji: 😀🚀", "中文字符 and \"quotes\"", "\u007f"}
	for _, input := range cases {
		got := string(pushJSONString(nil, input))
		want, _ := json.Marshal(input)
		if got != string(want) {
			t.Fatalf("pushJSONString diverged for %q: %s vs %s", input, got, want)
		}
	}
}
