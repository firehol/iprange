package iprangedb

import (
	"runtime"
	"testing"
)

// TestNoPageSizedHeapAllocations is the dynamic half of the complete-page
// ownership evidence (binary-format-v4.md:108): during a churn workload
// over every lookup surface, no Go heap size class >= 4096 bytes may
// allocate. A full mapped page copied into owned memory would allocate
// exactly such an object per copy. Size-class counters are monotonic, so
// the assertion is a plain before/after comparison; small record-value
// allocations (keys, metadata) hit smaller classes and stay legal.
func TestNoPageSizedHeapAllocations(t *testing.T) {
	direct := openPublic(t, "direct-ipv4.iprdb")
	defer direct.Close()
	member := openPublic(t, "membership-ipv4.iprdb")
	defer member.Close()
	pin, err := member.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	defer pin.Close()

	probe := []IPv4{
		IPv4(0x0a00000a), IPv4(0x0a00000e), IPv4(0x0a00000f), IPv4(0x0a000011),
		IPv4(0x0a000012), IPv4(0x0a000015), IPv4(0x0a00001c), IPv4(0x0a00001f),
		IPv4(0x0a000000), IPv4(0x0a00007f), IPv4(0x0a000080), IPv4(0x0a000100),
	}
	feedBuf := make([]byte, 16)
	query, err := member.MembershipQuery()
	if err != nil {
		t.Fatal("membership query:", err)
	}
	cur, err := direct.DirectCursorV4(RangeDirectionForward)
	if err != nil {
		t.Fatal("direct cursor:", err)
	}
	// Warm every path before the measured window.
	for range 32 {
		for _, ip := range probe {
			direct.LookupDirectV4(ip)
			pin.LookupMembershipV4(ip)
			pin.LookupFeedInto("feed-000", feedBuf)
		}
		cur.Seek(probe[0])
		cur.NextRange()
	}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for range 512 {
		for _, ip := range probe {
			direct.LookupDirectV4(ip)
			pin.LookupMembershipV4(ip)
			pin.LookupFeedInto("feed-000", feedBuf)
		}
		cur.Seek(probe[0])
		cur.NextRange()
		query.MatchingFeedsV4(probe[0], func(string) error { return nil }, nil)
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	beforeBySize := map[uint32]uint64{}
	for _, c := range before.BySize {
		beforeBySize[c.Size] = c.Mallocs
	}
	for _, c := range after.BySize {
		if c.Size < 4096 {
			continue
		}
		if c.Mallocs > beforeBySize[c.Size] {
			t.Fatalf("heap allocation of %d bytes during lookups (mallocs %d -> %d): a complete mapped page was copied into owned memory", c.Size, beforeBySize[c.Size], c.Mallocs)
		}
	}
}
