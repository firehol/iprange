//go:build windows

package recovery

// Windows retire arms of the abandoned-scratch removal (Rust Removal
// windows arms): the GC resume restores an abandoned retirement, and
// the retire arm runs the GC authority with the header's creation
// security.

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// resumePlatform resumes one abandoned retirement through its GC
// envelope (Rust Removal::resume windows arm); ok is false when no
// envelope exists.
func (r *scratchRemovalMachine) resumePlatform(sourcePresent bool) (publication.AbandonedArtifactRemoval, bool) {
	retired, err := live.GCResume(r.directory, &live.GCResumeAuthority{
		AttemptID:     r.attempt,
		Ordinal:       r.ordinal,
		Kind:          live.ArtifactAuthorizedScratch,
		DirectoryRole: live.DirectoryRoleScratchDirectory,
		SourceName:    r.name,
		Identity:      r.expectedArtifact,
		Payload:       nil,
	})
	if err != nil {
		return maintenanceRemoval(sourcePresent, maintenanceCleanupError(err), publication.HousekeepingNone, nil), true
	}
	if retired == nil {
		return publication.AbandonedArtifactRemoval{}, false
	}
	var visible []publication.HousekeepingArtifact
	if retired.Visible != nil {
		visible = append(visible, *retired.Visible)
	}
	return maintenanceRemoval(sourcePresent, retired.Problem, retired.Housekeeping, visible), true
}

// retirePlatform removes one authenticated artifact with the header's
// creation security (Rust Removal::retire windows arm).
func (r *scratchRemovalMachine) retirePlatform(file *os.File, header scratchDecodedHeader) (publication.AbandonedArtifactRemoval, error) {
	return r.retireCheckpointedPlatform(file, publication.CreationSecurity{
		Kind:       header.securityKind,
		Commitment: header.securityCommitment,
	})
}

// retireCheckpointedPlatform retires one artifact through the GC
// machine (Rust Removal::retire_checkpointed windows arm).
func (r *scratchRemovalMachine) retireCheckpointedPlatform(file *os.File, creationSecurity publication.CreationSecurity) (publication.AbandonedArtifactRemoval, error) {
	retirement := live.GCRetire(r.directory, &live.GCAuthority{
		AttemptID:        r.attempt,
		Ordinal:          r.ordinal,
		Kind:             live.ArtifactAuthorizedScratch,
		DirectoryRole:    live.DirectoryRoleScratchDirectory,
		SourceName:       r.name,
		SourceFile:       file,
		Identity:         r.expectedArtifact,
		CreationSecurity: creationSecurity,
		Payload:          nil,
	})
	var visible []publication.HousekeepingArtifact
	if retirement.Visible != nil {
		visible = append(visible, *retirement.Visible)
	}
	return maintenanceRemoval(true, retirement.Problem, retirement.Housekeeping, visible), nil
}
