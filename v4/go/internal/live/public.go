// Exported conversion helpers for the public SDK facade. The facade
// composes the internal owners and never touches namespace internals;
// these helpers expose the portable projections of retained identities,
// basenames, and housekeeping facts (Rust live_namespace::
// public_identity, live_writer::LocalBasename accessors,
// publication::Housekeeping).

package live

// CheckSupported reports whether the live coordination primitives are
// proven on this platform (Rust require_live_supported). The snapshot
// and recovery machines call it before budget validation, at the Rust
// api.rs refusal position; the owners repeat it before any path access.
func CheckSupported() error { return requireLiveSupported() }

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
func HousekeepingValue(h housekeeping) uint8 {
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
func HousekeepingFromValue(value uint8) housekeeping {
	return housekeeping(value)
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
