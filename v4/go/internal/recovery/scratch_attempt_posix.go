//go:build !windows

package recovery

import (
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/random"
)

// newScratchAttempt draws one nonzero 128-bit attempt identity (Rust
// Scratch::new_attempt unix arm: one CSPRNG draw).
func newScratchAttempt(_ *live.Directory) ([16]byte, error) {
	return random.Nonzero128()
}
