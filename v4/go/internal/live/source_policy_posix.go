//go:build !windows

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// mainSatisfiesProtectedContract reports whether the main file's own
// on-disk state already satisfies the protected contract, using the
// COMPLETE authoritative proof on the retained descriptor (the sol
// gate's round-3 P1): mode+nlink alone accepts a 0600 file carrying an
// extended access ACL or special bits, which the proof every later
// open demands would reject. The proof is the one the reader
// enforces — trivial ACL, regular, single link, mode exactly the
// creator mode including the special bits. Rust twin: live_sidecar's
// main_satisfies_protected_contract.
func mainSatisfiesProtectedContract(main string) bool {
	file, err := os.Open(main)
	if err != nil {
		return false
	}
	defer file.Close()
	_, err = security.CreatorOnlyCommitment(file)
	return err == nil
}
