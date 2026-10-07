//go:build windows

package recovery

// setUmask is a no-op on windows: mode bits do not exist there and the
// creator-only protection is the DACL (unix twin in umask_unix_test.go).
func setUmask(int) int { return 0 }
