// Snapshot output zero-allocation pins: a snapshot published by
// SnapshotTo is a first-class immutable v4 database, and warm point
// lookups over it allocate zero Go heap bytes exactly like any other mmap
// reader. The copy itself is allowed to allocate (the writer's pages and
// the concrete OutputWords passes); the published artifact must not.
// This builds on the chunk-3a zero-allocation acceptance matrix.

package iprangedb

import (
	"testing"
)

// TestSnapshotOutputWarmLookupsZeroAllocation snapshots the direct,
// membership, and structured fixtures, then runs the standard warm
// zero-allocation probes over each published output.
func TestSnapshotOutputWarmLookupsZeroAllocation(t *testing.T) {
	for _, fixtureName := range []string{"direct-ipv4.iprdb", "membership-ipv4.iprdb", "structured-ipv4.iprdb"} {
		t.Run(fixtureName, func(t *testing.T) {
			destination := snapshotDest(t, fixtureName+".zeroalloc")
			result, err := SnapshotTo(fixture(t, fixtureName), SnapshotSourceImmutable, destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
			if err != nil {
				t.Fatal("snapshot:", err)
			}
			if result.Publication.Publication != PublicationPublished {
				t.Fatalf("status = %v", result.Publication.Publication)
			}
			output := openPublished(t, destination)
			defer output.Close()
			pin, err := output.Pin()
			if err != nil {
				t.Fatal("pin:", err)
			}
			defer pin.Close()

			switch fixtureName {
			case "direct-ipv4.iprdb":
				probes := []IPv4{IPv4(0x0a00000a), IPv4(0x0a00000f), IPv4(0x0a00001c), IPv4(0x0a00001f)}
				for _, ip := range probes {
					output.LookupDirectV4(ip)
				}
				if allocations := testing.AllocsPerRun(100, func() {
					for _, ip := range probes {
						output.LookupDirectV4(ip)
					}
				}); allocations != 0 {
					t.Errorf("direct snapshot lookups allocate %v bytes/run", allocations)
				}
			case "membership-ipv4.iprdb":
				probes := []IPv4{IPv4(0x0a000005), IPv4(0x0a000080), IPv4(0x0a000100)}
				for _, ip := range probes {
					pin.LookupMembershipV4(ip)
				}
				if allocations := testing.AllocsPerRun(100, func() {
					for _, ip := range probes {
						if _, _, err := pin.LookupMembershipV4(ip); err != nil {
							t.Fatal(err)
						}
					}
				}); allocations != 0 {
					t.Errorf("membership snapshot lookups allocate %v bytes/run", allocations)
				}
			case "structured-ipv4.iprdb":
				probes := []IPv4{IPv4(0x0a010010), IPv4(0x0a010070), IPv4(0x0a0100f0)}
				for _, ip := range probes {
					pin.LookupNetworkEnrichmentV1V4(ip)
				}
				if allocations := testing.AllocsPerRun(100, func() {
					for _, ip := range probes {
						if _, _, err := pin.LookupNetworkEnrichmentV1V4(ip); err != nil {
							t.Fatal(err)
						}
					}
				}); allocations != 0 {
					t.Errorf("structured snapshot lookups allocate %v bytes/run", allocations)
				}
			}
		})
	}
}
