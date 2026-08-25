// Private attempt names (Rust publication/namespace.rs OUTPUT_PREFIX,
// RESERVATION_PREFIX, PRIVATE_SUFFIX + artifact_name.rs hex codec).
// The attempt identity encodes as 32 lowercase hex characters; the
// zero attempt id is never a valid private name.

package publication

import (
	"encoding/hex"

	"github.com/firehol/iprange/v4/go/internal/live"
)

const (
	// outputPrefix is the private publication-output prefix (Rust
	// namespace::OUTPUT_PREFIX).
	outputPrefix = ".iprange-publish-"
	// reservationPrefix is the private reservation prefix (Rust
	// namespace::RESERVATION_PREFIX).
	reservationPrefix = ".iprange-reservation-"
	// privateSuffix is the in-progress private suffix (Rust
	// namespace::PRIVATE_SUFFIX).
	privateSuffix = ".tmp"
	// attemptHexLen is the lowercase-hex identity length (Rust
	// artifact_name::ATTEMPT_HEX_SIZE).
	attemptHexLen = 32
)

// privateName builds one private artifact name: prefix, lowercase hex
// attempt id, suffix (Rust private_name). The zero attempt id is the
// InvalidName class.
func privateName(prefix string, attempt [16]byte) (string, error) {
	if attempt == [16]byte{} {
		return "", &live.NamespaceError{Kind: live.NamespaceInvalidName}
	}
	var name [maxPrivateNameLen]byte
	off := copy(name[:], prefix)
	hex.Encode(name[off:off+attemptHexLen], attempt[:])
	copy(name[off+attemptHexLen:], privateSuffix)
	return string(name[:off+attemptHexLen+len(privateSuffix)]), nil
}

// maxPrivateNameLen is the exact private-name length for both prefixes
// (Rust prefix + ATTEMPT_HEX_SIZE + suffix).
const maxPrivateNameLen = len(".iprange-reservation-") + attemptHexLen + len(".tmp")

// privateAttempt decodes one private artifact name back to its attempt
// identity (Rust private_attempt): the prefix and suffix must match,
// the hex must be exactly 32 lowercase characters, and the zero
// identity is rejected. ok is false otherwise.
func privateAttempt(prefix string, name []byte) ([16]byte, bool) {
	if !hasPrefixBytes(name, prefix) || !hasSuffixBytes(name, privateSuffix) {
		return [16]byte{}, false
	}
	encoded := name[len(prefix) : len(name)-len(privateSuffix)]
	if len(encoded) != attemptHexLen {
		return [16]byte{}, false
	}
	for _, b := range encoded {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return [16]byte{}, false
		}
	}
	var attempt [16]byte
	if _, err := hex.Decode(attempt[:], encoded); err != nil {
		return [16]byte{}, false
	}
	if attempt == [16]byte{} {
		return [16]byte{}, false
	}
	return attempt, true
}

func hasPrefixBytes(b []byte, prefix string) bool {
	if len(b) < len(prefix) {
		return false
	}
	return string(b[:len(prefix)]) == prefix
}

func hasSuffixBytes(b []byte, suffix string) bool {
	if len(b) < len(suffix) {
		return false
	}
	return string(b[len(b)-len(suffix):]) == suffix
}
