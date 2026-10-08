//go:build (linux || darwin || freebsd) && (amd64 || arm64)

package worker

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestCreateParentModeIndependentOfUmask proves the secure_creator_only
// core: with the switch on, the control file is exactly 0600 even under
// umask 0 (where a plain 0666 create would stay 0666 and this test
// would fail), and with the switch off under the same umask the file
// follows the process default instead (Rust control.rs create_file +
// security::secure_creator_only), so the worker can always reopen it
// read-write.
func TestCreateParentModeIndependentOfUmask(t *testing.T) {
	t.Setenv("IPRANGE_CREATOR_ONLY", "1")
	previous := unix.Umask(0)
	defer unix.Umask(previous)
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer c.Close()
	st, err := os.Stat(c.path)
	if err != nil {
		t.Fatal("stat control:", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("control mode = %#o, want 0600 (umask 0: a switch-ignoring create would stay 0666)", mode)
	}
}

// TestCreateParentWideningDetectsFloorWidening pins the owner-floor
// widening class (parity round 11): under umask 0 the 0666|0o600 ==
// 0666 identity makes a widened floor invisible, but under umask 027
// the process default is exactly 0640 and a floor widened to |0o666
// produces 0666.
func TestCreateParentWideningDetectsFloorWidening(t *testing.T) {
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	previous := unix.Umask(0o027)
	defer unix.Umask(previous)
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer c.Close()
	st, err := os.Stat(c.path)
	if err != nil {
		t.Fatal("stat control:", err)
	}
	if mode := st.Mode().Perm(); mode != 0o640 {
		t.Fatalf("control mode under umask 027 = %#o, want the exact default 0640 (a widened owner floor would produce 0666)", mode)
	}
}

// TestCreateParentFollowsProcessDefaultWithoutSwitch proves the other
// arm: the control file is a no-source artifact, so with the switch off
// and umask 0 it keeps the process default 0666 rather than forcing
// 0600.
func TestCreateParentFollowsProcessDefaultWithoutSwitch(t *testing.T) {
	t.Setenv("IPRANGE_CREATOR_ONLY", "")
	previous := unix.Umask(0)
	defer unix.Umask(previous)
	c, err := CreateParent()
	if err != nil {
		t.Fatal("create parent:", err)
	}
	defer c.Close()
	st, err := os.Stat(c.path)
	if err != nil {
		t.Fatal("stat control:", err)
	}
	if mode := st.Mode().Perm(); mode != 0o666 {
		t.Fatalf("control mode = %#o, want 0666 (the process default under umask 0)", mode)
	}
}
