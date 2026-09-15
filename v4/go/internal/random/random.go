// Package random draws nonzero identities from the operating-system
// CSPRNG (Rust random.rs nonzero_128: one fill, an all-zero draw is a
// hard error). This is the single authority for identity draws in the
// Go peer; writers, the live lifecycle, and the worker control page all
// compose it.

package random

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// Entropy fills b from the operating-system CSPRNG through the owner
// that keeps the draw free of runtime timers and descriptors (see
// entropy_linux.go / entropy_other.go). It is the single entropy entry
// point for handle and identity draws; error classes stay exactly as
// before the ownership change.
func Entropy(b []byte) error { return entropy(b) }

// Nonzero128 returns one 128-bit nonzero identity (Rust
// random::nonzero_128). A CSPRNG failure reports CodeIO; an all-zero
// draw reports CodeFormatInvalid (Rust Error::Corrupt) with the exact
// Rust detail.
func Nonzero128() ([16]byte, error) {
	var value [16]byte
	if err := entropy(value[:]); err != nil {
		return value, &format.Error{Code: format.CodeIO, Detail: "operating-system randomness failed: " + err.Error()}
	}
	if value == [16]byte{} {
		return value, &format.Error{Code: format.CodeFormatInvalid, Detail: "operating-system randomness returned an all-zero identity"}
	}
	return value, nil
}
