// Connection-local opaque handle generation (Rust rpc/mod.rs parity).
//
// A handle is 32 lowercase hex characters from 16 secure random
// bytes. Entropy failure is a server-side failure, never a silent
// zero handle: temporary names and connection handles must be
// unpredictable.

package rpc

import (
	"crypto/rand"
	"encoding/hex"
)

// NewHandle returns a fresh 32-hex-char handle or a product error with
// the documented adapter code `io` (an OS-level resource failure).
func NewHandle() (string, *HandlerError) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", NewHandlerError("io", "not_started",
			"secure handle generation failed: "+err.Error())
	}
	return hex.EncodeToString(bytes[:]), nil
}
