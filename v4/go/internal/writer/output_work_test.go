//go:build v4work

// Work-counter pins for the immutable output path (Rust
// immutable_output_tests.rs): the interned reference run applies one
// refcount batch and exactly one dictionary lookup, and every output
// build counts one output pass over the pushed ranges.

package writer_test

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/work"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// TestOutputMembershipReferenceRunCounts pins
// membership_refcount_batches == 1 and membership_lookups == 1 for the
// intern-once + 512-interned-pushes run (Rust
// repeated_immutable_membership_references_are_applied_once). The single
// lookup is the refcount apply on flush: the batch deduplicates all 512
// references to one delta, and the intern itself performs no counted
// lookup for a fresh bitmap.
func TestOutputMembershipReferenceRunCounts(t *testing.T) {
	work.Reset()
	b, _ := newOutput(t, membershipSpec(1), generousBudget())
	if err := b.PushFeed("feed", 0); err != nil {
		t.Fatalf("push feed: %v", err)
	}
	if _, err := b.InternMembership(writer.OutputWords{1}); err != nil {
		t.Fatalf("intern: %v", err)
	}
	for index := uint32(0); index < 512; index++ {
		address := index * 2
		if err := b.PushInternedMembershipV4(address, address, 1); err != nil {
			t.Fatalf("push %d: %v", index, err)
		}
	}
	if err := b.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	b.Close()

	snapshot := work.Read()
	if delta := snapshot.MembershipRefcountBatches; delta != 1 {
		t.Fatalf("membership refcount batches %d, want 1", delta)
	}
	if delta := snapshot.MembershipLookups; delta != 1 {
		t.Fatalf("membership lookups %d, want 1", delta)
	}
	if delta := snapshot.RangesEmitted; delta != 512 {
		t.Fatalf("ranges emitted %d, want 512", delta)
	}
	if delta := snapshot.OutputPasses; delta != 1 {
		t.Fatalf("output passes %d, want 1", delta)
	}
	if delta := snapshot.CatalogInterns; delta != 1 {
		t.Fatalf("catalog interns %d, want 1", delta)
	}
}

// TestOutputMembershipInternCounts pins one intern per pushed membership
// range and one interned-record reuse for the repeated wide bitmap (Rust
// membership_output_streams_sparse_words: entry count 2 after three
// pushes, the third reusing the stored record).
func TestOutputMembershipInternCounts(t *testing.T) {
	work.Reset()
	b, _ := newOutput(t, membershipSpec(32_002), generousBudget())
	for _, feed := range []struct {
		name  string
		index uint32
	}{{"alpha", 3}, {"middle", 31_999}, {"omega", 32_001}} {
		if err := b.PushFeed(feed.name, feed.index); err != nil {
			t.Fatalf("push feed %s: %v", feed.name, err)
		}
	}
	wide := make(writer.OutputWords, 501)
	wide[0] = 1 << 3
	wide[499] = 1 << 63
	wide[500] = 1 << 1
	if err := b.PushMembershipV4(0, 9, wide); err != nil {
		t.Fatalf("push wide: %v", err)
	}
	if err := b.PushMembershipV4(10, 19, writer.OutputWords{1 << 3}); err != nil {
		t.Fatalf("push alpha: %v", err)
	}
	if err := b.PushMembershipV4(30, 39, wide); err != nil {
		t.Fatalf("push wide again: %v", err)
	}
	if err := b.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	b.Close()

	snapshot := work.Read()
	if delta := snapshot.MembershipInterns; delta != 3 {
		t.Fatalf("membership interns %d, want 3", delta)
	}
	if delta := snapshot.MembershipLookups; delta != 3 {
		t.Fatalf("membership lookups %d, want 3 (one refcount apply per range)", delta)
	}
	if delta := snapshot.OutputPasses; delta != 1 {
		t.Fatalf("output passes %d, want 1", delta)
	}
}
