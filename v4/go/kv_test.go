package iprangedb

import (
	"bytes"
	"fmt"
	"testing"
)

// v4.1 per-scope KV tests (§C.4) — the Go port of the kv tests in
// v4/rust/iprange-livedb/src/writer.rs. They cover CRUD + reopen round-trip (scope + FILE),
// overwrite/delete, multi-page overflow chains, the inline/overflow boundary, empty values,
// a multi-level KV tree, type==0 UTF-8/NUL rejection (inline + overflow-spanning), key
// validation, allocator reclaim across commits, and ScopeDrop freeing KV+overflow.

// metaGetOK is a test helper: MetaGet with a fatal on error, returning (type, value, found).
func metaGetOK(t *testing.T, w *Writer[Ipv4Key], target uint32, key []byte) (uint32, []byte, bool) {
	t.Helper()
	typ, val, found, err := w.MetaGet(target, key)
	must(t, err)
	return typ, val, found
}

func TestKVCRUDAndReopenRoundTrip(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("feed-a"))
	must(t, err)
	// KV on a defined scope and on FILE (target 0).
	must(t, w.MetaSet(a, []byte("license"), 0, []byte("MIT")))
	must(t, w.MetaSet(a, []byte("category"), 0, []byte("malware")))
	must(t, w.MetaSet(fileScopeID, []byte("dataset"), 0, []byte("blocklist-ipsets")))
	// Binary (non-zero type) value stored unchecked.
	must(t, w.MetaSet(a, []byte("blob"), 7, []byte{0, 1, 2, 0xff}))
	must(t, w.Set(wk(10), wk(20), []byte{1})) // IP tree coexists
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)

	r, err := Open(img) // validates KV trees too
	if err != nil {
		t.Fatal(err)
	}
	if s, ok, _ := r.LookupV4(wk(15)); !ok || s[0] != 1 {
		t.Fatal("IP record lost")
	}

	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("license")); !ok || typ != 0 || !bytes.Equal(v, []byte("MIT")) {
		t.Fatalf("license = %d %q ok=%v", typ, v, ok)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("category")); !ok || typ != 0 || !bytes.Equal(v, []byte("malware")) {
		t.Fatalf("category = %d %q ok=%v", typ, v, ok)
	}
	if typ, v, ok := metaGetOK(t, w2, fileScopeID, []byte("dataset")); !ok || typ != 0 || !bytes.Equal(v, []byte("blocklist-ipsets")) {
		t.Fatalf("dataset = %d %q ok=%v", typ, v, ok)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("blob")); !ok || typ != 7 || !bytes.Equal(v, []byte{0, 1, 2, 0xff}) {
		t.Fatalf("blob = %d %v ok=%v", typ, v, ok)
	}
	// Ordered list.
	list, err := w2.MetaList(a)
	must(t, err)
	gotKeys := make([]string, len(list))
	for i := range list {
		gotKeys[i] = string(list[i].Key)
	}
	wantKeys := []string{"blob", "category", "license"}
	if fmt.Sprint(gotKeys) != fmt.Sprint(wantKeys) {
		t.Fatalf("list keys = %v, want %v", gotKeys, wantKeys)
	}
}

func TestKVOverwriteAndDelete(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.MetaSet(a, []byte("k"), 0, []byte("v1")))
	must(t, w.MetaSet(a, []byte("k"), 0, []byte("v2"))) // overwrite same key
	if typ, v, ok := metaGetOK(t, w, a, []byte("k")); !ok || typ != 0 || !bytes.Equal(v, []byte("v2")) {
		t.Fatalf("after overwrite = %d %q ok=%v", typ, v, ok)
	}
	// delete present -> ChangedYes; absent -> Unchanged.
	if c, err := w.MetaDelete(a, []byte("k")); err != nil || c != ChangedYes {
		t.Fatalf("delete present: c=%v err=%v", c, err)
	}
	if _, _, ok := metaGetOK(t, w, a, []byte("k")); ok {
		t.Fatal("k must be gone")
	}
	if c, err := w.MetaDelete(a, []byte("k")); err != nil || c != Unchanged {
		t.Fatalf("re-delete: c=%v err=%v", c, err)
	}
	if c, err := w.MetaDelete(a, []byte("never")); err != nil || c != Unchanged {
		t.Fatalf("delete absent: c=%v err=%v", c, err)
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := metaGetOK(t, w2, a, []byte("k")); ok {
		t.Fatal("k must be gone after reopen")
	}
}

func TestKVLargeValueOverflowChain(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	// A multi-page value (3+ overflow pages). Non-zero type so arbitrary bytes are OK.
	big := make([]byte, overflowPayload*2+123)
	for i := range big {
		big[i] = byte(i*31 + 7)
	}
	must(t, w.MetaSet(a, []byte("payload"), 9, big))
	// A value exactly on the inline/overflow boundary stays inline; one past it spills.
	onBoundary := bytes.Repeat([]byte{0xAB}, kvInlineMax)
	pastBoundary := bytes.Repeat([]byte{0xCD}, kvInlineMax+1)
	must(t, w.MetaSet(a, []byte("edge_in"), 1, onBoundary))
	must(t, w.MetaSet(a, []byte("edge_out"), 1, pastBoundary))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil { // validates the overflow chains
		t.Fatalf("file with overflow rejected: %v", err)
	}
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("payload")); !ok || typ != 9 || !bytes.Equal(v, big) {
		t.Fatalf("payload roundtrip failed: typ=%d ok=%v len=%d", typ, ok, len(v))
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("edge_in")); !ok || typ != 1 || !bytes.Equal(v, onBoundary) {
		t.Fatalf("edge_in roundtrip failed: typ=%d ok=%v len=%d", typ, ok, len(v))
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("edge_out")); !ok || typ != 1 || !bytes.Equal(v, pastBoundary) {
		t.Fatalf("edge_out roundtrip failed: typ=%d ok=%v len=%d", typ, ok, len(v))
	}
}

func TestKVEmptyValueRoundTrips(t *testing.T) {
	// A zero-length value is valid (stored inline) for both text and binary types.
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.MetaSet(a, []byte("empty-text"), 0, []byte{}))
	must(t, w.MetaSet(a, []byte("empty-bin"), 3, []byte{}))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("empty-text")); !ok || typ != 0 || len(v) != 0 {
		t.Fatalf("empty-text = %d %v ok=%v", typ, v, ok)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("empty-bin")); !ok || typ != 3 || len(v) != 0 {
		t.Fatalf("empty-bin = %d %v ok=%v", typ, v, ok)
	}
}

func TestKVManyEntriesForceMultiLevelTree(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	const n = 2000
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("key-%06d", i)
		val := fmt.Sprintf("value-for-%d", i)
		must(t, w.MetaSet(a, []byte(key), 0, []byte(val)))
	}
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if _, err := Open(img); err != nil { // full validation of the multi-level KV tree
		t.Fatalf("multi-level KV tree rejected: %v", err)
	}
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	list, err := w2.MetaList(a)
	must(t, err)
	if len(list) != n {
		t.Fatalf("list len = %d, want %d", len(list), n)
	}
	// spot-check across the tree
	for _, i := range []int{0, 1, 777, 1999} {
		key := fmt.Sprintf("key-%06d", i)
		val := fmt.Sprintf("value-for-%d", i)
		if typ, v, ok := metaGetOK(t, w2, a, []byte(key)); !ok || typ != 0 || !bytes.Equal(v, []byte(val)) {
			t.Fatalf("i=%d get = %d %q ok=%v", i, typ, v, ok)
		}
	}
	// entries returned in key order
	for i := 1; i < len(list); i++ {
		if bytes.Compare(list[i-1].Key, list[i].Key) >= 0 {
			t.Fatalf("keys not sorted at %d", i)
		}
	}
}

func TestKVType0UTF8Validation(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	// type==0 rejects invalid UTF-8 / NUL (inline).
	if err := w.MetaSet(a, []byte("x"), 0, []byte{0xff, 0xfe}); err == nil {
		t.Fatal("non-UTF-8 type-0 value must be rejected")
	}
	if err := w.MetaSet(a, []byte("x"), 0, []byte("has\x00nul")); err == nil {
		t.Fatal("NUL type-0 value must be rejected")
	}
	// type==0 rejects invalid bytes even when the value spans an overflow chain.
	badBig := bytes.Repeat([]byte{'a'}, overflowPayload+10)
	badBig[overflowPayload+5] = 0xff // invalid UTF-8 byte past page 1
	if err := w.MetaSet(a, []byte("x"), 0, badBig); err == nil {
		t.Fatal("non-UTF-8 type-0 value spanning overflow must be rejected")
	}
	// valid UTF-8 text accepted; non-zero type stores arbitrary bytes unchecked.
	must(t, w.MetaSet(a, []byte("ok"), 0, []byte("héllo-✓")))
	must(t, w.MetaSet(a, []byte("bin"), 5, []byte{0xff, 0x00, 0xfe}))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("ok")); !ok || typ != 0 || !bytes.Equal(v, []byte("héllo-✓")) {
		t.Fatalf("ok = %d %q ok=%v", typ, v, ok)
	}
	if typ, v, ok := metaGetOK(t, w2, a, []byte("bin")); !ok || typ != 5 || !bytes.Equal(v, []byte{0xff, 0x00, 0xfe}) {
		t.Fatalf("bin = %d %v ok=%v", typ, v, ok)
	}
}

func TestKVKeyValidationAndMissing(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	if err := w.MetaSet(a, []byte(""), 0, []byte("v")); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if err := w.MetaSet(a, bytes.Repeat([]byte{'k'}, 1025), 0, []byte("v")); err == nil {
		t.Fatal("> 1024-byte key must be rejected")
	}
	if err := w.MetaSet(a, []byte("a\x00b"), 0, []byte("v")); err == nil {
		t.Fatal("NUL in key must be rejected")
	}
	if err := w.MetaSet(a, []byte{0xff, 0xfe}, 0, []byte("v")); err == nil {
		t.Fatal("non-UTF-8 key must be rejected")
	}
	if err := w.MetaSet(a, bytes.Repeat([]byte{'k'}, 1024), 0, []byte("v")); err != nil {
		t.Fatalf("exactly-1024 key must be accepted: %v", err)
	}
	// missing key -> found=false (not an error)
	if _, _, ok := metaGetOK(t, w, a, []byte("absent")); ok {
		t.Fatal("absent key must report not-found")
	}
	// MetaSet on an undefined scope -> InvalidInput
	if err := w.MetaSet(999, []byte("k"), 0, []byte("v")); err == nil {
		t.Fatal("MetaSet on undefined scope must be rejected")
	}
	// FILE target is always valid.
	if err := w.MetaSet(fileScopeID, []byte("k"), 0, []byte("v")); err != nil {
		t.Fatalf("MetaSet on FILE must be accepted: %v", err)
	}
}

func TestKVRewriteReusesFreedPages(t *testing.T) {
	// The KV rebuild frees the old KV+overflow pages at commit (into freedThisTxn), which D7
	// reclaims only at the FOLLOWING commit. So a steady-state rewrite stops growing the file
	// after the second rewrite reuses the pages freed by the first.
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	rewrite := func(txn uint64) {
		for i := 0; i < 500; i++ {
			key := fmt.Sprintf("k%04d", i)
			must(t, w.MetaSet(a, []byte(key), 1, bytes.Repeat([]byte{byte(i & 0xff)}, 600)))
		}
		must(t, w.Commit(txn))
	}
	rewrite(1)
	rewrite(2) // frees txn1's KV pages (reclaimable from txn 3 on)
	pagesAfterSecond := len(w.Image()) / pageSize
	rewrite(3) // reuses pages freed at commit 2
	pagesAfterThird := len(w.Image()) / pageSize
	if pagesAfterThird > pagesAfterSecond+4 {
		t.Fatalf("steady-state KV rewrite must reuse freed pages: %d -> %d", pagesAfterSecond, pagesAfterThird)
	}
	img := append([]byte(nil), w.Image()...)
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	list, err := w2.MetaList(a)
	must(t, err)
	if len(list) != 500 {
		t.Fatalf("list len = %d, want 500", len(list))
	}
}

func TestKVScopeDropFreesKVAndOverflow(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	b, err := w.ScopeDefine([]byte("b"))
	must(t, err)
	// Give a a big KV (forces overflow pages), b a small one.
	must(t, w.MetaSet(a, []byte("big"), 1, bytes.Repeat([]byte{0x5A}, overflowPayload*2)))
	must(t, w.MetaSet(b, []byte("small"), 0, []byte("x")))
	must(t, w.Commit(1))
	// Drop a: its KV + overflow pages are freed at commit 2 (reclaimable from commit 3 on,
	// per D7). Then add data that must reuse them by commit 3.
	if _, err := w.ScopeDrop(a); err != nil {
		t.Fatal(err)
	}
	must(t, w.Commit(2))
	pagesAfterDrop := len(w.Image()) / pageSize
	for i := 0; i < 40; i++ {
		must(t, w.MetaSet(b, []byte(fmt.Sprintf("n%d", i)), 1, bytes.Repeat([]byte{7}, 100)))
	}
	must(t, w.Commit(3))
	pagesAfterRefill := len(w.Image()) / pageSize
	if pagesAfterRefill > pagesAfterDrop+2 {
		t.Fatalf("dropped scope's KV/overflow must be reused: %d -> %d", pagesAfterDrop, pagesAfterRefill)
	}
	img := append([]byte(nil), w.Image()...)
	w2, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w2.ScopeName(a); ok {
		t.Fatal("dropped scope a still present")
	}
	if typ, v, ok := metaGetOK(t, w2, b, []byte("small")); !ok || typ != 0 || !bytes.Equal(v, []byte("x")) {
		t.Fatalf("b/small = %d %q ok=%v", typ, v, ok)
	}
	if _, _, ok := metaGetOK(t, w2, a, []byte("big")); ok {
		t.Fatal("a/big must be gone (scope dropped)")
	}
}

func TestKVManySetsOneRebuildAndFileEmptyReturnsV40(t *testing.T) {
	// Many MetaSet on FILE in one txn must persist (one rebuild), and a file whose only
	// metadata is later removed returns to v4.0.
	w := CreateV4(1, 0)
	must(t, w.MetaSet(fileScopeID, []byte("a"), 0, []byte("1")))
	must(t, w.MetaSet(fileScopeID, []byte("b"), 0, []byte("2")))
	must(t, w.MetaSet(fileScopeID, []byte("c"), 0, []byte("3")))
	must(t, w.Commit(1))
	img := append([]byte(nil), w.Image()...)
	if active := activeMetaOf(img); active.versionMinor != versionMinorMetadata {
		t.Fatalf("minor = %d, want %d", active.versionMinor, versionMinorMetadata)
	}
	list, err := w.MetaList(fileScopeID)
	must(t, err)
	if len(list) != 3 {
		t.Fatalf("FILE list len = %d, want 3", len(list))
	}

	// Delete all FILE keys -> FILE record dropped -> back to v4.0.
	if _, err := w.MetaDelete(fileScopeID, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.MetaDelete(fileScopeID, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.MetaDelete(fileScopeID, []byte("c")); err != nil {
		t.Fatal(err)
	}
	must(t, w.Commit(2))
	img = append([]byte(nil), w.Image()...)
	active := activeMetaOf(img)
	if active.scopeTableRoot != 0 {
		t.Fatalf("scope_table_root = %d, want 0", active.scopeTableRoot)
	}
	if active.versionMinor != versionMinor {
		t.Fatalf("minor = %d, want %d (byte-compatible v4.0)", active.versionMinor, versionMinor)
	}
	if _, err := Open(img); err != nil {
		t.Fatalf("file rejected after FILE drop-all: %v", err)
	}
}
