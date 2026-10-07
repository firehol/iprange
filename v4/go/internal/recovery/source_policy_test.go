package recovery

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// TestSourcePolicyFollowsTheSidecarRecord pins the source-policy read
// the recovery destination uses (Rust live_sidecar::source_creator_only):
// a sidecar that records protected answers true, one that records
// unprotected answers false, and a path with no sidecar follows the
// process switch. The classifier and attempt-creation assertions run on
// every platform (Windows protection is the DACL, not mode bits); the
// mode discrimination is POSIX-only and detected under umask 0, where a
// switch-following default of a protected source would produce a 0666
// artifact.
func TestSourcePolicyFollowsTheSidecarRecord(t *testing.T) {
	liveGate(t)
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	// umask 0 explicitly: the mode discrimination below assumes it, and
	// an ambient umask like 0077 would strip group/other bits from the
	// unprotected create and false-red the != 0600 assertion.
	previous := setUmask(0)
	defer setUmask(previous)
	dir := t.TempDir()
	tag := [16]byte{}
	copy(tag[:], []byte("asn"))

	protected := filepath.Join(dir, "protected.iprdb")
	if _, err := live.CreateLive(protected, format.AddressFamilyIPv4, format.ValueKindDirect,
		format.StructureKindNone, tag, 2, nil, true); err != nil {
		t.Fatal(err)
	}
	if !sourceIsCreatorOnly(protected) {
		t.Fatal("protected source answered unprotected")
	}
	attempt, failure := publication.CreatePublishAttemptFollowing(
		filepath.Join(dir, "protected-out.iprdb"),
		publication.PolicyFailIfExists, sourceIsCreatorOnly(protected))
	if failure != nil {
		t.Fatalf("protected attempt: %v", failure)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, string(attempt.Facts().Basename)))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("protected recovery output mode = %#o, want 0600", mode)
		}
	}
	attempt.Discard()

	unprotected := filepath.Join(dir, "plain.iprdb")
	if _, err := live.CreateLive(unprotected, format.AddressFamilyIPv4, format.ValueKindDirect,
		format.StructureKindNone, tag, 2, nil, false); err != nil {
		t.Fatal(err)
	}
	if sourceIsCreatorOnly(unprotected) {
		t.Fatal("unprotected source answered protected")
	}
	plainOut := filepath.Join(dir, "plain-out.iprdb")
	attempt, failure = publication.CreatePublishAttemptFollowing(
		plainOut, publication.PolicyFailIfExists, sourceIsCreatorOnly(unprotected))
	if failure != nil {
		t.Fatalf("unprotected attempt: %v", failure)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, string(attempt.Facts().Basename)))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode == 0o600 {
			t.Fatal("unprotected recovery output forced mode 0600")
		}
	}
	attempt.Discard()

	// No sidecar (every immutable source): the process switch decides.
	t.Setenv("IPRANGE_CREATOR_ONLY", "1")
	if !sourceIsCreatorOnly(filepath.Join(dir, "absent.iprdb")) {
		t.Fatal("absent source with switch on answered unprotected")
	}
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	if sourceIsCreatorOnly(filepath.Join(dir, "absent.iprdb")) {
		t.Fatal("absent source with switch off answered protected")
	}
}
