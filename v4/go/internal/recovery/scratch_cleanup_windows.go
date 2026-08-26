//go:build windows

package recovery

// Windows scratch cleanup (Rust Scratch::cleanup_windows): every owned
// artifact retires through the GC machine with the AuthorizedScratch
// authority and the ScratchDirectory role; each retirement problem is
// retained as an exact residue and the housekeeping evidence merges
// into the cleanup terminal.

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
)

// cleanupPlatform runs the Windows GC retirement machine (Rust
// Scratch::cleanup_windows).
func (s *scratch) cleanupPlatform() *scratchCleanup {
	directoryIdentity := scratchLocal(s.directory.Identity())
	cleanup := &scratchCleanup{
		attemptID:                  s.attemptID,
		directoryIdentity:          directoryIdentity,
		creationSecurityKind:       scratchCreationSecurityKind(),
		creationSecurityCommitment: s.profile.Commitment(),
	}
	for index := 0; index < scratchMaxOwned; index++ {
		owner := s.owned[index]
		if owner == nil {
			continue
		}
		s.owned[index] = nil
		retirement := live.GCRetire(s.directory, &live.GCAuthority{
			AttemptID:     s.attemptID,
			Ordinal:       owner.ordinal,
			Kind:          live.ArtifactAuthorizedScratch,
			DirectoryRole: live.DirectoryRoleScratchDirectory,
			SourceName:    owner.name,
			SourceFile:    owner.shared.file,
			Identity:      owner.identity,
			CreationSecurity: publication.CreationSecurity{
				Kind:       scratchCreationSecurityKind(),
				Commitment: s.profile.Commitment(),
			},
			Payload: nil,
		})
		cleanup.housekeeping = cleanup.housekeeping.Merge(retirement.Housekeeping)
		if retirement.Visible != nil {
			cleanup.visibleHousekeeping = append(cleanup.visibleHousekeeping, *retirement.Visible)
		}
		if retirement.Problem != nil {
			problem := scratchProblemOfFormat(retirement.Problem)
			cleanup.residues = append(cleanup.residues, scratchResidueOf(directoryIdentity, s.profile, owner, problem))
		}
	}
	return cleanup
}

// scratchProblemOfFormat maps one GC retirement problem to its residue
// problem (Rust cleanup::residue over the retirement problem facts).
func scratchProblemOfFormat(cause error) scratchProblem {
	var fe *format.Error
	if errors.As(cause, &fe) {
		return scratchProblem{code: fe.Code, detail: fe.Detail}
	}
	return *conflictScratchProblem("recovery scratch ownership changed")
}
