//go:build windows

package recovery

// Windows retire arms of the abandoned-scratch removal (Rust Removal
// windows arms): the GC resume restores an abandoned retirement, and
// the retire arm runs the GC authority with the header's creation
// security.

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
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
	}, "abandoned scratch lost its exact name")
}

// retireCheckpointedPlatform retires one artifact through the GC
// machine (Rust Removal::retire_checkpointed windows arm).
//
// The GC move renames the payload through the retained source handle
// and flushes it (FlushFileBuffers), and both operations need a
// writable and delete-capable handle. This arm therefore re-opens the
// authenticated artifact with the exact writable access of the Rust
// `open_regular(name, true)` arm (GENERIC_READ | READ_CONTROL |
// GENERIC_WRITE | FILE_WRITE_ATTRIBUTES | DELETE), re-proves it
// (require_owned + verify-name, exactly like the checkpointed open),
// closes the read-only probe, and then retires the writable handle.
// The Rust windows arm (Removal::open_writable) implements the same
// shape.
func (r *scratchRemovalMachine) retireCheckpointedPlatform(file *os.File, creationSecurity publication.CreationSecurity, lostDetail string) (publication.AbandonedArtifactRemoval, error) {
	writable, err := r.openWritableRetireSource(file, lostDetail)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	retirement := live.GCRetire(r.directory, &live.GCAuthority{
		AttemptID:        r.attempt,
		Ordinal:          r.ordinal,
		Kind:             live.ArtifactAuthorizedScratch,
		DirectoryRole:    live.DirectoryRoleScratchDirectory,
		SourceName:       r.name,
		SourceFile:       writable,
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

// openWritableRetireSource re-opens the probe-authenticated artifact
// with the writable access of the Rust `open_regular(name, true)`
// arm and re-proves it exactly like the checkpointed open (the
// require_owned and verify-name proofs), then closes the read-only
// probe so its share mode cannot block the GC rename. The missing,
// identity-changed, and namespace failure arms map exactly like
// Rust Removal::open_exact / open_exact_checkpointed.
func (r *scratchRemovalMachine) openWritableRetireSource(probe *os.File, lostDetail string) (*os.File, error) {
	regular, err := r.directory.OpenRegular(r.name, true)
	if err != nil {
		probe.Close()
		return nil, maintenanceNamespaceError(err)
	}
	if regular == nil {
		probe.Close()
		return nil, &format.Error{Code: format.CodeCleanupConflict, Detail: lostDetail}
	}
	if err := requireMaintenanceOwned(true, 1, regular.Identity, r.expectedArtifact); err != nil {
		regular.File.Close()
		probe.Close()
		return nil, err
	}
	if err := r.directory.VerifyName(r.name, r.expectedArtifact); err != nil {
		regular.File.Close()
		probe.Close()
		return nil, maintenanceCleanupError(err)
	}
	probe.Close()
	return regular.File, nil
}
