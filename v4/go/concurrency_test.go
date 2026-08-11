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
			info := db.Info()
			const workers = 8
			const rounds = 500
			ops := []struct {
				name string
				fn   func() error
			}{}
			directV4 := info.ValueKind == ValueKindDirect && info.Family == AddressFamilyIPv4
			directV6 := info.ValueKind == ValueKindDirect && info.Family == AddressFamilyIPv6
			member := info.ValueKind == ValueKindMembership
			structured := info.ValueKind == ValueKindStructured
			if directV4 {
				ops = append(ops,
					struct {
						name string
						fn   func() error
					}{"direct-v4", func() error {
						_, _, err := db.LookupDirectV4(IPv4(0x0a00000a))
						return err
					}},
					struct {
						name string
						fn   func() error
					}{"direct-scan", func() error {
						return db.DirectRangesV4(func(DirectRangeV4) error { return nil })
					}},
				)
			}
			if directV6 {
				ops = append(ops, struct {
					name string
					fn   func() error
				}{"direct-v6", func() error {
					_, _, err := db.LookupDirectV6(IPv6{Hi: 1, Lo: 2})
					return err
				}})
			}
			if member && info.Family == AddressFamilyIPv4 {
				ops = append(ops, struct {
					name string
					fn   func() error
				}{"membership-v4", func() error {
					view, found, err := db.LookupMembershipV4(IPv4(0x0a000000))
					if err != nil {
						return err
					}
					if found {
						view.Release()
					}
					return nil
				}})
			}
			if member && info.Family == AddressFamilyIPv6 {
				ops = append(ops, struct {
					name string
					fn   func() error
				}{"membership-v6", func() error {
					view, found, err := db.LookupMembershipV6(IPv6{Hi: 0, Lo: 0})
					if err != nil {
						return err
					}
					if found {
						view.Release()
					}
					return nil
				}})
			}
			if structured {
				ops = append(ops, struct {
					name string
					fn   func() error
				}{"structured-v4", func() error {
					view, found, err := db.LookupNetworkEnrichmentV1V4(IPv4(0x0a010000))
					if err != nil {
						return err
					}
					if found {
						view.Release()
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
					op := ops[w%len(ops)]
					for i := 0; i < rounds; i++ {
						if err := op.fn(); err != nil {
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

// TestConcurrentViewCreateRelease hammers view registration while lookups
// run; any per-view shared mutable state would show up as a race here.
func TestConcurrentViewCreateRelease(t *testing.T) {
	db := mustOpen(t, "rust/membership-ipv4.iprdb")
	const workers = 8
	const rounds = 1000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				view, found, err := db.LookupMembershipV4(IPv4(uint32(0x0a000000 + i%256)))
				if err != nil {
					t.Error("lookup:", err)
					return
				}
				if !found {
					continue
				}
				if _, err := view.ContainsIndex(uint32(i % 70)); err != nil {
					t.Error("contains:", err)
					view.Release()
					return
				}
				view.Release()
			}
		}()
	}
	wg.Wait()
	if err := db.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
