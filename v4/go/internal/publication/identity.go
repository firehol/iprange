// Portable retained inode identity (Rust validation::
// LocalFileIdentity over publication::namespace::Identity). The
// internal machine keeps device+inode pairs in internal/live; the
// portable identity is the kind tag plus the 32-byte encoded payload
// that travels in reservation records and public result facts.

package publication

// LocalFileIdentity is the portable local identity of one retained
// inode (Rust validation::LocalFileIdentity): the identity kind plus
// the 32-byte encoded payload.
type LocalFileIdentity struct {
	Kind  uint16
	Bytes [32]byte
}

// LocalFileIdentityFromDeviceInode builds the portable identity of
// one device+inode pair (Rust namespace::local_identity). The
// platform arm selects the identity kind and the byte layout.
func LocalFileIdentityFromDeviceInode(device, inode uint64) LocalFileIdentity {
	return localIdentityFromDeviceInode(device, inode)
}

// DeviceInode decodes the portable identity to the internal
// device+inode pair (Rust Identity::decode); ok is false otherwise.
func (f LocalFileIdentity) DeviceInode() (device, inode uint64, ok bool) {
	return f.deviceInode()
}
