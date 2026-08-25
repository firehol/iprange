//go:build !windows

package publication

// basenameEncodingPosixBytes is the unix raw-bytes encoding (Rust
// BasenameEncoding::PosixBytes = 1).
const basenameEncodingPosixBytes basenameEncoding = 1

// validateEncoding proves one PosixBytes component is valid (Rust
// validate_posix): not "." or "..", no NUL, no separator.
func validateEncoding(encoding basenameEncoding, bytes []byte) error {
	if encoding != basenameEncodingPosixBytes {
		return &nameBindingError{}
	}
	if len(bytes) == 2 && bytes[0] == '.' && bytes[1] == '.' {
		return &nameBindingError{}
	}
	if len(bytes) == 1 && bytes[0] == '.' {
		return &nameBindingError{}
	}
	for _, b := range bytes {
		if b == 0 || b == '/' {
			return &nameBindingError{}
		}
	}
	return nil
}
