// Unit tests for the legacy IPv6 family implementation (ipv6.go).
// Inputs and expected values are the ipv6.rs test module ported to
// Go; the corresponding Rust reference is
// v4/rust/iprange-cli/src/legacy/ipv6.rs (and ipv4.rs for the
// ConvertForeignV4 mapped forms, whose behavior Go exposes as one
// function).

package legacy

import "testing"

func ip128(hi, lo uint64) IP128 { return IP128{Hi: hi, Lo: lo} }

func mustV6Addr(t *testing.T, s string) IP128 {
	t.Helper()
	a, err := ParseAddrV6(s)
	if err != nil {
		t.Fatalf("ParseAddrV6(%q): %v", s, err)
	}
	return a
}

func wantV6Err(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// cidr6 is the 4-arg ParseCIDRV6 contract that the parse worker
// uses: family default 128, --dont-fix-network passed through.
func cidr6(token string, prefix uint32, fixNetwork bool) (Range, error) {
	return ParseCIDRV6(token, prefix, fixNetwork, 128)
}

var (
	v6Max  = ip128(0xFFFF_FFFF_FFFF_FFFF, 0xFFFF_FFFF_FFFF_FFFF)
	v6MaxD = ip128(0x0000_00FF_FFFF_FFFF, 0xFFFF_FFFF_FFFF_FFFF) // 2^128-1 >> 24
)

func TestParseAddrV6FullAndCompressed(t *testing.T) {
	cases := []struct {
		in   string
		want IP128
	}{
		{"::", ip128(0, 0)},
		{"::1", ip128(0, 1)},
		{"0:0:0:0:0:0:0:1", ip128(0, 1)},
		{"2001:db8::1", ip128(0x2001_0db8_0000_0000, 0x0000_0000_0000_0001)},
		{"2001:db8:0:0:0:0:0:1", ip128(0x2001_0db8_0000_0000, 0x0000_0000_0000_0001)},
		{"ABCD::1", ip128(0xabcd_0000_0000_0000, 1)},
		{"1::", ip128(0x0001_0000_0000_0000, 0)},
		{"1:2:3:4:5:6:7:8", ip128(0x0001_0002_0003_0004, 0x0005_0006_0007_0008)},
	}
	for _, c := range cases {
		got, err := ParseAddrV6(c.in)
		if err != nil {
			t.Errorf("ParseAddrV6(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseAddrV6(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestParseAddrV6EmbeddedV4Tail(t *testing.T) {
	// ::ffff:1.2.3.4
	want := ip128(0, 0xFFFF_0102_0304)
	if got := mustV6Addr(t, "::ffff:1.2.3.4"); got != want {
		t.Errorf("::ffff:1.2.3.4 = %#v, want %#v", got, want)
	}
	// Words for the mapped 1.2.3.4 = 0xffff00000000 | 0x01020304.
	if got := mustV6Addr(t, "::ffff:1.2.3.4"); got != ip128(0, 0xFFFF_0000_0000|0x0102_0304) {
		t.Errorf("mapped word form mismatch: %#v", got)
	}
	// IPv4-compatible tail is accepted too.
	if got := mustV6Addr(t, "::1.2.3.4"); got != ip128(0, 0x0102_0304) {
		t.Errorf("::1.2.3.4 = %#v, want 0.0.0.0:0.0.0.0:1.2.3.4", got)
	}
	// Hex groups parse the same as the dotted tail.
	if mustV6Addr(t, "::ffff:0102:0304") != mustV6Addr(t, "::ffff:1.2.3.4") {
		t.Error("::ffff:0102:0304 != ::ffff:1.2.3.4")
	}
	// 7 groups + tail is a valid full address.
	if _, err := ParseAddrV6("1:2:3:4:5:6:1.2.3.4"); err != nil {
		t.Errorf("1:2:3:4:5:6:1.2.3.4: %v", err)
	}
	// Five-octet tail is rejected.
	_, err := ParseAddrV6("::ffff:1.2.3.4.5")
	wantV6Err(t, err, "iprange: Invalid IPv6 address ::ffff:1.2.3.4.5")
}

func TestParseAddrV6BareV4NormalizesToMapped(t *testing.T) {
	cases := []struct {
		in   string
		want IP128
	}{
		{"1.2.3.4", MappedAddr(IP128{Lo: 0x0102_0304})},
		{"1.2.3", MappedAddr(IP128{Lo: 0x0102_0003})}, // a.b.c -> a.b.0.c
		{"1.2", MappedAddr(IP128{Lo: 0x0100_0002})},   // a.b -> a.0.0.b
		{"1", MappedAddr(IP128{Lo: 1})},
		{"127.1", MappedAddr(IP128{Lo: 0x7F00_0001})},     // a.b -> a.0.0.b
		{"010.1.1.1", MappedAddr(IP128{Lo: 0x0801_0101})}, // octal leading zero
		{"4294967295", MappedAddr(IP128{Lo: 0xFFFF_FFFF})},
		// glibc inet_aton accepts trailing whitespace.
		{"1.2.3.4 ", MappedAddr(IP128{Lo: 0x0102_0304})},
	}
	for _, c := range cases {
		got, err := ParseAddrV6(c.in)
		if err != nil {
			t.Errorf("ParseAddrV6(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseAddrV6(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
	// The mapped forms agree with the explicit ::ffff: spellings.
	got, err := ParseAddrV6("1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if got != mustV6Addr(t, "::ffff:1.2.3.4") {
		t.Error("1.2.3.4 != ::ffff:1.2.3.4")
	}
}

func TestParseAddrV6ErrorsMatchC(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"::garbage", "iprange: Invalid IPv6 address ::garbage"},
		{"102:304", "iprange: Invalid IPv6 address 102:304"},
		{"1.2.3.99999", "iprange: Invalid address 1.2.3.99999."},
		{"256.1.1.1", "iprange: Invalid address 256.1.1.1."},
		{"4294967296", "iprange: Invalid address 4294967296."},
		{"abcdef", "iprange: Cannot parse address: abcdef"},
		{"", "iprange: Cannot parse address: "},
	}
	for _, c := range cases {
		_, err := ParseAddrV6(c.in)
		wantV6Err(t, err, c.want)
	}
}

func TestParseCIDRV6RangesMatchC(t *testing.T) {
	// /128 keeps a single address.
	got, err := cidr6("::1", 128, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Range{Lo: ip128(0, 1), Hi: ip128(0, 1)}) {
		t.Errorf("::1/128 = %#v", got)
	}
	// /0 with fix-network is the full universe.
	got, _ = cidr6("::1", 0, true)
	if got != (Range{Lo: ip128(0, 0), Hi: v6Max}) {
		t.Errorf("::1/0 fix = %#v", got)
	}
	// /0 without fix-network keeps the raw start.
	got, _ = cidr6("::1", 0, false)
	if got != (Range{Lo: ip128(0, 1), Hi: v6Max}) {
		t.Errorf("::1/0 nofix = %#v", got)
	}
	// /64 fix vs dont-fix (C probe: 2001:db8::7/64).
	got, _ = cidr6("2001:db8::7", 64, true)
	if got != (Range{Lo: ip128(0x2001_0db8_0000_0000, 0), Hi: ip128(0x2001_0db8_0000_0000, 0xFFFF_FFFF_FFFF_FFFF)}) {
		t.Errorf("2001:db8::7/64 fix = %#v", got)
	}
	got, _ = cidr6("2001:db8::7", 64, false)
	if got != (Range{Lo: ip128(0x2001_0db8_0000_0000, 7), Hi: ip128(0x2001_0db8_0000_0000, 0xFFFF_FFFF_FFFF_FFFF)}) {
		t.Errorf("2001:db8::7/64 nofix = %#v", got)
	}
	// Mapped input under a v6-class token masks the FULL 128 bits
	// (C probe: ::ffff:1.2.3.7/24 fix -> ::/24).
	got, _ = cidr6("::ffff:1.2.3.7", 24, true)
	if got != (Range{Lo: ip128(0, 0), Hi: v6MaxD}) {
		t.Errorf("::ffff:1.2.3.7/24 fix = %#v", got)
	}
	got, _ = cidr6("::ffff:1.2.3.7", 24, false)
	if got != (Range{Lo: ip128(0, 0xFFFF_0102_0307), Hi: v6MaxD}) {
		t.Errorf("::ffff:1.2.3.7/24 nofix = %#v", got)
	}
	// Full-token form with an embedded prefix (C str2netaddr6 split).
	a, err := cidr6("2001:db8::7/64", 128, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cidr6("2001:db8::7", 64, true)
	if a != b {
		t.Errorf("embedded prefix mismatch: %#v vs %#v", a, b)
	}
}

func TestParseCIDRV6V4ClassUses32BitPrefix(t *testing.T) {
	// C probe: 1.2.3.4/24 -> ::ffff:1.2.3.0-::ffff:1.2.3.255.
	got, err := cidr6("1.2.3.4", 24, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Range{Lo: ip128(0, 0xFFFF_0102_0300), Hi: ip128(0, 0xFFFF_0102_03FF)}) {
		t.Errorf("1.2.3.4/24 fix = %#v", got)
	}
	// dont-fix keeps the raw host start (C probe: 1.2.3.7/24).
	got, _ = cidr6("1.2.3.7", 24, false)
	if got != (Range{Lo: ip128(0, 0xFFFF_0102_0307), Hi: ip128(0, 0xFFFF_0102_03FF)}) {
		t.Errorf("1.2.3.7/24 nofix = %#v", got)
	}
	// /0 over the v4-class token is the mapped IPv4 space.
	got, _ = cidr6("1.2.3.4", 0, true)
	if got != (Range{Lo: ip128(0, 0xFFFF_0000_0000), Hi: ip128(0, 0x0000_FFFF_FFFF_FFFF)}) {
		t.Errorf("1.2.3.4/0 fix = %#v", got)
	}
	// Full-token netmask form (C probe: 1.2.3.4/255.255.255.0).
	a, err := cidr6("1.2.3.4/255.255.255.0", 24, true)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cidr6("1.2.3.4", 24, true)
	if a != b {
		t.Errorf("netmask text mismatch: %#v vs %#v", a, b)
	}
	// Out-of-range numeric prefix falls to the C netmask error.
	_, err = cidr6("1.2.3.4", 64, true)
	wantV6Err(t, err, "iprange: Invalid netmask 64")
	_, err = cidr6("1.2.3.4/255.255.255.1", 24, true)
	wantV6Err(t, err, "iprange: Invalid netmask 255.255.255.1")
}

func TestParseCIDRV6ErrorsMatchC(t *testing.T) {
	_, err := cidr6("::1", 129, true)
	wantV6Err(t, err, "iprange: Invalid IPv6 prefix /129")
	_, err = cidr6("::1/129", 129, true)
	wantV6Err(t, err, "iprange: Invalid IPv6 prefix /129")
	_, err = cidr6("::1/xyz", 129, true)
	wantV6Err(t, err, "iprange: Invalid IPv6 prefix /xyz")
	_, err = cidr6("1.2.3.4/64", 64, true)
	wantV6Err(t, err, "iprange: Invalid netmask 64")
	// parse_addr ignores an embedded prefix (address part only).
	if got := mustV6Addr(t, "::1/24"); got != ip128(0, 1) {
		t.Errorf("ParseAddrV6(::1/24) = %#v, want ::1", got)
	}
}

func TestParsePrefixV6MatchesStrtol(t *testing.T) {
	ok := []struct {
		in   string
		want uint32
	}{
		{"128", 128},
		{"0", 0},
		{"24", 24},
		{"012", 12}, // base 10, not octal
		{" 24", 24}, // strtol skips spaces
		{"+24", 24}, // strtol accepts +
		{"-0", 0},   // strtol accepts -0
	}
	for _, c := range ok {
		got, err := ParsePrefixV6(c.in)
		if err != nil {
			t.Errorf("ParsePrefixV6(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePrefixV6(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "129", "-1", "24x", "24 ", "0x10", "abc", "9999999999999999999999"} {
		_, err := ParsePrefixV6(bad)
		wantV6Err(t, err, "iprange: Invalid IPv6 prefix /"+bad)
	}
}

func TestFmtAddrV6MatchesGlibcInetNtop(t *testing.T) {
	cases := []struct {
		in   IP128
		want string
	}{
		{ip128(0, 0), "::"},
		{ip128(0, 1), "::1"},
		{ip128(0x2001_0db8_0000_0000, 0x0000_0000_0000_0001), "2001:db8::1"},
		// Mapped dotted-quad form (probed oracle outputs).
		{ip128(0, 0xFFFF_0102_0304), "::ffff:1.2.3.4"},
		{ip128(0, 0xFFFF_0000_0001), "::ffff:0.0.0.1"},
		{ip128(0, 0x0000_FFFF_FFFF_FFFF), "::ffff:255.255.255.255"},
		// glibc quirks: 6-word zero run prints the tail as dotted quad.
		{ip128(0, 0x1234_5678), "::18.52.86.120"},
		{ip128(0x0000_0000_0000_0000, 0x0000_0000_0001_0000), "::0.1.0.0"},
		// First longest zero run wins ties.
		{ip128(0x0001_0000_0000_0002, 0x0000_0000_0003_0004), "1::2:0:0:3:4"},
		{ip128(0x2001_0db8_0000_0000, 0x0001_0000_0000_0001), "2001:db8::1:0:0:1"},
		// Trailing run compresses with a trailing colon.
		{ip128(0x0001_0000_0000_0000, 0), "1::"},
		{ip128(0x2001_0db8_0000_0000, 0), "2001:db8::"},
		{ip128(0x0001_0002_0003_0004, 0x0005_0006_0007_0000), "1:2:3:4:5:6:7:0"},
		// No zero run: no compression.
		{ip128(0x0001_0002_0003_0004, 0x0005_0006_0007_0008), "1:2:3:4:5:6:7:8"},
		// Lowercase hex.
		{ip128(0xabcd_0000_0000_0000, 1), "abcd::1"},
	}
	for _, c := range cases {
		if got := FmtAddrV6(c.in); got != c.want {
			t.Errorf("FmtAddrV6(%#x) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFmtCIDRV6MatchesCPrintAddr6(t *testing.T) {
	cases := []struct {
		addr   IP128
		prefix uint32
		want   string
	}{
		{ip128(0, 0), 0, "::/0"},
		{ip128(0, 1), 128, "::1"},
		{ip128(0x2001_0db8_0000_0000, 0), 64, "2001:db8::/64"},
		{ip128(0, 0xFFFF_0102_0300), 120, "::ffff:1.2.3.0/120"},
	}
	for _, c := range cases {
		if got := FmtCIDRV6(c.addr, c.prefix); got != c.want {
			t.Errorf("FmtCIDRV6(%#x, %d) = %q, want %q", c.addr, c.prefix, got, c.want)
		}
	}
}

func TestConvertForeignV4MappedForms(t *testing.T) {
	// Mapped IPv6 with dotted quad, mixed-case f's, and the
	// shortened/octal spellings the C tail scan accepts.
	cases := []struct {
		in   string
		want Range
	}{
		{"::ffff:1.2.3.4", Range{Lo: ip128(0, 0x0102_0304), Hi: ip128(0, 0x0102_0304)}},
		{"::FFFF:1.2.3.4", Range{Lo: ip128(0, 0x0102_0304), Hi: ip128(0, 0x0102_0304)}},
		{"::ffff:1.2.3", Range{Lo: ip128(0, 0x0102_0003), Hi: ip128(0, 0x0102_0003)}},
		{"::ffff:1.2.3.4/24", Range{Lo: ip128(0, 0x0102_0300), Hi: ip128(0, 0x0102_03FF)}},
	}
	for _, c := range cases {
		got, ok := ConvertForeignV4(c.in)
		if !ok || got != c.want {
			t.Errorf("ConvertForeignV4(%q) = %#v, %v; want %#v, true", c.in, got, ok, c.want)
		}
	}
	// Not converted (oracle drops these as IPv6).
	for _, bad := range []string{
		"::ffff:0102:0304",
		"::ffff:1.2.3.4/33",
		"::ffff:1.2.3.999",
		"2001:db8::1",
		"1.2.3.4",
		"::ffff:",
		"::ffff:1.2.3.4 x",
		"1.2.3.4/24",
	} {
		if r, ok := ConvertForeignV4(bad); ok {
			t.Errorf("ConvertForeignV4(%q) = %#v, true; want false", bad, r)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want Class
	}{
		{"::", ClassV6},
		{"::1", ClassV6},
		{"2001:db8::1", ClassV6},
		{"::ffff:1.2.3.4", ClassV6},
		{"1.2.3.4", ClassV4},
		{"1.2.3.4/24", ClassV4},
		{"10", ClassV4},
		{"127.1", ClassV4},
		{"abcdef", ClassOther},
		{"", ClassOther},
		{"hostname", ClassOther},
		// A dotted token is IPv4-class even when it is not a valid
		// address (C classify_address: '.' marks IPv4 text).
		{"hostname.example", ClassV4},
	}
	for _, c := range cases {
		if got := Classify(c.in); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMappedAddr(t *testing.T) {
	for _, v4 := range []uint32{0, 1, 0x0102_0304, 0xFFFF_FFFF, 0x7F00_0001} {
		got := MappedAddr(IP128{Lo: uint64(v4)})
		want := ip128(0, mappedPrefix|uint64(v4))
		if got != want {
			t.Errorf("MappedAddr(%08x) = %#v, want %#v", v4, got, want)
		}
	}
}
