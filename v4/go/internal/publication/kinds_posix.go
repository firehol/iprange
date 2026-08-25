//go:build !windows

package publication

// Platform record kinds of the unix namespace machine (Rust
// namespace/unix.rs): PosixBytes basename encoding, creator-only kind 1.
const (
	basenameEncodingKind uint16 = 1
	creationSecurityKind uint16 = 1
)
