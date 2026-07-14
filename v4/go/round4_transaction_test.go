package iprangedb

import (
	"errors"
	"math"
	"testing"
)

var (
	round4ErrSync  = errors.New("injected sync failure")
	round4ErrAlloc = errors.New("injected allocation failure")
)

type round4FaultStore struct {
	*vecPageStore
	events    []string
	failSync  bool
	failAlloc bool
}

func (s *round4FaultStore) pageMut(pgno uint32) []byte {
	if pgno < 2 {
		s.events = append(s.events, "meta")
	}
	return s.vecPageStore.pageMut(pgno)
}

func (s *round4FaultStore) sync() error {
	s.events = append(s.events, "sync")
	if s.failSync {
		s.failSync = false
		return round4ErrSync
	}
	return nil
}

func (s *round4FaultStore) allocPage() (uint32, error) {
	if s.failAlloc {
		s.failAlloc = false
		return 0, round4ErrAlloc
	}
	return s.vecPageStore.allocPage()
}

func round4OpenFaultWriter(t *testing.T) (*Writer[Ipv4Key], *round4FaultStore) {
	t.Helper()
	created, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	image, ok := created.IntoImage()
	if !ok {
		t.Fatal("missing image")
	}
	store := &round4FaultStore{vecPageStore: newVecPageStore(image)}
	writer, err := openWriter[Ipv4Key](store)
	if err != nil {
		t.Fatal(err)
	}
	return writer, store
}

func TestCommitFlushesDataBeforePublishingMetadata(t *testing.T) {
	writer, store := round4OpenFaultWriter(t)
	if err := writer.Set(1, 10, 1); err != nil {
		t.Fatal(err)
	}
	store.events = nil
	if err := writer.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}

	metaIndex := -1
	for i, event := range store.events {
		if event == "meta" {
			metaIndex = i
			break
		}
	}
	if metaIndex < 0 {
		t.Fatalf("commit events %v contain no metadata publication", store.events)
	}
	hasSyncBefore := false
	hasSyncAfter := false
	for i, event := range store.events {
		if event != "sync" {
			continue
		}
		if i < metaIndex {
			hasSyncBefore = true
		}
		if i > metaIndex {
			hasSyncAfter = true
		}
	}
	if !hasSyncBefore || !hasSyncAfter {
		t.Fatalf("commit events %v, want data sync -> metadata publication -> metadata sync", store.events)
	}
}

func TestSyncFailurePoisonsWriter(t *testing.T) {
	writer, store := round4OpenFaultWriter(t)
	if err := writer.Set(1, 10, 1); err != nil {
		t.Fatal(err)
	}
	store.failSync = true
	if err := writer.Commit(1, math.MaxUint64); !errors.Is(err, round4ErrSync) {
		t.Fatalf("Commit error = %v, want injected sync failure", err)
	}
	if err := writer.Set(20, 30, 2); err == nil {
		t.Fatal("writer accepted Set after a failed commit sync")
	}
	if err := writer.Commit(2, math.MaxUint64); err == nil {
		t.Fatal("writer accepted Commit after a failed commit sync")
	}
}

func TestAllocationFailurePoisonsTransaction(t *testing.T) {
	writer, store := round4OpenFaultWriter(t)
	store.failAlloc = true
	if err := writer.Set(1, 10, 1); !errors.Is(err, round4ErrAlloc) {
		t.Fatalf("Set error = %v, want injected allocation failure", err)
	}
	if err := writer.Commit(1, math.MaxUint64); err == nil {
		t.Fatal("Commit accepted a transaction after storage allocation failed")
	}
}
