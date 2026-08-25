//go:build windows

package live

import "github.com/firehol/iprange/v4/go/internal/format"

// identityKind is the Windows local-identity kind (Rust
// publication/namespace/windows.rs IDENTITY_KIND = 2).
const identityKind = 2

// localIdentityFromDeviceInode builds the kind-2 identity: volume
// little-endian at bytes 0..8, the lower file reference at bytes
// 8..16, zero tail (Rust namespace::local_identity). On NTFS the
// 64-bit file index equals the low half of the 128-bit FILE_ID_INFO
// identifier, so the encoding is byte-identical to the Rust arm.
func localIdentityFromDeviceInode(device, inode uint64) LocalFileIdentity {
	var out LocalFileIdentity
	out.Kind = identityKind
	format.PutU64(out.Bytes[0:8], device)
	format.PutU64(out.Bytes[8:16], inode)
	return out
}

// deviceInode decodes the kind-2 identity: the kind must match, the
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
