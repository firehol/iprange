//go:build !windows

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainSatisfiesProtectedContract reports whether the RETAINED, locked
// main descriptor already satisfies the protected contract, using the
// COMPLETE authoritative proof (the sol gate's round-3/4 P1s):
// mode+nlink alone accepts a 0600 file carrying an extended ACL or
// special bits, which the proof every later open demands would
// reject — and reopening the PATH would re-arm the Go poller's fatal
// initialization and block on a substituted FIFO. The descriptor the
// transition already holds and validated is the identity to prove.
// Rust twin: live_sidecar's main_satisfies_protected_contract.
func mainSatisfiesProtectedContract(main *os.File) bool {
	_, err := security.CreatorOnlyCommitment(main)
	return err == nil
}
