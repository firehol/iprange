//go:build windows

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainSatisfiesProtectedContract reports whether the main file's own
// on-disk state already satisfies the protected contract: the DACL
// zero-additional-access commitment the security module records (a
// main the creator-only path secured carries it). Rust twin:
// live_sidecar's main_satisfies_protected_contract.
func mainSatisfiesProtectedContract(main string) bool {
	file, err := os.Open(main)
	if err != nil {
		return false
	}
	defer file.Close()
	_, err = security.CreatorOnlyCommitment(file)
	return err == nil
}
