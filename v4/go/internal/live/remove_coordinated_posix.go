//go:build !windows

package live

import (
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// removeCoordinated removes the path only when it still names the
// retained identity and synchronizes the parent directory (Rust
// live_cleanup::remove POSIX arm: live_namespace::remove_exact; the
// file and authority feed only the Windows GC transition).
func removeCoordinated(path string, _ *os.File, expected FileIdentity, _ cleanupAuthority) cleanupOutcome {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	defer dir.Close()
	if err := dir.VerifyName(name, expected); err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	removed, err := dir.UnlinkExact(name, expected)
	if err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	if !removed {
		return cleanupOutcomeFailed(&format.Error{Code: format.CodeNameNotFound, Detail: "feed name does not exist"})
	}
	if err := dir.Sync(); err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	if err := dir.RequireAbsent(name); err != nil {
		return cleanupOutcomeFailed(nsMap(err))
	}
	return cleanupOutcome{}
}
