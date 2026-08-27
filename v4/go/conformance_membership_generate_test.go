package iprangedb

// Go-produced membership conformance fixture generation (SOW-0027
// milestone 3 slice 3a): the membership-ipv4 and membership-ipv6
// fixtures mirror the Rust generate.rs membership_ipv4 / membership_ipv6
// op sequences exactly, through the public membership transactions and
// the public live SnapshotTo publish (regenPublish), so the corpus
// gains Go-produced membership coverage that both readers verify.

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

// regenMembershipIPv4 writes membership-ipv4.iprdb into dir with the
// exact op sequence of the Rust membership_ipv4 generator
// (generate.rs:213): 70 named feeds committed first, then feed-005
// deleted with its index reused by feed-reused, two membership sets
// applied as Replace over 10.0.0.0/24 and 10.0.1.0/24 with a Union
// carving 10.0.1.0-127, and no metadata.
func regenMembershipIPv4(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("threat-feeds"))
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(t.TempDir(), "live-membership-v4")
	if _, err := CreateLive(live, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLiveWriter(live, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := w.BeginMembershipTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 70; index++ {
		feed, err := tx.EnsureFeed(FeedName(fmt.Sprintf("feed-%03d", index)))
		if err != nil {
			t.Fatal(err)
		}
		if feed.Index() != uint32(index) {
			t.Fatalf("feed %d index = %d, want %d", index, feed.Index(), index)
		}
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = w.BeginMembershipTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	removed, ok, err := tx.LookupFeed(FeedName("feed-005"))
	if err != nil || !ok {
		t.Fatalf("lookup feed-005 = ok %v err %v", ok, err)
	}
	if err := tx.DeleteFeed(removed); err != nil {
		t.Fatal(err)
	}
	reused, err := tx.EnsureFeed(FeedName("feed-reused"))
	if err != nil {
		t.Fatal(err)
	}
	if reused.Index() != 5 {
		t.Fatalf("reused feed index = %d, want 5", reused.Index())
	}
	membership := func(names ...string) MembershipRef {
		t.Helper()
		m, err := tx.EmptyMembership()
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			feed, ok, err := tx.LookupFeed(FeedName(name))
			if err != nil || !ok {
				t.Fatalf("membership lookup %s = ok %v err %v", name, ok, err)
			}
			m, err = tx.AddFeed(m, feed)
			if err != nil {
				t.Fatal(err)
			}
		}
		return m
	}
	a := membership("feed-000", "feed-reused", "feed-063", "feed-064", "feed-069")
	b := membership("feed-001", "feed-065")
	if _, err := tx.ApplyV4(IPv4(0x0a000000), IPv4(0x0a0000ff), a, MembershipReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV4(IPv4(0x0a000100), IPv4(0x0a0001ff), b, MembershipReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV4(IPv4(0x0a000100), IPv4(0x0a00017f), a, MembershipUnion); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	regenPublish(t, live, filepath.Join(dir, "membership-ipv4.iprdb"))
}

// regenMembershipIPv6 writes membership-ipv6.iprdb into dir with the
// exact op sequence of the Rust membership_ipv6 generator
// (generate.rs:274): the global feed replaces the whole IPv6 space, the
// special feed unions over 2001:db8::/64, and the 1 MiB repeated-byte
// metadata is staged through the public transaction (Rust parity:
// generate.rs fixtures carry the raw 0x78 byte run).
func regenMembershipIPv6(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("threat-feeds"))
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(t.TempDir(), "live-membership-v6")
	if _, err := CreateLive(live, AddressFamilyIPv6, ValueKindMembership, StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLiveWriter(live, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginMembershipTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	globalFeed, err := tx.EnsureFeed(FeedName("global"))
	if err != nil {
		t.Fatal(err)
	}
	specialFeed, err := tx.EnsureFeed(FeedName("special"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	global, err := tx.AddFeed(empty, globalFeed)
	if err != nil {
		t.Fatal(err)
	}
	special, err := tx.AddFeed(empty, specialFeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV6(IPv6{}, IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}, global, MembershipReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV6(IPv6{Hi: 0x20010db800000000}, IPv6{Hi: 0x20010db800000000, Lo: 0xffff}, special, MembershipUnion); err != nil {
		t.Fatal(err)
	}
	// The Rust generator writes fixture.metadata.bytes() for this
	// fixture: 1 MiB of byte 0x78 (state "repeat" in cases.json). The
	// Go metadata setter stores exact opaque bytes, so the same run is
	// reproducible here.
	if _, err := tx.SetMetadataJSON(bytes.Repeat([]byte{120}, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	regenPublish(t, live, filepath.Join(dir, "membership-ipv6.iprdb"))
}
