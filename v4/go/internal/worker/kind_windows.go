//go:build windows

package worker

// Platform record kinds of the Windows namespace machine (Rust
// publication/namespace/windows.rs): WindowsUtf16Le basename encoding
// (2) and creator-only kind 2. The worker cleanup codecs validate the
// checkpoint and cleanup security facts against the platform kind,
// exactly like the Rust wire_cleanup valid_security arm.
const (
	basenameEncodingKind uint16 = 2
	creationSecurityKind uint16 = 2
)
