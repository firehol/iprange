//go:build (linux || darwin || freebsd) && (amd64 || arm64)

package worker

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// createControlFile opens the private control path with the creator-only
// policy (Rust control.rs create_file unix arm): create_new with mode
// exactly 0600, then the secure_creator_only proof (mode verification,
// no inherited access ACL, ownership commitment). A restrictive umask
// can never make the control file unopenable by the worker. Every
// failure maps to the worker's Conflict class exactly like Rust
// namespace_error over create_file.
func createControlFile(path string, profile security.Profile) (*os.File, error) {
	// O_NONBLOCK is promptness for the open; calleropen clears it before
	// handing the handle back, so the control page stays out of the
	// runtime network poller (wave-19.25 design section 5).
	f, err := calleropen.Open(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|calleropen.NonBlocking, 0o600)
	if err != nil {
		return nil, workerSecurityFailure(err)
	}
	if err := security.SecureCreatorOnly(f, profile); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, workerSecurityFailure(err)
	}
	return f, nil
}
