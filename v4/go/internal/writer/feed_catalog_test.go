// Draft feed catalog tests (Rust draft_store::catalog tests): ensure
// creates and reuses entries, lookup resolves exact names, feed indexes
// allocate from zero, and the namespace exhaustion class fires at 2^32.

package writer

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestDraftFeedCatalogEnsureInsertLookup pins the Rust ensure_feed
// contract over a real opened draft: the first ensure creates the feed at
// the lowest index, the second reuses it, and the active count and draft
// changed flag move exactly like the Rust draft.
func TestDraftFeedCatalogEnsureInsertLookup(t *testing.T) {
	path := makeEmptyDB(t)
	draft, store, _ := openDraftStore(t, path, PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}, [16]byte{3})

	entry, created, err := store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first ensure did not create")
	}
	if entry.name != "alpha" || entry.index != 0 {
		t.Fatalf("first entry = %+v, want alpha@0", entry)
	}
	if draft.meta.ActiveFeedCount != 1 || draft.meta.FeedIndexLimit != 1 {
		t.Fatalf("after first insert: active = %d limit = %d", draft.meta.ActiveFeedCount, draft.meta.FeedIndexLimit)
	}
	if !draft.Changed() {
		t.Fatal("insert did not mark the draft changed")
	}

	entry, created, err = store.ensureFeed("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second ensure created again")
	}
	if entry.name != "alpha" || entry.index != 0 {
		t.Fatalf("reused entry = %+v, want alpha@0", entry)
	}

	entry, created, err = store.ensureFeed("beta")
	if err != nil {
		t.Fatal(err)
	}
	if !created || entry.index != 1 {
		t.Fatalf("beta = %+v created %v, want beta@1 created", entry, created)
	}
	if draft.meta.ActiveFeedCount != 2 {
		t.Fatalf("active feed count = %d, want 2", draft.meta.ActiveFeedCount)
	}

	found, ok, err := store.lookupFeed("beta")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.index != 1 {
		t.Fatalf("lookup beta = %+v ok %v, want index 1", found, ok)
	}
	if _, ok, err := store.lookupFeed("gamma"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("lookup of an absent feed reported found")
	}

	// Index-zero root resets in an empty draft stay absent, mirroring
	// the Rust absent-root shortcut.
	draft.meta.CatalogNameRoot = 0
	if _, ok, err := store.lookupFeed("beta"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("absent name root reported found")
	}
}

// TestDraftFeedCatalogInvalidNameRefused pins the boundary validation:
// an invalid feed name is refused with the name-invalid class before any
// tree operation.
func TestDraftFeedCatalogInvalidNameRefused(t *testing.T) {
	path := makeEmptyDB(t)
	_, store, _ := openDraftStore(t, path, PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}, [16]byte{3})
	if _, _, err := store.ensureFeed("-bad"); err == nil {
		t.Fatal("invalid feed name accepted")
	} else if code := errCode(err); code != format.CodeNameInvalid {
		t.Fatalf("code = %d, want NameInvalid", code)
	}
	if _, _, err := store.lookupFeed(""); err == nil {
		t.Fatal("empty feed name accepted")
	} else if code := errCode(err); code != format.CodeNameInvalid {
		t.Fatalf("code = %d, want NameInvalid", code)
	}
}

// TestDraftFeedCatalogIndexExhaustion pins the 2^32 feed-index limit:
// a full dense namespace (a used bitmap that declares no hole at the
// 2^32 limit) fails with the feed-index-exhausted class (Rust
// FeedIndexExhausted). The crafted empty level-3 feed bitmap is exactly
// the shape of the used bitmap after every index was handed out.
func TestDraftFeedCatalogIndexExhaustion(t *testing.T) {
	path := makeEmptyDB(t)
	draft, store, _ := openDraftStore(t, path, PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}, [16]byte{3})
	page, err := store.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(page, func(view []byte) error {
		bitmap.Initialize(view, draft.meta.TxnID, 3, bitmap.KindFeed)
		// A branch page must declare a nonzero item count; the zeroed
		// summaries then report no hole at any level.
		format.PutU16(view[format.HeaderCount:], 1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	draft.meta.FeedUsedRoot = page
	draft.meta.FeedIndexLimit = 1 << 32
	if _, err := store.allocateFeedIndex(); err == nil {
		t.Fatal("feed index exhaustion accepted")
	} else if code := errCode(err); code != format.CodeFeedIndexExhausted {
		t.Fatalf("code = %d, want FeedIndexExhausted; detail: %v", code, err)
	}
}

// TestDraftFeedCatalogDenseAppend pins the dense allocation path: the
// index equals the old limit, the limit advances, and the used bit is
// set, so the next allocation does not hand out the same index twice.
func TestDraftFeedCatalogDenseAppend(t *testing.T) {
	path := makeEmptyDB(t)
	_, store, _ := openDraftStore(t, path, PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}, [16]byte{3})
	for want := uint32(0); want < 8; want++ {
		index, err := store.allocateFeedIndex()
		if err != nil {
			t.Fatal(err)
		}
		if index != want {
			t.Fatalf("index = %d, want %d", index, want)
		}
	}
}
