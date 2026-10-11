//go:build windows

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainSatisfiesProtectedContract reports whether the RETAINED, locked
// main descriptor already satisfies the protected contract: the DACL
// zero-additional-access commitment the security module records. Rust
// twin: live_sidecar's main_satisfies_protected_contract.
func mainSatisfiesProtectedContract(main *os.File) bool {
	_, err := security.CreatorOnlyCommitment(main)
	return err == nil
}
