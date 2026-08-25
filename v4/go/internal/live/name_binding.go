// Exact platform basename binding and commitment (Rust
// name_binding.rs). The commitment is SHA-256 over the fixed IPR4NAME
// domain, the little-endian encoding tag, the little-endian byte
// length, and the raw bytes; unix names use PosixBytes (tag 1) and
// must be one exact component without NUL or separator bytes, Windows
// names use WindowsUtf16Le (tag 2). The publication records and the
// GC envelope codec share this single authority.

package live

import (
	"crypto/sha256"
	"encoding/binary"
)

// BasenameEncoding is the platform basename encoding tag (Rust
// BasenameEncoding); the platform values live in
// name_binding_posix.go / name_binding_windows.go.
type BasenameEncoding uint16

// nameDomain is the exact basename commitment domain (Rust
// NAME_DOMAIN).
const nameDomain = "IPR4NAME"

// ValidateEncodingBinding proves one encoded name binds under its
// encoding (Rust basename_binding::bind validation); the platform arm
// selects the exact rule.
func ValidateEncodingBinding(encoding BasenameEncoding, bytes []byte) error {
	return validateEncodingBinding(encoding, bytes)
}

// BasenameCommitment computes the exact basename commitment (Rust
// basename_commitment): empty components and invalid characters are
// the InvalidName class, every other failure is impossible in Go.
func BasenameCommitment(encoding BasenameEncoding, bytes []byte) ([32]byte, error) {
	if len(bytes) == 0 {
		return [32]byte{}, nsInvalidNameError()
	}
	if err := validateEncodingBinding(encoding, bytes); err != nil {
		return [32]byte{}, err
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
