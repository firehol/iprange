//go:build !windows

package worker

// Platform record kinds of the unix namespace machine (Rust
// publication/namespace/unix.rs): PosixBytes basename encoding (1)
// and creator-only kind 1. The worker cleanup codecs validate the
// checkpoint and cleanup security facts against the platform kind,
// exactly like the Rust wire_cleanup valid_security arm.
const (
	basenameEncodingKind uint16 = 1
	creationSecurityKind uint16 = 1
)
