//go:build unix

package legacy

// rawBytePin is the identity on a POSIX target: a file name is a byte
// string there, so the unrepresentable byte the fixture writes comes back
// through readdir unchanged and the pinned bytes are the measured bytes.
func rawBytePin(s string) string { return s }

// overCapPin is the identity on a POSIX target: every supported POSIX
// target names the over-long-open failure ENAMETOOLONG and the shared
// errno table renders it with the glibc text the released tool prints, so
// the pinned message half is contractual there and the C oracle confirms
// it byte for byte.
func overCapPin(s string) string { return s }
