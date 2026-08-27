//go:build windows

package worker

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/security"
)

// createControlFile opens the private control path with the creator-only
// policy (Rust control.rs create_file windows arm): the protected
// single-user DACL is established at creation by security::create_private
// (CREATE_NEW, no inheritance), so no post-create strip exists. Every
// failure maps to the worker's Conflict class exactly like Rust
// namespace_error over create_file.
func createControlFile(path string, profile security.Profile) (*os.File, error) {
	f, err := security.CreatePrivate(path, profile, false)
	if err != nil {
		return nil, workerSecurityFailure(err)
	}
	return f, nil
}
