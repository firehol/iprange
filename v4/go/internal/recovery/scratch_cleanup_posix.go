//go:build !windows

package recovery

// POSIX scratch cleanup (Rust recovery/scratch/cleanup.rs unix arm):
// every owned artifact is removed exactly, the removal is proved
// through the directory sync/verify and the absent-name proof, and
// each unproved removal is retained as an exact residue.

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// cleanupPlatform runs the POSIX removal machine (Rust
// Scratch::cleanup_unix).
func (s *scratch) cleanupPlatform() *scratchCleanup {
	directoryIdentity := scratchLocal(s.directory.Identity())
	cleanup := &scratchCleanup{
		attemptID:                  s.attemptID,
		directoryIdentity:          directoryIdentity,
		creationSecurityKind:       scratchCreationSecurityKind(),
		creationSecurityCommitment: s.profile.Commitment(),
	}
	var removed [scratchMaxOwned]bool
	var problems [scratchMaxOwned]*scratchProblem
	for index := 0; index < scratchMaxOwned; index++ {
		owner := s.owned[index]
		if owner == nil {
			continue
		}
		if err := scratchRemovePosix(s.directory, owner); err != nil {
			problems[index] = err
		} else {
			removed[index] = true
		}
	}
	s.proveRemovals(removed[:], problems[:])
	for index, problem := range problems {
		if problem == nil {
			continue
		}
		owner := s.owned[index]
		cleanup.residues = append(cleanup.residues, scratchResidueOf(directoryIdentity, s.profile, owner, *problem))
		s.owned[index] = nil
		if owner.shared != nil {
			owner.shared.close()
		}
	}
	for index := 0; index < scratchMaxOwned; index++ {
		owner := s.owned[index]
		if owner == nil {
			continue
		}
		s.owned[index] = nil
		if owner.shared != nil {
			owner.shared.close()
		}
	}
	return cleanup
}

// proveRemovals proves every successful removal durable (Rust
// Scratch::prove_removals: the directory sync and verify, then the
// absent-name proof per removed owner; any proof failure replaces the
// removal problem set).
func (s *scratch) proveRemovals(removed []bool, problems []*scratchProblem) {
	anyRemoved := false
	for _, value := range removed {
		if value {
			anyRemoved = true
			break
		}
	}
	if !anyRemoved {
		return
	}
	verifyErr := s.directory.Sync()
	if verifyErr == nil {
		verifyErr = s.directory.Verify()
	}
	if verifyErr != nil {
		problem := scratchProblemOfNamespace(scratchNamespaceError(verifyErr))
		for index := 0; index < scratchMaxOwned; index++ {
			if removed[index] {
				problems[index] = problem
			}
		}
		return
	}
	for index := 0; index < scratchMaxOwned; index++ {
		owner := s.owned[index]
		if owner == nil || !removed[index] {
			continue
		}
		if err := s.directory.RequireAbsent(owner.name); err != nil {
			problem := scratchProblemOfNamespace(scratchNamespaceError(err))
			problems[index] = problem
		}
	}
}

// scratchRemovePosix removes one owned artifact exactly (Rust
// cleanup::remove: the named-link proof, the exact unlink, and the
// unlinked proof; an already-absent artifact is a clean removal).
func scratchRemovePosix(directory *live.Directory, owner *scratchOwned) *scratchProblem {
	if err := requireNamedLink(directory, owner); err != nil {
		return err
	}
	removed, err := directory.UnlinkExact(owner.name, owner.identity)
	if err != nil {
		return scratchProblemOfNamespace(scratchNamespaceError(err))
	}
	if !removed {
		return conflictScratchProblem("owned recovery scratch lost its exact name")
	}
	return requireUnlinked(owner)
}

// requireNamedLink proves the owned artifact is still named exactly
// once (Rust cleanup::require_named_link: unexpected links are a
// conflict, an absent artifact proves its own durable absence).
func requireNamedLink(directory *live.Directory, owner *scratchOwned) *scratchProblem {
	links, err := live.RegularLinkCount(owner.shared.file)
	if err != nil {
		return scratchProblemOfNamespace(scratchNamespaceError(err))
	}
	if links > 1 {
		return conflictScratchProblem("owned recovery scratch has unexpected links")
	}
	if links == 0 {
		if err := directory.RequireAbsent(owner.name); err != nil {
			return scratchProblemOfNamespace(scratchNamespaceError(err))
		}
		return nil
	}
	return nil
}

// requireUnlinked proves the removed artifact no longer carries its
// name (Rust cleanup::require_unlinked).
func requireUnlinked(owner *scratchOwned) *scratchProblem {
	links, err := live.RegularLinkCount(owner.shared.file)
	if err != nil {
		return scratchProblemOfNamespace(scratchNamespaceError(err))
	}
	if links != 0 {
		return conflictScratchProblem("owned recovery scratch remained linked after removal")
	}
	return nil
}

// scratchProblemOfNamespace maps one scratch-owner failure to its
// residue problem (Rust cleanup::scratch_problem over the namespace
// classes).
func scratchProblemOfNamespace(err error) *scratchProblem {
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		var fe *format.Error
		if errors.As(err, &fe) {
			return &scratchProblem{code: fe.Code, detail: fe.Detail}
		}
		return conflictScratchProblem("recovery scratch ownership changed")
	}
	switch nerr.Kind {
	case live.NamespaceForkedHandle:
		return &scratchProblem{code: format.CodeForkedHandle, detail: "scratch owner crossed fork"}
	case live.NamespaceIo, live.NamespaceIoAt:
		return &scratchProblem{
			code:   format.CodeIO,
			osCode: rawOSError(nerr.Err),
			detail: "recovery scratch cleanup failed",
		}
	default:
		return conflictScratchProblem("recovery scratch ownership changed")
	}
}
