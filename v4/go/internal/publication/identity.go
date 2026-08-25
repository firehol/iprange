// Portable retained inode identity (Rust validation::
// LocalFileIdentity over publication::namespace::Identity). The type
// authority lives in internal/live where the GC machine runs; the
// publication surface re-exports the constructor for the wire and
// result facts.

package publication

import "github.com/firehol/iprange/v4/go/internal/live"

// LocalFileIdentity is the live portable identity (Rust
// validation::LocalFileIdentity): the identity kind plus the 32-byte
// encoded payload.
type LocalFileIdentity = live.LocalFileIdentity

// LocalFileIdentityFromDeviceInode builds the portable identity of
// one device+inode pair (Rust namespace::local_identity); the
// platform arm selects the identity kind and the byte layout.
func LocalFileIdentityFromDeviceInode(device, inode uint64) LocalFileIdentity {
	return live.LocalFileIdentityFromDeviceInode(device, inode)
}
