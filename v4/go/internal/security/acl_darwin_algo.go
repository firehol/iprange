// Darwin creator-only probe classification (Rust publication/
// security/apple.rs parity): the pure classification of one syscall
// outcome, shared by the darwin arm and unit-tested on every host.
// The darwin arm mirrors libc filesec with raw fchmod_extended /
// fstat_extended syscalls (Decision 2A forbids cgo; the syscall
// numbers and sentinels come from the public XNU syscalls.master and
// kauth.h, exactly like the FreeBSD arm uses the public ACL syscall
// surface).

package security

import (
	"errors"
	"syscall"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// Darwin kauth_filesec layout constants (bsd/sys/kauth.h
// KAUTH_FILESEC_SIZE): the fixed prefix before the first ACE
// (fsec_magic 4 + fsec_owner 16 + fsec_group 16 + acl_entrycount 4 +
// acl_flags 4) and the on-disk ACE size (ace_applicant 16 +
// ace_rights 4 + ace_flags 4). The probe only needs a buffer the
// kernel can fill, and grows with the kernel-returned size like libc
// statx1.
const (
	headerBeforeACEs    = 44
	aceSize             = 24
	aclEntryCountOffset = 36 // struct kauth_filesec acl_entrycount
	aclEntryCountNoACL  = 0xFFFFFFFF
)

// kauthFilesecSize returns KAUTH_FILESEC_SIZE(entries): the header
// with the acl_ace offset plus the exact ACE array (kauth.h).
func kauthFilesecSize(entries int) int {
	return headerBeforeACEs + entries*aceSize
}

// darwinSysFchmodExtended and darwinSysFstatExtended are the XNU
// syscall numbers of fchmod_extended / fstat_extended (bsd/kern/
// syscalls.master entries 283 and 281, stable since macOS 10.4 and
// used by libc chmodx_np.c / statx_np.c). The darwin arm keeps the
// numbers local because Go's darwin syscall table does not export the
// extended-security entries; the sentinel values are the libc
// _FILESEC_REMOVE_ACL (1) and kauth.h KAUTH_UID_NONE / KAUTH_GID_NONE
// (~0-100) / mode -1 (leave untouched), which the XNU handlers
// interpret exactly like the Rust libc calls.
const (
	darwinSysFchmodExtended = uintptr(283)
	darwinSysFstatExtended  = uintptr(281)
	darwinRemoveACL         = uintptr(1)           // libc _FILESEC_REMOVE_ACL
	darwinUIDNone           = ^uintptr(100)        // KAUTH_UID_NONE (2^64-101 on 64-bit; the darwin sentinel, portable to 32-bit words)
	darwinGIDNone           = ^uintptr(100)        // KAUTH_GID_NONE (darwin sentinel parity)
	darwinModeUnchanged     = uintptr(^uintptr(0)) // -1: leave mode untouched
)

// darwinACLAppliedAt classifies one fchmod_extended outcome (Rust
// apple.rs remove_inherited): any failure is the Io class with the
// operation label; success is the clean state.
func darwinACLAppliedAt(operation string, errno error) error {
	if errno == nil {
		return nil
	}
	return &format.Error{Code: format.CodeIO, Detail: operation + ": " + errno.Error()}
}

// darwinTrivialProbe classifies one fstat_extended probe outcome
// (Rust apple.rs require_trivial over acl_get_fd_np): a syscall
// failure maps ENOENT to clean (no ACL), EOPNOTSUPP to the
// durability-unsupported class (filesystem without the required ACL
// operations), and any other errno to the Io class; a successful
// probe with the KAUTH_FILESEC_NOACL sentinel or a zero ACE entry
// count is the clean state, and one or more ACE entries fails the
// creator-only proof.
func darwinTrivialProbe(errno error, aclEntries uint32) error {
	if errno != nil {
		if errors.Is(errno, syscall.ENOENT) {
			return nil
		}
		if errors.Is(errno, syscall.EOPNOTSUPP) {
			return &format.Error{Code: format.CodeDurabilityUnsupported, Detail: "verify absent access ACL: " + errno.Error()}
		}
		return &format.Error{Code: format.CodeIO, Detail: "verify absent access ACL: " + errno.Error()}
	}
	if aclEntries != 0 && aclEntries != aclEntryCountNoACL {
		return accessPolicy()
	}
	return nil
}
