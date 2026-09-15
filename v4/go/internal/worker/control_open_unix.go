//go:build !windows && (amd64 || arm64)

package worker

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// openControlFile opens an existing control file read-write (Rust
// Control::open_worker). The POSIX handle needs no share mode; the
// Windows arm must advertise DELETE sharing because the creator-only
// control handle grants FILE_ALL_ACCESS.
func openControlFile(path string) (*os.File, error) {
	// The owner-side open keeps the control page out of the runtime
	// network poller, whose initialization has no failure path under a low
	// RLIMIT_NOFILE (wave-19.25 design section 5).
	return calleropen.Open(path, os.O_RDWR|calleropen.NonBlocking, 0)
}
