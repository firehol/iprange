//go:build windows

package publication

// Platform record kinds of the Windows namespace machine (Rust
// namespace/windows.rs): WindowsUtf16Le basename encoding (2),
// creator-only kind 2, Windows local-identity kind 2.
const (
	basenameEncodingKind uint16 = 2
	creationSecurityKind uint16 = 2
	identityKind         uint16 = 2
)
