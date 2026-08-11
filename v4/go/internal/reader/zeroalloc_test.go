package reader

import (
	"testing"
)

// The internal mapped-access paths must allocate zero Go heap bytes for warm
// point lookups, including feed lookup (whose public facade adds only the
// returned string copy).

func TestInternalZeroAllocation(t *testing.T) {
	direct := openFixture(t, "direct-ipv4.iprdb")
	member := openFixture(t, "membership-ipv4.iprdb")
	member6 := openFixture(t, "membership-ipv6.iprdb")
	structured := openFixture(t, "structured-ipv4.iprdb")

	addrs := []uint32{0x0a00000a, 0x0a00000e, 0x0a00000f, 0x0a000013}
	for _, a := range addrs {
		direct.LookupDirect4(a)
		member.LookupMembership4(a)
		structured.LookupNetworkEnrichmentV14(a)
		member.LookupFeed("feed-000")
	}
	member6.LookupMembership6(0, 0)

	checks := []struct {
		name string
		fn   func() error
	}{
		{"direct", func() error {
			for _, a := range addrs {
				if _, _, err := direct.LookupDirect4(a); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership", func() error {
			for _, a := range addrs {
				if _, _, err := member.LookupMembership4(a); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-blob", func() error {
			for i := 0; i < 4; i++ {
				if _, _, err := member6.LookupMembership6(0, 0); err != nil {
					return err
				}
			}
			return nil
		}},
		{"structured", func() error {
			for _, a := range addrs {
				if _, _, err := structured.LookupNetworkEnrichmentV14(a); err != nil {
					return err
				}
			}
			return nil
		}},
		{"feed", func() error {
			for i := 0; i < 70; i++ {
				if _, _, err := member.LookupFeed("feed-000"); err != nil {
					return err
				}
			}
			return nil
		}},
		{"scan", func() error {
			return direct.ScanDirect4(func(RangeVisit4) error { return nil })
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
			if allocs != 0 {
				t.Errorf("%s allocated %f heap bytes per run", check.name, allocs)
			}
		})
	}
}
