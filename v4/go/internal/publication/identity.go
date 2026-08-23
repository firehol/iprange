// Portable retained inode identity (Rust validation::
// LocalFileIdentity over publication::namespace::Identity). The
// internal machine keeps device+inode pairs in internal/live; the
// portable identity is the kind tag plus the 32-byte encoded payload
// that travels in reservation records and public result facts.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/format"
)

// identityKind is the retained-unix identity kind (Rust
// publication/namespace/unix.rs IDENTITY_KIND = 1). The reservation
// file is written only by unix peers (Windows refuses publication
// opens), so the Go codec accepts and emits kind 1 only.
const identityKind = 1

// LocalFileIdentity is the portable local identity of one retained
// inode (Rust validation::LocalFileIdentity): the identity kind plus
// the 32-byte encoded payload.
type LocalFileIdentity struct {
	Kind  uint16
	Bytes [32]byte
}

// localIdentityFromDeviceInode builds the portable identity of one
// device+inode pair (Rust namespace::local_identity: kind 1, device
// little-endian at bytes 0..8, inode little-endian at bytes 8..16,
// zero tail).
func localIdentityFromDeviceInode(device, inode uint64) LocalFileIdentity {
	var out LocalFileIdentity
	out.Kind = identityKind
	format.PutU64(out.Bytes[0:8], device)
	format.PutU64(out.Bytes[8:16], inode)
	return out
}

// deviceInode decodes the portable identity to the internal
// device+inode pair (Rust namespace::identity_from_local +
// Identity::decode): the kind must match, the payload must be nonzero,
// and the tail beyond the pair must be zero. ok is false otherwise.
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
