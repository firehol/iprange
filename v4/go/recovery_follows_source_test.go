//go:build !windows

package iprangedb

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Recovery follows the source sidecar's recorded choice end to end: a
// protected source recovers to a 0600 output through the worker (the
// parent creates the attempt under the source policy, and the worker
// resumes it under the policy the attempt facts record), and an
// unprotected source does not force 0600. Detected under umask 0200,
// which distinguishes an exact-0600 set from a plain 0600 create.
func TestRecoveryFollowsSourceEndToEnd(t *testing.T) {
	installWorkerForTest(t)
	requireLiveCreation(t)
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	dir := t.TempDir()
	// umask 0 distinguishes source-following from switch-following
	// (0600 vs 0666) while leaving the worker's own control-file create
	// writable; the umask-independence of the exact-0600 set is pinned
	// separately by the CLI export detector under umask 0200.
	previous := unix.Umask(0)
	defer unix.Umask(previous)
	budget := RecoveryHeapOnly(16<<20, 100_000, 4)

	protected := filepath.Join(dir, "protected.v4")
	createRecoverySource(t, protected, true)
	candidates, err := InspectRecoveryCandidates(protected, RecoveryInspectionLive, HeapOnly(1<<20, 8), nil)
	if err != nil {
		t.Fatalf("inspect protected: %v", err)
	}
	if candidates.CandidateCount() == 0 {
		t.Fatal("no protected candidates")
	}
	protectedOut := filepath.Join(dir, "protected-out.v4")
	result, failure := RecoverLive(protected, candidates.Candidate(0), protectedOut, budget, nil, nil)
	if failure != nil {
		t.Fatalf("recover protected: %v", failure.Cause)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("protected publication = %v", result.Publication.Publication)
	}
	info, err := os.Stat(protectedOut)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("protected recovery mode = %#o, want 0600 end to end (a switch-following default would stay 0666 under umask 0)", mode)
	}

	plain := filepath.Join(dir, "plain.v4")
	createRecoverySource(t, plain, false)
	candidates, err = InspectRecoveryCandidates(plain, RecoveryInspectionLive, HeapOnly(1<<20, 8), nil)
	if err != nil {
		t.Fatalf("inspect plain: %v", err)
	}
	if candidates.CandidateCount() == 0 {
		t.Fatal("no plain candidates")
	}
	plainOut := filepath.Join(dir, "plain-out.v4")
	result, failure = RecoverLive(plain, candidates.Candidate(0), plainOut, budget, nil, nil)
	if failure != nil {
		t.Fatalf("recover plain: %v", failure.Cause)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("plain publication = %v", result.Publication.Publication)
	}
	info, err = os.Stat(plainOut)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode == 0o600 {
		t.Fatal("unprotected recovery forced mode 0600")
	}
}

// The switch-ON arm of recovery-follows-source: an unprotected source
// recovered with the process switch on must still bind the recorded
// choice (unprotected) — the facts the attempt records win over the
// worker's process switch. Under umask 0 a switch-following worker
// would create 0600 where the recorded facts keep the 0666 default.
// Rust twin: recovery_follows_source.rs
// recovery_follows_the_source_with_the_switch_on.
func TestRecoveryFollowsSourceWithSwitchOn(t *testing.T) {
	installWorkerForTest(t)
	requireLiveCreation(t)
	t.Setenv("IPRANGE_CREATOR_ONLY", "1")
	dir := t.TempDir()
	previous := unix.Umask(0)
	defer unix.Umask(previous)
	budget := RecoveryHeapOnly(16<<20, 100_000, 4)

	plain := filepath.Join(dir, "plain.v4")
	createRecoverySource(t, plain, false)
	candidates, err := InspectRecoveryCandidates(plain, RecoveryInspectionLive, HeapOnly(1<<20, 8), nil)
	if err != nil {
		t.Fatalf("inspect plain: %v", err)
	}
	if candidates.CandidateCount() == 0 {
		t.Fatal("no plain candidates")
	}
	plainOut := filepath.Join(dir, "plain-out.v4")
	result, failure := RecoverLive(plain, candidates.Candidate(0), plainOut, budget, nil, nil)
	if failure != nil {
		t.Fatalf("recover plain: %v", failure.Cause)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("plain publication = %v", result.Publication.Publication)
	}
	info, err := os.Stat(plainOut)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode == 0o600 {
		t.Fatal("switch-on worker forced 0600 on an unprotected source: the recorded facts must win over the process switch")
	}
}

func createRecoverySource(t *testing.T, main string, creatorOnly bool) {
	t.Helper()
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateLive(main, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag, 2, nil, creatorOnly)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != CreationStateCreated {
		t.Fatalf("state = %v", created.State)
	}
}
