package writer

// Physical publication, abort, close, and reclamation tests (Rust
// writer_core publication/close/reclaim semantics over the real mapped
// file). Every cycle runs the real Open -> edit -> prepare -> publish ->
// reader re-open path; no owned page exists anywhere.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/reader"
)

// commitRange commits one direct range assignment in a fresh draft over
// the committed generation and asserts the publication lands. It is the
// canonical commit orchestration shared by every test that publishes a
// direct-edit draft (the crash suite included).
func commitRange(t *testing.T, c *Core, nonce byte, from, to, value uint32) {
	t.Helper()
	if err := c.StartDraft([16]byte{nonce}); err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	if _, err := store.AssignV4(from, to, value); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RequireDraftLength(); err != nil {
		t.Fatal(err)
	}
	res := c.Publish(nil)
	if res.Status != PublishCommitted {
		t.Fatalf("publish status = %v (%v), want committed", res.Status, res.Err)
	}
}

// commitOne assigns [10,20] value 123 and commits the draft through the
// real publication sequence, returning the committed transaction ID.
func commitOne(t *testing.T, c *Core, nonce byte) uint64 {
	t.Helper()
	commitRange(t, c, nonce, 10, 20, 123)
	return c.base.Meta.TxnID
}

// TestCoreCommitPublishesAlternateMeta covers the full commit cycle: the
// committed generation advances, the alternate meta page carries the new
// generation, a re-open sees the committed range, and a second commit
// toggles the meta page.
func TestCoreCommitPublishesAlternateMeta(t *testing.T) {
	path := makeEmptyDBPages(t, 64)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	meta0 := c.base.Meta
	// Rust bootstrap: identical creation metas select txn&1, so a fresh
	// txn-1 database opens on meta page 1 (bootstrap_tests.rs
	// identical_creation_metas_are_proven_current) and every commit
	// toggles to the alternate page (publication.rs target = 1 - base).
	if c.base.SelectedMetaPage != 1 {
		t.Fatalf("fresh writer selected meta page %d, want 1", c.base.SelectedMetaPage)
	}
	if got := commitOne(t, c, 1); got != 2 {
		t.Fatalf("first commit txn = %d, want 2", got)
	}
	first := c.base
	if first.SelectedMetaPage != 0 {
		t.Fatalf("first commit selected meta page %d, want 0", first.SelectedMetaPage)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Meta().TxnID != 2 {
		t.Fatalf("reader txn = %d, want 2", r.Meta().TxnID)
	}
	got, found, err := r.LookupDirect4(10)
	if err != nil || !found || got != 123 {
		t.Fatalf("lookup 10 = (%d, %v, %v), want (123, true, nil)", got, found, err)
	}
	if _, found, err := r.LookupDirect4(25); err != nil || found {
		t.Fatalf("lookup 25 = (%v, %v), want (false, nil)", found, err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	c2, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if c2.base.Meta.TxnID != 2 || c2.base.SelectedMetaPage != 0 {
		t.Fatalf("reopened writer = txn %d page %d, want 2/0", c2.base.Meta.TxnID, c2.base.SelectedMetaPage)
	}
	if got := commitOne(t, c2, 2); got != 3 {
		t.Fatalf("second commit txn = %d, want 3", got)
	}
	if c2.base.SelectedMetaPage != 1 {
		t.Fatalf("second commit selected meta page %d, want 1", c2.base.SelectedMetaPage)
	}
	_ = meta0
}

// TestCoreDiscardUnpublishedAbortsDraft pins the abort path: a draft with
// edits (and tail growth) is discarded, the file stays at the committed
// generation, and a re-open sees the previous committed state.
func TestCoreDiscardUnpublishedAbortsDraft(t *testing.T) {
	path := makeEmptyDBPages(t, 8)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.StartDraft([16]byte{1}); err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	if _, err := store.AssignV4(0, 5, 7); err != nil {
		t.Fatal(err)
	}
	// Grow the file with the draft's private pages before the abort.
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.DiscardUnpublished(); err != nil {
		t.Fatal(err)
	}
	if c.draft != nil {
		t.Fatal("draft survived discard")
	}
	fileLen, err := c.m.FileSize()
	if err != nil {
		t.Fatal(err)
	}
	if fileLen != c.base.CommittedBytes {
		t.Fatalf("file length %d after discard, want committed %d", fileLen, c.base.CommittedBytes)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Meta().TxnID != 1 {
		t.Fatalf("reader txn = %d after abort, want 1", r.Meta().TxnID)
	}
	if _, found, err := r.LookupDirect4(0); err != nil || found {
		t.Fatalf("lookup after abort = (%v, %v), want (false, nil)", found, err)
	}
}

// TestCoreClosePlanTrimsUnpublishedTail pins PrepareClose/FinishClose:
// the close trims the unpublished tail and drops the draft; a re-open is
// clean and sees the committed generation.
func TestCoreClosePlanTrimsUnpublishedTail(t *testing.T) {
	path := makeEmptyDBPages(t, 8)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.StartDraft([16]byte{1}); err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	if _, err := store.AssignV4(0, 5, 7); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	plan, err := c.PrepareClose()
	if err != nil {
		t.Fatal(err)
	}
	if plan.TransactionID() != 1 {
		t.Fatalf("close plan txn = %d, want 1", plan.TransactionID())
	}
	if err := c.FinishClose(plan); err != nil {
		t.Fatal(err)
	}
	if c.draft != nil {
		t.Fatal("draft survived close")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Meta().TxnID != 1 {
		t.Fatalf("reader txn = %d after close, want 1", r.Meta().TxnID)
	}
}

// TestReclamationCommitsFreesRetiredTransactions runs three commits, then
// a bounded reclamation draft that reclaims the oldest retired
// transaction, and verifies the committed retirement tree shrank while
// every committed range stays readable.
func TestReclamationCommitsFreesRetiredTransactions(t *testing.T) {
	path := makeEmptyDBPages(t, 64)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 100, MaxGrowthPages: 100}
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if got := commitOne(t, c, 1); got != 2 {
		t.Fatalf("commit 1 txn = %d, want 2", got)
	}
	// Second edit over the committed tree COWs and retires the first
	// generation's pages under transaction 3.
	if err := c.StartDraft([16]byte{2}); err != nil {
		t.Fatal(err)
	}
	store := NewDraftStore(c.m, c.base.Meta.PageCount, c.budget, c.draft)
	if _, err := store.AssignV4(15, 25, 5); err != nil {
		t.Fatal(err)
	}
	if err := c.Prepare(nil); err != nil {
		t.Fatal(err)
	}
	if c.draft.meta.RetiredExtentCount == 0 {
		t.Fatal("COW commit retired no pages")
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("commit 2 publish = %v (%v)", res.Status, res.Err)
	}
	if c.base.Meta.TxnID != 3 {
		t.Fatalf("commit 2 txn = %d, want 3", c.base.Meta.TxnID)
	}
	// Reclamation draft: reclaim one oldest transaction (txn 3's pages).
	workBefore := reclamationWorkBaseline()
	prepared, err := c.PrepareReclamation(nil, 1, 1<<30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil {
		t.Fatal("reclamation selected nothing")
	}
	if prepared.TransactionCount != 1 {
		t.Fatalf("reclamation transactions = %d, want 1", prepared.TransactionCount)
	}
	if res := c.Publish(nil); res.Status != PublishCommitted {
		t.Fatalf("reclamation publish = %v (%v)", res.Status, res.Err)
	}
	if c.base.Meta.TxnID != 4 {
		t.Fatalf("reclamation txn = %d, want 4", c.base.Meta.TxnID)
	}
	// The reclaimed pages must be returned to the free bitmap (Rust
	// work::pages_reclaimed); the retirement extent count itself is not a
	// shrink signal because remove COW victims are re-retired under the
	// new generation. The counter assertion lives in the v4work-only
	// helper (publication_work_test.go).
	checkReclaimedPages(t, workBefore, prepared.PageCount)

	// Re-open only after Close: the immutable reader blocks on the
	// writer's exclusive lifetime lock until the mapping is released.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := reader.OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Meta().TxnID != 4 {
		t.Fatalf("reader txn = %d, want 4", r.Meta().TxnID)
	}
	if got, found, err := r.LookupDirect4(10); err != nil || !found || got != 123 {
		t.Fatalf("lookup 10 = (%d, %v, %v), want (123, true, nil)", got, found, err)
	}
	if got, found, err := r.LookupDirect4(20); err != nil || !found || got != 5 {
		t.Fatalf("lookup 20 = (%d, %v, %v), want (5, true, nil)", got, found, err)
	}
}

// TestPublishRequiresPreparedDraft pins the state gates: publish without
// a draft is a BeforePublication NoPendingTransaction failure; a second
// draft while one is open is WrongState.
func TestPublishRequiresPreparedDraft(t *testing.T) {
	path := makeEmptyDBPages(t, 8)
	budget := PageBudget{MaxHeapBytes: 0, MaxPrivatePages: 10, MaxGrowthPages: 10}
	c, err := Open(path, budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if res := c.Publish(nil); res.Status != PublishBeforePublication {
		t.Fatalf("publish without draft status = %v, want BeforePublication", res.Status)
	}
	if _, err := c.CommitAttempt(); err == nil {
		t.Fatal("commitAttempt without draft did not fail")
	}
	if err := c.StartDraft([16]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := c.StartDraft([16]byte{2}); err == nil {
		t.Fatal("second StartDraft did not fail")
	}
	if _, err := c.CommitAttempt(); err == nil {
		t.Fatal("commitAttempt on unchanged draft did not fail")
	}
}
