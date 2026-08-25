//go:build !windows

package publication

import "github.com/firehol/iprange/v4/go/internal/live"

// basenameEncodingPosixBytes is the unix raw-bytes encoding (Rust
// BasenameEncoding::PosixBytes = 1).
const basenameEncodingPosixBytes basenameEncoding = live.BasenameEncoding(1)

// validateEncoding proves one PosixBytes component binds (Rust
// validate_posix); the authority lives in live.
func validateEncoding(encoding basenameEncoding, bytes []byte) error {
	return live.ValidateEncodingBinding(encoding, bytes)
}
