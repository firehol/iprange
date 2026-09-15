//go:build !windows

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// resumePlatform is the no-op POSIX arm (Rust Artifact::remove has no
// resume before open_owned on unix: an unlink cannot be abandoned half
// way, so there is no envelope to restore).
func (a *maintenanceArtifact) resumePlatform(dir *live.Directory, attempt [16]byte, name string, expected live.FileIdentity, ordinal uint32, kind live.ArtifactKind, payload *maintenanceRetirementPayload, sourcePresent bool) (AbandonedArtifactRemoval, bool) {
	return AbandonedArtifactRemoval{}, false
}

// retirePlatform unlinks the exact inode and proves the retained link
// count and the durable absence (Rust Artifact::retire_unix: every
// unlink error is the cleanup class, a missing exact name is the lost
// class, a linked inode is the remained-linked cause, and the
// post-removal absence failure folds into the sdk class). The
// retirement authority is unused here: POSIX removes the name directly
// and needs no GC envelope.
func (a *maintenanceArtifact) retirePlatform(dir *live.Directory, name string, regular *live.RegularFile, attempt [16]byte, expected live.FileIdentity, ordinal uint32, kind live.ArtifactKind, payload *maintenanceRetirementPayload) (AbandonedArtifactRemoval, error) {
	unlinked, err := dir.UnlinkExact(name, expected)
	if err != nil {
		return AbandonedArtifactRemoval{}, a.cleanupError(err)
	}
	if !unlinked {
		return AbandonedArtifactRemoval{}, problem(format.CodeCleanupConflict, a.lostName)
	}
	count, err := live.RegularLinkCount(regular.File)
	var cause error
	switch {
	case err != nil:
		cause = namespaceProblem(err)
	case count != 0:
		cause = problem(format.CodeCleanupConflict, a.remainedLinked)
	}
	if cause != nil {
		return removalResult(true, cause), nil
	}
	if err := a.durableAbsence(dir, name); err != nil {
		return removalResult(true, sdkProblem(err)), nil
	}
	return removalResult(true, nil), nil
}
