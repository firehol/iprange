//go:build linux

package security

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// accessACL is the inherited-access ACL xattr name (Rust
// security/linux.rs ACCESS_ACL).
const accessACL = "system.posix_acl_access"

// removeInheritedACL removes an inherited access ACL from one open
// artifact; a missing or unsupported xattr is the clean state (Rust
// security/linux.rs remove_inherited: ENODATA and EOPNOTSUPP are
// tolerated).
func removeInheritedACL(f *os.File) error {
	err := unix.Fremovexattr(int(f.Fd()), accessACL)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.EOPNOTSUPP) {
		return nil
	}
	return &format.Error{Code: format.CodeIO, Detail: "remove inherited access ACL: " + err.Error()}
}

// requireTrivialACL proves the artifact carries no access ACL: reading
// the xattr must report ENODATA (absent) or EOPNOTSUPP (unsupported
// filesystem), and a present xattr fails the creator-only proof (Rust
// security/linux.rs require_trivial).
func requireTrivialACL(f *os.File) error {
	_, err := unix.Fgetxattr(int(f.Fd()), accessACL, nil)
	if err == nil {
		return accessPolicy()
	}
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.EOPNOTSUPP) {
		return nil
	}
	return &format.Error{Code: format.CodeIO, Detail: "verify absent access ACL: " + err.Error()}
}
