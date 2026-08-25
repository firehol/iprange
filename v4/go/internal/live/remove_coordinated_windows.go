//go:build windows

package live

import (
	"os"
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// removeCoordinated retires the exact artifact through the Windows GC
// transition (Rust live_cleanup::remove windows arm: the creator-only
// commitment of the retained handle is captured and the artifact moves
// to its authenticated inert name before any unlink).
func removeCoordinated(path string, file *os.File, identity FileIdentity, authority cleanupAuthority) cleanupOutcome {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		return cleanupOutcomeFailed(gcNamespaceProblem(err))
	}
	defer dir.Close()
	commitment, err := security.CreatorOnlyCommitment(file)
	if err != nil {
		return cleanupOutcomeFailed(gcNamespaceProblem(err))
	}
	retired := gcRetire(dir, gcAuthority{
		attemptID:     authority.attemptID,
		ordinal:       authority.ordinal,
		kind:          authority.kind,
		directoryRole: authority.directoryRole,
		sourceName:    name,
		sourceFile:    file,
		identity:      identity,
		creationSecurity: CreationSecurity{
			Kind:       gcCreationSecurityKind(),
			Commitment: commitment,
		},
		payload: nil,
	})
	outcome := cleanupOutcome{
		cause:        retired.problem,
		housekeeping: retired.housekeeping,
	}
	if retired.visible != nil {
		outcome.visible = append(outcome.visible, *retired.visible)
	}
	return outcome
}
