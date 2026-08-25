//go:build windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// retireResidueCoordination retires one coordination inode through
// its authenticated GC transition (Rust retirement.rs retire windows
// arm): a fresh collision-free attempt id is drawn for the exact
// source, the creator-only commitment of the retained handle is
// captured, and the envelope move runs the common resolver.
func retireResidueCoordination(destination *destination, file *os.File, identity live.FileIdentity) (retirementOutcome, error) {
	attemptID, err := live.GCFreshAttempt(destination.directory(), destination.coordinationName(), identity, 1, ArtifactOwnedCoordination, DirectoryRoleDestination)
	if err != nil {
		return retirementOutcome{}, err
	}
	commitment, commitErr := security.CreatorOnlyCommitment(file)
	if commitErr != nil {
		return retirementOutcome{}, namespaceProblem(commitErr)
	}
	retired := live.GCRetire(destination.directory(), &live.GCAuthority{
		AttemptID:     attemptID,
		Ordinal:       1,
		Kind:          ArtifactOwnedCoordination,
		DirectoryRole: DirectoryRoleDestination,
		SourceName:    destination.coordinationName(),
		SourceFile:    file,
		Identity:      identity,
		CreationSecurity: CreationSecurity{
			Kind:       creationSecurityKind,
			Commitment: commitment,
		},
		Payload: nil,
	})
	outcome := retirementOutcome{
		cause:        retired.Problem,
		housekeeping: retired.Housekeeping,
	}
	if retired.Visible != nil {
		outcome.visible = append(outcome.visible, *retired.Visible)
	}
	return outcome, nil
}

// retryResidueRetirement re-runs the GC transition only when the
// retirement was pending and the coordination name still names the
// exact identity (Rust retirement.rs retry windows arm); every other
// retry is clean without further namespace work.
func retryResidueRetirement(destination *destination, file *os.File, identity live.FileIdentity, retirementPending bool) retirementOutcome {
	if retirementPending && destination.directory().VerifyName(destination.coordinationName(), identity) == nil {
		retired, err := retireResidueCoordination(destination, file, identity)
		if err != nil {
			return retirementOutcome{cause: err}
		}
		return retired
	}
	return retirementOutcome{}
}
