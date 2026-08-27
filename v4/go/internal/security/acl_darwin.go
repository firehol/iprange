//go:build darwin

// Darwin creator-only ACL machine (Rust publication/security/apple.rs
// parity): inherited extended ACLs are removed with the raw
// fchmod_extended syscall and the libc _FILESEC_REMOVE_ACL sentinel,
// and the trivial-ACL proof probes with fstat_extended, the same
// syscalls libc filesec_init / fchmodx_np / acl_get_fd_np wrap
// (chmodx_np.c, statx_np.c, acl_file.c). Decision 2A forbids cgo; the
// numbers and sentinels are the public XNU surface documented in
// acl_darwin_algo.go. Filesystems without the required operations
// report the durability-unsupported class exactly like the Rust
// machine.

package security

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// aclSupported reports the creator-only machine availability (darwin: true).
const aclSupported = true

// darwinACLBufferACE is the initial kauth_filesec capacity in ACL
// entries (libc ACL_MIN_SIZE_HEURISTIC, KAUTH_FILESEC_SIZE(16)); the
// probe grows on demand like libc statx1.
const darwinACLBufferACE = 16

// removeInheritedACL removes one inherited extended access ACL with
// the raw fchmod_extended syscall and the libc remove-ACL sentinel
// (Rust apple.rs remove_inherited over filesec + fchmodx_np): XNU
// vfs_syscalls.c chmod_extended_init treats the xsecurity value 1 as
// _FILESEC_REMOVE_ACL and sets the vnode ACL to NULL; the owner,
// group, and mode stay untouched.
func removeInheritedACL(f *os.File) error {
	_, _, errno := syscall.Syscall6(darwinSysFchmodExtended,
		uintptr(f.Fd()), darwinUIDNone, darwinGIDNone, darwinModeUnchanged, darwinRemoveACL, 0)
	return darwinACLAppliedAt("remove inherited access ACL", errnoToError(errno))
}

// requireTrivialACL proves the artifact carries no extended access
// ACL with the raw fstat_extended syscall (Rust apple.rs
// require_trivial over acl_get_fd_np). The XNU fill contract
// (fstatat_internal): a vnode without a kauth_filesec returns the
// size 0; a vnode with one returns KAUTH_FILESEC_COPYSIZE and copies
// the filesec only when the caller's buffer is large enough, always
// with errno 0. A zero entry count or the KAUTH_FILESEC_NOACL
// sentinel is the clean state; one or more ACE entries fails the
// creator-only proof.
func requireTrivialACL(f *os.File) error {
	var stat unix.Stat_t
	size := darwinACLBufferACE
	buf := make([]byte, kauthFilesecSize(size))
	for {
		var got uintptr
		args := [4]uintptr{
			uintptr(f.Fd()),
			uintptr(unsafe.Pointer(&stat)),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&got)),
		}
		_, _, errno := syscall.Syscall6(darwinSysFstatExtended, args[0], args[1], args[2], args[3], 0, 0)
		if errno != 0 {
			return darwinTrivialProbe(errnoToError(errno), 0)
		}
		if got == 0 {
			// No kauth_filesec on the vnode at all: the clean state
			// (fstatat_internal writes the size 0 for KAUTH_FILESEC_NONE).
			return darwinTrivialProbe(nil, 0)
		}
		if got > uintptr(len(buf)) {
			// The kernel needs more room: it wrote the required size
			// without copying (fstatat_internal), so grow to the
			// requested size (libc statx1 growth arm) and re-probe once.
			if got > 1<<20 {
				return darwinTrivialProbe(syscall.EIO, 0)
			}
			buf = make([]byte, got)
			continue
		}
		// The filesec was copied: classify on the ACE entry count
		// (kauth.h struct kauth_filesec: magic@0, owner@4, group@20,
		// acl_entrycount@36, acl_flags@40, acl_ace@44).
		entries := binary.LittleEndian.Uint32(buf[aclEntryCountOffset : aclEntryCountOffset+4])
		return darwinTrivialProbe(nil, entries)
	}
}

// errnoToError maps one raw syscall errno to an error, preserving the
// clean zero state as nil (syscall.Errno(0) is never returned by the
// darwin arms; the helper keeps the classification inputs uniform).
func errnoToError(errno syscall.Errno) error {
	if errno == 0 {
		return nil
	}
	return errno
}
