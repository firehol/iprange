//go:build !windows

package publication

import "github.com/firehol/iprange/v4/go/internal/format"

// identityKind is the retained-unix identity kind (Rust
// publication/namespace/unix.rs IDENTITY_KIND = 1).
const identityKind = 1

// localIdentityFromDeviceInode builds the kind-1 identity: device
// little-endian at bytes 0..8, inode little-endian at bytes 8..16,
// zero tail (Rust namespace::local_identity).
func localIdentityFromDeviceInode(device, inode uint64) LocalFileIdentity {
	var out LocalFileIdentity
	out.Kind = identityKind
	format.PutU64(out.Bytes[0:8], device)
	format.PutU64(out.Bytes[8:16], inode)
	return out
}

// deviceInode decodes the kind-1 identity (Rust
// identity_from_local + Identity::decode): the kind must match, the
// payload must be nonzero, and the tail beyond the pair must be zero.
func (f LocalFileIdentity) deviceInode() (device, inode uint64, ok bool) {
	if f.Kind != identityKind {
		return 0, 0, false
	}
	if f.Bytes == [32]byte{} {
		return 0, 0, false
	}
	for _, b := range f.Bytes[16:] {
		if b != 0 {
			return 0, 0, false
		}
	}
	return format.U64(f.Bytes[0:8]), format.U64(f.Bytes[8:16]), true
}
