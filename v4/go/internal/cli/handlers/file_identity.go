// File identities for the output-over-source guard, mirroring Rust
// iprange-livedb file_identity: (device, inode) on POSIX and (volume
// serial, 64-bit file index) on Windows through GetFileInformationByHandle.
//
// The identity captured at reader open must survive a later rename of
// the source pathname: os.SameFile (the previous implementation) does
// not on Windows, because it re-opens the recorded stat paths and a
// renamed-away path no longer exists; numeric identities compare
// directly and keep refusing renamed sources and hard links (wave 19
// round 19.13 portability finding).

package handlers

import "github.com/firehol/iprange/v4/go/internal/cli/rpc"

// captureFileIdentity returns the (device, inode) / (volume serial,
// file index) identity of path when it exists (platform
// implementation in file_identity_unix.go / file_identity_windows.go).
func captureFileIdentity(path string) *rpc.FileIdentity {
	dev, ino, ok := captureFileIdentityPlatform(path)
	if !ok {
		return nil
	}
	return &rpc.FileIdentity{Dev: dev, Ino: ino}
}

// sidecarIdentity captures the identity of the reader-coordination
// sidecar (<main>.readers) when it exists (lexically derived like
// outputSidecarPath; Rust sidecar_identity parity).
func sidecarIdentity(path string) *rpc.FileIdentity {
	sidecar, ok := outputSidecarPath(path)
	if !ok {
		return nil
	}
	return captureFileIdentity(sidecar)
}
