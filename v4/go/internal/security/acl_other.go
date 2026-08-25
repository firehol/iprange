//go:build !linux && !darwin && !freebsd && !windows

package security

import (
	"os"
)

// aclSupported reports the creator-only machine availability (false off
// linux, freebsd, and darwin).
const aclSupported = false

// removeInheritedACL on platforms without a pure-Go ACL machine is an
// honest typed refusal. The live surface is already refused earlier on
// these platforms (the windows lock machine), so the security step is
// unreachable in practice; refusing keeps the proof honest for any
// future caller.
func removeInheritedACL(*os.File) error {
	return unsupported("creator-only access policy requires libc ACL APIs unavailable to pure Go on this platform")
}

// requireTrivialACL mirrors removeInheritedACL on these platforms.
func requireTrivialACL(*os.File) error {
	return unsupported("creator-only access policy requires libc ACL APIs unavailable to pure Go on this platform")
}
