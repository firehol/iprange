package iprangedb

import (
	"sync"
	"testing"
)

// TestConcurrentLookupsAndScans pins the concurrency contract
// (design-iprange-engine.md: reader lookups and independent scans may run
// concurrently without a per-call mutex, atomic, or active counter; callers
// must not race Close with reader work — this test joins before closing).
// Run under -race.
func TestConcurrentLookupsAndScans(t *testing.T) {
	fixtures := []string{
		"rust/direct-ipv4.iprdb",
		"rust/first-seen-ipv6.iprdb",
		"rust/membership-ipv4.iprdb",
		"rust/membership-ipv6.iprdb",
		"rust/structured-ipv4.iprdb",
	}
	for _, f := range fixtures {
		f := f
		t.Run(f, func(t *testing.T) {
			db := mustOpen(t, f)
			info, err := db.Info()
			if err != nil {
				t.Fatal(err)
			}
			const workers = 8
			const rounds = 500
			ops := []struct {
				name string
				fn   func(p *Pin) error
			}{}
			directV4 := info.ValueKind == ValueKindDirect && info.Family == AddressFamilyIPv4
			directV6 := info.ValueKind == ValueKindDirect && info.Family == AddressFamilyIPv6
			member := info.ValueKind == ValueKindMembership
			structured := info.ValueKind == ValueKindStructured
			if directV4 {
				ops = append(ops,
					struct {
						name string
						fn   func(p *Pin) error
					}{"direct-v4", func(p *Pin) error {
						_, _, err := db.LookupDirectV4(IPv4(0x0a00000a))
						return err
					}},
					struct {
						name string
						fn   func(p *Pin) error
					}{"direct-scan", func(p *Pin) error {
						return db.DirectRangesV4(func(DirectRangeV4) error { return nil })
					}},
				)
			}
			if directV6 {
				ops = append(ops, struct {
					name string
					fn   func(p *Pin) error
				}{"direct-v6", func(p *Pin) error {
					_, _, err := db.LookupDirectV6(IPv6{Hi: 1, Lo: 2})
					return err
				}})
			}
			if member && info.Family == AddressFamilyIPv4 {
				ops = append(ops, struct {
					name string
					fn   func(p *Pin) error
				}{"membership-v4", func(p *Pin) error {
					view, found, err := p.LookupMembershipV4(IPv4(0x0a000000))
					if err != nil {
						return err
					}
					if found {
						if _, err := view.ContainsIndex(0); err != nil {
							return err
						}
					}
					return nil
				}})
			}
			if member && info.Family == AddressFamilyIPv6 {
				ops = append(ops, struct {
					name string
					fn   func(p *Pin) error
				}{"membership-v6", func(p *Pin) error {
					view, found, err := p.LookupMembershipV6(IPv6{Hi: 0, Lo: 0})
					if err != nil {
						return err
					}
					if found {
						if _, err := view.ContainsIndex(0); err != nil {
							return err
						}
					}
					return nil
				}})
			}
			if structured {
				ops = append(ops, struct {
					name string
					fn   func(p *Pin) error
				}{"structured-v4", func(p *Pin) error {
					view, found, err := p.LookupNetworkEnrichmentV1V4(IPv4(0x0a010000))
					if err != nil {
						return err
					}
					if found {
						if _, err := view.Value(); err != nil {
							return err
						}
					}
					return nil
				}})
			}
			if len(ops) == 0 {
				t.Fatalf("no operations for fixture %s", f)
			}
			var wg sync.WaitGroup
			errs := make(chan error, workers)
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					// One pin per goroutine, outside the hot loop
					// (decision 4A): lookups inside take no atomics.
					pin, err := db.Pin()
					if err != nil {
						errs <- err
						return
					}
					defer pin.Close()
					op := ops[w%len(ops)]
					for i := 0; i < rounds; i++ {
						if err := op.fn(pin); err != nil {
							errs <- err
							return
						}
					}
				}(w)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
			// Join before Close: Close must not race reader work.
			if err := db.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
			_ = info
		})
	}
}

// TestConcurrentPinnedLookups hammers pinned lookups and view operations
// from per-goroutine pins; any per-call shared mutable state would show up
// as a race here (design-iprange-engine.md: lookups run without a per-call
// mutex, atomic, or active counter).
func TestConcurrentPinnedLookups(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	const workers = 8
	const rounds = 1000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			pin, err := db.Pin()
			if err != nil {
				t.Error("pin:", err)
				return
			}
			defer pin.Close()
			for i := 0; i < rounds; i++ {
				view, found, err := pin.LookupMembershipV4(IPv4(uint32(0x0a000000 + i%256)))
				if err != nil {
					t.Error("lookup:", err)
					return
				}
				if !found {
					continue
				}
				if _, err := view.ContainsIndex(uint32(i % 70)); err != nil {
					t.Error("contains:", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if err := db.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
