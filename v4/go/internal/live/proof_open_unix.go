//go:build !windows

package live

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// openForCreatorProof opens the main file for the creator-only proof.
// os.Open registers the descriptor with the runtime poller, which aborts
// the process under a low descriptor limit. The live path already opens
// through calleropen, so the proof uses that same open.
func openForCreatorProof(path string) (*os.File, error) {
	return calleropen.Open(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}
