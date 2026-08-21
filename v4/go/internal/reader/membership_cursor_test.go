package reader

// Exported membership range cursor surface (slice A of chunk 3b-3):
// NewMembershipRangeCursor4/6 now serve callers outside this package,
// with the exact guard classes the internal joins rely on. The fixtures
// are frozen conformance content, so the walked ranges below are stable.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func TestMembershipRangeCursor4Exported(t *testing.T) {
	r := openFixture(t, "membership-ipv4.iprdb")
	c, err := r.NewMembershipRangeCursor4()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ from, to uint32 }{
		{0x0a000000, 0x0a0000ff}, // 10.0.0.0 - 10.0.0.255
		{0x0a000100, 0x0a00017f}, // 10.0.1.0 - 10.0.1.127
		{0x0a000180, 0x0a0001ff}, // 10.0.1.128 - 10.0.1.255
	}
	for i, w := range want {
		got, ok, err := c.Next()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("record %d: cursor finished early", i)
		}
		if got.From != w.from || got.To != w.to {
			t.Fatalf("record %d: (%x,%x) want (%x,%x)", i, got.From, got.To, w.from, w.to)
		}
		if got.Membership == 0 || uint64(got.Membership) >= r.Meta().MembershipIDLimit {
			t.Fatalf("record %d: membership %d outside the dictionary", i, got.Membership)
		}
		if i > 0 && (got.From <= want[i-1].from) {
			t.Fatalf("record %d not strictly ascending", i)
		}
	}
	if got, ok, err := c.Next(); err != nil || ok {
		t.Fatalf("cursor past the last record: %v %v", got, ok)
	}
}

func TestMembershipRangeCursor6Exported(t *testing.T) {
	r := openFixture(t, "membership-ipv6.iprdb")
	c, err := r.NewMembershipRangeCursor6()
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ fhi, flo, thi, tlo uint64 }{
		{0, 0, 0x20010db7ffffffff, 0xffffffffffffffff},                        // :: - 2001:db7:ffff:...
		{0x20010db800000000, 0, 0x20010db800000000, 0xffff},                   // 2001:db8:: - 2001:db8::ffff
		{0x20010db800000000, 0x10000, 0xffffffffffffffff, 0xffffffffffffffff}, // 2001:db8::1:0 - max
	}
	for i, w := range want {
		got, ok, err := c.Next()
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("record %d: cursor finished early", i)
		}
		if got.FromHi != w.fhi || got.FromLo != w.flo || got.ToHi != w.thi || got.ToLo != w.tlo {
			t.Fatalf("record %d: (%x:%x-%x:%x) want (%x:%x-%x:%x)", i, got.FromHi, got.FromLo, got.ToHi, got.ToLo, w.fhi, w.flo, w.thi, w.tlo)
		}
		if got.Membership == 0 || uint64(got.Membership) >= r.Meta().MembershipIDLimit {
			t.Fatalf("record %d: membership %d outside the dictionary", i, got.Membership)
		}
	}
	if got, ok, err := c.Next(); err != nil || ok {
		t.Fatalf("cursor past the last record: %v %v", got, ok)
	}
}

func TestMembershipRangeCursorGuards(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		new     func(*ImmutableReader) error
		code    format.ErrorCode
	}{
		{"ipv4 cursor on ipv6 db", "membership-ipv6.iprdb", func(r *ImmutableReader) error { _, err := r.NewMembershipRangeCursor4(); return err }, format.CodeWrongAddressFamily},
		{"ipv6 cursor on ipv4 db", "membership-ipv4.iprdb", func(r *ImmutableReader) error { _, err := r.NewMembershipRangeCursor6(); return err }, format.CodeWrongAddressFamily},
		{"ipv4 cursor on direct db", "direct-ipv4.iprdb", func(r *ImmutableReader) error { _, err := r.NewMembershipRangeCursor4(); return err }, format.CodeWrongValueKind},
		{"ipv4 cursor on structured db", "structured-ipv4.iprdb", func(r *ImmutableReader) error { _, err := r.NewMembershipRangeCursor4(); return err }, format.CodeWrongValueKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.new(openFixture(t, tc.fixture)); mustCode(err) != tc.code {
				t.Fatalf("code %v want %v (err %v)", mustCode(err), tc.code, err)
			}
		})
	}
}
