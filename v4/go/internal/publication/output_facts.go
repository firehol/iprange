//go:build !windows

// Portable facts of one output artifact (Rust output.rs facts /
// namespace::local_identity). The facts travel in reservation records
// and public result surfaces; they never disclose the raw path.

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/live"
)

// outputFacts builds the portable private-output facts of one attempt
// (Rust output.rs facts). A nil identity reports the absent identity
// fact.
func outputFacts(d *destination, attemptID [16]byte, name string, identity *live.FileIdentity) PrivateOutputAttempt {
	var local *LocalFileIdentity
	if identity != nil {
		converted := localIdentityFromDeviceInode(live.IdentityDeviceInode(identity))
		local = &converted
	}
	return PrivateOutputAttempt{
		PublicationAttemptID: attemptID,
		DirectoryIdentity:    directoryLocalIdentity(d),
		BasenameEncoding:     basenameEncodingKind,
		Basename:             []byte(name),
		Identity:             local,
		CreationSecurity: CreationSecurity{
			Kind:       creationSecurityKind,
			Commitment: d.securityCommitment(),
		},
	}
}

// directoryLocalIdentity builds the portable identity of the
// destination directory (Rust local(destination.directory().identity())).
func directoryLocalIdentity(d *destination) LocalFileIdentity {
	directoryIdentity := d.directory().Identity()
	return localIdentityFromDeviceInode(live.IdentityDeviceInode(&directoryIdentity))
}
