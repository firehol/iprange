package iprangedb

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Warm successful point lookups and cursor steps must allocate zero Go heap
// bytes (acceptance criterion; decision 4A). Direct lookups, scans, and
// cardinality run at reader level; membership, structured, and feed
// operations run through a pin created outside the measured loop. Every
// operation is warmed before the measured run, and AllocsPerRun must
// report exactly zero.

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

	// Cursor and query handles are opened once outside the measured loop:
	// seek+next steps are self-restoring per iteration, so every measured
	// iteration repeats the real work. Feed-range projections scan to
	// exhaustion and are pinned separately as an open-vs-open+scan delta
	// below; name-yielding surfaces (NextFeed, MatchingFeeds yields) copy
	// names to owned strings by design at the root boundary and are not
	// zero-alloc.
	directCur, err := direct.DirectCursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal("direct cursor:", err)
	}
	v6Cur, err := v6.DirectCursorV6(RangeDirectionForward)
	if err != nil {
		t.Fatal("v6 cursor:", err)
	}
	memberQuery, err := member.MembershipQuery()
	if err != nil {
		t.Fatal("membership query:", err)
	}
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
		{"direct-cursor-v4", func() error {
			for _, ip := range probe {
				if err := directCur.Seek(ip); err != nil {
					return err
				}
				if _, _, err := directCur.NextRange(); err != nil {
					return err
				}
			}
			return nil
		}},
		{"direct-cursor-v6", func() error {
			for _, ip := range probe64 {
				if err := v6Cur.Seek(IPv6{Hi: ip.hi, Lo: ip.lo}); err != nil {
					return err
				}
				if _, _, err := v6Cur.NextRange(); err != nil {
					return err
				}
			}
			return nil
		}},
		{"matching-feeds-v4-nil", func() error {
			for _, ip := range probe {
				if _, err := memberQuery.MatchingFeedsV4(ip, nil, nil); err != nil {
					return err
				}
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
				t.Errorf("%s allocated %f allocations per run (contract: exactly zero)", check.name, allocs)
			}
		})
	}

	// Feed-range projections are single-scan cursors: a handle shared
	// across measured runs would pin only the finished path (the
	// AllocsPerRun warm-up consumes the only full scan). The honest pin
	// is the delta between opening a fresh projection and opening and
	// scanning it to exhaustion: the open cost is identical in both
	// runs, so any per-scan or per-interval allocation surfaces as a
	// nonzero delta.
	t.Run("feedrange-cursor-v4-delta", func(t *testing.T) {
		openAllocs := testing.AllocsPerRun(200, func() {
			cur, err := member.FeedRangeCursorV4("feed-000", RangeDirectionForward)
			if err != nil {
				t.Fatal("open:", err)
			}
			_ = cur
		})
		scanAllocs := testing.AllocsPerRun(200, func() {
			cur, err := member.FeedRangeCursorV4("feed-000", RangeDirectionForward)
			if err != nil {
				t.Fatal("open:", err)
			}
			for {
				if _, ok, err := cur.NextRange(); err != nil {
					t.Fatal(err)
				} else if !ok {
					break
				}
			}
		})
		if delta := scanAllocs - openAllocs; delta != 0 {
			t.Errorf("v4 feed-range scan allocated %f allocations per scan (open %.3f, open+scan %.3f); contract: exactly zero", delta, openAllocs, scanAllocs)
		}
	})
	t.Run("feedrange-cursor-v6-delta", func(t *testing.T) {
		openAllocs := testing.AllocsPerRun(200, func() {
			cur, err := member6.FeedRangeCursorV6("global", RangeDirectionForward)
			if err != nil {
				t.Fatal("open:", err)
			}
			_ = cur
		})
		scanAllocs := testing.AllocsPerRun(200, func() {
			cur, err := member6.FeedRangeCursorV6("global", RangeDirectionForward)
			if err != nil {
				t.Fatal("open:", err)
			}
			for {
				if _, ok, err := cur.NextRange(); err != nil {
					t.Fatal(err)
				} else if !ok {
					break
				}
			}
		})
		if delta := scanAllocs - openAllocs; delta != 0 {
			t.Errorf("v6 feed-range scan allocated %f allocations per scan (open %.3f, open+scan %.3f); contract: exactly zero", delta, openAllocs, scanAllocs)
		}
	})
}

// TestZeroAllocationFeedSliceIngestion mirrors the Rust
// slice_ingestion_and_feed_comparison_allocate_nothing_per_record
// thread-allocation pin on the public writer surface: each 1000-record
// AddRangesV4 batch over the mmap-backed private draft must allocate
// zero Go heap bytes (the coverage tree grows only mapped pages). The
// warm batch starts the ordered-prefix builder and every measured batch
// continues the strictly ascending stream, exactly like the Rust test
// measures the first slice of a fresh workflow: re-adding the same
// ranges would wrap around, prove the input unordered, and legitimately
// charge the general input's one-time locator, so the measured stream
// never wraps.
func TestZeroAllocationFeedSliceIngestion(t *testing.T) {
	path := testFeedMembership(t)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// Pin the fresh-workflow first batch: the very first slice ingestion
	// (which proves the ascending prefix and starts the ordered builder)
	// must also allocate nothing, mirroring the Rust
	// count_thread_allocations window around the first add_ranges_v4_slice.
	// The one exception is the Go runtime's one-time type-assertion cache:
	// the first interface assertion of the coverage path charges a single
	// 48-byte runtime metadata entry that no user code can avoid (Rust's
	// thread-allocation counter does not see Go runtime internals). One
	// throwaway workflow warms that cache so the measured window is
	// exactly the user-code allocation behavior.
	warm, err := w.BeginCreateFeed(feedName(t, "warm"), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	if err := warm.AddRangesV4(feedRanges1000()); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	create, err := w.BeginCreateFeed(feedName(t, "feed"), NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := create.AddRangesV4(feedRanges1000()); err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if delta := after.TotalAlloc - before.TotalAlloc; delta != 0 {
		t.Fatalf("fresh-workflow first batch allocated %d bytes, want 0", delta)
	}
	// 51 continuation batches of 1000 strictly ascending records (one
	// extra for the AllocsPerRun warmup); the first record of batch b
	// follows the last record of batch b-1 with the same two-address
	// stride as the warm batch.
	const runs = 50
	batches := make([][]AddressRange4, runs+1)
	for b := range batches {
		batches[b] = make([]AddressRange4, 1000)
		for i := range batches[b] {
			index := (1000 + b*1000 + i) * 2
			batches[b][i] = AddressRange4{From: IPv4(index), To: IPv4(index)}
		}
	}
	run := 0
	if allocs := testing.AllocsPerRun(runs, func() {
		if err := create.AddRangesV4(batches[run]); err != nil {
			t.Fatal(err)
		}
		run++
	}); allocs != 0 {
		t.Fatalf("feed slice ingestion allocates %v objects per batch, want 0", allocs)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
}
