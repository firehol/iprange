//go:build linux || darwin || freebsd || netbsd || windows

package reader

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// crossCheckFileIdentityProbe compares one reported file identity against the
// mapping owner's own probe, the single identity authority the reader routes
// through. The constraint above is the union of the internal/mapping
// StatIdentity variants (mapping_identity_probe.go for linux/darwin,
// mapping_identity_probe_bsd.go for freebsd/netbsd,
// mapping_identity_probe_windows.go for Windows); where none of them is
// built, the counterpart file reports the gap as a skip. Removal criterion:
// add the platform's StatIdentity variant in internal/mapping and list its
// GOOS in both this constraint and that counterpart.
func crossCheckFileIdentityProbe(t *testing.T, path string, device, inode uint64) {
	t.Helper()
	device2, inode2, err := mapping.StatIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if device != device2 || inode != inode2 {
		t.Fatalf("identity (%d,%d) want (%d,%d)", device, inode, device2, inode2)
	}
}
