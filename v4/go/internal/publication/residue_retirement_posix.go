//go:build !windows

package publication

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/live"
)

// retireResidueCoordination unlinks one coordination inode at its
// canonical name (Rust retirement::retire, unix arm: every unlink
// error is the ownership-changed class, a missing name is the
// disappeared class, and the retained link count must prove zero).
func retireResidueCoordination(destination *destination, file *os.File, identity live.FileIdentity) (retirementOutcome, error) {
	ok, err := destination.directory().UnlinkExact(destination.coordinationName(), identity)
	if err != nil {
		return retirementOutcome{}, cleanupConflictProblem("canonical coordination ownership changed")
	}
	if !ok {
		return retirementOutcome{}, cleanupConflictProblem("canonical coordination disappeared before removal")
	}
	return retirementOutcome{
		cause:        linkCountProofResidue(file),
		housekeeping: HousekeepingNone,
		visible:      nil,
	}, nil
}

// retryResidueRetirement re-proves the coordination unlink (Rust
// retirement::retry unix arm: only the retained link count; the
// retirement-pending flag feeds only the Windows arm).
func retryResidueRetirement(_ *destination, file *os.File, _ live.FileIdentity, _ bool) retirementOutcome {
	return retirementOutcome{
		cause:        linkCountProofResidue(file),
		housekeeping: HousekeepingNone,
		visible:      nil,
	}
}

// linkCountProofResidue proves the removed coordination inode no
// longer has links (Rust regular_link_count proof).
func linkCountProofResidue(file *os.File) error {
	count, err := live.RegularLinkCount(file)
	switch {
	case err != nil:
		return namespaceProblem(err)
	case count != 0:
		return cleanupConflictProblem("removed coordination inode remains linked")
	}
	return nil
}
