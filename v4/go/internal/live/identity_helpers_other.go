//go:build !linux && !darwin && !freebsd

package live

import (
	"os"
)

// Windows and every remaining platform refuse the retained-identity
// helpers with the namespace-unsupported class: the live sidecar and
// publication surfaces refuse before any path access (Rust compiles
// real windows arms; the Go Windows publication surface is a tracked
// M5 item). The signatures exist so the machine compiles everywhere.

func RegularIdentityAnyLink(_ *os.File, _ FileIdentity) (FileIdentity, error) {
	return FileIdentity{}, nsUnsupportedError()
}

func RegularIdentity(_ *os.File, _ FileIdentity) (FileIdentity, error) {
	return FileIdentity{}, nsUnsupportedError()
}

func RegularLinkCount(_ *os.File) (uint64, error) {
	return 0, nsUnsupportedError()
}
