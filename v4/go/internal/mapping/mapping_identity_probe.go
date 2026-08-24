//go:build linux || darwin

package mapping

import (
	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// StatIdentity returns the device+inode identity of path (Rust
// Identity{device, inode}). The mapping owner keeps this probe as the
// single identity authority: the reader/snapshot identity tests
// cross-check the reader FileIdentity surface against it (Rust
// identity_any_link comparisons).
func StatIdentity(path string) (device uint64, inode uint64, err error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, &format.Error{Code: format.CodeIO, Detail: "publication filesystem operation failed"}
	}
	return st.Dev, st.Ino, nil
}
