package iprangedb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Recovery follows the source sidecar's recorded choice end to end: a
// protected source recovers to a 0600 output through the worker (the
// parent creates the attempt under the source policy, and the worker
// resumes it under the policy the attempt facts record), and an
// unprotected source does not force 0600. The publication assertions
// run on every platform the live-creation gate allows (the worker
// harness cross-builds for windows too); only the mode assertions are
// POSIX, because Windows protection is the DACL, not mode bits.
// Detected under umask 0, which distinguishes a switch-following
// default (0666) from the source-following 0600; the exact-0600 set's
// umask independence and the worker path's hostile-umask survival are
// pinned separately.
func TestRecoveryFollowsSourceEndToEnd(t *testing.T) {
	installWorkerForTest(t)
	requireLiveCreation(t)
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	dir := t.TempDir()
	// umask 0 distinguishes source-following from switch-following
	// (0600 vs 0666) while the control file's owner bits are floored
	// after create so the worker's O_RDWR re-open survives any umask;
	// the umask-independence of the exact-0600 set is pinned separately
	// by the CLI export detector under umask 0200 and by
	// TestRecoverySurvivesAHostileUmask for the worker path.
	previous := setUmask(0)
	defer setUmask(previous)
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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(protectedOut)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("protected recovery mode = %#o, want 0600 end to end (a switch-following default would stay 0666 under umask 0)", mode)
		}
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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(plainOut)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode == 0o600 {
			t.Fatal("unprotected recovery forced mode 0600")
		}
	}

	// The unprotected exact-default arm (parity round 7): spec 15.6
	// says an unprotected artifact keeps the umask default, and every
	// earlier pin was a != 0600 negation — a hard-coded 0644 passed the
	// whole suite while the true default under umask 027 is 0640. This
	// arm pins the exact mode; the owner-bit floor keeps 0640 (owner
	// bits present) at 0640.
	if runtime.GOOS != "windows" {
		setUmask(0o027)
		exact := filepath.Join(dir, "exact-out.v4")
		result, failure = RecoverLive(plain, candidates.Candidate(0), exact, budget, nil, nil)
		if failure != nil {
			t.Fatalf("recover exact-default: %v", failure.Cause)
		}
		if result.Publication.Publication != PublicationPublished {
			t.Fatalf("exact-default publication = %v", result.Publication.Publication)
		}
		info, err := os.Stat(exact)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o640 {
			t.Fatalf("unprotected recovery mode = %#o, want the exact umask default 0640 (a hard-coded 0644 would fail this arm)", mode)
		}
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
	previous := setUmask(0)
	defer setUmask(previous)
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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(plainOut)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode == 0o600 {
			t.Fatal("switch-on worker forced 0600 on an unprotected source: the recorded facts must win over the process switch")
		}
	}
}

// The worker path survives a hostile umask (parity round 5): the
// control file is re-opened O_RDWR by the worker, so a umask stripping
// owner bits at create used to fail the re-open EACCES and surface as a
// misleading "SDK worker version or protocol does not match" Conflict.
// The unprotected create floors the owner read/write bits after create;
// recovery under umask 0200 must therefore publish. Inspection runs
// under the normal umask (its validation scratch is the separately
// dispositioned residue class); the hostile window wraps only the
// worker-coordinated recover. Rust twin: recovery_follows_source.rs
// recovery_survives_a_hostile_umask.
func TestRecoverySurvivesAHostileUmask(t *testing.T) {
	installWorkerForTest(t)
	requireLiveCreation(t)
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	dir := t.TempDir()
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
	previous := setUmask(0o200)
	plainOut := filepath.Join(dir, "plain-out.v4")
	result, failure := RecoverLive(plain, candidates.Candidate(0), plainOut, budget, nil, nil)
	setUmask(previous)
	if failure != nil {
		t.Fatalf("recover under umask 0200: %v (the worker control file lost its owner-write bits)", failure.Cause)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("publication = %v, want published under umask 0200", result.Publication.Publication)
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
