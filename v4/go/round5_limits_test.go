package iprangedb

import (
	"math"
	"testing"
)

func round5ActiveMetaPage(t *testing.T, image []byte) ([]byte, meta) {
	t.Helper()
	first := decodeMeta(image[:PageSize])
	second := decodeMeta(image[PageSize : 2*PageSize])
	if first.txnID >= second.txnID {
		return image[:PageSize], first
	}
	return image[PageSize : 2*PageSize], second
}

func round5MaxScopeIDImage(t *testing.T) []byte {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.ScopeIntern([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(10, 20, id); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	_, m := round5ActiveMetaPage(t, image)
	scope := image[int(m.scopeTableRoot)*PageSize : int(m.scopeTableRoot+1)*PageSize]
	if decodeHeader(scope).pageType != PageTypeScopeLeaf {
		t.Fatal("fixture scope root is not a leaf")
	}
	putU32(scope, PageHeaderSize, math.MaxUint32)
	finalizeChecksum(scope)
	data := image[int(m.rootPgno)*PageSize : int(m.rootPgno+1)*PageSize]
	if decodeHeader(data).pageType != PageTypeLeaf {
		t.Fatal("fixture data root is not a leaf")
	}
	putU32(data, PageHeaderSize+2*int(m.keyWidth), math.MaxUint32)
	finalizeChecksum(data)
	return image
}

func round5OpenWriterWithoutPanic(image []byte) (writer *Writer[Ipv4Key], err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	writer, err = openWriter[Ipv4Key](newVecPageStore(image))
	return writer, err, nil
}

func TestRound5ScopeIDExhaustionReturnsErrorWithoutLosingReadableScopes(t *testing.T) {
	image := round5MaxScopeIDImage(t)
	r, err := Open(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("valid max-scope image rejected: %v", err)
	}
	if got := r.ScopeResolve(math.MaxUint32); len(got) != 1 || got[0] != 1 {
		t.Fatalf("max scope resolved to %v", got)
	}

	w, err, panicValue := round5OpenWriterWithoutPanic(append([]byte(nil), image...))
	if panicValue != nil {
		t.Fatalf("writable open panicked at scope_id exhaustion: %v", panicValue)
	}
	if err != nil {
		t.Fatalf("writable open rejected readable max-scope image: %v", err)
	}
	if id, err := w.ScopeIntern([]byte{1}); err != nil || id != math.MaxUint32 {
		t.Fatalf("existing max scope re-intern=(%d,%v)", id, err)
	}
	if id, err := w.ScopeIntern([]byte{2}); err == nil {
		t.Fatalf("scope exhaustion minted reserved/wrapped scope_id %d", id)
	}
}

func TestRound5TransactionIDExhaustionReturnsErrorInsteadOfWrapping(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(1, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	active, _ := round5ActiveMetaPage(t, image)
	putU64(active, MetaTxnID, math.MaxUint64)
	finalizeChecksum(active)

	w, err, panicValue := round5OpenWriterWithoutPanic(image)
	if panicValue != nil || err != nil {
		t.Fatalf("open at max txn_id panic=%v err=%v", panicValue, err)
	}
	if err := w.Set(2, 2, 2); err != nil {
		t.Fatal(err)
	}
	var commitPanic any
	func() {
		defer func() { commitPanic = recover() }()
		err = w.Commit(2, math.MaxUint64)
	}()
	if commitPanic != nil {
		t.Fatalf("Commit panicked at txn_id exhaustion: %v", commitPanic)
	}
	if err == nil {
		t.Fatal("Commit wrapped txn_id after the maximum generation")
	}
}
