//go:build !windows

package publication

// Platform record kinds of the unix namespace machine (Rust
// namespace/unix.rs): PosixBytes basename encoding (1), creator-only
// kind 1, retained-unix identity kind 1.
const (
	basenameEncodingKind uint16 = 1
	creationSecurityKind uint16 = 1
	identityKind         uint16 = 1
)
