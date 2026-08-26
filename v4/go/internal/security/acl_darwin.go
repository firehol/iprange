//go:build darwin

package security

import (
	"os"
)

// aclSupported reports the creator-only machine availability (false on
// darwin; the pure-Go ACL machine exists on linux, freebsd, and
// windows).
const aclSupported = false

// removeInheritedACL on darwin is an honest typed refusal: the Rust
// authority removes inherited extended ACLs through libc filesec and
// acl_get_fd_np, which pure Go cannot call (Decision 2A forbids cgo and
// x/sys exposes no binding). Refusing keeps the creator-only proof
// honest instead of silently weakening it; the live create/initialize
// surface therefore reports CodeOSUnsupported on darwin until a
// pure-Go ACL mechanism exists (tracked by the SOW-0026 platform
// completion work).
func removeInheritedACL(*os.File) error {
	return unsupported("creator-only access policy requires libc filesec APIs unavailable to pure Go on darwin")
}

// requireTrivialACL mirrors removeInheritedACL on darwin.
func requireTrivialACL(*os.File) error {
	return unsupported("creator-only access policy requires libc ACL APIs unavailable to pure Go on darwin")
}
