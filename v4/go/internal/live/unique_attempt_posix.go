//go:build !windows

package live

import "github.com/firehol/iprange/v4/go/internal/random"

// uniqueAttemptID draws one nonzero 128-bit cleanup attempt identity
// (Rust live_cleanup::unique_attempt_id POSIX arm: a plain nonzero
// draw).
func uniqueAttemptID(_ string, _ uint32) ([16]byte, error) {
	return random.Nonzero128()
}

// freshCleanupAttempt draws one attempt identity for a fresh cleanup
// (Rust live_cleanup::fresh_cleanup_attempt POSIX arm: a plain
// nonzero draw).
func freshCleanupAttempt(_ string, _ FileIdentity, ordinal uint32, _ ArtifactKind, _ DirectoryRole) ([16]byte, error) {
	return random.Nonzero128()
}
