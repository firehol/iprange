// Exported conversion helpers for the public SDK facade. The facade
// composes the internal owners and never touches namespace internals;
// these helpers expose the portable projections of retained identities,
// basenames, and housekeeping facts (Rust live_namespace::
// public_identity, live_writer::LocalBasename accessors,
// publication::Housekeeping).

package live

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
