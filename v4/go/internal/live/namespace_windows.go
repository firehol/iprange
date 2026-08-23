//go:build windows

package live

import (
	"os"
)

// Windows stub of the live namespace: the whole live surface is a
// tracked M5 item and every namespace operation refuses before any path
// access (lock_refuse.go refuses the byte-range locks the same way, and
// mapping.MapFile refuses its Windows mapping). The fileIdentity shape
// stays identical so the portable slot/cleanup/header code compiles
// against it; no Windows code path reaches these functions before M5.
type fileIdentity struct {
	info os.FileInfo
}

func identityOf(*os.File) (fileIdentity, error) {
	return fileIdentity{}, liveRefusal()
}

func verifyPath(string, fileIdentity) error {
	return liveRefusal()
}

func openRw(string) (*os.File, fileIdentity, error) {
	return nil, fileIdentity{}, liveRefusal()
}

func createPrivate(string, cleanupAuthority) (*os.File, fileIdentity, error) {
	return nil, fileIdentity{}, liveRefusal()
}

func removeExact(string, fileIdentity) cleanupOutcome {
	return cleanupOutcomeFailed(liveRefusal())
}

func syncParent(string) error {
	return liveRefusal()
}
