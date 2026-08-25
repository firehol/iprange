//go:build windows

package publication

import "github.com/firehol/iprange/v4/go/internal/live"

// requireSourceAvailable proves one retained source is not owned by
// Windows housekeeping before any ordinary open (Rust
// gc_barrier::require_source_available windows arm): a matching
// selected envelope means cleanup owns the inode and the ordinary
// operation must fail with CleanupInProgress.
func requireSourceAvailable(directory *live.Directory, attemptID [16]byte, ordinal uint32, kind ArtifactKind, directoryRole DirectoryRole, name string, identity live.FileIdentity) error {
	return live.GCRequireSourceAvailable(directory, attemptID, ordinal, kind, directoryRole, name, identity)
}
