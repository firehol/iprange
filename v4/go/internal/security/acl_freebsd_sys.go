//go:build freebsd

// FreeBSD creator-only ACL machine: the exact libc acl_get_fd /
// acl_strip_np / acl_is_trivial_np / acl_set_fd behavior of the Rust
// publication/security/freebsd.rs machine, implemented with raw
// __acl_get_fd/__acl_set_fd syscalls (Decision 2A forbids cgo; the
// algorithms live in acl_freebsd_algo.go and run identically in the
// unit tests on every host). The brand is the libc fpathconf(_PC_ACL_NFS4)
// probe: value 1 selects the NFSv4 brand (ZFS acltype=nfsv4), anything
// else the POSIX.1e brand. A get that reports EOPNOTSUPP means the
// filesystem lacks required ACL operations and folds to the Rust
// NamespaceError::Unsupported class (DurabilityUnsupported) exactly
// like the Rust machine.

package security

import (
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// aclSupported reports the creator-only machine availability (freebsd: true).
const aclSupported = true

// FreeBSD constant values of the ACL syscall surface (sys/acl.h and
// sys/unistd.h; the x/sys tables carry the syscall numbers and errnos).
const (
	fbsdACLTypeAccess = uintptr(0x00000002) // ACL_TYPE_ACCESS (POSIX.1e)
	fbsdACLTypeNFS4   = uintptr(0x00000004) // ACL_TYPE_NFS4
	fbsdPCACLNFS4     = 64                  // _PC_ACL_NFS4 pathconf name
)

// freebsdGetACL reads one fd's access ACL into the caller-provided
// buffer (libc acl_get_fd + acl_get_fd_np). The caller's stack keeps
// the 4088-byte kernel struct: the buffer never escapes, so no owned
// page-sized heap object exists on the hot proof paths (the
// complete-page ownership pin measures it). The kernel struct is
// sized to the kernel maximum before the call because the kernel
// refuses undersized acl_maxcnt, exactly like libc acl_init.
func freebsdGetACL(f *os.File, acl *freebsdACL) (freebsdACLBrand, error) {
	acl.MaxCnt = fbsdMaxEntries
	brand := fbsdBrandPOSIX
	typ := fbsdACLTypeAccess
	if n, err := unix.Fpathconf(int(f.Fd()), fbsdPCACLNFS4); err == nil && n == 1 {
		brand = fbsdBrandNFS4
		typ = fbsdACLTypeNFS4
	}
	_, _, errno := unix.Syscall(unix.SYS___ACL_GET_FD, uintptr(f.Fd()), typ, uintptr(unsafe.Pointer(acl)))
	if errno != 0 {
		if errno == unix.EOPNOTSUPP {
			return brand, &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "read access ACL: " + errno.Error()}
		}
		return brand, &format.Error{Code: format.CodeIO, Detail: "read access ACL: " + errno.Error()}
	}
	return brand, nil
}

// freebsdSetACL writes one access ACL with its brand (libc acl_set_fd:
// the POSIX.1e arm presorts into the canonical kernel order first; the
// NFSv4 arm submits the ACL as built).
func freebsdSetACL(f *os.File, acl *freebsdACL, brand freebsdACLBrand) error {
	if brand == fbsdBrandPOSIX {
		fbsdPOSIXSort(acl)
	}
	typ := fbsdACLTypeAccess
	if brand == fbsdBrandNFS4 {
		typ = fbsdACLTypeNFS4
	}
	_, _, errno := unix.Syscall(unix.SYS___ACL_SET_FD, uintptr(f.Fd()), typ, uintptr(unsafe.Pointer(acl)))
	if errno != 0 {
		return &format.Error{Code: format.CodeIO, Detail: "apply stripped access ACL: " + errno.Error()}
	}
	return nil
}

// removeInheritedACL removes an inherited access ACL from one open
// artifact (Rust security/freebsd.rs remove_inherited): a trivial ACL
// is the clean state, otherwise the libc strip (acl_strip_np with the
// recalculate flag) rebuilds the trivial form of the brand and the
// result is applied.
func removeInheritedACL(f *os.File) error {
	var acl freebsdACL
	brand, err := freebsdGetACL(f, &acl)
	if err != nil {
		return err
	}
	if fbsdTrivial(&acl, brand) {
		return nil
	}
	stripped := fbsdStrip(&acl, brand)
	return freebsdSetACL(f, &stripped, brand)
}

// requireTrivialACL proves the artifact carries no inherited access
// ACL (Rust security/freebsd.rs require_trivial): a nontrivial ACL
// fails the creator-only proof with the access-policy class.
func requireTrivialACL(f *os.File) error {
	var acl freebsdACL
	brand, err := freebsdGetACL(f, &acl)
	if err != nil {
		return err
	}
	if !fbsdTrivial(&acl, brand) {
		return accessPolicy()
	}
	return nil
}
