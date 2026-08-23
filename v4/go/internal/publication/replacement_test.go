//go:build linux

// Previous-main replacement bind tests (Rust main_file_tests.rs
// replacement arms): bind, bind_no_rollback, the missing and
// same-identity refusals, the content-change detection, and the
// cancellation surface.

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

func TestBindPrevious(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare: %v", failure)
	}
	t.Cleanup(func() { prepared.Close() })

	// The replaced main is any retained regular file; the bind digests
	// its exact bytes without validating the v4 structure (Rust
	// main_file_tests uses raw bytes too).
	previousBytes := []byte("previous bytes")
	if err := os.WriteFile(main, previousBytes, 0o600); err != nil {
		t.Fatalf("write previous main: %v", err)
	}

	bound, bindFailure := bindPrevious(prepared, nil)
	if bindFailure != nil {
		t.Fatalf("bind: %v", bindFailure)
	}
	if bound.policy != reservationPolicyReplaceExisting {
		t.Fatalf("bound policy %d, want replace-existing", bound.policy)
	}
	previous := bound.previous
	if previous == nil {
		t.Fatal("bound output must carry the previous main")
	}
	if previous.byteLength != uint64(len(previousBytes)) {
		t.Fatalf("previous length %d, want %d", previous.byteLength, len(previousBytes))
	}
	// verifyCanonicalNamespace and verifyContent pass on the stable
	// previous main (Rust replacement flow proof after the bind).
	if err := previous.verifyCanonicalNamespace(bound.attempt.destination); err != nil {
		t.Fatalf("verifyCanonicalNamespace: %v", err)
	}
	if err := previous.verifyContent(bound.attempt.destination, nil); err != nil {
		t.Fatalf("verifyContent: %v", err)
	}
	if err := bound.verifyDestinationBeforeMain(); err != nil {
		t.Fatalf("verifyDestinationBeforeMain: %v", err)
	}

	// verifyRetired must fail while the previous still has its link.
	if err := previous.verifyRetired(bound.attempt.destination, bound.attempt.name); err == nil {
		t.Fatal("verifyRetired must fail while the previous still has one link")
	}
}

func TestBindPreviousNoRollback(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare: %v", failure)
	}
	t.Cleanup(func() { prepared.Close() })
	if err := os.WriteFile(main, []byte("previous bytes"), 0o600); err != nil {
		t.Fatalf("write previous main: %v", err)
	}
	bound, bindFailure := bindPreviousNoRollback(prepared, nil)
	if bindFailure != nil {
		t.Fatalf("bind no rollback: %v", bindFailure)
	}
	if bound.policy != reservationPolicyReplaceExistingNoRollback {
		t.Fatalf("bound policy %d, want no-rollback", bound.policy)
	}
}

func TestBindPreviousMissing(t *testing.T) {
	dir := t.TempDir()
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare: %v", failure)
	}
	t.Cleanup(func() { prepared.Close() })
	_, bindFailure := bindPrevious(prepared, nil)
	if bindFailure == nil {
		t.Fatal("bind without a main must fail")
	}
	nerr, ok := live.AsNamespaceError(bindFailure.cause)
	if !ok || nerr.Kind != live.NamespaceMissing {
		t.Fatalf("missing-main bind failure: %v", bindFailure.cause)
	}
}

func TestBindPreviousSameIdentity(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	_, _ = testFinishedOutput(t, file)
	private := filepath.Join(dir, attempt.name)
	file.Close()
	// Move the attempt inode to the main name: the bind then proves the
	// identities match and refuses (Rust SameIdentity arm). The bind
	// touches only the attempt facts, so a bare prepared output is
	// sufficient.
	if err := os.Rename(private, main); err != nil {
		t.Fatalf("rename to main: %v", err)
	}
	prepared := &preparedOutput{attempt: attempt}
	_, bindFailure := bindPrevious(prepared, nil)
	if bindFailure == nil {
		t.Fatal("bind to the same inode must fail")
	}
	fe, ok := bindFailure.cause.(*format.Error)
	if !ok || fe.Code != format.CodeConflict || fe.Detail != "replacement source and destination identities match" {
		t.Fatalf("same-identity failure class: %v", bindFailure.cause)
	}
}

func TestBindPreviousContentChanged(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare: %v", failure)
	}
	t.Cleanup(func() { prepared.Close() })
	previousBytes := []byte("previous bytes")
	if err := os.WriteFile(main, previousBytes, 0o600); err != nil {
		t.Fatalf("write previous main: %v", err)
	}
	bound, bindFailure := bindPrevious(prepared, nil)
	if bindFailure != nil {
		t.Fatalf("bind: %v", bindFailure)
	}

	// Same-length in-place mutation: the namespace proof still passes
	// but the content digest differs.
	mainHandle, err := os.OpenFile(main, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open main for mutation: %v", err)
	}
	if _, err := mainHandle.WriteAt([]byte("PREVIOUS bytes"), 0); err != nil {
		t.Fatalf("mutate main: %v", err)
	}
	mainHandle.Close()

	if err := bound.previous.verifyContent(bound.attempt.destination, nil); err == nil {
		t.Fatal("verifyContent must detect the changed previous main")
	}
}

func TestBindPreviousCancellation(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "result.v4")
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare: %v", failure)
	}
	t.Cleanup(func() { prepared.Close() })
	if err := os.WriteFile(main, []byte("previous bytes"), 0o600); err != nil {
		t.Fatalf("write previous main: %v", err)
	}
	want := errors.New("cancel")
	_, bindFailure := bindPrevious(prepared, func() error { return want })
	if bindFailure == nil {
		t.Fatal("bind must propagate the cancellation")
	}
	if !errors.Is(bindFailure.cause, want) {
		t.Fatalf("bind cancellation cause %v, want %v", bindFailure.cause, want)
	}
}
