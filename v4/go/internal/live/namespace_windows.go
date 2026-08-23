//go:build windows

package live

import "os"

// Windows stub of the live namespace: the whole live surface is a
// tracked M5 item and every namespace operation refuses before any path
// access (lock_refuse.go refuses the byte-range locks the same way, and
// mapping.MapFile refuses its Windows mapping). The FileIdentity shape
// stays identical so the portable slot/cleanup/header code compiles
// against it; no Windows code path reaches these functions before M5.
type FileIdentity struct {
	device uint64
	inode  uint64
}

func identityOf(*os.File) (FileIdentity, error) {
	return FileIdentity{}, liveRefusal()
}

func verifyPath(string, FileIdentity) error {
	return liveRefusal()
}

func openRw(string) (*os.File, FileIdentity, error) {
	return nil, FileIdentity{}, liveRefusal()
}

func createPrivate(string, cleanupAuthority) (createdPrivate, *privateCreationFailure) {
	return createdPrivate{}, &privateCreationFailure{cause: liveRefusal()}
}

func removeExact(string, FileIdentity) cleanupOutcome {
	return cleanupOutcomeFailed(liveRefusal())
}

func syncParent(string) error {
	return liveRefusal()
}

// parentIdentity refuses before path access on Windows.
func parentIdentity(string) (FileIdentity, error) {
	return FileIdentity{}, liveRefusal()
}

// pathIdentity refuses before path access on Windows.
func pathIdentity(string) (*FileIdentity, error) {
	return nil, liveRefusal()
}

// publicIdentity projects nothing on Windows: the live surface refuses
// before any identity is captured.
func publicIdentity(FileIdentity) (device uint64, inode uint64) {
	return 0, 0
}
