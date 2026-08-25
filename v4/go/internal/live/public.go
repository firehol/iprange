// Exported conversion helpers for the public SDK facade. The facade
// composes the internal owners and never touches namespace internals;
// these helpers expose the portable projections of retained identities,
// basenames, and housekeeping facts (Rust live_namespace::
// public_identity, live_writer::LocalBasename accessors,
// publication::Housekeeping).

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/security"
)

// CheckSupported reports whether the live coordination primitives are
// proven on this platform (Rust require_live_supported). The snapshot
// and recovery machines call it before budget validation, at the Rust
// api.rs refusal position; the owners repeat it before any path access.
func CheckSupported() error { return requireLiveSupported() }

// CreationSupported reports whether live database creation works on
// this platform: the live coordination primitives must be proven
// (CheckSupported) and the creator-only security machine must be
// available (internal/security; pure Go implements the linux and
// freebsd machines — the darwin filesec and other-OS libc ACL machines
// are refused honestly). Suite gates and public capability checks use
// this single authority so a refusal cannot drift from the create
// path.
func CreationSupported() error {
	if err := requireLiveSupported(); err != nil {
		return err
	}
	if !security.CreatorOnlySupported() {
		return &format.Error{Code: format.CodeOSUnsupported, Detail: "creator-only access policy requires libc ACL APIs unavailable to pure Go on this platform"}
	}
	return nil
}

// IdentityDeviceInode reports the portable device+inode pair of one
// retained identity (Rust live_namespace::public_identity). A nil
// identity reports the zero pair.
func IdentityDeviceInode(id *FileIdentity) (device uint64, inode uint64) {
	if id == nil {
		return 0, 0
	}
	return publicIdentity(*id)
}

// BasenameParts exposes the portable encoding tag and content bytes of
// one local basename (Rust LocalBasename::encoding and as_bytes).
func BasenameParts(b LocalBasename) (encoding uint16, bytes []byte) {
	return b.encodingValue(), b.bytesValue()
}

// HousekeepingValue exposes the numeric housekeeping fact class (Rust
// publication::Housekeeping discriminant).
func HousekeepingValue(h Housekeeping) uint8 {
	return uint8(h)
}

// IdentityFromDeviceInode builds one retained identity from the
// portable device+inode pair (the inverse of IdentityDeviceInode; the
// public resolver surfaces supply their facts back to the internal
// owners).
func IdentityFromDeviceInode(device, inode uint64) FileIdentity {
	return FileIdentity{device: device, inode: inode}
}

// BasenameFromParts builds one local basename from the portable
// encoding tag and content bytes (the inverse of BasenameParts).
func BasenameFromParts(encoding uint16, bytes []byte) LocalBasename {
	var out LocalBasename
	if len(bytes) > len(out.bytes) {
		bytes = bytes[:len(out.bytes)]
	}
	out.encoding = encoding
	out.length = uint16(len(bytes))
	copy(out.bytes[:], bytes)
	return out
}

// HousekeepingFromValue builds the internal housekeeping fact class
// from its numeric discriminant (the inverse of HousekeepingValue; the
// public resolver entry points supply their facts back to the internal
// owners).
func HousekeepingFromValue(value uint8) Housekeeping {
	return Housekeeping(value)
}

// IsNofollowSymlink reports whether a syscall failure is the no-follow
// final-symlink class (Rust publication::namespace::
// is_nofollow_symlink: ELOOP on unix, plus EMLINK on freebsd). The
// publication problem surface uses it to fold IoAt symlink failures to
// the Conflict class.
func IsNofollowSymlink(err error) bool { return isNofollowSymlink(err) }

// MainLifetimeOffset is the byte-range offset of the artifact lifetime
// lock of one publication artifact (Rust live_sidecar
// MAIN_LIFETIME_LOCK = 1u64 << 44; the sidecar writer lease keeps the
// same constant in sidecar.go). The publication owner locks complete
// main files and private outputs at this offset.
const MainLifetimeOffset = mainLifetimeOffset

// Checkpoint runs one cancellation checkpoint; a nil check never
// cancels (Rust CancellationToken::check; the publication owner passes
// the writer-core cancellation function, internal callers pass nil).
func Checkpoint(check func() error) error {
	if check == nil {
		return nil
	}
	return check()
}

// CanonicalSidecarPath is the exact database pathname plus .readers
// (Rust path::canonical_sidecar). The validation immutable source
// derives the sidecar to refuse before opening the main file.
func CanonicalSidecarPath(main string) (string, error) {
	return canonicalSidecarPath(main)
}

// RequireSidecarAbsent refuses any canonical .readers sidecar entry
// (Rust database_file::require_sidecar_absent; the WrongState class).
func RequireSidecarAbsent(path string) error {
	return requireSidecarAbsent(path)
}

// VerifyPathAnyLink re-checks that path still names the retained
// identity as one regular file, accepting any link count (Rust
// live_namespace::verify_path_any_link; the validation immutable
// source re-verifies the database main under the shared lifetime
// lock).
func VerifyPathAnyLink(path string, expected FileIdentity) error {
	return verifyPathInner(path, expected, false)
}

// CoordinationCause maps one live-coordination failure to the Rust
// recovery coordination class (recovery/source_guard.rs): Cancelled,
// ForkedHandle, and an already-coordination class keep their class;
// every other cause surfaces as LiveRecoveryCoordinationUnavailable.
// The recovery-candidate inspection wraps its sidecar and gate
// failures with this entry.
func CoordinationCause(cause error) error { return liveCoordination(cause) }

// IdentityAnyLink captures the retained identity of one open regular
// file, accepting any link count (Rust live_namespace::
// identity_any_link over retained_regular_identity without the
// single-link rule; the wrong-mode classes fold through
// namespace_error like every live identity capture).
func IdentityAnyLink(f *os.File) (FileIdentity, error) {
	identity, err := regularIdentityAnyLink(f)
	if err != nil {
		return FileIdentity{}, nsMap(err)
	}
	return identity, nil
}

// RequireLiveSupported refuses the live coordination surface on
// platforms without proven coordination before any path access (Rust
// live_lock::require_live_supported; the recover_live api arm refuses
// with the same class before the budget).
func RequireLiveSupported() error {
	return requireLiveSupported()
}
