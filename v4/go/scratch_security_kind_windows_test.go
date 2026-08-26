//go:build windows

package iprangedb

// scratchPublicSecurityKind is the platform creator-only security
// kind of the scratch ownership header (Rust scratch format
// creation_security_kind): 1 on POSIX, 2 on Windows.
func scratchPublicSecurityKind() uint16 { return 2 }
