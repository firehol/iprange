//go:build darwin

package security

import (
	"os"
)

// removeInheritedACL on darwin is an honest typed refusal: the Rust
// authority removes inherited extended ACLs through libc filesec and
// acl_get_fd_np, which pure Go cannot call (Decision 2A forbids cgo and
// x/sys exposes no binding). Refusing keeps the creator-only proof
// honest instead of silently weakening it; the live create/initialize
// surface therefore reports CodeOSUnsupported on darwin until a
// pure-Go ACL mechanism exists (tracked with the 4-12 platform proof).
func removeInheritedACL(*os.File) error {
	return unsupported("creator-only access policy requires libc filesec APIs unavailable to pure Go on darwin")
}

// requireTrivialACL mirrors removeInheritedACL on darwin.
func requireTrivialACL(*os.File) error {
	return unsupported("creator-only access policy requires libc ACL APIs unavailable to pure Go on darwin")
}
