//go:build linux || darwin || freebsd

package worker

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// testFileIdentity returns the device+inode pair of one retained file
// (Rust regular_identity; the scratch-checkpoint fixtures capture the
// live identities of the created artifact and its directory).
func testFileIdentity(t *testing.T, f *os.File) (device, inode uint64) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal("fstat:", err)
	}
	return uint64(st.Dev), uint64(st.Ino)
}
