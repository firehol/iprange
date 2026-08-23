//go:build linux

// Prepared-output machine tests (Rust output_tests.rs preparation
// arms): the exact digest and retained lifetime lock, the metadata
// change refusal, the hard-link custody refusal, and the pre-rename
// destination evidence.

package publication

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

func prepareTestOutput(t *testing.T, dir string) (*preparedOutput, string, [64]byte) {
	t.Helper()
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, expectedDigest := testFinishedOutput(t, file)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare: %v", failure)
	}
	t.Cleanup(func() { prepared.Close() })
	return prepared, filepath.Join(dir, attempt.name), expectedDigest
}

func TestPrepareHashesExactBytesAndRetainsLifetimeLock(t *testing.T) {
	dir := t.TempDir()
	prepared, private, expectedDigest := prepareTestOutput(t, dir)

	if prepared.sha512 != expectedDigest {
		t.Fatal("prepared digest differs from the SHA-512 of the exact file bytes")
	}
	if prepared.byteLength != 2*format.PageSize {
		t.Fatalf("prepared byte length %d, want %d", prepared.byteLength, 2*format.PageSize)
	}
	expectedMeta := prepared.meta
	if expectedMeta.DatabaseID == [16]byte{} {
		t.Fatal("prepared meta did not carry the fixture identity")
	}

	// The artifact lifetime lock is held while the output is prepared.
	contender, err := os.OpenFile(private, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	defer contender.Close()
	acquired, err := live.TryLockFile(contender, live.MainLifetimeOffset, live.LockExclusive)
	if err != nil {
		t.Fatalf("contender lock: %v", err)
	}
	if acquired {
		t.Fatal("contender must not acquire the lifetime lock while prepared")
	}

	// verifyPrivate passes at the private position; verifyMain before
	// the rename reports the absent main (Rust publish-state flow calls
	// verify_main only after the rename).
	if err := prepared.verifyPrivate(); err != nil {
		t.Fatalf("verifyPrivate: %v", err)
	}
	if err := prepared.verifyMain(); err == nil {
		t.Fatal("verifyMain before the rename must fail with the missing main")
	}

	// verifyDestinationBeforeMain proves the main is absent.
	if err := prepared.verifyDestinationBeforeMain(); err != nil {
		t.Fatalf("verifyDestinationBeforeMain with absent main: %v", err)
	}
	foreign := filepath.Join(dir, "foreign")
	if err := os.WriteFile(filepath.Join(dir, "result.v4"), []byte("foreign"), 0o600); err != nil {
		t.Fatalf("write foreign main: %v", err)
	}
	if err := prepared.verifyDestinationBeforeMain(); err == nil {
		t.Fatal("verifyDestinationBeforeMain with foreign main must fail")
	}
	_ = foreign
}

func TestPrepareRejectsHardLink(t *testing.T) {
	dir := t.TempDir()
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	if err := os.Link(filepath.Join(dir, attempt.name), filepath.Join(dir, "extra-link")); err != nil {
		t.Fatalf("hard link: %v", err)
	}
	_, failure := attempt.prepareCancellable(finished, nil)
	if failure == nil {
		t.Fatal("prepare must reject a hard-linked private output")
	}
	nerr, ok := live.AsNamespaceError(failure.cause)
	if !ok || nerr.Kind != live.NamespaceLinkCount || nerr.Links != 2 {
		t.Fatalf("hard-link prepare failure: %v (namespace %v)", failure.cause, ok)
	}
	if failure.owner.attempt.identity != attempt.identity {
		t.Fatal("failure must carry the exact owned attempt")
	}
}

func TestPrepareRejectsChangedMeta(t *testing.T) {
	dir := t.TempDir()
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	// Mutate a scalar of page 0 and re-CRC it: the pair stays valid but
	// selects a different meta than the expected finished meta.
	mutated := testMetaPage(2, 2)
	if _, err := file.WriteAt(mutated, 0); err != nil {
		t.Fatalf("write mutated meta: %v", err)
	}
	_, failure := attempt.prepareCancellable(finished, nil)
	if failure == nil {
		t.Fatal("prepare must reject a changed finished meta")
	}
	fe, ok := failure.cause.(*format.Error)
	if !ok || fe.Code != format.CodeConflict || fe.Detail != "finished output metadata changed" {
		t.Fatalf("meta-changed failure class: %v", failure.cause)
	}
}

func TestPrepareReleasesLockOnClose(t *testing.T) {
	dir := t.TempDir()
	attempt, file := testSecuredAttempt(t, dir, "result.v4")
	finished, _ := testFinishedOutput(t, file)
	private := filepath.Join(dir, attempt.name)
	prepared, failure := attempt.prepareCancellable(finished, nil)
	if failure != nil {
		t.Fatalf("prepare: %v", failure)
	}
	contender, err := os.OpenFile(private, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	defer contender.Close()
	if err := prepared.Close(); err != nil {
		t.Fatalf("close prepared: %v", err)
	}
	acquired, err := live.TryLockFile(contender, live.MainLifetimeOffset, live.LockExclusive)
	if err != nil || !acquired {
		t.Fatalf("lifetime lock after close: acquired=%v err=%v", acquired, err)
	}
	if err := live.UnlockFile(contender, live.MainLifetimeOffset); err != nil {
		t.Fatalf("unlock contender: %v", err)
	}
}
