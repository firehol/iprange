//go:build !windows

package recovery

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// TestSourcePolicyFollowsTheSidecarRecord pins the source-policy read
// the recovery destination uses (Rust live_sidecar::source_creator_only):
// a sidecar that records protected answers true, one that records
// unprotected answers false, and a path with no sidecar follows the
// process switch. Detected under umask 0, where a switch-following
// default of a protected source would produce a 0666 artifact.
func TestSourcePolicyFollowsTheSidecarRecord(t *testing.T) {
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
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
	info, err := os.Stat(filepath.Join(dir, string(attempt.Facts().Basename)))
	attempt.Discard()
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("protected recovery output mode = %#o, want 0600", mode)
	}

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
	info, err = os.Stat(filepath.Join(dir, string(attempt.Facts().Basename)))
	attempt.Discard()
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode == 0o600 {
		t.Fatal("unprotected recovery output forced mode 0600")
	}

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
