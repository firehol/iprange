//go:build windows

package recovery

import (
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/random"
)

// newScratchAttempt draws one nonzero attempt identity whose scratch
// source, GC envelope, and GC inert names are all absent for every
// owned ordinal (Rust Scratch::new_attempt windows arm: the collision
// loop over both scratch slots).
func newScratchAttempt(directory *live.Directory) ([16]byte, error) {
	for {
		attempt, err := random.Nonzero128()
		if err != nil {
			return [16]byte{}, err
		}
		collision := false
		for ordinal := 0; ordinal < scratchMaxOwned; ordinal++ {
			source, err := scratchNameOf(attempt, uint32(ordinal))
			if err != nil {
				return [16]byte{}, err
			}
			envelope, err := live.GCEnvelopeName(attempt, uint32(ordinal))
			if err != nil {
				return [16]byte{}, err
			}
			inert, err := live.GCInertName(attempt, uint32(ordinal))
			if err != nil {
				return [16]byte{}, err
			}
			for _, name := range []string{source, envelope, inert} {
				if err := directory.RequireAbsent(name); err != nil {
					if isNamespaceExistsErr(err) {
						collision = true
						break
					}
					return [16]byte{}, scratchNamespaceError(err)
				}
			}
			if collision {
				break
			}
		}
		if !collision {
			return attempt, nil
		}
	}
}

// isNamespaceExistsErr reports the exists class of the retained
// directory machine (Rust NamespaceError::Exists match arm).
func isNamespaceExistsErr(err error) bool {
	nerr, ok := live.AsNamespaceError(err)
	return ok && nerr.Kind == live.NamespaceExists
}
