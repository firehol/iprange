//go:build windows

// Exact attempt-bound Windows housekeeping names (Rust
// publication/gc_name.rs). The envelope is the authenticated GC
// authority for one retired artifact; the inert twin receives the
// payload after the exact move. Both names fix the attempt identity
// and ordinal in lowercase hex, like every other attempt-derived
// basename. Go directory scans project names to ASCII bytes on every
// platform, so candidate detection runs over the raw ASCII form.

package live

import (
	"encoding/hex"
)

const (
	gcEnvelopePrefix = ".iprange-gcauth-"
	gcInertPrefix    = ".iprange-gc-"
	gcSuffix         = ".tmp"
)

// gcCandidate classifies one scanned housekeeping name (Rust
// gc_name::Candidate): matched selects any name with a housekeeping
// prefix; the decoded attempt/ordinal pair is present only when the
// name is fully canonical.
type gcCandidate struct {
	matched  bool
	envelope bool
	attempt  [16]byte
	ordinal  uint32
	decoded  bool
}

// gcEnvelopeName builds the authenticated envelope name of one
// attempt (Rust gc_name::envelope). The zero attempt id is invalid.
func gcEnvelopeName(attempt [16]byte, ordinal uint32) (string, error) {
	return gcName(gcEnvelopePrefix, attempt, ordinal)
}

// gcInertName builds the inert payload name of one attempt (Rust
// gc_name::inert).
func gcInertName(attempt [16]byte, ordinal uint32) (string, error) {
	return gcName(gcInertPrefix, attempt, ordinal)
}

// gcName assembles one fixed-width housekeeping name: prefix, 32 hex
// attempt characters, dash, 8 hex ordinal characters, suffix. The
// longest name is the 62-byte scratch name (17-byte prefix; Rust
// artifact_name::SCRATCH_NAME_SIZE); the envelope and inert names are
// shorter, so the one buffer covers every prefix.
const gcNameFixedBytes = 32 + 1 + 8 + len(gcSuffix)

func gcName(prefix string, attempt [16]byte, ordinal uint32) (string, error) {
	if attempt == [16]byte{} {
		return "", nsInvalidNameError()
	}
	var buf [len(".iprange-scratch-") + gcNameFixedBytes]byte
	off := copy(buf[:], prefix)
	hex.Encode(buf[off:off+32], attempt[:])
	buf[off+32] = '-'
	hex.Encode(buf[off+33:off+41], []byte{byte(ordinal >> 24), byte(ordinal >> 16), byte(ordinal >> 8), byte(ordinal)})
	copy(buf[off+41:], gcSuffix)
	return string(buf[:off+gcNameFixedBytes]), nil
}

// gcDecodeEnvelope decodes one envelope name to its attempt/ordinal
// pair (Rust gc_name::decode_envelope): canonical lowercase hex only,
// nonzero attempt.
func gcDecodeEnvelope(name []byte) ([16]byte, uint32, bool) {
	return gcDecode(name, gcEnvelopePrefix)
}

// gcDecodeInert decodes one inert name to its attempt/ordinal pair
// (Rust gc_name::decode_inert).
func gcDecodeInert(name []byte) ([16]byte, uint32, bool) {
	return gcDecode(name, gcInertPrefix)
}

// gcCandidateOf classifies one scanned name (Rust gc_name::candidate):
// envelope or inert prefix selects the class; the decode must be fully
// canonical for the attempt/ordinal pair to be present.
func gcCandidateOf(name []byte) gcCandidate {
	if hasPrefixBytes(name, gcEnvelopePrefix) {
		attempt, ordinal, ok := gcDecode(name, gcEnvelopePrefix)
		return gcCandidate{matched: true, envelope: true, attempt: attempt, ordinal: ordinal, decoded: ok}
	}
	if hasPrefixBytes(name, gcInertPrefix) {
		attempt, ordinal, ok := gcDecode(name, gcInertPrefix)
		return gcCandidate{matched: true, envelope: false, attempt: attempt, ordinal: ordinal, decoded: ok}
	}
	return gcCandidate{}
}

// gcDecode decodes one housekeeping name: the prefix and suffix must
// match exactly, the middle must be 32 lowercase hex attempt
// characters, a dash, and 8 lowercase hex ordinal characters, and the
// attempt must be nonzero (Rust gc_name::decode; uppercase, signs,
// prefixes, wrong widths, and non-hex bytes are rejected).
func gcDecode(name []byte, prefix string) ([16]byte, uint32, bool) {
	if !hasPrefixBytes(name, prefix) || !hasSuffixBytes(name, gcSuffix) {
		return [16]byte{}, 0, false
	}
	encoded := name[len(prefix) : len(name)-len(gcSuffix)]
	if len(encoded) != 32+1+8 || encoded[32] != '-' {
		return [16]byte{}, 0, false
	}
	attempt, ok := gcHex16(encoded[:32])
	if !ok || attempt == [16]byte{} {
		return [16]byte{}, 0, false
	}
	ordinal, ok := gcHex4(encoded[33:])
	if !ok {
		return [16]byte{}, 0, false
	}
	return attempt, ordinal, true
}

// gcHex16 decodes 32 lowercase hex bytes to a 16-byte array.
func gcHex16(encoded []byte) ([16]byte, bool) {
	var out [16]byte
	for i := 0; i < 16; i++ {
		hi, ok1 := gcNibble(encoded[2*i])
		lo, ok2 := gcNibble(encoded[2*i+1])
		if !ok1 || !ok2 {
			return [16]byte{}, false
		}
		out[i] = hi<<4 | lo
	}
	return out, true
}

// gcHex4 decodes 8 lowercase hex bytes to a uint32.
func gcHex4(encoded []byte) (uint32, bool) {
	var out uint32
	for i := 0; i < 8; i++ {
		nibble, ok := gcNibble(encoded[i])
		if !ok {
			return 0, false
		}
		out = out<<4 | uint32(nibble)
	}
	return out, true
}

// gcNibble decodes one lowercase hex digit.
func gcNibble(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	}
	return 0, false
}

// hasPrefixBytes reports one exact byte prefix (Rust starts_with).
func hasPrefixBytes(name []byte, prefix string) bool {
	return len(name) >= len(prefix) && string(name[:len(prefix)]) == prefix
}

// hasSuffixBytes reports one exact byte suffix (Rust ends_with).
func hasSuffixBytes(name []byte, suffix string) bool {
	return len(name) >= len(suffix) && string(name[len(name)-len(suffix):]) == suffix
}
