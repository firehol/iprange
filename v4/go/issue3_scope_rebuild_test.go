package iprangedb

import "testing"

// Issue 3 guard: a commit that performs ONLY record mutations (no scope_intern
// / no feed-bit ops) must NOT rebuild the scope table. scopeDirty stays false,
// so the commit takes the fast path and the committed scope root is unchanged.
//
// This documents the accepted design: scope-table rebuild happens only when a
// new scope is (or may be) created, which is rare in production (new feed
// combinations). build_scope_tree already reuses freed pages via free_pool, so
// the O(S) sort+build only runs when genuinely needed.
func TestIssue3_RecordOnlyCommitDoesNotRebuildScope(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a few scopes + records and commit.
	id1, _ := w.ScopeIntern([]byte{0x01})
	id2, _ := w.ScopeIntern([]byte{0x02})
	if err := w.Set(Ipv4Key(10), Ipv4Key(20), id1); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(30), Ipv4Key(40), id2); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	rootBefore := w.scopeRoot()
	if rootBefore == 0 {
		t.Fatal("expected a non-empty scope table after seeding")
	}
	scopePagesBefore := w.ScopePageCount()

	// Record-only mutations: delete + set. No scope ops → scopeDirty must stay false.
	if _, err := w.Delete(Ipv4Key(10), Ipv4Key(20)); err != nil {
		t.Fatal(err)
	}
	if err := w.Set(Ipv4Key(50), Ipv4Key(60), id2); err != nil {
		t.Fatal(err)
	}
	if w.scopeDirty {
		t.Fatal("record-only ops set scopeDirty — scope table would be needlessly rebuilt")
	}
	if err := w.Commit(2, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	// Root unchanged ⇒ no rebuild happened.
	if w.scopeRoot() != rootBefore {
		t.Fatalf("record-only commit rebuilt scope table: root %d -> %d", rootBefore, w.scopeRoot())
	}
	if w.ScopePageCount() != scopePagesBefore {
		t.Fatalf("scope page count changed across record-only commit: %d -> %d", scopePagesBefore, w.ScopePageCount())
	}
}
