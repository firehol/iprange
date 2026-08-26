// Public commit-resolution surface tests (Rust
// tests/commit_lifecycle.rs parity): resolve_commit proves the exact
// outcome of one attempted transaction and nonce against the two meta
// pages, coordinates through the live reader table and writer lease in
// Live mode, requires the sidecar-absent local file in Immutable mode,
// never invents an outcome for an old unproven attempt, and removes
// only the unpublished tail.

package iprangedb

import (
	"os"
	"path/filepath"
	"testing"
)

// commitResolutionPair creates one fresh direct IPv4 live pair and
// returns its path.
func commitResolutionPair(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "commit-resolution.iprdb")
	_, err := CreateLive(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, mustTag(t, "asn"), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// commitOneLive begins one direct transaction over the live writer,
// assigns one record, and commits it.
func commitOneLive(t *testing.T, w *LiveWriter, value uint32) LiveCommitResult {
	t.Helper()
	transaction, err := w.BeginDirect(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	changed, err := transaction.AssignV4(IPv4(value), IPv4(value), value)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("direct assignment on an empty base is not a change")
	}
	result, err := transaction.Commit()
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// alteredCommitAttempt builds one resolution attempt carrying the
// source attempt's identity facts with a replaced transaction id and
// nonce (Rust commit_lifecycle altered_attempt).
func alteredCommitAttempt(source LiveCommitResult, transactionID uint64, nonce [16]byte) LiveCommitResult {
	source.AttemptedTransactionID = transactionID
	source.AttemptedCommitNonce = nonce
	source.Status = CommitOutcomeUnknown
	source.Cleanup = LiveCommitCleanupArtifacts{}
	source.CoordinationCleanup = CoordinationCleanupNone
	source.Cause = nil
	return source
}

// TestCommitResultAndLiveResolutionReportExactFacts ports the Rust
// commit_result_and_live_resolution_report_exact_facts test: a commit
// reports its exact attempted identity facts, resolution against the
// live pair reports WriterBusy while the writer lease is held and then
// Committed with the SameLocalFile relation after close, and a wrong
// nonce at the same transaction reports NotCommitted.
func TestCommitResultAndLiveResolutionReportExactFacts(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	main := commitResolutionPair(t)

	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := commitOneLive(t, w, 7)
	if committed.Status != CommitCommitted {
		t.Fatalf("commit status = %v, want Committed", committed.Status)
	}
	if committed.AttemptedTransactionID != 2 {
		t.Fatalf("committed transaction id = %d, want 2", committed.AttemptedTransactionID)
	}
	if committed.DirectoryIdentity == nil || committed.MainIdentity == nil {
		t.Fatal("commit result carries no local identity")
	}
	if !committed.Cleanup.Empty() {
		t.Fatal("committed result carries cleanup artifacts")
	}
	if _, err := ResolveCommit(main, committed, CommitResolutionModeLive, nil); lifecycleCode(err) != ErrorWriterBusy {
		t.Fatalf("live resolution while the writer is open = %v, want WriterBusy", err)
	}

	closed, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closed.Outcome != CloseOutcomeClosed {
		t.Fatalf("close outcome = %v, want Closed", closed.Outcome)
	}

	resolved, err := ResolveCommit(main, committed, CommitResolutionModeLive, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Resolution != CommitResolutionCommitted {
		t.Fatalf("resolution = %v, want Committed", resolved.Resolution)
	}
	if resolved.LocalFileRelation != LocalFileRelationSameLocalFile {
		t.Fatalf("relation = %v, want SameLocalFile", resolved.LocalFileRelation)
	}

	wrongNonce := alteredCommitAttempt(committed, committed.AttemptedTransactionID, [16]byte{0x55})
	resolved, err = ResolveCommit(main, wrongNonce, CommitResolutionModeLive, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Resolution != CommitResolutionNotCommitted {
		t.Fatalf("wrong-nonce resolution = %v, want NotCommitted", resolved.Resolution)
	}
}

// TestLaterGenerationsDoNotInventAnOldCommitOutcome ports the Rust
// later_generations_do_not_invent_an_old_commit_outcome test: an
// attempt whose transaction id predates the selected generation and
// whose nonce matches no meta page is SupersededUnknown, never
// Committed.
func TestLaterGenerationsDoNotInventAnOldCommitOutcome(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	main := commitResolutionPair(t)

	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	first := commitOneLive(t, w, 7)
	commitOneLive(t, w, 8)
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	oldUnknown := alteredCommitAttempt(first, 1, [16]byte{0x66})
	resolved, err := ResolveCommit(main, oldUnknown, CommitResolutionModeLive, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Resolution != CommitResolutionSupersededUnknown {
		t.Fatalf("old-unknown resolution = %v, want SupersededUnknown", resolved.Resolution)
	}
}

// TestResolutionReportsADeliberateLogicalCopyAsADifferentLocalFile
// ports the Rust resolution_reports_a_deliberate_logical_copy_as_a_different_local_file
// test: an Immutable-mode resolution of a deliberate byte copy of the
// main file reports Committed with the DifferentLocalFile relation.
func TestResolutionReportsADeliberateLogicalCopyAsADifferentLocalFile(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	source := commitResolutionPair(t)
	copyPath := filepath.Join(t.TempDir(), "commit-resolution-copy.iprdb")

	w, err := OpenLiveWriter(source, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := commitOneLive(t, w, 7)
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveCommit(copyPath, committed, CommitResolutionModeImmutable, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Resolution != CommitResolutionCommitted {
		t.Fatalf("copy resolution = %v, want Committed", resolved.Resolution)
	}
	if resolved.LocalFileRelation != LocalFileRelationDifferentLocalFile {
		t.Fatalf("copy relation = %v, want DifferentLocalFile", resolved.LocalFileRelation)
	}
}

// TestCommitResolutionRemovesOnlyTheUnpublishedTail ports the Rust
// commit_resolution_removes_only_the_unpublished_tail test: a file
// extended by one unpublished page after a committed close is resolved
// back to the committed length with a clean ledger.
func TestCommitResolutionRemovesOnlyTheUnpublishedTail(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	main := commitResolutionPair(t)

	w, err := OpenLiveWriter(main, DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := commitOneLive(t, w, 7)
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	committedLength, err := commitResolutionFileSize(main)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(main, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(make([]byte, formatPage), int64(committedLength)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveCommit(main, committed, CommitResolutionModeLive, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Resolution != CommitResolutionCommitted {
		t.Fatalf("tail resolution = %v, want Committed", resolved.Resolution)
	}
	if !resolved.Cleanup.Empty() {
		t.Fatal("tail resolution carries cleanup artifacts")
	}
	length, err := commitResolutionFileSize(main)
	if err != nil {
		t.Fatal(err)
	}
	if length != committedLength {
		t.Fatalf("main length after resolution = %d, want %d", length, committedLength)
	}

	reader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := reader.LookupDirectV4(IPv4(7))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != 7 {
		t.Fatalf("lookup after tail resolution = (%d, %v), want (7, true)", value, found)
	}
	if _, err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

// commitResolutionFileSize reports the physical length of one main
// file.
func commitResolutionFileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// TestResolveCommitRefusesAnIncompleteAttempt pins the Rust
// validate_attempt boundary: a zero database id, transaction id, or
// commit nonce is InvalidArgument before any file access.
func TestResolveCommitRefusesAnIncompleteAttempt(t *testing.T) {
	requireLiveCreation(t)
	requirePublicationSecurity(t)
	main := commitResolutionPair(t)
	incomplete := LiveCommitResult{
		AttemptedDatabaseID:    [16]byte{},
		AttemptedTransactionID: 0,
		AttemptedCommitNonce:   [16]byte{},
	}
	if _, err := ResolveCommit(main, incomplete, CommitResolutionModeImmutable, nil); lifecycleCode(err) != ErrorInvalidArgument {
		t.Fatalf("incomplete attempt = %v, want InvalidArgument", err)
	}
}
