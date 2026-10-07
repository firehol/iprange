//go:build (linux || darwin || freebsd) && (amd64 || arm64)

package worker

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"

	"github.com/firehol/iprange/v4/go/internal/calleropen"
)

// createControlFile opens the private control path following the process
// switch (Rust control.rs create_file unix arm): the control file is a
// no-source artifact, so with IPRANGE_CREATOR_ONLY=1 it is created with
// mode exactly 0600 and proved through secure_creator_only, and with the
// switch off it keeps the process default. Every failure maps to the
// worker's Conflict class exactly like Rust namespace_error over
// create_file.
func createControlFile(path string, profile security.Profile) (*os.File, error) {
	creatorOnly := security.CreatorOnlyRequested()
	mode := os.FileMode(0o666)
	if creatorOnly {
		mode = security.CreatorMode
	}
	// O_NONBLOCK is promptness for the open; calleropen clears it before
	// handing the handle back, so the control page stays out of the
	// runtime network poller (wave-19.25 design section 5).
	f, err := calleropen.Open(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|calleropen.NonBlocking, mode)
	if err != nil {
		return nil, workerSecurityFailure(err)
	}
	if creatorOnly {
		if err := security.SecureCreatorOnly(f, profile); err != nil {
			f.Close()
			return nil, workerSecurityFailure(err)
		}
	} else {
		// The worker re-opens the control file O_RDWR, so the owner
		// read/write bits must survive ANY umask (Rust twin floors
		// the unprotected create the same way): a umask stripping
		// owner bits at create would fail the worker's re-open with
		// EACCES and surface as a misleading protocol Conflict.
		// Group and other bits keep the process default the umask
		// left.
		info, statErr := f.Stat()
		if statErr != nil {
			f.Close()
			return nil, workerSecurityFailure(statErr)
		}
		if err := f.Chmod(info.Mode().Perm() | 0o600); err != nil {
			f.Close()
			return nil, workerSecurityFailure(err)
		}
	}
	return f, nil
}
