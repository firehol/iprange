package iprangedb

import (
	"bytes"
	"fmt"
	"testing"
)

// v4.1 scope-registry tests — the Go port of the scope tests in
// v4/rust/iprange-livedb/src/writer.rs. They cover CRUD + reopen round-trip, the v4.0→v4.1
// upgrade and the drop-all return to v4.0, a multi-level scope tree, name validation,
// ScopeDrop(0) rejection, and monotonic non-reused scope_ids.

// activeMetaOf decodes the active meta (higher txn_id) from a two-meta image.
func activeMetaOf(img []byte) meta {
	a := decodeMeta(img[:pageSize])
	b := decodeMeta(img[pageSize : 2*pageSize])
	if b.txnID > a.txnID {
		return b
	}
	return a
}

func TestScopeRegistryRoundTrip(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("feed-a"))
	must(t, err)
	b, err := w.ScopeDefine([]byte("feed-b"))
	must(t, err)
	if a != 1 || b != 2 { // monotonic; 0 reserved for FILE
		t.Fatalf("ids = %d, %d, want 1, 2", a, b)
	}
	if _, err := w.ScopeSetType(a, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeBumpVersion(a); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeBumpVersion(a); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeSetVersion(b, 100); err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(wk(10), wk(20), []byte{7})) // IP tree coexists
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)

	r, err := Open(img) // validates the v4.1 file incl. scope table
	if err != nil {
		t.Fatal(err)
	}
	if s, ok, _ := r.LookupV4(wk(15)); !ok || s[0] != 7 {
		t.Fatal("IP record lost")
	}

	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := w2.ScopeName(a); !ok || !bytes.Equal(n, []byte("feed-a")) {
		t.Fatalf("name a = %q ok=%v", n, ok)
	}
	if n, ok := w2.ScopeName(b); !ok || !bytes.Equal(n, []byte("feed-b")) {
		t.Fatalf("name b = %q ok=%v", n, ok)
	}
	if v, ok := w2.ScopeType(a); !ok || v != 2 {
		t.Fatalf("type a = %d ok=%v", v, ok)
	}
	if v, ok := w2.ScopeVersion(a); !ok || v != 2 {
		t.Fatalf("version a = %d ok=%v", v, ok)
	}
	if v, ok := w2.ScopeVersion(b); !ok || v != 100 {
		t.Fatalf("version b = %d ok=%v", v, ok)
	}
	if len(w2.ScopeList()) != 2 {
		t.Fatalf("list len = %d", len(w2.ScopeList()))
	}
	if _, ok := w2.ScopeName(999); ok {
		t.Fatal("missing scope must report not-found")
	}
}

func TestMetadataUpgradesToV41AndEmptyStaysV40(t *testing.T) {
	w := CreateV4(1, 0)
	must(t, w.Set(wk(1), wk(2), []byte{1}))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	active := activeMetaOf(img)
	if active.versionMinor != versionMinor { // no metadata ⇒ v4.0
		t.Fatalf("minor = %d, want %d", active.versionMinor, versionMinor)
	}
	if active.scopeTableRoot != 0 {
		t.Fatalf("scope_table_root = %d, want 0", active.scopeTableRoot)
	}

	if _, err := w.ScopeDefine([]byte("x")); err != nil {
		t.Fatal(err)
	}
	must(t, w.Commit(2))
	img = append([]byte(nil), w.Image()...)
	active = activeMetaOf(img)
	if active.versionMinor != versionMinorMetadata {
		t.Fatalf("minor = %d, want %d", active.versionMinor, versionMinorMetadata)
	}
	if active.metaSize != metaSizeV41 {
		t.Fatalf("meta_size = %d, want %d", active.metaSize, metaSizeV41)
	}
	if active.scopeTableRoot < 2 {
		t.Fatalf("scope_table_root = %d, want >= 2", active.scopeTableRoot)
	}
	if _, err := Open(img); err != nil {
		t.Fatalf("v4.1 file rejected: %v", err)
	}
}

func TestDroppingAllScopesReturnsToV40(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.Commit(1))
	if _, err := w.ScopeDrop(a); err != nil {
		t.Fatal(err)
	}
	must(t, w.Commit(2))
	img := append([]byte(nil), w.Image()...)
	active := activeMetaOf(img)
	if active.scopeTableRoot != 0 {
		t.Fatalf("scope_table_root = %d, want 0", active.scopeTableRoot)
	}
	// The freed scope-table pages are now on the persisted free-list, so the file may be
	// v4.2 (free_list_head != 0) rather than byte-compatible v4.0. The contract is "no
	// metadata" (scope_table_root == 0); the exact minor depends on whether free pages
	// remain, so only the scope_table_root invariant is asserted here.
	if _, err := Open(img); err != nil {
		t.Fatalf("file rejected after drop-all: %v", err)
	}
}

func TestScopeDropRemovesMetadataAndRejectsFileScope(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	b, err := w.ScopeDefine([]byte("b"))
	must(t, err)
	if _, err := w.ScopeDrop(fileScopeID); err == nil {
		t.Fatal("ScopeDrop(0) must be rejected")
	}
	if existed, err := w.ScopeDrop(a); err != nil || !existed {
		t.Fatalf("drop a: existed=%v err=%v", existed, err)
	}
	if existed, err := w.ScopeDrop(a); err != nil || existed {
		t.Fatalf("re-drop a: existed=%v err=%v (want false)", existed, err)
	}
	c, err := w.ScopeDefine([]byte("c"))
	must(t, err)
	if c != 3 { // dropped id 1 is NOT reused
		t.Fatalf("c = %d, want 3 (ids never reused)", c)
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w2.ScopeName(a); ok {
		t.Fatal("dropped scope a still present")
	}
	if n, ok := w2.ScopeName(b); !ok || !bytes.Equal(n, []byte("b")) {
		t.Fatalf("b name = %q ok=%v", n, ok)
	}
	if n, ok := w2.ScopeName(c); !ok || !bytes.Equal(n, []byte("c")) {
		t.Fatalf("c name = %q ok=%v", n, ok)
	}
}

func TestManyScopesForceScopeTreeAndValidate(t *testing.T) {
	w := CreateV4(1, 0)
	const n = 100 // > scopeLeafMax (14) ⇒ a multi-level scope tree
	for i := uint32(0); i < n; i++ {
		id, err := w.ScopeDefine([]byte(fmt.Sprintf("scope-%d", i)))
		must(t, err)
		if id != i+1 {
			t.Fatalf("id = %d, want %d", id, i+1)
		}
		if _, err := w.ScopeSetVersion(id, uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil { // full validation of the multi-level scope tree
		t.Fatalf("multi-level scope tree rejected: %v", err)
	}
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if len(w2.ScopeList()) != n {
		t.Fatalf("list len = %d, want %d", len(w2.ScopeList()), n)
	}
	if name, ok := w2.ScopeName(50); !ok || !bytes.Equal(name, []byte("scope-49")) {
		t.Fatalf("name 50 = %q ok=%v", name, ok)
	}
	if v, ok := w2.ScopeVersion(100); !ok || v != 99 {
		t.Fatalf("version 100 = %d ok=%v", v, ok)
	}
}

func TestScopeNameValidation(t *testing.T) {
	w := CreateV4(1, 0)
	if _, err := w.ScopeDefine([]byte{0xff, 0xfe}); err == nil { // not UTF-8
		t.Fatal("non-UTF-8 name must be rejected")
	}
	if _, err := w.ScopeDefine(bytes.Repeat([]byte{'a'}, 257)); err == nil { // too long
		t.Fatal("257-byte name must be rejected")
	}
	if _, err := w.ScopeDefine(bytes.Repeat([]byte{'a'}, 256)); err != nil { // exactly 256 OK
		t.Fatalf("256-byte name must be accepted: %v", err)
	}
}

func TestScopeUpdatePreservesKVRoot(t *testing.T) {
	// A header update (version/type/name) must never disturb a scope's kv_root (always 0 in
	// this increment, but the rebuild must carry it through unchanged, §C.2).
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.Commit(1))
	if _, err := w.ScopeBumpVersion(a); err != nil {
		t.Fatal(err)
	}
	must(t, w.Commit(2))
	img := append([]byte(nil), w.Image()...)
	scopes, err := loadAllScopes(img, activeMetaOf(img).scopeTableRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0].kvRoot != 0 {
		t.Fatalf("kv_root not preserved as 0: %+v", scopes)
	}
}
