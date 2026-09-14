//go:build !windows && !linux && !darwin && !freebsd

// Targets with no qualified live durability surface fail the durability
// proof for every filesystem (Rust require_local_filesystem fallback);
// the live machine refuses coordination on these targets earlier anyway,
// and the refusal keeps the proof total.

package fslocal

import "os"

// RequireLocal always reports ErrNotLocal on this target.
func RequireLocal(*os.File) error { return ErrNotLocal }
