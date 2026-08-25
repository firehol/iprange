package live

// RequireMainAvailable verifies one retained database main is not
// owned by Windows housekeeping (Rust
// live_cleanup::require_main_available): the attempt id is the
// database id, the ordinal is zero, and the authority is the owned
// main file of the main-file directory role. The Windows arm proves
// the exact envelope absence through requireAvailable; every other
// platform keeps the recorded no-op of the Rust non-windows arm.
func RequireMainAvailable(path string, expected FileIdentity, databaseID [16]byte) error {
	return requireAvailable(path, expected, cleanupAuthority{
		attemptID:     databaseID,
		ordinal:       0,
		kind:          ArtifactOwnedMain,
		directoryRole: DirectoryRoleMainFile,
	})
}
