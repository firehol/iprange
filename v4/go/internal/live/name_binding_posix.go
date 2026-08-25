//go:build !windows

package live

// basenameEncodingPosixBytes is the unix raw-bytes encoding (Rust
// BasenameEncoding::PosixBytes = 1).
const basenameEncodingPosixBytes BasenameEncoding = 1

// validateEncodingBinding proves one PosixBytes component is valid
// (Rust validate_posix): not "." or "..", no NUL, no separator.
func validateEncodingBinding(encoding BasenameEncoding, bytes []byte) error {
	if encoding != basenameEncodingPosixBytes {
		return nsInvalidNameError()
	}
	if len(bytes) == 2 && bytes[0] == '.' && bytes[1] == '.' {
		return nsInvalidNameError()
	}
	if len(bytes) == 1 && bytes[0] == '.' {
		return nsInvalidNameError()
	}
	for _, b := range bytes {
		if b == 0 || b == '/' {
			return nsInvalidNameError()
		}
	}
	return nil
}
