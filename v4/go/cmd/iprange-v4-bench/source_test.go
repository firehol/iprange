package main

import (
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// TestRequireAddressSpace pins the Rust require_address_space guard
// (source.rs): the maximum accepted count+phase is (u32::MAX-1)/4+1
// = 2^30. Anything above wraps the uint32 address arithmetic of the
// dispersed sources, so it must refuse instead of measuring a wrong
// workload (bench-only guard; the matrix itself stays far below).
func TestRequireAddressSpace(t *testing.T) {
	cases := []struct {
		count int
		phase uint32
		ok    bool
	}{
		{1, 0, true},
		{1_000_000, 421, true},
		{1 << 30, 0, true},
		{1<<30 - 1, 1, true},
		{0, 0, false},
		{1 << 30, 1, false},
		{1<<30 + 1, 0, false},
		{2_000_000_000, 0, false},
		{1, 0xffffffff, false},
	}
	for _, c := range cases {
		err := requireAddressSpace(c.count, c.phase)
		if (err == nil) != c.ok {
			t.Fatalf("requireAddressSpace(%d, %d) = %v, want ok=%v", c.count, c.phase, err, c.ok)
		}
	}
}

// TestStreamingSourcesReuseBatches pins the Rust shared [T;1024]
// buffer design: the streaming sources must not allocate a fresh
// batch per call inside the measured region (harness allocation
// facts stay near zero on streaming scenarios).
func TestStreamingSourcesReuseBatches(t *testing.T) {
	direct, err := newDirectSource(100_000)
	if err != nil {
		t.Fatal(err)
	}
	var first []iprangedb.DirectRangeV4
	for {
		batch, more := direct.nextBatch()
		if !more {
			break
		}
		if first == nil {
			first = batch
		} else if &batch[0] != &first[0] {
			t.Fatal("directSource.nextBatch changed its backing buffer")
		}
	}
	nested, err := newNestedSource(100_000)
	if err != nil {
		t.Fatal(err)
	}
	first = nil
	for {
		batch, more := nested.nextBatch()
		if !more {
			break
		}
		if first == nil {
			first = batch
		} else if &batch[0] != &first[0] {
			t.Fatal("nestedSource.nextBatch changed its backing buffer")
		}
	}
	address, err := newAddressSource(100_000, 7)
	if err != nil {
		t.Fatal(err)
	}
	var firstAddress []iprangedb.AddressRange4
	for {
		batch, more := address.nextBatch()
		if !more {
			break
		}
		if firstAddress == nil {
			firstAddress = batch
		} else if &batch[0] != &firstAddress[0] {
			t.Fatal("addressSource.nextBatch changed its backing buffer")
		}
	}
	shape, err := newFeedShapeSource(100_000, feedRandomDisjoint)
	if err != nil {
		t.Fatal(err)
	}
	firstAddress = nil
	for {
		batch, more := shape.nextBatch()
		if !more {
			break
		}
		if firstAddress == nil {
			firstAddress = batch
		} else if &batch[0] != &firstAddress[0] {
			t.Fatal("feedShapeSource.nextBatch changed its backing buffer")
		}
	}
}
