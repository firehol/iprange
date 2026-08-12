package iprangedb

import (
	"path/filepath"
	"testing"
)

// Warm successful point lookups and cursor steps must allocate zero Go heap
// bytes (acceptance criterion; decision 4A). Every operation below runs
// through a pin created outside the measured loop, is warmed before the
// measured run, and AllocsPerRun must report exactly zero.

func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "conformance", "rust", name)
}

func openPublic(t *testing.T, name string) *ImmutableReader {
	t.Helper()
	db, err := OpenImmutable(fixture(t, name))
	if err != nil {
		t.Fatal("open:", err)
	}
	return db
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

	pins := map[string]*Pin{}
	for name, db := range map[string]*ImmutableReader{
		"member":     member,
		"member6":    member6,
		"structured": structured,
	} {
		pin, err := db.Pin()
		if err != nil {
			t.Fatal("pin:", err)
		}
		pins[name] = pin
		defer pin.Close()
	}
	memberPin := pins["member"]
	member6Pin := pins["member6"]
	structuredPin := pins["structured"]

	// IPv64 pairs exercise the full 2^128 span on the v6 fixtures.
	type IPv64 struct{ hi, lo uint64 }

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
		memberPin.LookupMembershipV4(ip)
		structuredPin.LookupNetworkEnrichmentV1V4(ip)
	}
	for _, ip := range probe64 {
		v6.LookupDirectV6(IPv6{Hi: ip.hi, Lo: ip.lo})
		member6Pin.LookupMembershipV6(IPv6{Hi: ip.hi, Lo: ip.lo})
	}
	memberPin.LookupFeedInto("feed-000", make([]byte, 16))
	view, _, _ := memberPin.LookupMembershipV4(IPv4(0x0a000000))
	view.ContainsIndex(0)
	words := make([]uint64, 8)
	view.ReadWords(0, words)

	// One feed-name buffer reused across the measured loop.
	feedBuf := make([]byte, 16)

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
		{"direct-scan", func() error {
			return direct.DirectRangesV4(func(DirectRangeV4) error { return nil })
		}},
		{"direct-cardinality", func() error {
			_, err := direct.Cardinality()
			return err
		}},
		{"membership-v4", func() error {
			for _, ip := range probe {
				if _, _, err := memberPin.LookupMembershipV4(ip); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-v6-inline", func() error {
			for _, ip := range probe64 {
				if _, _, err := member6Pin.LookupMembershipV6(IPv6{Hi: ip.hi, Lo: ip.lo}); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-contains", func() error {
			view, _, err := memberPin.LookupMembershipV4(IPv4(0x0a000000))
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
			view, _, err := member6Pin.LookupMembershipV6(IPv6{Hi: 0, Lo: 0})
			if err != nil {
				return err
			}
			for i := uint32(0); i < 4; i++ {
				if _, _, err := view.Word(i); err != nil {
					return err
				}
			}
			return nil
		}},
		{"membership-readwords", func() error {
			view, _, err := memberPin.LookupMembershipV4(IPv4(0x0a000000))
			if err != nil {
				return err
			}
			_, err = view.ReadWords(0, words)
			return err
		}},
		{"structured-v4", func() error {
			for _, ip := range probe {
				view, found, err := structuredPin.LookupNetworkEnrichmentV1V4(ip)
				if err != nil {
					return err
				}
				if !found {
					continue
				}
				if _, err := view.Value(); err != nil {
					return err
				}
			}
			return nil
		}},
		{"structured-threat", func() error {
			view, _, err := structuredPin.LookupNetworkEnrichmentV1V4(IPv4(0x0a010000))
			if err != nil {
				return err
			}
			threat, _, err := view.ThreatMembership()
			if err != nil {
				return err
			}
			_, err = threat.ContainsIndex(0)
			return err
		}},
		{"feed-lookup-into", func() error {
			if _, _, err := memberPin.LookupFeedInto("feed-000", feedBuf); err != nil {
				return err
			}
			return nil
		}},
	}
	for _, check := range checks {
		check := check
		t.Run(check.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(400, func() {
				if err := check.fn(); err != nil {
					t.Fatal(err)
				}
			})
			if allocs != 0 {
				t.Errorf("%s allocated %f heap bytes per run (contract: exactly zero)", check.name, allocs)
			}
		})
	}
}
