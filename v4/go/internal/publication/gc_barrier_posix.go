//go:build !windows

package publication

import "github.com/firehol/iprange/v4/go/internal/live"

// requireSourceAvailable is a POSIX no-op: the Windows GC custody
// barrier only (Rust gc_barrier::require_source_available
// non-windows arm).
func requireSourceAvailable(_ *live.Directory, _ [16]byte, _ uint32, _ ArtifactKind, _ DirectoryRole, _ string, _ live.FileIdentity) error {
	return nil
}
