// Public membership transaction tests (Rust live_writer/membership.rs
// surface parity): reference pinning, epochs, apply/ensure/rename/
// delete, ordered enumeration, metadata staging, and commit/abort.
// Every vector runs over a database with committed feeds so the
// membership operations transform real range segments.

package iprangedb

import (
	"errors"
	"testing"
)

// memberTxDB creates one membership database with two committed feeds:
// alpha covers [0,100] and beta covers [50,150] (each a single-point
// inclusive range).
func memberTxDB(t *testing.T) string {
	t.Helper()
	path := testFeedMembership(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, feed := range []struct {
		name string
		from uint32
		to   uint32
	}{
		{"alpha", 0, 100},
		{"beta", 50, 150},
	} {
		create, err := w.BeginCreateFeed(feedName(t, feed.name), cancellation)
		if err != nil {
			t.Fatal(err)
		}
		if err := create.AddRangesV4([]AddressRange4{{From: IPv4(feed.from), To: IPv4(feed.to)}}); err != nil {
			t.Fatal(err)
		}
		finished, err := create.FinishInput()
		if err != nil {
			t.Fatal(err)
		}
		if result, err := finished.Commit(); err != nil {
			t.Fatal(err)
		} else if result.Status != CommitCommitted {
			t.Fatalf("feed %s commit = %v, want committed", feed.name, result.Status)
		}
	}
	return path
}

// TestPublicMembershipTransactionApplyCommit runs one complete
// transaction: feed references, an empty membership, alternating union
// and intersection ranges over committed segments, a feed added to a
// membership, metadata staging, the committed outcome, and the
// cross-transaction reference refusal (Rust ForeignReference).
func TestPublicMembershipTransactionApplyCommit(t *testing.T) {
	requireFileCreation(t)
	path := memberTxDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx, err := w.BeginMembershipTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	alpha, ok, err := tx.LookupFeed(feedName(t, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || alpha.Name() != "alpha" || alpha.Index() != 0 {
		t.Fatalf("lookup alpha = %q/%d/%v, want alpha/0/true", alpha.Name(), alpha.Index(), ok)
	}
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	// Union of the empty membership over alpha's committed segments
	// [4,12] is a no-op: combining {} with the existing {alpha} bit
	// reproduces the same membership id (Rust combine identity).
	if changed, err := tx.ApplyV4(IPv4(4), IPv4(12), empty, MembershipUnion); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Fatal("empty union over committed segments changed the database")
	}
	beta, err := tx.EnsureFeed(feedName(t, "beta"))
	if err != nil {
		t.Fatal(err)
	}
	withBeta, err := tx.AddFeed(empty, beta)
	if err != nil {
		t.Fatal(err)
	}
	// Union with {beta} extends the committed {alpha} segments:
	// [4,12] becomes {alpha,beta}, a real change.
	if changed, err := tx.ApplyV4(IPv4(4), IPv4(12), withBeta, MembershipUnion); err != nil {
		t.Fatal(err)
	} else if !changed {
		t.Fatal("union with beta over committed segments is not a change")
	}
	// Intersection with {beta} over [8,20] keeps beta on the
	// overlapped [8,12] and removes the membership on [12,20]
	// where beta never covered alpha.
	if changed, err := tx.ApplyV4(IPv4(8), IPv4(20), withBeta, MembershipIntersection); err != nil {
		t.Fatal(err)
	} else if !changed {
		t.Fatal("intersection over committed segments is not a change")
	}
	if _, err := tx.SetMetadataJSON([]byte(`{"transaction":1}`)); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("commit status = %v, want committed", result.Status)
	}

	// The committed transaction pins its references: a membership from
	// the spent transaction is stale in a fresh one (Rust
	// require_membership_reference: same database, different operation
	// nonce -> StaleReference; ForeignReference is reserved for a
	// different database id).
	second, err := w.BeginMembershipTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ApplyV4(IPv4(0), IPv4(1), empty, MembershipUnion); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("cross-transaction membership = %v, want stale reference", err)
	}
	if err := second.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicMembershipTransactionStaleEpoch pins the Rust
// membership-epoch rule: a membership reference produced before a feed
// deletion is stale for every later mutation of the same transaction.
func TestPublicMembershipTransactionStaleEpoch(t *testing.T) {
	requireFileCreation(t)
	path := memberTxDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx, err := w.BeginMembershipTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	alpha, _, err := tx.LookupFeed(feedName(t, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV4(IPv4(0), IPv4(100), empty, MembershipUnion); err != nil {
		t.Fatal(err)
	}
	if err := tx.DeleteFeed(alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV4(IPv4(0), IPv4(50), empty, MembershipUnion); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("apply with pre-delete membership = %v, want stale reference", err)
	}
	// The feed reference itself is stale after the delete.
	if _, err := tx.RenameFeed(alpha, feedName(t, "renamed")); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("rename of deleted feed = %v, want stale reference", err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicMembershipTransactionSurface pins the remaining surface
// rules: the ordered enum cursor, lookup/ensure/rename, the metadata
// stage, error classes, and the committed-transaction abort.
func TestPublicMembershipTransactionSurface(t *testing.T) {
	requireFileCreation(t)
	path := memberTxDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx, err := w.BeginMembershipTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	alpha, _, err := tx.LookupFeed(feedName(t, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := tx.RenameFeed(alpha, feedName(t, "alpha-renamed"))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name() != "alpha-renamed" || renamed.Index() != alpha.Index() {
		t.Fatalf("renamed feed = %q/%d, want alpha-renamed/%d", renamed.Name(), renamed.Index(), alpha.Index())
	}
	// The old-name reference is stale after the rename.
	if _, err := tx.RenameFeed(alpha, feedName(t, "again")); !isPubCode(err, ErrorStaleReference) {
		t.Fatalf("rename of stale feed = %v, want stale reference", err)
	}
	// A rename onto an existing name fails inside the edit (Rust
	// rename_current_feed inside mutate), so it aborts the transaction
	// with TransactionAborted wrapping NameExists and the writer is
	// clean again.
	if _, err := tx.RenameFeed(renamed, feedName(t, "beta")); err == nil {
		t.Fatal("rename onto existing name was accepted")
	} else {
		var ab *abortError
		if !errors.As(err, &ab) || !isPubCode(ab.cause, ErrorNameExists) {
			t.Fatalf("rename onto existing = %v, want transaction aborted wrapping name exists", err)
		}
	}
	// The transaction is dead: later ops report the inactive class and
	// Commit/Abort report the writer's clean state (Rust require_active
	// / commit_attempt / abort after abort_after).
	if _, err := tx.FeedCursor(); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("feed cursor on aborted transaction = %v, want wrong state", err)
	}
	if _, err := tx.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit on aborted transaction = %v, want no pending transaction", err)
	}
	if err := tx.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort on aborted transaction = %v, want no pending transaction", err)
	}

	// A fresh transaction serves the remaining surface. The aborted
	// rename was discarded with the draft, so the catalog still shows
	// the committed alpha/beta names.
	tx, err = w.BeginMembershipTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := tx.FeedCursor()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for {
		ref, ok, err := cursor.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		names = append(names, ref.Name().String())
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("cursor order = %v, want [alpha beta]", names)
	}
	if changed, err := tx.ClearMetadataJSON(); err != nil {
		t.Fatal(err)
	} else if changed {
		t.Fatal("clear metadata on an absent database reported a change")
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicMembershipTransactionErrors pins the invalid arguments of
// the transaction surface: reversed ranges, wrong family, and the
// inactive-transaction class after abort.
func TestPublicMembershipTransactionErrors(t *testing.T) {
	requireFileCreation(t)
	path := memberTxDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx, err := w.BeginMembershipTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV4(IPv4(10), IPv4(9), empty, MembershipUnion); !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("reversed range = %v, want invalid argument", err)
	}
	if _, err := tx.ApplyV6(IPv6{Hi: 1}, IPv6{Hi: 2}, empty, MembershipUnion); !isPubCode(err, ErrorWrongAddressFamily) {
		t.Fatalf("wrong family = %v, want wrong address family", err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ApplyV4(IPv4(0), IPv4(1), empty, MembershipUnion); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("apply after abort = %v, want wrong state", err)
	}
	// Aborting a second time reports the writer's clean state (Rust
	// writer.abort NoPendingTransaction parity).
	if err := tx.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("second abort = %v, want no pending transaction", err)
	}
}

// TestPublicMembershipTransactionCancellation pins the Rust
// check_or_abort path: a cancelled token refuses the next mutation and
// aborts the transaction through the writer (ErrorTransactionAborted).
func TestPublicMembershipTransactionCancellation(t *testing.T) {
	requireFileCreation(t)
	path := memberTxDB(t)
	cancellation := NewCancellationToken()
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	tx, err := w.BeginMembershipTransaction(cancellation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.EnsureFeed(feedName(t, "gamma")); err != nil {
		t.Fatal(err)
	}
	cancellation.Cancel()
	if _, err := tx.EmptyMembership(); !isPubCode(err, ErrorTransactionAborted) {
		t.Fatalf("mutation after cancel = %v, want transaction aborted", err)
	}
}
