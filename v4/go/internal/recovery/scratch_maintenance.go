package recovery

// Offline discovery and exact removal of abandoned recovery scratch
// (Rust recovery/scratch_maintenance.rs): exact-pattern names are
// listed without following their final component, authenticated
// through the 128-byte ownership header, and removed through the same
// exact-identity machines the live cleanup uses.

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// ScratchOwnerKind is the operation which created an authenticated
// scratch artifact (Rust ScratchOwnerKind).
type ScratchOwnerKind uint16

const (
	ScratchOwnerValidation ScratchOwnerKind = 1
	ScratchOwnerRecovery   ScratchOwnerKind = 2
)

// AbandonedScratchAuthentication classifies one exact-pattern entry
// by its ownership header (Rust AbandonedScratchAuthentication).
type AbandonedScratchAuthentication struct {
	Authenticated bool
	Owner         ScratchOwnerKind
}

func unauthenticatedScratch() AbandonedScratchAuthentication {
	return AbandonedScratchAuthentication{Authenticated: false}
}

func authenticatedScratch(owner ScratchOwnerKind) AbandonedScratchAuthentication {
	return AbandonedScratchAuthentication{Authenticated: true, Owner: owner}
}

// AbandonedScratchEntry is one exact-pattern scratch-directory entry
// (Rust AbandonedScratchEntry).
type AbandonedScratchEntry struct {
	DirectoryIdentity publication.LocalFileIdentity
	ArtifactIdentity  publication.LocalFileIdentity
	AttemptID         [16]byte
	Ordinal           uint32
	Authentication    AbandonedScratchAuthentication
}

// AbandonedScratchList is one completed constant-memory directory
// scan (Rust AbandonedScratchList).
type AbandonedScratchList struct {
	DirectoryIdentity publication.LocalFileIdentity
	Entries           uint64
}

// errScratchSinkStop is the Go control value of the Rust
// AbandonedScratchSinkControl::Stop; deliverScratch maps it to the
// StoppedBySink class at the boundary.
var errScratchSinkStop = errors.New("abandoned scratch listing stopped")

// ListAbandonedScratch lists exact scratch-pattern names without
// following their final component (Rust list_abandoned_scratch): the
// scan proves the directory before and after the stream, exact-pattern
// entries authenticate through their headers, and the sink receives
// every entry. Returning errScratchSinkStop stops the scan.
func ListAbandonedScratch(directoryPath string, check func() error, sink func(entry *AbandonedScratchEntry) error) (AbandonedScratchList, error) {
	if err := live.Checkpoint(check); err != nil {
		return AbandonedScratchList{}, err
	}
	directory, err := live.OpenDirectory(directoryPath)
	if err != nil {
		return AbandonedScratchList{}, maintenanceNamespaceError(err)
	}
	defer directory.Close()
	directoryIdentity := scratchLocal(directory.Identity())
	var count uint64
	scanErr := directory.Scan(func(bytes []byte) error {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		attempt, ordinal, ok := decodeScratchName(bytes)
		if !ok {
			return nil
		}
		found, present, err := inspectScratchEntry(directory, string(bytes), attempt, ordinal, directoryIdentity)
		if err != nil {
			return err
		}
		if !present {
			return nil
		}
		// The Rust visitor checkpoints between the proof and the
		// deliver: a cancellation during the proof suppresses the
		// delivery.
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		if err := deliverScratch(sink, &found); err != nil {
			return err
		}
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		next := count + 1
		if next == 0 {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "abandoned scratch entries"}
		}
		count = next
		return nil
	})
	if scanErr != nil {
		if nerr, isNamespace := live.AsNamespaceError(scanErr); isNamespace {
			return AbandonedScratchList{}, maintenanceNamespaceError(nerr)
		}
		return AbandonedScratchList{}, scanErr
	}
	return AbandonedScratchList{DirectoryIdentity: directoryIdentity, Entries: count}, nil
}

// inspectScratchEntry classifies one exact-pattern entry (Rust
// inspect + inspect_regular: a non-regular or multi-link entry is
// unauthenticated, a regular entry authenticates through its header
// with the re-entry identity check).
func inspectScratchEntry(directory *live.Directory, name string, attempt [16]byte, ordinal uint32, directoryIdentity publication.LocalFileIdentity) (AbandonedScratchEntry, bool, error) {
	found, present, err := directory.Entry(name)
	if err != nil {
		return AbandonedScratchEntry{}, false, maintenanceNamespaceError(err)
	}
	if !present {
		return AbandonedScratchEntry{}, false, nil
	}
	if !found.Regular || found.Links != 1 {
		return newScratchEntry(directoryIdentity, scratchLocal(found.Identity), attempt, ordinal, unauthenticatedScratch()), true, nil
	}
	regular, err := directory.OpenRegular(name, false)
	if err != nil {
		return AbandonedScratchEntry{}, false, maintenanceNamespaceError(err)
	}
	if regular == nil {
		return AbandonedScratchEntry{}, false, nil
	}
	defer regular.File.Close()
	authentication, err := authenticateScratchFile(regular.File, attempt, ordinal)
	if err != nil {
		return AbandonedScratchEntry{}, false, err
	}
	current, present, err := directory.Entry(name)
	if err != nil {
		return AbandonedScratchEntry{}, false, maintenanceNamespaceError(err)
	}
	if !present || !current.Regular || current.Links != 1 || current.Identity != regular.Identity {
		return newScratchEntry(directoryIdentity, scratchLocal(current.Identity), attempt, ordinal, unauthenticatedScratch()), true, nil
	}
	return newScratchEntry(directoryIdentity, scratchLocal(regular.Identity), attempt, ordinal, authentication), true, nil
}

// newScratchEntry builds one exact-pattern entry (Rust
// support::entry).
func newScratchEntry(directoryIdentity, artifactIdentity publication.LocalFileIdentity, attempt [16]byte, ordinal uint32, authentication AbandonedScratchAuthentication) AbandonedScratchEntry {
	return AbandonedScratchEntry{
		DirectoryIdentity: directoryIdentity,
		ArtifactIdentity:  artifactIdentity,
		AttemptID:         attempt,
		Ordinal:           ordinal,
		Authentication:    authentication,
	}
}

// RemoveAbandonedScratch removes one authenticated artifact after the
// caller certifies quiescence (Rust remove_abandoned_scratch).
func RemoveAbandonedScratch(directoryPath string, expectedDirectoryIdentity publication.LocalFileIdentity, attempt [16]byte, ordinal uint32, expectedArtifactIdentity publication.LocalFileIdentity, check func() error) (publication.AbandonedArtifactRemoval, error) {
	if err := live.Checkpoint(check); err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	expectedDirectory, err := maintenanceIdentity(expectedDirectoryIdentity)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	expectedArtifact, err := maintenanceIdentity(expectedArtifactIdentity)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	directory, err := live.OpenDirectory(directoryPath)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, maintenanceNamespaceError(err)
	}
	defer directory.Close()
	if err := requireMaintenanceDirectory(directory, expectedDirectory); err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	name, err := scratchNameOf(attempt, ordinal)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	machine := scratchRemovalMachine{
		directory:        directory,
		name:             name,
		expectedArtifact: expectedArtifact,
		attempt:          attempt,
		ordinal:          ordinal,
	}
	return machine.run(check)
}

// RemoveCheckpointedScratch removes one checkpointed scratch artifact
// recorded by an interrupted worker session (Rust
// remove_checkpointed_scratch): the removal trusts the recorded
// creation security and does not re-read the header.
func RemoveCheckpointedScratch(directoryPath string, expectedDirectoryIdentity publication.LocalFileIdentity, attempt [16]byte, ordinal uint32, expectedArtifactIdentity publication.LocalFileIdentity, creationSecurity publication.CreationSecurity) (publication.AbandonedArtifactRemoval, error) {
	expectedDirectory, err := maintenanceIdentity(expectedDirectoryIdentity)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	expectedArtifact, err := maintenanceIdentity(expectedArtifactIdentity)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	directory, err := live.OpenDirectory(directoryPath)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, maintenanceNamespaceError(err)
	}
	defer directory.Close()
	if err := requireMaintenanceDirectory(directory, expectedDirectory); err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	name, err := scratchNameOf(attempt, ordinal)
	if err != nil {
		return publication.AbandonedArtifactRemoval{}, err
	}
	machine := scratchRemovalMachine{
		directory:        directory,
		name:             name,
		expectedArtifact: expectedArtifact,
		attempt:          attempt,
		ordinal:          ordinal,
	}
	return machine.runCheckpointed(creationSecurity)
}

// deliverScratch runs one sink call and maps the control surface
// (Rust support::deliver: Continue passes, Stop becomes
// StoppedBySink, any sink error becomes SinkFailed). The exported
// publication sentinel is the public control value; the private
// sentinel exists for in-package tests.
func deliverScratch(sink func(entry *AbandonedScratchEntry) error, entry *AbandonedScratchEntry) error {
	if err := sink(entry); err != nil {
		if errors.Is(err, errScratchSinkStop) || errors.Is(err, publication.ErrMaintenanceSinkStop) {
			return &format.Error{Code: format.CodeStoppedBySink, Detail: "abandoned scratch listing stopped"}
		}
		return &format.Error{Code: format.CodeSinkFailed, Detail: err.Error()}
	}
	return nil
}
