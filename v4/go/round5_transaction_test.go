package iprangedb

import (
	"errors"
	"math"
	"testing"
)

var (
	round5ErrAlloc    = errors.New("round5 injected allocation failure")
	round5ErrSync     = errors.New("round5 injected sync failure")
	round5ErrTruncate = errors.New("round5 injected truncate failure")
)

type round5FaultStore struct {
	*vecPageStore
	events       []string
	failAlloc    bool
	failSync     bool
	failTruncate bool
}

func (s *round5FaultStore) pageMut(pgno uint32) []byte {
	if pgno < 2 {
		s.events = append(s.events, "meta")
	}
	return s.vecPageStore.pageMut(pgno)
}

func (s *round5FaultStore) allocPage() (uint32, error) {
	if s.failAlloc {
		s.failAlloc = false
		return 0, round5ErrAlloc
	}
	return s.vecPageStore.allocPage()
}

func (s *round5FaultStore) sync() error {
	s.events = append(s.events, "sync")
	if s.failSync {
		s.failSync = false
		return round5ErrSync
	}
	return nil
}

func (s *round5FaultStore) truncate(newTotalPages uint32) error {
	s.events = append(s.events, "truncate")
	if s.failTruncate {
		s.failTruncate = false
		return round5ErrTruncate
	}
	return s.vecPageStore.truncate(newTotalPages)
}

func round5ScalarTreeImage(t *testing.T, records uint32) []byte {
	t.Helper()
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < records; i++ {
		if err := w.Set(Ipv4Key(i*2), Ipv4Key(i*2), 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := w.IntoImage()
	if !ok {
		t.Fatal("missing committed image")
	}
	return image
}

func round5OpenFaultWriter(t *testing.T, image []byte) (*Writer[Ipv4Key], *round5FaultStore) {
	t.Helper()
	store := &round5FaultStore{vecPageStore: newVecPageStore(append([]byte(nil), image...))}
	w, err := openWriter[Ipv4Key](store)
	if err != nil {
		t.Fatal(err)
	}
	return w, store
}

func TestRound5CommitPublishesDurableMetadataBeforePhysicalTruncation(t *testing.T) {
	w, store := round5OpenFaultWriter(t, round5ScalarTreeImage(t, 800))
	if _, err := w.Delete(0, math.MaxUint32); err != nil {
		t.Fatal(err)
	}
	store.events = nil
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}

	truncateAt := -1
	metaAt := -1
	lastSyncAfterMeta := -1
	for i, event := range store.events {
		switch event {
		case "truncate":
			if truncateAt < 0 {
				truncateAt = i
			}
		case "meta":
			if metaAt < 0 {
				metaAt = i
			}
		case "sync":
			if metaAt >= 0 {
				lastSyncAfterMeta = i
			}
		}
	}
	if truncateAt < 0 {
		t.Fatalf("fixture did not exercise truncation; events=%v", store.events)
	}
	if metaAt < 0 || lastSyncAfterMeta < 0 || truncateAt < lastSyncAfterMeta {
		t.Fatalf("commit events=%v, want data sync -> metadata publication -> metadata sync -> truncate", store.events)
	}
}

func TestRound5PreviousGenerationSurvivesSyncFailureAfterTruncation(t *testing.T) {
	w, store := round5OpenFaultWriter(t, round5ScalarTreeImage(t, 800))
	if _, err := w.Delete(0, math.MaxUint32); err != nil {
		t.Fatal(err)
	}
	store.failSync = true
	if err := w.Commit(2, math.MaxUint64); !errors.Is(err, round5ErrSync) {
		t.Fatalf("Commit error=%v, want injected sync failure", err)
	}

	r, err := Open(store.image)
	if err != nil {
		t.Fatalf("previous generation cannot be opened after failed commit: %v", err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("previous generation is invalid after failed commit: %v", err)
	}
	if r.RecordCount() != 800 {
		t.Fatalf("previous generation record count=%d, want 800", r.RecordCount())
	}
	for _, ip := range []Ipv4Key{0, 798, 1598} {
		if scope, ok := r.LookupV4(ip); !ok || scope != 1 {
			t.Fatalf("previous generation lookup(%d)=(%d,%v), want (1,true)", ip, scope, ok)
		}
	}
}

func TestRound5TruncateFailurePoisonsWriter(t *testing.T) {
	w, store := round5OpenFaultWriter(t, round5ScalarTreeImage(t, 800))
	if _, err := w.Delete(0, math.MaxUint32); err != nil {
		t.Fatal(err)
	}
	store.failTruncate = true
	if err := w.Commit(2, math.MaxUint64); !errors.Is(err, round5ErrTruncate) {
		t.Fatalf("Commit error=%v, want injected truncate failure", err)
	}
	if err := w.Set(1, 1, 2); err == nil {
		t.Fatal("writer accepted Set after truncate failed during commit")
	}
	if err := w.Commit(3, math.MaxUint64); err == nil {
		t.Fatal("writer accepted Commit after truncate failed during commit")
	}
}

func TestRound5ScopeRebuildAllocationFailurePoisonsWriter(t *testing.T) {
	created, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	image, ok := created.IntoImage()
	if !ok {
		t.Fatal("missing image")
	}
	w, store := round5OpenFaultWriter(t, image)
	id, err := w.ScopeIntern(round5Bitmap(MaxBitmapWidth + 1))
	if err != nil {
		t.Fatal(err)
	}
	store.failAlloc = true
	if err := w.Commit(2, math.MaxUint64); !errors.Is(err, round5ErrAlloc) {
		t.Fatalf("Commit error=%v, want scope rebuild allocation failure", err)
	}
	if err := w.Set(1, 1, id); err == nil {
		t.Fatal("writer accepted Set after scope rebuild allocation failure")
	}
	if err := w.Commit(3, math.MaxUint64); err == nil {
		t.Fatal("writer accepted Commit after scope rebuild allocation failure")
	}
}

func TestRound5FreeListAllocationFailurePoisonsWriter(t *testing.T) {
	w, store := round5OpenFaultWriter(t, round5ScalarTreeImage(t, 800))
	if _, err := w.Delete(200, 200); err != nil {
		t.Fatal(err)
	}
	store.failAlloc = true
	if err := w.Commit(2, math.MaxUint64); !errors.Is(err, round5ErrAlloc) {
		t.Fatalf("Commit error=%v, want free-list allocation failure", err)
	}
	if err := w.Set(2, 2, 2); err == nil {
		t.Fatal("writer accepted Set after free-list allocation failure")
	}
	if err := w.Commit(3, math.MaxUint64); err == nil {
		t.Fatal("writer accepted Commit after free-list allocation failure")
	}
}

func TestRound5CommitRejectsScopeCorruptionDiscoveredAfterOpen(t *testing.T) {
	created, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldID, err := created.ScopeIntern([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Set(1, 1, oldID); err != nil {
		t.Fatal(err)
	}
	if err := created.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	image, ok := created.IntoImage()
	if !ok {
		t.Fatal("missing image")
	}
	w, store := round5OpenFaultWriter(t, image)
	m, err := selectActiveMeta(store.image)
	if err != nil {
		t.Fatal(err)
	}
	scopePage := store.pageMut(m.scopeTableRoot)
	scopePage[PHChecksum] ^= 0x80
	newID, err := w.ScopeIntern([]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Set(2, 2, newID); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(2, math.MaxUint64); err == nil {
		t.Fatal("Commit silently rebuilt the scope table after its committed input became corrupt")
	}
	if err := w.Set(3, 3, newID); err == nil {
		t.Fatal("writer accepted Set after commit discovered corrupt committed scope data")
	}
}

func TestRound5NoOpAndRejectedScopeMutationsDoNotRebuildScopeTable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Writer[Ipv4Key], uint32) error
	}{
		{
			name: "set-already-present-feed",
			mutate: func(w *Writer[Ipv4Key], id uint32) error {
				got, err := w.ScopeBitmapSetFeed(id, 0)
				if err == nil && got != id {
					return errors.New("setting an existing feed changed scope_id")
				}
				return err
			},
		},
		{
			name: "clear-absent-feed",
			mutate: func(w *Writer[Ipv4Key], id uint32) error {
				got, err := w.ScopeBitmapClearFeed(id, 8)
				if err == nil && got != id {
					return errors.New("clearing an absent feed changed scope_id")
				}
				return err
			},
		},
		{
			name: "reject-unknown-scope",
			mutate: func(w *Writer[Ipv4Key], _ uint32) error {
				if _, err := w.ScopeBitmapSetFeed(math.MaxUint32, 1); err == nil {
					return errors.New("unknown scope_id was accepted")
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := Create[Ipv4Key](ScopeModeIndirect, 0)
			if err != nil {
				t.Fatal(err)
			}
			id, err := created.ScopeIntern([]byte{1})
			if err != nil {
				t.Fatal(err)
			}
			if err := created.Commit(1, math.MaxUint64); err != nil {
				t.Fatal(err)
			}
			before, ok := created.IntoImage()
			if !ok {
				t.Fatal("missing committed image")
			}
			metaBefore, err := selectActiveMeta(before)
			if err != nil {
				t.Fatal(err)
			}
			rootBefore := metaBefore.scopeTableRoot
			if rootBefore == 0 {
				t.Fatal("fixture has no scope table")
			}
			w, err := openWriter[Ipv4Key](newVecPageStore(append([]byte(nil), before...)))
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.mutate(w, id); err != nil {
				t.Fatal(err)
			}
			if err := w.Commit(2, math.MaxUint64); err != nil {
				t.Fatal(err)
			}
			after, ok := w.IntoImage()
			if !ok {
				t.Fatal("missing committed image after mutation")
			}
			metaAfter, err := selectActiveMeta(after)
			if err != nil {
				t.Fatal(err)
			}
			if rootAfter := metaAfter.scopeTableRoot; rootAfter != rootBefore {
				t.Fatalf("scope root changed after no-op/rejected mutation: %d -> %d", rootBefore, rootAfter)
			}
		})
	}
}
