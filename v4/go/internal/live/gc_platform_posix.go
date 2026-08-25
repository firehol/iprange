//go:build !windows

package live

// gcBasenameEncodingValue is the unix PosixBytes tag (Rust
// namespace::BASENAME_ENCODING_KIND on unix).
func gcBasenameEncodingValue() BasenameEncoding { return basenameEncodingPosixBytes }

// gcCreationSecurityKind is the unix creator-only kind (Rust
// namespace::CREATION_SECURITY_KIND on unix).
func gcCreationSecurityKind() uint16 { return 1 }

// gcNameBytesPlatform keeps the ASCII name bytes raw on unix (Rust
// Name::bytes).
func gcNameBytesPlatform(name string) []byte { return []byte(name) }

// gcSourceEncodedMatches compares one stored encoded source against an
// ASCII name (raw byte equality on unix).
func gcSourceEncodedMatches(encoded []byte, name string) bool {
	return string(encoded) == name
}

// gcDecodeNameBytes projects one stored encoded source back to ASCII
// (raw bytes on unix; the binding validation already ran).
func gcDecodeNameBytes(encoded []byte) (string, bool) {
	return string(encoded), true
}
