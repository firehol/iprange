package iprangedb

import (
	"bytes"
	"testing"
)

// v4.1 metadata READ tests on *Reader (§C.2/§C.4). They build an image with a Writer, Open it
// as a read-only Reader, and assert the Reader's metadata getters descend the on-disk committed
// scope table + per-scope KV trees and return exactly what the Writer's reads return for the
// same image. Also: missing scope/key ⇒ not-found, an overflow-spanning value reads back
// identical, the FILE(0) target, and a v4.0 image (no metadata) ⇒ empty/not-found, no panic.

// openReader builds the writer's committed image and opens it as a validated Reader.
func openReader(t *testing.T, w *Writer[Ipv4Key]) *Reader {
	t.Helper()
	img := append([]byte(nil), w.Image()...)
	r, err := Open(img)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

func TestReaderScopeReadsMatchWriter(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("feed-a"))
	must(t, err)
	b, err := w.ScopeDefine([]byte("feed-b"))
	must(t, err)
	c, err := w.ScopeDefine([]byte("feed-c"))
	must(t, err)
	if _, err := w.ScopeSetType(a, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeSetVersion(a, 42); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeSetVersion(b, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ScopeSetType(c, 255); err != nil {
		t.Fatal(err)
	}
	must(t, w.Set(wk(10), wk(20), []byte{1})) // IP tree coexists
	must(t, w.Commit(1))

	// The Writer's own reads are the oracle: reopen the committed image as a Writer too.
	img := append([]byte(nil), w.Image()...)
	oracle, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	r := openReader(t, w)

	// ScopeList: same length and same (id, name) pairs, in id order.
	wl, rl := oracle.ScopeList(), r.ScopeList()
	if len(wl) != len(rl) || len(rl) != 3 {
		t.Fatalf("ScopeList len reader=%d writer=%d want 3", len(rl), len(wl))
	}
	for i := range wl {
		if wl[i].ID != rl[i].ID || !bytes.Equal(wl[i].Name, rl[i].Name) {
			t.Fatalf("ScopeList[%d] reader=%+v writer=%+v", i, rl[i], wl[i])
		}
	}

	// Per-scope getters agree for every defined scope.
	for _, id := range []uint32{a, b, c} {
		wn, wok := oracle.ScopeName(id)
		rn, rok := r.ScopeName(id)
		if wok != rok || !bytes.Equal(wn, rn) {
			t.Fatalf("ScopeName(%d) reader=(%q,%v) writer=(%q,%v)", id, rn, rok, wn, wok)
		}
		wv, wvok := oracle.ScopeVersion(id)
		rv, rvok := r.ScopeVersion(id)
		if wvok != rvok || wv != rv {
			t.Fatalf("ScopeVersion(%d) reader=(%d,%v) writer=(%d,%v)", id, rv, rvok, wv, wvok)
		}
		wt, wtok := oracle.ScopeType(id)
		rt, rtok := r.ScopeType(id)
		if wtok != rtok || wt != rt {
			t.Fatalf("ScopeType(%d) reader=(%d,%v) writer=(%d,%v)", id, rt, rtok, wt, wtok)
		}
	}

	// Spot-check concrete values.
	if v, ok := r.ScopeVersion(a); !ok || v != 42 {
		t.Fatalf("reader ScopeVersion(a) = %d ok=%v want 42", v, ok)
	}
	if v, ok := r.ScopeType(c); !ok || v != 255 {
		t.Fatalf("reader ScopeType(c) = %d ok=%v want 255", v, ok)
	}
}

func TestReaderMetaReadsMatchWriter(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("feed-a"))
	must(t, err)
	// KV on a defined scope and on FILE (target 0), text + binary types.
	must(t, w.MetaSet(a, []byte("license"), 0, []byte("MIT")))
	must(t, w.MetaSet(a, []byte("category"), 0, []byte("malware")))
	must(t, w.MetaSet(a, []byte("blob"), 7, []byte{0, 1, 2, 0xff}))
	must(t, w.MetaSet(fileScopeID, []byte("dataset"), 0, []byte("blocklist-ipsets")))
	must(t, w.MetaSet(fileScopeID, []byte("rows"), 5, []byte{0xde, 0xad}))
	must(t, w.Set(wk(10), wk(20), []byte{1}))
	must(t, w.Commit(1))

	img := append([]byte(nil), w.Image()...)
	oracle, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	r := openReader(t, w)

	for _, target := range []uint32{a, fileScopeID} {
		wList, err := oracle.MetaList(target)
		must(t, err)
		rList, err := r.MetaList(target)
		must(t, err)
		if len(wList) != len(rList) {
			t.Fatalf("MetaList(%d) len reader=%d writer=%d", target, len(rList), len(wList))
		}
		for i := range wList {
			if !bytes.Equal(wList[i].Key, rList[i].Key) || wList[i].Type != rList[i].Type ||
				!bytes.Equal(wList[i].Value, rList[i].Value) {
				t.Fatalf("MetaList(%d)[%d] reader=%+v writer=%+v", target, i, rList[i], wList[i])
			}
			// MetaGet must agree with the listed entry.
			typ, val, found, err := r.MetaGet(target, rList[i].Key)
			must(t, err)
			if !found || typ != rList[i].Type || !bytes.Equal(val, rList[i].Value) {
				t.Fatalf("MetaGet(%d,%q) reader=(%d,%q,%v)", target, rList[i].Key, typ, val, found)
			}
		}
	}

	// Spot-check concrete values via the Reader.
	if typ, v, ok, _ := r.MetaGet(a, []byte("license")); !ok || typ != 0 || !bytes.Equal(v, []byte("MIT")) {
		t.Fatalf("reader MetaGet(a, license) = (%d,%q,%v)", typ, v, ok)
	}
	if typ, v, ok, _ := r.MetaGet(a, []byte("blob")); !ok || typ != 7 || !bytes.Equal(v, []byte{0, 1, 2, 0xff}) {
		t.Fatalf("reader MetaGet(a, blob) = (%d,%q,%v)", typ, v, ok)
	}
	if typ, v, ok, _ := r.MetaGet(fileScopeID, []byte("dataset")); !ok || typ != 0 || !bytes.Equal(v, []byte("blocklist-ipsets")) {
		t.Fatalf("reader MetaGet(FILE, dataset) = (%d,%q,%v)", typ, v, ok)
	}
}

func TestReaderMetaMissingScopeAndKey(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	must(t, w.MetaSet(a, []byte("present"), 0, []byte("yes")))
	must(t, w.Commit(1))
	r := openReader(t, w)

	// Missing scope id ⇒ all scope getters report not-found.
	if _, ok := r.ScopeName(999); ok {
		t.Fatal("ScopeName(missing) reported found")
	}
	if _, ok := r.ScopeVersion(999); ok {
		t.Fatal("ScopeVersion(missing) reported found")
	}
	if _, ok := r.ScopeType(999); ok {
		t.Fatal("ScopeType(missing) reported found")
	}
	// FILE (scope_id 0) is not a defined scope for the scope getters.
	if _, ok := r.ScopeName(fileScopeID); ok {
		t.Fatal("ScopeName(FILE) must report not-found")
	}

	// Missing key on an existing scope ⇒ found=false, empty value.
	typ, v, found, err := r.MetaGet(a, []byte("absent"))
	must(t, err)
	if found || typ != 0 || len(v) != 0 {
		t.Fatalf("MetaGet(a, absent) = (%d,%q,%v) want not-found", typ, v, found)
	}
	// MetaGet/MetaList on a missing target ⇒ not-found / empty.
	if _, _, found, err := r.MetaGet(999, []byte("present")); err != nil || found {
		t.Fatalf("MetaGet(missing target) found=%v err=%v", found, err)
	}
	list, err := r.MetaList(999)
	must(t, err)
	if len(list) != 0 {
		t.Fatalf("MetaList(missing target) len = %d, want 0", len(list))
	}
}

func TestReaderMetaOverflowSpanningValue(t *testing.T) {
	w := CreateV4(1, 0)
	a, err := w.ScopeDefine([]byte("a"))
	must(t, err)
	// A value larger than one overflow page's payload forces a multi-page chain.
	big := make([]byte, 3*overflowPayload+17)
	for i := range big {
		big[i] = byte(i*31 + 7)
	}
	must(t, w.MetaSet(a, []byte("bigblob"), 9, big))
	must(t, w.MetaSet(fileScopeID, []byte("bigtext"), 0, bytes.Repeat([]byte("x"), kvInlineMax+1)))
	must(t, w.Commit(1))

	img := append([]byte(nil), w.Image()...)
	oracle, err := OpenImageV4(img)
	if err != nil {
		t.Fatal(err)
	}
	r := openReader(t, w)

	// Overflow-spanning binary value reads back byte-identical via the Reader and matches the Writer.
	typ, v, found, err := r.MetaGet(a, []byte("bigblob"))
	must(t, err)
	if !found || typ != 9 || !bytes.Equal(v, big) {
		t.Fatalf("reader MetaGet(a, bigblob): found=%v typ=%d len=%d (want %d)", found, typ, len(v), len(big))
	}
	wTyp, wVal, wFound, err := oracle.MetaGet(a, []byte("bigblob"))
	must(t, err)
	if wFound != found || wTyp != typ || !bytes.Equal(wVal, v) {
		t.Fatal("reader/writer disagree on overflow value")
	}
	// Overflow text value on FILE.
	if _, v, ok, _ := r.MetaGet(fileScopeID, []byte("bigtext")); !ok || len(v) != kvInlineMax+1 {
		t.Fatalf("reader MetaGet(FILE, bigtext) len = %d ok=%v", len(v), ok)
	}
}

func TestReaderMetaOnV40ImageIsEmpty(t *testing.T) {
	// A v4.0 image has no metadata (scope_table_root == 0): all reads return empty/not-found
	// and never panic.
	w := CreateV4(1, 0)
	must(t, w.Set(wk(1), wk(2), []byte{1}))
	must(t, w.Commit(1))
	if activeMetaOf(w.Image()).scopeTableRoot != 0 {
		t.Fatal("expected a v4.0 image (scope_table_root == 0)")
	}
	r := openReader(t, w)

	if list := r.ScopeList(); len(list) != 0 {
		t.Fatalf("ScopeList on v4.0 = %d entries, want 0", len(list))
	}
	if _, ok := r.ScopeName(1); ok {
		t.Fatal("ScopeName on v4.0 reported found")
	}
	if _, ok := r.ScopeVersion(1); ok {
		t.Fatal("ScopeVersion on v4.0 reported found")
	}
	if _, ok := r.ScopeType(1); ok {
		t.Fatal("ScopeType on v4.0 reported found")
	}
	typ, v, found, err := r.MetaGet(fileScopeID, []byte("anything"))
	must(t, err)
	if found || typ != 0 || len(v) != 0 {
		t.Fatalf("MetaGet on v4.0 = (%d,%q,%v), want not-found", typ, v, found)
	}
	list, err := r.MetaList(fileScopeID)
	must(t, err)
	if len(list) != 0 {
		t.Fatalf("MetaList on v4.0 = %d entries, want 0", len(list))
	}
}
