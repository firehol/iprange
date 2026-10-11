// The initialize/reset transitions follow the recorded source choice
// (the milestone's own rule applied to its own transitions): resetting
// an UNPROTECTED database records Unprotected, and the next live open
// succeeds without a creator-only proof. The hardcoded-true defect
// (the sol milestone gate's P1) recorded Protected over an unprotected
// source and locked later live opens out. Rust twin:
// tests/transitions_follow_source.rs.
package live

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// resetPolicy picks the strongest reset policy the platform supports:
// rollback-safe needs renameat2 exchange (Linux); Windows uses
// discard-previous. The creator-only resolution under test is the
// same either way.
func resetPolicy() LiveResetPolicy {
	if mapping.ExchangeAvailable() {
		return LiveResetRollbackSafe
	}
	return LiveResetDiscardPrevious
}

func followsSourcePair(t *testing.T, label string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "iprange-v4-follows-source-"+label+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "source.v4")
}

func createUnprotectedForTransition(t *testing.T, main string) {
	t.Helper()
	umaskWindow(t, 0o022)
	_, err := CreateLive(main, format.AddressFamilyIPv4,
		format.ValueKindDirect, format.StructureKindNone,
		[16]byte{}, 3, neverCheck, false)
	if err != nil {
		t.Fatal("create unprotected:", err)
	}
}

func openLiveReaderForFollows(t *testing.T, main string) {
	t.Helper()
	reader, err := OpenLiveReader(main, nil)
	if err != nil {
		t.Fatalf("live open after the transition: %v", err)
	}
	reader.Close()
}

func TestResetOfUnprotectedDatabaseStaysOpenable(t *testing.T) {
	main := followsSourcePair(t, "reset")
	createUnprotectedForTransition(t, main)

	if _, err := ResetLiveCoordination(main, 3, resetPolicy(), neverCheck); err != nil {
		t.Fatal("reset:", err)
	}
	// The very next open after the maintenance operation.
	openLiveReaderForFollows(t, main)
}

func TestInitializeOfUnprotectedImmutableSourceStaysOpenable(t *testing.T) {
	main := followsSourcePair(t, "init")
	createUnprotectedForTransition(t, main)
	// The immutable shape: the sidecar absent (the transition's own
	// fixture pattern).
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal("remove sidecar:", err)
	}

	if _, err := InitializeLive(main, 3, neverCheck); err != nil {
		t.Fatal("initialize:", err)
	}
	openLiveReaderForFollows(t, main)
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestUnprotectedModesSurviveTheTransitions(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mode assertions are POSIX")
	}
	main := followsSourcePair(t, "modes")
	createUnprotectedForTransition(t, main)

	if _, err := ResetLiveCoordination(main, 3, resetPolicy(), neverCheck); err != nil {
		t.Fatal("reset:", err)
	}
	if mode := fileMode(t, main); mode != 0o644 {
		t.Fatalf("main mode after reset = %#o, want 0644", mode)
	}
}

func TestSwitchOnCannotLockOutAnUnprotectedMain(t *testing.T) {
	// The sol gate's round-2 P1: with IPRANGE_CREATOR_ONLY=1 and a
	// missing sidecar, the transition must NOT record Protected over
	// an unprotected main. The compatible fallback classifies from
	// the main's own state.
	main := followsSourcePair(t, "switch-on")
	createUnprotectedForTransition(t, main)
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal("remove sidecar:", err)
	}
	t.Setenv("IPRANGE_CREATOR_ONLY", "1")
	if _, err := InitializeLive(main, 3, neverCheck); err != nil {
		t.Fatal("initialize:", err)
	}
	openLiveReaderForFollows(t, main)
}

func TestSwitchOnPreservesAnAlreadyProtectedMain(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mode shaping is POSIX")
	}
	main := followsSourcePair(t, "switch-on-prot")
	createUnprotectedForTransition(t, main)
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal("remove sidecar:", err)
	}
	if err := os.Chmod(main, 0o600); err != nil {
		t.Fatal("chmod:", err)
	}
	if _, err := InitializeLive(main, 3, neverCheck); err != nil {
		t.Fatal("initialize:", err)
	}
	// The PRESERVATION assertion (sol round-4 P2): reopening alone
	// cannot detect a fallback that always records Unprotected — the
	// sidecar itself must record Protected for a protected main.
	if !SourceCreatorOnly(main) {
		t.Fatal("a protected main must preserve the Protected policy through the transition")
	}
	openLiveReaderForFollows(t, main)
}

func TestSpecialBitsMainIsNotClassifiedProtected(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("mode shaping is POSIX")
	}
	// The sol gate's round-3 P1: mode 04600 (setuid) must classify
	// Unprotected — Mode().Perm() drops the special bits, but the
	// authoritative proof rejects them.
	main := followsSourcePair(t, "special-bits")
	createUnprotectedForTransition(t, main)
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal("remove sidecar:", err)
	}
	if err := os.Chmod(main, 0o600|os.ModeSetuid); err != nil {
		t.Fatal("chmod:", err)
	}
	if _, err := InitializeLive(main, 3, neverCheck); err != nil {
		t.Fatal("initialize:", err)
	}
	openLiveReaderForFollows(t, main)
}

func TestACLCarryingMainIsNotClassifiedProtected(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("setfacl is a Linux tool")
	}
	// The exact shape a mode-only classifier misses (the sol gate's
	// round-3 P1): a named-user ACL entry granting nothing keeps the
	// mask (and the mode's group bits) empty — mode stays 0600 — but
	// the access ACL is EXTENDED, which the complete proof rejects.
	main := followsSourcePair(t, "acl-main")
	createUnprotectedForTransition(t, main)
	if err := os.Remove(main + ".readers"); err != nil {
		t.Fatal("remove sidecar:", err)
	}
	if err := os.Chmod(main, 0o600); err != nil {
		t.Fatal("chmod:", err)
	}
	if out, err := exec.Command("setfacl", "-m", "u:65534:---", main).CombinedOutput(); err != nil {
		t.Skipf("setfacl unavailable (%v): %s", err, out)
	}
	if info, err := os.Stat(main); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("the ACL fixture must keep mode 0600, got %#o", info.Mode().Perm())
	}
	if _, err := InitializeLive(main, 3, neverCheck); err != nil {
		t.Fatal("initialize:", err)
	}
	openLiveReaderForFollows(t, main)
}
