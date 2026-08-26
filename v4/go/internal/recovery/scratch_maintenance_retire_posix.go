//go:build !windows

package recovery

// POSIX retire arms of the abandoned-scratch removal (Rust Removal
// unix arms): the exact unlink, the unlinked link-count proof, and
// the durable-absence proof.

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// resumePlatform is the no-op POSIX arm (Rust resume is
// windows-only).
func (r *scratchRemovalMachine) resumePlatform(bool) (publication.AbandonedArtifactRemoval, bool) {
	return publication.AbandonedArtifactRemoval{}, false
}

// retirePlatform removes one authenticated artifact (Rust
// Removal::retire unix arm: the fixed empty creation security).
func (r *scratchRemovalMachine) retirePlatform(file *os.File, _ scratchDecodedHeader) (publication.AbandonedArtifactRemoval, error) {
	return r.retireCheckpointedPlatform(file, publication.CreationSecurity{})
}

// retireCheckpointedPlatform removes one artifact through the exact
// unlink and proves the durable absence (Rust Removal::
// retire_checkpointed unix arm).
func (r *scratchRemovalMachine) retireCheckpointedPlatform(file *os.File, _ publication.CreationSecurity) (publication.AbandonedArtifactRemoval, error) {
	removed, err := r.directory.UnlinkExact(r.name, r.expectedArtifact)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, maintenanceCleanupError(err)
	}
	if !removed {
		return publication.AbandonedArtifactRemoval{}, &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch lost its exact name"}
	}
	links, err := live.RegularLinkCount(file)
	switch {
	case err == nil && links == 0:
		// The durable absence proof runs below.
	case err == nil:
		return maintenanceRemoval(true, &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch remained linked after removal"}, publication.HousekeepingNone, nil), nil
	default:
		return maintenanceRemoval(true, maintenanceNamespaceError(err), publication.HousekeepingNone, nil), nil
	}
	if err := durableScratchAbsence(r.directory, r.name); err != nil {
		return maintenanceRemoval(true, err, publication.HousekeepingNone, nil), nil
	}
	return maintenanceRemoval(true, nil, publication.HousekeepingNone, nil), nil
}
