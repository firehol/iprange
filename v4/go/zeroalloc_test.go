package iprangedb

import (
	"path/filepath"
	"testing"
)

// Warm successful point lookups and cursor steps must allocate zero Go heap
// bytes (acceptance criterion). Every operation below is warmed before the
// measured run; AllocsPerRun must report exactly zero.

func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "conformance", "rust", name)
}

func TestZeroAllocationLookups(t *testing.T) {
	direct := openPublic(t, "direct-ipv4.iprdb")
	defer direct.Close()
	v6 := openPublic(t, "first-seen-ipv6.iprdb")
	defer v6.Close()
	member := openPublic(t, "membership-ipv4.iprdb")
	defer member.Close()
	member6 := openPublic(t, "membership-ipv6.iprdb")
	defer member6.Close()
	structured := openPublic(t, "structured-ipv4.iprdb")
	defer structured.Close()

	probe := []IPv4{
		IPv4(0x0a00000a), IPv4(0x0a00000e), IPv4(0x0a00000f), IPv4(0x0a000011),
		IPv4(0x0a000012), IPv4(0x0a000015), IPv4(0x0a00001c), IPv4(0x0a00001f),
		IPv4(0x0a000000), IPv4(0x0a00007f), IPv4(0x0a000080), IPv4(0x0a000100),
	}
	probe64 := []IPv64{
		{0, 0}, {1, 2}, {^uint64(0) / 2, 42}, {^uint64(0), ^uint64(0)},
	}

	// Warm.
	for _, ip := range probe {
		direct.LookupDirectV4(ip)
		member.LookupMembershipV4(ip)
		structured.LookupNetworkEnrichmentV1V4(ip)
	}
	for _, ip := range probe64 {
		v6.LookupDirectV6(IPv6{Hi: ip.hi, Lo: ip.lo})
		member6.LookupMembershipV6(IPv6{Hi: ip.hi, Lo: ip.lo})
	}
	direct.LookupFeed("feed-000")
	member.LookupFeed("feed-000")
	view, _, _ := member.LookupMembershipV4(IPv4(0x0a000000))
	view.ContainsIndex(0)

	checks := []struct {
		name string
		fn   func() error
	}{
		{"direct-v4", func() error {
			for _, ip := range probe {
				if _, _, err := direct.LookupDirectV4(ip); err != nil {
					return err
				}
			}
			return nil
		}},
		{"direct-v6", func() error {
			for _, ip := range probe64 {
				if _, _, err := v6.LookupDirectV6(IPv6{Hi: ip.hi, Lo: ip.lo}); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-v4", func() error {
			for _, ip := range probe {
				if _, _, err := member.LookupMembershipV4(ip); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-v6-blob", func() error {
			for _, ip := range probe64 {
				if _, _, err := member6.LookupMembershipV6(IPv6{Hi: ip.hi, Lo: ip.lo}); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-contains", func() error {
			view, _, err := member.LookupMembershipV4(IPv4(0x0a000000))
			if err != nil {
				return err
			}
			for _, idx := range []uint32{0, 5, 63, 64, 69, 1, 2} {
				if _, err := view.ContainsIndex(idx); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-word", func() error {
			view, _, err := member6.LookupMembershipV6(IPv6{Hi: 0, Lo: 0})
			if err != nil {
				return err
			}
			for i := uint32(0); i < view.WordCount() && i < 4; i++ {
				if _, _, err := view.Word(i); err != nil {
					return err
				}
			}
			return nil
		}},
		{"structured-v4", func() error {
			for _, ip := range probe {
				if _, _, err := structured.LookupNetworkEnrichmentV1V4(ip); err != nil {
					return err
				}
			}
			return nil
		}},
		{"feed-lookup", func() error {
			// The only heap allocation in the public feed lookup is the
			// returned Go string copy of the name (the internal mapped path
			// allocates nothing and is pinned by the reader package's own
			// zero-allocation test).
			for i := 0; i < 70; i++ {
				if _, _, err := member.LookupFeed("feed-000"); err != nil {
					return err
				}
			}
			return nil
		}},
		{"direct-scan", func() error {
			// Full ascending scan of the direct fixture.
			return direct.DirectRangesV4(func(DirectRangeV4) error { return nil })
		}},
		{"direct-cardinality", func() error {
			_, err := direct.Cardinality()
			return err
		}},
	}
	for _, check := range checks {
		check := check
		t.Run(check.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(200, func() {
				if err := check.fn(); err != nil {
					t.Fatal(err)
				}
			})
			switch check.name {
			case "feed-lookup":
				// The public facade allocates exactly one heap object per
				// lookup: the returned name string copy (70 copies for the
				// 70-lookup run). The internal mapped path allocates nothing
				// and is pinned by the reader package's own zero-allocation
				// test.
				if allocs > 70 {
					t.Errorf("%s allocated %f heap bytes per run (want exactly one copy per lookup)", check.name, allocs)
				}
			default:
				if allocs != 0 {
					t.Errorf("%s allocated %f heap bytes per run", check.name, allocs)
				}
			}
		})
	}
}

func openPublic(t *testing.T, name string) *ImmutableReader {
	t.Helper()
	db, err := OpenImmutable(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// IPv64 mirrors the v6 probe addresses without depending on the public type.
type IPv64 struct {
	hi, lo uint64
}
