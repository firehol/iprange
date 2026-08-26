package recovery

// Exact removal machine of one abandoned scratch artifact (Rust
// scratch_maintenance platform::Removal): the present-name proof, the
// exact open with header authentication, and the platform retire arm.

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// scratchRemovalMachine is one exact abandoned-scratch removal (Rust
// platform::Removal).
type scratchRemovalMachine struct {
	directory        *live.Directory
	name             string
	expectedArtifact live.FileIdentity
	attempt          [16]byte
	ordinal          uint32
}

// run executes one removal against the retained directory (Rust
// Removal::run: the present proof, the windows resume arm, the
// durable-absence proof for an already-absent artifact, and the exact
// open with the header authentication before the platform retire).
func (r *scratchRemovalMachine) run(check func() error) (publication.AbandonedArtifactRemoval, error) {
	present, err := r.present()
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	if resumed, ok := r.resumePlatform(present); ok {
		return resumed, nil
	}
	if !present {
		if err := durableScratchAbsence(r.directory, r.name); err != nil {
			return publication.AbandonedArtifactRemoval{}, err
		}
		return maintenanceRemoval(false, nil, publication.HousekeepingNone, nil), nil
	}
	file, header, err := r.openExact()
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	if err := live.Checkpoint(check); err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	return r.retirePlatform(file, header)
}

// runCheckpointed executes one checkpointed removal (Rust
// Removal::run_checkpointed): the same flow without the header
// authentication, retiring with the recorded creation security.
func (r *scratchRemovalMachine) runCheckpointed(creationSecurity publication.CreationSecurity) (publication.AbandonedArtifactRemoval, error) {
	present, err := r.present()
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	if resumed, ok := r.resumePlatform(present); ok {
		return resumed, nil
	}
	if !present {
		if err := durableScratchAbsence(r.directory, r.name); err != nil {
			return publication.AbandonedArtifactRemoval{}, err
		}
		return maintenanceRemoval(false, nil, publication.HousekeepingNone, nil), nil
	}
	file, err := r.openExactCheckpointed()
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	return r.retireCheckpointedPlatform(file, creationSecurity, "checkpointed scratch lost its exact name")
}

// present proves the artifact is still present and exactly owned
// (Rust Removal::present).
func (r *scratchRemovalMachine) present() (bool, error) {
	found, present, err := r.directory.Entry(r.name)
	if err != nil {
		return false, maintenanceNamespaceError(err)
	}
	if !present {
		return false, nil
	}
	if err := requireMaintenanceOwned(found.Regular, found.Links, found.Identity, r.expectedArtifact); err != nil {
		return false, err
	}
	return true, nil
}

// openExact opens the artifact and authenticates its header (Rust
// Removal::open_exact).
func (r *scratchRemovalMachine) openExact() (*os.File, scratchDecodedHeader, error) {
	regular, err := r.directory.OpenRegular(r.name, false)
	if err != nil {
		return nil, scratchDecodedHeader{}, maintenanceNamespaceError(err)
	}
	if regular == nil {
		return nil, scratchDecodedHeader{}, &format.Error{Code: format.CodeCleanupConflict, Detail: "abandoned scratch lost its exact name"}
	}
	if err := requireMaintenanceOwned(true, 1, regular.Identity, r.expectedArtifact); err != nil {
		regular.File.Close()
		return nil, scratchDecodedHeader{}, err
	}
	header, err := requireScratchHeader(regular.File, r.attempt, r.ordinal)
	if err != nil {
		regular.File.Close()
		return nil, scratchDecodedHeader{}, err
	}
	if err := r.directory.VerifyName(r.name, r.expectedArtifact); err != nil {
		regular.File.Close()
		return nil, scratchDecodedHeader{}, maintenanceCleanupError(err)
	}
	return regular.File, header, nil
}

// openExactCheckpointed opens the artifact without re-reading its
// header (Rust Removal::open_exact_checkpointed).
func (r *scratchRemovalMachine) openExactCheckpointed() (*os.File, error) {
	regular, err := r.directory.OpenRegular(r.name, false)
	if err != nil {
		return nil, maintenanceNamespaceError(err)
	}
	if regular == nil {
		return nil, &format.Error{Code: format.CodeCleanupConflict, Detail: "checkpointed scratch lost its exact name"}
	}
	if err := requireMaintenanceOwned(true, 1, regular.Identity, r.expectedArtifact); err != nil {
		regular.File.Close()
		return nil, err
	}
	if err := r.directory.VerifyName(r.name, r.expectedArtifact); err != nil {
		regular.File.Close()
		return nil, maintenanceCleanupError(err)
	}
	return regular.File, nil
}
