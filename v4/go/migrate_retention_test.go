package iprangedb

import (
	"math"
	"testing"
)

// ── MigrateRetention: Mode 0 timestamp preservation ───────────────────────
//
// scope_id is treated as a unix timestamp. On a scope mismatch over an
// overlapping region, MigrateRetention keeps min(old, new) — the older
// timestamp wins, so a record is only rewritten when the desired stream
// carries an older timestamp than what is already stored.

func TestRetentionKeepsOlderWhenDesiredIsNewer(t *testing.T) {
	// Stored ts=100 is OLDER than desired ts=200. Keep min=100 → no rewrite.
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 100)
	w.Commit(0, math.MaxUint64)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 200},
	})
	counters, err := MigrateRetention(w, desired)
	if err != nil {
		t.Fatal(err)
	}
	if counters.Changed != 1 {
		t.Fatalf("changed=%d, want 1", counters.Changed)
	}
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if s, ok := r.LookupV4(Ipv4Key(15)); !ok || s != 100 {
		t.Fatalf("lookup(15)=%d,%v, want 100,true (older timestamp survives)", s, ok)
	}
}

func TestRetentionOverwritesWhenDesiredIsOlder(t *testing.T) {
	// Stored ts=200 is NEWER than desired ts=100. Keep min=100 → rewrite.
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 200)
	w.Commit(0, math.MaxUint64)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 100},
	})
	if _, err := MigrateRetention(w, desired); err != nil {
		t.Fatal(err)
	}
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if s, ok := r.LookupV4(Ipv4Key(15)); !ok || s != 100 {
		t.Fatalf("lookup(15)=%d,%v, want 100,true (older desired wins)", s, ok)
	}
}

func TestRetentionPartialOverlapPreservesOldOnOverlap(t *testing.T) {
	// Old [10-30] ts=100, desired [15-25] ts=300.
	// - old-only [10-14] and [26-30] removed (not in desired)
	// - overlap [15-25] keeps min(100,300)=100
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(30), 100)
	w.Commit(0, math.MaxUint64)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(15), To: Ipv4Key(25), ScopeID: 300},
	})
	if _, err := MigrateRetention(w, desired); err != nil {
		t.Fatal(err)
	}
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if s, ok := r.LookupV4(Ipv4Key(20)); !ok || s != 100 {
		t.Fatalf("overlap lookup(20)=%d,%v, want 100,true", s, ok)
	}
	if _, ok := r.LookupV4(Ipv4Key(12)); ok {
		t.Fatal("old-only prefix 12 should be removed")
	}
	if _, ok := r.LookupV4(Ipv4Key(28)); ok {
		t.Fatal("old-only suffix 28 should be removed")
	}
}

func TestRetentionCombineNilIsLegacyOverwrite(t *testing.T) {
	// Sanity: Migrate with nil Combine overwrites (desired wins).
	w, _ := Create[Ipv4Key](0, 0)
	w.Set(Ipv4Key(10), Ipv4Key(20), 100)
	w.Commit(0, math.MaxUint64)

	desired := FromUnsorted([]DesiredRecord[Ipv4Key]{
		{From: Ipv4Key(10), To: Ipv4Key(20), ScopeID: 200},
	})
	if _, err := Migrate(w, desired, nil); err != nil {
		t.Fatal(err)
	}
	w.Commit(0, math.MaxUint64)
	img, _ := w.IntoImage()
	r, _ := Open(img)
	if s, ok := r.LookupV4(Ipv4Key(15)); !ok || s != 200 {
		t.Fatalf("lookup(15)=%d,%v, want 200,true (default overwrites)", s, ok)
	}
}
