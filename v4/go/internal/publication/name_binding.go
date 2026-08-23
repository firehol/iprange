//go:build !windows

// Exact platform basename encoding and commitment (Rust
// name_binding.rs). The commitment is SHA-256 over the fixed
// IPR4NAME domain, the little-endian encoding tag, the little-endian
// byte length, and the raw bytes; unix names use PosixBytes (tag 1)
// and must be one exact component without NUL or separator bytes.

package publication

import (
	"crypto/sha256"
	"encoding/binary"
)

// basenameEncoding is the platform basename encoding tag (Rust
// BasenameEncoding).
type basenameEncoding uint16

const (
	// basenameEncodingPosixBytes is the unix raw-bytes encoding (Rust
	// BasenameEncoding::PosixBytes = 1).
	basenameEncodingPosixBytes basenameEncoding = 1
)

// nameDomain is the exact basename commitment domain (Rust
// NAME_DOMAIN).
const nameDomain = "IPR4NAME"

// basenameCommitment computes the exact basename commitment (Rust
// basename_commitment): empty components and invalid characters are
// the InvalidName class, every other failure is impossible in Go.
func basenameCommitment(encoding basenameEncoding, bytes []byte) ([32]byte, error) {
	if len(bytes) == 0 {
		return [32]byte{}, &nameBindingError{empty: true}
	}
	switch encoding {
	case basenameEncodingPosixBytes:
		if !validPosixComponent(bytes) {
			return [32]byte{}, &nameBindingError{invalidPosix: true}
		}
	}
	var encoded [2]byte
	var length [4]byte
	binary.LittleEndian.PutUint16(encoded[:], uint16(encoding))
	binary.LittleEndian.PutUint32(length[:], uint32(len(bytes)))
	h := sha256.New()
	h.Write([]byte(nameDomain))
	h.Write(encoded[:])
	h.Write(length[:])
	h.Write(bytes)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// nameBindingError is the internal commitment failure (Rust
// BasenameBindingError); the caller folds every arm to InvalidName.
type nameBindingError struct {
	empty        bool
	invalidPosix bool
}

func (*nameBindingError) Error() string { return "basename binding is invalid" }

// validPosixComponent proves one PosixBytes component is valid (Rust
// validate_posix): not "." or "..", no NUL, no separator.
func validPosixComponent(bytes []byte) bool {
	if len(bytes) == 2 && bytes[0] == '.' && bytes[1] == '.' {
		return false
	}
	if len(bytes) == 1 && bytes[0] == '.' {
		return false
	}
	for _, b := range bytes {
		if b == 0 || b == '/' {
			return false
		}
	}
	return true
}
