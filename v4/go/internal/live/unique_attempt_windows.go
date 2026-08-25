//go:build windows

package live

import (
	"path/filepath"

	"github.com/firehol/iprange/v4/go/internal/random"
)

// uniqueAttemptID draws one nonzero 128-bit cleanup attempt identity
// whose envelope and inert names are both absent in the source
// directory (Rust live_cleanup::unique_attempt_id windows arm: the
// collision loop over the exact GC names).
func uniqueAttemptID(path string, ordinal uint32) ([16]byte, error) {
	clean := filepath.Clean(path)
	dir, _, err := bindPath(clean)
	if err != nil {
		return [16]byte{}, gcNamespaceProblem(err)
	}
	defer dir.Close()
	for {
		attempt, err := random.Nonzero128()
		if err != nil {
			return [16]byte{}, err
		}
		envelopeName, err := gcEnvelopeName(attempt, ordinal)
		if err != nil {
			return [16]byte{}, gcNamespaceProblem(err)
		}
		inertName, err := gcInertName(attempt, ordinal)
		if err != nil {
			return [16]byte{}, gcNamespaceProblem(err)
		}
		envelopeErr := dir.RequireAbsent(envelopeName)
		inertErr := dir.RequireAbsent(inertName)
		if envelopeErr == nil && inertErr == nil {
			return attempt, nil
		}
		if isNamespaceExists(envelopeErr) || isNamespaceExists(inertErr) {
			continue
		}
		if envelopeErr != nil {
			return [16]byte{}, gcNamespaceProblem(envelopeErr)
		}
		return [16]byte{}, gcNamespaceProblem(inertErr)
	}
}

// freshCleanupAttempt draws one attempt identity after proving the
// exact source unclaimed (Rust live_cleanup::fresh_cleanup_attempt
// windows arm over publication::gc::fresh_attempt).
func freshCleanupAttempt(path string, identity FileIdentity, ordinal uint32, kind ArtifactKind, directoryRole DirectoryRole) ([16]byte, error) {
	clean := filepath.Clean(path)
	dir, name, err := bindPath(clean)
	if err != nil {
		return [16]byte{}, gcNamespaceProblem(err)
	}
	defer dir.Close()
	attempt, err := gcFreshAttempt(dir, name, identity, ordinal, kind, directoryRole)
	if err != nil {
		return [16]byte{}, err
	}
	return attempt, nil
}
