//go:build linux || darwin || freebsd

package worker

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestCreateParentModeIndependentOfUmask proves the secure_creator_only
// core: the control file is exactly 0600 even under a restrictive umask
// (Rust control.rs create_file + security::secure_creator_only), so the
// worker can always reopen it read-write.
func TestCreateParentModeIndependentOfUmask(t *testing.T) {
	previous := unix.Umask(0o077)
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
		t.Fatalf("control mode = %#o, want 0600", mode)
	}
}
