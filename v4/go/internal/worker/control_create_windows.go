//go:build windows && (amd64 || arm64)

package worker

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// createControlFile opens the private control path following the process
// switch (Rust control.rs create_file windows arm): with
// IPRANGE_CREATOR_ONLY=1 the protected single-user DACL is established
// at creation by security::create_private (CREATE_NEW, no inheritance);
// with the switch off the process default descriptor is used. Every
// failure maps to the worker's Conflict class exactly like Rust
// namespace_error over create_file.
func createControlFile(path string, profile security.Profile) (*os.File, error) {
	if security.CreatorOnlyRequested() {
		f, err := security.CreatePrivate(path, profile, false)
		if err != nil {
			return nil, workerSecurityFailure(err)
		}
		return f, nil
	}
	f, err := security.CreateUnprotected(path, false)
	if err != nil {
		return nil, workerSecurityFailure(err)
	}
	return f, nil
}
